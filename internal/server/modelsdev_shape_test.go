package server

import (
	"reflect"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestModelsDevShapeTextOnly 断言纯文本模型：modalities 只有 text，没有数据源的
// 字段（limit / tool_call / reasoning*）一个都不写。
func TestModelsDevShapeTextOnly(t *testing.T) {
	entry := map[string]any{"id": "cn:m1", "object": "model"}
	applyModelsDevShape(entry, upstream.ModelInfo{ID: "m1"}, nil, false)
	if entry["id"] != "cn:m1" || entry["object"] != "model" {
		t.Fatalf("既有键被改写: %v", entry)
	}
	mod, _ := entry["modalities"].(map[string]any)
	if !reflect.DeepEqual(mod["input"], []string{"text"}) || !reflect.DeepEqual(mod["output"], []string{"text"}) {
		t.Fatalf("modalities=%v", mod)
	}
	for _, k := range []string{"limit", "tool_call", "reasoning", "reasoning_options", "supported_reasoning_levels"} {
		if _, ok := entry[k]; ok {
			t.Fatalf("无数据源却写了 %s: %v", k, entry[k])
		}
	}
}

// TestModelsDevShapeFull 断言视觉 + 工具 + 多档思考模型：能力旗标与档位表齐全，
// off 折成 toggle + supported_reasoning_levels 的 none。
func TestModelsDevShapeFull(t *testing.T) {
	entry := map[string]any{"id": "cn:m2", "context_length": 200000, "max_output_tokens": 32000}
	mi := upstream.ModelInfo{ID: "m2", SupportsImages: true, SupportsToolCall: true, SupportsReasoning: true}
	applyModelsDevShape(entry, mi, []string{"low", "high", "off"}, false)

	mod := entry["modalities"].(map[string]any)
	if !reflect.DeepEqual(mod["input"], []string{"text", "image"}) {
		t.Fatalf("input 模态=%v want [text image]", mod["input"])
	}
	limit := entry["limit"].(map[string]any)
	if limit["context"] != 200000 || limit["output"] != 32000 {
		t.Fatalf("limit 未搬运原值: %v", limit)
	}
	if entry["tool_call"] != true || entry["reasoning"] != true {
		t.Fatalf("能力旗标缺失: %v", entry)
	}
	opts := entry["reasoning_options"].([]map[string]any)
	if len(opts) != 2 || opts[0]["type"] != "effort" || opts[1]["type"] != "toggle" {
		t.Fatalf("reasoning_options=%v", opts)
	}
	if !reflect.DeepEqual(opts[0]["values"], []string{"low", "high"}) {
		t.Fatalf("档位里混进了 off: %v", opts[0]["values"])
	}
	levels := entry["supported_reasoning_levels"].([]map[string]string)
	if len(levels) != 3 || levels[0]["effort"] != "none" {
		t.Fatalf("supported_reasoning_levels=%v", levels)
	}
}

// TestModelsDevShapeImageOut 断言出图模型：output 模态为 image、limit 省略。
func TestModelsDevShapeImageOut(t *testing.T) {
	entry := map[string]any{"id": "cn:img"}
	applyModelsDevShape(entry, upstream.ModelInfo{ID: "img"}, nil, true)
	mod := entry["modalities"].(map[string]any)
	if !reflect.DeepEqual(mod["output"], []string{"image"}) {
		t.Fatalf("output 模态=%v want [image]", mod["output"])
	}
	if _, ok := entry["limit"]; ok {
		t.Fatalf("出图模型不该有 limit: %v", entry)
	}
}
