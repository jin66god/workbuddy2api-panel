package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestReportWebEventNodeShape /v2/report 必须是官方 Node 形态：
// 不带 Origin / Referer / sec-ch-* 这类**浏览器族**头，带 X-Domain / X-Product /
// X-Request-ID（32 位 hex）。往这条链补浏览器头反而是破功。
//
// X-Client-Platform 不再按「有这个头」判：桌面端共享 HTTP 层在 5.11.3 起会按
// origin 注入 X-Client-* 三头（含本路径），真机形态是 `windows`；浏览器族才用
// `web`。所以判据改为「取值不能是 web」，既保住「别退回浏览器族」的原意，也不再
// 拦住与版本号同源的桌面身份头。
func TestReportWebEventNodeShape(t *testing.T) {
	old := t.TempDir()
	_ = old
	var got http.Header
	c := &Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			got = r.Header.Clone()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)),
			}, nil
		})},
		WebBaseCN: "https://www.workbuddy.cn",
	}
	a := &auth.Auth{Domain: "www.workbuddy.cn", AccessToken: "at1", UID: "u1"}
	if err := c.ReportWebEvent(a, "web_element_click", "https://www.workbuddy.cn/lib", "el1", "文档"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	for _, banned := range []string{"Origin", "Referer", "Sec-Ch-Ua", "Sec-Fetch-Site"} {
		if v := got.Get(banned); v != "" {
			t.Errorf("%s = %q，官方 report 不发这个头", banned, v)
		}
	}
	// 浏览器族标志：platform 只能是桌面端取值，不能退回 web。
	if v := got.Get("X-Client-Platform"); v != "" && v != "windows" {
		t.Errorf("X-Client-Platform = %q，桌面端形态只能是 windows 或留空（浏览器族才发 web）", v)
	}
	if got.Get("X-Product") != "SaaS" {
		t.Errorf("X-Product = %q want SaaS", got.Get("X-Product"))
	}
	if got.Get("X-Domain") == "" {
		t.Error("缺少 X-Domain")
	}
	rid := got.Get("X-Request-ID")
	if len(rid) != 32 || strings.ContainsAny(rid, "-stuvwxyz") {
		t.Errorf("X-Request-ID = %q want 32 位 hex（真机无时间位）", rid)
	}
	if ua := got.Get("User-Agent"); !strings.HasPrefix(ua, "WorkBuddy/") {
		t.Errorf("User-Agent = %q，应为官方桌面三段式/单段，不是 Chrome", ua)
	}
}

// TestReportWebEventLegacySwitch 回滚开关必须能恢复旧浏览器族（否则出问题没法退）。
func TestReportWebEventLegacySwitch(t *testing.T) {
	t.Setenv("WB2_REPORT_LEGACY", "1")
	var got http.Header
	c := &Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			got = r.Header.Clone()
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`))}, nil
		})},
		WebBaseCN: "https://www.workbuddy.cn",
	}
	a := &auth.Auth{Domain: "www.workbuddy.cn", AccessToken: "at1", UID: "u1"}
	if err := c.ReportWebEvent(a, "web_element_click", "https://www.workbuddy.cn/lib", "el1", "文档"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	if got.Get("x-client-platform") != "web" || got.Get("Origin") == "" {
		t.Errorf("legacy 开关未恢复旧形态：platform=%q origin=%q", got.Get("x-client-platform"), got.Get("Origin"))
	}
}
