package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// custom(freeform) 工具（Codex 的 apply_patch 就是它）在请求阶段必须被降级成
// 「单 input 参数的 function」再送给上游。不降级 = 这个工具被丢掉，模型只能把
// 改动当正文吐出来，客户端看到「说一句就停」、任务做一半要人喊继续。
func TestResponsesCustomToolDowngrade(t *testing.T) {
	body := `{"model":"glm-5.2","input":"改个文件","tools":[
		{"type":"custom","name":"apply_patch","description":"打补丁","format":{"type":"grammar","definition":"start: patch"}}],
		"tool_choice":"auto"}`
	chat, plan, err := responsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(chat, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len=%d want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("type=%v want function（上游只认 function 包裹）", tool["type"])
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "apply_patch" {
		t.Errorf("name=%v want apply_patch", fn["name"])
	}
	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["input"]; !ok {
		t.Errorf("parameters 缺 input 字段: %#v", params)
	}
	if plan.customTools["apply_patch"] != true {
		t.Errorf("plan.customTools 应记住 apply_patch: %#v", plan.customTools)
	}
	desc, _ := fn["description"].(string)
	if !strings.Contains(desc, "freeform") || !strings.Contains(desc, "Grammar") {
		t.Errorf("描述应带 freeform 提示与 grammar: %q", desc)
	}
}

// 回程：模型返回的 function_call 必须还原成 custom_tool_call，且 input 是原始载荷。
func TestResponsesCustomToolCallRestore(t *testing.T) {
	payload := "*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch"
	args, _ := json.Marshal(map[string]any{"input": payload})
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"apply_patch\",\"arguments\":" +
		mustJSON(string(args)) + "}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	body := `{"model":"glm-5.2","input":"改文件","stream":true,"tools":[{"type":"custom","name":"apply_patch","description":"d"}]}`
	rec := doResponses(t, h, body)
	events := parseResponsesEvents(t, rec.Body.String())

	if !containsEvent(events, "response.custom_tool_call_input.delta") {
		t.Errorf("缺 custom_tool_call_input.delta（names=%v）", eventNames(events))
	}
	if containsEvent(events, "response.function_call_arguments.delta") {
		t.Errorf("custom 工具不应发 arguments.delta（names=%v）", eventNames(events))
	}
	last := events[len(events)-1]
	resp, _ := last["response"].(map[string]any)
	out, _ := resp["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output len=%d want 1: %#v", len(out), out)
	}
	item, _ := out[0].(map[string]any)
	if item["type"] != "custom_tool_call" {
		t.Errorf("type=%v want custom_tool_call", item["type"])
	}
	if item["name"] != "apply_patch" {
		t.Errorf("name=%v", item["name"])
	}
	if item["input"] != strings.TrimSpace(payload) {
		t.Errorf("input 未还原原始载荷: %q", item["input"])
	}
	if item["status"] != "completed" {
		t.Errorf("status=%v want completed", item["status"])
	}
	if item["call_id"] != "call_1" {
		t.Errorf("call_id=%v want call_1", item["call_id"])
	}
}

// 载荷形态不可信（多键 JSON）时不能当可执行调用发出去：item 标 incomplete。
func TestResponsesCustomToolCallUntrustedPayload(t *testing.T) {
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":\"apply_patch\",\"arguments\":\"{\\\"a\\\":1,\\\"b\\\":2}\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	body := `{"model":"glm-5.2","input":"改文件","stream":true,"tools":[{"type":"custom","name":"apply_patch","description":"d"}]}`
	rec := doResponses(t, h, body)
	events := parseResponsesEvents(t, rec.Body.String())
	last := events[len(events)-1]
	resp, _ := last["response"].(map[string]any)
	out, _ := resp["output"].([]any)
	if len(out) == 0 {
		t.Fatal("output 为空")
	}
	item, _ := out[0].(map[string]any)
	if item["status"] != "incomplete" {
		t.Errorf("status=%v want incomplete（不可信载荷不得当可执行调用）", item["status"])
	}
	if item["input"] != "" {
		t.Errorf("input=%v want 空串", item["input"])
	}
}

// namespace 工具组要摊成扁平 function，回程把 ns__name 还原成 name + namespace。
func TestResponsesNamespaceToolRestore(t *testing.T) {
	body := `{"model":"glm-5.2","input":"列个线程","stream":true,"tools":[
		{"type":"namespace","name":"codex_app","tools":[
			{"name":"list_threads","description":"列出线程","parameters":{"type":"object","properties":{}}}]}]}`
	chat, plan, err := responsesToChat([]byte(body))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(chat, &got)
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("namespace 未展开: %#v", got["tools"])
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "list_threads" {
		t.Errorf("出站名=%v want list_threads", fn["name"])
	}
	if plan.nsMap["list_threads"] != "codex_app" {
		t.Errorf("nsMap=%#v want list_threads→codex_app", plan.nsMap)
	}

	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_3\",\"type\":\"function\",\"function\":{\"name\":\"codex_app__list_threads\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	rec := doResponses(t, h, body)
	events := parseResponsesEvents(t, rec.Body.String())
	last := events[len(events)-1]
	resp, _ := last["response"].(map[string]any)
	out, _ := resp["output"].([]any)
	if len(out) == 0 {
		t.Fatal("output 为空")
	}
	item, _ := out[0].(map[string]any)
	if item["name"] != "list_threads" {
		t.Errorf("回程 name=%v want list_threads（应剥掉 ns 前缀）", item["name"])
	}
	if item["namespace"] != "codex_app" {
		t.Errorf("回程 namespace=%v want codex_app（客户端按二元组派发）", item["namespace"])
	}
}

// mustJSON 把字符串包成 JSON 字面量，便于拼进 SSE 帧。
func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var _ = http.StatusOK
