package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// 兜底 system 只声明行为约定，不得自带身份声明（upstream issue #187）：
// 网关插入的身份句会与客户端 harness 的人设并列，被模型当成外部注入而拒绝执行。
func TestEnsureConsoleSystemNoIdentity(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`)
	out := ensureConsoleSystem(body)
	var obj struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, out)
	}
	if len(obj.Messages) != 2 || obj.Messages[0].Role != "system" {
		t.Fatalf("应在首位插入 system: %+v", obj.Messages)
	}
	first := strings.ToLower(obj.Messages[0].Content)
	if strings.Contains(first, "you are") || strings.Contains(first, "assistant") {
		t.Fatalf("兜底 system 不应声明身份: %q", obj.Messages[0].Content)
	}
	if !strings.Contains(first, "follow the user's instructions") {
		t.Fatalf("应声明行为约定: %q", obj.Messages[0].Content)
	}
	// 首条已是 system 时不重复注入。
	again := ensureConsoleSystem([]byte(`{"messages":[{"role":"system","content":"x"},{"role":"user","content":"hi"}]}`))
	var obj2 struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(again, &obj2)
	if len(obj2.Messages) != 2 {
		t.Fatalf("首条已是 system 时不应注入，得到 %d 条", len(obj2.Messages))
	}
}
