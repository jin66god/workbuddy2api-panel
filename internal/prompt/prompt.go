// Package prompt 提供网关自有系统提示词：内置默认 + 文件覆盖 + 降级中性提示词。
//
// 背景：客户端（Claude Code/Codex 等 CLI）在 system prompt 注入固定模板句，
// 上游内容审核按逐字精确匹配误杀合法流量（issue #36/PR39 的 11128）。
// 方案：网关在出站前用自有系统提示词替换客户端 system/developer 消息，
// 从源头消灭 system 来源的指纹误报（用户/assistant 消息里的指纹串仍由
// internal/upstream/sanitize.go 清洗，两层叠加、互不替代）。
package prompt

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed defaultprompt.md
var defaultPrompt string

// Degraded 降级提示词：误报处理用，刻意极简中性。
//
// 触发场景：passthrough 模式下请求被上游内容策略拦截（HTTP 400 + 审核文案），
// 判定为指纹误报后换最小中性提示词重试一次。非对抗框架——只用于绕开
// system 来源的误报，不改变用户指令的合法性语义。
const Degraded = "You are a helpful assistant. Respond in the user's language, follow the user's instructions, and be direct and concise."

// DefaultHarnessIdentity 是身份首句替换功能的默认值，开关缺省关闭。
const DefaultHarnessIdentity = "You are CodeBuddy Code."

// Load 按 mode 与 file 加载系统提示词文本。
//   - file 非空 → 读文件（不存在/读失败返回 error，调用方 fail fast）；
//   - file 空 → 返回内置 defaultPrompt。
//
// mode 在此仅做透传记录（实际 custom/passthrough 路由由调用方决定），
// Load 只负责"拿到一段提示词文本"，不关心路由语义。
func Load(mode, file string) (string, error) {
	if file == "" {
		return defaultPrompt, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("prompt file %s: %w", file, err)
	}
	return string(raw), nil
}

// Rewrite 解析 OpenAI 请求体并替换系统提示词：
//   - 删除 messages 中所有 role 为 system/developer 的消息；
//   - 在 messages 头部插入一条 {"role":"system","content":systemPrompt}；
//   - 其余字段与 user/assistant/tool 消息逐字不动。
//
// 解析失败 → 原样返回（绝不失败）：Rewrite 是出站改写的关键路径，
// 任何解析错误都不应阻塞请求转发，让上游按其原始语义处理。
func Rewrite(body []byte, systemPrompt string) []byte {
	if len(body) == 0 || systemPrompt == "" {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	msgs, ok := obj["messages"].([]any)
	if !ok {
		// 无 messages 字段或类型不符 → 插入单条 system 后原样保留其余字段。
		obj["messages"] = []any{map[string]any{"role": "system", "content": systemPrompt}}
		if out, err := json.Marshal(obj); err == nil {
			return out
		}
		return body
	}
	// 过滤掉所有 system/developer 消息，保留 user/assistant/tool 及其他角色。
	kept := make([]any, 0, len(msgs)+1)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			kept = append(kept, m)
			continue
		}
		role, _ := mm["role"].(string)
		if role == "system" || role == "developer" {
			continue
		}
		kept = append(kept, m)
	}
	// 头部插入单条 system 消息（prepend 避免整体重排语义）。
	rewritten := append(
		[]any{map[string]any{"role": "system", "content": systemPrompt}},
		kept...,
	)
	obj["messages"] = rewritten
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// Append 解析 OpenAI 请求体并在"开头连续 system/developer 块"之后插入一条
// 网关自有 system 提示词（issue #129 append 模式）：
//   - 开头连续块 = 从 messages[0] 起向后 role 为 system/developer（精确字符串
//     匹配，与 Rewrite 删除口径一致）的消息；遇第一条非 system/developer
//     消息（含非 map 消息、无 role 消息）即停；
//   - 插入点 = 连续块末尾之后（块长 0 时即 messages 最前）；
//   - 所有既有消息（含开头块、中途 system、user/assistant/tool）逐字不动
//     ——客户端项目规范/工具约定与网关提示词并用。
//
// 守卫与 Rewrite 逐条一致：空 body / 空 systemPrompt / 坏 JSON → 原样返回
// （绝不失败）；无 messages 字段 → messages=[网关 system]，其余字段原样。
//
// 边界判定须显式同时匹配 system 与 developer：归一（developer→system）在
// 下游 prepareBody 的 normalizeRoles，Append 执行时开头块里的 developer
// 还是 developer。网关消息角色用 system 而非 developer——上游 role 白名单
// 不含 developer，插 developer 等于制造一次必然归一与多余的 11128 风险窗口。
func Append(body []byte, systemPrompt string) []byte {
	if len(body) == 0 || systemPrompt == "" {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	msgs, ok := obj["messages"].([]any)
	if !ok {
		// 无 messages 字段或类型不符 → 插入单条 system 后原样保留其余字段。
		obj["messages"] = []any{map[string]any{"role": "system", "content": systemPrompt}}
		if out, err := json.Marshal(obj); err == nil {
			return out
		}
		return body
	}
	// 扫描开头连续 system/developer 块，遇第一条非 system/developer 即停。
	insertAt := 0
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			break
		}
		role, _ := mm["role"].(string)
		if role != "system" && role != "developer" {
			break
		}
		insertAt++
	}
	gw := map[string]any{"role": "system", "content": systemPrompt}
	// 已有消息逐字不动：只在插入点拼接，不重排、不改写任何元素。
	rewritten := make([]any, 0, len(msgs)+1)
	rewritten = append(rewritten, msgs[:insertAt]...)
	rewritten = append(rewritten, gw)
	rewritten = append(rewritten, msgs[insertAt:]...)
	obj["messages"] = rewritten
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// OverrideHarnessIdentity 仅在开头连续的 system/developer 消息中，寻找以
// "You are " 开头的第一句身份声明并替换；其余提示词内容、消息顺序及其他字段保留。
// 仅供 passthrough/append 模式按需调用；无命中时原样返回（不重新序列化）。
func OverrideHarnessIdentity(body []byte, replacement string) []byte {
	if len(body) == 0 {
		return body
	}
	if strings.TrimSpace(replacement) == "" {
		replacement = DefaultHarnessIdentity
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	msgs, ok := obj["messages"].([]any)
	if !ok {
		return body
	}
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			break
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			break // 不触碰对话中段及 user/assistant/tool 的内容
		}

		switch content := msg["content"].(type) {
		case string:
			updated, changed := replaceHarnessIdentitySentence(content, replacement)
			if !changed {
				continue
			}
			msg["content"] = updated
		case []any:
			// OpenAI 多模态形式：只检查首个文本块，不改动其他块。
			changed := false
			for _, rawPart := range content {
				part, ok := rawPart.(map[string]any)
				if !ok || part["type"] != "text" {
					break
				}
				text, ok := part["text"].(string)
				if !ok {
					break
				}
				updated, didChange := replaceHarnessIdentitySentence(text, replacement)
				if didChange {
					part["text"] = updated
					changed = true
				}
				break
			}
			if !changed {
				continue
			}
		default:
			continue
		}

		out, err := json.Marshal(obj)
		if err != nil {
			return body
		}
		return out // 仅替换找到的第一处声明
	}
	return body
}

// replaceHarnessIdentitySentence 识别英文 harness 常见的 "You are ..." 首句。
// 以句末标点（后接空白/结尾）或换行作为边界；不把 v4.1 等词内句点当句尾。
// 保留前导空白、句尾之后的所有文本及原有换行。非身份首句不动。
func replaceHarnessIdentitySentence(text, replacement string) (string, bool) {
	start := len(text) - len(strings.TrimLeft(text, " \t\r\n\ufeff"))
	const prefix = "You are "
	if len(text)-start < len(prefix) || !strings.EqualFold(text[start:start+len(prefix)], prefix) {
		return text, false
	}
	end := len(text)
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '\r', '\n':
			end = i
			goto replace
		case '.', '!', '?':
			if i+1 == len(text) || strings.ContainsRune(" \t\r\n", rune(text[i+1])) {
				end = i + 1
				goto replace
			}
		}
	}
replace:
	if text[start:end] == replacement {
		return text, false
	}
	return text[:start] + replacement + text[end:], true
}

// EnsureSystemFirst 保证 messages[0].role == "system" —— 上游渠道校验的硬要求.
//
// 2026-10-20 真机实测（国际版）：同一 token、同一 body，只改首条角色：
//
//	user      → 400 code=11-128 "first message is not system prompt"
//	developer → 400 code=11-128 "Illegal API invocation from an unapproved channel"
//	system    → 200（模型正常回包）
//
// passthrough 模式原样透传客户端 body（多数 OpenAI 兼容客户端首条是 user），
// 于是每次请求都撞 11-128，账号被反复记成「渠道校验拦截」→ 全池冷却，表象即
// issue #231「国际版账号全部被禁用，但网页还能聊」。这不是账号问题、也不是内容
// 策略问题：**首条角色本身**属于渠道校验的一环。
//
// 处置：
//   - 首条 developer：就地把 role 改成 system（OpenAI 新规范里它是 system 的别名，
//     内容逐字保留，不删不并）；
//   - 首条 user/assistant/tool：在最前补一条 gateway system，其余消息逐字不动；
//   - 已是 system、messages 缺失/为空、非法 JSON：原样返回（不凭空造对话）。
//
// fallback 为补入的 system 内容；空则用 Degraded（与降级重试同一中性提示词）。
//
// 与 upstream.normalizeRoles 的分工：本函数在 handler 入口按**首条**兜底、覆盖
// 全部 realm；normalizeRoles 在 prepareBody 里把所有位置的 developer 归一，且
// 对不经过 handler 的直接调用方（面板任务/夜猫子）仍然生效。两层都有意保留。
func EnsureSystemFirst(body []byte, fallback string) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		return body
	}
	role, _ := first["role"].(string)
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return body // 已是官方形态，零改动。
	case "developer":
		// 首条 developer 同样被拒（报的是另一句 unapproved channel）。就地改成
		// system，内容逐字保留——不删不并，避免改变客户端语义。
		first["role"] = "system"
		if out, err := json.Marshal(obj); err == nil {
			return out
		}
		return body
	}
	systemPrompt := fallback
	if systemPrompt == "" {
		systemPrompt = Degraded
	}
	if systemPrompt == "" {
		return body
	}
	rewritten := make([]any, 0, len(msgs)+1)
	rewritten = append(rewritten, map[string]any{"role": "system", "content": systemPrompt})
	rewritten = append(rewritten, msgs...)
	obj["messages"] = rewritten
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
