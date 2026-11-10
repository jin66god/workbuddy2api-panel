// egress.go 出口级瞬时失败计数。
//
// 为什么需要它：账号级连败兜底（degrade.go 的 NoteFailures）把「连续失败」记在
// **账号**头上，达阈临时出池。这条口径在失败确实属于某个号时是对的，但当失败来自
// **出口/链路**的瞬时抖动时，它会把一次抖动摊到每个恰好被选中的号上——上游 fork 与
// 参考实现（workbuddy2api-hub issue #271）的现场实测：4 小时 196 次传输层失败陆续把
// 13 个账号降到出池，期间池子几乎空掉，而链路本身只断了几秒钟。
//
// 本模块把这类失败改记在**出口**上：同一个出口的账号共用一个计数器，于是一次抖动只
// 换来一段秒级的全局退避，而不是 N 个账号级惩罚。
//
// 尺度是重点：账号级阶梯是分钟级（degradeCooldown 起、封顶 degradeCooldownMax），
// 出口级阶梯是秒级（1 秒起、翻倍、封顶 15 秒、带抖动）。几秒钟的链路抖动不该换来
// 半小时不可用。
//
// 本模块只持有计数，不决定退避时长（阶梯在 server 侧，与 rotateBackoff 同处）；也不
// 持久化：链路状态天然易失，重启后从零开始是正确的，落盘只会让一次重启前的抖动继续
// 压着新进程的请求。恢复也由时间窗口负责——60 秒没有新的失败即视为新一轮。
package pool

import "time"

// egressWindow 距上次失败超过这么久，连续计数视为新的一轮（而不是接着上一次翻倍）。
//
// 取值理由：一个请求自己的重试链（默认 3 次轮转、每次退避 0.5s/1s/2s）全部落在几秒内，
// 60 秒足够把「同一次抖动」圈进来；再长会把两次无关的偶发抖动并成一条阶梯。
const egressWindow = 60 * time.Second

// maxEgressKeys 计数表的硬上限。本库的键空间实际只有「cn / global」两三个（出口 =
// 上游域名，账号不携带代理身份），但保留上限是为了防止未来接入代理池后键随节点增删
// 无限增长。超限时整体清空而不是逐条淘汰：这些都是秒级状态，清空的代价只是「本轮
// 阶梯重来」，而逐条淘汰要维护 LRU。
const maxEgressKeys = 128

// egressStat 一个出口的连续失败状态。last 为零值表示从未记录过。
type egressStat struct {
	streak int
	last   time.Time
}

// NoteEgressFailure 记一次出口级瞬时失败，返回**含本次**的连续次数。
//
// streak 从 1 开始；调用方据此查退避阶梯并在达到上限时快速失败。now 由调用方传入
// （测试要能走时间，不想真睡）。
//
// 空 key 一律忽略并返回 0：调用方拿不到出口身份时不该悄悄记到某个共享桶里——那等于
// 把本来要治的「张冠李戴」换个地方重演。
func (p *Pool) NoteEgressFailure(key string, now time.Time) int {
	if key == "" {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.egress == nil {
		p.egress = make(map[string]egressStat)
	}
	st := p.egress[key]
	if st.last.IsZero() || now.Sub(st.last) > egressWindow {
		st.streak = 0
	}
	st.streak++
	st.last = now
	if len(p.egress) > maxEgressKeys {
		if _, exists := p.egress[key]; !exists {
			p.egress = make(map[string]egressStat, 1)
		}
	}
	p.egress[key] = st
	return st.streak
}

// EgressStreak 该出口当前的连续失败次数；超过窗口未再失败则返回 0。
func (p *Pool) EgressStreak(key string, now time.Time) int {
	if key == "" {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, ok := p.egress[key]
	if !ok || st.last.IsZero() || now.Sub(st.last) > egressWindow {
		return 0
	}
	return st.streak
}
