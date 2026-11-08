// stream_truncation_terminal_test.go 上游断流的终态兜底（84a48d9e 语义移植）。
//
// 背景：上游既没给 finish_reason、也没发 data: [DONE] 就 EOF（连接说到一半断了）时，
// 此前 StreamHint 只补一个 [DONE] 就当成功结束：客户端看到「内容 + 正常结束」但
// finish_reason 为空，Agent 侧表现就是「说到一半停了、也不报错」，只能人工接着催。
// 现在补一帧 finish_reason=length，让客户端自己决定续写/重试。
package upstream

import (
	"bytes"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// finishReasonCount 统计出站 body 里 finish_reason 出现的次数（含 null 形态不计）。
func finishReasonCount(body, want string) int {
	n := 0
	for _, part := range strings.Split(body, `"finish_reason":"`)[1:] {
		if strings.HasPrefix(part, want) {
			n++
		}
	}
	return n
}

// 上游断流（无 finish_reason、无 [DONE]）→ 必须补且只补一帧截断终态。
func TestStreamInterruptedEmitsTruncationTerminal(t *testing.T) {
	raw := ": keepalive\n\n" +
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"半句"}}]}` + "\n\n"
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	body := rec.Body.String()
	if got := finishReasonCount(body, TruncationFinishReason); got != 1 {
		t.Fatalf("断流应恰好补 1 帧 finish_reason=%s，实际 %d 帧，body=%s", TruncationFinishReason, got, body)
	}
	if TruncationFinishReason != "length" {
		t.Fatalf("截断终态必须是 length（stop 等于把断流伪装成正常收尾），实际 %q", TruncationFinishReason)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("[DONE] 必须是最后一帧，body=%s", body)
	}
}

// 正常收尾（上游给了 finish_reason）→ 不得多补一帧，否则两个终态后一个会洗掉前一个。
func TestStreamNormalDoneNoExtraTerminal(t *testing.T) {
	raw := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"整句"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	body := rec.Body.String()
	if got := finishReasonCount(body, "stop"); got != 1 {
		t.Fatalf("正常收尾应恰好 1 帧 stop，实际 %d，body=%s", got, body)
	}
	if got := finishReasonCount(body, TruncationFinishReason); got != 0 {
		t.Fatalf("正常收尾不得补截断终态，实际 %d 帧，body=%s", got, body)
	}
}

// 上游给了 finish_reason 但没发 [DONE]（部分渠道如此）→ 也不算断流，不得补帧。
func TestStreamFinishWithoutDoneNoExtraTerminal(t *testing.T) {
	raw := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"整句"},"finish_reason":"stop"}]}` + "\n\n"
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if got := finishReasonCount(rec.Body.String(), TruncationFinishReason); got != 0 {
		t.Fatalf("已给终态的流不得补截断终态，实际 %d 帧", got)
	}
}

// 客户端已断开时不打「上游未给终态」WARN（上游只是被动跟随），但终态照补。
func TestStreamTruncationSilentWhenClientGone(t *testing.T) {
	raw := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"半句"}}]}` + "\n\n"
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	rec := httptest.NewRecorder()
	err := StreamHint(rec, strings.NewReader(raw), nil, WithClientGoneFn(func() bool { return true }))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if strings.Contains(buf.String(), "上游未给终态") {
		t.Fatalf("客户端已断开却仍记上游断流 WARN：%s", buf.String())
	}
	if got := finishReasonCount(rec.Body.String(), TruncationFinishReason); got != 1 {
		t.Fatalf("静默只针对日志，终态仍必须补，实际 %d 帧", got)
	}
}

// 客户端仍在等时必须留下 WARN（否则真正的上游断流无迹可查）。
func TestStreamTruncationWarnsWhenClientWaiting(t *testing.T) {
	raw := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"半句"}}]}` + "\n\n"
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil, WithClientGoneFn(func() bool { return false })); err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if !strings.Contains(buf.String(), "上游未给终态") {
		t.Fatalf("客户端仍在等时必须记 WARN，log=%s", buf.String())
	}
}

// 空流（0 有效帧）不得走截断终态分支：那是上游缺陷，走首帧否决/error 帧。
func TestStreamEmptyNoTruncationTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	err := StreamHint(rec, strings.NewReader(": keepalive\n\n"), nil)
	if !IsEmptyStreamError(err) {
		t.Fatalf("空流应返回 errEmptyStream，实际 %v", err)
	}
	if got := finishReasonCount(rec.Body.String(), TruncationFinishReason); got != 0 {
		t.Fatalf("空流不得补截断终态，body=%s", rec.Body.String())
	}
}
