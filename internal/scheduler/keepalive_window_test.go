package scheduler

// 周期保活按临期窗口刷新：排程到点只刷剩余有效期 <= window 的账号，
// 手动「全部保活」仍全量（schedule.keepalive_refresh_window）。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func newKeepaliveWindowScheduler(t *testing.T, refreshN *int32) (*Scheduler, *pool.Pool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			atomic.AddInt32(refreshN, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":7200}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	t.Cleanup(srv.Close)

	now := time.Now()
	mk := func(uid string, exp int64) *auth.Auth {
		return &auth.Auth{
			UID: uid, AccessToken: "at-" + uid, RefreshToken: "rt-" + uid,
			ExpiresAt: exp, FilePath: filepath.Join(t.TempDir(), "workbuddy-"+uid+".json"),
		}
	}
	p := pool.New("")
	p.Add(mk("u1", now.Add(30*24*time.Hour).Unix())) // 窗口外：不该刷
	p.Add(mk("u2", now.Add(2*24*time.Hour).Unix()))  // 窗口内：该刷
	p.Add(mk("u3", 0))                               // 无 expiry：恒刷（兜底）
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), p
}

// TestKeepaliveWindowSelective 排程路径只刷临期窗口内的账号。
func TestKeepaliveWindowSelective(t *testing.T) {
	var n int32
	s, _ := newKeepaliveWindowScheduler(t, &n)

	s.runKeepalive(false, true)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("排程保活刷新 %d 个账号，want 2（窗口内 u2 + 无 expiry u3）", got)
	}

	// 手动入口仍全量：再刷 3 个。
	s.RunKeepaliveNow()
	if got := atomic.LoadInt32(&n); got != 5 {
		t.Fatalf("手动保活后累计 %d 次，want 5（3 个全量）", got)
	}
}

// TestKeepaliveWindowDisabledWhenNonPositive 窗口 <=0 视为不过滤（退化为全量）。
func TestKeepaliveWindowDisabledWhenNonPositive(t *testing.T) {
	var n int32
	s, _ := newKeepaliveWindowScheduler(t, &n)
	s.SetKeepaliveRefreshWindow(-time.Hour)

	s.runKeepalive(false, true)
	if got := atomic.LoadInt32(&n); got != 3 {
		t.Fatalf("窗口 <=0 应全量刷新，实际 %d", got)
	}
}

// TestKeepaliveWindowHotReload 保存配置后的热更新生效。
func TestKeepaliveWindowHotReload(t *testing.T) {
	var n int32
	s, _ := newKeepaliveWindowScheduler(t, &n)
	s.SetKeepaliveRefreshWindow(1 * time.Hour) // 只有 u3（无 expiry）命中

	s.runKeepalive(false, true)
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("窗口 1h 只应刷无 expiry 的 u3，实际 %d", got)
	}
}
