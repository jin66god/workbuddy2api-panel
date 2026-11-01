package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatQueueWaitingNoRotate429 6020 带 data.retry_after 时：
// 上游只应被调用 1 次（通道级排队，换号必撞同一堵墙），对外 429 + Retry-After=40。
func TestChatQueueWaitingNoRotate429(t *testing.T) {
	const body = `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca3b67934d01c3f64599c8490c","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`
	var calls int32
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return http.StatusTooManyRequests, body, false
	})
	h := NewHandler(Config{
		Pool: testPoolWith(
			&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999},
			&auth.Auth{UID: "q2", AccessToken: "at-q2", ExpiresAt: 9999999999},
		),
		Upstream:  up,
		MaxRotate: 3,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("上游调用 %d 次，want 1（给出 retry_after 时不得换号重打排队请求）", n)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429 (body=%s)", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "40" {
		t.Errorf("Retry-After=%q want 40（按上游给的时长睡，别用本地兜底）", ra)
	}
	if !strings.Contains(rec.Body.String(), "upstream_busy") {
		t.Errorf("body 应保留 upstream_busy 语义: %s", rec.Body)
	}
}

// TestChatQueueFullStill503 6021 无 retry_after：保持 503，不臆造 Retry-After。
func TestChatQueueFullStill503(t *testing.T) {
	const body = `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea3a771e835e388328713d47cf"}`
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusTooManyRequests, body, false
	})
	h := NewHandler(Config{
		Pool:      testPoolWith(&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999}),
		Upstream:  up,
		MaxRotate: 3,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 (body=%s)", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "" {
		t.Errorf("6021 无上游明示时长，不应带 Retry-After，实际 %q", ra)
	}
}
