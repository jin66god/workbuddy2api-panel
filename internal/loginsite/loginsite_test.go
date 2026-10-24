package loginsite

import (
	"strings"
	"testing"
)

func TestForAndForRealm(t *testing.T) {
	if got := For("intl"); got.API != BaseOfficial || got.Realm != RealmGlobal {
		t.Errorf("For(intl)=%+v", got)
	}
	if got := For("workbuddy"); got.API != BaseWorkBuddy {
		t.Errorf("For(workbuddy)=%+v", got)
	}
	if got := For(""); got.ID != SiteCN || got.Realm != RealmCN {
		t.Errorf("For(empty)=%+v want cn", got)
	}
	if got := For("weird"); got.ID != SiteCN {
		t.Errorf("For(weird)=%+v want cn", got)
	}
	if got := ForRealm("global"); got.ID != SiteWorkBuddy {
		t.Errorf("ForRealm(global)=%+v want workbuddy（历史行为）", got)
	}
	if got := ForRealm("cn"); got.ID != SiteCN {
		t.Errorf("ForRealm(cn)=%+v", got)
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"cn", "intl", "workbuddy", "INTL"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q)=false", id)
		}
	}
	for _, id := range []string{"", "global", "foo"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q)=true", id)
		}
	}
}

func TestBaseForDomain(t *testing.T) {
	cases := map[string]string{
		"www.codebuddy.ai": BaseOfficial,
		"codebuddy.ai":     BaseOfficial,
		"a.b.codebuddy.ai": BaseOfficial,
		"www.workbuddy.ai": BaseWorkBuddy,
		"workbuddy.ai":     BaseWorkBuddy,
		"www.workbuddy.cn": "",
		"":                 "",
		"evilcodebuddy.ai": "",
	}
	for in, want := range cases {
		if got := BaseForDomain(in); got != want {
			t.Errorf("BaseForDomain(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCodeSemantics(t *testing.T) {
	if !CodePending(CodeRetryFetchToken) || !CodePending(CodeRetryFetchAccount) {
		t.Error("11217/12151 应为 pending")
	}
	for _, c := range []int{CodeLicenseSeatLimit, CodeLicenseExpired, CodeTrialExpired} {
		if !CodeFatal(c) {
			t.Errorf("code %d 应为终态", c)
		}
		if CodePending(c) {
			t.Errorf("code %d 不应为 pending", c)
		}
	}
	if CodeFatal(0) || CodePending(0) {
		t.Error("code 0 不属于两类")
	}
}

func TestDecorateAuthURL(t *testing.T) {
	got := DecorateAuthURL("https://example.com/login?platform=CLI&state=abc", SiteCN, "")
	if !strings.Contains(got, "version="+ClientVersion) {
		t.Errorf("got %q, want version=%s", got, ClientVersion)
	}
	if !strings.Contains(got, "state=abc") || !strings.Contains(got, "platform=CLI") {
		t.Errorf("原有参数被破坏: %q", got)
	}

	keep := "https://example.com/login?version=9.9.9"
	if got := DecorateAuthURL(keep, SiteCN, ""); got != keep {
		t.Errorf("已带 version 不应覆盖: %q", got)
	}

	// nonce 只对 workbuddy 平台生效。
	if got := DecorateAuthURL("https://example.com/login", SiteCN, "n1"); strings.Contains(got, "ww_device_nonce") {
		t.Errorf("cn 站点不应带 nonce: %q", got)
	}
	if got := DecorateAuthURL("https://example.com/login", SiteWorkBuddy, "n1"); !strings.Contains(got, "ww_device_nonce=n1") {
		t.Errorf("workbuddy 站点应带 nonce: %q", got)
	}
	if got := DecorateAuthURL("https://example.com/login", SiteWorkBuddy, ""); strings.Contains(got, "ww_device_nonce") {
		t.Errorf("无 nonce 不应伪造: %q", got)
	}

	bad := "://bad"
	if got := DecorateAuthURL(bad, SiteCN, ""); got != bad {
		t.Errorf("非法 URL 应原样返回: %q", got)
	}
}
