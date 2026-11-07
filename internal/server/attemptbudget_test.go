package server

import (
	"context"
	"testing"
	"time"
)

// 无客户端 deadline：不设上限，行为与旧版完全一致（长生成不被掐）。
func TestAttemptBudgetNoDeadline(t *testing.T) {
	budget, ok := attemptBudgetFor(context.Background())
	if !ok || budget != 0 {
		t.Fatalf("budget=%v ok=%v, want 0/true", budget, ok)
	}
}

// 有 deadline：预算 = remaining - margin，且必须小于 remaining（留出写回错误的时间）。
func TestAttemptBudgetShrinksToRemaining(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	budget, ok := attemptBudgetFor(ctx)
	if !ok {
		t.Fatal("60s 预算应足够")
	}
	if budget <= 0 || budget >= 60*time.Second {
		t.Fatalf("budget=%v, want 0<budget<60s", budget)
	}
	if want := 60*time.Second - attemptDeadlineMargin; budget > want {
		t.Fatalf("budget=%v 超过 remaining-margin=%v", budget, want)
	}
}

// 剩余不足：不发新一跳（末端 504），而不是发出去再被客户端取消。
func TestAttemptBudgetExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if _, ok := attemptBudgetFor(ctx); ok {
		t.Fatal("预算耗尽时应返回 ok=false")
	}
}
