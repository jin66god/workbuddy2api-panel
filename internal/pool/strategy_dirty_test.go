package pool

import (
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestPickDoesNotDirtyState 游标（scheduling）是运行态、不进 state.json：
// 选号不能置 dirty，否则有流量时 flusher 每 5 秒重写并 fsync 一遍内容未变的
// state.json（写放大），而持久化的游标对重启后的公平性收益极小。
func TestPickDoesNotDirtyState(t *testing.T) {
	dir := t.TempDir()
	p := New(filepath.Join(dir, "state.json"))
	p.Add(&auth.Auth{UID: "u1", AccessToken: "a"})
	p.Add(&auth.Auth{UID: "u2", AccessToken: "b"})
	if err := p.SetScheduling(RegionStrategies{Domestic: SequentialExhaust, International: SequentialExhaust},
		RegionAccountOrder{}); err != nil {
		t.Fatalf("SetScheduling: %v", err)
	}
	p.Flush()
	if p.dirty.Load() {
		t.Fatal("Flush 后仍为 dirty")
	}
	for i := 0; i < 4; i++ {
		if got := p.PickExcludingForRealm(nil, "", "cn"); got == nil {
			t.Fatalf("第 %d 次选号返回 nil", i)
		}
	}
	if p.dirty.Load() {
		t.Fatal("选号置脏了：游标是运行态，不应触发 state.json 落盘")
	}
}
