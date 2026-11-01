package upstream

import (
	"testing"
	"time"
)

func TestQueueRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
		ok   bool
	}{
		{"6020 实测体", `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`, 40 * time.Second, true},
		{"字符串秒", `{"code":6020,"data":{"retry_after":"15"}}`, 15 * time.Second, true},
		{"error.data 嵌套", `{"error":{"code":6020,"data":{"retry_after":8}}}`, 8 * time.Second, true},
		{"6021 无该字段", `{"code":6021,"msg":"queue.full.title"}`, 0, false},
		{"零值不算明示", `{"code":6020,"data":{"retry_after":0}}`, 0, false},
		{"非 JSON", `queue.waiting.title`, 0, false},
		{"空体", ``, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := QueueRetryAfter([]byte(c.body))
			if ok != c.ok || got != c.want {
				t.Fatalf("QueueRetryAfter = %v/%v, want %v/%v", got, ok, c.want, c.ok)
			}
		})
	}
}
