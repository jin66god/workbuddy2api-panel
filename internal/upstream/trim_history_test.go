package upstream

import (
	"encoding/json"
	"testing"
)

// TestTrimHistoryKeepsLastRounds 断言 keep=N 只保留最近 N 条 user 消息起的整轮，
// system 全留、cut 落在 user 下标上（不会切出半个 tool 配对）。
func TestTrimHistoryKeepsLastRounds(t *testing.T) {
	body := `{"messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},
		{"role":"user","content":"u2"},{"role":"assistant","content":"a2"},
		{"role":"user","content":"u3"},{"role":"assistant","tool_calls":[{"id":"c1"}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"user","content":"u4"},{"role":"assistant","content":"a4"}
	]}`
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatal(err)
	}
	trimHistory(obj, 2)
	msgs := obj["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	want := []string{"system", "user", "assistant", "tool", "user", "assistant"}
	if len(roles) != len(want) {
		t.Fatalf("roles=%v want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles=%v want %v", roles, want)
		}
	}
	// 保留段必须从 user 开始（不是 tool 结果）——否则上游判配对断裂。
	if roles[1] != "user" {
		t.Fatalf("保留段首条不是 user: %v", roles)
	}
}

// TestTrimHistoryKeepsSystemInsideCut 断言被裁掉区间里的 system 消息被提到前面保留。
func TestTrimHistoryKeepsSystemInsideCut(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"u1"},
		{"role":"system","content":"mid-system"},
		{"role":"assistant","content":"a1"},
		{"role":"user","content":"u2"},
		{"role":"assistant","content":"a2"}
	]}`
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatal(err)
	}
	trimHistory(obj, 1)
	msgs := obj["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("len=%d want 3: %v", len(msgs), msgs)
	}
	if msgs[0].(map[string]any)["content"] != "mid-system" {
		t.Fatalf("被裁区间的 system 未保留在前: %v", msgs)
	}
	if msgs[1].(map[string]any)["content"] != "u2" {
		t.Fatalf("保留段应从 u2 开始: %v", msgs)
	}
}

// TestTrimHistoryDisabledIsNoop 断言 keep<=0 时 body 一个字节都不变（默认关闭）。
func TestTrimHistoryDisabledIsNoop(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`)
	out := PrepareBodyOptWithRounds(in, false, nil, nil, 0)
	var a, b map[string]any
	_ = json.Unmarshal(in, &a)
	_ = json.Unmarshal(out, &b)
	am := a["messages"].([]any)
	bm := b["messages"].([]any)
	if len(am) != len(bm) {
		t.Fatalf("keep=0 改动了消息数: %d -> %d", len(am), len(bm))
	}
}

// TestTrimHistoryRoundsAppliedOnWire 断言 keep=1 时出站 body 只剩最后一轮
// （prompt.keep_recent_rounds 的真正落点）。
func TestTrimHistoryRoundsAppliedOnWire(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-flash","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"old-1"},{"role":"assistant","content":"a1"},
		{"role":"user","content":"old-2"},{"role":"assistant","content":"a2"},
		{"role":"user","content":"latest"},{"role":"assistant","content":"a3"}]}`)
	out := PrepareBodyOptWithRounds(in, false, nil, nil, 1)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("出站 body 非法 JSON: %v", err)
	}
	msgs := obj["messages"].([]any)
	if len(msgs) != 3 { // system + user(latest) + assistant
		t.Fatalf("keep=1 后消息数=%d want 3: %v", len(msgs), msgs)
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("system 被裁: %v", msgs)
	}
	if msgs[1].(map[string]any)["content"] != "latest" {
		t.Fatalf("最后一轮不是 latest: %v", msgs)
	}
}
