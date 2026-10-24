package server

// 网关 401 的误杀防护：先 refresh 求证，再决定是否计 session dead。
//
// 背景：网关边缘拦截（APISIX/openresty 的 HTML 401/403，无业务信封）与 token 真失效
// 的响应体可能完全一样，单看 401 分不出来。旧逻辑 refresh 失败即计死 → 3 次就把健康
// 号禁用；refresh 端点自己也被边缘拦时同样复发。判据见 refreshFailureIsVerdict。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

const gateway401HTML = "<html><head><title>401 Authorization Required</title></head>" +
	"<body><center><h1>401 Authorization Required</h1></center><hr><center>openresty</center></body></html>"

// gateway401Upstream 按路径区分 chat / refresh：chat 恒回网关 401 HTML，
// refresh 按 mode 应答（ok=瞬时拦截 / blocked=边缘拦截 / reject=应用层判词）。
type gateway401Upstream struct {
	mu       sync.Mutex
	chatN    int
	refreshN int
	mode     string
}

func (u *gateway401Upstream) client() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			isRefresh := strings.Contains(r.URL.Path, "/refresh")
			u.mu.Lock()
			if isRefresh {
				u.refreshN++
			} else {
				u.chatN++
			}
			mode := u.mode
			u.mu.Unlock()

			reply := func(status int, ct, body string) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{ct}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}
			if isRefresh {
				switch mode {
				case "ok":
					return reply(200, "application/json",
						`{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":7200}}`)
				case "blocked":
					return reply(502, "text/html", gateway401HTML)
				default:
					return reply(400, "application/json", `{"error":"invalid_grant"}`)
				}
			}
			return reply(401, "text/html", gateway401HTML)
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

func (u *gateway401Upstream) counts() (chat, refresh int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.chatN, u.refreshN
}

func newGateway401Handler(t *testing.T, u *gateway401Upstream) (*Handler, *auth.Auth) {
	t.Helper()
	a := &auth.Auth{
		UID: "gw1", AccessToken: "at-gw1", RefreshToken: "rt-gw1", ExpiresAt: 9999999999,
		FilePath: filepath.Join(t.TempDir(), "workbuddy-gw1.json"),
	}
	h := NewHandler(Config{Pool: testPoolWith(a), Upstream: u.client(), MaxRotate: 1})
	return h, a
}

// doGateway401Chat 发一次 chat 请求，并清掉软冷却以便下一轮仍能选中该号
// （Cooldown 传 0 时长 = until 立刻过期，等价清冷却）。
func doGateway401Chat(t *testing.T, h *Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))))
	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusTooManyRequests && rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	h.cfg.Pool.Cooldown("gw1", pool.CoolSoft, 0, "")
}

// TestGateway401TransientNotDisabled ⭐ 核心：refresh 成功 → 判为瞬时拦截，
// 不得禁用账号，session dead 计数保持 0，且新 token 落盘。
func TestGateway401TransientNotDisabled(t *testing.T) {
	up := &gateway401Upstream{mode: "ok"}
	h, a := newGateway401Handler(t, up)

	// 跑 5 轮：若仍按旧逻辑计死，第 3 轮就会被禁用。
	for i := 0; i < 5; i++ {
		doGateway401Chat(t, h)
	}

	st, ok := h.cfg.Pool.Status("gw1")
	if !ok {
		t.Fatal("账号应仍在池中")
	}
	if st.Disabled {
		t.Fatalf("refresh 成功时不应禁用账号（5 次 401 后已被禁用）")
	}
	if _, refreshN := up.counts(); refreshN == 0 {
		t.Fatal("未观察到 refresh 请求 —— 401 分支没走求证")
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		t.Fatalf("refresh 成功后应落盘: %v", err)
	}
	if !strings.Contains(string(raw), "new-rt") {
		t.Fatalf("落盘内容应含新 refreshToken，实际 %s", raw)
	}
}

// TestGateway401RefreshBlockedNotDisables refresh 端点也被边缘拦（502 HTML）时，
// 既非成功也非应用层判词 → 不得计死。把「拦截」当「判词」等于换个地方复发误杀。
func TestGateway401RefreshBlockedNotDisables(t *testing.T) {
	up := &gateway401Upstream{mode: "blocked"}
	h, _ := newGateway401Handler(t, up)

	for i := 0; i < 5; i++ {
		doGateway401Chat(t, h)
	}

	st, ok := h.cfg.Pool.Status("gw1")
	if !ok {
		t.Fatal("账号应仍在池中")
	}
	if st.Disabled {
		t.Fatal("refresh 被边缘拦截（非明确判词）时不应禁用账号")
	}
}

// TestGateway401RealDeadStillDisables refresh 被应用层拒绝（400 invalid_grant）→
// token 确实失效，连续达阈值仍应禁用（不因本次修改放过真死号）。
func TestGateway401RealDeadStillDisables(t *testing.T) {
	up := &gateway401Upstream{mode: "reject"}
	h, _ := newGateway401Handler(t, up)

	for i := 0; i < 3; i++ {
		doGateway401Chat(t, h)
	}

	st, ok := h.cfg.Pool.Status("gw1")
	if !ok {
		t.Fatal("账号应仍在池中（禁用不等于删除）")
	}
	if !st.Disabled {
		t.Fatalf("refresh 被明确拒绝时，连续 3 次 401 应禁用账号，实际 disabled=%v", st.Disabled)
	}
}

// TestCapacityEndpoint 覆盖 /v1/capacity：鉴权、缺参、按模型计数、hidden 归零。
func TestCapacityEndpoint(t *testing.T) {
	a1 := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	a2 := &auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999}
	p := testPoolWith(a1, a2)
	p.Disable("u2", "test")
	h := NewHandler(Config{Pool: p, APIKey: "k"})

	// 无鉴权 → 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/capacity?model=cn:glm-5.2", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无鉴权 code=%d want 401", rec.Code)
	}

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/capacity"+query, nil)
		req.Header.Set("Authorization", "Bearer k")
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		return r
	}

	// 缺 model → 400
	if r := get(""); r.Code != http.StatusBadRequest {
		t.Fatalf("缺 model code=%d want 400 body=%s", r.Code, r.Body)
	}

	// 正常：1 个健康号
	r := get("?model=cn:glm-5.2")
	if r.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", r.Code, r.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["model_available_accounts"].(float64) != 1 || got["available_accounts"].(float64) != 1 {
		t.Fatalf("capacity=%v want model=1 pool=1", got)
	}

	// hidden 模型 → model 归零，整池计数不变
	h2 := NewHandler(Config{Pool: p, APIKey: "k", ModelsHidden: []string{"glm-5.2"}})
	req := httptest.NewRequest("GET", "/v1/capacity?model=cn:glm-5.2", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req)
	var got2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode hidden: %v", err)
	}
	if got2["model_available_accounts"].(float64) != 0 {
		t.Fatalf("hidden 模型应报 0，实际 %v", got2)
	}
}
