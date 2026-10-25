package scheduler

import (
	"testing"
	"time"
)

func TestJitterFracStableAndBounded(t *testing.T) {
	setJitterSeed(12345)
	defer setJitterSeed(0)
	a := jitterFrac("bal|uid-1")
	b := jitterFrac("bal|uid-1")
	if a != b {
		t.Fatalf("same key must be stable: %v vs %v", a, b)
	}
	if a < 0 || a >= 1 {
		t.Fatalf("frac %v outside [0,1)", a)
	}
	distinct := 0
	prev := a
	for _, key := range []string{"bal|u1", "bal|u2", "bal|u3", "sweep|x", "sweep|y"} {
		f := jitterFrac(key)
		if f != prev {
			distinct++
		}
		prev = f
	}
	if distinct < 3 {
		t.Fatalf("expected spread across keys, got %d distinct of 5", distinct)
	}
	// 种子变化 → 同 key 结果变化（重启换新抖动相位）。
	setJitterSeed(999)
	if jitterFrac("bal|uid-1") == a {
		t.Fatal("new seed should change offset")
	}
}

func TestPeriodicJitter(t *testing.T) {
	interval := 5 * time.Minute
	if got := periodicJitter(interval, 0, "k"); got != 0 {
		t.Fatalf("frac 0 must be no-op, got %v", got)
	}
	if got := periodicJitter(0, 0.2, "k"); got != 0 {
		t.Fatalf("zero interval must be no-op, got %v", got)
	}
	setJitterSeed(42)
	defer setJitterSeed(0)
	for range 50 {
		d := periodicJitter(interval, 0.2, "k")
		if d < -time.Minute || d > time.Minute { // ±20% of 5m = ±1m
			t.Fatalf("jitter %v outside ±1m", d)
		}
	}
}

func TestBalanceRefreshStagger(t *testing.T) {
	if got := spreadIntervalStagger(5*time.Minute, 0); got != 0 {
		t.Fatalf("frac 0 must disable stagger, got %v", got)
	}
	setJitterSeed(7)
	defer setJitterSeed(0)
	window := spreadIntervalStagger(5*time.Minute, 0.2) // 1m
	seen := map[time.Duration]bool{}
	for _, uid := range []string{"a", "b", "c", "d", "e"} {
		d := spreadStaggerDelay(window, uid)
		if d < 0 || d >= window {
			t.Fatalf("uid %s delay %v outside [0,%v)", uid, d, window)
		}
		seen[d] = true
	}
	if len(seen) < 4 {
		t.Fatalf("expected spread across uids, got %d distinct of 5", len(seen))
	}
}

// 抖动关闭时余额刷新不等待（旧行为：全账号并发齐射）。
func TestRunBalanceRefreshNowNoJitterNoWait(t *testing.T) {
	// stagger 上界为 0 → spreadStaggerDelay 恒 0；不触上游（Pool/Auth 为测试桩场景
	// 已在其他覆盖），这里只验窗口计算。
	if got := spreadIntervalStagger(time.Minute, 0); got != 0 {
		t.Fatalf("got %v", got)
	}
}
