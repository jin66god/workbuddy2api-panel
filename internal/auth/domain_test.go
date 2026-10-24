package auth

import "testing"

// TestRealmRecognizesCodebuddyAI 官方 CLI 新国际站把 domain 落成 www.codebuddy.ai：
// 必须判 global，否则会打到 copilot.tencent.com（错域 401）。
func TestRealmRecognizesCodebuddyAI(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"www.codebuddy.ai", "global"},
		{"codebuddy.ai", "global"},
		{"api.codebuddy.ai", "global"},
		{"www.workbuddy.ai", "global"},
		{"workbuddy.ai", "global"},
		{"www.codebuddy.cn", "cn"},
		{"", "cn"},
		{"evilcodebuddy.ai", "cn"},
	}
	for _, c := range cases {
		a := &Auth{Domain: c.domain}
		if got := a.Realm(); got != c.want {
			t.Errorf("Domain=%q Realm()=%q want %q", c.domain, got, c.want)
		}
	}
}
