package scheduler

import (
	"fmt"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

func TestCapByCredits(t *testing.T) {
	// 健康号：可用量与快照通常相同。
	mk := func(uid string, c int64) pool.Status {
		return pool.Status{UID: uid, Credits: c, CreditsRaw: c}
	}
	// cap<=0 或不超上限：原样返回。
	list := []pool.Status{mk("a", 3), mk("b", 9)}
	if got := capByCredits(list, 0); len(got) != 2 || &got[0] != &list[0] {
		t.Fatal("cap 0 must be no-op returning same slice")
	}
	if got := capByCredits(list, 5); len(got) != 2 {
		t.Fatal("cap >= len must be no-op")
	}
	// 收窄为积分 Top-N。
	list = []pool.Status{mk("a", 100), mk("b", 500), mk("c", 300), mk("d", 400), mk("e", 200)}
	got := capByCredits(list, 2)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Top-2 必须是 b(500) 和 d(400)，顺序不限但集合确定。
	set := map[string]bool{}
	for _, st := range got {
		set[st.UID] = true
	}
	if !set["b"] || !set["d"] {
		t.Fatalf("want {b,d}, got %v", set)
	}
	// 禁用号的 credits 是对外可用量（恒 0），排序必须用 credits_raw：它仍在候选集里
	// 跑保号任务，不该因为「不可用」被统一压到末尾。
	if got := capByCredits([]pool.Status{
		{UID: "dead", Credits: 0, CreditsRaw: 900, Disabled: true},
		mk("x", 100), mk("y", 200),
	}, 1); len(got) != 1 || got[0].UID != "dead" {
		t.Fatalf("禁用号应按 credits_raw 参与排序，got %v", got)
	}
	// 原切片不被打乱（调用方 Pool.List 的共享底层数组不能被改序）。
	if list[0].UID != "a" {
		t.Fatal("input slice must not be reordered")
	}
	_ = fmt.Sprint()
}

// 夜间上限生效路径：cap 后不参与账号连 spread 计划都不进（候选先收窄）。
// 资格过滤/门控在 runBlackcat 内部已有用例覆盖，这里验纯函数口径即可。
func TestCapByCreditsUnknownCredits(t *testing.T) {
	list := []pool.Status{{UID: "a"}, {UID: "b"}, {UID: "c"}} // Credits 全 0（未知）
	got := capByCredits(list, 2)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}
