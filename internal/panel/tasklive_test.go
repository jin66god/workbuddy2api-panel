// tasklive_test.go 面板「实时任务」的计数口径、口径回落与缓存语义。
//
// 三个不变量：
//   - 三类计数按 claimed / claimable / 其余 归类，与面板「扫描待办」同口径；
//   - 小程序口径失败时回落默认口径，整行不报错（能查到什么就显示什么）；
//   - 缓存 TTL 内复用、fresh=1 穿透，避免多标签页把看板变成压测。
package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type liveRT func(*http.Request) (*http.Response, error)

func (f liveRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func liveClient(rt liveRT) *upstream.Client {
	return &upstream.Client{
		HTTP:          &http.Client{Transport: rt},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

func liveResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestLiveTasksOfCounts 三类计数与 mp 口径头。
func TestLiveTasksOfCounts(t *testing.T) {
	var sawMP bool
	c := liveClient(func(r *http.Request) (*http.Response, error) {
		// 按取值认，不按「有这个头」认：桌面端身份头也会带 X-Client-Platform
		// （取值 windows），只有 miniprogram 才是小程序口径。
		if r.Header.Get("X-Client-Platform") == "miniprogram" {
			sawMP = true
		}
		// 夹具用真实字段：claimed 由 accept_status 判定，claimable 由进度达标推算，
		// 直接写 claimed/claimable 布尔是出站字段、不会被解析。
		return liveResp(200, `{"code":0,"data":{"tasks":[
			{"task_code":"done_one","accept_status":"claimed"},
			{"task_code":"claimable_one","progress":{"current":1,"target":1}},
			{"task_code":"pending_one","progress":{"current":0,"target":3}}]}}`), nil
	})
	got := liveTasksOf(c, &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	if got.Err != "" {
		t.Fatalf("不应报错: %s", got.Err)
	}
	if got.Claimed != 1 || got.Claimable != 1 || got.Pending != 1 {
		t.Fatalf("计数 claimed=%d claimable=%d pending=%d，期望各 1", got.Claimed, got.Claimable, got.Pending)
	}
	if len(got.Tasks) != 3 {
		t.Fatalf("任务条数 %d 期望 3", len(got.Tasks))
	}
	if !sawMP {
		t.Fatalf("第一次读取应带小程序口径头")
	}
}

// TestLiveTasksOfFallsBackToDefault mp 口径失败时回落默认口径，而不是整行报错。
func TestLiveTasksOfFallsBackToDefault(t *testing.T) {
	c := liveClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Client-Platform") == "miniprogram" {
			return liveResp(400, `{"code":400,"msg":"unsupported"}`), nil
		}
		return liveResp(200, `{"code":0,"data":{"tasks":[{"task_code":"only_default"}]}}`), nil
	})
	got := liveTasksOf(c, &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	if got.Err != "" {
		t.Fatalf("回落成功后不应报错: %s", got.Err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].TaskCode != "only_default" {
		t.Fatalf("回落结果不符: %+v", got.Tasks)
	}
}

// TestLiveTasksOfSkipsNonPersonal 企业版 / API Key 账号不发上游请求，只给说明。
func TestLiveTasksOfSkipsNonPersonal(t *testing.T) {
	hits := 0
	c := liveClient(func(r *http.Request) (*http.Response, error) {
		hits++
		return liveResp(200, `{"code":0,"data":{"tasks":[]}}`), nil
	})
	got := liveTasksOf(c, &auth.Auth{UID: "u1", AccessToken: "at1", EnterpriseID: "e1", ExpiresAt: 9999999999})
	if got.Err != taskLiveNoPersonal {
		t.Fatalf("期望说明文案 %q，实际 %q", taskLiveNoPersonal, got.Err)
	}
	if hits != 0 {
		t.Fatalf("不应发上游请求，实际 %d 次", hits)
	}
}

// TestTasksLiveCache 首次穿透、TTL 内复用、fresh=1 再穿透。
func TestTasksLiveCache(t *testing.T) {
	taskLive = &liveCache{}
	p := New(Config{Version: "test", Pool: pool.New(""), Upstream: liveClient(func(r *http.Request) (*http.Response, error) {
		return liveResp(200, `{"code":0,"data":{"tasks":[]}}`), nil
	})})

	call := func(q string) map[string]any {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/api/tasks/live"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		return out
	}

	if got := call(""); got["cached"] != false {
		t.Fatalf("首次应为 cached=false，实际 %v", got["cached"])
	}
	if got := call(""); got["cached"] != true {
		t.Fatalf("TTL 内应命中缓存，实际 %v", got["cached"])
	}
	if got := call("?fresh=1"); got["cached"] != false {
		t.Fatalf("fresh=1 应穿透，实际 %v", got["cached"])
	}
	if got := call(""); got["ttl_sec"].(float64) != float64(int(taskLiveTTL.Seconds())) {
		t.Fatalf("ttl_sec=%v 期望 %d", got["ttl_sec"], int(taskLiveTTL.Seconds()))
	}
}

// TestTasksLiveNoUpstream 未配置账号池/上游时返回 501，而不是在子 goroutine 里
// nil 解引用（那会直接把进程带走，net/http 的 recover 拦不住）。
func TestTasksLiveNoUpstream(t *testing.T) {
	taskLive = &liveCache{}
	p := New(Config{Version: "test", Pool: pool.New("")})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/api/tasks/live", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code=%d 期望 501，body=%s", rec.Code, rec.Body.String())
	}
}
