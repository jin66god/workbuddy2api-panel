package server

import (
	"testing"
	"time"
)

func TestRestrictionThresholdScalesWithHealthy(t *testing.T) {
	cases := []struct {
		healthy int
		want    int
	}{
		{0, 2}, {5, 2}, {20, 2}, {100, 10}, {340, 34},
	}
	for _, c := range cases {
		if got := restrictionThreshold(c.healthy); got != c.want {
			t.Errorf("restrictionThreshold(%d) = %d, want %d", c.healthy, got, c.want)
		}
	}
}

func TestRestrictionGateCountsDistinctAccountsOnly(t *testing.T) {
	g := &restrictionGate{}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if pause := g.note("cn", "same-uid", 20, now); pause != 0 {
			t.Fatalf("同一账号重复命中触发了暂停：%s", pause)
		}
	}
	if _, paused := g.paused("cn", now); paused {
		t.Fatal("同一账号不应触发连坐暂停")
	}
}

func TestRestrictionGateTriggersBacksOffAndResets(t *testing.T) {
	g := &restrictionGate{}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	// healthy=20 → 阈值 2：两个不同账号即触发。
	if pause := g.note("cn", "u1", 20, now); pause != 0 {
		t.Fatalf("first hit pause = %s, want 0", pause)
	}
	if pause := g.note("cn", "u2", 20, now); pause != restrictionHoldBase {
		t.Fatalf("first wave pause = %s, want %s", pause, restrictionHoldBase)
	}
	if until, paused := g.paused("cn", now.Add(10*time.Second)); !paused || until.IsZero() {
		t.Fatal("暂停期内 paused() 应为 true")
	}
	if _, paused := g.paused("cn", now.Add(restrictionHoldBase+time.Second)); paused {
		t.Fatal("暂停到期后 paused() 应为 false")
	}

	// 连续触发按 2 倍退避：30s → 1m → 2m …封顶 10m。
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, restrictionHoldMax}
	for i, w := range want {
		if p := g.note("cn", "a", 20, now); p != 0 {
			t.Fatalf("wave %d first hit pause = %s, want 0", i, p)
		}
		if p := g.note("cn", "b", 20, now); p != w {
			t.Fatalf("wave %d pause = %s, want %s", i, p, w)
		}
	}

	// 平静期后清零：间隔超过 calmReset，下一次重新从 30s 起。
	calm := now.Add(restrictionCalmReset + time.Hour)
	g.note("cn", "c", 20, calm)
	if p := g.note("cn", "d", 20, calm); p != restrictionHoldBase {
		t.Fatalf("calm reset pause = %s, want %s", p, restrictionHoldBase)
	}
}
