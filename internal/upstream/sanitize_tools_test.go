package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// quote 把字符串编码成 JSON 字符串字面量（测试拼 body 用）。
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// tools[].function.description 也要走脱敏。
//
// 此前 sanitize 只进 messages：messages[].content 被洗，而客户端自带的工具描述原样
// 出站。工具描述与 system prompt 同源、都是客户端作者写的固定模板句（DeepSeek Harness
// 这类身份句就写在 description 里），属同一类校验高发区。
func TestSanitizeCoversToolsDescription(t *testing.T) {
	desc := "You are DeepSeek Harness, a coding agent. Read a file from disk when needed."
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read_file","description":` + quote(desc) + `}}]}`

	out := PrepareBodyOpt([]byte(body), true)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, out)
	}
	fn := obj["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	got := fn["description"].(string)
	if strings.Contains(got, "DeepSeek Harness") {
		t.Fatalf("tools 描述里的身份句未被净化，原样出站: %q", got)
	}
	if !strings.Contains(got, "DeepSeekRuntime") {
		t.Fatalf("应是同义改写而不是删除: %q", got)
	}
	if !strings.Contains(got, "coding agent") {
		t.Fatalf("改写不得丢失语义: %q", got)
	}
}

// 反例：没有指纹的工具描述必须一字不动（不能为覆盖 tools 把正常描述改写/裁空白）。
func TestSanitizeToolsLeavesCleanDescription(t *testing.T) {
	desc := "Read a file from disk."
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read_file","description":` + quote(desc) + `}}]}`

	var obj map[string]any
	if err := json.Unmarshal(PrepareBodyOpt([]byte(body), true), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	fn := obj["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if got := fn["description"].(string); got != desc {
		t.Fatalf("无指纹描述被误改: %q", got)
	}
}

// sanitize=false 时 tools 一律不动（脱敏开关关闭后不得留后门）。
func TestSanitizeDisabledLeavesTools(t *testing.T) {
	desc := "You are DeepSeek Harness."
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"x","description":` + quote(desc) + `}}]}`

	var obj map[string]any
	if err := json.Unmarshal(PrepareBodyOpt([]byte(body), false), &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	fn := obj["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if got := fn["description"].(string); got != desc {
		t.Fatalf("脱敏关闭时不该改写 tools: %q", got)
	}
}
