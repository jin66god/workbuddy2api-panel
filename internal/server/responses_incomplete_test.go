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
