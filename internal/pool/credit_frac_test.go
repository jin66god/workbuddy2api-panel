package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 小额请求（credit<0.5）必须靠余数累加落账：逐笔四舍五入选成 0 时，账面 credits 与
// 到期批次永不减少——面板上就是「一直在用但过期积分没变」。
func TestNoteModelCostAccumulatesFractionalCredits(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.SetCreditsSnapshot("u1", CreditsSnapshot{
		Remain: 100, Total: 100, Expiring: 60,
		EarliestAt: time.Now().Add(24 * time.Hour), EarliestRemaining: 60,
	})

	// 4 笔 0.24 = 0.96：还不满 1 分，账面不动。
	for i := 0; i < 4; i++ {
		p.NoteModelCost("u1", "m", 0.24, 1000)
	}
	if st, _ := p.Status("u1"); st.Credits != 100 {
		t.Fatalf("未攒满 1 分不应扣账，credits=%d", st.Credits)
	}

	// 第 5 笔：累计 1.20 → 扣 1 分，余 0.20。
	p.NoteModelCost("u1", "m", 0.24, 1000)
	st, _ := p.Status("u1")
	if st.Credits != 99 {
		t.Fatalf("攒满 1 分应扣 1，credits=%d", st.Credits)
	}
	if st.CreditsExpiring != 59 {
		t.Fatalf("快过期子集应同步扣减，creditsExpiring=%d", st.CreditsExpiring)
	}

	// 余数不能丢：再 3 笔 0.24 = 0.20+0.72=0.92 仍不动，第 4 笔到 1.16 → 再扣 1。
	for i := 0; i < 3; i++ {
		p.NoteModelCost("u1", "m", 0.24, 1000)
	}
	if st, _ := p.Status("u1"); st.Credits != 99 {
		t.Fatalf("余数应保留，credits=%d", st.Credits)
	}
	p.NoteModelCost("u1", "m", 0.24, 1000)
	if st, _ := p.Status("u1"); st.Credits != 98 {
		t.Fatalf("跨批次余数应继续累加，credits=%d", st.Credits)
	}
}
