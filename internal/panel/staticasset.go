// staticasset.go 面板静态资源的内容协商（gzip）、强 ETag / 304 与显式 Content-Length。
//
// 背景（实测，2026-11-26）：面板四个文本资源此前是「裸传 + 每次全量重下」——
//   - 没有 Content-Encoding：index.html 98 KB、app.js 205 KB、mobile.css 20 KB、
//     mobile.js 13 KB 原样出站，合计 337 KB；直连访问时 RTT 166ms，仅 TCP 慢启动就要
//     多跑 3~4 轮，首屏 1.9s（同一批文件经 CDN 压缩后只有 114 KB、451ms）。
//   - 没有 ETag / Last-Modified：`Cache-Control: no-cache` 要求每次使用前回源校验，
//     但没有校验器可对比，浏览器只能整份重下，刷新一次又是 337 KB。
//   - 没有 Content-Length：net/http 回落 chunked，浏览器拿不到体积、也缺少复用判据。
//
// 这里给「四个内置文本资源」补上三件事：启动时预压缩（运行时零 CPU）、强 ETag
// （未变化回 304、0 字节）、显式 Content-Length。**只作用于面板静态资源**：SSE
// （/v1/chat/completions 等）与 /panel/api/* 一律不压——压缩缓冲会毁掉流式首包，
// 而那正是网关的核心链路；面板 JSON 只有几 KB，压了也不省什么。
package panel

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minCompressBytes 小于这个大小不做压缩：gzip 头尾固定开销 + CPU 换不来收益。
const minCompressBytes = 1024

// staticAsset 一份可协商的内置资源。
type staticAsset struct {
	raw   []byte // 原文（未压缩）
	gz    []byte // gzip 预压缩副本；nil = 该资源不提供压缩形态
	etag  string // 强 ETag（内容哈希）
	ctype string
}

// newStaticAsset 构造资源描述并**在启动时**压好 gzip：一次性 CPU，之后每次请求零成本。
// 压缩收益不足（压完没有明显变小）时不保留压缩副本，避免为几十字节多一层变异。
func newStaticAsset(raw []byte, ctype string) *staticAsset {
	a := &staticAsset{raw: raw, ctype: ctype}
	sum := sha256.Sum256(raw)
	// 取前 16 字节（128 位）足够防碰撞，头也更短。
	a.etag = `"` + hex.EncodeToString(sum[:16]) + `"`
	if len(raw) >= minCompressBytes {
		if gz, ok := gzipBytes(raw); ok && len(gz) < len(raw)-len(raw)/10 {
			a.gz = gz
		}
	}
	return a
}

// gzipBytes 以最高压缩级别压一份（内置资源总量几百 KB，启动期几十毫秒可接受）。
func gzipBytes(raw []byte) ([]byte, bool) {
	var buf bytes.Buffer
	buf.Grow(len(raw) / 3)
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, false
	}
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, false
	}
	if err := zw.Close(); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// indexAssetCache 页面资源带 ?v=<版本>，正文按版本替换过；每个进程只有一个版本，
// 因此按版本缓存一份即可（重启/升级后版本变，缓存自然失效）。
var indexAssetCache struct {
	mu      sync.Mutex
	version string
	asset   *staticAsset
}

// indexAsset 返回当前版本的面板页面资源（含预压缩与 ETag）。
func indexAsset(version string) *staticAsset {
	indexAssetCache.mu.Lock()
	defer indexAssetCache.mu.Unlock()
	if indexAssetCache.asset != nil && indexAssetCache.version == version {
		return indexAssetCache.asset
	}
	a := newStaticAsset(versionedIndex(version), "text/html; charset=utf-8")
	indexAssetCache.version = version
	indexAssetCache.asset = a
	return a
}

// serveStatic 输出一份内置资源：安全头 → Vary → ETag → 304 短路 → 压缩协商 → 显式长度。
//
// 顺序是有意的：304 必须在写 Content-Encoding/Content-Length 之前返回（304 不带正文，
// 带上这两个头会让部分客户端把「没有正文」当成截断）。
func serveStatic(w http.ResponseWriter, r *http.Request, a *staticAsset) {
	setSecurityHeaders(w)
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	// 同一 URL 会按 Accept-Encoding 返回不同字节：缺 Vary 会被中间缓存/CDN 串味
	// （缓存了压缩版却发给不支持的客户端，或反之）。
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", a.etag)
	if etagMatches(r.Header.Get("If-None-Match"), a.etag) {
		// 资源内容未变（URL 已带 ?v=<版本>，升级即换 URL）：0 字节回 304。
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.raw
	if a.gz != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	// 显式长度：否则 net/http 回落 chunked，浏览器拿不到体积、也缺少复用判据。
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// etagMatches If-None-Match 是否命中给定 ETag。支持 `*` 与逗号分隔的多个值，
// 并忽略弱校验前缀 `W/`（RFC 9110 的强/弱比较在这里等价：内置资源不会随时间变化）。
func etagMatches(ifNoneMatch, etag string) bool {
	header := strings.TrimSpace(ifNoneMatch)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		v := strings.TrimSpace(part)
		v = strings.TrimPrefix(v, "W/")
		if v == etag {
			return true
		}
	}
	return false
}

// acceptsGzip Accept-Encoding 是否接受 gzip（含 q=0 显式拒绝与 `*` 通配）。
func acceptsGzip(header string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	star := false
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		token := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		explicit := false
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if len(f) > 2 && (f[0] == 'q' || f[0] == 'Q') && f[1] == '=' {
				explicit = true
				if v, err := strconv.ParseFloat(strings.TrimSpace(f[2:]), 64); err == nil {
					q = v
				}
			}
		}
		switch token {
		case "gzip", "x-gzip":
			return q > 0 || !explicit
		case "*":
			star = q > 0 || !explicit
		}
	}
	return star
}
