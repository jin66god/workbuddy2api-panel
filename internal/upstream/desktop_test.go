package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestReportDesktopEventFingerprint 断言桌面指纹上报：走 chatBase、UA 为桌面形状、
// 事件数组自动注入 workbuddy-desktop 公共指纹（覆盖同名业务键）。
func TestReportDesktopEventFingerprint(t *testing.T) {
	var got []map[string]any
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			t.Errorf("path=%s want /v2/report", r.URL.Path)
		}
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("X-User-Id") != "u-dt" {
			t.Errorf("X-User-Id=%q want u-dt", r.Header.Get("X-User-Id"))
		}
		if r.Header.Get("X-Product") != "SaaS" {
			t.Errorf("X-Product=%q want SaaS", r.Header.Get("X-Product"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("body must be array: %v", err)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	err := c.ReportDesktopEvent(&auth.Auth{AccessToken: "at", UID: "u-dt", Nickname: "RenJie"},
		DesktopEvent{"eventCode": "agent_task_created", "mode": "craft"})
	if err != nil {
		t.Fatalf("report desktop: %v", err)
	}
	if wantUA := "WorkBuddy/5.11.3 WorkBuddy/5.11.3 CLI/2.176.0"; gotUA != wantUA {
		t.Errorf("UA=%q want %q", gotUA, wantUA)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	ev := got[0]
	if ev["extName"] != "workbuddy-desktop" {
		t.Errorf("extName=%v want workbuddy-desktop", ev["extName"])
	}
	if ev["ideType"] != "WorkBuddy" || ev["ideName"] != "WorkBuddy" {
		t.Errorf("ideType/ideName=%v/%v want WorkBuddy", ev["ideType"], ev["ideName"])
	}
	if ev["userId"] != "u-dt" {
		t.Errorf("userId=%v want u-dt", ev["userId"])
	}
	if ev["userNickname"] != "RenJie" {
		t.Errorf("userNickname=%v want RenJie", ev["userNickname"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("业务字段 mode=%v 被指纹覆盖或缺失", ev["mode"])
	}
	// machineId 应为 uid 稳定派生（36 hex）。
	if len(ev["machineId"].(string)) != 36 {
		t.Errorf("machineId 长度=%d want 36", len(ev["machineId"].(string)))
	}
}

// TestDesktopChatSequenceShape 断言完整对话事件链的事件码顺序与关键字段。
func TestDesktopChatSequenceShape(t *testing.T) {
	events := DesktopChatSequence("conv-1", "req-1", "msg-1", "fast-model", "fast-model")
	wantCodes := []string{
		"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events=%d want %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode=%v want %v", i, events[i]["eventCode"], want)
		}
	}
	resp := events[3]
	if resp["isSuccessful"] != true {
		t.Errorf("chat_message_response.isSuccessful=%v want true（RichMeow 判据）", resp["isSuccessful"])
	}
	if resp["finishReason"] != "stop" {
		t.Errorf("finishReason=%v want stop", resp["finishReason"])
	}
	if resp["conversationId"] != "conv-1" {
		t.Errorf("conversationId=%v want conv-1", resp["conversationId"])
	}
}

// TestSetAppearanceTheme 断言主题设置端点形状（点亮 Hp_Appearance 的 API）。
func TestSetAppearanceTheme(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/user-asset/appearance/set" {
			t.Errorf("path=%s want /v2/user-asset/appearance/set", r.URL.Path)
		}
		if wantUA := "WorkBuddy/5.11.3 WorkBuddy/5.11.3 CLI/2.176.0"; r.Header.Get("User-Agent") != wantUA {
			t.Errorf("UA=%q want %q", r.Header.Get("User-Agent"), wantUA)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	if err := c.SetAppearanceTheme(&auth.Auth{AccessToken: "at", UID: "u1"}, "theme-tkmw7j"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if got["kind"] != "theme" || got["resource_key"] != "theme-tkmw7j" {
		t.Errorf("body=%v want kind=theme resource_key=theme-tkmw7j", got)
	}
}

// TestDesktopBuddyAppSequenceIsFirstPageIsBool 断言 buddyapp_enter_click 的
// isFirstPage 是布尔 true。
//
// 回归位：服务端收紧 /v2/report 的格式校验后，字符串 "1" 会让整条事件被丢弃，
// 响应仍是 2xx，只在 body 里说 dropped=1 / invalidIndices=[2]（正是五连的第三条）。
// 任务判据吃不到这条，Buddy_App 与 Buddy_App_QQ 永远停在 0/1，日志只有一行 WARN。
func TestDesktopBuddyAppSequenceIsFirstPageIsBool(t *testing.T) {
	var found bool
	for _, ev := range DesktopBuddyAppSequence("cb_test", "测试助手") {
		if ev["eventCode"] != "buddyapp_enter_click" {
			continue
		}
		found = true
		v, ok := ev["isFirstPage"].(bool)
		if !ok || !v {
			t.Fatalf("isFirstPage = %#v (%T), want bool true", ev["isFirstPage"], ev["isFirstPage"])
		}
	}
	if !found {
		t.Fatal("五连事件里没有 buddyapp_enter_click")
	}
}

// TestDesktopTripleByRealm 桌面端上报三元组必须按 realm 取，且与 UA / X-IDE-Version 同源。
//
// 恒用单一三元组会给另一域账号盖上不属于其分发包的包指纹（版本号与安装清单自相矛盾），
// 是设备侧写最容易识破的一处；这条用例同时钉住「三元组 / UA / X-IDE-Version 三者一致」。
func TestDesktopTripleByRealm(t *testing.T) {
	c := &Client{}
	cn := &auth.Auth{UID: "u-cn", Domain: "www.workbuddy.cn"}
	global := &auth.Auth{UID: "u-gl", Domain: "www.workbuddy.ai"}

	ver, commit, release := desktopTriple(cn)
	if ver != "5.11.3" || commit != "ecace0f7028166695994300182295329b8b95449" || release != int64(1794502169764) {
		t.Fatalf("CN triple = %s/%s/%d，与 5.11.3 分发包不符", ver, commit, release)
	}
	fp := desktopFingerprint(cn)
	if fp["ideVersion"] != ver || fp["extVersion"] != ver || fp["commit"] != commit || fp["releaseDate"] != release {
		t.Fatalf("CN 指纹与三元组不一致: %#v", fp)
	}
	if ua := c.desktopUAFor(cn); ua != "WorkBuddy/5.11.3 WorkBuddy/5.11.3 CLI/2.176.0" {
		t.Fatalf("CN desktop UA=%q", ua)
	}
	if got := desktopVersionFor(cn); got != ver {
		t.Fatalf("CN X-IDE-Version=%q want %q", got, ver)
	}

	gver, gcommit, grelease := desktopTriple(global)
	if gver != "5.9.4" || gcommit != "fa0a0901906addaa1a4259f6df347388a5563ded" || grelease != int64(1793288804029) {
		t.Fatalf("global triple = %s/%s/%d，与 5.9.4 分发包不符", gver, gcommit, grelease)
	}
	if ua := c.desktopUAFor(global); ua != "workbuddy-ai/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0" {
		t.Fatalf("global desktop UA=%q", ua)
	}
}
