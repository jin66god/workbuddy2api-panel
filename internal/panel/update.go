// update.go 版本更新检查与面板侧「一键更新」入口。
//
// 两件事分开：
//
//  1. 更新提醒：定时拉 GitHub Releases 的 latest tag，与进程内 appVersion 比较，
//     结果并入 /panel/api/overview（前端已有 5 秒轮询，不额外增加请求）。检查结果
//     缓存 interval（默认 6 小时），失败只记 last_err 不打扰用户，下次轮询重试。
//
//  2. 一键更新：**面板自己不碰 Docker**。容器内既没有 docker CLI 也没有 socket，
//     给它 socket 等于把宿主 root 交给一个 HTTP 面板，代价远大于收益。真正的一键
//     由旁路升级器完成（compose 里的 watchtower，HTTP API 模式）：
//     面板 POST {watchtower}/v1/update → 它拉新镜像并按 label 重建本容器。
//     没配 watchtower_url 时按钮降级为「复制升级命令」，不假装能一键。
package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdateOptions 更新检查配置（由 cmd/server 从环境变量注入，见 WB2API_UPDATE_*）。
type UpdateOptions struct {
	Enabled         bool          // false = 完全不查、不展示更新信息
	Repo            string        // GitHub owner/name，空 = 不查
	Interval        time.Duration // 检查间隔，<=0 时回落 6h
	WatchtowerURL   string        // 旁路升级器地址（如 http://watcher:8080）；空 = 无一键能力
	WatchtowerToken string        // 升级器 HTTP API 的 Bearer token（可空）
}

// updateInfo 更新检查的缓存状态。零值可用（Enabled=false）。
type updateInfo struct {
	mu          sync.Mutex
	opt         UpdateOptions
	latest      string    // 最新 tag（原样，如 v1.25.0）
	releaseURL  string    // 发布页地址
	publishedAt time.Time // 该 release 发布时间
	checkedAt   time.Time // 上次成功（或失败）检查时刻
	lastErr     string    // 上次检查错误（成功时清空）
	checking    bool      // 已有一次检查在飞，避免并发拉取
}

// newUpdateInfo 构建检查器；opt.Enabled 为 false 时所有方法都退化为空操作。
func newUpdateInfo(opt UpdateOptions) *updateInfo {
	if opt.Interval <= 0 {
		opt.Interval = 6 * time.Hour
	}
	return &updateInfo{opt: opt}
}

// view 返回给前端的更新状态；顺便在缓存过期时触发一次异步刷新。
//
// 返回字段：
//   - current / latest：当前版本与最新 tag
//   - has_update：latest 是否比 current 新（语义化比较）
//   - one_click：是否配置了旁路升级器（决定按钮是「立即更新」还是「复制命令」）
//   - command：无一键能力时给用户复制的 compose 命令
func (u *updateInfo) view(current string) map[string]any {
	if u == nil || !u.opt.Enabled || u.opt.Repo == "" {
		return map[string]any{"enabled": false}
	}
	u.maybeRefresh()

	u.mu.Lock()
	latest, url, checkedAt, lastErr := u.latest, u.releaseURL, u.checkedAt, u.lastErr
	u.mu.Unlock()

	out := map[string]any{
		"enabled":    true,
		"current":    current,
		"latest":     latest,
		"has_update": latest != "" && versionNewer(current, latest),
		"checked_at": checkedAt,
		"one_click":  strings.TrimSpace(u.opt.WatchtowerURL) != "",
		"command":    composeUpgradeCommand,
	}
	if url != "" {
		out["release_url"] = url
	}
	if lastErr != "" {
		out["error"] = lastErr
	}
	return out
}

// composeUpgradeCommand 无旁路升级器时展示给用户的命令。镜像 tag 用 compose 里的
// ${WB2API_TAG:-latest}，所以 pull 会拿到最新；不加 --force-recreate，compose 自己
// 判断镜像 digest 变化后重建。
const composeUpgradeCommand = "docker compose pull && docker compose up -d"

// maybeRefresh 缓存过期且无检查在飞时，异步拉一次 latest release。
//
// 异步是刻意的：overview 是 5 秒轮询的热路径，GitHub API 慢或挂住时不能让面板跟着卡。
func (u *updateInfo) maybeRefresh() {
	u.mu.Lock()
	if u.checking {
		u.mu.Unlock()
		return
	}
	if time.Since(u.checkedAt) < u.opt.Interval && !u.checkedAt.IsZero() {
		u.mu.Unlock()
		return
	}
	u.checking = true
	u.mu.Unlock()

	go func() {
		latest, url, published, err := fetchLatestRelease(u.opt.Repo)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.checking = false
		u.checkedAt = time.Now()
		if err != nil {
			u.lastErr = err.Error()
			return
		}
		u.latest, u.releaseURL, u.publishedAt, u.lastErr = latest, url, published, ""
	}()
}

// fetchLatestRelease 读 GitHub Releases API 的 latest。
//
// 不带 token 时限流 60 次/小时/IP，按 6 小时一次的节奏绰绰有余；私有库需要 token，
// 但本仓是公开库，不引入 key 管理。
func fetchLatestRelease(repo string) (tag, url string, published time.Time, err error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "workbuddy2api-panel-update-check")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("检查更新失败：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", time.Time{}, fmt.Errorf("检查更新失败：GitHub 返回 %d", resp.StatusCode)
	}
	var rel struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", time.Time{}, fmt.Errorf("检查更新失败：响应解析 %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return "", "", time.Time{}, fmt.Errorf("检查更新失败：release 缺少 tag_name")
	}
	return rel.TagName, rel.HTMLURL, rel.PublishedAt, nil
}

// versionNewer 报告 latest 是否严格新于 current。
//
// 两边都可能带杂讯：appVersion 是 "1.24.9-panel"，tag 是 "v1.25.0"。统一剥掉
// 前导 v/V 与首个 '-' 之后的后缀（-panel / -ci 等），再按点分段逐位比较；
// 缺位补 0（"1.25" == "1.25.0"）。非数字段按 0 处理，宁可漏报也不误报——
// 误报会让用户频繁看到「有新版本」却升不上去。
func versionNewer(current, latest string) bool {
	c, l := parseVersion(current), parseVersion(latest)
	n := len(c)
	if len(l) > n {
		n = len(l)
	}
	for i := 0; i < n; i++ {
		var a, b int
		if i < len(c) {
			a = c[i]
		}
		if i < len(l) {
			b = l[i]
		}
		if b != a {
			return b > a
		}
	}
	return false
}

// parseVersion 把 "v1.24.9-panel" 解析成 [1,24,9]。
func parseVersion(v string) []int {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, n)
	}
	return out
}

// updateStatus GET /panel/api/update：单独查询更新状态（前端刷新按钮/外部脚本用）。
func (p *Panel) updateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, p.upd.view(p.cfg.Version))
}

// updateApply POST /panel/api/update/apply：触发旁路升级器重建容器。
//
// 未配置 watchtower_url 时返回 409 + 可复制的命令：面板没有 Docker 权限，这是
// 能力边界而不是故障，前端按 mode 决定展示按钮还是命令。
func (p *Panel) updateApply(w http.ResponseWriter, r *http.Request) {
	if p.upd == nil || !p.upd.opt.Enabled || p.upd.opt.Repo == "" {
		writeErr(w, http.StatusConflict, "更新检查未启用")
		return
	}
	target := strings.TrimSpace(p.upd.opt.WatchtowerURL)
	if target == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":      false,
			"mode":    "command",
			"command": composeUpgradeCommand,
			"message": "未配置旁路升级器（update.watchtower_url），请在宿主机执行上面的命令；" +
				"配置后本按钮可直接触发重建。",
		})
		return
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(target, "/")+"/v1/update", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "升级器地址无效："+err.Error())
		return
	}
	if tok := strings.TrimSpace(p.upd.opt.WatchtowerToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "触发升级器失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("升级器返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(body))))
		return
	}
	log.Printf("panel: 已触发容器更新（升级器 %s）", target)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"mode":    "watchtower",
		"message": "已触发更新：升级器正在拉取新镜像并重建容器，约 30~60 秒后本页面会短暂断连。",
	})
}
