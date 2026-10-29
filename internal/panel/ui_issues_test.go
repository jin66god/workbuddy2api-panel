package panel

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestLogTimeHasCalendarDate issue #7：请求记录 / 运行日志 / 积分历史的时间必须
// 带年月日（只显示 时:分:秒 时跨天分不清），且三处共用同一个格式化函数。
func TestLogTimeHasCalendarDate(t *testing.T) {
	js, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`function fmtLogTime(v)`,
		`d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate())`,
		`const t = fmtLogTime(e.ts);`,
		`return fmtLogTime(e.time);`,
	} {
		if !bytes.Contains(js, []byte(want)) {
			t.Fatalf("app.js 缺少 %q", want)
		}
	}
	if n := strings.Count(string(js), `const when = fmtLogTime(e && e.time) || '—';`); n != 2 {
		t.Fatalf("请求记录表/提示行应各有 1 处 fmtLogTime，got %d", n)
	}
	// 旧的纯时间格式不得残留实际调用（注释里提到不限）。
	if strings.Contains(string(js), `toLocaleTimeString('zh-CN'`) {
		t.Fatal("仍有 toLocaleTimeString 调用：三处时间必须统一走 fmtLogTime")
	}
}

// TestChartAxisLabelsDoNotOverlap issue #8：x 轴刻度必须做宽度估算与最小间距过滤，
// 否则数据点聚簇时 start 与第一个 middle 会叠字（桌面 1200 宽与窄屏 440 宽都要）。
func TestChartAxisLabelsDoNotOverlap(t *testing.T) {
	for _, f := range []struct {
		name, file, gap string
	}{
		{"桌面", "app.js", `if (c.left >= keep[keep.length - 1].right + 10) { keep.push(c); continue; }`},
		{"窄屏", "mobile.js", `if (c.left >= keep[keep.length - 1].right + 8) { keep.push(c); continue; }`},
	} {
		js, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`const cand = [];`,
			`const w = Math.max(18, label.length * 6.6);`,
			`const keep = [];`,
			f.gap,
			`while (keep.length >= 2 && c.left < keep[keep.length - 1].right`,
		} {
			if !bytes.Contains(js, []byte(want)) {
				t.Fatalf("%s(%s) 缺少 %q", f.name, f.file, want)
			}
		}
	}
}
