package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.24.9-panel", "v1.24.9", false}, // 后缀/前导 v 归一后相同
		{"1.24.9-panel", "v1.24.10", true}, // 按数字比，不是字典序（"10" > "9"）
		{"1.24.9-panel", "v1.25.0", true},
		{"1.25.0-panel", "v1.24.9", false}, // 比当前旧不提示
		{"1.25", "v1.25.0", false},         // 缺位补 0
		{"v1.24.9", "1.24.9", false},
		{"", "v1.0.0", true},                     // 当前版本未知：视为有更新（宁可提示）
		{"1.24.9-panel", "not-a-version", false}, // 非数字段按 0，不误报
	}
	for _, c := range cases {
		if got := versionNewer(c.current, c.latest); got != c.want {
			t.Errorf("versionNewer(%q,%q)=%v want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	got := parseVersion("v1.24.9-panel")
	if len(got) != 3 || got[0] != 1 || got[1] != 24 || got[2] != 9 {
		t.Fatalf("parseVersion = %v", got)
	}
	if len(parseVersion("")) != 0 {
		t.Fatal("空版本应返回空切片")
	}
}

// 未启用时 view 只返回 enabled=false：前端据此完全隐藏提示，不做任何额外请求。
func TestUpdateViewDisabled(t *testing.T) {
	u := newUpdateInfo(UpdateOptions{Enabled: false})
	v := u.view("1.24.9-panel")
	if v["enabled"] != false {
		t.Fatalf("view=%v", v)
	}
	if _, ok := v["latest"]; ok {
		t.Fatal("未启用时不应带 latest")
	}
}

// 已检查到更新的场景：has_update / one_click / command 三个字段决定前端行为。
func TestUpdateViewHasUpdate(t *testing.T) {
	u := newUpdateInfo(UpdateOptions{Enabled: true, Repo: "o/r", Interval: time.Hour})
	u.latest = "v1.25.0"
	u.releaseURL = "https://example/r"
	u.checkedAt = time.Now() // 缓存新鲜，view 不会触发网络刷新
	v := u.view("1.24.9-panel")
	if v["has_update"] != true {
		t.Fatalf("has_update=%v", v["has_update"])
	}
	if v["one_click"] != false {
		t.Fatal("未配 watchtower 时 one_click 必须为 false")
	}
	if v["command"] != composeUpgradeCommand {
		t.Fatalf("command=%v", v["command"])
	}
	if v["release_url"] != "https://example/r" {
		t.Fatalf("release_url=%v", v["release_url"])
	}
}

// 没配旁路升级器：apply 返回 409 + 可复制命令（能力边界，不是故障）。
func TestUpdateApplyWithoutWatchtower(t *testing.T) {
	p := &Panel{cfg: Config{Version: "1.24.9-panel"}, upd: newUpdateInfo(UpdateOptions{
		Enabled: true, Repo: "o/r", Interval: time.Hour,
	})}
	rec := httptest.NewRecorder()
	p.updateApply(rec, httptest.NewRequest(http.MethodPost, "/panel/api/update/apply", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["mode"] != "command" || got["command"] != composeUpgradeCommand {
		t.Fatalf("body=%v", got)
	}
}

// 配了旁路升级器：面板 POST {url}/v1/update，带 Bearer token，成功回 ok。
func TestUpdateApplyWatchtower(t *testing.T) {
	var hit bool
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/update" {
			http.NotFound(w, r)
			return
		}
		hit = true
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Version: "1.24.9-panel"}, upd: newUpdateInfo(UpdateOptions{
		Enabled: true, Repo: "o/r", Interval: time.Hour,
		WatchtowerURL: srv.URL, WatchtowerToken: "tok-1",
	})}
	rec := httptest.NewRecorder()
	p.updateApply(rec, httptest.NewRequest(http.MethodPost, "/panel/api/update/apply", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !hit {
		t.Fatal("没有请求升级器")
	}
	if auth != "Bearer tok-1" {
		t.Fatalf("Authorization=%q", auth)
	}
	if !strings.Contains(rec.Body.String(), `"mode":"watchtower"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

// 升级器 5xx：面板回 502 并带上游状态，前端据此降级为「复制命令」。
func TestUpdateApplyWatchtowerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	p := &Panel{cfg: Config{Version: "1.24.9-panel"}, upd: newUpdateInfo(UpdateOptions{
		Enabled: true, Repo: "o/r", WatchtowerURL: srv.URL,
	})}
	rec := httptest.NewRecorder()
	p.updateApply(rec, httptest.NewRequest(http.MethodPost, "/panel/api/update/apply", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rec.Code)
	}
}
