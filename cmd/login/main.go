// login.go — WorkBuddy OAuth 登录（设备授权流程，CN realm；--realm=global 供国际版）。
//
// 两个子命令，由 login.sh 顺序驱动：
//
//	login [--realm=cn|global] url   → POST /v2/plugin/auth/state?platform=CLI 拿 state+authUrl，
//	                                  state 落 /tmp/wb2api-login-state.json，stdout 打印授权 URL
//	login [--realm=cn|global] poll  → 读 state，GET /v2/plugin/auth/token?state= 一次，
//	                                  成功再 GET /v2/plugin/login/account?state= 拿 uid/nickname，
//	                                  stdout 打印完整 token+account JSON（含 realm 键）
//
// --realm 默认 cn。按 realm 切换上游端点与 Origin/Referer：
//
//	cn     → https://copilot.tencent.com（Origin: https://www.codebuddy.cn）
//	global → https://www.workbuddy.ai（Origin: https://www.workbuddy.ai）
//
// state 落盘带 realm，poll 读回校验与命令行 --realm 一致（防混域）。
// 无 PKCE（workbuddy 设备流由服务端签发 state）。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	auth2 "github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/loginsite"
)

// 登录链路的上游 base / Origin 全部来自 internal/loginsite 的站点表（cn / intl /
// workbuddy 三站点，base 与 Origin/Referer 恒同域）；这里只留公共请求头默认值。
const (
	// clientVersion 官方 CodeBuddy CLI 版本（UA 与授权链接 version 参数同源）。
	// 只作用于登录链路；chat 侧 UA 按账号身份分档，两者不同源。
	clientVersion   = loginsite.ClientVersion
	clientUA        = loginsite.ClientUA
	originRefererCN = "https://www.codebuddy.cn"
)

// 登录 state 落盘路径（var 便于测试替换临时文件）
// Portable across OSes: the upstream hardcoded "/tmp/...", which on
// Windows resolves to <drive>:\tmp\... and aborts the OAuth flow with
// "The system cannot find the path specified". os.TempDir() is /tmp on Linux.
var stateFile = filepath.Join(os.TempDir(), "wb2api-login-state.json")

// exitFunc 供测试替换（默认 os.Exit；测试持临时替换为 panic 以进程内捕获 fatal）。
var exitFunc = os.Exit

// commonHeaders 按 origin 设置通用请求头（Origin/Referer 随站点变化）。
// 返回 func(*http.Request)，由调用方按站点选定的 origin 构造一次后复用。
func commonHeaders(origin string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("User-Agent", clientUA)
	}
}

// apiEnvelope 与 main.go:429-433 一致
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// envelopeError 上游业务码非 0 的结构化错误：调用方按官方码表区分
// 「继续轮询」（11217/12151）与「终态失败」（12005/11212/11216）。
type envelopeError struct {
	Code   int
	Msg    string
	Status int
}

func (e *envelopeError) Error() string {
	return fmt.Sprintf("code=%d msg=%s", e.Code, e.Msg)
}

// doJSON 与 oauth.go:33-66 一致：{code,msg,data} 信封，code!=0 → error
func doJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		// 缺省头：CN origin（与原 commonHeaders() 行为一致，零回归）
		commonHeaders(originRefererCN)(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream redirect %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, &envelopeError{Code: env.Code, Msg: env.Msg, Status: resp.StatusCode}
	}
	return env.Data, resp.StatusCode, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "login: "+format+"\n", args...)
	exitFunc(1)
}

type loginState struct {
	State string `json:"state"`
	Realm string `json:"realm,omitempty"` // url 落盘时写回的 realm，poll 读回校验防混域
	Site  string `json:"site,omitempty"`  // 站点标识（cn/intl/workbuddy），poll 读回校验防换站
}

// realm 取值枚举（与 internal/auth 的 Realm() 归一化输出一致）。
const (
	realmCN     = "cn"
	realmGlobal = "global"
)

// parseRealmArgs 解析开头的 --realm=cn|global（或分离式 --realm <v>）flag，缺省 cn。
// 大小写不敏感归一化；非法值/缺值报错。桌椅剩余参数（子命令）顺序不变。
func parseRealmArgs(args []string) (realm string, rest []string, err error) {
	realm = realmCN
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--realm":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--realm requires a value")
			}
			v := strings.ToLower(strings.TrimSpace(args[i+1]))
			if v != realmCN && v != realmGlobal {
				return "", nil, fmt.Errorf("invalid --realm %q (want cn|global)", args[i+1])
			}
			realm = v
			i++
		case strings.HasPrefix(a, "--realm="):
			v := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a, "--realm=")))
			if v != realmCN && v != realmGlobal {
				return "", nil, fmt.Errorf("invalid --realm %q (want cn|global)", v)
			}
			realm = v
		default:
			rest = append(rest, a)
		}
	}
	return realm, rest, nil
}

// parseSiteArgs 解析 --site=cn|intl|workbuddy（官方域表），并把剩余参数里的
// --realm 一并消化：显式 --site 优先；否则按 --realm 映射（cn → 国内站，
// global → 旧桌面端 workbuddy.ai，保持老脚本语义）。
func parseSiteArgs(args []string) (site loginsite.Site, rest []string, err error) {
	var siteID string
	var kept []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--site":
			if i+1 >= len(args) {
				return loginsite.Site{}, nil, fmt.Errorf("--site requires a value")
			}
			siteID = args[i+1]
			i++
		case strings.HasPrefix(a, "--site="):
			siteID = strings.TrimPrefix(a, "--site=")
		default:
			kept = append(kept, a)
		}
	}
	if siteID != "" {
		id := strings.ToLower(strings.TrimSpace(siteID))
		if !loginsite.ValidID(id) {
			return loginsite.Site{}, nil, fmt.Errorf("invalid --site %q (want cn|intl|workbuddy)", siteID)
		}
		// 仍消化 --realm（老脚本会同时传），但以 --site 为准。
		_, kept, err = parseRealmArgs(kept)
		if err != nil {
			return loginsite.Site{}, nil, err
		}
		return loginsite.For(id), kept, nil
	}
	realm, kept, err := parseRealmArgs(kept)
	if err != nil {
		return loginsite.Site{}, nil, err
	}
	return loginsite.ForRealm(realm), kept, nil
}

// resolveRealmInput 把交互式选域的一行输入归一化为 realm（纯函数，login.sh 交互分支
// 的核心决策，可测）。规则：
//
//	"1"/"cn"（大小写不敏感）/""（回车默认）→ cn
//	"2"/"global" → global
//	其他 → ("", false)（调用方回默认 cn）
func resolveRealmInput(input string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "1", "cn":
		return realmCN, true
	case "2", "global":
		return realmGlobal, true
	}
	return "", false
}

// promptRealm 交互式选域：向 out 打印选项提示（out 接 stderr，stdout 留给 realm 本身），
// 从 in 读一行，返回归一化 realm。非法输入警告后回落 cn；EOF（非交互/管道）回落 cn。
func promptRealm(in io.Reader, out io.Writer) string {
	fmt.Fprintln(out, "选择登录版本: 1) 国内版(cn) 2) 国际版(global) [默认 1/cn]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		// EOF/非交互 → 回落默认 cn
		return realmCN
	}
	if realm, ok := resolveRealmInput(line); ok {
		return realm
	}
	fmt.Fprintln(out, "无效选择，默认国内版 cn")
	return realmCN
}

// validateRealmMatch 校验 state 文件 realm 与命令行 --realm 一致（防混域）：
// state 无 realm（旧文件）放行；非空且不一致 → error。
func validateRealmMatch(stateRealm, cliRealm string) error {
	if stateRealm != "" && stateRealm != cliRealm {
		return fmt.Errorf("realm mismatch: state file realm=%q, command --realm=%q（url 与 poll 需同一 realm）", stateRealm, cliRealm)
	}
	return nil
}

// runURL 执行 url 子命令：向 upstreamBase 的 state 端点 POST 取授权 URL，
// state 落盘（带 realm），stdout 打印 authURL。out 接 stdout；stateFile 为落盘路径
// （可注入临时文件便于测试）。空 realm 视为缺省（调用方已归一）。
func runURL(base, origin, realm, site, statePath string, client *http.Client, out io.Writer) {
	headers := commonHeaders(origin)
	data, _, err := doJSON(client, http.MethodPost, base+"/v2/plugin/auth/state?platform=CLI", headers, bytes.NewReader([]byte("{}")))
	if err != nil {
		fatal("auth state failed: %v", err)
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		fatal("auth state: missing state or authUrl")
	}
	raw, _ := json.Marshal(loginState{State: st.State, Realm: realm, Site: site})
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		fatal("write state: %v", err)
	}
	fmt.Fprintln(out, decorateAuthURL(st.AuthURL))
}

// decorateAuthURL 按官方 CLI 行为给授权链接补 version 参数。已带则保持原值；
// 解析失败原样返回，不阻断登录。
func decorateAuthURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("version") == "" {
		q.Set("version", clientVersion)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// runPoll 执行 poll 子命令：读 state 文件（realm 校验），向 upstreamBase 的 token 端点
// GET 一次，成功再 GET login/account（带 Bearer），stdout 打印完整 token+account JSON。
// statePath 可注入临时文件便于测试。
func runPoll(base, origin, realm, site, statePath string, client *http.Client, out io.Writer) {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		fatal("read state: %v (先跑 login url)", err)
	}
	var ls loginState
	if err := json.Unmarshal(raw, &ls); err != nil {
		fatal("parse state: %v", err)
	}
	// 防混域：state 落盘 realm 与命令行 --realm 不一致则拒绝（url 与 poll 必须同域）
	if err := validateRealmMatch(ls.Realm, realm); err != nil {
		fatal("%v", err)
	}
	// 防换站：intl 与 workbuddy 同 realm（global）但主机不同，必须按 site 再校验一次。
	if ls.Site != "" && site != "" && ls.Site != site {
		fatal("site mismatch: state file site=%q, command --site=%q（url 与 poll 需同一站点）", ls.Site, site)
	}
	headers := commonHeaders(origin)
	// handlePollLogin (oauth.go:108-162)：auth/token 是权威登录状态端点。
	//   11217（RetryFetchToken）→ 还没登完，继续轮询；
	//   12005/11212/11216（席位满/许可证过期/试用到期）→ 终态，立刻报错。
	tokRaw, status, errTok := doJSON(client, http.MethodGet, base+"/v2/plugin/auth/token?state="+ls.State, headers, nil)
	if errTok != nil {
		var envErr *envelopeError
		if errors.As(errTok, &envErr) && loginsite.CodeFatal(envErr.Code) {
			fatal("登录被上游拒绝: code=%d %s", envErr.Code, envErr.Msg)
		}
		if status == 0 || status >= 500 {
			fatal("token endpoint error: %v", errTok)
		}
		// 走到这里的是「非信封 4xx」（HTML 错误页等）：state 多半已过期/失效，
		// 反复「确认浏览器登录 + 按 y」永远不会再成功——正确动作是重跑 login url。
		fatal("登录状态查询被拒（HTTP %d）：%v\nstate 可能已过期，请重新运行 login url", status, errTok)
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		fatal("token 响应异常（缺少 accessToken）：%v\n若浏览器侧已显示登录失败，请重新运行 login url", err)
	}
	// login/account 拿 uid/nickname（带 Bearer）
	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	acctHeaders := func(r *http.Request) {
		headers(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := doJSON(client, http.MethodGet, base+"/v2/plugin/login/account?state="+ls.State, acctHeaders, nil); errAcct == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}
	oraw, _ := json.Marshal(buildLoginOutput(tok, realm, acct))
	fmt.Fprintln(out, string(oraw))
	os.Remove(statePath)
}

// buildLoginOutput 组装 poll 输出的完整 JSON（login.sh 据此落盘 auth 文件）。
// realm 永不空：显式 --realm 优先（ResolveRealm 处理），否则按上游返回的 domain 推断——
// 保证登录落盘的 auth 文件恒带 realm 键。
func buildLoginOutput(tok struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	Domain       string `json:"domain"`
}, realm string, acct struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}) map[string]any {
	return map[string]any{
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
		"expires_in":    tok.ExpiresIn,
		"domain":        tok.Domain,
		"realm":         auth2.ResolveRealm(realm, tok.Domain),
		"uid":           acct.UID,
		"enterprise_id": acct.EnterpriseID,
		"nickname":      acct.Nickname,
	}
}

func main() {
	site, rest, err := parseSiteArgs(os.Args[1:])
	if err != nil {
		fatal("%v (usage: login [--site=cn|intl|workbuddy] [--realm=cn|global] <url|poll|realm>)", err)
	}
	realm := site.Realm
	if len(rest) < 1 {
		fatal("usage: login [--site=cn|intl|workbuddy] [--realm=cn|global] <url|poll>")
	}
	// 每个流程独立 cookie jar（oauth.go:22-29：多账号登录互不串会话）
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}

	base, origin := site.API, site.Origin

	switch rest[0] {
	case "url":
		runURL(base, origin, realm, site.ID, stateFile, client, os.Stdout)

	case "poll":
		runPoll(base, origin, realm, site.ID, stateFile, client, os.Stdout)

	case "realm":
		// 交互式选域（login.sh 无 --realm/--site 传参且 stdin 为 tty 时调用）。
		// 提示打到 stderr，stdout 只输出归一化 realm，供 $( ) 捕获。
		realm := promptRealm(os.Stdin, os.Stderr)
		fmt.Println(realm)

	default:
		fatal("unknown subcommand %q (want url|poll|realm)", rest[0])
	}
}
