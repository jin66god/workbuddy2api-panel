package upstream

import (
	"encoding/json"
	"testing"
)

// TestMergeAdjacentTextThenToolCalls 钉住「同一轮的正文与工具调用被拆成两条 assistant」
// 的合并（形态三：正文在前、调用在后）。
//
// 这段历史来自 Codex 类客户端：一轮里先输出一句进度正文，再单独发 function_call item，
// /v1/responses 适配层按 input 顺序拼出的就是 assistant(正文) + assistant(tool_calls)。
// 不合并的话，Kimi/GLM 系会在「进度那一句」处收尾——它看到上一轮已经说过话、没有待办，
// 于是只回一句进度就停，工具永远不被调用，而网关日志里一切正常。
//
// 同时钉住三条不该合并的边界，避免修复过头把不同轮的消息粘在一起。
func TestMergeAdjacentTextThenToolCalls(t *testing.T) {
	call := func(id string) map[string]any {
		return map[string]any{
			"id":       id,
			"type":     "function",
			"function": map[string]any{"name": "ls", "arguments": "{}"},
		}
	}

	t.Run("正文 + 调用合并成一条", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "system", "content": "s"},
			map[string]any{"role": "user", "content": "u"},
			map[string]any{"role": "assistant", "content": "我先看看目录。"},
			map[string]any{"role": "assistant", "tool_calls": []any{call("c1")}},
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": "ok"},
		}
		out, changed := mergeAdjacentToolCalls(msgs)
		if !changed {
			t.Fatal("正文 + 调用必须合并（形态三）")
		}
		if len(out) != 4 {
			t.Fatalf("合并后应有 4 条消息，得到 %d 条: %v", len(out), out)
		}
		a, ok := out[2].(map[string]any)
		if !ok {
			t.Fatalf("第 3 条不是对象: %#v", out[2])
		}
		if a["content"] != "我先看看目录。" {
			t.Fatalf("正文丢失: %#v", a["content"])
		}
		tcs, ok := a["tool_calls"].([]any)
		if !ok || len(tcs) != 1 {
			t.Fatalf("调用未挂到正文那条: %#v", a["tool_calls"])
		}
		// 顺序必须是 assistant(正文+调用) 紧跟 tool，否则上游 11148。
		if r, _ := out[3].(map[string]any)["role"].(string); r != "tool" {
			t.Fatalf("合并后应紧跟 tool，得到 %q", r)
		}
	})

	t.Run("隔着 user 消息不合并", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "assistant", "content": "上一轮的答复"},
			map[string]any{"role": "user", "content": "下一轮请求"},
			map[string]any{"role": "assistant", "tool_calls": []any{call("c1")}},
		}
		if _, changed := mergeAdjacentToolCalls(msgs); changed {
			t.Fatal("隔着 user 消息的两条 assistant 不是同一轮，不得合并")
		}
	})

	t.Run("后一条有正文不合并", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "assistant", "content": "a"},
			map[string]any{"role": "assistant", "content": "b", "tool_calls": []any{call("c1")}},
		}
		if _, changed := mergeAdjacentToolCalls(msgs); changed {
			t.Fatal("两条都有正文无法无损拼接，不得合并")
		}
	})

	t.Run("正文是数组（多模态块）不合并", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "a"}}},
			map[string]any{"role": "assistant", "tool_calls": []any{call("c1")}},
		}
		if _, changed := mergeAdjacentToolCalls(msgs); changed {
			t.Fatal("数组正文合并会丢结构，不得合并")
		}
	})

	t.Run("reasoning_content 搬到合并结果", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "assistant", "content": "正文"},
			map[string]any{"role": "assistant", "tool_calls": []any{call("c1")}, "reasoning_content": "thought"},
		}
		out, changed := mergeAdjacentToolCalls(msgs)
		if !changed || len(out) != 1 {
			t.Fatalf("应合并成一条，changed=%v len=%d", changed, len(out))
		}
		a := out[0].(map[string]any)
		if a["reasoning_content"] != "thought" {
			t.Fatalf("thinking 回填字段丢失: %#v", a["reasoning_content"])
		}
	})

	t.Run("已经合一的单条不动", func(t *testing.T) {
		msgs := []any{
			map[string]any{"role": "assistant", "content": "正文", "tool_calls": []any{call("c1")}},
		}
		if _, changed := mergeAdjacentToolCalls(msgs); changed {
			t.Fatal("单条已合一不应再改")
		}
	})
}

// TestPrepareBodyMergesTextThenCalls 走真实出站管线，确认形态三在 prepareBody 里生效
// （不是只在单测里直接调 mergeAdjacentToolCalls）。
func TestPrepareBodyMergesTextThenCalls(t *testing.T) {
	body := `{"model":"cn:m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},` +
		`{"role":"assistant","content":"我先看看目录。"},` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	out := string(PrepareBodyOptWithEfforts([]byte(body), false, nil))
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("out not json: %v", err)
	}
	msgs := obj["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("出站应有 4 条消息（正文与调用合一），得到 %d 条: %v", len(msgs), msgs)
	}
	a := msgs[2].(map[string]any)
	if a["content"] != "我先看看目录。" {
		t.Fatalf("正文丢失: %#v", a["content"])
	}
	if _, ok := a["tool_calls"].([]any); !ok {
		t.Fatalf("调用未合并到正文那条: %#v", a)
	}
}
