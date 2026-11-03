package panel

import (
	"net/http"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestRunAutoAllSkipsNoGrowthAccount 内部全量执行也要有兜底门控：HTTP 入口已按
// SupportsPersonalGrowth 拒绝，但队列与未来新增的内部调用可能绕过入口。企业版 /
// 静态 API Key 的行为事件上游直接 403，跑一遍只会白耗真实对话配额，因此必须在
// 发第一个上游请求之前返回 skipped。
func TestRunAutoAllSkipsNoGrowthAccount(t *testing.T) {
	called := false
	p := panelWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	})
	cases := []*auth.Auth{
		{UID: "u-ent", AccessToken: "at-ent", EnterpriseID: "ent-1"},
		{UID: "u-key", AccessToken: "ck_live_gate_test"},
		nil,
	}
	for _, a := range cases {
		out := p.runAutoAll(a)
		if len(out) != 1 || out[0]["status"] != "skipped" {
			t.Fatalf("account=%v out=%v want 单项 skipped", a, out)
		}
	}
	if called {
		t.Error("无个人成长体系的账号不应发起任何上游请求")
	}
}
