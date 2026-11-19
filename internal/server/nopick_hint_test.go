package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestNoHealthyAccountHintCarriesNoPick 钉住 503 的 gateway_hint 带成因拆解。
//
// 回归的是「池子里有号、面板上看着全好，却只回一句 no_healthy_account」：用户没法
// 从响应判断该等冷却、恢复暂停、提并发还是调积分保底。禁用号是最稳定的取证形态
// ——它不参与全冷却兜底，必然让 Pick 返回 nil。
func TestNoHealthyAccountHintCarriesNoPick(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "t1", ExpiresAt: 9999999999})
	p.Disable("u1", "12153 session dead")
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		t.Fatal("账号全被挡时不该打上游")
		return 500, "", false
	})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

	rec := httptest.NewRecorder()
	body := `{"model":"cn:m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != 503 {
		t.Fatalf("状态码 = %d want 503；body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no_pick{") {
		t.Fatalf("gateway_hint 缺成因拆解：%s", out)
	}
	for _, kv := range []string{"total=1", "disabled=1"} {
		if !strings.Contains(out, kv) {
			t.Errorf("成因拆解缺 %q：%s", kv, out)
		}
	}
	// 不能因为加了拆解就把原有 code 改掉（客户端按 code 分类）。
	if !strings.Contains(out, `"no_healthy_account"`) {
		t.Errorf("code 变了：%s", out)
	}
}
