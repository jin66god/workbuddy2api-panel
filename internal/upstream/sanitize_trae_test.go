package upstream

import (
	"strings"
	"testing"
)

// Trae 客户端的系统提示以「You are an interactive agent in TraeCode that helps the USER …」
// 开头，服务端按「interactive agent in TraeCode」这一连续短语判非官方渠道并回 400 11-128。
// 逐条二分过：单独出现 TraeCode / "in Trae IDE" / "in Cursor" 均放行，只删掉 "in TraeCode"
// 后整段 23KB system（21 个工具）即 200 —— 所以要改的是这**一个短语**，不是品牌词。
func TestSanitizeTraeCodeIdentitySentence(t *testing.T) {
	in := "You are an interactive agent in TraeCode that helps the USER with coding tasks."
	out := sanitizeText(in)
	if strings.Contains(out, "in TraeCode") {
		t.Fatalf("TraeCode 自述短语未改写: %q", out)
	}
	if !strings.HasPrefix(out, "You are an interactive agent that helps the USER") {
		t.Fatalf("改写后句子其余部分应原样保留，得到 %q", out)
	}
}

// 散词提及不得误伤：模型/用户在正文里聊到 TraeCode 这个产品名时不该被改写。
func TestSanitizeTraeCodeStandaloneUntouched(t *testing.T) {
	for _, in := range []string{
		"TraeCode is a code editor.",
		"我用 TraeCode 写代码",
		"You are an interactive agent in Cursor that helps the USER.",
	} {
		if got := sanitizeText(in); got != in {
			t.Errorf("散词被改写: %q → %q", in, got)
		}
	}
}

// 预检串必须让普通请求仍走零分配快路，同时命中新指纹。
func TestSanitizeTraeCodeFingerprint(t *testing.T) {
	if !hasFingerprint("You are an interactive agent in TraeCode") {
		t.Fatal("TraeCode 指纹未进入预检，净化会被整段跳过")
	}
	if hasFingerprint("You are an interactive agent in Cursor") {
		t.Fatal("Cursor 形态不应命中指纹")
	}
}
