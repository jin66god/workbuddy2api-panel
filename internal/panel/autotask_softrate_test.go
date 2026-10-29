package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// fastSoftRate 把 429 重试预算压到毫秒级，避免测试真的睡 15~60s。
func fastSoftRate(t *testing.T) {
	t.Helper()
	oldGap, oldRetries, oldMax := softRateGap, softRateRetries, softRateGapMax
	softRateGap, softRateRetries, softRateGapMax = time.Millisecond, 2, 2*time.Millisecond
	t.Cleanup(func() { softRateGap, softRateRetries, softRateGapMax = oldGap, oldRetries, oldMax })
}

// TestListTasksRetriesSoftRateThenSucceeds 拉任务列表撞两次 429 后成功：
// 一轮全量任务不再因为一次频控就断在「查询任务失败」。
func TestListTasksRetriesSoftRateThenSucceeds(t *testing.T) {
	fastSoftRate(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":1,"msg":"rate limit"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"tasks":[{"task_code":"chat_5","title":"t","target":5,"current":0,"accept_status":""}]}}`))
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
	}}}
	task, err := p.taskByCode(&auth.Auth{AccessToken: "at", UID: "u1"}, "chat_5")
	if err != nil {
		t.Fatalf("两次 429 后应重试成功: %v", err)
	}
	if task == nil || task.TaskCode != "chat_5" {
		t.Fatalf("task=%+v", task)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls=%d want 3（首次 + 2 次重试）", got)
	}
}

// TestListTasksSoftRateGivesUpAndFastFails 重试用尽后：
//   - 错误文案带「已重试 N 次」与剩余时间；
//   - 窗口内再查直接快失败，不再打上游（否则每个动作各睡一遍，作业卡几十分钟）。
func TestListTasksSoftRateGivesUpAndFastFails(t *testing.T) {
	fastSoftRate(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":1,"msg":"rate limit"}`))
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
	}}}
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	_, err := p.taskByCode(a, "chat_5")
	if err == nil || !strings.Contains(err.Error(), "已重试 2 次") {
		t.Fatalf("err=%v want 重试用尽文案", err)
	}
	before := atomic.LoadInt32(&calls)
	if before != 3 {
		t.Fatalf("calls=%d want 3（首次 + 2 次重试）", before)
	}
	if _, err2 := p.taskByCode(a, "chat_5"); err2 == nil || !strings.Contains(err2.Error(), "后恢复") {
		t.Fatalf("窗口内应快失败并给剩余时间: %v", err2)
	}
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatalf("窗口内又打了上游: calls=%d want %d", got, before)
	}
}

// TestSoftRateWaitPrefersRetryAfterAndCaps Retry-After 优先，且不能被单个头拖住。
func TestSoftRateWaitPrefersRetryAfterAndCaps(t *testing.T) {
	fastSoftRate(t)
	ue := &upstream.Error{Kind: upstream.ErrSoftRate, RetryAfter: 5 * time.Minute}
	if got := softRateWait(ue, 1); got != softRateGapMax {
		t.Fatalf("Retry-After 应封顶到 %s，got %s", softRateGapMax, got)
	}
	ue.RetryAfter = 0
	if got := softRateWait(ue, 1); got != softRateGap {
		t.Fatalf("无头应取 attempt*gap=%s，got %s", softRateGap, got)
	}
}

// TestRunAutoActionRetriesSoftRateOnce 动作撞 429 时重发一次（429 = 请求未执行，动作幂等）。
func TestRunAutoActionRetriesSoftRateOnce(t *testing.T) {
	fastSoftRate(t)
	var n int32
	act := &autoAction{TaskCode: "x", run: func(p *Panel, a *auth.Auth) (string, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return "", &upstream.Error{Kind: upstream.ErrSoftRate, Status: 429}
		}
		return "ok", nil
	}}
	p := &Panel{}
	msg, err := p.runAutoAction(&auth.Auth{UID: "u1"}, act)
	if err != nil || msg != "ok" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("run 次数=%d want 2（首次 + 重发一次）", got)
	}
}
