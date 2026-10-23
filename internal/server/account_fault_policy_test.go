package server

import (
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// issue #231 / PR #233 回归：11-128 通道校验失败（msg 同为 "request illegal"）
// 是**请求级**错误——上游 displayTips 明示「可能不是官方客户端发出的」，账号本身
// 健康（官方客户端可用同一账号）。处置必须与 11140 账号限制相反：
// 不冷却、不熔断、不累计错误计数，由 chat 循环直接透传 400 终止轮转。
// 历史实现按裸文案判 accountFault → 软冷却，全池每号撞一次就被移出 600s，
// 表象就是 issue #231「国际版账号全部被禁用」。
func TestApplyErrorPolicyChannelRefusedNoPenalty(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p, SoftCooldown: 600 * time.Second})

	body := `{"code":"11-128","msg":"request illegal","displayMsg":{"zh":"请求被拦截，请重新发送"},"displayTips":{"zh":"渠道校验失败，可能不是从官方客户端发出的，请用官方客户端重新发送"}}`
	if got := upstream.Classify(400, body); got != upstream.ErrChannelRefused {
		t.Fatalf("Classify=%v want ErrChannelRefused", got)
	}
	h.applyErrorPolicy("u1", upstream.ErrChannelRefused, body, "", nil)

	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("账号不在池中")
	}
	if st.Disabled {
		t.Fatalf("11-128 不应禁用账号: %+v", st)
	}
	if st.Cooling {
		t.Fatalf("11-128 不应冷却账号（请求级错误）: %+v", st)
	}
	if st.ErrTotal != 0 {
		t.Fatalf("11-128 不应累计错误计数: err_total=%d", st.ErrTotal)
	}
}

// 11140 账号限制：单次不禁用（可能是上游瞬时风控误判），连续 3 次才硬禁用，
// 且 reason 必须写明「在 WorkBuddy 提交反馈申诉」（重登无效，不能误导用户）。
func TestApplyErrorPolicyAccountRestrictedDisablesAfterThreshold(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p, SoftCooldown: 600 * time.Second})
	body := `{"code":11140,"msg":"request illegal"}`

	for i := 1; i < pool.AccountRestrictedThreshold(); i++ {
		h.applyErrorPolicy("u1", upstream.ErrAccountFault, body, "", nil)
		st, _ := p.Status("u1")
		if st.Disabled {
			t.Fatalf("第 %d 次 11140 不应禁用（未达阈值 %d）: %+v", i, pool.AccountRestrictedThreshold(), st)
		}
	}
	if pool.AccountRestrictedThreshold() != 3 {
		t.Fatalf("阈值=%d want 3", pool.AccountRestrictedThreshold())
	}
	// 第 3 次达阈 → 禁用。
	h.applyErrorPolicy("u1", upstream.ErrAccountFault, body, "", nil)
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("连续 %d 次 11140 应禁用: %+v", pool.AccountRestrictedThreshold(), st)
	}
	if !strings.Contains(st.Reason, "appeal in WorkBuddy") {
		t.Errorf("禁用 reason 应提示申诉路径, got %q", st.Reason)
	}
}

// 11140 的模型级限流文案不是账号限制：必须留在 ErrSoftRate（短冷却），
// 不能连续累计到禁用。防止后续把 IsAccountRestricted 放宽成「见 11140 即禁用」。
func TestClassifyAccountRestrictedExcludesRateLimitVariant(t *testing.T) {
	body := `{"code":11140,"msg":"The model provider is rate-limiting requests."}`
	if got := upstream.Classify(400, body); got != upstream.ErrSoftRate {
		t.Fatalf("Classify=%v want ErrSoftRate（11140 模型级限流变体）", got)
	}
	if upstream.IsAccountRestricted(body) {
		t.Fatal("模型级限流变体不应被判成账号限制")
	}
}

// 同一分支的 14017（trial not activated）仍走软冷却——补完 register 后自愈，
// 禁用会让用户补完后仍无法使用。此测试防止后续改动把两条子分支合并成 Disable。
func TestApplyErrorPolicyTrialNotActivatedStaysSoft(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p, SoftCooldown: 600 * time.Second})

	h.applyErrorPolicy("u1", upstream.ErrAccountFault, `{"code":14017,"msg":"trial not activated"}`, "", nil)

	st, _ := p.Status("u1")
	if st.Disabled {
		t.Fatalf("14017 不应永久禁用账号: %+v", st)
	}
	if !st.Cooling || st.CoolKind != "soft_rate" {
		t.Fatalf("14017 应为 soft_rate 冷却: %+v", st)
	}
}
