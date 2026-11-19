package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// captureClient 返回一个把首个请求头克隆到 got 的 Client（各测试共用）。
func captureClient(got *http.Header) *Client {
	return &Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			*got = r.Header.Clone()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)),
			}, nil
		})},
		ChatBaseCN:    "https://www.workbuddy.cn",
		BillingBaseCN: "https://www.codebuddy.cn",
		WebBaseCN:     "https://www.workbuddy.cn",
	}
}

// TestClientIdentityRealmIsolation 桌面端 X-Client-* 三头必须按 realm 隔离：
// 国内号每条出站都带，国际号一个都不发。
//
// 为什么必须按 realm 分：两侧分发包不同（国际版停在 5.9.4，没有这层注入），
// 把国内的形态套上去等于给国际链路加一层官方不存在的指纹；而国内只对齐版本号
// 不发三头则是「5.11.3 UA + 旧头集」——官方客户端不会出现的组合。
func TestClientIdentityRealmIsolation(t *testing.T) {
	paths := map[string]func(c *Client, a *auth.Auth) error{
		"common": func(c *Client, a *auth.Auth) error {
			req, err := http.NewRequest(http.MethodPost, c.chatBase(a)+"/v2/chat/completions", nil)
			if err != nil {
				return err
			}
			c.CommonHeaders(req, a)
			_, err = c.doJSON(req)
			return err
		},
		"billing": func(c *Client, a *auth.Auth) error {
			req, err := http.NewRequest(http.MethodGet, c.billingBase(a)+"/v2/billing/resource-summary", nil)
			if err != nil {
				return err
			}
			c.BillingHeaders(req, a)
			_, err = c.doJSON(req)
			return err
		},
		"desktop_report": func(c *Client, a *auth.Auth) error {
			return c.ReportDesktopEvent(a, DesktopEvent{"eventCode": "chat_message_send"})
		},
		"v3_config": func(c *Client, a *auth.Auth) error {
			// 桩返回空模型目录 → 该函数会报错；这里只关心出站头，忽略返回值。
			_, _ = c.fetchV3ConfigModelMap(a, "WorkBuddy/5.11.3 WorkBuddy/5.11.3 CLI/2.176.0")
			return nil
		},
	}
	cn := &auth.Auth{Domain: "www.workbuddy.cn", AccessToken: "at-cn", UID: "u-cn"}
	gl := &auth.Auth{Domain: "www.workbuddy.ai", AccessToken: "at-gl", UID: "u-gl"}
	if !gl.IsGlobal() {
		t.Fatalf("测试前提不成立：Domain=www.workbuddy.ai 的账号应判为 global")
	}

	for name, run := range paths {
		var got http.Header
		c := captureClient(&got)
		if err := run(c, cn); err != nil {
			t.Fatalf("%s(cn): %v", name, err)
		}
		if v := got.Get("X-Client-Version"); v != defaultClientVersionCN {
			t.Errorf("%s(cn): X-Client-Version = %q want %q", name, v, defaultClientVersionCN)
		}
		if v := got.Get("X-Client-Platform"); v != "windows" {
			t.Errorf("%s(cn): X-Client-Platform = %q want windows", name, v)
		}
		if v := got.Get("X-Client-Product"); v != "WorkBuddy" {
			t.Errorf("%s(cn): X-Client-Product = %q want WorkBuddy", name, v)
		}

		var gg http.Header
		cg := captureClient(&gg)
		if err := run(cg, gl); err != nil {
			t.Fatalf("%s(global): %v", name, err)
		}
		for _, h := range []string{"X-Client-Version", "X-Client-Platform", "X-Client-Product"} {
			if v := gg.Get(h); v != "" {
				t.Errorf("%s(global): %s = %q，国际链路不发这个头", name, h, v)
			}
		}
	}
}

// TestClientIdentityNotOnMPPaths 小程序口径请求必须撤掉桌面端三头，只留平台段。
//
// 不撤就会发出 `5.11.3 / miniprogram / WorkBuddy`：桌面端不会说 miniprogram，
// 小程序也不会自报桌面版本号与产品名——谁都不像的第四种组合。
func TestClientIdentityNotOnMPPaths(t *testing.T) {
	cn := &auth.Auth{Domain: "www.workbuddy.cn", AccessToken: "at-cn", UID: "u-cn"}
	cases := map[string]func(c *Client, a *auth.Auth) error{
		"growth_list_mp": func(c *Client, a *auth.Auth) error {
			_, err := c.ListTasksMP(a)
			return err
		},
		"claim_reward_mp": func(c *Client, a *auth.Auth) error {
			_, _, err := c.ClaimRewardMP(a, "RichMeow_Chat")
			return err
		},
	}
	for name, run := range cases {
		var got http.Header
		c := captureClient(&got)
		if err := run(c, cn); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if v := got.Get("X-Client-Platform"); v != mpPlatform {
			t.Errorf("%s: X-Client-Platform = %q want %q", name, v, mpPlatform)
		}
		if v := got.Get("X-Client-Version"); v != "" {
			t.Errorf("%s: X-Client-Version = %q，小程序口径不该带桌面版本号", name, v)
		}
		if v := got.Get("X-Client-Product"); v != "" {
			t.Errorf("%s: X-Client-Product = %q，小程序口径不该带桌面产品名", name, v)
		}
	}
}

// TestClientIdentityEscapeSwitch 逃生开关必须能整体关掉注入（上游改判时不必回滚二进制）。
func TestClientIdentityEscapeSwitch(t *testing.T) {
	t.Setenv(noClientIdentityEnv, "1")
	var got http.Header
	c := captureClient(&got)
	a := &auth.Auth{Domain: "www.workbuddy.cn", AccessToken: "at-cn", UID: "u-cn"}
	req, err := http.NewRequest(http.MethodPost, c.chatBase(a)+"/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommonHeaders(req, a)
	if _, err := c.doJSON(req); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"X-Client-Version", "X-Client-Platform", "X-Client-Product"} {
		if v := got.Get(h); v != "" {
			t.Errorf("%s = %q，开关打开后不应注入", h, v)
		}
	}
	// 其余头不受影响（防止顺手把整段头集改没）。
	if got.Get("X-CodeBuddy-Request") != "1" {
		t.Error("X-CodeBuddy-Request 丢失：开关只应影响三头")
	}
}
