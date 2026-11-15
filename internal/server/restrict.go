package server

import (
	"errors"
	"sync"
	"time"
)

// 上游封号潮的「连坐」暂停：
//
// 单个账号被 11140 限制是账号自己的事（由 pool 连续计数后禁用）；但短时间内同一
// realm 有多个不同账号被限制，说明上游正在成批扫号。此时继续轮转只会用剩余健康号
// 逐个去撞同一堵墙，把整个池子烧穿。这里按 realm 维护一个短时闸门：达到阈值就暂停
// 该域派发，等窗口过去再恢复。
//
// 阈值与暂停时长都必须随规模/次数自适应：
//   - 阈值 = max(2, 本域可用号/10)。固定 2 在小池子合理，在 340 号的池子上等于
//     0.6% 的账号出事就让 100% 的请求失败。
//   - 首次暂停 30 秒，连续触发按 2 倍升到上限 10 分钟；平静 30 分钟没有新限制就
//     清零重来，避免一次历史封号潮让后续每次暂停都停满 10 分钟。
//
// 状态只在进程内、重启清零：暂停的语义是「现在先别打」，重启后重新判定即可。
var (
	restrictionWaveWindow = 15 * time.Minute
	restrictionWaveMin    = 2
	restrictionWaveDiv    = 10
	restrictionHoldBase   = 30 * time.Second
	restrictionHoldMax    = 10 * time.Minute
	restrictionCalmReset  = 30 * time.Minute

	errRestrictionPause = errors.New("upstream restriction wave: dispatch paused")
)

// restrictionThreshold 本域可用号数 → 触发暂停所需的不同账号数。
// healthy<=0（拿不到池子规模）时退回固定下限：未知规模下宁可早停。
func restrictionThreshold(healthy int) int {
	if restrictionWaveDiv <= 0 {
		return restrictionWaveMin
	}
	if n := healthy / restrictionWaveDiv; n > restrictionWaveMin {
		return n
	}
	return restrictionWaveMin
}

type restrictionState struct {
	hits   map[string]time.Time // uid → 最近一次被限制的时刻（窗口内，惰性剪枝）
	until  time.Time            // 该域暂停截止；零值 = 未暂停
	streak int                  // 连续触发次数（决定本次暂停时长）
	last   time.Time            // 最近一次触发时刻（判平静清零）
}

// restrictionGate 按 realm 的封号潮状态机。零值可用。
type restrictionGate struct {
	mu sync.Mutex
	m  map[string]*restrictionState
}

// paused 该域当前是否处于暂停期；返回截止时刻。
func (g *restrictionGate) paused(realm string, now time.Time) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.m[realm]
	if st == nil || st.until.IsZero() {
		return time.Time{}, false
	}
	if !now.Before(st.until) {
		return time.Time{}, false
	}
	return st.until, true
}

// note 记一次账号被上游限制，返回本次触发的暂停时长（0 = 未触发）。
// healthy 是本域当前可用号数，用于缩放阈值。
func (g *restrictionGate) note(realm, uid string, healthy int, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*restrictionState{}
	}
	st := g.m[realm]
	if st == nil {
		st = &restrictionState{hits: map[string]time.Time{}}
		g.m[realm] = st
	}
	for u, t := range st.hits {
		if now.Sub(t) > restrictionWaveWindow {
			delete(st.hits, u)
		}
	}
	// 平静期后重新从基数起：上一波已经结束，不该继续拿旧 streak 加码。
	if !st.last.IsZero() && now.Sub(st.last) > restrictionCalmReset {
		st.streak = 0
	}
	st.hits[uid] = now

	need := restrictionThreshold(healthy)
	if len(st.hits) < need {
		return 0
	}
	st.streak++
	st.last = now
	pause := restrictionHoldBase
	for i := 1; i < st.streak; i++ {
		pause *= 2
		if pause >= restrictionHoldMax {
			break
		}
	}
	if pause > restrictionHoldMax {
		pause = restrictionHoldMax
	}
	st.until = now.Add(pause)
	st.hits = map[string]time.Time{}
	return pause
}
