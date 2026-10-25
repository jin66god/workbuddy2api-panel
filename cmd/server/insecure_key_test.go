package main

import "testing"

// TestInsecureKeyWarning 空 api_key（完全不鉴权）与样例占位符 test_key 叠加
// 非回环监听时的启动告警；强 key 或只听回环不告警。
func TestInsecureKeyWarning(t *testing.T) {
	cases := []struct {
		listen, apiKey string
		want           bool
	}{
		{":7863", "", true},                 // 缺省监听绑所有网卡 + 不鉴权
		{":7863", "test_key", true},         // 照抄样例占位符
		{"0.0.0.0:7863", "", true},          // 显式全网卡
		{"192.168.1.5:7863", "test_key", true}, // 具体内网地址
		{"127.0.0.1:7863", "", false},       // 只听回环
		{"[::1]:7863", "", false},           // IPv6 回环
		{"localhost:7863", "", false},       // localhost
		{":7863", "strong-key-xyz", false},  // 有有效 key
	}
	for _, c := range cases {
		got := insecureKeyWarning(c.listen, c.apiKey)
		if (got != "") != c.want {
			t.Errorf("insecureKeyWarning(%q, %q) = %q, want 告警=%v", c.listen, redact(c.apiKey), got, c.want)
		}
	}
}

func redact(key string) string {
	if key == "" {
		return "<empty>"
	}
	return "<set>"
}
