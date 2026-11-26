package panel

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 面板静态资源的内容协商与缓存校验（v1.27.19）。
//
// 三条目标：① 声明支持 gzip 的客户端拿到压缩副本、不支持的拿原文，两者字节数各自与
// Content-Length 自洽；② 资源未变化时 304 且 0 字节（此前每次刷新都整份重下 337 KB）；
// ③ 304 仍然带齐安全头，并且 API 响应一律不压（压缩缓冲会毁掉流式/增加无谓 CPU）。

func get(t *testing.T, p *Panel, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	return out
}

func TestStaticAssetGzipNegotiation(t *testing.T) {
	p := newTestPanel()

	t.Run("不声明 gzip 时回原文", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d，want 200", rec.Code)
		}
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Fatalf("未声明 gzip 却带 Content-Encoding=%q", enc)
		}
		if got, want := rec.Body.Len(), len(appJS); got != want {
			t.Fatalf("正文 %d 字节，want %d", got, want)
		}
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(appJS)) {
			t.Fatalf("Content-Length = %q，want %d", cl, len(appJS))
		}
		if !bytes.Equal(rec.Body.Bytes(), appJS) {
			t.Fatal("原文与内置资源不一致")
		}
	})

	t.Run("声明 gzip 时回压缩副本", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
		if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
			t.Fatalf("Content-Encoding = %q，want gzip", enc)
		}
		body := rec.Body.Bytes()
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
			t.Fatalf("Content-Length = %q，实际正文 %d 字节（不匹配会被客户端判截断）", cl, len(body))
		}
		if len(body) >= len(appJS)*9/10 {
			t.Fatalf("压缩后 %d 字节，未明显小于原文 %d", len(body), len(appJS))
		}
		if !bytes.Equal(gunzip(t, body), appJS) {
			t.Fatal("解压后与内置资源不一致")
		}
	})

	t.Run("gzip;q=0 视为拒绝", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", map[string]string{"Accept-Encoding": "gzip;q=0, identity"})
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Fatalf("q=0 仍压缩：Content-Encoding=%q", enc)
		}
	})

	t.Run("Accept-Encoding: * 通配接受", func(t *testing.T) {
		rec := get(t, p, "/panel/mobile.css", map[string]string{"Accept-Encoding": "*"})
		if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
			t.Fatalf("通配未启用压缩：Content-Encoding=%q", enc)
		}
	})

	t.Run("Vary 必须声明", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", nil)
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
			t.Fatalf("Vary = %q，缺 Accept-Encoding（中间缓存会把压缩版发给不支持的客户端）", v)
		}
	})
}

func TestStaticAssetETagNotModified(t *testing.T) {
	p := newTestPanel()

	first := get(t, p, "/panel/app.js", map[string]string{"Accept-Encoding": "gzip"})
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("缺少 ETag")
	}

	t.Run("命中 ETag 回 304 且 0 字节", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", map[string]string{"If-None-Match": etag})
		if rec.Code != http.StatusNotModified {
			t.Fatalf("code = %d，want 304", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("304 仍带 %d 字节正文", rec.Body.Len())
		}
		// 304 不带正文：带上 Content-Encoding / Content-Length 会被当成截断。
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Fatalf("304 带 Content-Encoding=%q", enc)
		}
		if cl := rec.Header().Get("Content-Length"); cl != "" {
			t.Fatalf("304 带 Content-Length=%q", cl)
		}
		// 安全头必须照样在（老浏览器不会拿缓存的响应头重放这些策略）。
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("304 缺 CSP")
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("304 缺 nosniff")
		}
	})

	t.Run("多值 / 弱校验 / 通配", func(t *testing.T) {
		for _, inm := range []string{`"other", ` + etag, "W/" + etag, "*"} {
			rec := get(t, p, "/panel/app.js", map[string]string{"If-None-Match": inm})
			if rec.Code != http.StatusNotModified {
				t.Fatalf("If-None-Match=%q 未命中 304（code=%d）", inm, rec.Code)
			}
		}
	})

	t.Run("不匹配的 ETag 回 200", func(t *testing.T) {
		rec := get(t, p, "/panel/app.js", map[string]string{"If-None-Match": `"deadbeef"`})
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d，want 200", rec.Code)
		}
	})
}

func TestIndexAssetVersionedAndCacheable(t *testing.T) {
	p := newTestPanel()
	rec := get(t, p, "/panel/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("app.js?v=test")) {
		t.Fatal("页面正文缺少版本查询串（升级后 CDN/浏览器会继续用旧脚本）")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("页面缺少 ETag")
	}
	// 同一版本两次请求 ETag 必须一致（否则永远拿不到 304）。
	if again := get(t, p, "/panel/", nil).Header().Get("ETag"); again != etag {
		t.Fatalf("ETag 不稳定：%q vs %q", etag, again)
	}
	// 压缩版本解压后与版本化正文一致。
	gz := get(t, p, "/panel/", map[string]string{"Accept-Encoding": "gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("页面未压缩")
	}
	if !bytes.Equal(gunzip(t, gz.Body.Bytes()), rec.Body.Bytes()) {
		t.Fatal("页面压缩副本与原文不一致")
	}
}

// API 响应一律不压：压缩会给几 KB 的 JSON 增加 CPU 与一条出错路径，更重要的是
// 提醒后来者不要把 gzip 挂成全局中间件（那会波及 SSE 流式首包）。
func TestPanelAPIResponsesNeverCompressed(t *testing.T) {
	p := newTestPanel()
	for _, path := range []string{"/panel/api/overview", "/panel/api/models", "/panel/api/logs"} {
		rec := get(t, p, path, map[string]string{"Accept-Encoding": "gzip"})
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Errorf("%s 被压缩（Content-Encoding=%q）", path, enc)
		}
	}
}
