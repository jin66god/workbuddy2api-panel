// isAlready 判定的正反例：已签到必须窄判，任何 400 都不能算 ALREADY。
//
// 背景：上游业务错误统一是 HTTP 400 + code=%d，旧实现宽匹配 "checkin" 与
// "code=400"，于是参数缺失、风控拦截、活动未开启这些真实失败全被统计成
// 「已签到」，每日结果看起来一切正常而实际没签上。
package main

import "testing"

func TestIsAlready(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		// 正例：上游幂等语义
		{"今天已签到", true},
		{"Already checked in", true},
		{"code=10001 msg=already signed", true},
		// 反例：真实失败不能被吞
		{"code=400 msg=missing param", false},
		{"code=400 msg=request illegal", false},
		{"checkin failed: activity not started", false},
		{"code=10002 msg=activity closed", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isAlready(c.msg); got != c.want {
			t.Errorf("isAlready(%q)=%v want %v", c.msg, got, c.want)
		}
	}
}
