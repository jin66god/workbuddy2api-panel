package upstream

import (
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatSentinelHeadersAreBooleanStrings 断言未鉴权/无 uid 时的 X-No-* 哨兵头
// 值是字符串 "true"（官方桌面端真机形态），不是 "1"。
//
// 回归位：曾发 "1"，与真机报文逐字比对不符——哨兵头是每条 chat 都带的固定形态，
// 值不同就是另一种客户端指纹。
func TestChatSentinelHeadersAreBooleanStrings(t *testing.T) {
	c := &Client{}
	req := httptest.NewRequest("POST", "https://example.com/v2/chat/completions", nil)
	a := &auth.Auth{UID: ""}
	c.ChatHeaders(req, a, "", ChatMeta{ConversationID: "c1", ConversationRequestID: "r1"})
	for _, k := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id"} {
		if got := req.Header.Get(k); got != "true" {
			t.Errorf("%s=%q want \"true\"", k, got)
		}
	}
}

// TestGlobalClientVersionMatchesPackage 断言国际版默认客户端版本与国际分发包一致
// （2026-10-30 拆包：package.json version / product.json genieVersion / 真机 UA 三处同为 5.9.4）。
func TestGlobalClientVersionMatchesPackage(t *testing.T) {
	if defaultGlobalClientVersion != "5.9.4" {
		t.Fatalf("defaultGlobalClientVersion=%q want 5.9.4", defaultGlobalClientVersion)
	}
	c := &Client{}
	if got := c.clientVersionFor(&auth.Auth{Domain: "www.workbuddy.ai"}); got != "5.9.4" {
		t.Fatalf("global clientVersion=%q want 5.9.4", got)
	}
}
