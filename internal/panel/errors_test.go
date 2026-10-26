package panel

import "testing"

// kindOf 的口径回归：分类必须稳定，否则错误汇总会把同一类错误拆成多行。
func TestKindOf(t *testing.T) {
	cases := []struct{ line, kind, sev string }{
		{`WARN: [upstream] chat_stream acct=a(u1): upstream 429 soft_rate body={"code":6004}`, "限流/额度", sevBad},
		{`WARN: [upstream] chat_stream acct=a(u1): upstream 401 session_dead body=`, "会话失效", sevBad},
		{`WARN: [upstream] chat_stream acct=a(u1): upstream 403 waf_block body=`, "WAF 拦截", sevBad},
		{`WARN: [upstream] chat_stream acct=a(u1): upstream 500 server body=`, "上游 5xx", sevBad},
		{`WARN: [upstream] chat_stream acct=a(u1): upstream 400 content_blocked body=`, "内容拦截", sevWarn},
		{`WARN: keepalive 昵称(u2): refresh failed`, "保活失败", sevWarn},
		{`WARN: [pool] 账号被禁用 acct=x(u1) reason="session dead"（终态，需人工恢复）`, "账号被禁用", sevBad},
		{`WARN: [server] cache miss: model=m 本次大前缀未命中`, "前缀缓存未命中", sevInfo},
		{`INFO: 完全正常的一行`, "", ""},
	}
	for _, c := range cases {
		k, _, s := kindOf(c.line)
		if k != c.kind || (c.kind != "" && s != c.sev) {
			t.Errorf("kindOf(%q)=(%q,%q) want (%q,%q)", c.line, k, s, c.kind, c.sev)
		}
	}
}
