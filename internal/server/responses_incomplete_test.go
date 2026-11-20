package server

import (
	"net/http"
	"testing"
)

// 上游被输出上限截断（finish_reason=length）时，末事件必须是 response.incomplete。
//
// 为什么单独钉这条：OpenAI Responses 流式协议里「截断」是独立事件
// response.incomplete，客户端按**事件类型**判定收尾。此前只把 response.status
// 写成 incomplete、事件名仍发 response.completed，Codex 一类客户端会把这轮当
// 正常完成——既不提示被截断也不再续跑，用户看到的就是「任务没做完直接结束，
// 要手动发一句继续」。
func TestResponsesStreamIncompleteOnLength(t *testing.T) {
	const sse = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"写到一半\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8192,\"total_tokens\":8202}}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, sse, true
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"写个长文档","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	events := parseResponsesEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal("没有解析出任何事件")
	}
	names := eventNames(events)
	last := events[len(events)-1]
	if last["type"] != "response.incomplete" {
		t.Errorf("末事件=%v want response.incomplete（names=%v）", last["type"], names)
	}
	if containsEvent(events, "response.completed") {
		t.Errorf("截断时不得再发 response.completed（names=%v）", names)
	}
	resp, _ := last["response"].(map[string]any)
	if resp == nil {
		t.Fatal("response.incomplete 缺少 response 对象")
	}
	if got, _ := resp["status"].(string); got != "incomplete" {
		t.Errorf("status=%q want incomplete", got)
	}
	if det, _ := resp["incomplete_details"].(map[string]any); det == nil {
		t.Errorf("incomplete_details 缺失: %#v", resp["incomplete_details"])
	} else if reason, _ := det["reason"].(string); reason != "max_output_tokens" {
		t.Errorf("incomplete_details.reason=%q want max_output_tokens", reason)
	}
}

// 正常收尾（stop）仍必须是 response.completed，别把上一条修过头。
func TestResponsesStreamCompletedOnStop(t *testing.T) {
	h := newResponsesHandler(t, func(authz string) (int, string, bool) {
		return 200, responsesSSECHAT, true
	})
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"你好","stream":true}`)
	events := parseResponsesEvents(t, rec.Body.String())
	names := eventNames(events)
	last := events[len(events)-1]
	if last["type"] != "response.completed" {
		t.Errorf("末事件=%v want response.completed（names=%v）", last["type"], names)
	}
	if containsEvent(events, "response.incomplete") {
		t.Errorf("正常收尾不得发 response.incomplete（names=%v）", names)
	}
}

// 只有思考、没有正文与工具调用时必须是 response.failed，而不是 completed。
// 参考 misakano7545 fork 的 empty_reasoning_only 判据（线上高频形态：模型把整轮
// 预算花在思考上，Agent 客户端看到 completed 就收尾，用户只能手动喊「继续」）。
func TestResponsesReasoningOnlyFails(t *testing.T) {
	const sse = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"先想一会儿\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":12,\"total_tokens\":22}}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"把仓库重构一遍","stream":true}`)
	events := parseResponsesEvents(t, rec.Body.String())
	last := events[len(events)-1]
	if last["type"] != "response.failed" {
		t.Fatalf("末事件=%v want response.failed（names=%v）", last["type"], eventNames(events))
	}
	resp, _ := last["response"].(map[string]any)
	errObj, _ := resp["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "empty_reasoning_only" {
		t.Errorf("error.code=%v want empty_reasoning_only", errObj["code"])
	}
}

// 内容过滤截断同样是 incomplete，且理由要如实写 content_filter。
func TestResponsesContentFilterIncomplete(t *testing.T) {
	const sse = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"half\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"x","stream":true}`)
	events := parseResponsesEvents(t, rec.Body.String())
	last := events[len(events)-1]
	if last["type"] != "response.incomplete" {
		t.Fatalf("末事件=%v want response.incomplete（names=%v）", last["type"], eventNames(events))
	}
	resp, _ := last["response"].(map[string]any)
	det, _ := resp["incomplete_details"].(map[string]any)
	if reason, _ := det["reason"].(string); reason != "content_filter" {
		t.Errorf("incomplete_details.reason=%q want content_filter", reason)
	}
}

// 截断时 output item 自身也要标 incomplete：客户端据此知道这条没写完，
// 不会把半截参数当完整调用去执行。
func TestResponsesTruncatedItemStatus(t *testing.T) {
	const sse = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"写到一半\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"

	h := newResponsesHandler(t, func(authz string) (int, string, bool) { return 200, sse, true })
	rec := doResponses(t, h, `{"model":"glm-5.2","input":"写长文","stream":true}`)
	events := parseResponsesEvents(t, rec.Body.String())
	last := events[len(events)-1]
	resp, _ := last["response"].(map[string]any)
	out, _ := resp["output"].([]any)
	if len(out) == 0 {
		t.Fatal("output 为空")
	}
	item, _ := out[0].(map[string]any)
	if item["status"] != "incomplete" {
		t.Errorf("output[0].status=%v want incomplete", item["status"])
	}
}
