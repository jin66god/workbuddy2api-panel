package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

const cf520HTML = `<!DOCTYPE html><html><head><title>456996.xyz | 520: Web server is returning an unknown error</title></head>
<body><div id="cf-error-details">Error code 520</div></body></html>`

// TestChatCloudflare520FastFailsWithoutPenalty 上游 CF 520 时必须：
//  1. 只打一次上游（不轮转——所有号共用同一入口，换号改变不了回源）；
//  2. 返回 503 + upstream_gateway_error + Retry-After，message 是摘要而非 7KB HTML；
//  3. 不罚号（不冷却、不累计连续失败，否则 3 次就把整池推向熔断 1h）。
func TestChatCloudflare520FastFailsWithoutPenalty(t *testing.T) {
	var hits int32
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		atomic.AddInt32(&hits, 1)
		return http.StatusInternalServerError + 20, cf520HTML, false // 520
	})
	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After=%q want 30", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"gateway_hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body 不是 JSON: %v / %s", err, rec.Body)
	}
	if body.Error.Code != "upstream_gateway_error" {
		t.Fatalf("code=%q body=%s", body.Error.Code, rec.Body)
	}
	if !strings.Contains(body.Error.Message, "Cloudflare 520") || !strings.Contains(body.Error.Message, "host=456996.xyz") {
		t.Fatalf("message 缺摘要: %q", body.Error.Message)
	}
	if strings.Contains(body.Error.Message, "<!DOCTYPE") || strings.Contains(body.Error.Message, "cf-error-details") {
		t.Fatalf("HTML 泄漏进 message: %q", body.Error.Message)
	}
	if !strings.Contains(body.Error.Hint, "not an account problem") {
		t.Fatalf("hint=%q", body.Error.Hint)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits=%d want 1（CF 回源失败不得轮转）", got)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		// NoteError 的痕迹是 ErrTotal / BreakerFails（ConsecutiveFails 是连败降权那套）。
		if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 || st.ConsecutiveFails != 0 {
			t.Fatalf("%s 被误罚: %+v", uid, st)
		}
	}
}

// TestChat502StillPenalized 同一路径下普通 5xx 仍按账号级故障处理（语义不被放宽）。
func TestChat502StillPenalized(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusBadGateway, `{"code":10000,"msg":"boom"}`, false
	})
	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	st, _ := p.Status("a1")
	if st.ErrTotal == 0 && st.BreakerFails == 0 {
		t.Fatalf("普通 502 应喂失败计数: %+v (status=%d)", st, rec.Code)
	}
}
