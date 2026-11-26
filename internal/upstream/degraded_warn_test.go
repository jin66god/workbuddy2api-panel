package upstream

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// 永续降级的目录探测每轮刷新都打一行 WARN，几天下来几十条会把真故障淹掉。
// 这里钉死「同 key 只报一次、不同 key 各报一次」，防止后续新增探测路径又把裸
// log.Printf 加回来。
func TestDegradedWarnOnceThrottles(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	degradedWarnOnce("t/once", "WARN: boom %d", 1)
	degradedWarnOnce("t/once", "WARN: boom %d", 1)
	degradedWarnOnce("t/once", "WARN: boom %d", 1)
	degradedWarnOnce("t/other", "WARN: other %d", 2)

	out := buf.String()
	if n := strings.Count(out, "boom"); n != 1 {
		t.Errorf("同 key 只该报一次，实际 %d 次：%s", n, out)
	}
	if n := strings.Count(out, "other"); n != 1 {
		t.Errorf("不同 key 各报一次，实际 %d 次：%s", n, out)
	}
}

// 四个降级点必须都走节流版：漏一处就仍然每轮刷一行。
// 判据是源码里不再有裸 log.Printf 打这两句话（日志串是排障口径，逐字匹配）。
func TestDegradedWarnSitesThrottled(t *testing.T) {
	for _, f := range []string{"client.go", "global_models.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		src := string(raw)
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "log.Printf(") {
				continue
			}
			if strings.Contains(line, "probe failed (degraded to enterprise endpoint)") ||
				strings.Contains(line, "enterprise endpoint failed") {
				t.Errorf("%s 仍有不节流的降级 WARN：%s", f, trimmed)
			}
		}
	}
}
