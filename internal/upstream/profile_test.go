// profile_test.go 钉住账号资料拉取（issue #94）：Bearer + web 平台头可访问
// /console/account；只解析 nickname，uid 不一致防串号；业务错误原样返回。
package upstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestFetchAccountProfile(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/console/account") {
			return nil, errors.New("wrong path")
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("x-client-platform") != "web" {
			return nil, errors.New("missing bearer / web platform header")
		}
		// 响应刻意带 phoneNumber：方法必须只解析 nickname/uid，敏感字段不得进入返回值。
		return jsonResp(200, `{"code":0,"msg":"OK","data":{"uid":"u1","nickname":"新名字","phoneNumber":"13800000000"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	nick, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil || nick != "新名字" {
		t.Fatalf("nick=%q err=%v, want 新名字 nil", nick, err)
	}
}

func TestFetchAccountProfileUIDMismatch(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":0,"data":{"uid":"someone-else","nickname":"x"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"}); err == nil {
		t.Fatal("uid 不一致应报错（防串号）")
	}
}

func TestFetchAccountProfileBusinessError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(401, `{"code":1002,"msg":"unauthorized"}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "bad"}); err == nil {
		t.Fatal("401 应返回错误")
	}
}

// TestFetchAccountProfileFlatShape 顶层平铺形态 {uid,nickname}（无 code/data 信封）。
// 实测该接口两种形态都出现过；信封解析后 Data 为空时必须回落到原始响应体，否则
// 平铺响应会被当成「空对象」→ 昵称恒空 → 整批刷新零更新且零日志。
func TestFetchAccountProfileFlatShape(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"uid":"u1","nickname":"平铺名字","phoneNumber":"13800000000"}`), nil
	})
	c.WebBaseCN = "https://web.example"
	nick, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil || nick != "平铺名字" {
		t.Fatalf("nick=%q err=%v, want 平铺名字 nil", nick, err)
	}
}

// TestFetchAccountProfileEmptyNickname 空/空白/缺失昵称必须按失败返回。
// 否则调用方 SetNickname("") 返回 false：既不算更新也不算失败，整批拿不到昵称时
// 日志一行都不打（看起来像什么都没发生）。
func TestFetchAccountProfileEmptyNickname(t *testing.T) {
	cases := []string{
		`{"code":0,"data":{"uid":"u1","nickname":""}}`,
		`{"code":0,"data":{"uid":"u1","nickname":"   "}}`,
		`{"code":0,"data":{"uid":"u1"}}`,
		`{"uid":"u1","nickname":null}`,
	}
	for _, body := range cases {
		c := testClient(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, body), nil
		})
		c.WebBaseCN = "https://web.example"
		nick, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"})
		if err == nil {
			t.Fatalf("body=%s: 空昵称应报错，实际 nick=%q", body, nick)
		}
	}
}
