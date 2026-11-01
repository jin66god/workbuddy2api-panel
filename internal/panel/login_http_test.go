package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoginHTTPDisablesH2 登录 client 必须真正禁 h2 并带分阶段超时。
//
// 默认 transport 经 ALPN 协商出 h2，容器 DNS 抖动留下的半死 h2 流被复用时
// 会一直等响应头直到 30s 总超时——现象是「拿不到授权链接 / 登录已完成但检测不到」。
// 判据是 TLSNextProto 非 nil（空映射）而不是 ForceAttemptHTTP2，后者对默认
// TLS 握手不生效（upstream/transport.go 有同样的实证）。
func TestLoginHTTPDisablesH2(t *testing.T) {
	tr, ok := loginHTTP.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("loginHTTP.Transport = %T，want *http.Transport", loginHTTP.Transport)
	}
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto 必须是非 nil 空映射（nil 会让标准库注入默认 h2 映射）")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 必须为空（禁 h2），实际 %d 项", len(tr.TLSNextProto))
	}
	for name, got := range map[string]time.Duration{
		"TLSHandshakeTimeout":   tr.TLSHandshakeTimeout,
		"ResponseHeaderTimeout": tr.ResponseHeaderTimeout,
	} {
		if got <= 0 {
			t.Errorf("%s 必须为正（分阶段超时，避免拖满总超时）", name)
		}
	}
}

// TestLoginJSONRetriesTransportOnly 「一个响应都没拿到」重试，已应答的业务错误不重试。
func TestLoginJSONRetriesTransportOnly(t *testing.T) {
	var drop int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&drop, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server 不支持 hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close() // 连接直接断开：客户端拿不到任何响应
		}
	}))
	defer srv.Close()

	_, status, err := doJSON(http.MethodPost, srv.URL, "", strings.NewReader(`{}`), "")
	if err == nil {
		t.Fatal("连接被断开时必须返回错误")
	}
	if status != 0 {
		t.Fatalf("status = %d，传输层失败应为 0", status)
	}
	if n := atomic.LoadInt32(&drop); n != int32(loginAttempts) {
		t.Fatalf("上游收到 %d 次请求，want %d（传输层失败应按 loginAttempts 重试）", n, loginAttempts)
	}

	var hit int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":10001,"msg":"bad"}`))
	}))
	defer srv2.Close()

	_, status, err = doJSON(http.MethodPost, srv2.URL, "", strings.NewReader(`{}`), "")
	if err == nil || status != http.StatusBadRequest {
		t.Fatalf("status=%d err=%v，want 400 + error", status, err)
	}
	if n := atomic.LoadInt32(&hit); n != 1 {
		t.Fatalf("已应答的 400 被请求了 %d 次，want 1（业务错误不得重试）", n)
	}
}
