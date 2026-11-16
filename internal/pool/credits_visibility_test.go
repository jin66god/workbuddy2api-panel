package pool

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestDisabledAccountReportsZeroCredits 钉住对外积分语义的分离：
//
//   - credits     = 当前**可用量**，禁用号必须为 0（封号后余额是冻结快照，
//     把死钱算进可用额度会让面板合计与外部消费方全部基于死数据）；
//   - credits_raw = 真实快照，必须保留（调度器据此判断「下一个该启用谁」）；
//   - 暂停号不受影响：它照常保号、余额仍刷新，只是暂不接流量，积分是真的。
//
// 变异验证：把 activeCredits 改回直接返回 v，本用例的 disabled 分支会转红。
func TestDisabledAccountReportsZeroCredits(t *testing.T) {
	p := &Pool{byUID: map[string]*entry{}}
	cases := []struct {
		name     string
		disabled bool
		paused   bool
		want     int64
	}{
		{"healthy", false, false, 4552},
		{"paused", false, true, 4552},
		{"disabled", true, false, 0},
	}
	for _, tc := range cases {
		e := &entry{
			a:            &auth.Auth{UID: "u1"},
			credits:      4552,
			creditsTotal: 6000,
			disabled:     tc.disabled,
			paused:       tc.paused,
		}
		st := p.statusOf("u1", e)
		if st.Credits != tc.want {
			t.Errorf("%s: Credits=%d want %d", tc.name, st.Credits, tc.want)
		}
		if st.CreditsRaw != 4552 {
			t.Errorf("%s: CreditsRaw=%d want 4552（快照不能丢，轮换判断要用）", tc.name, st.CreditsRaw)
		}
		if tc.disabled && st.CreditsTotal != 0 {
			t.Errorf("%s: CreditsTotal=%d want 0（总额度同属可用量口径）", tc.name, st.CreditsTotal)
		}
	}

	if got := activeCredits(100, true); got != 0 {
		t.Errorf("activeCredits(100,true)=%d want 0", got)
	}
	if got := activeCredits(100, false); got != 100 {
		t.Errorf("activeCredits(100,false)=%d want 100", got)
	}
}
