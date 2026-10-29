package upstream

import (
	"net/http"
	"strings"
	"testing"
)

// cf520Body 是 Cloudflare 520 错误页的最小形态（真实页面 7KB，这里留判据字段）。
const cf520Body = `<!DOCTYPE html><html class="no-js" lang="en-US"><head>
<title>456996.xyz | 520: Web server is returning an unknown error</title></head>
<body><div id="cf-error-details"><h1>Web server is returning an unknown error
<span class="code-label">Error code 520</span></h1></div></body></html>`

// TestClassifyCloudflareGateway 520/522/524 等 CF 回源失败必须与业务 5xx 分开：
// 前者是「网关→源站」一跳坏了（换号无用、不罚号），后者仍按账号级故障处理。
func TestClassifyCloudflareGateway(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   ErrKind
	}{
		{"520 错误页", 520, cf520Body, ErrGatewayDown},
		{"522 错误页", 522, cf520Body, ErrGatewayDown},
		{"524 错误页", 524, cf520Body, ErrGatewayDown},
		{"502 但 body 是 CF 页", 502, cf520Body, ErrGatewayDown},
		{"520 但 body 是上游 JSON 信封", 520, `{"code":10000,"msg":"API request failed"}`, ErrServer},
		{"502 上游 JSON", 502, `{"code":10000,"msg":"boom"}`, ErrServer},
		{"500 纯文本", 500, `internal server error`, ErrServer},
		{"429 仍按限流", 429, `{"code":1,"msg":"rate limit"}`, ErrSoftRate},
	} {
		if got := Classify(tc.status, tc.body); got != tc.want {
			t.Fatalf("%s: Classify=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestGatewayErrorSummary 摘要必须给 host/错误码，且不能把 HTML 透给客户端。
func TestGatewayErrorSummary(t *testing.T) {
	msg := errorMsg(ErrGatewayDown, 520, cf520Body)
	if !strings.Contains(msg, "Cloudflare 520") {
		t.Fatalf("缺错误码: %q", msg)
	}
	if !strings.Contains(msg, "host=456996.xyz") {
		t.Fatalf("缺 host: %q", msg)
	}
	if strings.Contains(msg, "<!DOCTYPE") || strings.Contains(msg, "cf-error-details") {
		t.Fatalf("HTML 泄漏进 message: %q", msg)
	}
	// ray 优先从响应头补（错误页 body 通常不回显）。
	resp := &http.Response{StatusCode: 520, Header: http.Header{"Cf-Ray": []string{"a52371091b7a6368-WAW"}}}
	if got := gatewayMsgFromResp(ErrGatewayDown, resp, cf520Body); !strings.Contains(got, "ray=a52371091b7a6368-WAW") {
		t.Fatalf("Cf-Ray 未进摘要: %q", got)
	}
	// 非网关错误仍保留原文前 200 字符（不改变既有 5xx 语义）。
	if got := errorMsg(ErrServer, 500, `{"code":10000,"msg":"boom"}`); !strings.Contains(got, "boom") {
		t.Fatalf("普通 5xx 原文被改写: %q", got)
	}
}

// TestGatewayHintNotAccountFault hint 必须明确「不是账号问题」。
func TestGatewayHintNotAccountFault(t *testing.T) {
	got := GatewayHint(ErrGatewayDown, "", HintContext{})
	if !strings.Contains(got, "not an account problem") {
		t.Fatalf("hint=%q", got)
	}
}

// TestGatewayDownIsTransientForBilling 计费维护类调用（签到/余额）对 CF 回源失败
// 保留有界重试；429 等业务错误仍不重试（重试只会原样失败）。
func TestGatewayDownIsTransientForBilling(t *testing.T) {
	if !isTransientBillingErr(&Error{Kind: ErrGatewayDown}) {
		t.Fatal("ErrGatewayDown 应可重试（源站瞬时重启）")
	}
	if isTransientBillingErr(&Error{Kind: ErrSoftRate}) {
		t.Fatal("ErrSoftRate 不应重试")
	}
}
