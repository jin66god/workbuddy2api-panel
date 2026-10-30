package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// mcfFake 假上游：任务列表只下发 codes 里的 code，其余请求一律 200 空信封。
// 记录所有请求的 path+body，供断言「没有为不存在的任务打上游」。
type mcfFake struct {
	codes []string
	mu    sync.Mutex
	seen  []string
	mpErr bool // true = mp 口径任务列表回 500
}

func (f *mcfFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf strings.Builder
		buf.WriteString(r.URL.Path)
		mp := r.Header.Get("X-Client-Platform") != ""
		if mp {
			buf.WriteString(" mp")
		}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			buf.WriteString(" ")
			buf.Write(b)
		}
		f.mu.Lock()
		f.seen = append(f.seen, buf.String())
		mpErr := f.mpErr
		f.mu.Unlock()

		if strings.Contains(r.URL.Path, "/growth/tasks") {
			if mp && mpErr {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			tasks := make([]any, 0, len(f.codes))
			for _, c := range f.codes {
				tasks = append(tasks, map[string]any{"task_code": c, "current": 0, "target": 1})
			}
			body, _ := json.Marshal(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{"tasks": tasks}})
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}
}

func mcfPanel(t *testing.T, f *mcfFake) (*Panel, *auth.Auth) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	a := &auth.Auth{UID: "cn-1", Nickname: "cn", AccessToken: "at"}
	pl := pool.New("")
	pl.Add(a)
	return New(Config{Pool: pl, Upstream: up}), a
}

// TestRunAutoAllSkipsCodesAccountDoesNotHave 账号任务列表里没有的 code 不再打上游
// （线上 26 个 CN 号每轮为 4 个已下架的 code 白打 100+ 次调用），但结果行照旧是 skipped。
func TestRunAutoAllSkipsCodesAccountDoesNotHave(t *testing.T) {
	old := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = old })

	f := &mcfFake{codes: []string{"chat_5"}}
	p, a := mcfPanel(t, f)
	res := p.runAutoAll(a)

	var sawSkipped bool
	for _, r := range res {
		if r["task_code"] == "create_canvas" {
			sawSkipped = r["status"] == "skipped" && r["message"] == "该账号没有此任务"
		}
	}
	if !sawSkipped {
		t.Errorf("create_canvas 应输出 skipped 行，实际结果 %+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.seen {
		if strings.Contains(req, "create_canvas") {
			t.Fatalf("不该为账号没有的任务打上游: %s", req)
		}
	}
}

// TestRunAutoAllNoFilterWhenListFails mp 列表读失败时不能按默认口径筛选：
// 默认列表不含 mp 专属任务（Sequential_Tasks_1 / school_season），拿它当全集会把
// mp 任务整片误跳过。一次读失败只该让本轮退化成旧行为（全部照旧跑）。
func TestRunAutoAllNoFilterWhenListFails(t *testing.T) {
	old := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = old })

	f := &mcfFake{codes: []string{"chat_5"}, mpErr: true}
	p, a := mcfPanel(t, f)
	res := p.runAutoAll(a)
	if len(res) == 0 {
		t.Fatal("列表失败时仍应逐项执行并输出结果")
	}
	for _, r := range res {
		if r["task_code"] == "create_canvas" {
			if r["status"] == "skipped" && r["message"] == "该账号没有此任务" {
				t.Fatalf("mp 列表失败时不该按默认口径筛选（create_canvas 被误跳）")
			}
		}
	}
}
