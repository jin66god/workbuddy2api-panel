package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// ctxBlockingReader 阻塞到 ctx 取消再返回 ctx.Err()：模拟「上游正文还在流、客户端先断开」。
type ctxBlockingReader struct{ ctx context.Context }

func (r ctxBlockingReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// TestChatSyncClientCancelIsNotUpstreamParse 钉住客户端断开的分支：上游正文流到一半、
// 客户端先掐断（前置 CDN 回源超时 / AsyncOpenAI 类客户端 timeout=15s / 用户点停止）时，
// 不再记 502 upstream_parse —— 上游当时在正常输出，网关没出错。
// 线上一天 11 次这类 502，全部恰好 total=15.0s、tok=-、do=1~3s，被统计成了上游故障。
// 断言三件事：不写响应体（连接已关）、日志归「调用方断开」而非解析失败、handler 不卡死。
func TestChatSyncClientCancelIsNotUpstreamParse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	var once sync.Once
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			once.Do(func() { close(entered) })
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(ctxBlockingReader{ctx}),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	var buf strings.Builder
	old := log.Writer()
	log.SetOutput(&buf)
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.SetOutput(old)
		t.Fatal("handler 未在 ctx 取消后返回（卡在读上游）")
	}
	log.SetOutput(old)
	if rec.Body.Len() != 0 {
		t.Fatalf("客户端已断开，不该还写响应体: %s", rec.Body.String())
	}
	out := buf.String()
	if strings.Contains(out, "upstream parse failed") {
		t.Fatalf("客户端断开不该记成上游解析失败: %s", out)
	}
	if !strings.Contains(out, "调用方断开") {
		t.Fatalf("应落一条调用方断开的日志: %s", out)
	}
	if !strings.Contains(out, "EdgeOne") {
		t.Fatalf("日志应点出 CDN 回源超时这条真实成因: %s", out)
	}
	if !strings.Contains(out, "context canceled") {
		t.Fatalf("断开原因原文应留在日志里: %s", out)
	}
}
