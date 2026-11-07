package upstream

import (
	"net/http"
	"testing"
	"time"
)

// 11134 是 provider 侧过载：必须与通用 5xx（ErrServer → NoteError 熔断）分开，
// 且与 6020/6021 同族（ErrUpstreamBusy：不罚号、不轮转、带等待口径）。
func TestClassifyProviderOverload(t *testing.T) {
	cases := []struct {
		name string
		st   int
		body string
		want ErrKind
	}{
		{
			name: "11134 结构化 code（真实抓包形态）",
			st:   http.StatusInternalServerError,
			body: `{"code":11134,"msg":"the model provider is temporarily unavailable, please retry later or switch to another model","requestId":"f3a011ef","retry_policy":{"retry":true,"retry_after":40},"extError":{"code":"internal_error"}}`,
			want: ErrUpstreamBusy,
		},
		{
			name: "只有固定英文文案（上游换状态码）",
			st:   http.StatusServiceUnavailable,
			body: `{"msg":"The model provider is temporarily unavailable"}`,
			want: ErrUpstreamBusy,
		},
		{
			name: "extError 夹带 rate limit 也不得误判成账号级限流",
			st:   http.StatusInternalServerError,
			body: `{"code":11134,"msg":"the model provider is temporarily unavailable","extError":{"code":"internal_error","msg":"rate limit hit on provider side"}}`,
			want: ErrUpstreamBusy,
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

// 11134 的等待口径在 retry_policy.retry_after（不是 data.retry_after，也没有头）。
// 网关要按上游给的秒数回 429 + Retry-After，而不是自己拍一个短值。
func TestQueueRetryAfterProviderOverload(t *testing.T) {
	body := []byte(`{"code":11134,"msg":"the model provider is temporarily unavailable","retry_policy":{"retry":true,"retry_after":40}}`)
	d, ok := QueueRetryAfter(body)
	if !ok || d != 40*time.Second {
		t.Fatalf("QueueRetryAfter = %v/%v, want 40s/true", d, ok)
	}
	// 6020 的 data.retry_after 语义不变。
	if d, ok := QueueRetryAfter([]byte(`{"code":6020,"data":{"retry_after":12}}`)); !ok || d != 12*time.Second {
		t.Fatalf("6020 retry_after = %v/%v, want 12s/true", d, ok)
	}
	// 没有等待口径时不能编造。
	if _, ok := QueueRetryAfter([]byte(`{"code":6021,"msg":"queue.full.title"}`)); ok {
		t.Fatal("无 retry_after 字段时应返回 false")
	}
}
