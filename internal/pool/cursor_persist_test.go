package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestCursorPersistsAtShutdownOnly 调度游标只在停机时落盘：
//   - 运行期选号不置脏 → Flush() 不重写 state.json（消除每次选号触发的 5s 写放大）
//   - Close() 强制落一次 → 重启后 round-robin 从下一个号继续（既有契约不变）
func TestCursorPersistsAtShutdownOnly(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	accounts := []*auth.Auth{{UID: "a"}, {UID: "b"}}
	p.SyncToDir(accounts)
	if err := p.SetScheduling(RegionStrategies{Domestic: RoundRobin, International: RoundRobin},
		RegionAccountOrder{}); err != nil {
		t.Fatalf("SetScheduling: %v", err)
	}
	p.Flush() // 账号入池的 dirty 在这里落掉，后续用于对照

	// 入池/策略设置未必置脏：没有文件属于正常起点，用「文件是否存在」作对照基线。
	before, err := os.ReadFile(fp)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读状态失败: %v", err)
	}

	// 两次选号推进游标（a → b），运行期不应触发落盘。
	if got := p.PickExcludingForRealm(nil, "m", "cn"); got == nil || got.UID != "a" {
		t.Fatalf("第一次选号=%v want a", got)
	}
	if got := p.PickExcludingForRealm(nil, "m", "cn"); got == nil || got.UID != "b" {
		t.Fatalf("第二次选号=%v want b", got)
	}
	p.Flush()
	after, err := os.ReadFile(fp)
	if before == nil {
		if !os.IsNotExist(err) {
			t.Fatalf("选号后的 Flush 新建了 state.json（运行期游标不应落盘）: %v", err)
		}
	} else if err != nil || string(after) != string(before) {
		t.Fatalf("选号后的 Flush 改写了 state.json（运行期游标不应落盘）: err=%v", err)
	}

	// 停机强制落盘：游标写入，重启后从下一个号继续。
	p.Close()
	shutdown, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("Close 未落盘: %v", err)
	}
	if string(shutdown) == string(before) {
		t.Fatal("Close 没有写入调度游标（重启后续不上轮询位置）")
	}
	if len(shutdown) == 0 {
		t.Fatal("Close 写出空文件")
	}

	q := New(fp)
	defer q.Close()
	q.SyncToDir(accounts)
	if err := q.SetScheduling(RegionStrategies{Domestic: RoundRobin, International: RoundRobin},
		RegionAccountOrder{}); err != nil {
		t.Fatalf("重启后 SetScheduling: %v", err)
	}
	if got := q.PickExcludingForRealm(nil, "m", "cn"); got == nil || got.UID != "a" {
		t.Fatalf("重启后应续到 a（a→b 之后的下一个），got %v", got)
	}
}

// TestCursorCheckpointWritesPeriodically 崩溃安全网：即使没有 Close，检查点也必须把
// 游标写进 state.json（否则 kill -9 / OOM 后轮询位置退回起点）。
func TestCursorCheckpointWritesPeriodically(t *testing.T) {
	old := cursorCheckpointInterval
	cursorCheckpointInterval = 5 * time.Millisecond
	defer func() { cursorCheckpointInterval = old }()

	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	defer p.Close()
	accounts := []*auth.Auth{{UID: "a"}, {UID: "b"}}
	p.SyncToDir(accounts)
	if err := p.SetScheduling(RegionStrategies{Domestic: RoundRobin, International: RoundRobin},
		RegionAccountOrder{}); err != nil {
		t.Fatalf("SetScheduling: %v", err)
	}
	p.PickExcludingForRealm(nil, "m", "cn") // 推进游标到 a
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(fp); err == nil {
			var st struct {
				Scheduling map[string]struct {
					UID string `json:"uid"`
				} `json:"scheduling"`
			}
			if json.Unmarshal(raw, &st) == nil && len(st.Scheduling) > 0 {
				for _, c := range st.Scheduling {
					if c.UID == "a" {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("检查点周期内没有把调度游标写入 state.json")
}
