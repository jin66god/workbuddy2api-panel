package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func hasUID(list []string, uid string) bool {
	for _, u := range list {
		if u == uid {
			return true
		}
	}
	return false
}

// 新会话分配：快过期子集非空时只分配快过期号；lookup 口径仍保留普通号（避免会话抖动）。
func TestStickyAssignPrefersExpiringButLookupDoesNot(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "plain"})
	p.Add(&auth.Auth{UID: "soon"})
	now := time.Now()
	p.SetCreditsDetailed("plain", 100, 100, 0, time.Time{}, 0)
	p.SetCreditsDetailed("soon", 100, 100, 50, now.Add(time.Hour), 50)

	assign := p.StickyAssignableForModelRealm("", "")
	if len(assign) == 0 || hasUID(assign, "plain") {
		t.Fatalf("assign 应只含快过期号, got=%v", assign)
	}
	if !hasUID(assign, "soon") {
		t.Fatalf("assign 缺 soon: %v", assign)
	}

	lookup := p.WeightedAvailableUIDsForModelRealm("", "")
	if !hasUID(lookup, "plain") || !hasUID(lookup, "soon") {
		t.Fatalf("lookup 口径应同时包含普通号与快过期号, got=%v", lookup)
	}
}

// 快过期子集为空：分配回落全量。
func TestStickyAssignFallsBackWhenNoExpiring(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	p.SetCreditsDetailed("a", 100, 100, 0, time.Time{}, 0)
	p.SetCreditsDetailed("b", 100, 100, 0, time.Time{}, 0)
	got := p.StickyAssignableForModelRealm("", "")
	if !hasUID(got, "a") || !hasUID(got, "b") {
		t.Fatalf("应回落全量, got=%v", got)
	}
}

// 积分保底：触底号不进入粘性分配与 lookup 两条路径。
func TestStickyFiltersCreditFloor(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "rich"})
	p.Add(&auth.Auth{UID: "low"})
	p.SetCreditsDetailed("rich", 500, 500, 0, time.Time{}, 0)
	p.SetCreditsDetailed("low", 20, 100, 20, time.Now().Add(time.Hour), 20)
	p.SetCreditFloor(100)
	p.NoteModelCost("rich", "m1", 5, 1000)
	p.NoteModelCost("low", "m1", 5, 1000)

	for name, list := range map[string][]string{
		"assign": p.StickyAssignableForModelRealm("m1", ""),
		"lookup": p.WeightedAvailableUIDsForModelRealm("m1", ""),
	} {
		if hasUID(list, "low") {
			t.Fatalf("%s 不应包含触底号 low: %v", name, list)
		}
		if !hasUID(list, "rich") {
			t.Fatalf("%s 应包含 rich: %v", name, list)
		}
	}
	if strings.Contains(strings.Join(p.StickyAssignableForModelRealm("m1", ""), ","), "low") {
		t.Fatal("assign 仍含 low")
	}
}
