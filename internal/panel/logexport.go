package panel

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// logExportLock 单飞：同一时刻只允许一个导出任务，避免并发大导出拖死磁盘。
var logExportLock sync.Mutex

var (
	reSecretKV = regexp.MustCompile(`(?i)((?:access_?token|refresh_?token|token|api_?key|authorization|cookie)\s*[:=]\s*"?)([A-Za-z0-9._\-]{6,})`)
	rePhone    = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	reBearer   = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._\-]{6,}`)
)

// maskSecrets 导出前脱敏：token/key/cookie、Bearer 串与手机号。
func maskSecrets(s string) string {
	s = reSecretKV.ReplaceAllString(s, "${1}***")
	s = reBearer.ReplaceAllString(s, "${1}***")
	s = rePhone.ReplaceAllString(s, "1**********")
	return s
}

func cstMidnight(t time.Time) time.Time {
	t = t.In(cstZone)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, cstZone)
}

// logsExport 导出运行日志（ZIP：panel-logs.ndjson + meta.json）。
//
// 参数：range=today|3d|7d|all，channels=sys,task,chat（缺省全部）。
// 上限由 logging.log_export_max_mb 控制，超限返回 413，不产生半截文件。
func (p *Panel) logsExport(w http.ResponseWriter, r *http.Request) {
	if p.logStore == nil {
		writeErr(w, http.StatusBadRequest, "运行日志持久化未启用（logging.panel_log_persist_enabled=false）")
		return
	}
	q := r.URL.Query()
	rng := strings.TrimSpace(q.Get("range"))
	if rng == "" {
		rng = "today"
	}
	now := time.Now().In(cstZone)
	var cutoff time.Time
	label := rng
	switch rng {
	case "today":
		cutoff = cstMidnight(now)
	case "3d":
		cutoff = cstMidnight(now.AddDate(0, 0, -2))
	case "7d":
		cutoff = cstMidnight(now.AddDate(0, 0, -6))
	case "all":
		cutoff = time.Time{}
		label = "all"
	default:
		writeErr(w, http.StatusBadRequest, "range 只支持 today / 3d / 7d / all")
		return
	}
	chans := map[string]bool{}
	if v := strings.TrimSpace(q.Get("channels")); v != "" && v != "all" {
		for _, c := range strings.Split(v, ",") {
			switch strings.TrimSpace(c) {
			case "sys", "task", "chat":
				chans[strings.TrimSpace(c)] = true
			}
		}
	}
	maxMB := p.cfg.LogExportMaxMB
	if maxMB <= 0 {
		maxMB = 200
	}
	if !logExportLock.TryLock() {
		writeErr(w, http.StatusTooManyRequests, "已有导出任务正在进行，请稍后再试")
		return
	}
	defer logExportLock.Unlock()

	entries, truncated := p.logStore.ReadSince(cutoff, chans, int64(maxMB)<<20)
	if truncated {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("导出内容超过 %d MiB 上限，请缩短范围或减少频道", maxMB))
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="wb2api-logs-%s-%s.zip"`, now.Format("20060102-1504"), label))
	zw := zip.NewWriter(w)
	f, err := zw.Create("panel-logs.ndjson")
	if err != nil {
		return
	}
	bw := bufio.NewWriterSize(f, 64<<10)
	for _, e := range entries {
		row := struct {
			TS   string `json:"ts"`
			Ch   string `json:"ch"`
			Text string `json:"text"`
		}{e.TS.In(cstZone).Format(time.RFC3339), e.Ch, maskSecrets(e.Text)}
		if b, err := json.Marshal(row); err == nil {
			_, _ = bw.Write(append(b, '\n'))
		}
	}
	_ = bw.Flush()
	if mf, err := zw.Create("meta.json"); err == nil {
		_ = json.NewEncoder(mf).Encode(map[string]any{
			"exported_at": now.Format(time.RFC3339),
			"range":       rng,
			"channels":    chans,
			"lines":       len(entries),
			"version":     p.cfg.Version,
			"note":        "运行日志（sys/task/chat）。请求逐条流水请在面板「请求记录」页查看。",
		})
	}
	_ = zw.Close()
	// 导出自证：写一条 sys 日志（会同时落盘）。
	_, _ = p.logs.Write([]byte(fmt.Sprintf("panel: 运行日志已导出 range=%s lines=%d", rng, len(entries))))
}
