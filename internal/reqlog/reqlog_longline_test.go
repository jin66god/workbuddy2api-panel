package reqlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 一条超长事件行不得让整个归档读失败，也不得吞掉同文件里其它事件。
//
// 触发条件：开启 logging.request_archive_context 时 Event.Context 带整轮
// system+messages，客户端塞一张 base64 图片就是 MB 级单行。旧实现用
// bufio.Scanner（1MB 上限），超限后 Scan 直接返回 false，readFile 把
// bufio.ErrTooLong 上抛 → 请求记录页 / 导出整页 500。
func TestReadFileSkipsOversizedLine(t *testing.T) {
	oldMax := eventLineMaxBytes
	eventLineMaxBytes = 1024
	defer func() { eventLineMaxBytes = oldMax }()

	dir := t.TempDir()
	now := time.Now().Add(-time.Minute)
	line := func(e Event) string {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	big := line(Event{Time: now, RequestID: "big", Context: json.RawMessage(
		`"` + strings.Repeat("x", 4096) + `"`)})
	body := line(Event{Time: now, RequestID: "before", Model: "glm-5.2"}) + "\n" +
		big + "\n" +
		line(Event{Time: now, RequestID: "after", Model: "glm-5.2"}) + "\n"

	path := filepath.Join(dir, "requests-"+now.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, skipped, err := readFile(path, Filter{})
	if err != nil {
		t.Fatalf("一条超长行不该让整档读取失败: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(rows) != 2 || rows[0].RequestID != "before" || rows[1].RequestID != "after" {
		t.Fatalf("超长行前后的事件必须都读出来，got %+v", rows)
	}
}

// 上限之内的长行仍要正常解析（抬高上限不是关掉解析）。
func TestReadFileKeepsLongLineUnderLimit(t *testing.T) {
	oldMax := eventLineMaxBytes
	eventLineMaxBytes = 1 << 20
	defer func() { eventLineMaxBytes = oldMax }()

	dir := t.TempDir()
	now := time.Now()
	e := Event{Time: now, RequestID: "ctx", Context: json.RawMessage(
		`"` + strings.Repeat("y", 200<<10) + `"`)}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "requests-"+now.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, skipped, err := readFile(path, Filter{})
	if err != nil || skipped != 0 {
		t.Fatalf("err=%v skipped=%d", err, skipped)
	}
	if len(rows) != 1 || rows[0].RequestID != "ctx" || len(rows[0].Context) < 200<<10 {
		t.Fatalf("长上下文行必须完整解析，got %d rows", len(rows))
	}
}
