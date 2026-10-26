// Package loginsite 官方登录站点表与登录协议常量（对齐 CodeBuddy CLI 的域表与
// auth 模块码表）。
//
// 为什么单独一个包：设备授权链路有两个调用方——面板内嵌流程（internal/panel/login.go）
// 与命令行工具（cmd/login）。域表、UA 版本、登录码语义一旦在两处各写一份，就会出现
// 「面板能登、CLI 工具不能登」这种最难查的分叉。所有官方口径收敛在这里，两处只做
// 流程编排。
package loginsite

import (
	"net/url"
	"strings"
)

// ClientVersion 官方 CodeBuddy CLI 版本（UA 与授权链接 version 参数同源）。
// 对齐 2026-10-26 的 @tencent-ai/codebuddy-code@2.176.0。只作用于登录链路；
// chat 侧 UA 另按账号身份分档（见 upstream/headers.go），两者不同源、不要合并。
const ClientVersion = "2.176.0"

// ClientUA 设备授权链路的 User-Agent（与官方 CLI 同形）。
const ClientUA = "CLI/" + ClientVersion + " CodeBuddy/" + ClientVersion

// 站点标识（对外 API 取值）。
const (
	SiteCN        = "cn"        // 国内站：API copilot.tencent.com，Origin www.codebuddy.cn
	SiteIntl      = "intl"      // 官方国际站：www.codebuddy.ai
	SiteWorkBuddy = "workbuddy" // 旧桌面端国际版：www.workbuddy.ai
)

// Realm 取值（池内分域标识，与 auth.Realm() 输出一致）。
const (
	RealmCN     = "cn"
	RealmGlobal = "global"
)

// Site 一个登录站点的全部端点要素。
type Site struct {
	ID       string
	Realm    string // 落盘 realm：cn / global
	API      string // 上游 API base（三端点同域）
	Origin   string // Origin/Referer
	Platform string // 授权页 platform 口径；workbuddy 桌面端为 workbuddy
}

var (
	siteCN = Site{
		ID: SiteCN, Realm: RealmCN,
		API: "https://copilot.tencent.com", Origin: "https://www.codebuddy.cn",
		Platform: "CLI",
	}
	siteIntl = Site{
		ID: SiteIntl, Realm: RealmGlobal,
		API: "https://www.codebuddy.ai", Origin: "https://www.codebuddy.ai",
		Platform: "CLI",
	}
	siteWorkBuddy = Site{
		ID: SiteWorkBuddy, Realm: RealmGlobal,
		API: "https://www.workbuddy.ai", Origin: "https://www.workbuddy.ai",
		Platform: "workbuddy",
	}
)

// For 按站点标识返回站点定义；空/未知 → 国内站（零回归）。
func For(id string) Site {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case SiteIntl:
		return siteIntl
	case SiteWorkBuddy:
		return siteWorkBuddy
	}
	return siteCN
}

// Default 缺省站点（国内站）。
func Default() Site { return siteCN }

// ForRealm 按 realm 映射站点：global → 旧桌面端 workbuddy.ai（历史行为，保证
// 只传 realm 的老调用方语义不变），cn → 国内站。
//
// 要官方国际站请显式传 SiteIntl（codebuddy.ai）。
func ForRealm(realm string) Site {
	if strings.TrimSpace(realm) == RealmGlobal {
		return siteWorkBuddy
	}
	return siteCN
}

// ValidID 报告 id 是否是已知站点标识。
func ValidID(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case SiteCN, SiteIntl, SiteWorkBuddy:
		return true
	}
	return false
}

// Endpoints 返回设备授权三端点（auth/state、token、account）与 Origin。
func (s Site) Endpoints() (state, token, account, origin string) {
	return s.API + "/v2/plugin/auth/state?platform=CLI",
		s.API + "/v2/plugin/auth/token?state=",
		s.API + "/v2/plugin/login/account?state=",
		s.Origin
}

// 官方国际域主机（按 auth 文件 domain 选用，不按 realm 猜）。
const (
	BaseOfficial  = "https://www.codebuddy.ai" // 官方国际站
	BaseWorkBuddy = "https://www.workbuddy.ai" // 旧桌面端国际版
)

// BaseForDomain 按 auth 文件的 domain 返回国际域上游 base：
//
//	*.codebuddy.ai → BaseOfficial
//	*.workbuddy.ai → BaseWorkBuddy
//	其它/空        → ""（调用方回落配置或默认值，不猜域）
//
// 国际域两套主机名并存（老账号 workbuddy.ai、官方 CLI 新登录 codebuddy.ai），
// 用 realm 决定 base 必然把一半账号打到非其签发域。
func BaseForDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	switch {
	case d == "codebuddy.ai" || strings.HasSuffix(d, ".codebuddy.ai"):
		return BaseOfficial
	case d == "workbuddy.ai" || strings.HasSuffix(d, ".workbuddy.ai"):
		return BaseWorkBuddy
	}
	return ""
}

// 官方登录业务码（CLI auth 模块枚举）。语义分两类，混为一谈会让用户在本该立刻
// 报错的场景白等满 TTL：
//
//	11217 RetryFetchToken   / 12151 RetryFetchAccount  → 还没登完，继续轮询
//	12005 LicenseSeatLimit  / 11212 LicenseExpired / 11216 TrialExpired → 终态
const (
	CodeRetryFetchToken   = 11217
	CodeRetryFetchAccount = 12151
	CodeLicenseSeatLimit  = 12005
	CodeLicenseExpired    = 11212
	CodeTrialExpired      = 11216
)

// CodePending 报告该业务码是否表示「登录进行中，应继续轮询」。
func CodePending(code int) bool {
	return code == CodeRetryFetchToken || code == CodeRetryFetchAccount
}

// CodeFatal 报告该业务码是否属于官方定义的终态许可错误（SignLicenseError）：
// 官方对这三类直接把上游 msg 抛给用户并结束登录，不再轮询。
func CodeFatal(code int) bool {
	switch code {
	case CodeLicenseSeatLimit, CodeLicenseExpired, CodeTrialExpired:
		return true
	}
	return false
}

// DecorateAuthURL 按官方 CLI 行为补齐授权链接参数（对应 appendAuthUrlParams /
// appendLoginPageHintParams）：
//
//	version=<ClientVersion>   官方对每个站点都加
//	ww_device_nonce=<nonce>   官方仅 workbuddy 平台且拿到设备 nonce 时加
//
// 本仓没有桌面端设备指纹 SDK，nonce 只能由部署侧提供；为空则不加——不伪造
// 设备指纹，错值比缺值更容易被上游风控判异常。raw 解析失败时原样返回。
func DecorateAuthURL(raw, siteID, nonce string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("version") == "" {
		q.Set("version", ClientVersion)
	}
	if For(siteID).Platform == "workbuddy" && nonce != "" && q.Get("ww_device_nonce") == "" {
		q.Set("ww_device_nonce", nonce)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
