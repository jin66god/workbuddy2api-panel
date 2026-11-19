package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestNoPickForRealmBreakdown 钉住「选不到号」的成因拆解：每个账号必须按 pick 的
// 实际判据顺序归到唯一一类。
//
// 为什么这条要钉：no_healthy_account 背后是四类完全不同的成因（暂停 / 禁用 / 冷却 /
// 在途满），处置分别是「点启用」「重新登录」「等窗口」「提并发」。归类顺序与 pick
// 不一致时，日志和 503 会给出互相矛盾的结论——比不给还糟。
func TestNoPickForRealmBreakdown(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := New("")
	for _, uid := range []string{"paused", "disabled", "cooling", "inflight", "healthy", "tried", "g1"} {
		a := &auth.Auth{UID: uid}
		if uid == "g1" {
			a.Domain = "www.workbuddy.ai" // global 域：realm 过滤用
		}
		p.Add(a)
	}
	p.SetMaxInFlight(1)

	p.mu.Lock()
	p.byUID["paused"].paused = true
	p.byUID["disabled"].disabled = true
	p.byUID["cooling"].until = time.Now().Add(time.Hour)
	p.byUID["inflight"].inFlight.Store(1) // 上限 1 → 已占满
	p.mu.Unlock()

	n := p.NoPickForRealm("", "cn", map[string]bool{"tried": true})
	if n.Total != 6 {
		t.Fatalf("Total = %d want 6（cn 域 6 个号，global 的 g1 不计）", n.Total)
	}
	want := NoPick{Total: 6, Healthy: 1, Paused: 1, Disabled: 1, Cooling: 1, InFlightFull: 1, Tried: 1}
	if n != want {
		t.Fatalf("拆解 = %+v\nwant    %+v", n, want)
	}
	// 单行文案要能直接进日志/响应：字段名与计数都得在。
	s := n.String()
	for _, kv := range []string{"total=6", "healthy=1", "cooling=1", "paused=1", "disabled=1", "in_flight_full=1", "tried=1"} {
		if !strings.Contains(s, kv) {
			t.Errorf("String() 缺 %q：%s", kv, s)
		}
	}
}

// TestNoPickForRealmNilTried nil tried 表示不计轮转（面板/后台巡检调用）。
func TestNoPickForRealmNilTried(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	n := p.NoPickForRealm("", "", nil)
	if n.Total != 1 || n.Tried != 0 || n.Healthy != 1 {
		t.Fatalf("拆解 = %+v want total=1 tried=0 healthy=1", n)
	}
}
