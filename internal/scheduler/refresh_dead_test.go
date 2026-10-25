package scheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// refresh 恒回 12153、但 access token 仍远未过期 → 不计 session dead，
// 永远不会被保活禁用（issue #83）。
//
// 生产实测依据：29 个账号里 4 个 refresh 恒 12153，AT 还剩 26~333 天，chat 全 200。
// 旧行为会在连续 3 轮保活后把这种健康号禁用，面板一片橙色。
func TestRunKeepaliveRefreshDeadButAccessTokenAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"code":12153,"msg":"12153:refresh token failed:400 Bad Request: invalid_grant: Session doesn't have required client"}`))
	}))
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(26 * 24 * time.Hour).Unix()}
	p.Add(a)

	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	for i := 0; i < pool.SessionDeadThreshold()+2; i++ {
		s.RunKeepaliveNow()
	}
	st, _ := p.Status("u1")
	if st.Disabled {
		t.Fatalf("AT 仍有效时不该禁用（refresh 死 ≠ 账号死）: %+v", st)
	}
}
