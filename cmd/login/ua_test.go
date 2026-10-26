package main

import (
	"strings"
	"testing"
)

// TestDecorateAuthURL 授权链接补官方 version 参数（与面板登录链路同口径）。
func TestDecorateAuthURL(t *testing.T) {
	got := decorateAuthURL("https://example.com/login?platform=CLI&state=abc")
	if !strings.Contains(got, "version="+clientVersion) {
		t.Errorf("got %q, want version=%s", got, clientVersion)
	}
	if !strings.Contains(got, "state=abc") {
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

// TestLoginClientVersion UA 与 version 参数同源。
func TestLoginClientVersion(t *testing.T) {
	if clientVersion != "2.176.0" {
		t.Fatalf("clientVersion=%s want 2.176.0", clientVersion)
	}
	if clientUA != "CLI/2.176.0 CodeBuddy/2.176.0" {
		t.Errorf("clientUA=%q", clientUA)
	}
}
