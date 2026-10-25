package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// ModelBlockStatus.Reason 必须与 Until 出自同一个账号（最早解封那条），
// 否则面板会把 A 号的锁因配上 B 号的解锁时刻。Reasons 保留全量供调用方聚合。
func TestModelBlockedReasonMatchesEarliestUntil(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999})

	now := time.Now()
	late := now.Add(30 * time.Minute)
	early := now.Add(5 * time.Minute)
	// 先写 u1（解封晚），再写 u2（解封早）：旧实现 Reason 取"第一条非空"，
	// 会把 u1 的原因配 u2 的时刻。
	p.CooldownSoftForModel("u1", time.Minute, late, "glm-5.3", "11102 model not found")
	p.CooldownSoftForModel("u2", time.Minute, early, "glm-5.3", `{"code":6004,"msg":"usage limit, resets at 23:00"}`)

	st := p.ModelBlocked("glm-5.3")
	if !st.Blocked {
		t.Fatalf("全池都被该模型挡住，应判 Blocked: %+v", st)
	}
	if st.Count != 2 || len(st.Reasons) != 2 {
		t.Fatalf("Count=%d Reasons=%v want 2/2", st.Count, st.Reasons)
	}
	if got, want := st.Reason, `{"code":6004,"msg":"usage limit, resets at 23:00"}`; got != want {
		t.Errorf("Reason=%q want %q（应与最早解封的 u2 同源）", got, want)
	}
	if d := st.Until.Sub(early); d > time.Second || d < -time.Second {
		t.Errorf("Until=%v want≈%v", st.Until, early)
	}
}
