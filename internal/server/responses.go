// responses.go OpenAI Responses 兼容端点 POST /v1/responses。
//
// 存在理由：Codex CLI 一类客户端只走 Responses 协议（`/v1/responses` + `input[]`
// 条目 + 事件流），本库此前只有 `/v1/chat/completions`，这类客户端必须在前置一层
// new-api 做 responses→chat 转换才能用。那一跳既多一个故障面，又要靠 new-api 的
// 转换器兜字段形状——本库的 payload 归一（image_url 字符串形态、reasoning_text
// 片段、max_completion_tokens 别名）本来就是在为那个转换器的产物擦屁股。
//
// 设计取舍：**不做协议实现，做协议适配**。
//   - 入站：Responses 请求 → chat 请求体（`responsesToChat`），然后原样交给
//     `chatCompletions`。账号池、粘性会话、轮转、冷却、计费、请求日志、前缀缓存
//     注入全部复用同一条链路——适配器不复制任何调度逻辑，也就不会与主链路漂移。
//   - 出站：把 chat 处理器写出的 SSE（`upstream.StreamHint` 的规范帧）翻译成
//     Responses 事件序列（`responsesEmitter`）。非流式请求内部同样强制流式上游，
//     再由 emit 器聚合成一个 `response` 对象——上游本来也只支持流式。
//
// 有状态接口（`previous_response_id` / `store=true`）本期不支持：Codex 默认
// `store=false` 且每轮重发全量 input，无状态即可满足。收到 `previous_response_id`
// 时明确回 400，而不是静默丢掉历史——静默丢历史会让模型"失忆"且客户端毫不知情。
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// responsesPath 是 Responses 端点的规范路径。抽成常量是因为它同时被三处引用：
// 路由注册、请求日志记账（ServeHTTP 的 tracedRequest 白名单）、以及适配器内部
// 判断是否要继续沿用入站路径——任何一处写错都表现为「面板里看不到这类流量」。
const responsesPath = "/v1/responses"

// ───────────────────────────── 请求侧：Responses → Chat ─────────────────────────────

// responsesRequest 是 Responses 请求里本适配器关心的字段集合。
// 未列出的字段（include / truncation / user / prompt_cache_key 等）按"无副作用忽略"
// 处理：它们要么只影响客户端侧的渲染，要么上游不支持，透传只会引发 11101。
type responsesRequest struct {
	Model             string            `json:"model"`
	Instructions      json.RawMessage   `json:"instructions"`
	Input             json.RawMessage   `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	MaxOutputTokens   *int              `json:"max_output_tokens"`
	Reasoning         *struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	Text *struct {
		Format json.RawMessage `json:"format"`
	} `json:"text"`
	Stream             bool           `json:"stream"`
	Temperature        *float64       `json:"temperature"`
	TopP               *float64       `json:"top_p"`
	Store              *bool          `json:"store"`
	PreviousResponseID string         `json:"previous_response_id"`
	Metadata           map[string]any `json:"metadata"`
}

// responsesPlan 是"原样回显给客户端"的请求侧快照：响应对象里要出现 instructions /
// max_output_tokens / tools / tool_choice 等字段，客户端（Codex）据此记账与重放。
// 不复用 responsesRequest 是因为其中若干字段已被改写（tools 形态、tool_choice 形态），
// 回显必须是客户端发来的原始形态。
type responsesPlan struct {
	model           string
	stream          bool
	instructions    string
	maxOutputTokens *int
	toolsEcho       []any
	toolChoiceEcho  any
	effort          string
	store           bool
	// parallelToolCalls 回显客户端的取值；nil = 客户端没声明（按上游语义给 true）。
	parallelToolCalls *bool
	temperature       *float64
	topP              *float64
	textFormat        any
	metadata          map[string]any
	// previousResponseID 仅用于错误文案（收到即 400，不进入后续链路）。
	previousResponseID string
}

// errResponsesUnsupported 表示请求形态合法但本适配器有意不支持（回 400，可读文案）。
var errResponsesUnsupported = errors.New("unsupported")

// responsesToChat 把 Responses 请求体翻译成 chat 请求体。
// 返回的 plan 供响应侧回显；任何解析失败都返回错误，调用方回 400 invalid_request。
func responsesToChat(body []byte) ([]byte, *responsesPlan, error) {
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse responses request: %w", err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, nil, errors.New("model is required")
	}
	if req.PreviousResponseID != "" {
		return nil, nil, fmt.Errorf("%w: previous_response_id is not supported by this gateway (stateless adapter); resend the full input array with store=false", errResponsesUnsupported)
	}
	plan := &responsesPlan{
		model:              req.Model,
		stream:             req.Stream,
		maxOutputTokens:    req.MaxOutputTokens,
		toolChoiceEcho:     "auto",
		store:              req.Store != nil && *req.Store,
		temperature:        req.Temperature,
		topP:               req.TopP,
		metadata:           req.Metadata,
		previousResponseID: req.PreviousResponseID,
	}
	plan.parallelToolCalls = req.ParallelToolCalls
	if req.Reasoning != nil {
		plan.effort = strings.TrimSpace(req.Reasoning.Effort)
	}

	messages, err := responsesMessages(req)
	if err != nil {
		return nil, nil, err
	}
	if len(messages) == 0 {
		return nil, nil, errors.New("input is required")
	}

	chat := map[string]any{
		"model":    req.Model,
		"messages": messages,
		// 上游只认流式（payload.PrepareBody 亦强制 stream:true）。这里显式写死，
		// 让下游 handler 走流式分支——非流式客户端由 emit 器在网关侧聚合。
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(messages) > 0 {
		if inst := strings.TrimSpace(instructionsText(req.Instructions)); inst != "" {
			plan.instructions = inst
		}
	}
	if req.MaxOutputTokens != nil {
		// 直接写 max_tokens：payload 层对 max_completion_tokens 的翻译是给外部客户端
		// 兜底的，适配器自己不需要多绕一跳。
		chat["max_tokens"] = *req.MaxOutputTokens
	}
	if plan.effort != "" {
		chat["reasoning_effort"] = plan.effort
	}
	if req.Temperature != nil {
		chat["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chat["top_p"] = *req.TopP
	}
	if tc := responsesToolChoice(req.ToolChoice); tc != nil {
		chat["tool_choice"] = tc
		plan.toolChoiceEcho = responsesToolChoiceEcho(req.ToolChoice)
	}
	if tools, echo, err := responsesTools(req.Tools); err != nil {
		return nil, nil, err
	} else if len(tools) > 0 {
		chat["tools"] = tools
		plan.toolsEcho = echo
	}
	if rf := responsesResponseFormat(req.Text); rf != nil {
		chat["response_format"] = rf
	}
	// 回显必须是 Responses 原形态：plan 里存 chat 形态会让 response.text.format
	// 多出一层 json_schema，客户端按自己发过的结构比对时会认为配置被改写。
	plan.textFormat = responsesFormatEcho(req.Text)
	out, err := json.Marshal(chat)
	if err != nil {
		return nil, nil, fmt.Errorf("encode chat request: %w", err)
	}
	return out, plan, nil
}

// instructionsText 取 instructions（规范是字符串，个别客户端发数组）。
func instructionsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// responsesMessages 把 instructions + input 翻译成 chat messages。
//
// input 三种形态都认：纯字符串、单个 item 对象、item 数组。item 里本适配器处理
// message / function_call / function_call_output，reasoning 与 item_reference 直接
// 跳过（思考片段不参与续写；item_reference 依赖 previous_response_id，已在入口拒绝）。
func responsesMessages(req responsesRequest) ([]any, error) {
	var items []map[string]any
	raw := bytes.TrimSpace(req.Input)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return nil, nil
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parse input string: %w", err)
		}
		items = []map[string]any{{"type": "message", "role": "user", "content": s}}
	case raw[0] == '{':
		var one map[string]any
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("parse input item: %w", err)
		}
		items = []map[string]any{one}
	default:
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("parse input items: %w", err)
		}
	}

	out := make([]any, 0, len(items)+1)
	if inst := strings.TrimSpace(instructionsText(req.Instructions)); inst != "" {
		out = append(out, map[string]any{"role": "system", "content": inst})
	}
	// 并行工具调用在 Responses 里是"连续多个 function_call，再连续多个输出"。
	// chat 协议要求 tool 消息**紧跟**它所属的 assistant 消息，所以必须把连续的
	// function_call 合并进同一条 assistant.tool_calls，否则第二个 tool 消息会因
	// "找不到对应的 tool_call_id" 被上游判 400。
	var pendingCalls []any
	flushCalls := func() {
		if len(pendingCalls) == 0 {
			return
		}
		out = append(out, map[string]any{"role": "assistant", "tool_calls": pendingCalls})
		pendingCalls = nil
	}

	for _, it := range items {
		typ, _ := it["type"].(string)
		switch typ {
		case "", "message":
			flushCalls()
			role, _ := it["role"].(string)
			if role == "" {
				role = "user"
			}
			if role == "developer" {
				role = "system"
			}
			content, err := responsesContent(it["content"])
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"role": role, "content": content})
		case "function_call":
			name, _ := it["name"].(string)
			args, _ := it["arguments"].(string)
			callID, _ := it["call_id"].(string)
			if callID == "" {
				callID, _ = it["id"].(string)
			}
			if callID == "" {
				callID = responsesID("call_")
			}
			if name == "" {
				return nil, errors.New("function_call item is missing name")
			}
			pendingCalls = append(pendingCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
		case "function_call_output":
			flushCalls()
			callID, _ := it["call_id"].(string)
			if callID == "" {
				return nil, errors.New("function_call_output item is missing call_id")
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      responsesToolOutput(it["output"]),
			})
		case "reasoning", "item_reference":
			// reasoning：思考片段不进入上下文（上游会把它当 reasoning_text 片段 400）。
			// item_reference：依赖 previous_response_id，入口已拒绝，这里不会被命中。
			continue
		default:
			return nil, fmt.Errorf("%w: input item type %q is not supported", errResponsesUnsupported, typ)
		}
	}
	flushCalls()
	return out, nil
}

// responsesContent 把 Responses 的 content（字符串或 part 数组）翻成 chat content。
func responsesContent(v any) (any, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	parts, ok := v.([]any)
	if !ok {
		return nil, errors.New("message content must be a string or an array of content parts")
	}
	out := make([]any, 0, len(parts))
	for _, raw := range parts {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := p["type"].(string)
		switch typ {
		case "input_text", "output_text", "text", "summary_text", "refusal":
			text, _ := p["text"].(string)
			if typ == "refusal" && text == "" {
				text, _ = p["refusal"].(string)
			}
			out = append(out, map[string]any{"type": "text", "text": text})
		case "input_image":
			url, _ := p["image_url"].(string)
			if url == "" {
				return nil, errors.New("input_image part is missing image_url (file_id uploads are not supported)")
			}
			img := map[string]any{"url": url}
			if d, ok := p["detail"].(string); ok && d != "" {
				img["detail"] = d
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": img})
		default:
			return nil, fmt.Errorf("%w: content part type %q is not supported", errResponsesUnsupported, typ)
		}
	}
	return out, nil
}

// responsesToolOutput 把 function_call_output.output 归一成字符串（chat 的 tool content）。
func responsesToolOutput(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, raw := range t {
			if p, ok := raw.(map[string]any); ok {
				if s, ok := p["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	default:
		bs, _ := json.Marshal(t)
		return string(bs)
	}
}

// responsesTools 把 Responses 的扁平工具声明翻成 chat 的嵌套形态。
// 非 function 类型（web_search / file_search / computer_use …）本库无法执行，
// 直接剔除：保留会让上游因未知工具类型 400，剔除后至少纯对话与本地工具可用。
func responsesTools(tools []json.RawMessage) (out []any, echo []any, err error) {
	for _, raw := range tools {
		var t map[string]any
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, nil, fmt.Errorf("parse tool: %w", err)
		}
		typ, _ := t["type"].(string)
		if typ != "" && typ != "function" {
			continue
		}
		name, _ := t["name"].(string)
		if name == "" {
			// 个别客户端发嵌套形态（{type:function, function:{...}}），兼容一下。
			if fn, ok := t["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
				// chat 形态与嵌套声明同构：必须保留外层 {type,function} 包装，
				// 只把内层对象塞进 tools[] 会让上游按「缺 type 的工具」判 400。
				out = append(out, map[string]any{"type": "function", "function": fn})
				echo = append(echo, t)
				continue
			}
			return nil, nil, errors.New("tool is missing name")
		}
		fn := map[string]any{"name": name}
		if d, ok := t["description"].(string); ok && d != "" {
			fn["description"] = d
		}
		if params, ok := t["parameters"]; ok && params != nil {
			fn["parameters"] = params
		}
		if strict, ok := t["strict"].(bool); ok {
			fn["strict"] = strict
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
		echo = append(echo, t)
	}
	return out, echo, nil
}

// responsesToolChoice 把 Responses 的 tool_choice 翻成 chat 形态（上游只认字符串或
// 嵌套对象，payload 层还会再兜一次）。
func responsesToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
		return map[string]any{"type": "function", "function": map[string]any{"name": obj.Name}}
	}
	return nil
}

// responsesToolChoiceEcho 回显用：保持客户端原始形态。
func responsesToolChoiceEcho(raw json.RawMessage) any {
	if len(raw) == 0 {
		return "auto"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "auto"
	}
	return v
}

// responsesResponseFormat 把 text.format 的 json_schema 翻成 chat 的 response_format。
func responsesResponseFormat(text *struct {
	Format json.RawMessage `json:"format"`
}) any {
	if text == nil || len(text.Format) == 0 {
		return nil
	}
	var f struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict *bool           `json:"strict"`
	}
	if err := json.Unmarshal(text.Format, &f); err != nil {
		return nil
	}
	if f.Type != "json_schema" {
		return nil // text / json_object：chat 侧语义等价于不设，不编造
	}
	js := map[string]any{"name": f.Name, "schema": json.RawMessage(f.Schema)}
	if f.Strict != nil {
		js["strict"] = *f.Strict
	}
	return map[string]any{"type": "json_schema", "json_schema": js}
}

// responsesFormatEcho 取客户端原始 text.format 供响应回显。
// 与 responsesResponseFormat（出站 chat 形态）刻意分开：回显必须与客户端发来的
// 结构一致，否则客户端按自己发过的配置比对时会认为网关改写了设置。
func responsesFormatEcho(text *struct {
	Format json.RawMessage `json:"format"`
}) any {
	if text == nil || len(text.Format) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(text.Format, &v); err != nil {
		return nil
	}
	return v
}

// ───────────────────────────── 端点 ─────────────────────────────

// responses POST /v1/responses：翻译入站协议后交给 chatCompletions，出站经
// responsesSink 翻回 Responses 事件流/对象。
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	chatBody, plan, err := responsesToChat(body)
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errResponsesUnsupported) {
			code = "unsupported_feature"
		}
		writeOpenAIError(w, http.StatusBadRequest, code, err.Error())
		return
	}

	// 克隆请求：保留头/上下文（鉴权已在 mux 层完成，客户端 IP、UA、request id 都
	// 要跟着走进 chat 链路，否则流水行与归档会缺来源），只换 body。
	// 路径刻意保留 /v1/responses：chatCompletions 不按路径分支，而 trace.path 与
	// 面板「请求记录」要显示真实入站端点；改成 chat 路径会让两类流量在账本里混成一堆。
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(chatBody))
	r2.ContentLength = int64(len(chatBody))

	sink := newResponsesSink(w, plan)
	// panic 兜底：适配器在 chat 链路之外多了一层翻译，任何 panic 都不能让客户端
	// 收到半个事件流就断线（那是"上游无响应"的假象）。回 response.failed 更诚实。
	defer sink.recoverPanic()

	h.chatCompletions(sink, r2)
	sink.Finish()
}

// ───────────────────────────── 出站：Chat SSE → Responses ─────────────────────────────

// responsesSink 是套在 chat 处理器外面的 ResponseWriter。
//
// 关键点：chat 链路在出错时会写 JSON（writeOpenAIError），成功时写 SSE。两种形态
// 必须分开处置——JSON 错误原样透传（状态码 + 上游原文 + gateway_hint 全保留），
// SSE 逐帧翻译。因此这里**延迟决定**：直到看见第一段 body 才知道走哪条路；
// 非 SSE 时什么都不写，交给 Finish 统一回放。
type responsesSink struct {
	real http.ResponseWriter
	hdr  http.Header
	plan *responsesPlan

	code    int
	decided bool
	sse     bool

	buf []byte       // SSE 未成帧残留
	raw bytes.Buffer // 非 SSE 全量 body

	em      *responsesEmitter
	collect bool // 客户端要非流式：事件只在网关内累积，Finish 时回一个 response 对象
	done    bool
}

func newResponsesSink(w http.ResponseWriter, plan *responsesPlan) *responsesSink {
	return &responsesSink{real: w, hdr: http.Header{}, plan: plan, collect: !plan.stream}
}

func (s *responsesSink) Header() http.Header { return s.hdr }

// adoptHeaders 把 chat 链路写在 sink 上的响应头搬到真实 writer。
// 不搬的后果不是「少个装饰头」：429 的 Retry-After、上游透出的限流提示、
// 以及 StreamHint 设的 Cache-Control 都会在翻译层被静默丢弃，客户端只能看到
// 一个没有任何重试提示的 429，或者一个会被中间层缓冲的 SSE。
// 同名键以真实 writer 上已有的为准（ServeHTTP 入口已写入 X-Request-Id）。
func (s *responsesSink) adoptHeaders() {
	if s.real == nil {
		return
	}
	dst := s.real.Header()
	for k, vs := range s.hdr {
		if strings.EqualFold(k, "Content-Length") {
			continue // 翻译后长度必变，交给 net/http 现算
		}
		if len(dst.Values(k)) > 0 {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// Unwrap 让 http.ResponseController（若中间件使用）能拿到底层 ResponseWriter。
func (s *responsesSink) Unwrap() http.ResponseWriter { return s.real }

func (s *responsesSink) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
}

func (s *responsesSink) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	if !s.decided {
		s.decided = true
		s.adoptHeaders()
		ct := strings.ToLower(s.hdr.Get("Content-Type"))
		s.sse = strings.Contains(ct, "text/event-stream")
		if s.sse {
			s.em = newResponsesEmitter(s.real, s.plan)
			s.em.silent = s.collect
		}
	}
	if !s.sse {
		// 非流式（错误 JSON，或未强制的 chat JSON）：先攒着，Finish 再决定形态。
		return s.raw.Write(p)
	}
	s.buf = append(s.buf, p...)
	s.pump()
	return len(p), nil
}

// Flush 透传 flush（SSE 逐帧刷新依赖它）。
//
// 但不是无条件透传：Flush 在 net/http 与 httptest 里都会**隐式提交 200**，一旦提交，
// Finish 再想回 400/502 就只能得到 "superfluous WriteHeader" 警告，客户端最终收到
// 「200 + 错误体」——Responses 客户端会把它当正常 response 去读 output，直接崩。
// 因此只在「已判定是 SSE 且客户端确实要流式」时才向下 flush：
//   - 非流式（collect）：整段在网关内聚合，绝不能提前提交状态码；
//   - 尚未判定形态：还没看到 Content-Type，此时 flush 等于赌它一定是 200。
func (s *responsesSink) Flush() {
	if s.collect || !s.decided || !s.sse {
		return
	}
	if f, ok := s.real.(http.Flusher); ok {
		f.Flush()
	}
}

// pump 把缓冲里已成帧的 `data:` 载荷交给 emit 器。
// 只认 `\n\n` 分隔（upstream.StreamHint 的写出格式），残余半帧留到下一次。
func (s *responsesSink) pump() {
	for {
		i := bytes.Index(s.buf, []byte("\n\n"))
		if i < 0 {
			return
		}
		frame := string(s.buf[:i])
		s.buf = s.buf[i+2:]
		s.handleFrame(frame)
	}
}

func (s *responsesSink) handleFrame(frame string) {
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			continue
		}
		if s.em != nil {
			s.em.frame(obj)
		}
	}
}

// Finish 收尾：SSE 走事件流收束，非 SSE 回放（错误透传 / chat JSON 转 Responses）。
func (s *responsesSink) Finish() {
	if s.done {
		return
	}
	s.done = true
	if s.em != nil {
		s.em.finish()
		if s.collect {
			s.writeCollected()
		}
		return
	}
	// 走到这里说明链路只写了 JSON：要么是错误信封，要么是（理论上不会出现的）
	// 非流式 chat 响应。错误必须原样透传——状态码、上游原文、gateway_hint 都是
	// 客户端排障依赖的信息，翻译成 Responses 错误信封会把 requestId 洗掉。
	s.adoptHeaders()
	code := s.code
	if code == 0 {
		code = http.StatusOK
	}
	body := s.raw.Bytes()
	if code >= 400 || bytes.Contains(body, []byte(`"error"`)) {
		if code < 400 {
			// chat 链路把「上游 200 + error 帧」聚合成 JSON 错误时仍用 200 写出。
			// HTTP 层成功但内容是错误，对 Responses 客户端最坏：它会把 200 当正常
			// response 去读 output，直接崩在缺字段上，且不会重试。抬成 502
			// （可重试的上游失败）比保留 200 诚实。
			code = http.StatusBadGateway
		}
		s.real.Header().Set("Content-Type", "application/json")
		s.real.WriteHeader(code)
		_, _ = s.real.Write(body)
		return
	}
	// 非流式 chat.completion → Responses 对象（兜底路径，正常不会触发）。
	obj, err := chatCompletionToResponse(body, s.plan)
	if err != nil {
		s.real.Header().Set("Content-Type", "application/json")
		s.real.WriteHeader(http.StatusBadGateway)
		_, _ = s.real.Write([]byte(`{"error":{"message":"failed to translate upstream response","type":"api_error","code":"upstream_parse"}}`))
		return
	}
	s.real.Header().Set("Content-Type", "application/json")
	s.real.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(s.real).Encode(obj)
}

// writeCollected 把静默累积的事件结果写成单个 JSON（非流式客户端）。
// 上游 error 帧先落在这里：此时 HTTP 头还没发，用 502 让客户端按可重试错误处理，
// 而不是伪造一个 200 + 空 output。
func (s *responsesSink) writeCollected() {
	s.adoptHeaders()
	s.real.Header().Set("Content-Type", "application/json")
	if s.em.err != nil {
		s.real.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(s.real).Encode(map[string]any{"error": s.em.err})
		return
	}
	resp := s.em.final
	if resp == nil {
		resp = s.em.shell("completed")
	}
	s.real.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(s.real).Encode(resp)
}

// recoverPanic 把适配器内的 panic 转成可读错误，避免"半个事件流后静默断线"。
func (s *responsesSink) recoverPanic() {
	if rec := recover(); rec != nil {
		if s.em != nil {
			s.em.fail(map[string]any{"type": "api_error", "code": "gateway_panic",
				"message": fmt.Sprintf("gateway adapter panicked: %v", rec)})
			s.em.finish()
			s.done = true
			if s.collect {
				// 非流式：panic 发生在 Finish 之前，必须在这里把错误写成 JSON，
				// 否则客户端拿到的是「200 + 空 body」。
				s.writeCollected()
			}
			return
		}
		s.adoptHeaders()
		s.real.Header().Set("Content-Type", "application/json")
		s.real.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(s.real).Encode(map[string]any{"error": map[string]any{
			"message": fmt.Sprintf("gateway adapter panicked: %v", rec),
			"type":    "api_error", "code": "gateway_panic",
		}})
		s.done = true
	}
}

// respItem 是一个正在产出的 output item（message / reasoning / function_call）。
// Responses 的 output_index 是"在 output 数组里的位置"，因此 items 的追加顺序即
// 索引顺序——绝不重排，否则客户端按 index 拼装会错位。
type respItem struct {
	kind   string
	id     string
	callID string
	name   string
	out    int

	text      strings.Builder // message 正文 / reasoning 摘要 / function_call 参数
	partAdded bool
	closed    bool
}

// responsesEmitter 负责事件序列与最终 response 对象。
//
// 事件序（Codex 与官方 SDK 都按这个顺序解析）：
//
//	response.created → response.in_progress
//	→ [reasoning] output_item.added → reasoning_summary_part.added → *_text.delta … → *.done
//	→ [message]   output_item.added → content_part.added → output_text.delta … → *.done
//	→ [function]  output_item.added → function_call_arguments.delta … → *.done
//	→ response.completed | response.failed
type responsesEmitter struct {
	w     http.ResponseWriter
	plan  *responsesPlan
	flush func()

	id      string
	created int64
	seq     int

	items     []*respItem
	msg       *respItem
	reason    *respItem
	tools     map[int]*respItem
	toolOrder []int

	usage        map[string]any
	finishReason string

	started bool
	silent  bool // 非流式：只累积，不写任何事件
	done    bool
	err     map[string]any
	final   map[string]any
}

func newResponsesEmitter(w http.ResponseWriter, plan *responsesPlan) *responsesEmitter {
	e := &responsesEmitter{
		w:       w,
		plan:    plan,
		id:      responsesID("resp_"),
		created: time.Now().Unix(),
		tools:   map[int]*respItem{},
	}
	if f, ok := w.(http.Flusher); ok {
		e.flush = f.Flush
	} else {
		e.flush = func() {}
	}
	return e
}

// emit 写一个 SSE 事件（同时带 event: 与 data:，官方 SDK 与裸 HTTP 客户端都认）。
func (e *responsesEmitter) emit(event string, obj map[string]any) {
	if e.silent {
		return
	}
	obj["type"] = event
	obj["sequence_number"] = e.seq
	e.seq++
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	_, _ = io.WriteString(e.w, "event: "+event+"\ndata: "+string(b)+"\n\n")
	e.flush()
}

// start 首次写事件前设头并补 created/in_progress。
// 必须在真的有一帧上游数据之后才调用（见 responsesSink 的延迟决定）：否则错误
// JSON 就没机会以正确状态码透传了。
func (e *responsesEmitter) start() {
	if e.started {
		return
	}
	e.started = true
	if e.silent {
		return
	}
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	e.w.WriteHeader(http.StatusOK)
	e.emit("response.created", map[string]any{"response": e.shell("in_progress")})
	e.emit("response.in_progress", map[string]any{"response": e.shell("in_progress")})
}

// frame 处理一帧 chat chunk（upstream.StreamHint 规范化后的形态）。
func (e *responsesEmitter) frame(obj map[string]any) {
	if e.done {
		// 已收束（error 帧会立即 finish）：后续帧一律丢弃，否则会在 completed
		// 之后再冒出 item 事件，客户端按 output_index 拼装必然错位。
		return
	}
	if errObj, ok := obj["error"].(map[string]any); ok {
		// 上游 error 帧（6004/审核/empty stream 兜底）：200 已开流，只能以
		// response.failed 收束。message 原文照搬，网关不重写。
		e.fail(errObj)
		return
	}
	if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
		e.usage = u
	}
	choices, _ := obj["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	c, _ := choices[0].(map[string]any)
	if c == nil {
		return
	}
	if fr, ok := c["finish_reason"].(string); ok && fr != "" {
		e.finishReason = fr
	}
	delta, _ := c["delta"].(map[string]any)
	if delta == nil {
		return
	}
	if s, ok := delta["reasoning_content"].(string); ok && s != "" {
		e.reasoningDelta(s)
	}
	if s, ok := delta["content"].(string); ok && s != "" {
		e.textDelta(s)
	}
	if tcs, ok := delta["tool_calls"].([]any); ok {
		for _, raw := range tcs {
			tc, _ := raw.(map[string]any)
			if tc == nil {
				continue
			}
			e.toolDelta(tc)
		}
	}
}

// ── item 生命周期 ──

func (e *responsesEmitter) reasoningDelta(s string) {
	e.start()
	if e.msg != nil && !e.msg.closed {
		e.closeMessage()
	}
	e.closeTools()
	if e.reason != nil && e.reason.closed {
		// 思考内容在 message 之后又续了一段：已发过 output_item.done 的 item 不能再
		// 追加 delta（客户端会看到 done 之后还有事件，按 output_index 归并直接错乱）。
		e.reason = nil
	}
	if e.reason == nil {
		it := &respItem{kind: "reasoning", id: responsesID("rs_"), out: len(e.items)}
		e.items = append(e.items, it)
		e.reason = it
		e.emit("response.output_item.added", map[string]any{
			"output_index": it.out,
			"item": map[string]any{
				"id": it.id, "type": "reasoning", "summary": []any{},
			},
		})
	}
	it := e.reason
	if !it.partAdded {
		it.partAdded = true
		e.emit("response.reasoning_summary_part.added", map[string]any{
			"item_id": it.id, "output_index": it.out, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": ""},
		})
	}
	it.text.WriteString(s)
	e.emit("response.reasoning_summary_text.delta", map[string]any{
		"item_id": it.id, "output_index": it.out, "summary_index": 0, "delta": s,
	})
}

func (e *responsesEmitter) textDelta(s string) {
	e.start()
	e.closeReasoning()
	e.closeTools()
	if e.msg != nil && e.msg.closed {
		// 正文在工具调用之后又续了一段（部分模型会这样吐）：同上，另起一个 message
		// item，而不是往已 done 的 item 里继续写。
		e.msg = nil
	}
	if e.msg == nil {
		it := &respItem{kind: "message", id: responsesID("msg_"), out: len(e.items)}
		e.items = append(e.items, it)
		e.msg = it
		e.emit("response.output_item.added", map[string]any{
			"output_index": it.out,
			"item": map[string]any{
				"id": it.id, "type": "message", "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		})
	}
	it := e.msg
	if !it.partAdded {
		it.partAdded = true
		e.emit("response.content_part.added", map[string]any{
			"item_id": it.id, "output_index": it.out, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
	}
	it.text.WriteString(s)
	e.emit("response.output_text.delta", map[string]any{
		"item_id": it.id, "output_index": it.out, "content_index": 0,
		"delta": s, "logprobs": []any{},
	})
}

func (e *responsesEmitter) toolDelta(tc map[string]any) {
	e.start()
	e.closeReasoning()
	e.closeMessage()
	idx := 0
	if v, ok := tc["index"].(float64); ok {
		idx = int(v)
	}
	it := e.tools[idx]
	var argsChunk string
	var name, callID string
	if fn, ok := tc["function"].(map[string]any); ok {
		name, _ = fn["name"].(string)
		argsChunk, _ = fn["arguments"].(string)
	}
	if id, ok := tc["id"].(string); ok && id != "" {
		callID = id
	}
	if it != nil && it.closed {
		// 参数在 output_item.done 之后才到：丢弃。上游正常流不会这样；若强行写入，
		// 事件序会变成「done 之后还有 arguments.delta」，客户端解析必错。
		return
	}
	if it == nil {
		if callID == "" {
			callID = responsesID("call_")
		}
		it = &respItem{kind: "function_call", id: responsesID("fc_"), callID: callID, name: name, out: len(e.items)}
		e.items = append(e.items, it)
		e.tools[idx] = it
		e.toolOrder = append(e.toolOrder, idx)
		e.emit("response.output_item.added", map[string]any{
			"output_index": it.out,
			"item": map[string]any{
				"id": it.id, "type": "function_call", "status": "in_progress",
				"arguments": "", "call_id": it.callID, "name": it.name,
			},
		})
	} else if it.name == "" && name != "" {
		it.name = name
	}
	if callID != "" && it.callID == "" {
		it.callID = callID
	}
	if argsChunk != "" {
		it.text.WriteString(argsChunk)
		e.emit("response.function_call_arguments.delta", map[string]any{
			"item_id": it.id, "output_index": it.out, "delta": argsChunk,
		})
	}
}

func (e *responsesEmitter) closeReasoning() {
	it := e.reason
	if it == nil || it.closed {
		return
	}
	it.closed = true
	text := it.text.String()
	if it.partAdded {
		e.emit("response.reasoning_summary_text.done", map[string]any{
			"item_id": it.id, "output_index": it.out, "summary_index": 0, "text": text,
		})
		e.emit("response.reasoning_summary_part.done", map[string]any{
			"item_id": it.id, "output_index": it.out, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": text},
		})
	}
	e.emit("response.output_item.done", map[string]any{
		"output_index": it.out, "item": e.itemObject(it, true),
	})
}

func (e *responsesEmitter) closeMessage() {
	it := e.msg
	if it == nil || it.closed {
		return
	}
	it.closed = true
	text := it.text.String()
	if it.partAdded {
		e.emit("response.output_text.done", map[string]any{
			"item_id": it.id, "output_index": it.out, "content_index": 0, "text": text,
			"logprobs": []any{},
		})
		e.emit("response.content_part.done", map[string]any{
			"item_id": it.id, "output_index": it.out, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		})
	}
	e.emit("response.output_item.done", map[string]any{
		"output_index": it.out, "item": e.itemObject(it, true),
	})
}

func (e *responsesEmitter) closeTools() {
	for _, idx := range e.toolOrder {
		it := e.tools[idx]
		if it == nil || it.closed {
			continue
		}
		it.closed = true
		args := it.text.String()
		if args == "" {
			args = "{}"
		}
		e.emit("response.function_call_arguments.done", map[string]any{
			"item_id": it.id, "output_index": it.out, "arguments": args,
		})
		it.text.Reset()
		it.text.WriteString(args)
		e.emit("response.output_item.done", map[string]any{
			"output_index": it.out, "item": e.itemObject(it, true),
		})
	}
}

// fail 记录一个错误并立即收束为 response.failed（流已开，改不了状态码）。
func (e *responsesEmitter) fail(errObj map[string]any) {
	if e.err != nil {
		return
	}
	e.err = errObj
	e.finish()
}

// finish 收束：关完未闭合的 item，再发 completed / failed。
func (e *responsesEmitter) finish() {
	if e.done {
		return
	}
	e.done = true
	if e.err == nil && !e.started {
		// 一帧都没有：上游空流会先给 error 帧，所以这里几乎不可达（防御）。
		// 仍然按失败收束，避免客户端挂在"没有任何事件"上。
		e.err = map[string]any{"type": "api_error", "code": "empty_stream",
			"message": "upstream returned no content"}
	}
	// 错误帧可能先于任何正文到达（上游 200 + error 帧 / 空流兜底帧）：此时
	// created/in_progress 还没发过，必须先补——否则客户端只收到一个 failed，
	// 缺 response 对象，SDK 侧无法把错误挂到某个 response 上。
	e.start()
	e.closeReasoning()
	e.closeMessage()
	e.closeTools()
	if e.err != nil {
		if e.silent {
			e.final = nil
			return
		}
		e.emit("response.failed", map[string]any{"response": e.shell("failed")})
		return
	}
	status := "completed"
	if e.finishReason == "length" {
		status = "incomplete"
	}
	if e.silent {
		e.final = e.shell(status)
		return
	}
	e.emit("response.completed", map[string]any{"response": e.shell(status)})
}

// itemObject 产出单个 output item 的完整形态（用于 *.done 与 output[]）。
func (e *responsesEmitter) itemObject(it *respItem, completed bool) map[string]any {
	st := "in_progress"
	if completed {
		st = "completed"
	}
	switch it.kind {
	case "reasoning":
		summary := []any{}
		if completed && it.text.Len() > 0 {
			summary = append(summary, map[string]any{"type": "summary_text", "text": it.text.String()})
		}
		return map[string]any{"id": it.id, "type": "reasoning", "summary": summary}
	case "function_call":
		args := it.text.String()
		return map[string]any{
			"id": it.id, "type": "function_call", "status": st,
			"arguments": args, "call_id": it.callID, "name": it.name,
		}
	default:
		content := []any{}
		if it.partAdded {
			content = append(content, map[string]any{
				"type": "output_text", "text": it.text.String(), "annotations": []any{},
			})
		}
		return map[string]any{
			"id": it.id, "type": "message", "status": st,
			"role": "assistant", "content": content,
		}
	}
}

// output 返回已产出 item 的完整列表（顺序 = output_index 顺序）。
func (e *responsesEmitter) output() []any {
	out := make([]any, 0, len(e.items))
	for _, it := range e.items {
		out = append(out, e.itemObject(it, true))
	}
	return out
}

// shell 组装 response 对象。status 决定 output 是否已完结。
func (e *responsesEmitter) shell(status string) map[string]any {
	obj := map[string]any{
		"id":                   e.id,
		"object":               "response",
		"created_at":           e.created,
		"status":               status,
		"model":                e.plan.model,
		"output":               e.output(),
		"parallel_tool_calls":  e.parallelEcho(),
		"tool_choice":          e.plan.toolChoiceEcho,
		"tools":                echoOrEmpty(e.plan.toolsEcho),
		"instructions":         e.plan.instructions,
		"max_output_tokens":    e.plan.maxOutputTokens,
		"previous_response_id": nil,
		"reasoning": map[string]any{
			"effort":  nilIfEmpty(e.plan.effort),
			"summary": nil,
		},
		"store":       e.plan.store,
		"temperature": nilIfNilFloat(e.plan.temperature),
		"top_p":       nilIfNilFloat(e.plan.topP),
		"text":        map[string]any{"format": formatOrText(e.plan.textFormat)},
		"truncation":  "disabled",
		"metadata":    echoOrEmptyMap(e.plan.metadata),
		"error":       e.err,
		"incomplete_details": func() any {
			if status == "incomplete" {
				return map[string]any{"reason": "max_output_tokens"}
			}
			return nil
		}(),
	}
	if u := e.usagesObject(); u != nil {
		obj["usage"] = u
	} else {
		obj["usage"] = nil
	}
	// SDK 便利字段：output_text 是拼接后的纯文本（官方 SDK 在客户端拼，但也读这个）。
	obj["output_text"] = e.textOf()
	return obj
}

// parallelEcho 回显客户端的 parallel_tool_calls；未声明时给 true（与官方默认一致）。
func (e *responsesEmitter) parallelEcho() bool {
	if e.plan.parallelToolCalls != nil {
		return *e.plan.parallelToolCalls
	}
	return true
}

func (e *responsesEmitter) textOf() string {
	if e.msg != nil {
		return e.msg.text.String()
	}
	return ""
}

// usagesObject 把 chat usage 映射成 Responses usage。
// chat：prompt_tokens / completion_tokens / prompt_tokens_details.cached_tokens /
// completion_tokens_details.reasoning_tokens；Responses 用 input_tokens/output_tokens。
func (e *responsesEmitter) usagesObject() map[string]any {
	u := e.usage
	if len(u) == 0 {
		return nil
	}
	num := func(keys ...string) (int64, bool) {
		for _, k := range keys {
			if v, ok := u[k].(float64); ok {
				return int64(v), true
			}
		}
		return 0, false
	}
	in, okIn := num("prompt_tokens", "input_tokens")
	out, okOut := num("completion_tokens", "output_tokens")
	if !okIn && !okOut {
		return nil
	}
	cached := int64(0)
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v, ok := d["cached_tokens"].(float64); ok {
			cached = int64(v)
		}
	}
	if cached == 0 {
		if v, ok := num("cache_read_input_tokens", "cached_tokens", "prompt_cache_hit_tokens"); ok {
			cached = v
		}
	}
	reasoning := int64(0)
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		if v, ok := d["reasoning_tokens"].(float64); ok {
			reasoning = int64(v)
		}
	}
	if reasoning == 0 {
		if v, ok := num("completion_thinking_tokens"); ok {
			reasoning = v
		}
	}
	return map[string]any{
		"input_tokens":          in,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens":         out,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
		"total_tokens":          in + out,
	}
}

// ───────────────────────────── 兜底：chat.completion → response ─────────────────────────────

// chatCompletionToResponse 把非流式 chat 响应翻成 Responses 对象（兜底路径）。
func chatCompletionToResponse(body []byte, plan *responsesPlan) (map[string]any, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	e := &responsesEmitter{plan: plan, id: responsesID("resp_"), created: time.Now().Unix(), tools: map[int]*respItem{}}
	if resp.Model != "" {
		// 上游可能把模型名归一（去 realm 前缀），回显以请求侧为准更稳。
		e.plan.model = plan.model
	}
	if len(resp.Choices) > 0 {
		msg := resp.Choices[0].Message
		e.finishReason = resp.Choices[0].FinishReason
		if msg.ReasoningContent != "" {
			it := &respItem{kind: "reasoning", id: responsesID("rs_"), out: len(e.items)}
			it.text.WriteString(msg.ReasoningContent)
			it.partAdded = true
			it.closed = true
			e.items = append(e.items, it)
		}
		if msg.Content != "" {
			it := &respItem{kind: "message", id: responsesID("msg_"), out: len(e.items)}
			it.text.WriteString(msg.Content)
			it.partAdded = true
			it.closed = true
			e.items = append(e.items, it)
		}
		for _, tc := range msg.ToolCalls {
			callID := tc.ID
			if callID == "" {
				callID = responsesID("call_")
			}
			it := &respItem{kind: "function_call", id: responsesID("fc_"), callID: callID,
				name: tc.Function.Name, out: len(e.items)}
			args := tc.Function.Arguments
			if args == "" {
				args = "{}"
			}
			it.text.WriteString(args)
			it.closed = true
			e.items = append(e.items, it)
		}
	}
	e.usage = resp.Usage
	status := "completed"
	if e.finishReason == "length" {
		status = "incomplete"
	}
	return e.shell(status), nil
}

// ───────────────────────────── 小工具 ─────────────────────────────

// responsesID 生成上游风格的资源 id（resp_/msg_/rs_/fc_/call_ + 24 位 hex）。
// 用随机 hex 而非时间戳：Responses 客户端会把 id 当不透明串长期持有，时间戳形状
// 既会撞车也暴露网关身份。
func responsesID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfNilFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func echoOrEmpty(v []any) []any {
	if len(v) == 0 {
		return []any{}
	}
	return v
}

func echoOrEmptyMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func formatOrText(v any) any {
	if v == nil {
		return map[string]any{"type": "text"}
	}
	return v
}

// 保证 context 包被使用（responsesSink 不直接消费，但保留注释级依赖会引发未使用告警）。
var _ = context.Background
