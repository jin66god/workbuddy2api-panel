// responses_tool_order_test.go /v1/responses 入站顺序倒置的端到端钉桩。
//
// 复现的真实形态：Codex 经 cc-switch 类本地代理转发时，input 里
// function_call_output 排在 function_call 之前。适配器按输入顺序拼接（output 先落地成
// role:tool，call 到下一个非 function_call 项才 flush 成 assistant），出站就变成
// tool 在前、assistant 在后；上游按位置配对判 11148（tool_call_sequence_broken），
// 整条会话每轮报废。
//
// 这里断言的是「真正发到上游的 body」，不是适配器的中间结构——只有这样才能证明
// 出站管线（payload.go 的四步配对）真的兜住了它。
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestResponsesReordersEarlyFunctionCallOutput(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	// output 在 call 之前——这就是代理转发出来的倒置形态。
	body := `{"model":"glm-5.2","stream":true,"input":[
		{"role":"user","content":"跑一下 shell"},
		{"type":"function_call_output","call_id":"call_0","output":"ok"},
		{"type":"function_call","call_id":"call_0","name":"shell","arguments":"{}"},
		{"role":"user","content":"继续"}]}`
	rec := doResponses(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 取发往上游的那条 chat body（可能夹带余额等其它调用，按 messages 字段筛）。
	mu.Lock()
	defer mu.Unlock()
	var messages []any
	for _, b := range bodies {
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err != nil {
			continue
		}
		if ms, ok := parsed["messages"].([]any); ok {
			messages = ms
			break
		}
	}
	if messages == nil {
		t.Fatalf("没有抓到含 messages 的上游请求体（共 %d 次调用）", len(bodies))
	}

	// 逐条定位：assistant(tool_calls call_0) 必须排在 tool(call_0) 之前。
	callIdx, toolIdx := -1, -1
	for i, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, tci := range tcs {
					if tc, ok := tci.(map[string]any); ok {
						if id, _ := tc["id"].(string); id == "call_0" {
							callIdx = i
						}
					}
				}
			}
		case "tool":
			if id, _ := m["tool_call_id"].(string); id == "call_0" {
				toolIdx = i
			}
		}
	}
	if callIdx < 0 {
		t.Fatalf("出站 body 里没有声明 call_0 的 assistant（messages=%s）", bodies[len(bodies)-1])
	}
	if toolIdx < 0 {
		t.Fatalf("出站 body 里没有 call_0 的 tool 结果（messages=%s）", bodies[len(bodies)-1])
	}
	if toolIdx < callIdx {
		t.Fatalf("出站仍是倒置：tool(call_0) 在下标 %d，声明在 %d（上游会判 11148）", toolIdx, callIdx)
	}
	// 结果内容必须原样保留，重排只动顺序。
	if got, _ := messages[toolIdx].(map[string]any)["content"].(string); got != "ok" {
		t.Fatalf("tool 结果内容被改动: %q want %q", got, "ok")
	}
}
