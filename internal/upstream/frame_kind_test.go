package upstream

import "testing"

// TestFrameKindRequestCodes 钉住「首帧 error 帧按业务码定类」。
//
// 帧里的业务码在 error.code 上，message 措辞不保证带已知 marker：11133 的真实措辞
// 不含任何可识别文案，11151 在 Classify 里只认结构化 code，11129/11152 可能是英文
// 原文或中文 displayMsg。此前只拿 message 走 Classify，这四类都会判成普通 4xx
// （ErrClient）→ 健康号被当故障号喂连败，客户端看到 503 以为该重试。
func TestFrameKindRequestCodes(t *testing.T) {
	cases := []struct {
		payload string
		want    ErrKind
	}{
		{`{"error":{"code":11129,"message":"bad tool schema"}}`, ErrBadParams},
		{`{"error":{"code":"11129","message":"x"}}`, ErrBadParams},
		{`{"error":{"code":11152,"message":"duplicated tool name"}}`, ErrBadParams},
		{`{"error":{"code":11148,"message":"tool calls and tool results do not match"}}`, ErrToolMismatch},
		{`{"error":{"code":11133,"message":"something the markers do not know"}}`, ErrModelParamInvalid},
		{`{"error":{"code":11151,"message":"a message has empty content"}}`, ErrEmptyContent},
		{`{"error":{"code":11115,"message":"prompt is too long"}}`, ErrPromptTooLong},
		// 未登记的码走 message 兜底：保持既有行为（判不出 → ErrNone，不编造）。
		{`{"error":{"code":99999,"message":""}}`, ErrNone},
		// 结构不合法 / 无 error 对象 → ErrNone（防御路径）。
		{`not json`, ErrNone},
	}
	for _, c := range cases {
		if got := FrameKind(c.payload); got != c.want {
			t.Errorf("FrameKind(%s) = %v want %v", c.payload, got, c.want)
		}
	}
}
