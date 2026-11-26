package upstream

import (
	"encoding/json"
	"testing"
)

// 本文件钉住 tool 配对管线的三处缺口（2026-11-26 现场三修）：
//
//  1. 相邻两批调用里后一条**带正文**（「先说一句进度，再发调用」被拆成两条）必须并批；
//     不并批时前一批成了无结果调用，上游判 11148 并顶死整条会话。
//  2. 对不上本批的孤儿结果**不能遮挡**它后面的本批合法结果（原实现 break，导致插入物
//     被夹在两份结果之间）。
//  3. 同一 call_id 的**重复结果**只留第一条（上游一个调用只认一条结果）。
//
// 三条都走完整出站管线（PrepareBodyOptWithEfforts）验证，避免只测单函数却漏接管线。

func hubCall(id, name string) map[string]any {
	return map[string]any{
		"id":       id,
		"type":     "function",
		"function": map[string]any{"name": name, "arguments": "{}"},
	}
}

func hubTool(id, body string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": id, "content": body}
}

func hubWire(t *testing.T, msgs []any) []map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "glm-5.3",
		"messages": msgs,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := PrepareBodyOptWithEfforts(body, false, nil)
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal 出站 body: %v\n%s", err, out)
	}
	return got.Messages
}

// 出站形状：assistant 与其后连续 tool 结果之间不得夹任何非 tool 消息，且不得出现
// 「声明了调用但没有结果」或「有结果但无声明」的配对。
func assertWirePaired(t *testing.T, msgs []map[string]any) {
	t.Helper()
	for i := 0; i < len(msgs); i++ {
		role, _ := msgs[i]["role"].(string)
		calls, _ := msgs[i]["tool_calls"].([]any)
		if role != "assistant" || len(calls) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, c := range calls {
			if m, ok := c.(map[string]any); ok {
				if id, _ := m["id"].(string); id != "" {
					want[id] = true
				}
			}
		}
		for j := i + 1; j < len(msgs) && len(want) > 0; j++ {
			r, _ := msgs[j]["role"].(string)
			if r != "tool" {
				break // 结果区必须连续
			}
			id, _ := msgs[j]["tool_call_id"].(string)
			if !want[id] {
				t.Fatalf("第 %d 条 tool(%s) 不在前一批调用的 id 集合内（出站载荷配对断裂）", j, id)
			}
			delete(want, id)
		}
		if len(want) > 0 {
			t.Fatalf("第 %d 条 assistant 声明的调用没有结果：%v（出站即 11148）", i, want)
		}
	}
}

// 缺口一：后一条带正文的相邻批。
func TestAdjacentBatchWithTextMerged(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "查两个目录"},
		map[string]any{"role": "assistant", "tool_calls": []any{hubCall("c0", "ls")}},
		map[string]any{"role": "assistant", "content": "我先看目录", "tool_calls": []any{hubCall("c1", "ls")}},
		hubTool("c0", "a"),
		hubTool("c1", "b"),
	}

	out, changed := mergeAdjacentToolCalls(msgs)
	if !changed {
		t.Fatal("前一条带调用时，后一条带正文的相邻 assistant 必须并批")
	}
	if len(out) != 4 {
		t.Fatalf("合并后应为 4 条（user + assistant + 2 tool），得到 %d 条", len(out))
	}
	a, _ := out[1].(map[string]any)
	if a["content"] != "我先看目录" {
		t.Fatalf("正文丢失: %#v", a["content"])
	}
	if tcs, _ := a["tool_calls"].([]any); len(tcs) != 2 {
		t.Fatalf("调用未并批: %#v", a["tool_calls"])
	}

	// 端到端：出站必须是一条 assistant + 两条紧邻的 tool 结果。
	wire := hubWire(t, msgs)
	assertWirePaired(t, wire)
	if role, _ := wire[1]["role"].(string); role != "assistant" {
		t.Fatalf("wire[1] = %q，期望 assistant", role)
	}
	if tcs, _ := wire[1]["tool_calls"].([]any); len(tcs) != 2 {
		t.Fatalf("出站调用批未合并: %#v", wire[1]["tool_calls"])
	}
}

// 缺口二：孤儿结果不得遮挡它后面的本批合法结果。
func TestOrphanResultDoesNotBlockLaterResults(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "查"},
		map[string]any{"role": "assistant", "tool_calls": []any{hubCall("c0", "ls"), hubCall("c1", "ls")}},
		hubTool("c0", "a"),
		hubTool("ghost", "对不上任何调用"),           // 孤儿
		map[string]any{"role": "user", "content": "插一句"}, // 插入物
		hubTool("c1", "b"),
	}

	wire := hubWire(t, msgs)
	assertWirePaired(t, wire)

	// 孤儿必须被删掉。
	for i, m := range wire {
		if id, _ := m["tool_call_id"].(string); id == "ghost" {
			t.Fatalf("孤儿结果未被清理（第 %d 条）", i)
		}
	}
	// 两份合法结果必须回到 assistant 之后连续位置。
	got := []string{}
	for i, m := range wire {
		if role, _ := m["role"].(string); role == "tool" {
			id, _ := m["tool_call_id"].(string)
			got = append(got, id)
			if i < 2 {
				t.Fatalf("tool(%s) 出现在第 %d 条，未紧随 assistant", id, i)
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("合法结果数 = %v，want 2 条", got)
	}
}

// 缺口三：同一 call_id 的重复结果只留第一条。
func TestDuplicateToolResultsDeduped(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "查"},
		map[string]any{"role": "assistant", "tool_calls": []any{hubCall("c0", "ls")}},
		hubTool("c0", "第一次"),
		hubTool("c0", "第二次"),
	}

	out, changed := cleanupOrphanToolCalls(msgs)
	if !changed {
		t.Fatal("重复结果必须去重")
	}
	if len(out) != 3 {
		t.Fatalf("去重后应为 3 条，得到 %d 条", len(out))
	}
	if got, _ := out[2].(map[string]any)["content"].(string); got != "第一次" {
		t.Fatalf("保留的应是第一条结果，得到 %q", got)
	}

	wire := hubWire(t, msgs)
	n := 0
	for _, m := range wire {
		if role, _ := m["role"].(string); role == "tool" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("出站 tool 结果 = %d 条，want 1 条", n)
	}
}

// 边界：这些形态必须保持原样（修复不能过头）。
func TestHub26BoundariesNoOp(t *testing.T) {
	cases := []struct {
		name string
		msgs []any
	}{
		{
			name: "两条纯正文 assistant 相邻不合并",
			msgs: []any{
				map[string]any{"role": "user", "content": "q"},
				map[string]any{"role": "assistant", "content": "第一段"},
				map[string]any{"role": "assistant", "content": "第二段"},
			},
		},
		{
			name: "前一条无调用 + 后一条带正文与调用不合并",
			msgs: []any{
				map[string]any{"role": "user", "content": "q"},
				map[string]any{"role": "assistant", "content": "进度"},
				map[string]any{"role": "assistant", "content": "另一段", "tool_calls": []any{hubCall("c0", "ls")}},
				hubTool("c0", "ok"),
			},
		},
		{
			name: "隔着 tool 结果的两条 assistant 不合并",
			msgs: []any{
				map[string]any{"role": "assistant", "tool_calls": []any{hubCall("c0", "ls")}},
				hubTool("c0", "ok"),
				map[string]any{"role": "assistant", "content": "下一轮"},
			},
		},
		{
			name: "后一条是非空数组正文不拼接",
			msgs: []any{
				map[string]any{"role": "assistant", "tool_calls": []any{hubCall("c0", "ls")}},
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "x"}}, "tool_calls": []any{hubCall("c1", "ls")}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, changed := mergeAdjacentToolCalls(c.msgs); changed {
				t.Fatalf("%s：不应合并", c.name)
			}
		})
	}
}
