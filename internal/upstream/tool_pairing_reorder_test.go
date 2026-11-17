// tool_pairing_reorder_test.go 「结果早于调用」这一种顺序倒置的回位行为钉桩。
//
// 场景来源：Codex 经 cc-switch 类本地代理转发 /v1/responses 时，input 里
// function_call_output 会排在对应的 function_call 之前，适配器按输入顺序拼接，
// 出站就是 tool(c0) 在前、assistant(tool_calls c0) 在后。OpenAI 兼容协议按位置
// 配对，上游判 11148（tool_call_sequence_broken）并顶死整条会话，换号无效。
//
// 这一步只调顺序、不改内容，所以断言口径是「消息序列摘要 + 不变式」，
// 不是「删掉了什么」。
package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// assertToolAfterCall 断言 wire 不变式：任何 tool 结果都不得超过它的声明位置。
// 找不到声明者的孤儿不在本步职责内（交给 cleanupOrphanToolCalls），跳过。
func assertToolAfterCall(t *testing.T, messages []any) {
	t.Helper()
	declAt := map[string]int{}
	for i, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok || m["role"] != "assistant" {
			continue
		}
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tci := range tcs {
			tc, ok := tci.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := tc["id"].(string); id != "" {
				declAt[id] = i
			}
		}
	}
	for i, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok || m["role"] != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		j, ok := declAt[id]
		if !ok {
			continue
		}
		if j >= i {
			t.Fatalf("tool(%s) 在下标 %d，其声明在 %d：结果早于调用，上游会判 11148", id, i, j)
		}
	}
}

func TestReorderEarlyToolResults(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			// 主场景：代理把结果直接写在调用前面。
			name: "单条早到",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"c0","content":"ok"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]},
			      {"role":"user","content":"继续"}]`,
			want: []string{"user(-)", "assistant(c0)", "tool(c0)", "user(-)"},
		},
		{
			// 同批两条都早到：保持原相对顺序，一起补到调用之后。
			name: "同批两条都早到",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"c00","content":"a"},
			      {"role":"tool","tool_call_id":"c01","content":"b"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c00","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"c01","type":"function","function":{"name":"b","arguments":"{}"}}]},
			      {"role":"user","content":"继续"}]`,
			want: []string{"user(-)", "assistant(c00,c01)", "tool(c00)", "tool(c01)", "user(-)"},
		},
		{
			// 正常结果与早到结果混排：正常的先出（wire 顺序），早到的补在同批末尾。
			name: "正常与早到混排",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"c01","content":"b"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c00","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"c01","type":"function","function":{"name":"b","arguments":"{}"}}]},
			      {"role":"tool","tool_call_id":"c00","content":"a"},
			      {"role":"user","content":"继续"}]`,
			want: []string{"user(-)", "assistant(c00,c01)", "tool(c00)", "tool(c01)", "user(-)"},
		},
		{
			// 声明者在末尾：结果要一路挪到最后，跨过中间的 user 消息。
			name: "声明者在末尾",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"c0","content":"ok"},
			      {"role":"user","content":"继续"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]}]`,
			want: []string{"user(-)", "user(-)", "assistant(c0)", "tool(c0)"},
		},
		{
			// 孤儿结果没有声明者：原地不动，交给 cleanupOrphanToolCalls 删。
			name: "孤儿不动",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"zzz","content":"orphan"},
			      {"role":"tool","tool_call_id":"c0","content":"ok"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]}]`,
			want: []string{"user(-)", "tool(zzz)", "assistant(c0)", "tool(c0)"},
		},
		{
			// 同一 id 被两条 assistant 声明：跟最后一次（与 cleanup 的对称裁剪同口径）。
			name: "重复声明跟最后一次",
			in: `[{"role":"user","content":"hi"},
			      {"role":"tool","tool_call_id":"c0","content":"ok"},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]},
			      {"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]},
			      {"role":"user","content":"继续"}]`,
			want: []string{"user(-)", "assistant(c0)", "assistant(c0)", "tool(c0)", "user(-)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := reorderEarlyToolResults(msgs(t, c.in))
			if !changed {
				t.Fatalf("应当判定为倒置并重排，实际返回未改动")
			}
			assertSummary(t, summarize(got), c.want)
			assertToolAfterCall(t, got)
		})
	}
}

// TestReorderEarlyToolResultsNoop 顺序本就正确的请求必须零改动、零分配：
// 返回同一个 slice 与 false，否则每个正常请求都会多复制一遍消息数组。
func TestReorderEarlyToolResultsNoop(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"正常顺序", `[{"role":"user","content":"hi"},
			{"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c0","content":"ok"},
			{"role":"user","content":"继续"}]`},
		{"没有工具调用", `[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]`},
		{"只有孤儿结果", `[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"zzz","content":"x"}]`},
		{"单条消息", `[{"role":"user","content":"hi"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := msgs(t, c.in)
			got, changed := reorderEarlyToolResults(in)
			if changed {
				t.Fatalf("不应判定为倒置")
			}
			if len(got) != len(in) {
				t.Fatalf("长度变了: %d -> %d", len(in), len(got))
			}
			for i := range in {
				if got[i].(map[string]any)["role"] != in[i].(map[string]any)["role"] {
					t.Fatalf("消息[%d] 被改动", i)
				}
			}
		})
	}
}

// TestPrepareBodyReordersEarlyToolResults 走真实入口（prepareBody 管线），
// 确认重排真的接在 merge/repack/cleanup 之前，而不是只存在于孤立函数里。
func TestPrepareBodyReordersEarlyToolResults(t *testing.T) {
	body := `{"model":"glm-5.2","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"跑一下"},
		{"role":"tool","tool_call_id":"c0","content":"ok"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"shell","arguments":"{}"}}]},
		{"role":"user","content":"继续"}]}`
	out := PrepareBodyOpt([]byte(body), false)
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("出站 body 不是 JSON: %v (%s)", err, out)
	}
	messages, ok := parsed["messages"].([]any)
	if !ok {
		t.Fatalf("出站 body 没有 messages")
	}
	got := summarize(messages)
	// system 直通；tool(c0) 必须出现在 assistant(c0) 之后。
	if len(got) != 5 {
		t.Fatalf("消息数 %d want 5（%v）", len(got), got)
	}
	if got[2] != "assistant(c0)" || got[3] != "tool(c0)" {
		t.Fatalf("出站顺序仍是倒置: %v", got)
	}
	assertToolAfterCall(t, messages)
	if !strings.Contains(string(out), `"tool_call_id":"c0"`) {
		t.Fatalf("重排不应改动结果内容，出站缺 tool_call_id: %s", out)
	}
}

// TestPrepareBodyEarlyResultWithInterleavedNotice 四步互相配合的场景：
// 既有「结果早到」，又有 Codex 的 image_resize_notice 插在结果中间。
// 两步必须能叠加——早到的结果回位后同批 tool 才连续，repack 再把插在中间的
// 非 tool 消息挪到整组之后，出站才既无倒置、也无插入物。
func TestPrepareBodyEarlyResultWithInterleavedNotice(t *testing.T) {
	body := `{"model":"glm-5.2","messages":[
		{"role":"user","content":"并行跑两个"},
		{"role":"tool","tool_call_id":"c01","content":"b"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c00","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"c01","type":"function","function":{"name":"b","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c00","content":"a"},
		{"role":"developer","content":"<image_resize_notice/>"},
		{"role":"user","content":"继续"}]}`
	out := PrepareBodyOpt([]byte(body), false)
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("出站 body 不是 JSON: %v (%s)", err, out)
	}
	messages, _ := parsed["messages"].([]any)
	// developer 会在更早的 normalizeRoles 里归一成 system，这里按出站形态断言。
	assertSummary(t, summarize(messages), []string{
		"user(-)", "assistant(c00,c01)", "tool(c00)", "tool(c01)", "system(-)", "user(-)",
	})
	assertToolAfterCall(t, messages)
}
