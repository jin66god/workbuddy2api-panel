package upstream

import (
	"strings"
	"testing"
)

// TestGatewayHintRequestLevelModelCodes 钉住三个「模型级/请求级」业务码的对外指引：
// 客户端要做的事各不相同，写反了会白跑一轮。
//
//   - 11103 后端未启用 → 换模型（换号无用）。必须盖过 11133 的通用「参数被拒」文案，
//     否则客户端会去改参数；
//   - 11102 模型 id 不存在 → 按 /v1/models 抄 id（id 大小写敏感），换号无用；
//   - 11158 账号类型无权限 → 网关会换有权限的号，客户端不必改请求。
func TestGatewayHintRequestLevelModelCodes(t *testing.T) {
	cases := []struct {
		name     string
		kind     ErrKind
		msg      string
		mustHave string
		mustNot  string
	}{
		{
			name:     "11103 后端未启用",
			kind:     ErrModelParamInvalid,
			msg:      `{"code":11103,"msg":"Backend [deepseek-v4] is not supported"}`,
			mustHave: "backend is not enabled upstream",
			mustNot:  "parameters were rejected",
		},
		{
			name:     "11102 模型 id 不存在",
			kind:     ErrModelMissing,
			msg:      `{"code":11102,"msg":"model [DeepSeek-V4.1-Flash] service info not found"}`,
			mustHave: "switching accounts will not help",
		},
		{
			name:     "11158 账号类型无权限",
			kind:     ErrModelBlocked,
			msg:      `{"code":11158,"msg":"model [gpt-5.6-sol] is not allowed for user type [personal_free_user]"}`,
			mustHave: "accounts with permission",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GatewayHint(c.kind, c.msg, HintContext{})
			if !strings.Contains(got, c.mustHave) {
				t.Fatalf("hint = %q，必须包含 %q", got, c.mustHave)
			}
			if c.mustNot != "" && strings.Contains(got, c.mustNot) {
				t.Fatalf("hint = %q，不该包含 %q", got, c.mustNot)
			}
		})
	}
}
