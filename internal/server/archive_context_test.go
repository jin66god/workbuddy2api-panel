package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestArchiveContextKeepsSystemAndMessages 断言小请求体原样保留 system + messages。
func TestArchiveContextKeepsSystemAndMessages(t *testing.T) {
	body := []byte(`{"model":"m","system":"sys-text","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}],"tools":[{"type":"function"}]}`)
	out := archiveContext(body)
	if len(out) == 0 {
		t.Fatal("archiveContext 返回空")
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if obj["system"] != "sys-text" {
		t.Fatalf("system 丢失: %v", obj)
	}
	if msgs, _ := obj["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("messages=%v", obj["messages"])
	}
	// tools 等与上下文无关的字段不记录（体积与噪声）。
	if _, ok := obj["tools"]; ok {
		t.Fatalf("不该记录 tools: %v", obj)
	}
}

// TestArchiveContextDropsOldestWhenOversize 断言超预算时丢最旧轮次，输出仍是合法
// JSON 且不超过 contextMaxBytes（不截成半个 JSON）。
func TestArchiveContextDropsOldestWhenOversize(t *testing.T) {
	big := strings.Repeat("x", 8<<10)
	var sb strings.Builder
	sb.WriteString(`{"messages":[`)
	for i := 0; i < 6; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"role":"user","content":"` + big + `"},{"role":"assistant","content":"ok"}`)
	}
	sb.WriteString(`]}`)
	out := archiveContext([]byte(sb.String()))
	if len(out) == 0 {
		t.Fatal("archiveContext 返回空")
	}
	if len(out) > contextMaxBytes {
		t.Fatalf("超出预算: %d > %d", len(out), contextMaxBytes)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("裁剪后不是合法 JSON: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("messages 被裁空")
	}
}

// TestArchiveContextInvalidBody 断言非法请求体返回 nil（不写脏数据进归档）。
func TestArchiveContextInvalidBody(t *testing.T) {
	if out := archiveContext([]byte("not-json")); out != nil {
		t.Fatalf("非法 body 应返回 nil，得到 %s", out)
	}
}

// TestRequestEventContextOnlyWhenSet 断言开关关闭（context 为 nil）时事件里不出现
// context 字段，开启时原样带上——避免「默认把提示词落盘」。
func TestRequestEventContextOnlyWhenSet(t *testing.T) {
	tr := &requestTrace{}
	ev := tr.event(200)
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), `"context"`) {
		t.Fatalf("未开启却写入了 context: %s", b)
	}
	tr.context = json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`)
	ev = tr.event(200)
	b, _ = json.Marshal(ev)
	if !strings.Contains(string(b), `"context"`) {
		t.Fatalf("开启后 context 丢失: %s", b)
	}
}
