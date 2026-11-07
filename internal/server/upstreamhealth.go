package server

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

// 上游劣化滑窗告警（P1）。
//
// 问题：网关只在「请求失败」时说话。上游慢到不可用时，请求仍然 200、流水行照旧
// 绿着——2026-11-07 15:00-15:10 实案：那 10 分钟里 TTFB 中位 17.8s、最大 39.2s
// （全天其他时段中位 2.1-2.8s、max 6.3s），6 条请求全部 out=success，一条 WARN
// 都没有；客户端在自己的 60s 超时里连撞之后直接停摆，请求量从 100 条/30min 掉到
// 6 条/小时，监控面上完全看不出发生了什么。等到上游开始回 11134/500，才第一次
// 有日志——劣化期已经过去了。
//
// 做法：所有 chat 请求都在 chatStat.done() 收口，这里按 5 分钟滑窗采样，
// 命中「慢请求占比」或「5xx 占比」阈值就聚合打**一条** WARN，10 分钟内不重复。
// 只观测不改变任何请求路径。
const (
	upstreamHealthWindow    = 5 * time.Minute
	upstreamHealthWarnEvery = 10 * time.Minute
	upstreamHealthMinSample = 10 // 样本太少不判（避免单请求误报）
	upstreamSlowTTFB        = 8 * time.Second
	upstreamSlowRatio       = 0.30 // 慢请求占比 >=30%
	upstreamFailRatio       = 0.10 // 5xx 占比 >=10% 且至少 3 条
	upstreamMinFail         = 3
)

type upstreamSample struct {
	at   time.Time
	ttfb time.Duration
	fail bool // 5xx：上游侧失败（499/本地 4xx 不算）
}

type upstreamHealth struct {
	mu       sync.Mutex
	samples  []upstreamSample
	lastWarn time.Time
}

// chatUpstreamHealth 进程级单例：handler 只写，测试可替换。
var chatUpstreamHealth = &upstreamHealth{}

// record 采样一条 chat 请求结果。只接受两种样本：
//   - 成功且有 TTFB 观测（用于慢速比例）；
//   - 5xx（用于失败比例）。
//
// 499 / interrupted 一律丢弃：那是调用方走了，既不代表上游快也不代表上游坏，
// 混进来会同时污染慢速比例与失败比例（用户点停止就能触发告警）。
func (u *upstreamHealth) record(status int, outcome string, ttfb time.Duration) {
	if outcome == reqlog.OutcomeInterrupted || status == 499 {
		return
	}
	okSample := status >= 200 && status < 300 && ttfb > 0
	fail := status >= 500
	if !okSample && !fail {
		return
	}
	now := time.Now()

	u.mu.Lock()
	defer u.mu.Unlock()
	u.samples = append(u.samples, upstreamSample{at: now, ttfb: ttfb, fail: fail})
	// 滑窗裁剪：样本量 = 5 分钟内的请求数，几十到几百，线性扫描足够。
	cut := now.Add(-upstreamHealthWindow)
	drop := 0
	for drop < len(u.samples) && u.samples[drop].at.Before(cut) {
		drop++
	}
	if drop > 0 {
		u.samples = append([]upstreamSample(nil), u.samples[drop:]...)
	}

	total, slow, fails := 0, 0, 0
	var worst time.Duration
	for _, s := range u.samples {
		total++
		if s.fail {
			fails++
			continue
		}
		if s.ttfb >= upstreamSlowTTFB {
			slow++
		}
		if s.ttfb > worst {
			worst = s.ttfb
		}
	}
	if total < upstreamHealthMinSample {
		return
	}
	slowBad := float64(slow) >= float64(total)*upstreamSlowRatio
	failBad := fails >= upstreamMinFail && float64(fails) >= float64(total)*upstreamFailRatio
	if !slowBad && !failBad {
		return
	}
	if !u.lastWarn.IsZero() && now.Sub(u.lastWarn) < upstreamHealthWarnEvery {
		return
	}
	u.lastWarn = now

	// 一条聚合行：窗口、样本、慢/失败计数与占比、最慢值。够运维判断要不要
	// 提前摘上游或调大客户端 timeout，又不至于逐请求刷屏。
	log.Printf("WARN: [server] upstream degraded: 最近 %s 内 %d 条请求，TTFB>=%s 占 %.0f%%（最慢 %s），5xx 占 %.0f%%——上游整体劣化，请检查 provider 状态或客户端超时设置",
		upstreamHealthWindow, total,
		upstreamSlowTTFB, pct(slow, total), worst.Round(time.Millisecond),
		pct(fails, total))
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}

// String 便于测试与排障时打印当前窗口状态。
func (u *upstreamHealth) String() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return fmt.Sprintf("samples=%d lastWarn=%s", len(u.samples), u.lastWarn.Format(time.RFC3339))
}
