package logfmt

import (
	"strings"
	"testing"
)

// 长 JSON 错误 body 不能按字节截断：code/msg/requestId/extError.msg 必须留全。
func TestErrorBodyKeepsKeyFields(t *testing.T) {
	body := `{"code":11134,"msg":"the model provider is temporarily unavailable, please retry later or switch to another model","requestId":"f3a011ef01dfb1623708b93942d738e2","extError":{"code":"internal_error","msg":"provider shard 7 unhealthy"}}`
	// 窄预算下先保 code / requestId / ext.code（短标识，排障必需）。
	got := ErrorBody(body, 80)
	for _, want := range []string{"code=11134", "requestId=f3a011ef01dfb1623708b93942d738e2", "ext.code=internal_error"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ErrorBody 丢失 %q: %s", want, got)
		}
	}
	// 生产用的 300 字节预算下，ext.msg 也必须留全。
	if wide := ErrorBody(body, 300); !strings.Contains(wide, "provider shard 7 unhealthy") {
		t.Fatalf("300 字节预算丢失 ext.msg: %s", wide)
	}
	if !strings.Contains(got, "bytes)") {
		t.Fatalf("应标注原始字节数: %s", got)
	}
}

func TestErrorBodyShortAndNonJSON(t *testing.T) {
	if got := ErrorBody(` {"code":1} `, 200); got != `{"code":1}` {
		t.Fatalf("短 body 应原样（trim）: %q", got)
	}
	html := strings.Repeat("<html>x</html>", 40)
	if got := ErrorBody(html, 50); len(got) > 50 {
		t.Fatalf("非 JSON 应走 Truncate: %d bytes", len(got))
	}
	if got := ErrorBody("", 100); got != "" {
		t.Fatalf("空 body 应为空: %q", got)
	}
}
