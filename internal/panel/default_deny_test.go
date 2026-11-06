package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// 回归钉：鉴权在 ServeHTTP 层默认拒绝。
// 此前每条路由各自包 withAuth，新增路由忘包一次就是静默公开——本测试用
// 「未注册路径」证明闸门不依赖路由表：即使没人给它包 withAuth，也必须 401。
func TestDefaultDenyCoversUnregisteredPaths(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New("")})
	for _, path := range []string{
		"/panel/api/overview",       // 已注册
		"/panel/api/not-registered", // 未注册：不能因为 404 就先泄露「这个接口不存在」
		"/panel/api/",               // 前缀
		"/panel/api/accounts/u1/checkin",
	} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 无密钥应 401，实际 %d", path, rec.Code)
		}
	}
}

// 页面骨架与静态资源保持匿名可访问，否则面板连登录框都打不开。
func TestStaticAssetsStayPublic(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New("")})
	for _, path := range []string{"/panel/", "/panel/app.js", "/panel/mobile.css", "/panel/mobile.js"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s 是静态资源，不应被鉴权拦下", path)
		}
	}
}

// 带正确密钥的请求必须照常通过闸门（不能因为加了默认拒绝而把正常调用也拦掉）。
func TestAuthorizedRequestPassesGate(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New("")})
	req := httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("正确密钥仍被拒: %d %s", rec.Code, rec.Body.String())
	}
}
