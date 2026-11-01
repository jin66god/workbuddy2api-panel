package upstream

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

// TestDecodeResponseBody 五种响应编码都要解出原文（含 deflate 的 zlib / 裸流两种实现）。
func TestDecodeResponseBody(t *testing.T) {
	plain := []byte(`{"code":0,"data":{"ok":true}}`)

	brBuf := &bytes.Buffer{}
	bw := brotli.NewWriter(brBuf)
	bw.Write(plain)
	bw.Close()

	gzBuf := &bytes.Buffer{}
	gw := gzip.NewWriter(gzBuf)
	gw.Write(plain)
	gw.Close()

	zlBuf := &bytes.Buffer{}
	zw := zlib.NewWriter(zlBuf)
	zw.Write(plain)
	zw.Close()

	flBuf := &bytes.Buffer{}
	fw, _ := flate.NewWriter(flBuf, flate.DefaultCompression)
	fw.Write(plain)
	fw.Close()

	cases := []struct {
		enc  string
		body []byte
	}{
		{"", plain},
		{"identity", plain},
		{"gzip", gzBuf.Bytes()},
		{"br", brBuf.Bytes()},
		{"deflate", zlBuf.Bytes()},
		{"deflate", flBuf.Bytes()},
	}
	for _, c := range cases {
		got, err := decodeResponseBody(c.enc, c.body)
		if err != nil {
			t.Errorf("decode(%q): %v", c.enc, err)
			continue
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("decode(%q) = %q want %q", c.enc, got, plain)
		}
	}
	if _, err := decodeResponseBody("compress", plain); err == nil {
		t.Error("compress(LZW) 应报不支持而不是返回乱码")
	}
}

// TestDoJSONDecodesCompressedResponses doJSON 必须显式声明四段编码并解压后再解析。
// 漏掉解码时所有 JSON 接口全废；漏掉声明则服务端不会压缩、这条测试也测不到。
func TestDoJSONDecodesCompressedResponses(t *testing.T) {
	payload := []byte(`{"code":0,"data":{"ok":true}}`)
	encoders := map[string]func([]byte) []byte{
		"gzip": func(b []byte) []byte {
			var buf bytes.Buffer
			w := gzip.NewWriter(&buf)
			w.Write(b)
			w.Close()
			return buf.Bytes()
		},
		"br": func(b []byte) []byte {
			var buf bytes.Buffer
			w := brotli.NewWriter(&buf)
			w.Write(b)
			w.Close()
			return buf.Bytes()
		},
		"deflate": func(b []byte) []byte {
			var buf bytes.Buffer
			w := zlib.NewWriter(&buf)
			w.Write(b)
			w.Close()
			return buf.Bytes()
		},
	}
	for enc, encode := range encoders {
		t.Run(enc, func(t *testing.T) {
			var gotAE string
			c := &Client{HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				gotAE = r.Header.Get("Accept-Encoding")
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Encoding": []string{enc}, "Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(encode(payload))),
				}, nil
			})}}
			req, _ := http.NewRequest("POST", "https://fake.example/v2/x", strings.NewReader(`{}`))
			data, err := c.doJSON(req)
			if err != nil {
				t.Fatalf("doJSON: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(data, &obj); err != nil || obj["ok"] != true {
				t.Fatalf("data = %s err=%v，压缩体没有被解压", data, err)
			}
			if gotAE != "gzip, compress, deflate, br" {
				t.Errorf("Accept-Encoding = %q，want 官方四段", gotAE)
			}
		})
	}
}

// TestDoJSONClassifiesDecompressedError 400 + br 时 Classify 必须看到解压后的文本，
// 否则压缩乱码会让账号故障/限流判据全部失效（误罚号的经典现场）。
func TestDoJSONClassifiesDecompressedError(t *testing.T) {
	body := []byte(`{"code":14018,"msg":"credits exhausted"}`)
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	bw.Write(body)
	bw.Close()

	c := &Client{HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429,
			Header:     http.Header{"Content-Encoding": []string{"br"}},
			Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		}, nil
	})}}
	req, _ := http.NewRequest("POST", "https://fake.example/v2/x", strings.NewReader(`{}`))
	_, err := c.doJSON(req)
	ue, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %v want *upstream.Error", err)
	}
	if ue.Kind != ErrHardCredit {
		t.Fatalf("Kind = %v want ErrHardCredit（14018 在压缩体里没被识别 = 解压顺序错了）", ue.Kind)
	}
}
