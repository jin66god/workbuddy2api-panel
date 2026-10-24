package auth

import (
	"testing"
	"time"
)

// TestParseNormalizesExpiresAtMillis 官方 CLI 凭据写毫秒、本仓内部按秒：
// 解析入口必须归一，否则到期时间落到公元 58800 年 → NeedsRefresh 恒 false
// → token 永不刷新 → 到期后账号静默全 401。
func TestParseNormalizesExpiresAtMillis(t *testing.T) {
	const ms = int64(1795254516410) // 官方凭据实测形态
	const want = ms / 1000

	nested := []byte(`{"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":1795254516410,"domain":"www.workbuddy.ai"},"account":{"uid":"u1"}}`)
	a, err := Parse(nested)
	if err != nil {
		t.Fatalf("Parse(nested): %v", err)
	}
	if a.ExpiresAt != want {
		t.Errorf("nest: ExpiresAt=%d want %d（毫秒应归一为秒）", a.ExpiresAt, want)
	}

	flat := []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":1795254516410}`)
	b, err := Parse(flat)
	if err != nil {
		t.Fatalf("Parse(flat): %v", err)
	}
	if b.ExpiresAt != want {
		t.Errorf("flat: ExpiresAt=%d want %d（毫秒应归一为秒）", b.ExpiresAt, want)
	}
}

// TestParseKeepsExpiresAtSeconds 秒级时间戳（含边界 1e11）不得被误除。
func TestParseKeepsExpiresAtSeconds(t *testing.T) {
	sec := time.Now().Add(time.Hour).Unix()
	raw := []byte(`{"accessToken":"at","expiresAt":` + itoa(sec) + `}`)
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.ExpiresAt != sec {
		t.Errorf("ExpiresAt=%d want %d（秒级不应改动）", a.ExpiresAt, sec)
	}
}

func TestNormalizeExpiresAtThreshold(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{1_700_000_000, 1_700_000_000}, // 秒：2023 年
		{expiresAtMillisThreshold, expiresAtMillisThreshold}, // 边界不除
		{expiresAtMillisThreshold + 1, (expiresAtMillisThreshold + 1) / 1000},
		{1_795_254_516_410, 1_795_254_516}, // 毫秒：2026 年
	}
	for _, c := range cases {
		if got := normalizeExpiresAt(c.in); got != c.want {
			t.Errorf("normalizeExpiresAt(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

// itoa 避免为一条测试引入 strconv 依赖噪声（fmt 亦可，这里保持最小）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
