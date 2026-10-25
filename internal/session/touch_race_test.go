package session

import (
	"testing"
	"time"
)

// TestTouchDoesNotReviveRacedBinding touch 的 CAS 语义：快路径从 RUnlock 到加写锁
// 之间，同一会话的 Unbind（失败路径）/ Bind（粘性跟随成功号）/ 慢路径重分配都可能
// 已改写绑定。此前的无条件回写会把刚解绑的失败号复活、把粘性跟随的成功号冲回
// 旧号，且镜像进 Redis 后连重启都恢复成错误绑定。
func TestTouchDoesNotReviveRacedBinding(t *testing.T) {
	now := time.Now()

	// 场景 1：失败路径刚解绑，迟到的 touch 不得把失败号写回。
	// （不能借 Resolve 验证——它会合法地重新分配，新分配恰好选中 a1 也是对的；
	// 要看的是「条目被删后 touch 不得把它写回」。）
	st := newCountingStore()
	r := routerWith(st, []string{"a1", "a2"}, time.Minute)
	r.Bind("k", "a1")
	st.mu.Lock()
	setBindsBefore := st.setBinds
	st.mu.Unlock()
	r.Unbind("k")
	r.touch("k", "a1", now)
	r.mu.RLock()
	_, exists := r.entries["k"]
	r.mu.RUnlock()
	if exists {
		t.Fatal("Unbind 后迟到的 touch 不得复活旧绑定")
	}

	// 场景 2：成功路径已 Bind 到 a2，迟到的 touch(a1) 不得冲回 a1。
	st2 := newCountingStore()
	r2 := routerWith(st2, []string{"a1", "a2"}, time.Minute)
	r2.Bind("k", "a1")
	r2.Bind("k", "a2") // 粘性跟随最终成功号
	r2.touch("k", "a1", now)
	r2.mu.RLock()
	uid, bound := r2.entries["k"]
	r2.mu.RUnlock()
	if !bound || uid.uid != "a2" {
		t.Fatalf("Bind 后迟到的 touch 不得冲掉新绑定, got %q bound=%v", uid.uid, bound)
	}

	// 场景 3：绑定未变时 touch 正常滚动（快路径语义不受影响）。
	r2.touch("k", "a2", now)
	r2.mu.RLock()
	uid, bound = r2.entries["k"]
	r2.mu.RUnlock()
	if !bound || uid.uid != "a2" {
		t.Fatalf("绑定未变时 touch 应保持绑定, got %q bound=%v", uid.uid, bound)
	}

	// 场景 1 的镜像也不得多写一次（Unbind 走 DelBind，touch 未命中不应再 SetBind）。
	st.mu.Lock()
	wrote := st.setBinds - setBindsBefore
	st.mu.Unlock()
	if wrote != 0 {
		t.Fatalf("uid 不匹配的 touch 不应镜像, extra SetBind=%d", wrote)
	}
}
