package server

import (
	"log"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// noSelectLogInterval 同一 (realm, model, 成因) 组合两条「选不到号」日志的最小间隔。
//
// 为什么要限频：选中不了号时每个请求都会走到这里，突发流量下（客户端按 1s 重试）
// 一分钟能刷出几百行，把真正的一次性故障挤出日志环。限频后仍能看见「最先发生的
// 那一次」以及成因随时间的变化（成因键参与去重）。
const noSelectLogInterval = time.Minute

var (
	noSelectMu   sync.Mutex
	noSelectLast = map[string]time.Time{}
)

// logNoSelectable 池子里挑不出可派发的账号时记一条（按 realm+model+成因限频）。
//
// why 由调用方按 pick 的判据顺序算好（pool.NoPickForRealm）：这条日志的价值全在
// 「是哪一类把号挡完的」——只报 total/healthy 仍然得靠人去猜，而 /status 上这些号
// 看起来全都正常。
func (h *Handler) logNoSelectable(realm, model string, modelBlocked bool, why pool.NoPick) {
	if h.cfg.Pool == nil {
		return
	}
	key := realm + "\x00" + model + "\x00" + why.String()
	now := time.Now()
	noSelectMu.Lock()
	if last, ok := noSelectLast[key]; ok && now.Sub(last) < noSelectLogInterval {
		noSelectMu.Unlock()
		return
	}
	if len(noSelectLast) > 512 { // 防 key 无限膨胀（模型名来自客户端）
		noSelectLast = map[string]time.Time{}
	}
	noSelectLast[key] = now
	noSelectMu.Unlock()

	log.Printf("WARN: [pool] no selectable account realm=%q model=%q no_pick{%s} model_blocked=%v —— 该域本请求快速失败（503）；healthy>0 说明还有别的闸门（策略/快过期收敛）挡住了候选",
		realm, model, why.String(), modelBlocked)
}
