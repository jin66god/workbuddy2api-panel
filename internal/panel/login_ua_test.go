package panel

import (
	"strings"
	"testing"
)

// TestDecorateAuthURL 授权链接补官方 version 参数：缺失则补、已有则不动、
// 非法 URL 原样返回（不阻断登录）。
func TestDecorateAuthURL(t *testing.T) {
	got := decorateAuthURL("https://example.com/login?platform=CLI&state=abc")
	if !strings.Contains(got, "version="+clientVersion) {
		t.Errorf("got %q, want version=%s", got, clientVersion)
	}
	if !strings.Contains(got, "state=abc") || !strings.Contains(got, "platform=CLI") {
		t.Errorf("原有参数被破坏: %q", got)
	}

	keep := "https://example.com/login?version=9.9.9"
	if got := decorateAuthURL(keep); got != keep {
		t.Errorf("已带 version 不应覆盖: got %q want %q", got, keep)
	}

	bad := "://bad"
	if got := decorateAuthURL(bad); got != bad {
		t.Errorf("非法 URL 应原样返回: got %q want %q", got, bad)
	}
}

// TestLoginClientUA UA 与授权链接 version 同源，且是官方 CLI 2.176.0。
func TestLoginClientUA(t *testing.T) {
	if clientVersion != "2.176.0" {
		t.Fatalf("clientVersion=%s want 2.176.0", clientVersion)
	}
	want := "CLI/2.176.0 CodeBuddy/2.176.0"
	if clientUA != want {
		t.Errorf("clientUA=%q want %q", clientUA, want)
	}
}
