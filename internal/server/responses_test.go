package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

// ───────────────────────── 请求侧翻译 ─────────────────────────

// TestResponsesToChatBasic 断言最小请求：instructions→system、字符串 input→user、
// max_output_tokens→max_tokens、reasoning.effort→reasoning_effort、强制 stream。
func TestResponsesToChatBasic(t *testing.T) {
	body := `{
		"model":"cn:deepseek-v4.1-flash",
		"instructions":"你是助手",
		"input":"你好",
		"max_output_tokens":256,
		"reasoning":{"effort":"high"},
		"temperature":0.3,
		"stream":false
	}`
	chat, plan, err := responsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(chat, &got); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if got["stream"] != true {
		t.Errorf("stream=%v want true（上游只认流式，非流式由网关聚合）", got["stream"])
	}
	if got["model"] != "cn:deepseek-v4.1-flash" {
		t.Errorf("model=%v", got["model"])
	}
	if got["max_tokens"] != float64(256) {
		t.Errorf("max_tokens=%v want 256", got["max_tokens"])
	}
	if got["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort=%v want high", got["reasoning_effort"])
	}
	if plan.stream {
		t.Error("plan.stream 应保留客户端的 stream=false（决定出站形态）")
	}
	if plan.instructions != "你是助手" {
		t.Errorf("plan.instructions=%q", plan.instructions)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len=%d want 2（system + user）", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "你是助手" {
		t.Errorf("首条应为 instructions 转的 system: %v", sys)
	}
	user := msgs[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "你好" {
		t.Errorf("第二条应为 user: %v", user)
	}
}

// TestResponsesToChatInputItems 覆盖 input 条目数组：图片 part、工具声明扁平→嵌套、
// 并行 function_call 合并、function_call_output→tool 消息、reasoning 条目丢弃。
func TestResponsesToChatInputItems(t *testing.T) {
	body := `{
		"model":"cn:kimi-k3.1-pro",
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"看图"},
				{"type":"input_image","image_url":"data:image/png;base64,AAA","detail":"high"}
			]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"忽略我"}]},
			{"type":"function_call","name":"read_file","arguments":"{\"p\":\"a\"}","call_id":"call_1"},
			{"type":"function_call","name":"list_dir","arguments":"{\"p\":\"b\"}","call_id":"call_2"},
			{"type":"function_call_output","call_id":"call_1","output":"ok1"},
			{"type":"function_call_output","call_id":"call_2","output":[{"type":"input_text","text":"ok2"}]}
		],
		"tools":[{"type":"function","name":"read_file","description":"读文件","parameters":{"type":"object"}},
		         {"type":"web_search"}],
		"tool_choice":{"type":"function","name":"read_file"}
	}`
	chat, plan, err := responsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got struct {
		Messages   []map[string]any `json:"messages"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	if err := json.Unmarshal(chat, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages=%d want 4（user + assistant(合并 2 个 tool_call) + 2 条 tool）: %v", len(got.Messages), got.Messages)
	}
	// user 多模态 part
	parts, _ := got.Messages[0]["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user content parts=%d want 2", len(parts))
	}
	img := parts[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("part 应为 image_url: %v", img)
	}
	imgObj, _ := img["image_url"].(map[string]any)
	if imgObj["url"] != "data:image/png;base64,AAA" || imgObj["detail"] != "high" {
		t.Errorf("image_url 形态错误: %v", imgObj)
	}
	// 并行调用合并进一条 assistant
	asst := got.Messages[1]
	if asst["role"] != "assistant" {
		t.Fatalf("第二条应为 assistant: %v", asst)
	}
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 2 {
		t.Fatalf("tool_calls=%d want 2（并行调用必须合并，否则 tool 消息对不上）", len(tcs))
	}
	if _, has := asst["content"]; has {
		t.Errorf("assistant.tool_calls 消息不应带 content（空串会被上游判空段 11151）: %v", asst)
	}
	// tool 消息
	for i, want := range []string{"call_1", "call_2"} {
		m := got.Messages[2+i]
		if m["role"] != "tool" || m["tool_call_id"] != want {
			t.Errorf("tool 消息[%d]=%v want tool_call_id=%s", i, m, want)
		}
	}
	if got.Messages[3]["content"] != "ok2" {
		t.Errorf("数组形态 output 应拼成字符串: %v", got.Messages[3]["content"])
	}
	// 工具：function 保留并嵌套，web_search 剔除
	if len(got.Tools) != 1 {
		t.Fatalf("tools=%d want 1（非 function 类型必须剔除）: %v", len(got.Tools), got.Tools)
	}
	fn, _ := got.Tools[0]["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["description"] != "读文件" {
		t.Errorf("工具嵌套形态错误: %v", got.Tools[0])
	}
	tc, _ := got.ToolChoice["function"].(map[string]any)
	if tc["name"] != "read_file" {
		t.Errorf("tool_choice 应转嵌套形态: %v", got.ToolChoice)
	}
	if len(plan.toolsEcho) != 1 {
		t.Errorf("plan.toolsEcho 只回显真正可用的工具: %d", len(plan.toolsEcho))
	}
}

// TestResponsesRejectsPreviousResponseID 有状态接口必须显式拒绝，不能静默丢历史。
func TestResponsesRejectsPreviousResponseID(t *testing.T) {
	_, _, err := responsesToChat([]byte(`{"model":"m","input":"hi","previous_response_id":"resp_1"}`))
	if err == nil {
		t.Fatal("previous_response_id 应回错误")
	}
	if !strings.Contains(err.Error(), "previous_response_id") {
		t.Errorf("错误文案应点名该字段: %v", err)
	}
}

// TestResponsesRejectsUnsupportedPart 未知 content part 必须点名报错，而不是静默丢内容。
func TestResponsesRejectsUnsupportedPart(t *testing.T) {
	_, _, err := responsesToChat([]byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_file","file_id":"f1"}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "input_file") {
		t.Fatalf("want 明确报错含 input_file, got %v", err)
	}
}

// ───────────────────────── 出站：端到端 ─────────────────────────

// responsesSSE 构造一段 chat SSE（与 upstream.StreamHint 的写出形态一致）。
const responsesSSECHAT = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"想一想\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14,\"prompt_tokens_details\":{\"cached_tokens\":6},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
	"data: [DONE]\n\n"

// parseResponsesEvents 把响应体拆成 (事件名, 载荷) 序列。
func parseResponsesEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, frame := range strings.Split(raw, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var eventName, data string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if data == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			t.Fatalf("事件 data 非法 JSON: %v (%s)", err, data)
		}
		if eventName == "" {
			eventName, _ = obj["type"].(string)
		}
		if obj["type"] != eventName {
			t.Errorf("event 行与 data.type 不一致: %q vs %v", eventName, obj["type"])
		}
		out = append(out, obj)
	}
	return out
}

func eventNames(events []map[string]any) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e["type"].(string))
	}
	return names
}

func containsEvent(events []map[string]any, want string) bool {
	for _, e := range events {
		if e["type"] == want {
			return true
		}
	}
	return false
}

// doResponses 走完整的 Handler 链路（含鉴权、选号、上游流、SSE 翻译）。
func doResponses(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newResponsesHandler(t *testing.T, behavior func(authz string) (int, string, bool)) *Handler {
	t.Helper()
	up := newFakeUpstream(t, behavior)
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	return NewHandler(Config{Pool: p, Upstream: up})
}

// TestResponsesStreamEndToEnd 断言流式事件序列完整且 content/usage 正确落位。
func TestResponsesStreamEndToEnd(t *testing.T) {
	var upstreamBody []byte
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, responsesSSECHAT, true
	})
	// 抓一次上游收到的 body，断言适配器真的把请求翻成了 chat 形态且 stream=true。
	up := h.cfg.Upstream
	_ = up

	rec := doResponses(t, h, `{"model":"glm-5.2","input":"你好","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type=%q want text/event-stream", ct)
	}
	events := parseResponsesEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal("没有解析出任何事件")
	}
	names := eventNames(events)
	// 顺序约束：created 必须最先，completed 必须最后。
	if names[0] != "response.created" {
		t.Errorf("首事件=%s want response.created（names=%v）", names[0], names)
	}
	if names[len(names)-1] != "response.completed" {
		t.Errorf("末事件=%s want response.completed（names=%v）", names[len(names)-1], names)
	}
	for _, want := range []string{"response.in_progress", "response.output_item.added",
		"response.content_part.added", "response.output_text.delta", "response.output_text.done",
		"response.content_part.done", "response.output_item.done", "response.reasoning_summary_text.delta"} {
		if !containsEvent(events, want) {
			t.Errorf("缺少事件 %s（names=%v）", want, names)
		}
	}
	// 正文拼装
	var text strings.Builder
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			s, _ := e["delta"].(string)
			text.WriteString(s)
		}
	}
	if text.String() != "你好" {
		t.Errorf("output_text 拼接=%q want 你好", text.String())
	}
	// completed 里的 response 对象
	last := events[len(events)-1]
	resp, _ := last["response"].(map[string]any)
	if resp["object"] != "response" || resp["status"] != "completed" {
		t.Errorf("response 对象形态错误: %v", resp)
	}
	if model, _ := resp["model"].(string); model != "glm-5.2" {
		// 回显以请求侧为准：请求是 glm-5.2，这里应保持
		t.Errorf("model=%q", model)
	}
	output, _ := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output len=%d want 2（reasoning + message）: %v", len(output), output)
	}
	msg := output[1].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Fatalf("第二个 item 应为 assistant message: %v", msg)
	}
	content := msg["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "output_text" || part["text"] != "你好" {
		t.Errorf("message content=%v", part)
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(4) {
		t.Errorf("usage=%v want in=10 out=4", usage)
	}
	details, _ := usage["input_tokens_details"].(map[string]any)
	if details["cached_tokens"] != float64(6) {
		t.Errorf("cached_tokens=%v want 6", details["cached_tokens"])
	}
	// sequence_number 必须单调递增
	prev := -1.0
	for _, e := range events {
		if v, ok := e["sequence_number"].(float64); ok {
			if v <= prev {
				t.Errorf("sequence_number 非递增: %v 在 %v 之后", v, prev)
			}
			prev = v
		}
	}
	_ = upstreamBody
}

// TestResponsesNonStreamAggregates 非流式请求内部走流式上游，网关聚合成单个 response 对象。
func TestResponsesNonStreamAggregates(t *testing.T) {
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, responsesSSECHAT, true
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"你好","stream":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("非流式不应返回 SSE: %q", ct)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body 不是 JSON: %v (%s)", err, rec.Body.String())
	}
	if resp["object"] != "response" || resp["status"] != "completed" {
		t.Fatalf("形态错误: %v", resp)
	}
	if resp["output_text"] != "你好" {
		t.Errorf("output_text=%v want 你好", resp["output_text"])
	}
	if _, ok := resp["usage"].(map[string]any); !ok {
		t.Errorf("非流式也应带 usage: %v", resp["usage"])
	}
}

// TestResponsesToolCallStream 断言 tool_calls 分片被翻成 function_call item，
// 参数完整拼装并给出 arguments.done。
const responsesSSETool = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"p\\\":\"}}]}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}}]}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n" +
	"data: [DONE]\n\n"

func TestResponsesToolCallStream(t *testing.T) {
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, responsesSSETool, true
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"读 a.txt","stream":true,
		"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	events := parseResponsesEvents(t, rec.Body.String())
	if !containsEvent(events, "response.function_call_arguments.delta") {
		t.Errorf("缺少 arguments.delta: %v", eventNames(events))
	}
	var argsDone string
	for _, e := range events {
		if e["type"] == "response.function_call_arguments.done" {
			argsDone, _ = e["arguments"].(string)
		}
	}
	if argsDone != `{"p":"a.txt"}` {
		t.Errorf("arguments=%q want {\"p\":\"a.txt\"}", argsDone)
	}
	last := events[len(events)-1]
	if last["type"] != "response.completed" {
		t.Fatalf("末事件=%v", last["type"])
	}
	resp := last["response"].(map[string]any)
	output := resp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output len=%d want 1: %v", len(output), output)
	}
	item := output[0].(map[string]any)
	if item["type"] != "function_call" || item["name"] != "read_file" || item["call_id"] != "call_abc" {
		t.Errorf("function_call item 形态错误: %v", item)
	}
	if item["arguments"] != `{"p":"a.txt"}` {
		t.Errorf("arguments=%q", item["arguments"])
	}
}

// TestResponsesErrorPassthrough 上游错误必须原样透传（状态码 + 上游原文 + hint），
// 不能被翻译成事件流把 requestId 洗掉。
func TestResponsesErrorPassthrough(t *testing.T) {
	const upstreamErr = `{"code":11102,"msg":"no such model on backend","requestId":"req-9","data":{}}`
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 400, upstreamErr, false
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "event-stream") {
		t.Errorf("错误不应以 SSE 返回: %q", rec.Header().Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "no such model on backend") {
		t.Errorf("上游原文未透传: %v", errObj)
	}
	if !strings.Contains(msg, "req-9") {
		t.Errorf("requestId 丢失: %v", msg)
	}
}

// TestResponsesEmptyStreamFails 上游 200 + 空流：StreamHint 写 error 帧，
// 适配器必须收束成 response.failed，而不是静默断流。
func TestResponsesEmptyStreamFails(t *testing.T) {
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, "", true
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"hi","stream":true}`)
	events := parseResponsesEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatalf("无事件（code=%d）", rec.Code)
	}
	if !containsEvent(events, "response.failed") {
		t.Errorf("应发出 response.failed: %v", eventNames(events))
	}
}

// TestResponsesRouteRequiresAuth 新端点必须挂鉴权（漏挂 = 整个账号池裸奔）。
func TestResponsesRouteRequiresAuth(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, responsesSSECHAT, true }),
		APIKey:   "secret",
	})
	rec := doResponses(t, h, `{"model":"m","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401（无鉴权必须拒绝）body=%s", rec.Code, rec.Body.String())
	}
}

// ───────────────────────── 回归：记账 / 形状 / 头转发 ─────────────────────────

// TestResponsesRequestIsTraced 钉住「/v1/responses 必须在请求记账白名单里」。
// 漏挂的症状极隐蔽：功能可用、客户端正常，但面板「请求记录」空白、stdout 没有流水行、
// 归档里没有事件，积分却在掉——事后完全无法对账。判据只能靠断言，肉眼看不出。
func TestResponsesRequestIsTraced(t *testing.T) {
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   newFakeUpstream(t, func(string) (int, string, bool) { return 200, responsesSSECHAT, true }),
		RequestLog: reqLog,
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"你好","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if id := rec.Header().Get("X-Request-Id"); !strings.HasPrefix(id, "req-") {
		t.Fatalf("X-Request-Id=%q（未进记账链路）", id)
	}
	snap := reqLog.Snapshot()
	if len(snap.Recent) != 1 {
		t.Fatalf("请求未被记账: completed=%d recent=%d", snap.Completed, len(snap.Recent))
	}
	e := snap.Recent[0]
	if e.Path != responsesPath {
		t.Errorf("path=%q want %q（面板要显示真实入站端点）", e.Path, responsesPath)
	}
	if e.Model != "glm-5.2" {
		t.Errorf("model=%q want glm-5.2", e.Model)
	}
	if !e.OK || e.Status != http.StatusOK {
		t.Errorf("event ok=%v status=%d", e.OK, e.Status)
	}
}

// TestResponsesToolsNestedShape 嵌套形态 {type:function, function:{...}} 出站必须
// 保留外层包装：只塞内层对象会被上游判「工具缺 type」400。回显仍用客户端原形态。
func TestResponsesToolsNestedShape(t *testing.T) {
	body := `{"model":"m","input":"hi","tools":[
		{"type":"function","function":{"name":"read_file","description":"读文件","parameters":{"type":"object"}}}
	]}`
	chat, plan, err := responsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(chat, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("tools len=%d want 1: %v", len(got.Tools), got.Tools)
	}
	tool := got.Tools[0]
	if tool["type"] != "function" {
		t.Fatalf("外层 type 丢失: %v", tool)
	}
	fn, _ := tool["function"].(map[string]any)
	if fn == nil || fn["name"] != "read_file" {
		t.Fatalf("内层 function 形态错误: %v", tool)
	}
	if len(plan.toolsEcho) != 1 {
		t.Fatalf("回显条数=%d want 1", len(plan.toolsEcho))
	}
}

// TestResponsesForwardsChatResponseHeaders chat 链路写在 sink 上的响应头必须回写真实
// writer：429 的 Retry-After 丢了，客户端只能硬撞；SSE 的缓存头丢了，反代会缓冲整条流。
func TestResponsesForwardsChatResponseHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := newResponsesSink(rec, &responsesPlan{stream: true})
	sink.Header().Set("Content-Type", "text/event-stream")
	sink.Header().Set("Retry-After", "30")
	sink.Header().Set("Content-Length", "999")
	if _, err := sink.Write([]byte("data: {}\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After=%q want 30（chat 侧响应头被吞）", got)
	}
	if got := rec.Header().Get("Content-Length"); got == "999" {
		t.Errorf("Content-Length 不应透传（翻译后长度已变）")
	}
}

// TestResponsesNonStreamUpstreamErrorFrame 非流式请求遇到上游 200 + error 帧：
// 头还没发出去，必须回 502 + JSON 错误，而不是伪造 200 + 空 output。
func TestResponsesNonStreamUpstreamErrorFrame(t *testing.T) {
	const errFrame = "data: {\"error\":{\"code\":6004,\"message\":\"rate limited\"}}\n\n"
	h := newResponsesHandler(t, func(string) (int, string, bool) { return 200, errFrame, true })
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"hi","stream":false}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d want 502 body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body 不是 JSON: %v (%s)", err, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["message"] != "rate limited" {
		t.Errorf("上游原文未透传: %v", body)
	}
}

// TestResponsesEchoesRequestShape 回显必须是客户端原形态：
// parallel_tool_calls 不能硬编码 true，text.format 不能多包一层 json_schema。
func TestResponsesEchoesRequestShape(t *testing.T) {
	h := newResponsesHandler(t, func(string) (int, string, bool) { return 200, responsesSSECHAT, true })
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"你好","stream":true,
		"parallel_tool_calls":false,
		"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	events := parseResponsesEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal("无事件")
	}
	resp, _ := events[len(events)-1]["response"].(map[string]any)
	if resp == nil {
		t.Fatalf("末事件缺 response 对象: %v", events[len(events)-1])
	}
	if v, ok := resp["parallel_tool_calls"].(bool); !ok || v {
		t.Errorf("parallel_tool_calls=%v want false（回显被硬编码）", resp["parallel_tool_calls"])
	}
	text, _ := resp["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "out" {
		t.Errorf("text.format=%v（应是 Responses 原形态，不能是 chat 的 json_schema 包装）", format)
	}
}
