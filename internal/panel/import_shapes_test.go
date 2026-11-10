package panel

import "testing"

// TestDecodeCockpitAccountsShapes 锁住「同一个导出工具产出的多种形态」都能进池。
// 这些都是实际出现过的文件形状：批量导出（数组 + 蛇形）、单账号详情导出
// （{account,auth} 嵌套驼峰）、以及被用户手工包进信封或按 uid 做键的变体。
func TestDecodeCockpitAccountsShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "array snake",
			in:   `[{"uid":"u1","access_token":"a","refresh_token":"r","expires_at":1795097981000}]`,
			want: []string{"u1"},
		},
		{
			name: "array nested",
			in:   `[{"account":{"uid":"u2"},"auth":{"accessToken":"a","refreshToken":"r","expiresAt":1795097981}}]`,
			want: []string{"u2"},
		},
		{
			name: "single nested",
			in:   `{"account":{"uid":"u3"},"auth":{"accessToken":"a","refreshToken":"r"}}`,
			want: []string{"u3"},
		},
		{
			name: "single flat camel",
			in:   `{"uid":"u4","accessToken":"a","refreshToken":"r","expiresAt":1795097981}`,
			want: []string{"u4"},
		},
		{
			name: "envelope accounts",
			in:   `{"accounts":[{"uid":"u5","access_token":"a","refresh_token":"r"}]}`,
			want: []string{"u5"},
		},
		{
			name: "dict keyed by uid",
			in:   `{"u6":{"refresh_token":"r","domain":"www.workbuddy.ai"}}`,
			want: []string{"u6"},
		},
		{
			name: "dict of nested",
			in:   `{"u7":{"auth":{"refreshToken":"r"}}}`,
			want: []string{"u7"},
		},
	}
	for _, c := range cases {
		got, err := decodeCockpitAccounts([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %d accounts, want %d", c.name, len(got), len(c.want))
		}
		for i := range c.want {
			if got[i].UID != c.want[i] {
				t.Errorf("%s: uid[%d] = %q, want %q", c.name, i, got[i].UID, c.want[i])
			}
		}
	}
}

// TestDecodeCockpitAccountsNestedFields 校验嵌套形态的字段映射：
// uid/nickname/enterpriseId 在 account 段，token/domain/expiresAt 在 auth 段。
func TestDecodeCockpitAccountsNestedFields(t *testing.T) {
	in := `{"account":{"uid":"u9","nickname":"账号18","enterpriseId":"ent-1"},
	         "auth":{"accessToken":"AT","refreshToken":"RT","expiresAt":1795097981,"domain":"www.codebuddy.cn"}}`
	got, err := decodeCockpitAccounts([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d accounts, want 1", len(got))
	}
	a := got[0]
	if a.UID != "u9" || a.Nickname != "账号18" {
		t.Errorf("account 段映射错误: uid=%q nickname=%q", a.UID, a.Nickname)
	}
	if a.AccessToken != "AT" || a.RefreshToken != "RT" {
		t.Errorf("auth 段 token 映射错误: at=%q rt=%q", a.AccessToken, a.RefreshToken)
	}
	if a.Domain != "www.codebuddy.cn" {
		t.Errorf("domain = %q, want www.codebuddy.cn", a.Domain)
	}
	if a.EnterpriseIDCamel != "ent-1" {
		t.Errorf("enterpriseId = %q, want ent-1", a.EnterpriseIDCamel)
	}
	if a.ExpiresAt != 1795097981 {
		t.Errorf("expiresAt = %d, want 1795097981（解码层不做单位换算）", a.ExpiresAt)
	}
}

// TestDecodeCockpitAccountsKeepsBadItems 认得出结构但缺字段的条目不能被解码层吞掉，
// 否则 skipped 计数与前端提示都会对不上。
func TestDecodeCockpitAccountsKeepsBadItems(t *testing.T) {
	got, err := decodeCockpitAccounts([]byte(`[{"uid":"ok","access_token":"a","refresh_token":"r"},{"hello":"world"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2（坏条目应保留占位）", len(got))
	}
	if got[1].RefreshToken != "" {
		t.Errorf("第二条应为空占位")
	}
}

// TestDecodeCockpitAccountsRejects 认不出的结构必须报错，让接口回 400 而不是静默 0 条。
func TestDecodeCockpitAccountsRejects(t *testing.T) {
	for _, in := range []string{``, `not json`, `{"hello":"world"}`, `42`} {
		if _, err := decodeCockpitAccounts([]byte(in)); err == nil {
			t.Errorf("input %q: want error, got nil", in)
		}
	}
}

func TestNormalizeExpiresAtMillis(t *testing.T) {
	cases := []struct{ in, want int64 }{
		{0, 0},
		{-5, 0},
		{1795097981, 1795097981000},    // 秒 → 毫秒
		{1795097981000, 1795097981000}, // 已是毫秒
	}
	for _, c := range cases {
		if got := normalizeExpiresAtMillis(c.in); got != c.want {
			t.Errorf("normalizeExpiresAtMillis(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
