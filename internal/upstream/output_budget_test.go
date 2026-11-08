// output_budget_test.go 输出预算下限：客户端给高档位思考又给极小 max_tokens 时，
// 思考会先把额度吃掉一半，正文断在句子中间（上游 2026-11-08 生产实测：
// effort=high + max_tokens=2048 → think=2048 正文=2048 finish=length）。
package upstream

import (
	"encoding/json"
	"testing"
)

// maxTokensOf 取回写后的 max_tokens：直连 map 时是 int64，JSON 往返后是 float64，
// 断言不能只认一种形态（否则「值对但类型不同」会被误报成失败）。
func maxTokensOf(obj map[string]any) int64 {
	switch v := obj["max_tokens"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return -1
}

func TestEnsureMinOutputBudgetRaises(t *testing.T) {
	cases := []struct {
		name   string
		effort string
		budget any
		want   int64
	}{
		{"low", "low", float64(1024), 4096},
		{"minimal", "minimal", float64(2048), 4096},
		{"medium", "medium", float64(2048), 6144},
		{"high", "high", float64(2048), 8192},
		{"xhigh", "xhigh", float64(2048), 12288},
		{"max", "max", float64(2048), 16384},
		{"大写档位", "HIGH", float64(2048), 8192},
		{"带空白档位", " high ", float64(2048), 8192},
		{"字符串额度", "high", "2048", 8192},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := map[string]any{"max_tokens": c.budget}
			ensureMinOutputBudget(obj, c.effort, 0)
			if got := obj["max_tokens"]; got != c.want {
				t.Fatalf("max_tokens = %v (%T)，期望 %d", got, got, c.want)
			}
		})
	}
}

// 四种「都不猜」的情形必须原样不动。
func TestEnsureMinOutputBudgetLeavesAlone(t *testing.T) {
	t.Run("没有显式额度", func(t *testing.T) {
		obj := map[string]any{"model": "cn:deepseek-v4.1-flash"}
		ensureMinOutputBudget(obj, "high", 0)
		if _, has := obj["max_tokens"]; has {
			t.Fatalf("没有显式额度时不得凭空写入 max_tokens：%v", obj["max_tokens"])
		}
	})
	t.Run("探活级额度", func(t *testing.T) {
		obj := map[string]any{"max_tokens": float64(1)}
		ensureMinOutputBudget(obj, "high", 0)
		if got := obj["max_tokens"]; got != float64(1) {
			t.Fatalf("探活额度（<16）不得抬：%v", got)
		}
	})
	t.Run("关闭思考", func(t *testing.T) {
		obj := map[string]any{"max_tokens": float64(1024), "thinking": map[string]any{"type": "disabled"}}
		ensureMinOutputBudget(obj, "high", 0)
		if got := obj["max_tokens"]; got != float64(1024) {
			t.Fatalf("关闭思考后额度全给正文，不得抬：%v", got)
		}
	})
	t.Run("档位字段表达关闭", func(t *testing.T) {
		obj := map[string]any{"max_tokens": float64(1024), "reasoning_effort": "off"}
		ensureMinOutputBudget(obj, "high", 0)
		if got := obj["max_tokens"]; got != float64(1024) {
			t.Fatalf("reasoning_effort=off 不得抬：%v", got)
		}
	})
	t.Run("未知档位", func(t *testing.T) {
		obj := map[string]any{"max_tokens": float64(1024)}
		ensureMinOutputBudget(obj, "turbo", 0)
		if got := obj["max_tokens"]; got != float64(1024) {
			t.Fatalf("未知档位不得抬：%v", got)
		}
	})
	t.Run("已够大", func(t *testing.T) {
		obj := map[string]any{"max_tokens": float64(32768)}
		ensureMinOutputBudget(obj, "high", 0)
		if got := obj["max_tokens"]; got != float64(32768) {
			t.Fatalf("已不低于下限时不得改写：%v", got)
		}
	})
}

// 目录 maxOutputTokens 比下限还小时以下限为准会写出模型物理上做不到的值 → 以目录封顶。
func TestEnsureMinOutputBudgetRespectsCatalogCap(t *testing.T) {
	obj := map[string]any{"max_tokens": float64(1024)}
	ensureMinOutputBudget(obj, "max", 4096)
	if got := maxTokensOf(obj); got != 4096 {
		t.Fatalf("目录上限更小时应以目录封顶，得到 %d", got)
	}
}

// 端到端：走完整的 prepareBody 管线，抬额度但**不改档位**。
func TestPrepareBodyAppliesMinOutputBudget(t *testing.T) {
	src := []byte(`{"model":"deepseek-v4.1-flash","max_tokens":2048,"messages":[{"role":"user","content":"写一篇长文"}]}`)
	out := PrepareBodyOptWithEffortsAndDefault(src, true, nil, nil)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal prepared body: %v", err)
	}
	if got := maxTokensOf(obj); got != 8192 {
		t.Fatalf("max_tokens = %d，期望抬到 high 档下限 8192", got)
	}
	if got := obj["reasoning_effort"]; got != defaultDeepSeekEffort {
		t.Fatalf("抬额度不得改档位：reasoning_effort = %v，期望 %s", got, defaultDeepSeekEffort)
	}
	if got := obj["model"]; got != "deepseek-v4.1-flash" {
		t.Fatalf("模型不得被改写：%v", got)
	}
}

// 客户端额度本来就够大时一个字节都不动。
func TestPrepareBodyKeepsLargeClientBudget(t *testing.T) {
	src := []byte(`{"model":"deepseek-v4.1-flash","max_tokens":32768,"messages":[{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOptWithEffortsAndDefault(src, true, nil, nil)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal prepared body: %v", err)
	}
	if got := maxTokensOf(obj); got != 32768 {
		t.Fatalf("max_tokens = %d，期望原样保留 32768", got)
	}
}

// 关闭思考的请求整体不动额度（端到端，确认 injectThinking 与下限的先后顺序）。
func TestPrepareBodySkipsBudgetWhenThinkingDisabled(t *testing.T) {
	src := []byte(`{"model":"deepseek-v4.1-flash","max_tokens":1024,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOptWithEffortsAndDefault(src, true, nil, nil)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal prepared body: %v", err)
	}
	if got := maxTokensOf(obj); got != 1024 {
		t.Fatalf("思考关闭时不得抬额度，得到 %d", got)
	}
}
