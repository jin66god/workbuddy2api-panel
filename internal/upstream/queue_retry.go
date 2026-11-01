package upstream

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// QueueRetryAfter 从渠道级排队响应体里取上游明示的等待时长。
//
// 6020 的响应形如：
//
//	{"code":6020,"msg":"queue.waiting.title","data":{"queue_position":2502,
//	 "queue_size":"999+","retry_after":40,"estimated_wait":12510}}
//
// 这个值是「整条通道多久后重试」的口径（队列按 client_user_type 全局共享），
// 与账号无关。只读 body、不读响应头：Go 的 http 头在本仓没有该字段的来源，
// 而 body 是上游唯一明确下发的地方。
//
// 兼容数字与字符串两种写法，并在 error.data 嵌套形态里同样查找（部分网关
// 把业务体包在 {"error":{"data":{...}}} 里）。找不到或非法返回 false。
func QueueRetryAfter(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return 0, false
	}
	sec, ok := findRetryAfter(v, 0)
	if !ok || sec <= 0 {
		return 0, false
	}
	return time.Duration(sec * float64(time.Second)), true
}

// findRetryAfter 递归找 retry_after（限制深度，避免异常嵌套）。
func findRetryAfter(v any, depth int) (float64, bool) {
	if depth > 4 {
		return 0, false
	}
	switch t := v.(type) {
	case map[string]any:
		if raw, ok := t["retry_after"]; ok {
			if f, ok := retryAfterSeconds(raw); ok {
				return f, true
			}
		}
		for _, child := range t {
			if f, ok := findRetryAfter(child, depth+1); ok {
				return f, true
			}
		}
	case []any:
		for _, child := range t {
			if f, ok := findRetryAfter(child, depth+1); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func retryAfterSeconds(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}
