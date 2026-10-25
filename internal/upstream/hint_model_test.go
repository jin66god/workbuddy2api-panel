package upstream

import "testing"

// NoHealthyAccountHintForModel 只在「确认另一域能服务」时才改写文案；参数缺失一律
// 退回固定文案，不对能力做任何断言。
func TestNoHealthyAccountHintForModel(t *testing.T) {
	want := `model "glm-5.3" is only available in the global realm; retry with "global:glm-5.3"`
	if got := NoHealthyAccountHintForModel("glm-5.3", "global"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	for _, c := range []struct{ bare, other string }{
		{"glm-5.3", ""},
		{"", "global"},
	} {
		if got := NoHealthyAccountHintForModel(c.bare, c.other); got != noHealthyHint {
			t.Errorf("NoHealthyAccountHintForModel(%q,%q) = %q, want fallback", c.bare, c.other, got)
		}
	}
}
