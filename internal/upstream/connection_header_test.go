package upstream

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// captureWire 起一个裸 TCP 监听，用给定 client 发一次请求，返回请求头原文。
func captureWire(t *testing.T, path string) string {
	t.Helper()
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
	c := &Client{HTTP: &http.Client{Transport: &http.Transport{}}}
	req, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+path, strings.NewReader(`{}`))
	if _, err := c.doJSON(req); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	select {
	case s := <-got:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("没抓到请求头")
		return ""
	}
}

// TestConnectionHeaderMatchesOfficial 官方每条 RPC 都显式写 Connection：
// chat/report/chat_busy 是 keep-alive，/v3/config 是 close。Go 默认两条都不写。
func TestConnectionHeaderMatchesOfficial(t *testing.T) {
	wire := captureWire(t, "/v2/report")
	if !strings.Contains(wire, "Connection: keep-alive\r\n") {
		t.Errorf("/v2/report 缺少 Connection: keep-alive，wire:\n%s", wire)
	}
	wire = captureWire(t, "/v3/config")
	if !strings.Contains(wire, "Connection: close\r\n") {
		t.Errorf("/v3/config 缺少 Connection: close，wire:\n%s", wire)
	}
	// 没抓到的路径不臆造：billing 保持 Go 原生（无该行）。
	wire = captureWire(t, "/v2/billing/meter")
	if strings.Contains(wire, "Connection: keep-alive") {
		t.Errorf("billing 不在真机 Connection 表里，不应臆造，wire:\n%s", wire)
	}
}
