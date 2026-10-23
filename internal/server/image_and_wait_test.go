package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 图片检测必须容忍字符串 / null content：system 文本消息用字符串，若按 part 数组
// 直接解析会让整个 Unmarshal 失败，漏掉另一条消息里的 image_url。
func TestHasImagePartStringContent(t *testing.T) {
	withImage := []byte(`{"messages":[
		{"role":"system","content":"you are helpful"},
		{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}
	]}`)
	if !hasImagePart(withImage) {
		t.Error("字符串 system + 带图 user 应判定为带图")
	}
	textOnly := []byte(`{"messages":[{"role":"system","content":"hi"},{"role":"user","content":"纯文本"}]}`)
	if hasImagePart(textOnly) {
		t.Error("纯文本不应判定为带图")
	}
	nullContent := []byte(`{"messages":[{"role":"tool","content":null},{"role":"user","content":"x"}]}`)
	if hasImagePart(nullContent) {
		t.Error("null content 不应 panic 或误判")
	}
}

// 14003 的「每请求只等一次」额度不能被负向抖动白花：等待时长必须 >= 解封时长。
func TestWaitPastNeverEarly(t *testing.T) {
	d := 8 * time.Second
	for i := 0; i < 500; i++ {
		got := waitPast(d)
		if got < d {
			t.Fatalf("waitPast(%v)=%v 提前醒来（第 %d 次）", d, got, i)
		}
		if got > d+d/4+time.Millisecond {
			t.Fatalf("waitPast(%v)=%v 超出正向抖动上限", d, got)
		}
	}
	if waitPast(0) != 0 {
		t.Error("d<=0 应返回 0")
	}
}

// 轮转耗尽（MaxRotate=1）也必须报 503 model_busy：末端补一次 modelBlock 快照，
// 否则同一件「全池该模型繁忙」的事会退化成 429 rate_limit 且没有 Retry-After。
func TestChatRotationExhaustedReportsModelBusy(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 429, `{"code":14003,"msg":"Model busy. Please switch models or retry later"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 1})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != 503 {
		t.Fatalf("code=%d body=%s want 503", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "model_busy") {
		t.Fatalf("body 应含 model_busy: %s", rec.Body)
	}
}
