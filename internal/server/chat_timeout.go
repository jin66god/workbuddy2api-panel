package server

import (
	"sync"
	"time"
)

// upstreamTimeoutUnbindStreak 同一会话在同一账号上连续首包超时多少次后判定「这个号慢」。
//
// 取 2 的原因：偶发抖动（上游一次 GC、一次回源抖动）不该导致会话换号——换号会把
// 几十万 token 的前缀缓存整段丢掉重算。连续两次同一账号超时，才更像「该号被限速」。
const upstreamTimeoutUnbindStreak = 2

// chatTimeoutStreak 记录「会话键 + 账号」的首包超时连续次数。
//
// 背景（issue #13）：上游首包超时按设计「不罚号、不轮转」——全局慢时这是对的，
// 但单账号/单模型的慢（该号被上游限速）与全局慢无法区分，会话就被粘性钉死在同一个
// 号上，每次重试都撞同一个 120s，其余健康号零请求。这里只做计数：连续 N 次才认定
// 「是这个号的问题」，随后解绑粘性与缓存亲和，让下一次请求重新选号。
//
// 进程内状态，带 TTL 与容量上限，重启清零；账号健康度完全不参与（不记连败、不冷却）。
type chatTimeoutStreak struct {
	mu  sync.Mutex
	m   map[string]timeoutStreakEntry
	ttl time.Duration
	max int
}

type timeoutStreakEntry struct {
	count int
	at    time.Time
}

func newChatTimeoutStreak(ttl time.Duration) *chatTimeoutStreak {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &chatTimeoutStreak{m: make(map[string]timeoutStreakEntry), ttl: ttl, max: 4096}
}

// note 记录一次超时并返回该键的连续次数（超过 TTL 的旧计数自动作废）。
func (c *chatTimeoutStreak) note(key string) int {
	if c == nil || key == "" {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e.at.IsZero() || now.Sub(e.at) > c.ttl {
		e = timeoutStreakEntry{}
	}
	e.count++
	e.at = now
	if len(c.m) >= c.max {
		c.evictLocked(now)
	}
	c.m[key] = e
	return e.count
}

// reset 清零某个键（换号成功 / 触发解绑后调用）。
func (c *chatTimeoutStreak) reset(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

// evictLocked 先清过期项，仍满时按最旧时间丢掉一半（容量保护）。
func (c *chatTimeoutStreak) evictLocked(now time.Time) {
	for k, e := range c.m {
		if now.Sub(e.at) > c.ttl {
			delete(c.m, k)
		}
	}
	if len(c.m) < c.max {
		return
	}
	oldest := make([]time.Time, 0)
	cutoff := now
	// 取中位数时间做切分，避免遍历两次
	times := make([]time.Time, 0, len(c.m))
	for _, e := range c.m {
		times = append(times, e.at)
	}
	sortTimes(times)
	cutoff = times[len(times)/2]
	for k, e := range c.m {
		if e.at.Before(cutoff) {
			delete(c.m, k)
		}
	}
	_ = oldest
}

// sortTimes 插入排序（集合很小，标准库 sort 也行，这里避免额外分配）。
func sortTimes(a []time.Time) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j].After(v) {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
