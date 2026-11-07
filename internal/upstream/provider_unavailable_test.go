package upstream

import (
	"net/http"
	"testing"
)

// 11134 是 provider 侧不可用：必须与通用 5xx（ErrServer → NoteError 熔断）分开，
// 否则一次上游故障会把整池健康号逐个打进 30 分钟熔断。
func TestClassifyProviderUnavailable(t *testing.T) {
	cases := []struct {
		name string
		st   int
		body string
		want ErrKind
	}{
		{
			name: "11134 结构化 code（真实抓包形态）",
			st:   http.StatusInternalServerError,
			body: `{"code":11134,"msg":"the model provider is temporarily unavailable, please retry later or switch to another model","requestId":"f3a011ef","extError":{"code":"internal_error","msg":"upstream provider down"}}`,
			want: ErrProviderUnavailable,
		},
		{
			name: "只有固定英文文案（上游换状态码）",
			st:   http.StatusServiceUnavailable,
			body: `{"msg":"The model provider is temporarily unavailable"}`,
			want: ErrProviderUnavailable,
		},
		{
			name: "extError 夹带 rate limit 也不得误判成限流",
			st:   http.StatusInternalServerError,
			body: `{"code":11134,"msg":"the model provider is temporarily unavailable","extError":{"code":"internal_error","msg":"rate limit hit on provider side"}}`,
			want: ErrProviderUnavailable,
		},
		{
			name: "无业务码的 500 仍是 ErrServer",
			st:   http.StatusInternalServerError,
			body: `{"code":99999,"msg":"internal error"}`,
			want: ErrServer,
		},
		{
			name: "6021 队列满仍是渠道级 busy（优先级不被抢）",
			st:   http.StatusTooManyRequests,
			body: `{"code":6021,"msg":"queue.full.title"}`,
			want: ErrUpstreamBusy,
		},
		{
			name: "429 普通限流不受影响",
			st:   http.StatusTooManyRequests,
			body: `{"msg":"rate limit exceeded"}`,
			want: ErrSoftRate,
		},
	}
	for _, c := range cases {
		if got := Classify(c.st, c.body); got != c.want {
			t.Fatalf("%s: Classify(%d, %s) = %v, want %v", c.name, c.st, c.body, got, c.want)
		}
	}
}

// String() 会进日志与面板（kind=provider_unavailable），别写成 none。
func TestErrProviderUnavailableString(t *testing.T) {
	if got := ErrProviderUnavailable.String(); got != "provider_unavailable" {
		t.Fatalf("String() = %q", got)
	}
}

// isCallerGoneErr 必须是双条件：ctx 还活着时，上游自己的超时仍要算真故障。
func TestIsCallerGoneErr(t *testing.T) {
	if isCallerGoneErr(nil, nil) {
		t.Fatal("nil err 不应命中")
	}
}
