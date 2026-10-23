package panel

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// cstZone 与 scheduler 同口径（UTC+8），日志按 CST 切天。
var cstZone = time.FixedZone("CST", 8*3600)

// LogStore 运行日志的按天 JSONL 落盘（重启后回填环形缓冲，支持导出）。
//
// 与 reqlog 的归档互补：reqlog 记逐请求结构化元数据，这里记面板「运行日志」
// 的原始三频道文本（sys/task/chat）。落盘失败只降级为纯内存，不影响主流程。
type LogStore struct {
	mu            sync.Mutex
	dir           string
	retentionDays int
	maxBytes      int64
	file          *os.File
	bw            *bufio.Writer
	day           string
}

// NewLogStore 创建存储并做一次清理。retentionDays<=0 回退 7，maxMB<=0 回退 50。
func NewLogStore(dir string, retentionDays, maxMB int) *LogStore {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if maxMB <= 0 {
		maxMB = 50
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	s := &LogStore{dir: dir, retentionDays: retentionDays, maxBytes: int64(maxMB) << 20}
	s.Cleanup()
	return s
}

// Append 追加一条日志；跨天自动切文件。失败静默（日志系统不能反过来拖垮主流程）。
func (s *LogStore) Append(e LogEntry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	day := e.TS.In(cstZone).Format("2006-01-02")
	if s.file == nil || day != s.day {
		s.closeLocked()
		f, err := os.OpenFile(filepath.Join(s.dir, "panel-"+day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		s.file = f
		s.bw = bufio.NewWriterSize(f, 64<<10)
		s.day = day
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := s.bw.Write(append(b, '\n')); err != nil {
		return
	}
	if s.bw.Buffered() >= 48<<10 {
		_ = s.bw.Flush()
	}
}

func (s *LogStore) closeLocked() {
	if s.bw != nil {
		_ = s.bw.Flush()
		s.bw = nil
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// Close 刷盘并关闭（进程退出时调用）。
func (s *LogStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// fileList 返回升序排列的 panel-YYYY-MM-DD.jsonl 路径。
func (s *LogStore) fileList() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "panel-") || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(s.dir, n))
	}
	sort.Strings(out)
	return out
}

// LoadRecent 读取最近 limit 条（跨文件，按时间顺序），用于启动回填 Ring。
func (s *LogStore) LoadRecent(limit int) []LogEntry {
	if s == nil || limit <= 0 {
		return nil
	}
	s.mu.Lock()
	s.closeLocked() // 先刷盘，保证能读到刚写入的行
	s.mu.Unlock()
	files := s.fileList()
	// 只读最后两个文件足够覆盖 500 行（单日文件通常远小于此）。
	if len(files) > 2 {
		files = files[len(files)-2:]
	}
	var all []LogEntry
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e LogEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Text != "" {
				all = append(all, e)
			}
			if len(all) > limit*4 && len(all) > 4000 {
				// 单文件异常大时只保留尾部窗口。
				all = all[len(all)-limit:]
			}
		}
		_ = f.Close()
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

// Cleanup 按保留天数与总量上限删除旧文件（与 reqlog 同口径）。
func (s *LogStore) Cleanup() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.fileList()
	type item struct {
		path string
		size int64
	}
	items := make([]item, 0, len(files))
	var total int64
	for _, p := range files {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		items = append(items, item{p, info.Size()})
		total += info.Size()
	}
	cutoff := time.Now().In(cstZone).AddDate(0, 0, -s.retentionDays)
	for _, it := range items {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(it.path), "panel-"), ".jsonl")
		t, err := time.ParseInLocation("2006-01-02", day, cstZone)
		if err != nil {
			continue
		}
		if t.Before(cutoff) || total > s.maxBytes {
			if os.Remove(it.path) == nil {
				total -= it.size
			}
		}
	}
}

// TotalBytes 当前落盘总量（导出上限与面板展示用）。
func (s *LogStore) TotalBytes() int64 {
	if s == nil {
		return 0
	}
	var total int64
	for _, p := range s.fileList() {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}

// ReadSince 读取 cutoff 之后的日志（含频道过滤），最多 maxBytes 字节。
// truncated=true 表示达到上限被截断。
func (s *LogStore) ReadSince(cutoff time.Time, chans map[string]bool, maxBytes int64) ([]LogEntry, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	s.closeLocked()
	s.mu.Unlock()
	var out []LogEntry
	var bytes int64
	for _, path := range s.fileList() {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e LogEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if !cutoff.IsZero() && e.TS.Before(cutoff) {
				continue
			}
			if len(chans) > 0 && !chans[e.Ch] {
				continue
			}
			out = append(out, e)
			bytes += int64(len(e.Text) + 48)
			if maxBytes > 0 && bytes > maxBytes {
				_ = f.Close()
				return out, true
			}
		}
		_ = f.Close()
	}
	return out, false
}
