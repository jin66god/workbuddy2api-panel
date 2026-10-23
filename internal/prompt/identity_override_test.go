package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOverrideHarnessIdentityPreservesRest(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"system","content":"You are an AI agent powered by DeepSeek Harness.\n\nFollow the project rules."},{"role":"developer","content":"Keep tool calls correct."},{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`)
	out := OverrideHarnessIdentity(input, "You are CodeBuddy Code.")
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	want := "You are CodeBuddy Code.\n\nFollow the project rules."
	if body.Model != "m" || len(body.Messages) != 3 || body.Messages[0].Content != want {
		t.Fatalf("identity rewrite lost content: %+v", body)
	}
	if body.Messages[1].Content != "Keep tool calls correct." ||
		body.Messages[2].Content != "You are an AI agent powered by DeepSeek Harness." {
		t.Fatalf("non-identity messages changed: %+v", body.Messages)
	}
	if again := OverrideHarnessIdentity(out, "You are CodeBuddy Code."); string(again) != string(out) {
		t.Errorf("reapplying the same identity must be a no-op: %s", again)
	}
}

func TestOverrideHarnessIdentityDeveloperAndTextParts(t *testing.T) {
	input := []byte(`{"messages":[{"role":"developer","content":[{"type":"text","text":"  You are a coding assistant. More rules here."},{"type":"text","text":"Other blocks stay."}]},{"role":"user","content":"hi"}]}`)
	out := OverrideHarnessIdentity(input, "Custom identity.")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	msgs := obj["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	if got := parts[0].(map[string]any)["text"]; got != "  Custom identity. More rules here." {
		t.Errorf("first part=%v", got)
	}
	if got := parts[1].(map[string]any)["text"]; got != "Other blocks stay." {
		t.Errorf("second part changed=%v", got)
	}
}

func TestOverrideHarnessIdentityNoopGuards(t *testing.T) {
	for _, input := range []string{
		"",
		"{broken",
		`{"messages":[{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
		`{"messages":[{"role":"user","content":"hi"},{"role":"system","content":"You are an AI agent."}]}`,
		`{"messages":[{"role":"system","content":"Project instructions. You are a coding agent."}]}`,
		`{"messages":[{"role":"system","content":"You should follow these rules."}]}`,
		`{"messages":[{"role":"system","content":"You are CodeBuddy Code."}]}`,
	} {
		out := OverrideHarnessIdentity([]byte(input), "You are CodeBuddy Code.")
		if string(out) != input {
			t.Errorf("expected no change: %q => %q", input, out)
		}
	}
}

func TestOverrideHarnessIdentityNewlineAndVersion(t *testing.T) {
	input := []byte(`{"messages":[{"role":"system","content":"You are an agent for v4.1. Keep checking.\nSecond line"}]}`)
	out := OverrideHarnessIdentity(input, "Custom.")
	if !strings.Contains(string(out), "Custom. Keep checking.") || !strings.Contains(string(out), "Second line") {
		t.Errorf("first sentence boundary incorrect: %s", out)
	}
	line := []byte(`{"messages":[{"role":"system","content":"You are a coding agent\r\nFurther rules."}]}`)
	got := OverrideHarnessIdentity(line, "")
	if !strings.Contains(string(got), "You are CodeBuddy Code.\\r\\nFurther rules.") {
		t.Errorf("default identity or CRLF preservation failed: %s", got)
	}
}
