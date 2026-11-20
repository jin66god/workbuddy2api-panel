package upstream

import (
	"net/http"
	"testing"
	"time"
)

// issue #12 回归：Client.ChatHTTP.Transport 外层是 headerCaseTripper（值类型），
// 直接断言 .(*http.Transport) 恒失败，导致 upstream.header_timeout_seconds 长期
// 不生效、首包恒为 60s。SetChatHeaderTimeout 必须能剥掉包装写到内层。
func TestSetChatHeaderTimeoutReachesInnerTransport(t *testing.T) {
	t.Parallel()
	c := New()
	if _, ok := c.ChatHTTP.Transport.(*http.Transport); ok {
		t.Fatalf("前提失效：Transport 直接就是 *http.Transport，本用例的前提（被包装）不成立")
	}
	want := 180 * time.Second
	if !SetChatHeaderTimeout(c.ChatHTTP.Transport, want) {
		t.Fatal("SetChatHeaderTimeout 返回 false：包装层没剥开，配置又会静默失效")
	}
	inner := underlyingHTTPTransport(c.ChatHTTP.Transport)
	if inner == nil {
		t.Fatal("underlyingHTTPTransport 取不到内层 *http.Transport")
	}
	if inner.ResponseHeaderTimeout != want {
		t.Errorf("ResponseHeaderTimeout=%v want %v（写到了错误的对象，或没覆盖构造默认 60s）",
			inner.ResponseHeaderTimeout, want)
	}
}

// 非正数视为「不设置」，不允许把超时写成 0（0 = 永不超时，会挂死长请求）。
func TestSetChatHeaderTimeoutIgnoresNonPositive(t *testing.T) {
	t.Parallel()
	c := New()
	before := underlyingHTTPTransport(c.ChatHTTP.Transport).ResponseHeaderTimeout
	if SetChatHeaderTimeout(c.ChatHTTP.Transport, 0) {
		t.Fatal("0 不应返回 true")
	}
	if SetChatHeaderTimeout(c.ChatHTTP.Transport, -time.Second) {
		t.Fatal("负值不应返回 true")
	}
	if got := underlyingHTTPTransport(c.ChatHTTP.Transport).ResponseHeaderTimeout; got != before {
		t.Errorf("非正数不该改动超时：%v -> %v", before, got)
	}
}

// 包装层对外仍要转发 CloseIdleConnections（既有约束，防止剥包装时改坏连接池行为）。
func TestUnderlyingTransportSurvivesDoubleWrapping(t *testing.T) {
	t.Parallel()
	base := newTransport()
	double := headerCaseTripper{base: headerCaseTripper{base: base}}
	if got := underlyingHTTPTransport(double); got != base {
		t.Fatalf("双层包装未剥到内层：got=%T", got)
	}
}
