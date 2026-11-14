package server

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestCacheAffinityRememberRecallForget(t *testing.T) {
	c := newCacheAffinity(time.Minute, 8)
	if got := c.recall("conv"); got != "" {
		t.Fatalf("空表 recall=%q want 空", got)
	}
	c.remember("conv", "u1")
	if got := c.recall("conv"); got != "u1" {
		t.Fatalf("recall=%q want u1", got)
	}
	c.forget("conv")
	if got := c.recall("conv"); got != "" {
		t.Fatalf("forget 后 recall=%q want 空", got)
	}
}

func TestCacheAffinityExpires(t *testing.T) {
	c := newCacheAffinity(10*time.Millisecond, 8)
	c.remember("conv", "u1")
	time.Sleep(20 * time.Millisecond)
	if got := c.recall("conv"); got != "" {
		t.Fatalf("过期后 recall=%q want 空", got)
	}
	// 过期项应被清出表，而不是只返回空串。
	if len(c.entries) != 0 {
		t.Fatalf("过期项未清理: %v", c.entries)
	}
}

func TestCacheAffinityEvictsOldestAtCap(t *testing.T) {
	c := newCacheAffinity(time.Hour, 2)
	c.remember("a", "u1")
	c.remember("b", "u2")
	c.remember("c", "u3") // 超上限 → 淘汰最旧的 a
	if got := c.recall("a"); got != "" {
		t.Errorf("最旧项未被淘汰: recall(a)=%q", got)
	}
	if c.recall("b") != "u2" || c.recall("c") != "u3" {
		t.Errorf("淘汰错项: b=%q c=%q", c.recall("b"), c.recall("c"))
	}
}

func TestCacheAffinityEmptyKeysIgnored(t *testing.T) {
	c := newCacheAffinity(time.Minute, 4)
	c.remember("", "u1")
	c.remember("conv", "")
	c.forget("")
	if len(c.entries) != 0 {
		t.Fatalf("空键不应写入: %v", c.entries)
	}
	if c.recall("") != "" {
		t.Fatal("空键 recall 应为空")
	}
}

// TestNoteCacheAffinity 断言「命中即记、整段未命中即忘、小前缀不参与」三条语义。
func TestNoteCacheAffinity(t *testing.T) {
	h := &Handler{cacheAff: newCacheAffinity(time.Minute, 8)}
	const key = "conv-1"

	// 小前缀即便命中也不记：短问答换号本身无害，记了只会频繁改写绑定。
	h.noteCacheAffinity(key, "u1", true, 200, 180)
	if got := h.cacheAff.recall(key); got != "" {
		t.Fatalf("小前缀不应记录: %q", got)
	}

	// 大前缀命中 → 记住。
	h.noteCacheAffinity(key, "u1", true, 50000, 49000)
	if got := h.cacheAff.recall(key); got != "u1" {
		t.Fatalf("大前缀命中未记录: %q", got)
	}

	// 大前缀整段未命中 → 忘掉（上游缓存已失效，别再钉在这个号上）。
	h.noteCacheAffinity(key, "u1", true, 50000, 0)
	if got := h.cacheAff.recall(key); got != "" {
		t.Fatalf("整段未命中后仍保留: %q", got)
	}

	// 无观测（hasCache=false）不动表。
	h.noteCacheAffinity(key, "u1", true, 50000, 100)
	h.noteCacheAffinity(key, "u1", false, 0, 0)
	if got := h.cacheAff.recall(key); got != "u1" {
		t.Fatalf("hasCache=false 不应清除已有记录: %q", got)
	}
}

// TestNewHandlerInitializesCacheAffinity 防止「字段忘了初始化」导致的静默失效
// （nil 时 recall 直接返回空，功能看起来正常但完全不生效）。
func TestNewHandlerInitializesCacheAffinity(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})})
	if h.cacheAff == nil {
		t.Fatal("NewHandler 未初始化 cacheAff")
	}
}
