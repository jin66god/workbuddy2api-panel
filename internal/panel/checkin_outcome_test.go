package panel

import (
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestCheckinOutcome 断言面板回执四态的映射：null=真签到、幂等拒绝=already、
// 其它错误=failed，且文案只取上游 msg，不带「upstream client (http …)」前缀。
func TestCheckinOutcome(t *testing.T) {
	state, msg := checkinOutcome(nil)
	if state != "checked" || msg != "" {
		t.Fatalf("nil => %q/%q want checked/空", state, msg)
	}

	state, msg = checkinOutcome(&upstream.Error{Kind: upstream.ErrClient, Status: 400, Msg: "今天已签到，请明天再来"})
	if state != "already" {
		t.Fatalf("幂等拒绝 => %q want already", state)
	}
	if msg != "今天已签到，请明天再来" || strings.Contains(msg, "upstream client") {
		t.Fatalf("already msg=%q 应为上游原文、不含 error 前缀", msg)
	}

	state, msg = checkinOutcome(&upstream.Error{Kind: upstream.ErrClient, Status: 400, Msg: "签到失败，已签到天数不足 7 天"})
	if state != "failed" {
		t.Fatalf("失败文案 => %q want failed（不得误判成 already）", state)
	}
	if strings.Contains(msg, "upstream client") {
		t.Fatalf("failed msg=%q 不应塞整段 error 原文", msg)
	}

	// 非 *upstream.Error 的传输层错误回落 err.Error()，不丢信息。
	state, msg = checkinOutcome(errFake{})
	if state != "failed" || msg != "dial tcp: timeout" {
		t.Fatalf("传输错误 => %q/%q want failed/原文", state, msg)
	}
}

type errFake struct{}

func (errFake) Error() string { return "dial tcp: timeout" }
