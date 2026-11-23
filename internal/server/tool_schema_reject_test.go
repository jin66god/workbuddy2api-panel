package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestToolSchemaRejectedIsRequestLevel 钉住「工具定义/工具历史被上游拒收」这一类
// 确定性请求错误：立即回 400 + 明确 code，**不轮转、不罚号**。
//
// 回归的是这样一条链路：11129（schema 形态不合规）/ 11152（工具名不合规或重名）/
// 11148（tool_calls 与 tool 结果不匹配）/ 11133（模型供应商拒绝参数）此前都没登记，
// 统一落 Classify 的通用 4xx 兜底 → ErrClient → 每个健康号白记一笔连败（达阈降权
// 出池）并把整池轮转一遍，末端 503 no_healthy_account。客户端拿到「账号暂时不可用、
// 稍后重试」，对必然失败的请求反复重发；运维按 503 去查账号，查到的却是一池健康号。
//
// 判据三条缺一不可：状态码 400（不是 503）、code 指明是请求侧问题、上游只被打一次
// （轮转没发生）。罚号用 SetDegrade(1) 取证：阈值压到 1 后，只要喂过连败，下一次
// Pick 就会拿不到这个号。
func TestToolSchemaRejectedIsRequestLevel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{
			name:     "11129 schema 形态不合规",
			status:   400,
			body:     `{"code":11129,"msg":"invalid_function_call_parameters: tools[0].parameters.type: null","displayTips":{"zh":"工具定义不合规"}}`,
			wantCode: "bad_params",
		},
		{
			name:     "11152 工具名不合规",
			status:   400,
			body:     `{"code":11152,"msg":"duplicated tool name: fs.read","displayTips":{"zh":"工具名称不规范"}}`,
			wantCode: "bad_params",
		},
		{
			name:     "11148 tool 结果不匹配",
			status:   400,
			body:     `{"code":11148,"msg":"tool calls and tool results do not match"}`,
			wantCode: "tool_call_mismatch",
		},
		{
			name:     "11103 模型后端未启用",
			status:   400,
			body:     `{"code":11103,"msg":"Backend [deepseek-v4] is not supported"}`,
			wantCode: "model_param_invalid",
		},
		{
			name:     "11133 参数被模型拒绝",
			status:   400,
			body:     `{"code":11133,"msg":"model_param_invalid: max_tokens below minimum"}`,
			wantCode: "model_param_invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 两个号：若发生轮转，上游会被打两次；阈值压到 1 用于探查连败喂入。
			p := testPoolWith(
				&auth.Auth{UID: "u1", AccessToken: "t1", ExpiresAt: 9999999999},
				&auth.Auth{UID: "u2", AccessToken: "t2", ExpiresAt: 9999999999},
			)
			p.SetDegrade(1, time.Hour, time.Hour)
			calls := 0
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				calls++
				return tc.status, tc.body, false
			})
			h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

			rec := httptest.NewRecorder()
			body := `{"model":"cn:m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

			if rec.Code != 400 {
				t.Fatalf("状态码 = %d，确定性请求错误必须回 400 而不是 503；body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"code":"%s"`, tc.wantCode)) {
				t.Fatalf("期望 error.code=%q；body=%s", tc.wantCode, rec.Body.String())
			}
			if calls != 1 {
				t.Fatalf("上游被调用 %d 次，请求级错误不该轮转（应为 1 次）", calls)
			}
			// 不罚号：连败阈值已压到 1，喂过 NoteFailures 就会选不出来。
			if got := p.Pick(); got == nil {
				t.Fatal("账号被降权出池：请求级错误不该喂连败计数")
			}
		})
	}
}
