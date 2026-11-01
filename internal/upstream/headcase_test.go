package upstream

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
)

// TestNormalizeHeaderSpelling 逐名映射：官方混合拼写必须原样保留，Go canonical 的
// 缩写痕迹必须被纠正；六个特判头必须保持 canonical（否则 UA 回落、CL 重复、AE 双发）。
func TestNormalizeHeaderSpelling(t *testing.T) {
	in := http.Header{}
	for _, k := range []string{
		"X-B3-Parentspanid", // Go 会把 ParentSpanId 拍成 Parentspanid
		"X-Conversation-Id", // Go 会把 ID 拍成 Id
		"X-Stainless-Arch",  // 官方全小写
		"Acp-Connection-Id", // 官方全小写
		"X-Request-Id",      // 官方保留大写 ID
		"User-Agent",        // 特判头：必须保持 canonical
		"Content-Length",    // 特判头
		"Accept-Encoding",   // 特判头
		"X-Enterprise-Id",   // 表外：不猜，原样
	} {
		in.Set(k, "v")
	}
	out := normalizeHeaderSpelling(&http.Request{Header: in}).Header

	for _, want := range []string{
		"X-B3-ParentSpanId", "X-Conversation-ID", "x-stainless-arch",
		"acp-connection-id", "X-Request-ID", "User-Agent",
		"Content-Length", "Accept-Encoding", "X-Enterprise-Id",
	} {
		if _, ok := out[want]; !ok {
			t.Errorf("缺少 %q，实际键：%v", want, keysOf(out))
		}
	}
	// 特判头被小写化会直接破功：Go 查不到 user-agent 就回落 Go-http-client/1.1。
	if _, bad := out["user-agent"]; bad {
		t.Error("user-agent 被小写化：Go 将回落 Go-http-client/1.1")
	}
}

func keysOf(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	return out
}

// TestHeaderSpellingOnWire 真实 wire 上必须出现官方拼写（RoundTripper 层改 map 不够，
// 要看 net/http 写出的字节）。
func TestHeaderSpellingOnWire(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			got <- ""
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		var b strings.Builder
		for {
			line, rerr := br.ReadString('\n')
			b.WriteString(line)
			if rerr != nil || line == "\r\n" {
				break
			}
		}
		got <- b.String()
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"))
	}()

	// 用生产构造函数：拼写纠正挂在 Transport 外层，直接 new(http.Client) 测不到。
	c := New()
	defer c.HTTP.CloseIdleConnections()
	req, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+"/v2/report", strings.NewReader(`{}`))
	req.Header.Set("X-B3-Parentspanid", "abc")
	req.Header.Set("X-Stainless-Arch", "arm64")
	req.Header.Set("User-Agent", "WorkBuddy/9.9.9 WorkBuddy/9.9.9 CLI/2.0.0")
	if _, err := c.doJSON(req); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	wire := <-got
	// 给 channel 读取一个兜底，避免 goroutine 卡住时测试挂死（上面是阻塞读，已由 server 写回）。
	for _, want := range []string{"X-B3-ParentSpanId: abc", "x-stainless-arch: arm64", "User-Agent: WorkBuddy/9.9.9"} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire 缺少 %q，实际：\n%s", want, wire)
		}
	}
}
