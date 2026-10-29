package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFallbackHonorsModelCooldown 全冷却兜底必须遵守模型级冷却。
//
// 回归背景：模型级冷却（6004 / 手动模型停牌）只写 modelCooldowns，不写账号级 until；
// 而 pickEarliestExpiryLocked 早期只看 coolKind/until，于是「刚被 6004 停车的号」会被
// 兜底路径立刻选中，再打一次必然 429 的请求，同时把手动停牌也绕过去。
func TestFallbackHonorsModelCooldown(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	reset := time.Now().Add(30 * time.Minute)
	p.CooldownSoftForModel("u1", time.Minute, reset, "kimi-k3.1-pro", `{"code":6004}`)

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

	if got := p.PickExcludingForModel(nil, "glm-5.3"); got != nil {
		t.Fatalf("兜底绕过了手动模型停牌: %+v", got)
	}
}
