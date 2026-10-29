package server

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// assertModelRateCooling 断言「该账号只在 model 上被限流」：
//   - 账号级不冷却（Cooling=false）——普通 429 不再把整号停掉，同号其它模型照常可用；
//   - 该模型有生效中的模型级冷却，剩余时长约等于 want（允许 3s 调度误差）。
func assertModelRateCooling(t *testing.T, p *pool.Pool, uid, model string, want time.Duration) {
	t.Helper()
	st, _ := p.Status(uid)
	if st.Cooling {
		t.Fatalf("%s 不应被账号级冷却（限流按模型隔离）: %+v", uid, st)
	}
	found := false
	for i := range st.RateLimitedModels {
		rm := st.RateLimitedModels[i]
		if rm.Model != model {
			continue
		}
		found = true
		rem := time.Until(rm.Until)
		if rem <= 0 || rem > want+3*time.Second {
			t.Fatalf("%s/%s 模型冷却剩余=%s want ~%s", uid, model, rem, want)
		}
	}
	if !found {
		t.Fatalf("%s 缺 %s 的模型级冷却: %+v", uid, model, st.RateLimitedModels)
	}
}
