package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestRequestLevelErrorKeepsStickyBinding 钉住「请求级错误只释放租约、不解绑会话粘性」。
//
// 回归的是这样一个真实故障链：内容拦截 / prompt 超长 / 图片无效 / 参数错 / 11151
// 空 text 段这些错误与账号无关（同一个 body 换任何号都会撞），此前统一走 fail()，
// 把「释放租约」和「解绑会话」绑在一起——客户端重试就被分到另一个号上，上游前缀
// 缓存按账号隔离，十万~几十万 token 的前缀从头重算。观感是「报错一次之后缓存再也
// 不生效」。这里用 bindStore 的镜像删除次数取证：绑定必须还在。
func TestRequestLevelErrorKeepsStickyBinding(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "t1", ExpiresAt: 9999999999})
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls == 1 {
			return 200, sseOK, true // 首轮成功 → 建立绑定
		}
		// 11151：请求级错误，与账号无关。
		return 400, `{"code":11151,"msg":"a message has empty content"}`, false
	})
	store := newBindStore()
	sess := session.New(session.Config{TTL: time.Minute, Available: p.AvailableUIDs, Store: store})
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess, SoftCooldown: time.Minute})

	body := `{"model":"cn:m","messages":[],"metadata":{"conversation_id":"conv-sticky"}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("首轮应成功: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.binds) != 1 {
		t.Fatalf("首轮成功应建立 1 条粘性绑定，got %d", len(store.binds))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 400 {
		t.Fatalf("第二轮应透传 400: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.delCnt != 0 {
		t.Fatalf("请求级错误不该解绑粘性，DelBind 次数=%d", store.delCnt)
	}
	if len(store.binds) != 1 {
		t.Fatalf("绑定应保留，got %d", len(store.binds))
	}
}

// TestAccountLevelErrorUnbindsSticky 对照：账号级错误（401）仍必须解绑，让会话换号。
func TestAccountLevelErrorUnbindsSticky(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "t1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "t2", ExpiresAt: 9999999999},
	)
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls == 1 {
			return 200, sseOK, true
		}
		return 401, `{"code":10001,"msg":"unauthorized"}`, false
	})
	store := newBindStore()
	sess := session.New(session.Config{TTL: time.Minute, Available: p.AvailableUIDs, Store: store})
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess, SoftCooldown: time.Minute})

	body := `{"model":"cn:m","messages":[],"metadata":{"conversation_id":"conv-401"}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("首轮应成功: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if store.delCnt == 0 {
		t.Fatal("账号级 401 应该解绑粘性（下次换号），但 DelBind 一次都没调")
	}
}

// TestV1ModelsExposesPromoFields 钉住 /v1/models 的 promo_* 透出口径：
// 限时免费（factor=0）必须能被读出来（用非 nil 判断，零值不能当缺失），
// 只有标签没有系数的优惠也要透出，无优惠时三个字段都不出现。
func TestV1ModelsExposesPromoFields(t *testing.T) {
	zero := 0.0
	half := 0.5
	cases := []struct {
		name  string
		mi    upstream.ModelInfo
		want  map[string]any
		absent []string
	}{
		{
			name: "限时免费 factor=0 必须透出",
			mi:   upstream.ModelInfo{PromoFactor: &zero, PromoCredits: "0x", PromoLabel: "限时免费"},
			want: map[string]any{"promo_factor": 0.0, "promo_credits": "0x", "promo_label": "限时免费"},
		},
		{
			name: "只有标签没有系数",
			mi:   upstream.ModelInfo{PromoLabel: "错峰使用"},
			want: map[string]any{"promo_label": "错峰使用"},
			absent: []string{"promo_factor", "promo_credits"},
		},
		{
			name: "五折",
			mi:   upstream.ModelInfo{PromoFactor: &half, PromoCredits: "0.50x", PromoLabel: "夜间折扣"},
			want: map[string]any{"promo_factor": 0.5, "promo_credits": "0.50x", "promo_label": "夜间折扣"},
		},
		{
			name:   "无优惠一个都不写",
			mi:     upstream.ModelInfo{},
			absent: []string{"promo_factor", "promo_credits", "promo_label"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyModelInfoFields(map[string]any{"id": "cn:m"}, tc.mi)
			for k, want := range tc.want {
				v, ok := got[k]
				if !ok {
					t.Fatalf("%s 缺字段 %q: %+v", tc.name, k, got)
				}
				if fmt.Sprint(v) != fmt.Sprint(want) {
					t.Fatalf("%s 字段 %q=%v want %v", tc.name, k, v, want)
				}
			}
			for _, k := range tc.absent {
				if _, ok := got[k]; ok {
					t.Fatalf("%s 不该写 %q: %+v", tc.name, k, got)
				}
			}
		})
	}
}
