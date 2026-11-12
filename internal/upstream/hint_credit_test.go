package upstream

import (
	"strings"
	"testing"
)

// TestHardCreditHintRealmNeutral 断言额度耗尽提示不再承诺「等签到恢复」。
//
// 国际版账号没有签到活动（排程日志逐日「国际版签到活动未开启，跳过」），
// 积分只能靠充值/订阅恢复；旧文案对 global 号持有者是一句永远不会兑现的承诺。
func TestHardCreditHintRealmNeutral(t *testing.T) {
	got := GatewayHint(ErrHardCredit, "hard credit limit reached", HintContext{})
	if strings.Contains(got, "waiting for daily check-in") {
		t.Fatalf("hint 仍在承诺等签到恢复: %q", got)
	}
	if !strings.Contains(got, "CN") || !strings.Contains(got, "global") {
		t.Fatalf("hint 未区分 CN / global 两条恢复路径: %q", got)
	}
}
