package upstream

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// 官方 WorkBuddy 业务 API 的 ClientHello 指纹（2026-11-01 真机抓包，CN 与国际版同一套）。
//
//	JA3(CN/GL) 71dc8c533dd919ae9f4963224a4ba8fd
//	JA3 原文   771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-13-51-45-43,29-23-24,0
//	JA4(CN/GL) t13d0a1200_5d04281c6031_b76c5ee2507d
//
// 关键前提：官方 chat / report / billing / refresh 走的是 **Node 主进程**，不是渲染器
// Chromium——它的 ClientHello **不带 ALPN、不带 padding**，只有 10 个扩展。420 次握手
// 逐字节一致。渲染器另有一套 Chromium 形态（16 cipher / 18 扩展 / ALPN=h2），本仓出站
// 不发渲染器请求，不复刻。
//
// 对照基线：Go 标准库默认是 t13d131100_f57a46bbacb6_f50d94e863eb（13 cipher / 11 扩展 /
// 无 ALPN），与官方三项全不同，属于可被 JA4 一次筛出的特征。
//
// 关闭方式：WB2_TLS_FINGERPRINT=0（回退到标准库 http.Transport）。
// 注意：走 HTTPS_PROXY 时 http.Transport 用 DialContext 建隧道、TLS 在隧道内由标准库完成，
// 此时 uTLS 不生效，指纹仍是 Go 默认。
func tlsFingerprintEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("WB2_TLS_FINGERPRINT")))
	return !(v == "0" || v == "false" || v == "off" || v == "no")
}

// 顺序严格照抄抓包（JA3 ciphers 段），不要重排：cipher 顺序参与 JA3/JA4 计算。
var officialCipherSuites = []uint16{
	0x1301, // TLS_AES_128_GCM_SHA256
	0x1302, // TLS_AES_256_GCM_SHA384
	0x1303, // TLS_CHACHA20_POLY1305_SHA256
	0xC02F, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
	0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
	0xC030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
	0xC02C, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
	0xC027, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256
	0xCCA9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
	0xCCA8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256
	0xC009, // TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA
	0xC013, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
	0xC00A, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA
	0xC014, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA
	0x009C, // TLS_RSA_WITH_AES_128_GCM_SHA256
	0x009D, // TLS_RSA_WITH_AES_256_GCM_SHA384
	0x002F, // TLS_RSA_WITH_AES_128_CBC_SHA
	0x0035, // TLS_RSA_WITH_AES_256_CBC_SHA
}

// 官方 ClientHello 的扩展顺序：SNI / EMS / renegotiation_info / groups / point_formats /
// session_ticket / sig_algs / key_share / psk_modes / supported_versions（10 项）。
// 顺序错 JA3 的扩展段就对不上（JA3 用扩展出现序，JA4 用排序后）。
// **不发 ALPN、不发 padding**：官方 Node 主进程的 ClientHello 没有这两项，补上会多出
// 两个扩展、JA4 直接跑偏；这也与「禁 h2」的既有策略天然一致。
func officialClientHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CipherSuites:       officialCipherSuites,
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},                      // 0
			&utls.UtlsExtendedMasterSecretExtension{}, // 23
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},                       // 65281
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}}, // 10
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},                                          // 11
			&utls.SessionTicketExtension{}, // 35
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256, // 0x0403
				utls.PSSWithSHA256,          // 0x0804
				utls.PKCS1WithSHA256,        // 0x0401
				utls.ECDSAWithP384AndSHA384, // 0x0503
				utls.PSSWithSHA384,          // 0x0805
				utls.PKCS1WithSHA384,        // 0x0501
				utls.PSSWithSHA512,          // 0x0806
				utls.PKCS1WithSHA512,        // 0x0601
				utls.PKCS1WithSHA1,          // 0x0201
			}},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519}}},                  // 51
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},                        // 45
			&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}}, // 43
		},
	}
}

// newUTLSDialer 返回 DialTLSContext 实现：对直连 HTTPS 使用官方 ClientHello 指纹。
// 握手在此完成，http.Transport 不会二次握手；连接池/超时仍由 Transport 管理。
func newUTLSDialer(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return newUTLSDialerWithSNI(dialer, "")
}

// newUTLSDialerWithSNI 同 newUTLSDialer，但允许指定 SNI（仅测试用：裸 TCP 监听
// 在回环地址上时，从 addr 取 host 会得到 IP，uTLS 按 RFC 不发 SNI 扩展，指纹
// 必然少一项）。生产路径传空串，行为与原来完全一致。
func newUTLSDialerWithSNI(dialer *net.Dialer, sniOverride string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			raw.Close()
			return nil, err
		}
		if sniOverride != "" {
			host = sniOverride
		}
		// 不发 ALPN：官方 Node 主进程的 ClientHello 不带该扩展（JA4 第一段因此是 00），
		// 且 NextProtos 一旦有值会重新引入 ALPN。禁 h2 靠 Transport 的空 TLSNextProto。
		conn := utls.UClient(raw, &utls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}, utls.HelloCustom)
		if err := conn.ApplyPreset(officialClientHelloSpec()); err != nil {
			raw.Close()
			return nil, err
		}
		if err := conn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return conn, nil
	}
}
