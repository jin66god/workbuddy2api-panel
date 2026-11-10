package panel

import "testing"

// TestDecodeCockpitEnvelopeObjectNotUid 信封的值是**单个账号对象**时：
//   - 有真 uid 的照常导入（别把 uid 写成信封名）；
//   - 没有 uid 的不能把信封键（accounts/data/...）兜底成 uid —— 那会在账号池里
//     造出一个格式合法、内容垃圾的号，还会真的去 refresh 一次。
//
// 这是手工变体：把单账号详情再包一层 `{"accounts": {...}}`。
func TestDecodeCockpitEnvelopeObjectNotUid(t *testing.T) {
	type want struct {
		uids []string
		err  bool
	}
	cases := []struct {
		name string
		in   string
		want want
	}{
		{
			name: "信封值是对象且带 uid",
			in:   `{"data":{"uid":"u1","refresh_token":"r"}}`,
			want: want{uids: []string{"u1"}},
		},
		{
			// 解出「空 uid 占位」而不是报错：主循环按 missing required fields 记账，
			// 用户能看到「哪一条被跳过」；报错则整批都进不来。
			name: "信封值是对象且无 uid",
			in:   `{"accounts":{"refresh_token":"r"}}`,
			want: want{uids: []string{""}},
		},
		{
			name: "嵌套形态包一层信封",
			in:   `{"items":{"account":{"uid":"u2"},"auth":{"accessToken":"a"}}}`,
			want: want{uids: []string{"u2"}},
		},
		{
			name: "items 信封无 uid",
			in:   `{"items":{"refreshToken":"r"}}`,
			want: want{uids: []string{""}},
		},
	}
	for _, c := range cases {
		got, err := decodeCockpitAccounts([]byte(c.in))
		if c.want.err {
			if err == nil {
				t.Errorf("%s: 应报「认不出」，却解出 %d 条: %+v", c.name, len(got), got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 不应报错: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want.uids) {
			t.Errorf("%s: 解出 %d 条 want %d", c.name, len(got), len(c.want.uids))
			continue
		}
		for i, u := range c.want.uids {
			if got[i].UID != u {
				t.Errorf("%s: uid[%d]=%q want %q", c.name, i, got[i].UID, u)
			}
			if isCockpitEnvelopeKey(got[i].UID) {
				t.Errorf("%s: uid 被写成信封名 %q", c.name, got[i].UID)
			}
		}
	}

	// 正常字典不能被误伤：键就是 uid。
	got, err := decodeCockpitAccounts([]byte(`{"19800000001":{"refresh_token":"r"}}`))
	if err != nil {
		t.Fatalf("正常字典应能解: %v", err)
	}
	if len(got) != 1 || got[0].UID != "19800000001" {
		t.Fatalf("正常字典解析错: %+v", got)
	}
}
