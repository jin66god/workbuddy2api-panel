package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFallbackHonorsModelCooldown 全冷却兜底必须遵守模型级冷却。
//
// 回归背景：模型级冷却（6004 / 手动模型停牌）只写 modelCooldowns，不写账号级 until；
// 而 pickEarliestExpiryLocked 早期只看 coolKind/until，于是「账号另有降权/熔断、该模型
// 仍在冷却」的号会被兜底路径选中，再打一次必然 429 的请求，同时把手动停牌也绕过去。
//
// 注意必须让账号另有非零 expiry 才会进入兜底候选：只挂模型冷却时 expiry 为空，
// 旧代码也不选——那种场景测不出这个 bug（会变成空转假绿）。
func TestFallbackHonorsModelCooldown(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.CooldownSoftForModel("u1", time.Minute, time.Now().Add(30*time.Minute), "kimi-k3.1-pro", `{"code":6004}`)
	p.mu.Lock()
	p.byUID["u1"].degradeUntil = time.Now().Add(10 * time.Minute)
	p.mu.Unlock()

	if got := p.PickExcludingForModel(nil, "kimi-k3.1-pro"); got != nil {
		t.Fatalf("兜底绕过了模型级冷却: %+v", got)
	}
	if got := p.PickExcludingForModel(nil, "deepseek-v4.1-flash"); got == nil {
		t.Fatal("其它模型应照常可选")
	}
}

// TestFallbackHonorsManualModelBlock 手动模型停牌同样不能被兜底绕过。
func TestFallbackHonorsManualModelBlock(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.BlockModelBackoff("u1", "glm-5.3", "上游称该模型不存在")
	p.mu.Lock()
	p.byUID["u1"].degradeUntil = time.Now().Add(10 * time.Minute)
	p.mu.Unlock()

	if got := p.PickExcludingForModel(nil, "glm-5.3"); got != nil {
		t.Fatalf("兜底绕过了手动模型停牌: %+v", got)
	}
}

// TestCooldownSoftForModelWithoutResetIsModelScoped 无重置时刻的普通 429 也只停模型：
// 不得写账号级 until（旧实现会把整号停 base 且按 softStreak 指数翻倍）。
func TestCooldownSoftForModelWithoutResetIsModelScoped(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.CooldownSoftForModel("u1", time.Minute, time.Time{}, "kimi-k3.1-pro", "429 model rate limit")

	st, _ := p.Status("u1")
	if st.Cooling {
		t.Fatalf("账号级不应冷却: %+v", st)
	}
	if len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Model != "kimi-k3.1-pro" {
		t.Fatalf("缺模型级冷却: %+v", st.RateLimitedModels)
	}
	// 并发失败不延长已生效的冷却。
	first := st.RateLimitedModels[0].Until
	p.CooldownSoftForModel("u1", time.Hour, time.Time{}, "kimi-k3.1-pro", "429 model rate limit")
	st2, _ := p.Status("u1")
	if !st2.RateLimitedModels[0].Until.Equal(first) {
		t.Fatalf("重复失败延长了冷却: %s -> %s", first, st2.RateLimitedModels[0].Until)
	}
}

// TestCooldownSoftForModelEmptyModelFallsBack 无模型上下文时必须回落账号级，
// 不能写 modelCooldowns[""]（空键永远匹配不上请求模型 = 等于没冷却）。
func TestCooldownSoftForModelEmptyModelFallsBack(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.CooldownSoftForModel("u1", time.Minute, time.Time{}, "", "429 model rate limit")
	st, _ := p.Status("u1")
	if !st.Cooling {
		t.Fatalf("空 model 应回落账号级软冷却: %+v", st)
	}
	if len(st.RateLimitedModels) != 0 {
		t.Fatalf("不应写空模型条目: %+v", st.RateLimitedModels)
	}
}
