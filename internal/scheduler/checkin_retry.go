// checkin_retry.go 签到「瞬时失败」的延后补签。
//
// 背景：上游对签到偶发返回 500 + code 10001「签到失败，请稍后重试」。DailyCheckin
// 自身已有 2s/4s 两次内联重试（upstream.retryBillingTransient），但上游这句话要的是
// 分钟级沉降——6 秒内三次全败之后，旧逻辑只能等下一轮排程（默认 9/21 点，12 小时后）。
// 中间账号当天的签到就是没有的：漏一天即断连登，而面板只显示一行「签到失败」，
// 看不出「还会再试」。
//
// 这里只做一件事：主批收尾后，把「被判为瞬时失败」的账号在第 10、40 分钟各补签一次。
// 三点约束：
//   - 只排 IsTransientBilling 判为瞬时的错误。业务拒绝（企业号 10001「不支持该操作」、
//     参数错）重试必然同样失败，排进去只会每天多打几轮无效请求。
//   - 补签前先看本地 checkin_done：用户可能已在手机上签了，上一档补签也可能已经成功。
//   - 后台 goroutine + ctx 取消即退：不能阻塞 runCheckin 返回（摊开模式下 spreadRunning
//     要等它返回才清，阻塞会让下一个时点整批被跳过），也不能拖住优雅停机。
package scheduler

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// checkinRetryDelays 补签时点（相对本批收尾的绝对偏移）。
//
// 10 分钟覆盖「请稍后重试」这类短沉降；40 分钟再兜一次上游仍在恢复的形态。
// 两档都没成也不再排——继续排只会把无效请求拉长到整晚，真正的兜底是下一轮排程。
var checkinRetryDelays = []time.Duration{10 * time.Minute, 40 * time.Minute}

// startCheckinRetry 为瞬时失败的账号启动后台补签。uids 为空时零开销直接返回。
func (s *Scheduler) startCheckinRetry(ctx context.Context, uids []string) {
	if len(uids) == 0 {
		return
	}
	// 去重（同一账号理论上只会进一次，防御未来主批改动）+ 拷贝，避免调用方复用切片。
	seen := make(map[string]struct{}, len(uids))
	pending := make([]string, 0, len(uids))
	for _, uid := range uids {
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		pending = append(pending, uid)
	}
	log.Printf("INFO: [scheduler] 签到瞬时失败 %d 个账号，将在 %s / %s 后自动补签",
		len(pending), checkinRetryDelays[0], checkinRetryDelays[1])
	go s.runCheckinRetry(ctx, pending)
}

// runCheckinRetry 按 checkinRetryDelays 逐档补签，直到签成、判为不可重试或 ctx 取消。
func (s *Scheduler) runCheckinRetry(ctx context.Context, pending []string) {
	var elapsed time.Duration
	for _, delay := range checkinRetryDelays {
		if !sleepCtx(ctx, delay-elapsed) {
			return // 优雅停机 / 重排：不再补签，下一轮排程兜底
		}
		elapsed = delay
		var still []string
		for _, uid := range pending {
			if ctx.Err() != nil {
				return
			}
			// 已签（手机端、上一档补签、用户手动触发）→ 不再打上游。
			if s.cfg.Pool.CheckinDoneToday(uid) {
				continue
			}
			a := s.cfg.Pool.AuthByUID(uid)
			if a == nil || !a.SupportsPersonalEndpoints() {
				continue // 账号已删除 / 已变成企业或 API Key 形态：补签无意义
			}
			var nickname string
			if st, ok := s.cfg.Pool.Status(uid); ok {
				nickname = st.Nickname
			}
			done, err := s.checkinOne(a, uid, nickname)
			if !done && err != nil && upstream.IsTransientBilling(err) {
				still = append(still, uid)
			}
			// 非瞬时失败（业务拒绝、账号被禁）直接放弃：再补只会原样再失败。
		}
		pending = still
		if len(pending) == 0 {
			log.Printf("INFO: [scheduler] 签到补签完成（第 %s 档后无待补账号）", delay)
			return
		}
		log.Printf("WARN: [scheduler] 签到补签第 %s 档后仍有 %d 个账号瞬时失败：%s",
			delay, len(pending), labelsOf(s.cfg.Pool, pending))
	}
	log.Printf("WARN: [scheduler] 签到补签两档均未成功 %d 个账号，等下一轮排程：%s",
		len(pending), labelsOf(s.cfg.Pool, pending))
}

// labelsOf 把 uid 列表渲染成日志标签（带昵称），只用于补签结果行。
func labelsOf(p *pool.Pool, uids []string) string {
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		nickname := ""
		if st, ok := p.Status(uid); ok {
			nickname = st.Nickname
		}
		out = append(out, logfmt.Label(uid, nickname))
	}
	return strings.Join(out, ", ")
}
