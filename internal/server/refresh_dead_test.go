package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// 预刷新失败（refresh token 死）但 access token 仍有效时，chat 必须照常用现有
// AT 打上游：不能禁用账号、也不能换号。生产实测（issue #83）：refresh 恒 12153 的
// 号 AT 还剩几十天，/v2/chat/completions 全 200；旧行为在这一层直接 Disable 掉
// 一个完全健康的账号，面板显示「疑似登录失效」，对话却秒回。
func TestChatRefreshDeadButAccessTokenAliveStillServes(t *testing.T) {
	var chatHits, refreshHits int
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
				refreshHits++
				return &http.Response{
					StatusCode: 401,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(
						`{"code":12153,"msg":"12153:refresh token failed:400 Bad Request: invalid_grant: Session doesn't have required client"}`)),
				}, nil
			}
			chatHits++
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}

	// 距过期 5 分钟：落在 RefreshSkew(10m) 内 → 触发预刷新；但仍 > now，AT 还能用。
	a := &auth.Auth{UID: "u1", AccessToken: "at-u1", RefreshToken: "rt-1",
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	h := NewHandler(Config{Pool: testPoolWith(a), Upstream: up, MaxRotate: 3})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != 200 {
		t.Fatalf("status=%d want 200（AT 有效就该正常服务）: %s", rec.Code, rec.Body.String())
	}
	if refreshHits != 1 {
		t.Errorf("refresh 调用=%d want 1", refreshHits)
	}
	if chatHits != 1 {
		t.Errorf("chat 上游调用=%d want 1（不该因 refresh 失败换号重试）", chatHits)
	}
	if st, _ := h.cfg.Pool.Status("u1"); st.Disabled {
		t.Fatalf("AT 仍有效不该禁用: %+v", st)
	}
}

// AT 已过期时 refresh 判死仍按原语义处置：这类号确实不能干活，必须继续计死
// （不能因为上一条豁免把真正的死号一起放过）。
func TestChatRefreshDeadAndAccessTokenExpiredStillDisabled(t *testing.T) {
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"code":12153,"msg":"12153:refresh token failed: invalid_grant"}`)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	a := &auth.Auth{UID: "u2", AccessToken: "at-u2", RefreshToken: "rt-2",
		ExpiresAt: time.Now().Add(-time.Minute).Unix()}
	h := NewHandler(Config{Pool: testPoolWith(a), Upstream: up, MaxRotate: 3})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`)))

	if st, _ := h.cfg.Pool.Status("u2"); !st.Disabled {
		t.Fatalf("AT 已过期 + refresh 判死应禁用，实际 %+v body=%s", st, rec.Body.String())
	}
}

// 裸模型名默认落 cn 域（resolveModel）：池里只有 global 号时选号必然失败，而固定
// hint 指向的 /status 永远是健康的 —— 客户端据此只会一直重试。noHealthyHint 应把
// 「该怎么写」直接告诉客户端；显式带前缀或另一域也没有号时，一律退回固定文案。
func TestNoHealthyHintPointsAtRealmPrefix(t *testing.T) {
	globalPool := testPoolWith(&auth.Auth{
		UID: "g1", AccessToken: "at-g1", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai",
	})
	h := NewHandler(Config{Pool: globalPool})

	want := `model "glm-5.3" is only available in the global realm; retry with "global:glm-5.3"`
	if got := h.noHealthyHint("glm-5.3", "cn", "glm-5.3"); got != want {
		t.Fatalf("裸名 hint = %q, want %q", got, want)
	}

	// 显式带前缀：前缀不是它失败的原因，建议换域反而误导
	if got := h.noHealthyHint("global:glm-5.3", "global", "glm-5.3"); got != upstream.NoHealthyAccountHint() {
		t.Errorf("显式前缀不应改写 hint，got %q", got)
	}

	// 池里只有 cn 号：另一域无号可服务，不得断言模型在那边可用
	cnPool := testPoolWith(&auth.Auth{UID: "c1", AccessToken: "at-c1", ExpiresAt: 9999999999})
	h2 := NewHandler(Config{Pool: cnPool})
	if got := h2.noHealthyHint("glm-5.3", "cn", "glm-5.3"); got != upstream.NoHealthyAccountHint() {
		t.Errorf("另一域无健康号时应退回固定文案，got %q", got)
	}
}
