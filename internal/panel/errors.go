// errors.go 错误观测：把日志环形缓冲里的 WARN/ERR 行按「账号 × 错误类型」聚合。
//
// 为什么不在前端算：原始日志里几十行同质 WARN，人眼抓不到真正重要的差异
// （某个号报错 44 次、另一个只 1 次）。按 (账号, 类型) 聚合成十几行才读得动，
// 且聚合口径必须在服务端统一，面板轮询才不必每次重算全量文本。
//
// 数据源：面板日志环（Ring）——不新增存储。Ring 的 Text 已剥掉行首时间戳，
// 时刻取 LogEntry.TS，不从文本里解析。
package panel

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
)

// ErrStat 单条「账号 × 错误类型」聚合行。
type ErrStat struct {
	Account    string `json:"account"`               // 打印用账号标签（昵称(uid8)）；空 = 非账号级
	Kind       string `json:"kind"`                  // 人话类型
	Code       string `json:"code,omitempty"`        // 上游业务码（6004/11102/12153…）
	Model      string `json:"model,omitempty"`       // 相关模型
	Count      int    `json:"count"`                 // 累计次数
	Last       string `json:"last,omitempty"`        // 最近一次时刻 HH:MM:SS
	Sev        string `json:"sev"`                   // bad | warn | info
	Sample     string `json:"sample,omitempty"`      // 表格正文样本（截断）
	SampleFull string `json:"sample_full,omitempty"` // 悬停全文（放宽上限，够装上游 body）
}

const (
	sampleBytes     = 160
	sampleFullBytes = 1024
	sevBad          = "bad"  // 需处理：限流/额度/会话失效/WAF/传输失败
	sevWarn         = "warn" // 需留意：内容拦截、解析失败、目录拉取
	sevInfo         = "info" // 正常但值得知道：客户端中断、轮转退避
)

var (
	reUpstream = regexp.MustCompile(`upstream (\d{3}) ([a-z_]+)`) // WARN: [upstream] chat_stream acct=X: upstream 429 soft_rate body=…
	reAcct     = regexp.MustCompile(`acct=([^\s:]+)`)             // acct=昵称(a1b2c3d4)
	reModel    = regexp.MustCompile(`model=([^\s:,]+)`)
	reCode     = regexp.MustCompile(`"code"\s*:\s*"?(\d+)"?`)
	// 调度族失败行：`WARN: keepalive 昵称(uid8): <err>`——账号紧跟任务名，没有 acct=。
	reTask = regexp.MustCompile(`^(?:WARN: |ERR: )?(checkin|keepalive|balance|travel|activity|user-resource|streak-bonus|blackcat|lottery) ([^:]+):`)
)

var taskKinds = map[string]string{
	"checkin":       "签到失败",
	"keepalive":     "保活失败",
	"balance":       "余额查询失败",
	"travel":        "猫猫旅行失败",
	"activity":      "活跃上报失败",
	"user-resource": "账号资源查询失败",
	"streak-bonus":  "连登兑换/抽奖失败",
	"blackcat":      "夜猫子任务失败",
	"lottery":       "抽奖失败",
}

// kindOf 把一行归类为「人话类型 + 上游码 + 严重度」。空类型 = 不参与聚合。
// 不直接用上游原文当类型：原文带 requestId/时间戳，同一类错误会被拆成 N 条。
func kindOf(line string) (kind, code, sev string) {
	// 客户端掐断不是网关也不是上游故障，但文案多样（transport/parse/续写/轮转），
	// 先归 info，否则噪声会把真故障挤出首屏。
	if strings.Contains(line, "context canceled") || strings.Contains(line, "ctx cancelled") ||
		strings.Contains(line, "context cancelled") {
		return "客户端中断", "", sevInfo
	}
	if m := reUpstream.FindStringSubmatch(line); m != nil {
		code = m[1]
		switch m[2] {
		case "soft_rate", "hard_credit":
			kind, sev = "限流/额度", sevBad
		case "session_dead":
			kind, sev = "会话失效", sevBad
		case "waf_block":
			kind, sev = "WAF 拦截", sevBad
		case "server":
			kind, sev = "上游 5xx", sevBad
		case "account_fault":
			kind, sev = "账号授权/配额故障", sevBad
		case "model_blocked":
			kind, sev = "模型不可用", sevBad
		case "content_blocked":
			kind, sev = "内容拦截", sevWarn
		case "not_found":
			kind, sev = "上游 404", sevWarn
		case "bad_params", "client", "model_param_invalid", "prompt_too_long", "image_invalid":
			kind, sev = "请求参数错误", sevInfo
		default:
			kind, sev = "上游 "+m[2], sevWarn
		}
		return kind, code, sev
	}
	switch {
	case strings.Contains(line, "transport error"):
		kind, sev = "传输失败", sevBad
	case strings.Contains(line, "upstream timeout"):
		kind, sev = "上游超时（未罚号）", sevBad
	case strings.Contains(line, "waf ip-level block"):
		kind, sev = "WAF IP 级拦截", sevBad
	case strings.Contains(line, "empty upstream stream"):
		kind, sev = "上游空流", sevWarn
	case strings.Contains(line, "cache miss"):
		kind, sev = "前缀缓存未命中", sevInfo
	case strings.Contains(line, "账号被禁用"):
		kind, sev = "账号被禁用", sevBad
	case strings.Contains(line, "熔断触发"):
		kind, sev = "熔断出池", sevWarn
	case strings.Contains(line, "连败降权"):
		kind, sev = "连败降权", sevWarn
	}
	if kind == "" {
		if m := reTask.FindStringSubmatch(line); m != nil {
			if k, ok := taskKinds[m[1]]; ok {
				return k, "", sevWarn
			}
		}
	}
	return kind, "", sev
}

// errorSummary GET /panel/api/error_summary：聚合 + 按严重度/次数排序。
func (p *Panel) errorSummary(w http.ResponseWriter, r *http.Request) {
	agg := map[string]*ErrStat{}
	for _, e := range p.logs.Snapshot() {
		t := e.Text
		if !strings.Contains(t, "WARN") && !strings.Contains(t, "ERR") && !strings.HasPrefix(t, "ERR") {
			continue
		}
		kind, code, sev := kindOf(t)
		if kind == "" {
			continue
		}
		acct := ""
		if m := reAcct.FindStringSubmatch(t); m != nil {
			acct = m[1]
		} else if m := reTask.FindStringSubmatch(t); m != nil {
			acct = strings.TrimSpace(m[2])
		}
		if code == "" {
			if m := reCode.FindStringSubmatch(t); m != nil {
				code = m[1]
			}
		}
		model := ""
		if m := reModel.FindStringSubmatch(t); m != nil {
			model = m[1]
		}
		key := acct + "|" + kind + "|" + code
		st, ok := agg[key]
		if !ok {
			st = &ErrStat{Account: acct, Kind: kind, Code: code, Model: model, Sev: sev,
				Sample: truncBytes(t, sampleBytes), SampleFull: truncBytes(t, sampleFullBytes)}
			agg[key] = st
		}
		st.Count++
		st.Last = e.TS.Format("15:04:05")
		if model != "" {
			st.Model = model
		}
	}
	out := make([]ErrStat, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	rank := map[string]int{sevBad: 0, sevWarn: 1, sevInfo: 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Sev] != rank[out[j].Sev] {
			return rank[out[i].Sev] < rank[out[j].Sev]
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Last > out[j].Last
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rows": out, "total": len(out)})
}

// truncBytes 按字节截断并保证不切碎 UTF-8（日志正文含中文与 emoji）。
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

var _ = logfmt.Label
