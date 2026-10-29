package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// kimiToolBody 三个工具：
//   - lookup：parameters 里带 $schema/$defs 与两个 $ref；
//   - 两个 MCP 工具：PR 初版会按名字前缀丢弃，最终形态必须全部保留；
//   - metadata 里的大整数：只能重编码 tools，不能把整个 body 解成 float64。
const kimiToolBody = `{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"lookup","strict":true,"parameters":{"type":"object","$schema":"schema","$defs":{"Item":{"type":"string","enum":["a","b"]}},"properties":{"x":{"$ref":"#/$defs/Item"},"y":{"$ref":"#/$defs/Item"}}}}},{"type":"function","function":{"name":"mcp__rea__inspect","parameters":{"type":"object"}}},{"type":"function","function":{"name":"mcp__tencent_docs__read","parameters":{"type":"object"}}}],"metadata":{"keep":1234567890123456789}}`

func TestKimiToolsExpandsRefsWithoutDroppingTools(t *testing.T) {
	out := prepareKimiTools([]byte(kimiToolBody), "cn")
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tools := got["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools=%v want 3（只展开 $ref，不裁剪任何工具）", tools)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["strict"] != true {
		t.Fatal("lost function metadata")
	}
	params := fn["parameters"].(map[string]any)
	if _, ok := params["$defs"]; ok {
		t.Fatal("definitions remain")
	}
	if _, ok := params["$schema"]; ok {
		t.Fatal("schema marker remains")
	}
	props := params["properties"].(map[string]any)
	for _, key := range []string{"x", "y"} {
		v := props[key].(map[string]any)
		if v["type"] != "string" || len(v["enum"].([]any)) != 2 {
			t.Fatalf("ref %s=%v", key, v)
		}
	}
	if !strings.Contains(string(out), "1234567890123456789") {
		t.Fatal("unrelated number changed")
	}
	if !strings.Contains(string(out), `"content":"test"`) {
		t.Fatal("unrelated message changed")
	}
}

func TestKimiToolsPassthrough(t *testing.T) {
	for _, tc := range []struct{ name, body, realm string }{
		{"非 kimi", strings.ReplaceAll(kimiToolBody, "kimi-k3.1-pro", "deepseek-v4.1-flash"), "cn"},
		{"国际域", kimiToolBody, "global"},
		{"非法 JSON", `invalid`, "cn"},
		{"无 tools", `{"model":"kimi","tools":[]}`, "cn"},
		{"无 tools 字段", `{"model":"kimi","messages":[]}`, "cn"},
	} {
		if got := string(prepareKimiTools([]byte(tc.body), tc.realm)); got != tc.body {
			t.Fatalf("%s: 应原字节返回，got %s", tc.name, got)
		}
	}
}

// TestKimiToolsCyclicRefStops 自引用 schema 不能无限递归（budget/depth 双保险）。
func TestKimiToolsCyclicRefStops(t *testing.T) {
	body := `{"model":"kimi-k2.7","tools":[{"type":"function","function":{"name":"loop","parameters":{"type":"object","$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}},"properties":{"n":{"$ref":"#/$defs/Node"}}}}}]}`
	out := prepareKimiTools([]byte(body), "cn")
	if !json.Valid(out) {
		t.Fatalf("输出不是合法 JSON: %s", out)
	}
	if strings.Contains(string(out), "$ref") {
		t.Fatalf("仍有未展开引用: %s", out)
	}
}

// TestPrepareBodyAppliesKimiTools prepareBody 是 chat 出站唯一入口，接线必须生效。
func TestPrepareBodyAppliesKimiTools(t *testing.T) {
	c := &Client{}
	out := c.prepareBody([]byte(kimiToolBody), "cn", "", "")
	if strings.Contains(string(out), "$ref") {
		t.Fatalf("prepareBody 未展开 $ref: %s", out)
	}
	// 非 kimi 模型不受影响（避免把通用路径改成「所有请求都重编码 tools」）。
	other := strings.ReplaceAll(kimiToolBody, "kimi-k3.1-pro", "glm-5.2")
	if got := c.prepareBody([]byte(other), "cn", "", ""); !strings.Contains(string(got), "$ref") {
		t.Fatalf("非 kimi 模型被改写: %s", got)
	}
}
