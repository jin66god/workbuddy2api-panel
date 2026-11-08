// balance_hot_enable_test.go 热启用「余额后台刷新」必须真的启动循环。
//
// 现场形态（用户 2026-11-08 反馈）：面板「积分到期提醒」显示「每 5 分钟后台刷新，
// 1065495620 分钟前更新」——该数字是 Go 零值时间（0001-01-01）算出来的，说明
// RunBalanceRefreshNow 从未成功跑过一轮；而「快过期积分优先」只认 credits_expiring，
// 数据不刷新时勾选等于空转。
package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// 启动时关闭（interval=0）→ 面板里打开开关 → 必须开始刷新。
// 修复前 StartBalanceRefresh 直接 return，SetBalanceInterval 只存了个间隔值，
// 没有任何 goroutine 在跑：面板如实显示「每 5 分钟」，但数据永远不刷新。
func TestBalanceRefreshHotEnableStartsLoop(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 777}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.StartBalanceRefresh(ctx, 0) // 等价 schedule.balance_refresh_enabled=false
	if !s.BalanceRefreshedAt().IsZero() {
		t.Fatalf("关闭状态下不应发生刷新")
	}

	s.SetBalanceInterval(40 * time.Millisecond) // 面板里打开开关

	deadline := time.Now().Add(3 * time.Second)
	for s.BalanceRefreshedAt().IsZero() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.BalanceRefreshedAt().IsZero() {
		t.Fatalf("热启用后余额刷新循环仍未启动（面板会显示每 5 分钟，实际永不刷新）")
	}
	if st, _ := p.Status("u1"); st.Credits != 777 {
		t.Fatalf("刷新应写回余额，credits=%d", st.Credits)
	}
}

// 启动即启用（interval>0）→ 立刻刷一轮，不必等第一个 ticker。
func TestBalanceRefreshStartRunsImmediately(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 321}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartBalanceRefresh(ctx, time.Hour)

	deadline := time.Now().Add(2 * time.Second)
	for s.BalanceRefreshedAt().IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.BalanceRefreshedAt().IsZero() {
		t.Fatalf("启动即启用时应先跑一轮")
	}
	if st, _ := p.Status("u1"); st.Credits != 321 {
		t.Fatalf("credits=%d，期望 321", st.Credits)
	}
}

// 重复调用 StartBalanceRefresh 不得起第二个循环（幂等），否则热重载会叠加刷新。
func TestBalanceRefreshStartIdempotent(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 100}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartBalanceRefresh(ctx, 30*time.Millisecond)
	s.StartBalanceRefresh(ctx, 30*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	if n := f.resourceCalls.Load(); n > 6 {
		t.Fatalf("重复 Start 疑似起了多个循环：%d 次余额请求", n)
	}
}

// 关闭状态下的「任务补领清扫」同样必须能热启用。
func TestTaskSweepHotEnableStartsLoop(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := New(Config{Pool: p, Upstream: &upstream.Client{}})

	var runs int32
	s.SetTaskSweepHook(func() { atomic.AddInt32(&runs, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartTaskSweep(ctx, 0)
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&runs) != 0 {
		t.Fatalf("关闭状态不应执行清扫")
	}
	s.SetTaskSweepInterval(30 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&runs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&runs) == 0 {
		t.Fatalf("热启用后任务补领清扫循环仍未启动")
	}
}
