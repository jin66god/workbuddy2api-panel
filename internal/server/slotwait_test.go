package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func issue117Auths() (*auth.Auth, *auth.Auth) {
	return &auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999}
}

func issue117Chat(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"cn:deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestIssue117OneLocked issue #117 主现场：两个 CN 号，其中一个对该模型 6004 冷却，另一个应照常承接。
func TestIssue117OneLocked(t *testing.T) {
	a1, a2 := issue117Auths()
	p := testPoolWith(a1, a2)
	p.CooldownSoftForModel("a1", time.Minute, time.Now().Add(4*time.Hour), "deepseek-v4.1-flash",
		`{"code":6004,"msg":"您的使用量已超出频率限制，将在 2026-10-29 19:58:03 UTC+8 重置"}`)

	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := issue117Chat(t, h)
	if rec.Code != 200 {
		t.Errorf("期望 200（a2 健康），得到 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestIssue117OtherBusyWaitsForSlot issue #117 真实现场：另一号在途占满 → 等一个名额（而不是立刻 503）。
func TestIssue117OtherBusyWaitsForSlot(t *testing.T) {
	oldMax := slotWaitMax
	slotWaitMax = 3 * time.Second
	t.Cleanup(func() { slotWaitMax = oldMax })

	a1, a2 := issue117Auths()
	p := testPoolWith(a1, a2)
	p.SetMaxInFlight(3)
	p.CooldownSoftForModel("a1", time.Minute, time.Now().Add(4*time.Hour), "deepseek-v4.1-flash", "6004 ...")
	for i := 0; i < 3; i++ {
		if !p.Acquire("a2") {
			t.Fatalf("acquire %d 失败", i)
		}
	}
	t.Cleanup(func() {
		for i := 0; i < 3; i++ {
			p.Release("a2")
		}
	})
	if !p.SlotWaitForRealm("deepseek-v4.1-flash", "cn", nil) {
		t.Fatal("应判定为「都在途占满」")
	}
	// 200ms 后有一个在途请求收尾（释放名额）—— 请求应等到它并成功。
	go func() {
		time.Sleep(200 * time.Millisecond)
		p.Release("a2")
	}()
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up})
	start := time.Now()
	rec := issue117Chat(t, h)
	t.Logf("code=%d waited=%s", rec.Code, time.Since(start).Round(time.Millisecond))
	if rec.Code != 200 {
		t.Errorf("名额释放后应 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
}

// 名额一直不释放时必须收敛回 503（等待有上限，不能把请求挂死）。
func TestIssue117SlotWaitIsBounded(t *testing.T) {
	oldMax := slotWaitMax
	slotWaitMax = 150 * time.Millisecond
	t.Cleanup(func() { slotWaitMax = oldMax })

	a1, a2 := issue117Auths()
	p := testPoolWith(a1, a2)
	p.SetMaxInFlight(3)
	p.CooldownSoftForModel("a1", time.Minute, time.Now().Add(4*time.Hour), "deepseek-v4.1-flash", "6004 ...")
	for i := 0; i < 3; i++ {
		p.Acquire("a2")
	}
	t.Cleanup(func() {
		for i := 0; i < 3; i++ {
			p.Release("a2")
		}
	})
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up})
	start := time.Now()
	rec := issue117Chat(t, h)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("等待应被 slotWaitMax 收住，实际 %v", d)
	}
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Errorf("名额始终不释放应 503 no_healthy_account，得到 %d：%s", rec.Code, rec.Body.String())
	}
}
