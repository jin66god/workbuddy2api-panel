package server

import (
	"testing"
	"time"
)

// issue #13 回归：连续首包超时要能累计到阈值，偶发（未达阈）不应触发换号语义。
func TestChatTimeoutStreakCountsAndResets(t *testing.T) {
	t.Parallel()
	c := newChatTimeoutStreak(10 * time.Minute)
	key := "sess-a|uid-1"

	if n := c.note(key); n != 1 {
		t.Fatalf("第 1 次=%d want 1", n)
	}
	if n := c.note(key); n != upstreamTimeoutUnbindStreak {
		t.Fatalf("第 2 次=%d want %d（达到解绑阈值）", n, upstreamTimeoutUnbindStreak)
	}
	// 不同账号是独立的计数：同一会话换号后不该继承旧号的计数
	if n := c.note("sess-a|uid-2"); n != 1 {
		t.Fatalf("另一个账号=%d want 1", n)
	}
	c.reset(key)
	if n := c.note(key); n != 1 {
		t.Fatalf("reset 后=%d want 1", n)
	}
}

// TTL 过期后旧计数作废：用户隔了很久再遇到一次抖动，不该被算成「连续第二次」。
func TestChatTimeoutStreakExpiresByTTL(t *testing.T) {
	t.Parallel()
	c := newChatTimeoutStreak(50 * time.Millisecond)
	key := "sess-b|uid-1"
	c.note(key)
	time.Sleep(80 * time.Millisecond)
	if n := c.note(key); n != 1 {
		t.Fatalf("过期后=%d want 1", n)
	}
}

// nil 安全：handler 在测试里可能没初始化该字段，不允许 panic。
func TestChatTimeoutStreakNilSafe(t *testing.T) {
	t.Parallel()
	var c *chatTimeoutStreak
	if n := c.note("k"); n != 0 {
		t.Fatalf("nil note=%d want 0", n)
	}
	c.reset("k") // 不应 panic
}

// 容量保护：超过上限要淘汰旧项，不能无限增长（长跑进程 + 大量会话）。
func TestChatTimeoutStreakEvictsWhenFull(t *testing.T) {
	t.Parallel()
	c := newChatTimeoutStreak(10 * time.Minute)
	for i := 0; i < c.max+200; i++ {
		c.note(string(rune('a'+i%26)) + time.Now().Format("150405.000000000"))
	}
	c.mu.Lock()
	size := len(c.m)
	c.mu.Unlock()
	if size > c.max+1 {
		t.Fatalf("条目数=%d 超过上限 %d", size, c.max)
	}
}
