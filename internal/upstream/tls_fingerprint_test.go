// tls_fingerprint_test.go 出站 ClientHello 指纹的回归测试。
//
// 判据是真机抓包得到的常量（2026-11-01，420 次握手逐字节一致），不是手推值：
// 起一个裸 TCP 监听抓 uTLS 实际发出的原始字节，自己算 JA3/JA4 再比对。
// 这条链一封，说明「出站流量在 TLS 层看起来不再是官方客户端」。
package upstream

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// 真机实测常量（CN 与 GL 同值）。
const (
	officialJA3 = "771," +
		"4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53," +
		"0-23-65281-10-11-35-13-51-45-43," +
		"29-23-24," +
		"0"
	officialJA4 = "t13d0a1200_5d04281c6031_b76c5ee2507d"
)

// helloFields ClientHello 里算 JA3/JA4 需要的字段。
type helloFields struct {
	version           uint16
	ciphers           []uint16
	exts              []uint16
	groups            []uint16
	ecpf              []uint8
	sigalgs           []uint16
	supportedVersions []uint16
	alpn              []string
	hasSNI            bool
}

func parseClientHello(t *testing.T, raw []byte) *helloFields {
	t.Helper()
	f := &helloFields{}
	p := 5
	if len(raw) < p+4 || raw[p] != 1 {
		t.Fatalf("不是 ClientHello（首字节 %d）", raw[0])
	}
	p += 4
	f.version = binary.BigEndian.Uint16(raw[p : p+2])
	p += 2 + 32
	sidLen := int(raw[p])
	p += 1 + sidLen
	csLen := int(binary.BigEndian.Uint16(raw[p : p+2]))
	p += 2
	for i := 0; i+1 < csLen; i += 2 {
		f.ciphers = append(f.ciphers, binary.BigEndian.Uint16(raw[p+i:p+i+2]))
	}
	p += csLen
	cmLen := int(raw[p])
	p += 1 + cmLen

	if p+2 > len(raw) {
		t.Fatal("ClientHello 截断（无扩展段）")
	}
	total := int(binary.BigEndian.Uint16(raw[p : p+2]))
	p += 2
	end := p + total
	if end > len(raw) {
		end = len(raw)
	}
	for p+4 <= end {
		et := binary.BigEndian.Uint16(raw[p : p+2])
		el := int(binary.BigEndian.Uint16(raw[p+2 : p+4]))
		data := raw[p+4:]
		if len(data) > el {
			data = data[:el]
		}
		f.exts = append(f.exts, et)
		switch et {
		case 0:
			f.hasSNI = true
		case 10:
			for i := 2; i+1 < len(data); i += 2 {
				f.groups = append(f.groups, binary.BigEndian.Uint16(data[i:i+2]))
			}
		case 11:
			for i := 1; i < len(data); i++ {
				f.ecpf = append(f.ecpf, data[i])
			}
		case 13:
			if len(data) >= 2 {
				n := int(binary.BigEndian.Uint16(data[0:2]))
				for i := 2; i+1 < len(data) && i < 2+n; i += 2 {
					f.sigalgs = append(f.sigalgs, binary.BigEndian.Uint16(data[i:i+2]))
				}
			}
		case 43:
			if len(data) >= 1 {
				n := int(data[0])
				for i := 1; i+1 < len(data) && i < 1+n; i += 2 {
					f.supportedVersions = append(f.supportedVersions, binary.BigEndian.Uint16(data[i:i+2]))
				}
			}
		case 16:
			if len(data) >= 2 {
				q := 2
				for q < len(data) {
					l := int(data[q])
					if q+1+l > len(data) {
						break
					}
					f.alpn = append(f.alpn, string(data[q+1:q+1+l]))
					q += 1 + l
				}
			}
		}
		p += 4 + el
	}
	return f
}

func ja4Version(v uint16) string { return fmt.Sprintf("1%d", int(v&0xff)-1) }

func (f *helloFields) maxSupportedVersion() uint16 {
	var out uint16
	for _, v := range f.supportedVersions {
		if v > out {
			out = v
		}
	}
	return out
}

func (f *helloFields) ja3() string {
	join := func(list []string) string { return strings.Join(list, "-") }
	var cs, es, gs, ef []string
	for _, c := range f.ciphers {
		cs = append(cs, fmt.Sprintf("%d", c))
	}
	for _, e := range f.exts {
		es = append(es, fmt.Sprintf("%d", e))
	}
	for _, g := range f.groups {
		gs = append(gs, fmt.Sprintf("%d", g))
	}
	for _, x := range f.ecpf {
		ef = append(ef, fmt.Sprintf("%d", x))
	}
	return fmt.Sprintf("%d,%s,%s,%s,%s", f.version, join(cs), join(es), join(gs), join(ef))
}

// ja4 按 FoxIO 规范：a_b_c。
// a = t{ver}{d|i}{ext 数 hex2}{cipher 数 hex2}{ALPN 首 2 字符或 00}
// b = sha256(cipher 升序, 4 位 hex, 逗号分隔)[:12]
// c = sha256(扩展升序[排除 SNI/ALPN] + "_" + sigalgs 升序)[:12]
func (f *helloFields) ja4() string {
	tlsMax := f.version
	if v := f.maxSupportedVersion(); v > tlsMax {
		tlsMax = v
	}
	a := fmt.Sprintf("t%s%s%02x%02x", ja4Version(tlsMax),
		map[bool]string{true: "d", false: "i"}[f.hasSNI], len(f.exts), len(f.ciphers))
	if len(f.alpn) > 0 && len(f.alpn[0]) >= 2 {
		a += f.alpn[0][:2]
	} else {
		a += "00"
	}
	var cs []int
	for _, c := range f.ciphers {
		cs = append(cs, int(c))
	}
	sort.Ints(cs)
	var bIn []string
	for _, c := range cs {
		bIn = append(bIn, fmt.Sprintf("%04x", c))
	}
	b := sha256.Sum256([]byte(strings.Join(bIn, ",")))

	var ex, sa []int
	for _, e := range f.exts {
		if e != 0 && e != 16 {
			ex = append(ex, int(e))
		}
	}
	for _, s := range f.sigalgs {
		sa = append(sa, int(s))
	}
	sort.Ints(ex)
	sort.Ints(sa)
	var cIn, saIn []string
	for _, e := range ex {
		cIn = append(cIn, fmt.Sprintf("%04x", e))
	}
	for _, s := range sa {
		saIn = append(saIn, fmt.Sprintf("%04x", s))
	}
	c := sha256.Sum256([]byte(strings.Join(cIn, ",") + "_" + strings.Join(saIn, ",")))
	return fmt.Sprintf("%s_%s_%s", a, hex.EncodeToString(b[:])[:12], hex.EncodeToString(c[:])[:12])
}

// captureClientHello 起一个裸 TCP 监听，让 dialer 连上去，收回它发出的
// ClientHello 原始字节。不完成握手——只看第一包。
func captureClientHello(t *testing.T, dial func(addr string) (net.Conn, error)) []byte {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	got := make(chan []byte, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			got <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 8192)
		n, _ := c.Read(buf)
		got <- buf[:n]
	}()

	go func() {
		if conn, derr := dial(ln.Addr().String()); derr == nil {
			conn.Close()
		}
	}()

	select {
	case b := <-got:
		if len(b) == 0 {
			t.Fatal("没抓到 ClientHello")
		}
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("等 ClientHello 超时")
		return nil
	}
}

// dialForged 走**生产 dialer 构造函数**（newUTLSDialerWithSNI + officialClientHelloSpec），
// 只把 SNI 固定成真实域名：listener 在 127.0.0.1，从 addr 取 host 会得到 IP，
// uTLS 按 RFC 不发 SNI 扩展 → 扩展数少一项、JA3/JA4 必然对不上。
func dialForged(t *testing.T) func(addr string) (net.Conn, error) {
	t.Helper()
	dial := newUTLSDialerWithSNI(newDialer(), "www.workbuddy.cn")
	return func(addr string) (net.Conn, error) {
		// 握手必然失败（对端只读不回），但 ClientHello 已经发出。
		conn, _ := dial(context.Background(), "tcp", addr)
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

// TestOfficialClientHelloFingerprint 出站 ClientHello 必须与官方真机逐项一致。
func TestOfficialClientHelloFingerprint(t *testing.T) {
	raw := captureClientHello(t, dialForged(t))
	f := parseClientHello(t, raw)

	if got := f.ja3(); got != officialJA3 {
		t.Errorf("JA3 与官方不一致\n  want: %s\n  got : %s", officialJA3, got)
	}
	if got := f.ja4(); got != officialJA4 {
		t.Errorf("JA4 与官方不一致\n  want: %s\n  got : %s", officialJA4, got)
	}
	// 逐项拆开断言，失败时能直接看出是哪一项跑偏。
	if len(f.ciphers) != 18 {
		t.Errorf("cipher 数 = %d want 18", len(f.ciphers))
	}
	if len(f.exts) != 10 {
		t.Errorf("扩展数 = %d want 10（含 ALPN/padding 会让 JA4 跑偏）", len(f.exts))
	}
	if len(f.sigalgs) != 9 {
		t.Errorf("sigalgs = %d want 9（官方不含 ed25519/ed448/pss_pss）: %v", len(f.sigalgs), f.sigalgs)
	}
	if len(f.alpn) != 0 {
		t.Errorf("ALPN 必须为空（官方 Node 栈不发），got %v", f.alpn)
	}
	if got := fmt.Sprint(f.groups); got != "[29 23 24]" {
		t.Errorf("groups = %v want [29 23 24]", f.groups)
	}
}

// TestTLSFingerprintEnvSwitch 环境开关只关不猜：显式 false 才回落标准库。
func TestTLSFingerprintEnvSwitch(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"1":     true,
		"node":  true,
		"0":     false,
		"false": false,
		"off":   false,
		"no":    false,
		"OFF":   false,
	}
	old, had := os.LookupEnv("WB2_TLS_FINGERPRINT")
	defer func() {
		if had {
			os.Setenv("WB2_TLS_FINGERPRINT", old)
		} else {
			os.Unsetenv("WB2_TLS_FINGERPRINT")
		}
	}()
	for in, want := range cases {
		os.Setenv("WB2_TLS_FINGERPRINT", in)
		if got := tlsFingerprintEnabled(); got != want {
			t.Errorf("tlsFingerprintEnabled(%q) = %v want %v", in, got, want)
		}
	}
}

// TestTransportUsesFingerprintDialer newTransport 必须真的挂上 DialTLSContext（直连生效）。
func TestTransportUsesFingerprintDialer(t *testing.T) {
	os.Unsetenv("WB2_TLS_FINGERPRINT")
	tr := newTransport()
	defer tr.CloseIdleConnections()
	if tr.DialTLSContext == nil {
		t.Fatal("newTransport().DialTLSContext 为 nil：TLS 指纹未生效（总是回退标准库）")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 必须为空（禁 h2），实际 %d 项", len(tr.TLSNextProto))
	}
}

var _ = tls.VersionTLS13
