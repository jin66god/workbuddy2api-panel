// tasks.go 面板「积分任务」接口：查询任务进度、接受任务、领取奖励。
//
// 上游能力（internal/upstream/tasks.go）的三层薄封装；前端表格展示
// current/target 进度与可领取状态，运维点按钮即可，无需外部 Python 脚本。
package panel

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// acceptBatchGap 批量接受的批间节流（对齐脚本 1.05s 口径，避免上游风控）。
var acceptBatchGap = 1050 * time.Millisecond

// accountByUID 取账号凭证；不存在时写 404 并返回 nil。
func (p *Panel) accountByUID(w http.ResponseWriter, uid string) *auth.Auth {
	a := p.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return nil
	}
	return a
}

// accountTasks 查询单账号全量任务（进度/状态/可领取）。
func (p *Panel) accountTasks(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	// 静态 API Key 账号无成长任务体系：明确拒绝（不静默空跑）。
	if a.IsAPIKey() {
		writeErr(w, http.StatusUnprocessableEntity, "API Key 账号无签到/成长任务体系")
		return
	}
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks: "+err.Error())
		return
	}
	// 合并小程序口径任务（school_season / Sequential_Tasks_1 等仅在 mp 头列表下发）。
	// mp 列表是默认口径的超集（实测含常规任务），按 task_code 去重；失败静默。
	if mpTasks, mpErr := p.cfg.Upstream.ListTasksMP(a); mpErr == nil {
		seen := map[string]bool{}
		for _, t := range tasks {
			seen[t.TaskCode] = true
		}
		for _, t := range mpTasks {
			if !seen[t.TaskCode] {
				tasks = append(tasks, t)
				seen[t.TaskCode] = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tasks": tasks})
}

// accountTaskAccept 接受任务（报名；幂等）。
func (p *Panel) accountTaskAccept(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	var body struct {
		TaskCodes []string `json:"task_codes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TaskCodes) == 0 {
		writeErr(w, http.StatusBadRequest, "task_codes required")
		return
	}
	if err := p.cfg.Upstream.AcceptTasks(a, body.TaskCodes); err != nil {
		writeErr(w, http.StatusBadGateway, "accept: "+err.Error())
		return
	}
	// 回读确认：接口成功不代表状态落库（历史上出现过「提示已接受、列表仍未接受」）。
	verified := p.acceptedTaskCodes(a)
	_, unverified := countAccepted(body.TaskCodes, verified)
	if len(unverified) > 0 {
		if err := p.cfg.Upstream.AcceptTasksWeb(a, unverified); err == nil {
			time.Sleep(400 * time.Millisecond)
			verified = p.acceptedTaskCodes(a)
			_, unverified = countAccepted(body.TaskCodes, verified)
		}
	}
	log.Printf("panel: 接受任务 uid=%s codes=%v 未变化=%v", uid, body.TaskCodes, unverified)
	resp := map[string]any{"ok": true, "accepted": len(body.TaskCodes) - len(unverified), "unverified": unverified}
	if len(unverified) > 0 {
		resp["message"] = "平台返回成功，但部分任务状态未变为已接受，请重试"
	}
	writeJSON(w, http.StatusOK, resp)
}

// taskAcceptAll 接受该账号全部尚未接受的任务（跳过已 accepted/claimed 的）。
//
// 为什么值得做：accept 不产生进度（进度靠行为事件点亮），但让状态机规范
// （not_accepted → accepted → completed），也便于后续筛选"我报过名的任务"。
// 上游 scripts 的注释同样建议"先 accept"。
// 批量提交会分片（上游对 task_codes 数组长度无公开上限，保守每批 20 个）。
func (p *Panel) taskAcceptAll(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	// 静态 API Key 账号无成长任务体系：明确拒绝（不静默空跑）。
	if a.IsAPIKey() {
		writeErr(w, http.StatusUnprocessableEntity, "API Key 账号无签到/成长任务体系")
		return
	}
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks: "+err.Error())
		return
	}
	var codes []string
	for _, t := range tasks {
		// 跳过已完成/已领取/已接受的；locked 的也不碰（上游未开放）。
		if t.Claimed || t.Locked || t.AcceptStatus == "accepted" || t.AcceptStatus == "completed" {
			continue
		}
		codes = append(codes, t.TaskCode)
	}
	if len(codes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": 0, "message": "所有任务均已接受"})
		return
	}
	const batch = 20
	// 以「请求过的 code」为基准做回读核对：POST 成功只说明接口收了，不代表状态落库。
	requested := append([]string(nil), codes...)
	seenDefault := make(map[string]bool, len(codes))
	for _, c := range codes {
		seenDefault[c] = true
	}
	var failed []string
	for i := 0; i < len(codes); i += batch {
		end := i + batch
		if end > len(codes) {
			end = len(codes)
		}
		if err := p.cfg.Upstream.AcceptTasks(a, codes[i:end]); err != nil {
			log.Printf("panel: 批量接受失败 uid=%s codes=%v err=%v", uid, codes[i:end], err)
			failed = append(failed, codes[i:end]...)
			continue
		}
		time.Sleep(acceptBatchGap) // 批间节流（对齐脚本 1.05s 口径）
	}
	// 小程序口径任务单独批量接受（默认列表不含 mp 码，accept 也要求 mp 头）。
	if mpTasks, mpErr := p.cfg.Upstream.ListTasksMP(a); mpErr == nil {
		var mpCodes []string
		for _, t := range mpTasks {
			// mp 列表是默认口径的超集：默认列表里已处理的 code 不再用 mp 头重复
			// 报一遍（重复报名既多打请求，也可能把状态冲回未接受）。
			if seenDefault[t.TaskCode] {
				continue
			}
			if t.Claimed || t.Locked || t.AcceptStatus == "accepted" || t.AcceptStatus == "completed" {
				continue
			}
			mpCodes = append(mpCodes, t.TaskCode)
		}
		if len(mpCodes) > 0 {
			if err := p.cfg.Upstream.AcceptTasksMP(a, mpCodes); err != nil {
				log.Printf("panel: mp 批量接受失败 uid=%s err=%v", uid, err)
				failed = append(failed, mpCodes...)
			} else {
				time.Sleep(acceptBatchGap)
			}
		}
	}
	// 回读确认：逐个核对任务是否真的进入已接受/已完成状态。
	verified := p.acceptedTaskCodes(a)
	accepted, unverified := countAccepted(requested, verified)
	if len(unverified) > 0 {
		// chatBase 的 /v2 批量接口可能回 code=0 但不落库；改用 Web 成长中心口径
		// （webBase + /activity/...，带 Origin/Referer）重试未生效的 code，再回读一次。
		if err := p.cfg.Upstream.AcceptTasksWeb(a, unverified); err != nil {
			log.Printf("panel: Web 口径补报名失败 uid=%s codes=%v err=%v", uid, unverified, err)
		} else {
			time.Sleep(400 * time.Millisecond)
			verified = p.acceptedTaskCodes(a)
			accepted, unverified = countAccepted(requested, verified)
		}
	}
	log.Printf("panel: 全部接受 uid=%s 请求=%d 实际接受=%d 未变化=%d 失败=%d",
		uid, len(requested), accepted, len(unverified), len(failed))
	resp := map[string]any{
		"ok": true, "requested": len(requested), "accepted": accepted,
		"failed": failed, "unverified": unverified,
	}
	switch {
	case len(failed) > 0:
		resp["message"] = "部分任务接受失败（接口报错），可重试"
	case accepted == 0:
		resp["message"] = "平台返回成功，但任务状态未变为已接受（可能未真正报名），请重试"
	case len(unverified) > 0:
		resp["message"] = "部分任务状态未变为已接受，可重试"
	}
	writeJSON(w, http.StatusOK, resp)
}

// accountTaskClaim 领取任务奖励（未达标时上游返回业务错误，原样透出给前端提示）。
func (p *Panel) accountTaskClaim(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	var body struct {
		TaskCode string `json:"task_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskCode == "" {
		writeErr(w, http.StatusBadRequest, "task_code required")
		return
	}
	// 小程序口径任务走 chat 域 mp 头领奖（缺头实测不可领）；其余 Web 端接口。
	var credit, energy int64
	var err error
	if isMPTaskCode(body.TaskCode) {
		credit, energy, err = p.cfg.Upstream.ClaimRewardMP(a, body.TaskCode)
	} else {
		credit, energy, err = p.cfg.Upstream.ClaimReward(a, body.TaskCode)
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, "claim: "+err.Error())
		return
	}
	if credit == 0 && energy == 0 {
		// 领取接口返回 200 只代表请求被接受，不等于积分入账：先回读任务状态再下结论。
		// 旧实现把 0/0 一律当成「此前已领」，会在「已提交但未入账」时误报成功。
		claimed, verifyErr := p.taskClaimedAfterClaim(a, body.TaskCode)
		if verifyErr != nil {
			log.Printf("panel: 领取任务奖励 uid=%s code=%s 已提交，但回读状态失败: %v", uid, body.TaskCode, verifyErr)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "credited": false, "verified": false,
				"message": "领取请求已提交，但读取任务状态失败，暂无法确认是否到账",
			})
			return
		}
		if claimed {
			log.Printf("panel: 领取任务奖励 uid=%s code=%s（已领取过，无新增）", uid, body.TaskCode)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already_claimed": true, "message": "该奖励此前已领取"})
			return
		}
		log.Printf("panel: 领取任务奖励 uid=%s code=%s 上游已接受但任务状态未变为已领取", uid, body.TaskCode)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "credited": false, "verified": true,
			"message": "领取请求已提交，但任务状态未更新（未确认到账），请稍后刷新后重试",
		})
		return
	}
	log.Printf("panel: 领取任务奖励 uid=%s code=%s +%d分 +%d能", uid, body.TaskCode, credit, energy)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credit": credit, "energy": energy})
}

// countAccepted 按请求过的 code 统计「真的已接受」的数量与未变化列表。
func countAccepted(codes []string, verified map[string]bool) (int, []string) {
	accepted := 0
	var unverified []string
	for _, code := range codes {
		if verified[code] {
			accepted++
		} else {
			unverified = append(unverified, code)
		}
	}
	return accepted, unverified
}

// acceptedTaskCodes 回读默认口径 + 小程序口径，返回已接受/已完成/已领取的任务 code。
// 用于「全部接受」后的真实性校验：接口回 200 不等于是真的报名成功。
func (p *Panel) acceptedTaskCodes(a *auth.Auth) map[string]bool {
	out := map[string]bool{}
	collect := func(tasks []upstream.Task) {
		for _, t := range tasks {
			if t.Claimed || strings.EqualFold(t.AcceptStatus, "accepted") || strings.EqualFold(t.AcceptStatus, "completed") {
				out[t.TaskCode] = true
			}
		}
	}
	if tasks, err := p.cfg.Upstream.ListTasks(a); err == nil {
		collect(tasks)
	}
	if tasks, err := p.cfg.Upstream.ListTasksMP(a); err == nil {
		collect(tasks)
	}
	return out
}

// taskClaimedAfterClaim 领取后回读任务状态，确认是否真的标记为已领取。
//
// 先查默认口径，再查小程序口径（部分任务只在 mp 头列表下发）。任务在两边都
// 不存在时返回 (false, nil)：调用方按「未确认入账」处理，而不是当成已领取。
func (p *Panel) taskClaimedAfterClaim(a *auth.Auth, code string) (bool, error) {
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err == nil {
		for _, t := range tasks {
			if t.TaskCode == code {
				return t.Claimed, nil
			}
		}
	}
	mpTasks, mpErr := p.cfg.Upstream.ListTasksMP(a)
	if mpErr == nil {
		for _, t := range mpTasks {
			if t.TaskCode == code {
				return t.Claimed, nil
			}
		}
	}
	if err != nil {
		return false, err
	}
	if mpErr != nil {
		return false, mpErr
	}
	return false, nil
}
