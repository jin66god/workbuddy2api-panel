package upstream

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSplitConcatenatedJSON 钉住粘帧拆分：只有「首尾相接的多个合法 JSON 值」才切，
// 字符串里的花括号/转义引号不算结构，半截值宁可整份不切（交给原样透传）。
func TestSplitConcatenatedJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "两个对象紧接",
			in:   `{"id":"a"}{"id":"b"}`,
			want: []string{`{"id":"a"}`, `{"id":"b"}`},
		},
		{
			name: "两个对象空白分隔",
			in:   `{"id":"a"} {"id":"b"}`,
			want: []string{`{"id":"a"}`, `{"id":"b"}`},
		},
		{
			name: "字符串里的花括号不切",
			in:   `{"id":"a}{b","choices":[]}`,
			want: []string{`{"id":"a}{b","choices":[]}`},
		},
		{
			name: "字符串里的转义引号不切",
			in:   `{"id":"a\"}{\"b"}`,
			want: []string{`{"id":"a\"}{\"b"}`},
		},
		{
			name: "半截对象整份不切",
			in:   `{"id":"a"}{"id":"b"`,
			want: []string{`{"id":"a"}{"id":"b"`},
		},
		{
			name: "括号不配平整份不切",
			in:   `{"id":"a"}}`,
			want: []string{`{"id":"a"}}`},
		},
		{
			name: "纯文本原样",
			in:   `: keepalive`,
			want: []string{`: keepalive`},
		},
		{
			name: "单个对象原样",
			in:   `{"id":"a"}`,
			want: []string{`{"id":"a"}`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitConcatenatedJSON(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parts=%d want %d: %#v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("part[%d]=%q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestStreamHintSplitsGluedFrames 端到端：上游把两个 JSON 塞进同一个 data 行时，
// 客户端侧每个 SSE 事件恰好一个 data 行、每行都是单个合法 JSON、[DONE] 恰好一个。
func TestStreamHintSplitsGluedFrames(t *testing.T) {
	raw := `data: {"id":"cmb-1","choices":[{"delta":{"content":"你"}}]}{"id":"cmb-2","choices":[{"delta":{"content":"好"}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	body := rec.Body.String()
	lines := 0
	done := 0
	for _, ln := range strings.Split(body, "\n") {
		if !strings.HasPrefix(ln, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(ln, "data: ")
		if payload == "[DONE]" {
			done++
			continue
		}
		lines++
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			t.Fatalf("客户端会解析失败: %q: %v", payload, err)
		}
	}
	if lines != 2 {
		t.Fatalf("应拆成 2 个独立 data 帧，got %d（body=%q）", lines, body)
	}
	if done != 1 {
		t.Fatalf("[DONE] 应恰好 1 个，got %d", done)
	}
	if !strings.Contains(body, `"你"`) || !strings.Contains(body, `"好"`) {
		t.Fatalf("内容丢失: %q", body)
	}
}

// TestIsTransientBilling 导出判据的边界：5xx/网关失败值得补签，业务 4xx 不值得。
func TestIsTransientBilling(t *testing.T) {
	if !IsTransientBilling(&Error{Kind: ErrServer, Status: 500, Msg: "签到失败，请稍后重试"}) {
		t.Fatal("500 应判为瞬时")
	}
	if !IsTransientBilling(&Error{Kind: ErrGatewayDown, Status: 522}) {
		t.Fatal("云flare 回源失败应判为瞬时")
	}
	if IsTransientBilling(&Error{Kind: ErrClient, Status: 400, Msg: "企业账号不支持该操作"}) {
		t.Fatal("业务拒绝不该判为瞬时（否则每天多打无效请求）")
	}
	if IsTransientBilling(nil) {
		t.Fatal("nil 不该判为瞬时")
	}
}
