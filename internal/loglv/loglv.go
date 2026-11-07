// Package loglv 提供最小日志级别开关。
//
// 背景：全仓用 log.Printf + 手写 "INFO:/WARN:/ERR:" 前缀，没有级别概念。于是
// 「合法终态但看着像异常」的诊断行（声明了 tools 却零 tool_calls、reasoning_effort
// 被抬到下限）只能一直打 INFO——2026-11-07 的 12 小时日志里，这两类占了全部行的
// 7% 以上，巡检时全是噪音。这里只解决「能不能关掉」这一个问题，不引入日志框架：
//
//	WB2A_LOG_LEVEL=debug   # 或 LOG_LEVEL=debug；默认 info
//
// 级别：debug < info < warn < error。Debugf 只在 debug 级别输出。
package loglv

import (
	"log"
	"os"
	"strings"
	"sync"
)

// Level 日志级别，数值越大越宽松。
type Level int

const (
	LevelError Level = iota // 只输出 ERR
	LevelWarn               // ERR + WARN
	LevelInfo               // 默认：ERR + WARN + INFO
	LevelDebug              // 全量（含诊断细节）
)

var (
	once sync.Once
	cur  = LevelInfo
)

// current 解析环境变量，只做一次（进程内级别固定：启动后改 env 不生效，与
// 其他 WB2A_* 环境变量同语义）。WB2A_LOG_LEVEL 优先，兼容裸 LOG_LEVEL。
func current() Level {
	once.Do(func() {
		raw := strings.TrimSpace(os.Getenv("WB2A_LOG_LEVEL"))
		if raw == "" {
			raw = strings.TrimSpace(os.Getenv("LOG_LEVEL"))
		}
		switch strings.ToLower(raw) {
		case "debug":
			cur = LevelDebug
		case "warn", "warning":
			cur = LevelWarn
		case "error":
			cur = LevelError
		case "", "info":
			cur = LevelInfo
		default:
			// 拼错不静默停摆：保持默认 info，启动期由调用方自行打日志无必要。
			cur = LevelInfo
		}
	})
	return cur
}

// Enabled 报告某级别是否输出。
func Enabled(l Level) bool { return l <= current() }

// Debugf 仅在 debug 级别输出。格式与 log.Printf 一致，调用方自带级别前缀。
func Debugf(format string, args ...any) {
	if Enabled(LevelDebug) {
		log.Printf(format, args...)
	}
}

// resetForTest 只给单测用：清掉 once，让下一条用例能重新读 env。
func resetForTest() {
	once = sync.Once{}
	cur = LevelInfo
}
