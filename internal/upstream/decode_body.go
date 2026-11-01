package upstream

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
)

// decodeResponseBody 按 Content-Encoding 解压短 RPC 的响应体。
//
// 背景：出站显式声明 `Accept-Encoding: gzip, compress, deflate, br`（官方真机四段）
// 会**关闭 net/http 的透明解压**——不配这个函数时所有 JSON 接口读到的都是压缩字节，
// 解析全废；更糟的是 400 分支会把压缩体当文本喂给 Classify，按关键词误判账号故障。
// 两处是一个整体，不能只加头。
//
// 支持 gzip / deflate（zlib 包装与裸 flate 两种实现都兼容）/ br；identity 原样返回。
// compress（LZW）上游从不下发，遇到直接报错而不是静默返回乱码。
func decodeResponseBody(encoding string, raw []byte) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	if enc == "" || enc == "identity" {
		return raw, nil
	}
	// 多编码按声明逆序解（RFC 9110）；实际只见过单值。
	if i := strings.LastIndex(enc, ","); i >= 0 {
		inner, err := decodeResponseBody(enc[:i], raw)
		if err != nil {
			return nil, err
		}
		return decodeResponseBody(enc[i+1:], inner)
	}
	switch enc {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer zr.Close()
		return io.ReadAll(io.LimitReader(zr, 8<<20))
	case "deflate":
		// 历史上「deflate」既可能是 zlib 包装，也可能是裸 DEFLATE 流。
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			defer zr.Close()
			if out, err := io.ReadAll(io.LimitReader(zr, 8<<20)); err == nil {
				return out, nil
			}
		}
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		out, err := io.ReadAll(io.LimitReader(fr, 8<<20))
		if err != nil {
			return nil, fmt.Errorf("deflate: %w", err)
		}
		return out, nil
	case "br":
		return io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(raw)), 8<<20))
	case "compress":
		return nil, fmt.Errorf("compress(LZW) 编码不支持")
	default:
		return nil, fmt.Errorf("未知 Content-Encoding %q", encoding)
	}
}
