// 选号失败的成因拆解。
package pool

import (
	"fmt"
	"time"
)

// NoPick 一次「Pick 返回 nil」的成因拆解：每个账号按 pick 的实际判据顺序归一类。
//
// 为什么需要它：no_healthy_account 是一句笼统的「没有可用账号」，但池子里明明还有
// 十几个有余额的号——真实原因可能是账号冷却、暂停选号、在途名额占满、积分保底，
// 或者本次请求已经轮转试过。四类成因的处置完全不同（等冷却 / 恢复暂停 / 提并发 /
// 调阈值 / 换号），而 /status 上这些号看起来全都正常，用户只能靠猜。
//
// 判据顺序必须与 pick 一致（见 pick.go 的过滤顺序），否则「选不到」与「归到哪一类」
// 会自相矛盾。
type NoPick struct {
	Total        int // 本域账号数（realm 过滤后）
	Healthy      int // 过了全部判据却没被选中：正常应为 0，非 0 说明 pick 里还有别的闸门
	Paused       int // 暂停选号（面板手动暂停）
	Disabled     int // 已禁用（session 死 / 账号级故障达阈）
	Cooling      int // 账号级冷却、熔断或降权窗口
	InFlightFull int // 在途名额占满（pool.max_in_flight）
	Floor        int // 积分保底（pool.credit_floor，0 = 关闭）
	Tried        int // 本次请求已试过（请求级轮转）
}

// String 紧凑单行，供日志与 503 的 gateway_hint 直接引用。
func (n NoPick) String() string {
	return fmt.Sprintf("total=%d healthy=%d cooling=%d paused=%d disabled=%d in_flight_full=%d floor=%d tried=%d",
		n.Total, n.Healthy, n.Cooling, n.Paused, n.Disabled, n.InFlightFull, n.Floor, n.Tried)
}

// NoPickForRealm 复刻 pick 的排除顺序给账号归类；tried 为 nil 时不计轮转。
//
// 归类顺序（与 pick 一致）：tried → disabled → paused → 冷却/熔断 → 积分保底 →
// 在途占满 → 可选。disabled / paused 是冷却的两种具体成因，先于笼统的「不健康」
// 归类，运维才知道该去点「启用」还是等窗口。
func (p *Pool) NoPickForRealm(model, realm string, tried map[string]bool) NoPick {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	var n NoPick
	for uid, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		n.Total++
		switch {
		case tried != nil && tried[uid]:
			n.Tried++
		case e.disabled:
			n.Disabled++
		case e.paused:
			n.Paused++
		case !p.healthyForModelLocked(e, model, now):
			n.Cooling++
		case p.floorBlockedForRealmModel(e, model, realm, now):
			n.Floor++
		case p.inFlightFull(e):
			n.InFlightFull++
		default:
			n.Healthy++
		}
	}
	return n
}

// healthyForModelLocked 分类用的健康口径：model 非空时按模型口径判定（6004 模型级
// 冷却也算不可用），否则走账号级健康。仅限已持 p.mu 的调用方使用。
func (p *Pool) healthyForModelLocked(e *entry, model string, now time.Time) bool {
	if model != "" {
		return e.healthyForModel(now, model)
	}
	return e.healthy(now)
}
