package panel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 运行日志落盘 → 重启回填：更新面板后历史日志不再消失。
func TestLogStorePersistAndReload(t *testing.T) {
	dir := t.TempDir()
	st := NewLogStore(dir, 7, 50)
	if st == nil {
		t.Fatal("NewLogStore 失败")
	}
	now := time.Now().In(cstZone)
	st.Append(LogEntry{TS: now, Ch: ChSys, Text: "系统一行"})
	st.Append(LogEntry{TS: now, Ch: ChTask, Text: "travel a: ok"})
	st.Close()

	st2 := NewLogStore(dir, 7, 50)
	got := st2.LoadRecent(500)
	if len(got) != 2 {
		t.Fatalf("回填 %d 条 want 2: %+v", len(got), got)
	}
	if got[0].Ch != ChSys || got[1].Ch != ChTask {
		t.Fatalf("频道/顺序不对: %+v", got)
	}
}

// 导出范围 + 频道过滤。
func TestLogStoreReadSinceFilter(t *testing.T) {
	dir := t.TempDir()
	st := NewLogStore(dir, 7, 50)
	old := time.Now().In(cstZone).AddDate(0, 0, -3)
	st.Append(LogEntry{TS: old, Ch: ChSys, Text: "old"})
	st.Append(LogEntry{TS: time.Now().In(cstZone), Ch: ChSys, Text: "new-sys"})
	st.Append(LogEntry{TS: time.Now().In(cstZone), Ch: ChChat, Text: "new-chat"})
	st.Close()

	cutoff := cstMidnight(time.Now())
	got, truncated := st.ReadSince(cutoff, map[string]bool{ChSys: true}, 1<<20)
	if truncated {
		t.Fatal("不应截断")
	}
	if len(got) != 1 || got[0].Text != "new-sys" {
		t.Fatalf("过滤结果=%+v", got)
	}
}

// 导出脱敏：token / Bearer / 手机号不能原样出现在文件里。
func TestMaskSecrets(t *testing.T) {
	in := `refreshToken=abcdef123456 Bearer sk-live-abcdef 手机 13812345678`
	out := maskSecrets(in)
	for _, bad := range []string{"abcdef123456", "sk-live-abcdef", "13812345678"} {
		if strings.Contains(out, bad) {
			t.Errorf("未脱敏 %q: %s", bad, out)
		}
	}
}

// 导出 ZIP：含 panel-logs.ndjson 与 meta.json，且内容已脱敏。
func TestLogsExportZip(t *testing.T) {
	dir := t.TempDir()
	p := &Panel{cfg: Config{LogDir: dir, LogExportMaxMB: 10, Version: "test"}, logs: NewRing(100)}
	p.logStore = NewLogStore(dir, 7, 50)
	p.logs.SetSink(p.logStore.Append)
	_, _ = p.logs.Write([]byte("系统 token=secret123456 启动"))
	p.logStore.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panel/api/logs/export?range=all&channels=all", nil)
	p.logsExport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip 解析失败: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "panel-logs.ndjson" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(b), "secret123456") {
				t.Errorf("导出未脱敏: %s", b)
			}
			var row map[string]any
			if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(b), []byte("\n"))[0], &row); err != nil {
				t.Errorf("ndjson 行解析失败: %v", err)
			}
		}
	}
	if !names["panel-logs.ndjson"] || !names["meta.json"] {
		t.Fatalf("zip 内文件缺失: %v", names)
	}
}
