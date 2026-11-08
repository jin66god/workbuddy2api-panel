// output_budget.go 输出预算下限：客户端挡不住「高档位思考把自己那份额度吃光」的
// 组合错误，网关兜住它。
//
// 现象（上游 2026-11-08 生产实测，cn:deepseek-v4.1-flash，要求写 3000+ 字长文档）：
//
//	effort=high + max_tokens=2048 → total=4096 think=2048 正文=2048 finish=length
//	effort=max  + max_tokens=2048 → total=4096 think=2048 正文=2048 finish=length
//	effort=high + 不给额度        → total=16720 think=2998 正文=13722 finish=stop
//	effort=max  + 不给额度        → total=48816 think=37110 正文=11706 finish=stop
//
// 即：思考与正文在**同一份额度**里结算时，客户端给的 2048 先被思考吃掉一半，正文只剩
// 一半就断在句子中间——用户看到的就是「用着用着中断」。客户端并不知道模型的思考要花
// 多少，它只能给一个拍脑袋的数，这个组合错误只能由网关修。
//
// 抬的是**上限**不是目标长度：模型写完自然停（实测 high + 大额度收尾在 13722 token，
// 不会因为上限变大而多写一个字）。所以这既不改变答案长度，也不额外计费，只是把「预算
// 被思考偷走」这个 bug 修掉。
//
// 本库没有上游的 continue.go / starved.go 续写层——截断一旦发生就是终态，客户端只能
// 手动重发。所以这一层（出站前兜住）在本库是唯一防线，不是「三者中最早的一层」。
package upstream

import (
	"log"
	"strings"
)

// minBudgetByEffort 各思考档位下「正文还能写得出来」的最小输出预算。
//
// 取值依据（上游生产实测 think 用量，同一模型 cn:deepseek-v4.1-flash）：
//
//	low  → think 1853~2722
//	high → think 2048~4068
//	max  → think 7612~37110（同一个提示词能从 7.6k 波动到 37k）
//
// 下限按「该档思考的上界」再留一倍给正文，所以是 4k/8k/16k：低于它，思考必然先把预算
// 吃掉一半以上；高于它，思考与正文都够用，模型自然收尾。
var minBudgetByEffort = map[string]int64{
	"minimal": 4096,
	"low":     4096,
	"medium":  6144,
	"high":    8192,
	"xhigh":   12288,
	"max":     16384,
}

// minOutputBudgetProbe 低于这个额度的请求一律不动：官方客户端用 max_tokens=1 做 key
// 探活（"Reply with OK."），抬上去没有意义，还会让探活变慢。与 GPT 系 16 的探活下限同口径。
const minOutputBudgetProbe = 16

// MinOutputBudget 返回某档位的输出预算下限（未知档位 / 关闭思考 → 0 = 不设下限）。
// 供面板、测试与将来可能引入的续写层复用，避免在多处重复维护这张表。
func MinOutputBudget(effort string) int64 {
	return minBudgetByEffort[strings.ToLower(strings.TrimSpace(effort))]
}

// ensureMinOutputBudget 在「客户端显式给了 max_tokens」且「低于该档下限」时抬到下限。
//
// 不动的四种情形（都不猜）：
//   - 没有显式额度：上游按目录默认上限走，本来就不会被思考挤掉；
//   - 额度是探活级（< 16）；
//   - 档位为空或关闭思考（thinking.type=disabled / effort=off）：没有思考要花钱；
//   - 已经不低于下限。
//
// modelCap 是目录里该模型的 maxOutputTokens（0/负数 = 未知）：已知且比下限小时以目录为准
// ——不把额度抬到模型物理上写不了的值。
func ensureMinOutputBudget(obj map[string]any, effort string, modelCap int64) {
	v, ok := explicitOutputBudget(obj)
	if !ok || v <= 0 || v < minOutputBudgetProbe {
		return
	}
	floor, has := minBudgetByEffort[strings.ToLower(strings.TrimSpace(effort))]
	if !has {
		return
	}
	if thinkingDisabled(obj) {
		return
	}
	if modelCap > 0 && floor > modelCap {
		floor = modelCap
	}
	if v >= floor {
		return
	}
	obj["max_tokens"] = floor
	log.Printf("INFO: [budget] max_tokens %d -> %d（effort=%s 的输出预算下限：低于它时思考会先吃掉一半以上，正文被截断在句子中间；抬的是上限不是目标长度）",
		v, floor, effort)
}

// effectiveEffort 读出这份出站 body 里实际生效的思考档位（snake 优先、camel 兜底）。
// 必须在 injectThinking + normalizeReasoningEffort 之后调用：此时写进 body 的值已经是
// 降级/补档后的真实档位，与上游实际执行的档位一致。
func effectiveEffort(obj map[string]any) string {
	if v, ok := obj["reasoning_effort"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := obj["reasoningEffort"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// explicitOutputBudget 读客户端显式给的输出额度。translateMaxCompletionTokens 已把别名
// 折进 max_tokens，这里仍按三个键判「有没有显式给」——显式给极小值才是要修的组合错误。
func explicitOutputBudget(obj map[string]any) (int64, bool) {
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		switch v := obj[k].(type) {
		case float64:
			return int64(v), true
		case int:
			return int64(v), true
		case int64:
			return v, true
		case string:
			// 个别客户端把额度写成字符串；解析失败按「没给」处理。
			if n := parseInt64(v); n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// thinkingDisabled 报告请求是否显式关闭了思考（两种写法：thinking.type=disabled 与
// 档位字段取 off/none/disabled）。关闭后额度全给正文，没有「思考偷预算」这回事。
func thinkingDisabled(obj map[string]any) bool {
	if th, ok := obj["thinking"].(map[string]any); ok {
		if typ, ok := th["type"].(string); ok && strings.EqualFold(strings.TrimSpace(typ), "disabled") {
			return true
		}
	}
	return effortValueOff(obj)
}

// parseInt64 解析十进制整数字符串（额度字段的畸形形态兜底）；失败返回 0。
func parseInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
		if n > 1<<40 {
			return 0
		}
	}
	return n
}
