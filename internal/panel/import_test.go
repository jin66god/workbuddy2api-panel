package panel

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// postImport 以 multipart 表单发一次 cockpit 导入。
func postImport(t *testing.T, p *Panel, payload string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/panel/api/import/cockpit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

// TestImportCockpitAllowsEmptyRefreshToken 必填判据收敛为 uid + access_token：
// refresh_token 为空的记录不再被跳过（issue #165 的 access-token-only 场景，
// 如静态 API Key 凭证）。缺 access_token 的记录仍应被跳过。
func TestImportCockpitAllowsEmptyRefreshToken(t *testing.T) {
	p := panelWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"Response":{"Data":{"Accounts":[]}}}}`))
	})
	payload := `[` +
		`{"id":"a1","uid":"u-no-rt","access_token":"at-1","refresh_token":""},` +
		`{"id":"a2","uid":"u-no-at","access_token":""}` +
		`]`
	rec := postImport(t, p, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["imported"] != float64(1) || resp["skipped"] != float64(1) {
		t.Fatalf("imported/skipped=%v/%v want 1/1 body=%s", resp["imported"], resp["skipped"], rec.Body.String())
	}
	a := p.cfg.Pool.AuthByUID("u-no-rt")
	if a == nil {
		t.Fatal("refresh_token 为空的记录应被导入")
	}
	if a.RefreshTokenValue() != "" {
		t.Errorf("refreshToken 应为空，实际=%q", a.RefreshTokenValue())
	}
	if _, ok := p.cfg.Pool.Status("u-no-at"); ok {
		t.Error("缺 access_token 的记录不应被导入")
	}
}

// TestImportCockpitKeepsEnterpriseIDAndSkipsCheckin 企业号必须按企业形态落盘，
// 且导入阶段不发注定 400 的签到。
//
// 覆盖两种导出写法：cockpit 的 enterprise_id 与 auth/登录接口的 enterpriseId。
// 丢掉该字段的后果不止一次白签——IsEnterprise() 恒 false 会让额度误走个人
// get-user-resource（企业号返回空 Accounts，面板额度归零）、成长任务入队后被
// 上游 403。静态 API Key（ck_/pt_）同样没有个人成长体系，导入后也不该签到；
// 个人号必须保持原行为（签到恰好 1 次），防止门控过宽误伤。
func TestImportCockpitKeepsEnterpriseIDAndSkipsCheckin(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	p := panelWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"Response":{"Data":{"Accounts":[]}}}}`))
	})
	payload := `[` +
		`{"id":"e1","uid":"u-ent-snake","access_token":"at-e1","enterprise_id":"ent-1"},` +
		`{"id":"e2","uid":"u-ent-camel","access_token":"at-e2","enterpriseId":"ent-2"},` +
		`{"id":"k1","uid":"u-apikey","access_token":"ck_live_import_test"},` +
		`{"id":"p1","uid":"u-personal","access_token":"at-p1"}` +
		`]`
	rec := postImport(t, p, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["imported"] != float64(4) {
		t.Fatalf("imported=%v want 4 body=%s", resp["imported"], rec.Body.String())
	}

	for uid, want := range map[string]string{
		"u-ent-snake": "ent-1",
		"u-ent-camel": "ent-2",
	} {
		a := p.cfg.Pool.AuthByUID(uid)
		if a == nil {
			t.Fatalf("uid=%s 应被导入", uid)
		}
		if a.EnterpriseID != want {
			t.Errorf("uid=%s EnterpriseID=%q want %q（企业形态落盘丢失）", uid, a.EnterpriseID, want)
		}
		if !a.IsEnterprise() {
			t.Errorf("uid=%s 应判定为企业版", uid)
		}
		if a.SupportsPersonalEndpoints() {
			t.Errorf("uid=%s 企业版不应具备个人端点能力", uid)
		}
	}
	if a := p.cfg.Pool.AuthByUID("u-apikey"); a == nil || !a.IsAPIKey() {
		t.Fatalf("静态 API Key 应被识别为 API Key 账号")
	}
	if a := p.cfg.Pool.AuthByUID("u-personal"); a == nil || !a.SupportsPersonalEndpoints() {
		t.Fatalf("个人号不应被门控误伤")
	}

	mu.Lock()
	defer mu.Unlock()
	checkins := 0
	for _, h := range hits {
		if h == "/v2/billing/meter/daily-checkin" {
			checkins++
		}
	}
	if checkins != 1 {
		t.Errorf("签到请求应只对个人号发 1 次，实际 %d 次（hits=%v）", checkins, hits)
	}
}
