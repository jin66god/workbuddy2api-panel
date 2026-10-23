package scheduler

import (
	"sort"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// issue #45：偏移必须当天确定性——同一天重启后计划不变，否则重启即重排。
func TestSpreadPlanDeterministic(t *testing.T) {
	s := &Scheduler{cfg: Config{AccountSpreadMinutes: 60, AccountSpreadMinGapSeconds: 60}}
	uids := []string{"a", "b", "c", "d", "e"}
	p1 := s.planSpread(taskCheckin, uids)
	p2 := s.planSpread(taskCheckin, uids)
	if p1 == nil || p2 == nil {
		t.Fatal("窗口开启时不应返回 nil")
	}
	for _, u := range uids {
		if p1.offsets[u] != p2.offsets[u] {
			t.Fatalf("同一天两次计划不一致: %s %v vs %v", u, p1.offsets[u], p2.offsets[u])
		}
		if p1.offsets[u] < 0 || p1.offsets[u] > 60*time.Minute {
			t.Fatalf("偏移越界: %s %v", u, p1.offsets[u])
		}
	}
	// 最小间隔：排序后相邻不得低于 minGap（随机 + 冲突推开，不是等距）。
	offs := make([]time.Duration, 0, len(uids))
	for _, u := range uids {
		offs = append(offs, p1.offsets[u])
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	for i := 1; i < len(offs); i++ {
		if offs[i]-offs[i-1] < 60*time.Second {
			t.Fatalf("相邻间隔小于下限: %v -> %v", offs[i-1], offs[i])
		}
	}
}

// 不同任务用不同盐：签到与旅行的偏移不应完全重合（否则同一账号两类任务
// 仍在同一时刻连打）。
func TestSpreadPlanKindSeparated(t *testing.T) {
	s := &Scheduler{cfg: Config{AccountSpreadMinutes: 60}}
	uids := []string{"a"}
	c := s.planSpread(taskCheckin, uids).offsets["a"]
	tr := s.planSpread(taskTravel, uids).offsets["a"]
	if c == tr {
		t.Fatalf("不同任务的偏移完全重合: %v", c)
	}
}

// 窗口 0 = 关闭，保持整点立即执行。
func TestSpreadPlanDisabled(t *testing.T) {
	s := &Scheduler{}
	if p := s.planSpread(taskCheckin, []string{"a"}); p != nil {
		t.Fatalf("窗口 0 应返回 nil: %+v", p)
	}
	var nilPlan *spreadPlan
	nilPlan.wait("a") // 不应 panic / 阻塞
}

// issue #45：遍历必须按计划时刻升序，否则 uid 序里靠前的账号计划更晚时会把后面
// 的账号逐个堵住，最后挤在同一秒跑完（10 个账号塌缩成 2–3 个时刻）。
func TestSpreadPlanOrderByOffset(t *testing.T) {
	p := &spreadPlan{offsets: map[string]time.Duration{
		"a": 30 * time.Minute,
		"b": 1 * time.Minute,
		"c": 10 * time.Minute,
	}}
	list := []pool.Status{{UID: "a"}, {UID: "b"}, {UID: "c"}}
	got := p.order(list)
	want := []string{"b", "c", "a"}
	for i, u := range want {
		if got[i].UID != u {
			t.Fatalf("order=%v want %v", []string{got[0].UID, got[1].UID, got[2].UID}, want)
		}
	}
	// 原列表不能被就地改写（调用方仍按 uid 序做别的事）。
	if list[0].UID != "a" || list[1].UID != "b" {
		t.Fatalf("order 修改了入参: %v", list)
	}
	// 未启用摊开时原样返回。
	var nilPlan *spreadPlan
	if out := nilPlan.order(list); len(out) != len(list) || out[0].UID != "a" {
		t.Fatalf("nil plan 应原样返回: %v", out)
	}
}
