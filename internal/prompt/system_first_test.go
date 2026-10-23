// system_first_test.go 上游渠道校验「首条必须是 system」的回归（issue #231）。
//
// 2026-10-20 真机实测（国际版 5.8.4，同一 token / 同一 body，只改首条角色）：
//
//	首条 user      → 400 code=11-128 "first message is not system prompt"
//	首条 developer → 400 code=11-128 "Illegal API invocation from an unapproved channel"
//	首条 system    → 200，模型正常回包
//
// 这不是内容策略也不是账号问题：**首条角色本身**属于渠道校验。passthrough 模式
// 原样透传客户端 body 时，多数 OpenAI 兼容客户端首条是 user，于是每次请求必撞
// 11-128，账号被反复记成「渠道校验拦截」而全池冷却——表象即 issue #231
// 「国际版账号全部被禁用，但网页还能聊」。
package prompt

import (
	"encoding/json"
	"testing"
)

func firstRole(t *testing.T, body []byte) (string, []map[string]any) {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, body)
	}
	raw, _ := obj["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		mm, _ := m.(map[string]any)
		out = append(out, mm)
	}
	if len(out) == 0 {
		return "", nil
	}
	r, _ := out[0]["role"].(string)
	return r, out
}

// 首条 user（SDK/手搓客户端最常见形态）→ 在最前补一条 system，其余消息逐字不动。
func TestEnsureSystemFirstPrepends(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`)
	out := EnsureSystemFirst(in, "GW")
	role, msgs := firstRole(t, out)
	if role != "system" {
		t.Fatalf("首条 role=%q want system (%s)", role, out)
	}
	if len(msgs) != 3 {
		t.Fatalf("消息数=%d want 3 (%s)", len(msgs), out)
	}
	if msgs[0]["content"] != "GW" {
		t.Errorf("补入的 system 内容=%v want GW", msgs[0]["content"])
	}
	// 原消息逐字保留。
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "hi" {
		t.Errorf("原 user 消息被改动: %+v", msgs[1])
	}
	if msgs[2]["role"] != "assistant" || msgs[2]["content"] != "yo" {
		t.Errorf("原 assistant 消息被改动: %+v", msgs[2])
	}
}

// 首条 developer（OpenAI 新规范的 system 别名）→ 就地改 role，不增删消息。
func TestEnsureSystemFirstDeveloperInPlace(t *testing.T) {
	in := []byte(`{"messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}]}`)
	out := EnsureSystemFirst(in, "GW")
	role, msgs := firstRole(t, out)
	if role != "system" {
		t.Fatalf("首条 role=%q want system (%s)", role, out)
	}
	if len(msgs) != 2 {
		t.Fatalf("消息数=%d want 2（developer 应就地改写，不额外插入）", len(msgs))
	}
	if msgs[0]["content"] != "sys" {
		t.Errorf("developer 内容应逐字保留, got %v", msgs[0]["content"])
	}
}

// 首条已是 system → 一个字节都不改（避免重复注入 system）。
func TestEnsureSystemFirstNoop(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	out := EnsureSystemFirst(in, "GW")
	if string(out) != string(in) {
		t.Errorf("已是 system 时应原样返回\n got=%s\nwant=%s", out, in)
	}
}

// fallback 为空 → 使用 Degraded 中性提示词。
func TestEnsureSystemFirstFallbackDegraded(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	_, msgs := firstRole(t, EnsureSystemFirst(in, ""))
	if msgs[0]["content"] != Degraded {
		t.Errorf("补入内容=%v want Degraded", msgs[0]["content"])
	}
}

// 空 body / 非法 JSON / 无 messages / 空数组 → 原样返回，不 panic、不造消息。
func TestEnsureSystemFirstDegenerate(t *testing.T) {
	cases := []string{
		``,
		`not json`,
		`{"model":"m"}`,
		`{"messages":[]}`,
	}
	for _, in := range cases {
		out := EnsureSystemFirst([]byte(in), "GW")
		if string(out) != in {
			t.Errorf("输入 %q 应原样返回, got %q", in, out)
		}
	}
}

// 非对象元素（非法 messages[0]）不 panic，原样返回。
func TestEnsureSystemFirstNonObjectFirst(t *testing.T) {
	in := `{"messages":["weird",{"role":"user","content":"hi"}]}`
	if out := EnsureSystemFirst([]byte(in), "GW"); string(out) != in {
		t.Errorf("非对象首条应原样返回, got %s", out)
	}
}
