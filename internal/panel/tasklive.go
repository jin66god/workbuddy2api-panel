// tasklive.go 面板「实时任务」：一次性读回所有账号当前的成长任务进度。
//
// 与既有两种数据源的区别：
//   - tasks/jobs：后台任务**执行结果**（要有作业跑过才有内容）；
//   - tasks/scan_all + tasks/queue：手动触发的一次性扫描，跑完才有列表。
//
// 两者都要「有人先动一下」，看板永远滞后。本文件提供**只读的实时视图**：
// 直接向每个账号查一次任务表，按 TTL 缓存后交给前端定时轮询，不需要任何手动动作。
//
// 为什么只调小程序口径一次：ListTasksMP 是默认口径的超集（实测），全量账号一轮
// 也只是一号一次请求；两个口径都调会翻倍，而结果差集为空。该口径失败（老版本 /
// 该域不支持）再退回默认口径，保证「查得到什么就显示什么」。
//
// 缓存的意义：浏览器每轮轮询都会打一次服务端，多开页面/多人看会成倍放大；TTL 内
// 直接复用，`fresh=1` 可强制穿透（手动刷新按钮用）。
package panel

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// taskLiveTTL 实时任务缓存有效期。任务进度是分钟级聚合的，再密也不会更新；
// 而全量账号一轮就是 N 次上游 GET，压得太短会把看板变成压测。
const taskLiveTTL = 45 * time.Second

// taskLiveConcurrency 一轮实时查询的账号并发（与券码/活动页同档，避免打爆上游）。
const taskLiveConcurrency = 4

// taskLiveNoPersonal 不支持个人任务体系的账号（企业版 / API Key）的说明文案。
const taskLiveNoPersonal = "该账号不支持个人成长任务（企业版 / API Key）"

// liveTaskAccount 单账号的实时任务行。
type liveTaskAccount struct {
	UID      string          `json:"uid"`
	Nickname string          `json:"nickname"`
	Realm    string          `json:"realm"`
	Tasks    []upstream.Task `json:"tasks,omitempty"`
	// Pending 未完成的任务数（前端排序用）
	Pending int `json:"pending"`
	// Claimable 可领取的任务数
	Claimable int `json:"claimable"`
	// Claimed 已领取的任务数
	Claimed int    `json:"claimed"`
	Err     string `json:"error,omitempty"`
}

// liveCache 实时任务快照缓存（进程内，单面板实例）。
type liveCache struct {
	mu   sync.Mutex
	at   time.Time
	rows []liveTaskAccount
	// inflight 同一时刻只允许一轮上游查询：多标签页同时轮询时，
	// 后来的请求等前一轮结果，而不是各自再打一遍全量账号。
	inflight chan struct{}
}

var taskLive = &liveCache{}

// tasksLive GET /panel/api/tasks/live?fresh=1
//
// 返回所有启用账号的当前任务表。账号级失败只标在对应行（error 字段），
// 不影响其它账号——一个号的 token 过期不该让整个看板空掉。
func (p *Panel) tasksLive(w http.ResponseWriter, r *http.Request) {
	// 未配置账号池或上游时直接返回 501：这条路径会在 goroutine 里读 p.cfg.Upstream，
	// nil 解引用发生在子 goroutine 里，net/http 的 recover 兜不住，会把整个进程带走。
	if p.cfg.Pool == nil || p.cfg.Upstream == nil {
		writeErr(w, http.StatusNotImplemented, "面板未配置账号池或上游客户端")
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1"

	taskLive.mu.Lock()
	// 缓存标记用 at 的零值而不是「rows 非空」：池空或全部账号失败时 rows 也是空的，
	// 用长度判定会让这两种情况每轮都重新打一遍上游（还拿不到新东西）。
	if !fresh && !taskLive.at.IsZero() && time.Since(taskLive.at) < taskLiveTTL {
		rows, at := taskLive.rows, taskLive.at
		taskLive.mu.Unlock()
		writeJSON(w, http.StatusOK, livePayload(rows, at, true))
		return
	}
	if taskLive.inflight != nil {
		// 已有一轮在跑：等它，别叠加。
		ch := taskLive.inflight
		taskLive.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(30 * time.Second):
			writeErr(w, http.StatusServiceUnavailable, "上一轮任务查询仍在进行，稍后再试")
			return
		}
		taskLive.mu.Lock()
		rows, at := taskLive.rows, taskLive.at
		taskLive.mu.Unlock()
		writeJSON(w, http.StatusOK, livePayload(rows, at, true))
		return
	}
	ch := make(chan struct{})
	taskLive.inflight = ch
	taskLive.mu.Unlock()

	// 收尾必须走 defer：采集过程中即使 panic，也要清 inflight 并关通道，
	// 否则后续每次轮询都会撞在「上一轮仍在进行」上，只能重启进程才能恢复。
	var rows []liveTaskAccount
	func() {
		defer func() {
			if r := recover(); r != nil {
				rows = nil
			}
			taskLive.mu.Lock()
			taskLive.rows = rows
			taskLive.at = time.Now()
			taskLive.inflight = nil
			taskLive.mu.Unlock()
			close(ch)
		}()
		rows = p.collectLiveTasks()
	}()

	writeJSON(w, http.StatusOK, livePayload(rows, time.Now(), false))
}

// livePayload 统一响应形状：前端按 ttl_sec 给轮询间隔做下限，按 age_sec 显示新鲜度。
func livePayload(rows []liveTaskAccount, at time.Time, cached bool) map[string]any {
	return map[string]any{
		"ok":       true,
		"cached":   cached,
		"age_sec":  int(time.Since(at).Seconds()),
		"ttl_sec":  int(taskLiveTTL.Seconds()),
		"accounts": rows,
	}
}

// collectLiveTasks 并发查所有启用账号的任务表。禁用号不查（它们不接流量，
// 查了只会白打上游并让看板出现注定过期的进度）。
func (p *Panel) collectLiveTasks() []liveTaskAccount {
	stats := p.cfg.Pool.List()
	out := make([]liveTaskAccount, len(stats))
	sem := make(chan struct{}, taskLiveConcurrency)
	var wg sync.WaitGroup

	for i, st := range stats {
		if st.Disabled {
			continue
		}
		a := p.cfg.Pool.AuthByUID(st.UID)
		if a == nil {
			continue
		}
		wg.Add(1)
		go func(i int, a *auth.Auth) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// 兜住单个账号侧的 panic：子 goroutine 里的 panic 会终止整个进程，
			// 一个坏账号不该让面板连同网关一起挂掉。
			defer func() {
				if r := recover(); r != nil {
					out[i] = liveTaskAccount{
						UID: a.UID, Nickname: a.Nickname, Realm: a.Realm(),
						Err: fmt.Sprint("读取任务表异常: ", r),
					}
				}
			}()
			out[i] = liveTasksOf(p.cfg.Upstream, a)
		}(i, a)
	}
	wg.Wait()

	res := make([]liveTaskAccount, 0, len(out))
	for _, it := range out {
		if it.UID != "" {
			res = append(res, it)
		}
	}
	return res
}

// liveTasksOf 读一个账号的任务表并统计三类计数。
//
// 只发小程序口径一次；失败再退回默认口径，避免整行报错。
func liveTasksOf(c *upstream.Client, a *auth.Auth) liveTaskAccount {
	it := liveTaskAccount{UID: a.UID, Nickname: a.Nickname, Realm: a.Realm()}
	if !a.SupportsPersonalEndpoints() {
		it.Err = taskLiveNoPersonal
		return it
	}
	tasks, err := c.ListTasksMP(a)
	if err != nil {
		tasks, err = c.ListTasks(a)
	}
	if err != nil {
		it.Err = err.Error()
		return it
	}
	it.Tasks = tasks
	for _, t := range tasks {
		switch {
		case t.Claimed:
			it.Claimed++
		case t.Claimable:
			it.Claimable++
		default:
			it.Pending++
		}
	}
	return it
}
