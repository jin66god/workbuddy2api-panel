// Package cst 固定 UTC+8（北京时间）：上游「自然日」的唯一定义。
//
// 为什么需要一个包：WorkBuddy 的额度、签到、每日活跃、夜猫子窗口、积分包到期日
// 全部按北京时间零点切日，而网关可能跑在 Docker / NAS / 海外 VPS 上——那些机器的
// 本地时区不是 UTC+8。判定跟着宿主时区走，「今天」的边界就整体漂移 8 小时：
// UTC 主机上配的 activity_hours=[10] 实际在 CST 18:00 跑，硬冷却的「次日 04:00」
// 落在 CST 12:00（签到早已结束），夜猫子 23:00–08:00 的窗口也跟着错位。
// 这些错都不会报错，只会静默少拿积分——所以日界只能由一个常量决定。
//
// 用法约束：凡是「第几天」「几点档位」的判定一律走本包；纯粹的时长（冷却 30m、
// 退避 5s、TTL）用 time.Now()/time.Since 即可，与日界无关，不要多绕一层。
package cst

import "time"

const (
	// dayLayout 上游自然日的字符串口径（同时也是 state.json 里的日键格式）。
	dayLayout = "2006-01-02"
	// offsetSeconds UTC+8 固定偏移：中国无夏令时，用 FixedZone 免依赖 tzdata。
	offsetSeconds = 8 * 60 * 60
)

var zone = time.FixedZone("CST", offsetSeconds)

// Zone 返回 UTC+8 时区。需要 time.Date/ParseInLocation 时传它，不要再自建 FixedZone。
func Zone() *time.Location { return zone }

// Now 返回当前北京时间。
func Now() time.Time { return time.Now().In(zone) }

// In 把 t 换算到北京时间（只改呈现时区，不改时刻）。
func In(t time.Time) time.Time { return t.In(zone) }

// Day 返回 t 所属的自然日（CST），格式 2006-01-02。
// 这是所有「今天做过没有」判定的唯一日键来源：写入方与读取方必须用同一个函数，
// 否则会出现「刚签到完面板显示未签」这类只在宿主非 CST 时才复现的漂移。
func Day(t time.Time) string { return t.In(zone).Format(dayLayout) }

// DayOffset 返回 t 所在自然日偏移 n 天后的日键（-1 = 昨天，+1 = 明天）。
// 用 AddDate 而不是 Add(24h)：跨月/跨年由标准库进位，且 CST 无夏令时。
func DayOffset(t time.Time, n int) string {
	return t.In(zone).AddDate(0, 0, n).Format(dayLayout)
}

// Yesterday 返回 t 所在自然日的前一天（CST）。补签、夜猫子跨零点都按这个口径。
func Yesterday(t time.Time) string { return DayOffset(t, -1) }

// At 返回 t 所在自然日（CST）的 hour:00:00 时刻。
// 返回值的 Location 是 UTC+8；与任意时区的 time.Time 比较都是绝对时刻比较，安全。
// 若该时刻已过（例如 now 是 15:00、hour=10），由调用方决定顺延到次日。
func At(t time.Time, hour int) time.Time {
	n := t.In(zone)
	return time.Date(n.Year(), n.Month(), n.Day(), hour, 0, 0, 0, zone)
}
