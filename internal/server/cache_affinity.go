package server

import (
	"sync"
	"time"
)

// cacheAffinity 记住「某个会话键上一次确实吃到上游前缀缓存的账号」。
//
// 存在理由：上游前缀缓存按账号隔离（同号命中 97–99%、换号 0–12%）。会话粘性在
// 账号级故障（401/403/429/6004 限额/熔断）时会解绑，下一轮请求就被分到另一个号，
// 十万~几十万 token 的前缀全量重算——面板日志里的「大前缀未命中」几乎都是这条路径。
// 粘性本身只保证「成功时别换号」，解绑之后没有记忆；这里补上记忆：解绑后的下一轮
// 优先回到缓存还热着的那个号。
//
// 边界（刻意保守）：
//   - 只在「当前没有粘性绑定」时提供候选，绝不覆盖已有绑定——已有绑定由粘性自身
//     的失败/限额语义管理，这里插一脚会让「限额号换不动」的老问题复活；
//   - 候选号不可用（冷却/占满/跨域/该模型限额）时由既有选号逻辑回落普通轮换，
//     不会把请求钉死在死号上；
//   - 记录只在本进程内，重启即空——缓存亲和是优化，不是正确性依赖。
type cacheAffinity struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
	ttl     time.Duration
	max     int
}

type affinityEntry struct {
	uid string
	at  time.Time
}

// cacheAffinityTTL 亲和记录存活时间：上游前缀缓存的实际 TTL 以小时计，这里取 30m
// 足够覆盖「报错后客户端立刻重试/继续追问」的主场景，又不会长期钉住已过期的缓存。
var cacheAffinityTTL = 30 * time.Minute

// cacheAffinityMax 记录上限，防长跑进程被海量一次性会话撑爆（超出按最旧淘汰）。
var cacheAffinityMax = 4096

// cacheAffinityMinPrompt 只有大前缀值得追踪：小前缀重算的代价可以忽略，记了反而
// 会频繁改写绑定（一个短问答换号本来无害）。
const cacheAffinityMinPrompt = 1000

func newCacheAffinity(ttl time.Duration, max int) *cacheAffinity {
	if ttl <= 0 {
		ttl = cacheAffinityTTL
	}
	if max <= 0 {
		max = cacheAffinityMax
	}
	return &cacheAffinity{entries: make(map[string]affinityEntry), ttl: ttl, max: max}
}

// recall 返回该会话键上次产出过命中的账号；过期即删并返回空串。
func (c *cacheAffinity) recall(key string) string {
	if c == nil || key == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return ""
	}
	if time.Since(e.at) > c.ttl {
		delete(c.entries, key)
		return ""
	}
	return e.uid
}

// remember 记录/续期「该会话键在 uid 上吃到过缓存」。
func (c *cacheAffinity) remember(key, uid string) {
	if c == nil || key == "" || uid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 容量上限：先按过期清理，再按最旧淘汰一条（O(n) 仅在满时发生，n 有上界）。
	if len(c.entries) >= c.max {
		now := time.Now()
		for k, e := range c.entries {
			if now.Sub(e.at) > c.ttl {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			oldestKey, oldest := "", time.Time{}
			for k, e := range c.entries {
				if oldestKey == "" || e.at.Before(oldest) {
					oldestKey, oldest = k, e.at
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[key] = affinityEntry{uid: uid, at: time.Now()}
}

// forget 忘掉该会话的亲和（整段未命中 = 上游缓存已过期，继续钉在这个号上没有收益）。
func (c *cacheAffinity) forget(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// noteCacheAffinity 用一次成功请求的缓存观测更新亲和表。
//
// hit > 0：记住该会话在这个号上吃到过缓存（每轮续期）。
// hit == 0 且大前缀：上游缓存已失效，清掉记录，避免下一轮把请求钉在一个缓存已经
// 凉了的号上，挡住重新分配。
// 小前缀（< cacheAffinityMinPrompt）不参与：重算代价可以忽略，记录只会制造噪声。
func (h *Handler) noteCacheAffinity(bindKey, uid string, hasCache bool, prompt, hit int64) {
	if h == nil || h.cacheAff == nil || bindKey == "" || uid == "" || !hasCache {
		return
	}
	if prompt < cacheAffinityMinPrompt {
		// 小前缀不参与：重算代价可以忽略，记录只会让短问答也去改写绑定。
		return
	}
	if hit > 0 {
		h.cacheAff.remember(bindKey, uid)
		return
	}
	h.cacheAff.forget(bindKey)
}
