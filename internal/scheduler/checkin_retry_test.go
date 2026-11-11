package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// retryFixture 构造一个只服务签到端点的调度器：计费 meter 路径打同一个 httptest server。
func retryFixture(t *testing.T, handler http.HandlerFunc) (*Scheduler, *pool.Pool, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	p := pool.New("")
	a := &auth.Auth{UID: "u1", Nickname: "nick", AccessToken: "t", RefreshToken: "rt", ExpiresAt: 9999999999}
	p.Add(a)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), p, &calls
}

// TestCheckinRetrySucceeds 补签档位到点后真正重签，并把 checkin_done 落库。
func TestCheckinRetrySucceeds(t *testing.T) {
	old := checkinRetryDelays
	checkinRetryDelays = []time.Duration{10 * time.Millisecond}
	defer func() { checkinRetryDelays = old }()

	s, p, calls := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	s.runCheckinRetry(context.Background(), []string{"u1"})
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("补签应打 1 次上游，got %d", got)
	}
	if !p.CheckinDoneToday("u1") {
		t.Fatal("补签成功后 checkin_done 应落库")
	}
}

// TestCheckinRetrySkipsDone 已签账号（手机端 / 上一档 / 手动触发）不再打上游。
func TestCheckinRetrySkipsDone(t *testing.T) {
	old := checkinRetryDelays
	checkinRetryDelays = []time.Duration{10 * time.Millisecond}
	defer func() { checkinRetryDelays = old }()

	s, p, calls := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	p.NoteCheckinDone("u1")
	s.runCheckinRetry(context.Background(), []string{"u1"})
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("已签账号不该补签，上游调用 %d 次", got)
	}
}

// TestCheckinRetryStopsOnCancel ctx 取消（优雅停机 / 重排）立即退出，且不打上游。
func TestCheckinRetryStopsOnCancel(t *testing.T) {
	old := checkinRetryDelays
	checkinRetryDelays = []time.Duration{5 * time.Second} // 远大于取消窗口
	defer func() { checkinRetryDelays = old }()

	s, _, calls := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.runCheckinRetry(ctx, []string{"u1"}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后补签应立即退出")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("取消后不该打上游，got %d", got)
	}
}

// TestCheckinOneBusinessErrorNotRetried 业务拒绝（400）不进补签：done=false 且
// IsTransientBilling=false，调用方据此不排（避免每天多打无效请求）。
func TestCheckinOneBusinessErrorNotRetried(t *testing.T) {
	s, _, _ := retryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":10001,"msg":"企业账号不支持该操作"}`))
	})
	a := s.cfg.Pool.AuthByUID("u1")
	done, err := s.checkinOne(a, "u1", "nick")
	if done || err == nil {
		t.Fatalf("业务拒绝应 done=false 且 err!=nil: done=%v err=%v", done, err)
	}
	if upstream.IsTransientBilling(err) {
		t.Fatal("业务拒绝不该判为瞬时（否则会被排进补签队列）")
	}
}
