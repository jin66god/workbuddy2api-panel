// 窗口用量调度（pool.balance_window）：按 (账号, 模型) 在本窗口内已花的 token
// 反比降权，把热点号打散——避免一个号烧到模型限额而同期其他号闲置。
package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestBalanceWindowAccountsTokens 断言记账只累加窗口内 token、窗口翻篇后归零。
func TestBalanceWindowAccountsTokens(t *testing.T) {
	p := New("")
	p.SetBalanceWindow(time.Hour)
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteModelCost("u1", "hy4-preview", 0.001, 1000)
	p.NoteModelCost("u1", "hy4-preview", 0.001, 500)
	e := p.byUID["u1"]
	if got := e.windowTokens("hy4-preview", time.Hour, time.Now()); got != 1500 {
		t.Fatalf("windowTokens=%d want 1500", got)
	}
	// 另一个模型单独计量（按 账号×模型 记账）。
	if got := e.windowTokens("other-model", time.Hour, time.Now()); got != 0 {
		t.Fatalf("other model windowTokens=%d want 0", got)
	}
	// 窗口翻篇：把起点推到窗口外，读取侧应返回 0（下一次记账会重置）。
	mc := e.modelCost["hy4-preview"]
	mc.WinStart = time.Now().Add(-2 * time.Hour)
	e.modelCost["hy4-preview"] = mc
	if got := e.windowTokens("hy4-preview", time.Hour, time.Now()); got != 0 {
		t.Fatalf("过期窗口 windowTokens=%d want 0", got)
	}
}

// TestBalanceWindowDisabledDoesNotAccount 断言 SetBalanceWindow(0) 关停时完全不记账。
func TestBalanceWindowDisabledDoesNotAccount(t *testing.T) {
	p := New("")
	p.SetBalanceWindow(0)
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteModelCost("u1", "hy4-preview", 0.001, 1000)
	e := p.byUID["u1"]
	if got := e.windowTokens("hy4-preview", time.Hour, time.Now()); got != 0 {
		t.Fatalf("关停后仍记账: windowTokens=%d want 0", got)
	}
}

// TestBalanceWindowDeprioritizesHotAccount 断言同层同权重下，本窗口烧了大量 token
// 的号被降权，抽签明显偏向闲置号。
func TestBalanceWindowDeprioritizesHotAccount(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.SetCostExploreInterval(0)
	p.SetBalanceWindow(time.Hour)
	p.Add(&auth.Auth{UID: "hot"})
	p.Add(&auth.Auth{UID: "cold"})
	p.SetCredits("hot", 100000, 0)
	p.SetCredits("cold", 100000, 0)
	// 两个号同层（实测收费 tier 2），基数权重相同；hot 本窗口已烧 100 万 token。
	p.NoteModelCost("hot", "hy4-preview", 0.001, 1_000_000)
	p.NoteModelCost("cold", "hy4-preview", 0.001, 0)

	hot, cold := 0, 0
	for i := 0; i < 300; i++ {
		a := p.PickExcludingForRealm(nil, "hy4-preview", "")
		if a == nil {
			t.Fatalf("第 %d 次选号返回 nil", i)
		}
		switch a.UID {
		case "hot":
			hot++
		case "cold":
			cold++
		}
	}
	// 理论权重 hot = base/3、cold = base → cold 应占约 3/4。留足余量只断言方向。
	if cold <= hot {
		t.Fatalf("热点号未被降权: hot=%d cold=%d（want cold > hot）", hot, cold)
	}
}

// TestBalanceWindowEqualUsageKeepsBehavior 断言全员零用量时与关闭状态一致
// （除数恒 1，不改变既有权重语义）。
func TestBalanceWindowEqualUsageKeepsBehavior(t *testing.T) {
	p := New("")
	p.SetBalanceWindow(time.Hour)
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	p.SetCredits("a", 1000, 0)
	p.SetCredits("b", 1000, 0)
	e := p.byUID["a"]
	if d := p.usageDiv(e, usageCtx{model: "m", sum: 0, n: 2}, time.Now()); d != 1 {
		t.Fatalf("零用量除数=%v want 1", d)
	}
}
