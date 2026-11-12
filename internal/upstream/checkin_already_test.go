package upstream

import "testing"

// TestIsAlreadyCheckinFailureMarkerWins 断言「失败」类文案压过「已签到」关键词。
//
// 回归位：「签到失败，已签到天数不足 7 天」同时含「已签到」，旧逻辑会判成幂等
// 成功、把 checkin_done 置真，面板显示「今日已签」，实际是失败（少一次签到）。
func TestIsAlreadyCheckinFailureMarkerWins(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"签到失败，已签到天数不足 7 天", false},
		{"签到失败，请稍后重试", false},
		{"企业账号不支持该操作", false},
		{"签到活动未开启或已过期", false},
		{"今天已签到，请明天再来", true},
		{"今日已签到", true},
		{"already checked in today", true},
	}
	for _, c := range cases {
		err := &Error{Kind: ErrClient, Status: 400, Msg: c.msg}
		if got := IsAlreadyCheckin(err); got != c.want {
			t.Errorf("%q => %v want %v", c.msg, got, c.want)
		}
	}
}
