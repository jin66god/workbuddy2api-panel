package upstream

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// 上游心跳注释帧必须自带帧分隔空行，不能与紧随其后的 data 帧粘连。
//
// Stream 吞掉上游的空行、只给 data 帧自产 "\n\n"。注释行只写单行时输出会是
// ": heartbeat\ndata: {...}\n\n" —— 按 "\n\n" 切帧的适配层会把整块当成没有
// "data: " 前缀的注释丢掉，连带吞掉紧随其后的首个增量（GLM 工具调用首帧就带
// id+name，丢掉后整个调用消失）。
func TestStreamHeartbeatKeepsFrameBoundary(t *testing.T) {
	frames := ": heartbeat\n" +
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"REPL","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	rec := httptest.NewRecorder()
	if err := Stream(rec, strings.NewReader(frames)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()

	if !strings.Contains(body, ": heartbeat\n\n") {
		t.Fatalf("心跳注释帧未补分隔空行: %q", body)
	}
	if strings.Contains(body, ": heartbeat\ndata: ") {
		t.Fatalf("心跳与 data 帧粘连: %q", body)
	}
	if !strings.Contains(body, `"name":"REPL"`) {
		t.Fatalf("紧随心跳的工具调用帧丢失: %q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("DONE 未被写出: %q", body)
	}
}
