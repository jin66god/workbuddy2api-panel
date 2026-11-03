// login.go 面板内嵌的 WorkBuddy CN OAuth 设备授权流程（cmd/login 的进程内移植）。
//
//	POST /panel/api/login/start → 拿 state+authUrl，state 存进程内（不再落 /tmp，
//	  原方案在 Windows 上不可用），返回授权 URL；
//	GET  /panel/api/login/poll   → 面板前端每 3s 轮询本接口；未完成返回 done=false，
//	  完成后取 uid/nickname、凭证落盘 auths/workbuddy-<uid>.json、热加载进池
//	  （pool.Add + Revive），并顺带签到 + 余额刷新 —— 免重启加载新账号。
//
// 无 PKCE（workbuddy 设备流由服务端签发 state），请求头与上游端点与 cmd/login 保持一致。
package panel

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/loginsite"
)

const (
	upstreamBaseCN     = "https://copilot.tencent.com"
	upstreamBaseGlobal = loginsite.BaseWorkBuddy
	// 登录链路 UA / version 与站点表统一来自 internal/loginsite（面板与 cmd/login
	// 共用，避免「面板能登、CLI 不能登」的分叉）。chat 侧 UA 另按账号身份分档。
	clientVersion       = loginsite.ClientVersion
	clientUA            = loginsite.ClientUA
	originRefererCN     = "https://www.workbuddy.cn"
	originRefererGlobal = loginsite.BaseWorkBuddy
)

// loginDeviceNonce 官方 ww_device_nonce 的来源（桌面端设备指纹 SDK）。本仓无该
// SDK，取部署侧环境变量；空 = 不加该参数（不伪造设备指纹）。
func loginDeviceNonce() string {
	return strings.TrimSpace(os.Getenv("CODEBUDDY_LOGIN_DEVICE_NONCE"))
}

// decorateAuthURL 补官方 version 参数（保留旧签名供测试/调用；站点相关口径请直接
// 用 loginsite.DecorateAuthURL）。
func decorateAuthURL(raw string) string {
	return loginsite.DecorateAuthURL(raw, loginsite.SiteCN, "")
}

// loginEndpointsFor 按站点返回设备授权三端点 + Origin（官方域表唯一来源）。
func loginEndpointsFor(s loginsite.Site) (state, token, account, origin string) {
	return s.Endpoints()
}

// loginEndpoints 兼容旧签名：按 realm 选站点（global → 旧桌面端 workbuddy.ai）。
func loginEndpoints(realm string) (state, token, account, origin string) {
	return loginEndpointsFor(loginsite.ForRealm(realm))
}

// loginHTTP 设备授权专用 client：无 cookie（每请求携带 state，无会话态）。
//
// ⚠️ 不能直接用默认 transport（与 internal/upstream 的 newTransport 同源处置）：
// 默认 transport 会经 ALPN 协商出 HTTP/2，而容器 DNS（127.0.0.11）偶发
// "server misbehaving" 并留下半死连接——半死 h2 流被复用时一直等响应头，直到
// 客户端**总超时**才报错（auth state: context deadline exceeded）。用户可见表现：
// 国内版一直拿不到授权链接；登录轮询走的 auth/token 是同一个 client，同样会卡，
// 浏览器侧明明已登录成功、面板却检测不到。
//
// 两层防护：
//   - TLS 层：空 TLSNextProto 真正禁 h2（必须 make 而非 nil；只设
//     ForceAttemptHTTP2=false 无效，ALPN 仍会协商出 h2）；
//   - 连接层：建连 8s / TLS 握手 8s / 响应头 15s，让单次尝试在 ~15s 内失败
//     而不是拖满 30s 总超时，配合 doJSON 的「一个响应都没拿到」重试。
var loginHTTP = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 15 * time.Second,
		}).DialContext,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
	},
}

// loginAttempts 设备授权链路的单次逻辑请求最多尝试几次：只对「一个响应都没拿到」
// 的传输层失败重试（已拿到响应的业务错误立即返回，避免拖慢 3s 一轮的轮询）。
const loginAttempts = 2

func commonHeaders(req *http.Request, origin string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", clientUA)
}

// validUID 校验上游返回的 uid 是否可安全用于拼文件名。
// 只放行字母、数字、下划线、连字符（腾讯侧 uid 实测为 UUID 形态），
// 长度上限 64 兜底异常超长串；拒绝 . / \ 等路径字符与空串。
func validUID(uid string) bool {
	if uid == "" || len(uid) > 64 {
		return false
	}
	for _, c := range uid {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// apiEnvelope 与 upstream 同形：{code,msg,data}，code!=0 视为业务错误。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// loginEnvelopeError 上游业务码非 0 的结构化错误：调用方按官方码表区分
// 「继续轮询」（11217/12151）与「终态失败」（12005/11212/11216），不必解析字符串。
type loginEnvelopeError struct {
	Code   int
	Msg    string
	Status int
}

func (e *loginEnvelopeError) Error() string {
	return fmt.Sprintf("code=%d msg=%s", e.Code, e.Msg)
}

// doJSON 发一次 JSON 请求并解信封。origin 为 Origin/Referer 基础域（随站点切）。
//
// body 先整体读入内存再重放：登录链路的 body 都很小（"{}" / refreshToken），
// 读一次换来「传输层失败可重试」；已拿到响应（status != 0）一律不重试。
func doJSON(method, fullURL, bearer string, body io.Reader, origin string) (json.RawMessage, int, error) {
	var payload []byte
	if body != nil {
		payload, _ = io.ReadAll(io.LimitReader(body, 1<<20))
	}
	var lastErr error
	for attempt := 0; attempt < loginAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		data, status, err := doJSONOnce(method, fullURL, bearer, payload, origin)
		if err == nil {
			return data, status, nil
		}
		if status != 0 {
			return nil, status, err // 上游已应答：确定结果，不重试
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

// doJSONOnce 单次尝试（不做重试）。
func doJSONOnce(method, fullURL, bearer string, payload []byte, origin string) (json.RawMessage, int, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, fullURL, rdr)
	if err != nil {
		return nil, 0, err
	}
	commonHeaders(req, origin)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := loginHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, &loginEnvelopeError{Code: env.Code, Msg: env.Msg, Status: resp.StatusCode}
	}
	return env.Data, resp.StatusCode, nil
}

// loginStart 发起设备授权：POST auth/state 拿授权 URL。
// body 可带 {"site":"cn"|"intl"|"workbuddy"}（官方域表，缺省 cn）；兼容老前端
// {"realm":"global"} → 旧桌面端 workbuddy.ai（零回归）。state 会话记站点，
// poll 用同一站点取 token/落盘。
func (p *Panel) loginStart(w http.ResponseWriter, r *http.Request) {
	site := loginsite.Default()
	if r.Body != nil {
		var reqBody struct {
			Realm string `json:"realm"`
			Site  string `json:"site"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&reqBody); err == nil {
			if strings.TrimSpace(reqBody.Site) != "" {
				site = loginsite.For(reqBody.Site)
			} else {
				site = loginsite.ForRealm(reqBody.Realm)
			}
		}
	}
	epState, _, _, origin := loginEndpointsFor(site)
	data, status, err := doJSON(http.MethodPost, epState, "", bytes.NewReader([]byte("{}")), origin)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("auth state (upstream %d): %v", status, err))
		return
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		writeErr(w, http.StatusBadGateway, "auth state: missing state or authUrl")
		return
	}
	// 对齐官方 CLI：授权链接补 version（workbuddy 平台再加设备 nonce）。
	st.AuthURL = loginsite.DecorateAuthURL(st.AuthURL, site.ID, loginDeviceNonce())
	p.loginMu.Lock()
	// 顺手回收过期会话，防"开弹窗走开"的 state 滞留。
	for s, sess := range p.logins {
		if time.Since(sess.created) > loginTTL {
			delete(p.logins, s)
		}
	}
	p.logins[st.State] = loginSession{created: time.Now(), realm: site.Realm, site: site.ID}
	p.loginMu.Unlock()
	log.Printf("panel: 发起 OAuth 添加账号 realm=%s site=%s（state=%s...）", site.Realm, site.ID, st.State[:min(8, len(st.State))])
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": st.AuthURL, "state": st.State, "realm": site.Realm, "site": site.ID})
}

// loginPoll 轮询登录态。未完成 → {done:false}；完成 → 建凭证、落盘、热加载、签到。
func (p *Panel) loginPoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeErr(w, http.StatusBadRequest, "missing state")
		return
	}
	p.loginMu.Lock()
	sess, known := p.logins[state]
	p.loginMu.Unlock()
	if !known {
		writeErr(w, http.StatusNotFound, "unknown or expired state（请重新发起添加账号）")
		return
	}
	site := loginsite.For(sess.site)
	if strings.TrimSpace(sess.site) == "" {
		site = loginsite.ForRealm(sess.realm) // 旧会话（升级前发起）按 realm 回落
	}
	_, epToken, epAcct, origin := loginEndpointsFor(site)

	// auth/token 是权威登录状态端点。官方码表语义：
	//   11217（RetryFetchToken）→ 还没登完，继续轮询；
	//   12005/11212/11216（席位满/许可证过期/试用到期）→ 终态，立刻把上游 msg
	//   抛给前端并停轮询（官方 SignLicenseError 行为），不再空转到 TTL。
	tokRaw, _, err := doJSON(http.MethodGet, epToken+state, "", nil, origin)
	if err != nil {
		var envErr *loginEnvelopeError
		if errors.As(err, &envErr) && loginsite.CodeFatal(envErr.Code) {
			p.loginMu.Lock()
			delete(p.logins, state)
			p.loginMu.Unlock()
			log.Printf("panel: 登录被上游拒绝 state=%s... code=%d msg=%s", state[:min(8, len(state))], envErr.Code, envErr.Msg)
			writeJSON(w, http.StatusOK, map[string]any{
				"done":    false,
				"fatal":   true,
				"code":    envErr.Code,
				"message": envErr.Msg,
			})
			return
		}
		// pending / 未完成：面板前端继续轮询。
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": err.Error()})
		return
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": "waiting for login"})
		return
	}

	// 完成：取 uid/nickname（失败不阻塞，仅缺展示名）。
	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	if acctRaw, _, err := doJSON(http.MethodGet, epAcct+state, tok.AccessToken, nil, origin); err == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}
	if acct.UID == "" {
		writeErr(w, http.StatusBadGateway, "login done but no uid（token 已发但账号信息获取失败，请重试）")
		return
	}
	// UID 来自上游响应，未经校验就用于拼文件名会被路径穿越利用
	// （filepath.Join("./auths", "workbuddy-../../evil.json") → auths/evil.json）。
	// UID 是腾讯侧账号标识，实测为 UUID（十六进制与连字符），故只放行 [A-Za-z0-9_-]。
	if !validUID(acct.UID) {
		writeErr(w, http.StatusBadGateway, "上游返回的 uid 含非法字符，拒绝落盘（防路径穿越）")
		return
	}

	// 凭证落盘（嵌套形，与 auths/ 目录既有格式一致）→ 热加载进池。
	if err := os.MkdirAll(p.cfg.AuthDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir auth dir: "+err.Error())
		return
	}
	a := &auth.Auth{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix(),
		Domain:       tok.Domain,
		UID:          acct.UID,
		EnterpriseID: acct.EnterpriseID,
		Nickname:     acct.Nickname,
		FilePath:     filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", acct.UID)),
	}
	// global 登录：落盘 auth.realm=global（Realm() 按此判域；不写则依赖 domain 后缀回落）。
	if sess.realm == "global" {
		if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
			writeErr(w, http.StatusInternalServerError, "set realm: "+err.Error())
			return
		}
	} else {
		// CN 也显式补 realm 键（幂等），让 auth 文件形态统一（与 LoadDir 存量迁移对齐）。
		_, _ = a.BackfillRealm()
	}
	if err := a.SaveAtomic(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save auth: "+err.Error())
		return
	}
	p.cfg.Pool.Add(a)
	p.cfg.Pool.Revive(acct.UID) // 全新登录 = 人工恢复口径：清掉旧号遗留的禁用/冷却/熔断

	// 顺带签到 + 余额刷新（幂等；失败不影响登录结果，只体现在返回字段里）。
	// realm 分支：CN 走 DailyCheckin；global 无 CN 签到体系，改为注册激活 + trial 领取
	// （D4 门控同 scheduler：CN 任务端点对 global 不发起任何调用）。
	checkinMsg := ""
	remain := int64(-1)
	total := int64(0)
	if sess.realm == "global" {
		// 注册激活（幂等）：region required 时自动补地区（白名单首个，HK）后重新激活。
		// 失败不阻断登录结果（auth 已落盘），只在返回字段里体现。
		if activated, err := p.cfg.Upstream.GlobalCompleteRegistration(a); err != nil {
			checkinMsg = "注册激活失败: " + err.Error()
			log.Printf("panel: global 注册激活 uid=%s: %v", acct.UID, err)
		} else if activated {
			log.Printf("panel: global 注册激活 uid=%s 完成", acct.UID)
		}
		// trial 加油包（幂等 14051 = 已领过，非错误）。
		if claimed, err := p.cfg.Upstream.ClaimTrial(a); err != nil {
			checkinMsg = joinMsg(checkinMsg, "trial 领取失败: "+err.Error())
			log.Printf("panel: global trial uid=%s: %v", acct.UID, err)
		} else if claimed {
			log.Printf("panel: global trial uid=%s 已领", acct.UID)
		}
	} else if a.SupportsPersonalEndpoints() {
		// 无个人成长体系的账号跳过签到：企业版上游 400 code 10001「企业账号不支持
		// 该操作」，静态 API Key（ck_/pt_）没有个人账号身份——发了只会把注定失败的
		// 错误写进登录返回。判据与 import / 签到端点 / 成长任务同一把闸
		// （SupportsPersonalEndpoints），不再单看 IsEnterprise 而漏掉 API Key。
		// 下方 UserResource 余额查询照常（企业额度走 get-enterprise-user-usage 口径）。
		if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
			checkinMsg = err.Error()
		}
	}
	if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
		remain, total = rm, tt
		p.cfg.Pool.ReenableIfCredits(acct.UID, rm, tt)
	}

	p.loginMu.Lock()
	delete(p.logins, state)
	p.loginMu.Unlock()
	log.Printf("panel: 新账号已热加载 uid=%s nickname=%q realm=%s（免重启生效）", acct.UID, acct.Nickname, sess.realm)
	// 企业切换会话提示（**不阻断**）：该声明只记录本次授权走了企业切换流程，
	// 不代表账号是企业版；成长任务实测照常执行（issue #4 / misakano7545 #240）。
	// 仅在事后进度确实不跳动时，才建议退出企业空间后重新授权。
	if a.IsEnterpriseSession() && !a.IsEnterprise() {
		log.Printf("panel: INFO uid=%s 的凭证由企业切换流程签发（token_source=enterprise_switch）；不影响选号/保活/成长任务，若成长进度异常可退出企业空间后重新授权", acct.UID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"done":            true,
		"uid":             acct.UID,
		"nickname":        acct.Nickname,
		"realm":           sess.realm,
		"site":            site.ID,
		"credits":         remain,
		"credits_total":   total,
		"checkin_message": checkinMsg,
	})
}

// joinMsg 拼接 login 完成后的提示消息（多段用「；」连接，空段跳过）。
func joinMsg(parts ...string) string {
	out := ""
	for _, s := range parts {
		if s == "" {
			continue
		}
		if out != "" {
			out += "；"
		}
		out += s
	}
	return out
}

// loginRegions 返回 global 注册可选地区（panel 前端选地区弹窗用；CN 不调用）。
// 未持账号时返回白名单静态兜底（前端只读展示，不依赖上游）。
func (p *Panel) loginRegions(w http.ResponseWriter, r *http.Request) {
	// 静态白名单（对齐国际版 web 展示集）：面板前端只读展示，无需账号态。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"regions": []map[string]string{
			{"code": "HK", "name": "Hong Kong"},
			{"code": "MO", "name": "Macao"},
			{"code": "SG", "name": "Singapore"},
			{"code": "TH", "name": "Thailand"},
			{"code": "PH", "name": "Philippines"},
			{"code": "MY", "name": "Malaysia"},
			{"code": "ID", "name": "Indonesia"},
		},
	})
}
