package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// cockpitAccount 映射 cockpit tools 导出格式的单个账号。
type cockpitAccount struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	TokenType      string `json:"token_type"`
	ExpiresAt      int64  `json:"expires_at"`
	Domain         string `json:"domain"`
	DosageNotify   string `json:"dosage_notify_code"`
	PaymentType    string `json:"payment_type"`
	Status         string `json:"status"`
	UsageUpdatedAt int64  `json:"usage_updated_at"`
	LastCheckin    int64  `json:"last_checkin_time"`
	CheckinStreak  int    `json:"checkin_streak"`
	CreatedAt      int64  `json:"created_at"`
	LastUsed       int64  `json:"last_used"`

	// EnterpriseID 兼容导出工具的两种写法：cockpit 导出多为 snake_case，auth 文件与
	// 登录接口用 camelCase。任一带值都视为企业版——丢掉它会让企业号以个人号形态
	// 落盘：导入后白签一次（上游 400 code 10001）、额度误走个人口径（企业号
	// get-user-resource 返回空 Accounts）、成长任务入队后被 403。见 auth.IsEnterprise。
	EnterpriseID      string `json:"enterprise_id"`
	EnterpriseIDCamel string `json:"enterpriseId"`
}

// importCockpit 接收 cockpit tools 导出的 JSON 文件，批量导入账号到池中。
//
//	POST /panel/api/import/cockpit
//	Content-Type: multipart/form-data
//	Body: file=<json>
//
// 返回 {ok, total, imported, skipped, errors}。
func (p *Panel) importCockpit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file: "+err.Error())
		return
	}

	// 形态归一：批量导出是「顶层数组 + snake_case」，单账号详情导出是嵌套驼峰
	// {account,auth}，还有信封与 {"<uid>":{…}} 字典形态。用户不会区分这些，统一在
	// 解码处归一，而不是让人手工改写。
	accounts, err := decodeCockpitAccounts(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(accounts) == 0 {
		writeErr(w, http.StatusBadRequest, "empty accounts array")
		return
	}

	// 凭证目录必须在第一次落盘前就位。login.go / apikey.go 各自在写盘前建了目录，
	// 唯独本文件漏了：全新部署（auths/ 还没生成、也没登录过任何账号）时，写盘用的
	// 临时文件创建直接 ENOENT，用户看到的是「导入失败」，而服务本身完全正常
	// ——LoadDir 用 filepath.Glob，目录不存在时返回空列表且不报错，所以启动日志
	// 一切正常（loaded 0 account(s)），问题只在导入时暴露。
	// 放在循环外建一次：目录是进程级前置条件，逐个账号重复 MkdirAll 既无意义，
	// 也会在部分账号失败时给出误导性错误。
	if err := os.MkdirAll(p.cfg.AuthDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir auth dir: "+err.Error())
		return
	}

	var total, imported, skipped int
	var errs []string

	for _, acc := range accounts {
		uid := strings.TrimSpace(acc.UID)
		at := strings.TrimSpace(acc.AccessToken)
		rt := strings.TrimSpace(acc.RefreshToken)
		// 必填判据只要求 uid + access_token 非空：refresh_token 可以缺（issue #165 的
		// access-token-only 导出——例如静态 API Key 凭证，本就没有 refresh token）。
		// 缺 refresh_token 的账号仍可接流量与刷新额度，只是不参与保活刷新。
		if uid == "" || at == "" {
			skipped++
			errs = append(errs, fmt.Sprintf("missing required fields (id=%s)", acc.ID))
			continue
		}
		if !validImportUID(uid) {
			skipped++
			errs = append(errs, fmt.Sprintf("invalid uid (id=%s)", acc.ID))
			continue
		}

		// 按 domain 推断 realm：workbuddy.ai 家族 → global，否则 cn。
		realm := auth.ResolveRealm("", acc.Domain)

		// expires_at 先统一成毫秒再转秒：批量导出是毫秒，单账号详情导出是秒，直接
		// 除以 1000 会把秒级值算成 1970 年（账号一进池就是「已过期」）。
		expiresAt := normalizeExpiresAtMillis(acc.ExpiresAt) / 1000
		if expiresAt <= 0 {
			expiresAt = time.Now().Add(365 * 24 * time.Hour).Unix()
		}

		nickname := acc.Nickname
		if strings.TrimSpace(nickname) == "" {
			nickname = acc.Email
		}

		enterpriseID := strings.TrimSpace(acc.EnterpriseID)
		if enterpriseID == "" {
			enterpriseID = strings.TrimSpace(acc.EnterpriseIDCamel)
		}

		a := &auth.Auth{
			AccessToken:  at,
			RefreshToken: rt,
			ExpiresAt:    expiresAt,
			Domain:       acc.Domain,
			UID:          uid,
			EnterpriseID: enterpriseID,
			Nickname:     nickname,
			FilePath:     filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", uid)),
		}

		if realm == "global" {
			if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
				skipped++
				errs = append(errs, fmt.Sprintf("uid=%s: set realm failed: %v", uid, err))
				continue
			}
		} else {
			_, _ = a.BackfillRealm()
		}

		if err := a.SaveAtomic(); err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("uid=%s: save auth failed: %v", uid, err))
			continue
		}

		p.cfg.Pool.Add(a)
		p.cfg.Pool.Revive(uid)

		// 顺带签到/激活（幂等；失败仅记日志，不阻断导入）。
		if realm == "global" {
			if activated, err := p.cfg.Upstream.GlobalCompleteRegistration(a); err != nil {
				log.Printf("panel: import global 注册激活 uid=%s: %v", uid, err)
			} else if activated {
				log.Printf("panel: import global 注册激活 uid=%s 完成", uid)
			}
			if claimed, err := p.cfg.Upstream.ClaimTrial(a); err != nil {
				log.Printf("panel: import global trial uid=%s: %v", uid, err)
			} else if claimed {
				log.Printf("panel: import global trial uid=%s 已领", uid)
			}
		} else {
			// 企业版 / 静态 API Key 无个人成长体系：CN 签到端点对企业号直接 400
			// code 10001「企业账号不支持该操作」，静态 Key 没有个人账号身份。
			// 跳过签到（不制造注定失败的请求与误报），但下方余额查询照常——两种
			// 形态的额度都走企业口径（get-enterprise-user-usage）。
			if !a.SupportsPersonalEndpoints() {
				log.Printf("panel: import 跳过签到 uid=%s（企业版/API Key 无个人成长体系）", uid)
			} else if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
				log.Printf("panel: import checkin uid=%s: %v", uid, err)
			}
		}
		if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
			p.cfg.Pool.ReenableIfCredits(uid, rm, tt)
		}

		imported++
	}

	total = len(accounts)
	log.Printf("panel: cockpit import finished total=%d imported=%d skipped=%d", total, imported, skipped)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"total":    total,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
	})
}

// validImportUID 校验导入 uid 是否可用于拼文件名（同 login.go validUID 口径）。
func validImportUID(uid string) bool {
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

// ---------------------------------------------------------------------------
// 导入格式归一化
// ---------------------------------------------------------------------------

// nestedCockpit 是 cockpit tools「账号详情」导出的嵌套形态：
//
//	{"account":{"uid":"…","nickname":"…"},"auth":{"accessToken":"…","refreshToken":"…"}}
//
// 同一个导出工具会产出两种形态：批量导出是「顶层数组 + snake_case」，单账号详情
// 导出是上面这种嵌套驼峰。用户不会区分，故在解码处统一，而不是让人手工改写。
type nestedCockpit struct {
	Account struct {
		UID           string `json:"uid"`
		Nickname      string `json:"nickname"`
		EnterpriseID  string `json:"enterpriseId"`
		EnterpriseID2 string `json:"enterprise_id"`
	} `json:"account"`
	Auth struct {
		AccessToken   string `json:"accessToken"`
		AccessToken2  string `json:"access_token"`
		RefreshToken  string `json:"refreshToken"`
		RefreshToken2 string `json:"refresh_token"`
		Domain        string `json:"domain"`
		ExpiresAt     int64  `json:"expiresAt"`
		ExpiresAt2    int64  `json:"expires_at"`
	} `json:"auth"`
}

// decodeCockpitAccounts 把导入 JSON 归一成账号列表。接受：
//   - 顶层数组（批量导出）
//   - 单个扁平对象（蛇形或驼峰）
//   - 单个嵌套对象 {account,auth}（账号详情导出）
//   - 信封 {accounts|data|list|items|results|auths: [...]}
//   - 字典 {"<uid>": {...}}（键即 uid）
//
// 认不出的结构才返回 error（调用方转 400）；认得出但缺字段的条目照旧进主循环
// 记 skipped——错误信息里要能说出是哪一条，不能被解码层吞掉。
func decodeCockpitAccounts(raw []byte) ([]cockpitAccount, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		return decodeCockpitItems(items), nil
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, err
		}
		for _, k := range []string{"accounts", "data", "list", "items", "results", "auths"} {
			sub, ok := obj[k]
			if !ok {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(sub, &items); err == nil {
				return decodeCockpitItems(items), nil
			}
			// 信封的值是**单个账号对象**（用户把单账号详情又包了一层）：
			// 按条目解，但绝不能拿信封键当 uid——见字典分支的说明。
			if acc, ok := decodeCockpitOne(sub); ok {
				return []cockpitAccount{acc}, nil
			}
		}
		if _, ok := obj["account"]; ok {
			return decodeCockpitItems([]json.RawMessage{trimmed}), nil
		}
		if _, ok := obj["auth"]; ok {
			return decodeCockpitItems([]json.RawMessage{trimmed}), nil
		}
		if acc, ok := decodeCockpitOne(trimmed); ok {
			return []cockpitAccount{acc}, nil
		}
		// 字典：{"<uid>": {...}}，键当 uid 兜底
		//
		// 信封名不能当 uid：`{"accounts":{"refresh_token":"r"}}` 是「信封的值是单个
		// 对象」形态，走到这里时 accounts 这个键会命中内层（有 token），兜底就会把
		// 账号的 uid 写成字面量 "accounts"——池里从此多一个格式合法、内容垃圾的号。
		// 信封名本身不是 uid（真实 uid 是手机号/UUID），直接跳过。
		keys := make([]string, 0, len(obj))
		for k := range obj {
			if isCockpitEnvelopeKey(k) {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys) // 稳定顺序，便于前端展示与测试比对
		out := make([]cockpitAccount, 0, len(keys))
		for _, k := range keys {
			acc, ok := decodeCockpitOne(obj[k])
			if !ok {
				continue
			}
			if strings.TrimSpace(acc.UID) == "" {
				acc.UID = k
			}
			out = append(out, acc)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("无法识别的 JSON 结构：既不是账号数组，也不是 {account,auth} 或 {\"<uid>\":{…}}")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected JSON array or object")
	}
}

// isCockpitEnvelopeKey 报告键名是否是导出工具的信封名（不是 uid）。
func isCockpitEnvelopeKey(k string) bool {
	for _, e := range []string{"accounts", "data", "list", "items", "results", "auths"} {
		if strings.EqualFold(strings.TrimSpace(k), e) {
			return true
		}
	}
	return false
}

// decodeCockpitItems 逐条解码；认不出的条目保留一个空占位，
// 让主循环按「missing required fields」记账，而不是从结果里凭空消失。
func decodeCockpitItems(items []json.RawMessage) []cockpitAccount {
	out := make([]cockpitAccount, 0, len(items))
	for _, it := range items {
		if acc, ok := decodeCockpitOne(it); ok {
			out = append(out, acc)
			continue
		}
		out = append(out, cockpitAccount{})
	}
	return out
}

// decodeCockpitOne 依次尝试：蛇形扁平 → 驼峰扁平 → {account,auth} 嵌套。
// 判据统一是「拿到至少一个 token」：cockpitAccount 的 tag 是蛇形，直接
// Unmarshal 一个驼峰对象会成功但字段全空，不检查就会把有效账号当成空账号。
func decodeCockpitOne(raw json.RawMessage) (cockpitAccount, bool) {
	var a cockpitAccount
	if err := json.Unmarshal(raw, &a); err == nil && (a.AccessToken != "" || a.RefreshToken != "") {
		return a, true
	}
	var f struct {
		UID          string `json:"uid"`
		Nickname     string `json:"nickname"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
		Domain       string `json:"domain"`
		EnterpriseID string `json:"enterpriseId"`
	}
	if err := json.Unmarshal(raw, &f); err == nil && (f.AccessToken != "" || f.RefreshToken != "") {
		a.UID = f.UID
		a.Nickname = f.Nickname
		a.AccessToken = f.AccessToken
		a.RefreshToken = f.RefreshToken
		a.ExpiresAt = f.ExpiresAt
		a.Domain = f.Domain
		a.EnterpriseIDCamel = f.EnterpriseID
		return a, true
	}
	var n nestedCockpit
	if err := json.Unmarshal(raw, &n); err == nil {
		at := firstNonEmpty(n.Auth.AccessToken, n.Auth.AccessToken2)
		rt := firstNonEmpty(n.Auth.RefreshToken, n.Auth.RefreshToken2)
		if at != "" || rt != "" {
			return cockpitAccount{
				UID:               n.Account.UID,
				Nickname:          n.Account.Nickname,
				EnterpriseIDCamel: firstNonEmpty(n.Account.EnterpriseID, n.Account.EnterpriseID2),
				AccessToken:       at,
				RefreshToken:      rt,
				Domain:            n.Auth.Domain,
				ExpiresAt:         firstNonZero(n.Auth.ExpiresAt, n.Auth.ExpiresAt2),
			}, true
		}
	}
	return a, false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstNonZero(v ...int64) int64 {
	for _, n := range v {
		if n != 0 {
			return n
		}
	}
	return 0
}

// normalizeExpiresAtMillis 把 expires_at 统一成毫秒。
// 批量导出是毫秒（≈1.7e12），单账号详情导出是秒（≈1.7e9）。
// 以 1e11 分界：秒制要走 3000 年才到这个数，毫秒制的历史值也远大于它。
func normalizeExpiresAtMillis(v int64) int64 {
	if v <= 0 {
		return 0
	}
	if v < 1e11 {
		return v * 1000
	}
	return v
}
