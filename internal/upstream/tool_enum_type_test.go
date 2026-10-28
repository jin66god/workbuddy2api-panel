package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// 工具 schema 里「只有 enum、没有 type」的属性会被 Kimi 系模型严格拒掉（400 code=11133
// Invalid request parameters），而 DSH 等客户端的整套工具定义里就有这类 schema——不补类型
// 等于该模型对这些客户端全废。补上可无损推断的类型即可；推不出唯一类型就保持原样，
// 交上游按原语义报错，不猜。
func TestToolSchemaEnumTypeInflated(t *testing.T) {
	props := map[string]any{
		"d":       map[string]any{"enum": []any{"x", "y"}},
		"n":       map[string]any{"enum": []any{float64(1), float64(2)}},
		"r":       map[string]any{"enum": []any{float64(1), 2.5}},
		"b":       map[string]any{"enum": []any{true, false}},
		"mix":     map[string]any{"enum": []any{"a", float64(1)}},
		"nullish": map[string]any{"enum": []any{nil, "a"}},
		"typed":   map[string]any{"type": "string", "enum": []any{"p"}},
	}
	in, err := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "f",
				"parameters": map[string]any{"type": "object", "properties": props},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := PrepareBodyOpt(in, false)

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gotProps := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	want := map[string]string{
		"d": "string", "n": "integer", "r": "number", "b": "boolean",
		"mix": "", "nullish": "", "typed": "string", // 不可推断 / 已显式 → 不写 type
	}
	for k, w := range want {
		p := gotProps[k].(map[string]any)
		gotType, _ := p["type"].(string)
		if w == "" {
			if _, has := p["type"]; has && k != "typed" {
				t.Errorf("%s: 不该补 type，实际 %q", k, gotType)
			}
			continue
		}
		if gotType != w {
			t.Errorf("%s: type=%q want %q", k, gotType, w)
		}
		// enum 取值一律原样保留——只增补类型，不改客户端语义。
		if _, ok := p["enum"]; !ok {
			t.Errorf("%s: enum 被删", k)
		}
	}
}

// 嵌套位置同样要覆盖（items / anyOf / $defs / 数组形态的 properties）。
func TestToolSchemaEnumTypeNested(t *testing.T) {
	in := `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"list":{"type":"array","items":{"enum":["a"]}},"one":{"anyOf":[{"enum":["z"]}]}}}}}]}`
	out := PrepareBodyOpt([]byte(in), false)

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	if tp, _ := props["list"].(map[string]any)["items"].(map[string]any)["type"].(string); tp != "string" {
		t.Errorf("items 内 enum 未补类型: %q", tp)
	}
	if tp, _ := props["one"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["type"].(string); tp != "string" {
		t.Errorf("anyOf 内 enum 未补类型: %q", tp)
	}
}

// response_format 的 json_schema 不走这条归一化：它的 enum 属于模型输出约束，
// 补错类型会改变约束语义（上游按原样校验）。
func TestToolSchemaEnumTypeLeavesResponseFormat(t *testing.T) {
	in := `{"model":"m","messages":[],"response_format":{"type":"json_schema","json_schema":{"schema":{"properties":{"k":{"enum":["a"]}}}}}}`
	out := PrepareBodyOpt([]byte(in), false)
	if !strings.Contains(string(out), `"enum":["a"]`) {
		t.Fatalf("response_format 被改动: %s", out)
	}
	if strings.Contains(string(out), `"k":{"type":"string","enum":["a"]}`) {
		t.Errorf("response_format 的 enum 被补了 type: %s", out)
	}
}
