package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// TestModelMissingIsRequestLevel 钉住 11102「该模型 id 上游不存在」的处置：
// 立即 400 + model_unavailable、**不轮转、不罚号、不写 (账号,模型) 负缓存**。
//
// 回归的链路：11102 与 11158 共用 IsModelBlocked 一份判据，此前两者都走
// ErrModelBlocked → BlockModelBackoff + 换号。11102 是**模型级**（同一 body 换遍全池
// 是同一句，实测多个号一字不差），于是：每个号白打一次上游、每个号写一笔负缓存
// （一次客户端写错的 id 就把该模型从全池锁掉，之后正常请求也被迫换模型），末端还给出
// 「retry on another account」这种反向建议。
//
// 11158 是相反语义（账号类型权限，换到有权限的号真能成功），必须保留轮转 —— 所以本
// 用例同时钉住两条分支，避免修复过头把 11158 也变成 fail-fast。
func TestModelMissingIsRequestLevel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	newPool := func() *pool.Pool {
		p := testPoolWith(
			&auth.Auth{UID: "u1", AccessToken: "t1", ExpiresAt: 9999999999},
			&auth.Auth{UID: "u2", AccessToken: "t2", ExpiresAt: 9999999999},
		)
		// 连败阈值压到 1：只要喂过 NoteFailures，下一次 Pick 就拿不到号。
		p.SetDegrade(1, time.Hour, time.Hour)
		return p
	}

	chat := func(h *Handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := `{"model":"cn:m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		return rec
	}

	t.Run("11102 模型 id 不存在：不轮转不罚号不写负缓存", func(t *testing.T) {
		p := newPool()
		calls := 0
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			calls++
			return 400, `{"code":11102,"msg":"model [DeepSeek-V4.1-Flash] service info not found"}`, false
		})
		h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

		rec := chat(h)
		if rec.Code != 400 {
			t.Fatalf("状态码 = %d，模型级确定性错误必须回 400；body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"model_unavailable"`) {
			t.Fatalf("期望 error.code=model_unavailable；body=%s", rec.Body.String())
		}
		// 上游原文必须透传（含 requestId 才有对账价值），hint 点名「换号无用」。
		if !strings.Contains(rec.Body.String(), "service info not found") {
			t.Fatalf("上游原文未透传；body=%s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "switching accounts will not help") {
			t.Fatalf("hint 未点名换号无用；body=%s", rec.Body.String())
		}
		if calls != 1 {
			t.Fatalf("上游被调用 %d 次，模型级错误不该轮转（应为 1 次）", calls)
		}
		// 不写负缓存：一次写错的 id 不该把该模型从全池锁掉。
		if st := p.ModelBlockedForRealm("m", "cn"); st.Blocked || st.Count != 0 {
			t.Fatalf("11102 不应写 (账号,模型) 负缓存，得到 blocked=%v count=%d", st.Blocked, st.Count)
		}
		// 不罚号：连败阈值已压到 1。
		if got := p.Pick(); got == nil {
			t.Fatal("账号被降权出池：模型级错误不该喂连败计数")
		}
	})

	t.Run("11158 账号类型无权限：仍轮转并写负缓存", func(t *testing.T) {
		p := newPool()
		calls := 0
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			calls++
			return 400, `{"code":11158,"msg":"model [gpt-6-luna] is not allowed for user type [personal_free_user]"}`, false
		})
		h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

		rec := chat(h)
		if rec.Code != 400 {
			t.Fatalf("状态码 = %d；body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"model_unavailable"`) {
			t.Fatalf("期望 error.code=model_unavailable；body=%s", rec.Body.String())
		}
		if calls != 2 {
			t.Fatalf("上游被调用 %d 次，11158 应换号（两号池应为 2 次）", calls)
		}
		if st := p.ModelBlockedForRealm("m", "cn"); !st.Blocked || st.Count != 2 {
			t.Fatalf("11158 应写两条 (账号,模型) 负缓存，得到 blocked=%v count=%d", st.Blocked, st.Count)
		}
	})
}
