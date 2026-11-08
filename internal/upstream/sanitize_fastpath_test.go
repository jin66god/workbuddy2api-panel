// sanitize_fastpath_test.go 脱敏预检的快路径等价性：hasFingerprint 从「RE2 (?i)
// 整段折叠」改成「单遍 ASCII 折叠」后，判据必须与正则逐例一致——漏检会让整条净化
// 被跳过，指纹原样出站、上游回 11-128，是静默的线上故障。
package upstream

import (
	"regexp"
	"strings"
	"testing"
)

// TestBareHdrProbeMatchesRegex 快路径与兜底正则必须对同一批输入给出相同结论。
func TestBareHdrProbeMatchesRegex(t *testing.T) {
	probe := sanitizeBareHdrProbe
	if probe == "" {
		t.Fatal("sanitizeBareHdrProbe 为空：正则形态变了却没回落，快路径等于没启用")
	}
	mixed := make([]byte, len(probe))
	for i := 0; i < len(probe); i++ {
		c := probe[i]
		if i%2 == 0 && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		mixed[i] = c
	}
	cases := []string{
		probe,
		strings.ToUpper(probe),
		string(mixed),
		"prefix " + probe + " suffix",
		"``" + strings.ToUpper(probe) + "``",
		probe + ": value;",
		"unrelated text without the marker",
		"",
		probe[:len(probe)-1],           // 缺最后一个字符：不命中
		probe + probe,                  // 出现两次：命中
		strings.Repeat("x", 4096) + "", // 长串无命中（走完整条扫描）
	}
	for i, in := range cases {
		want := sanitizeBareHdrRe.MatchString(in)
		if got := containsFoldASCII(in, probe); got != want {
			t.Fatalf("case %d: containsFoldASCII=%v, regex=%v（len=%d）", i, got, want, len(in))
		}
		if got := hasFingerprint(in); got != want {
			t.Fatalf("case %d: hasFingerprint=%v, regex=%v", i, got, want)
		}
	}
}

// TestBareLiteralProbe 只认「(?i)+纯小写字面量」，其余形态必须回落（返回空串）。
func TestBareLiteralProbe(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"(?i)abc-def", "abc-def"},
		{"(?i:abc_def)", "abc_def"},
		{"(?i)a.c", ""},     // 含元字符
		{"(?i)ABC", ""},     // 含大写：不是规范的小写字面量
		{"abc", ""},         // 没有 (?i) 前缀
		{"(?i)", ""},        // 空字面量
		{"(?i:abc", ""},     // 括号不配对
		{"(?i)ab.*cd", ""},  // 含量词
		{"(?i)abc|def", ""}, // 含分支
	}
	for _, c := range cases {
		if got := bareLiteralProbe(c.expr); got != c.want {
			t.Fatalf("bareLiteralProbe(%q) = %q，期望 %q", c.expr, got, c.want)
		}
	}
}

// TestSanitizeStillStripsHeader 端到端：快路径启用后，真实指纹形态仍被预检命中并被净化。
func TestSanitizeStillStripsHeader(t *testing.T) {
	if sanitizeBareHdrProbe == "" {
		t.Skip("快路径未启用（正则不是纯字面量），本用例无意义")
	}
	in := "run this first " + strings.ToUpper(sanitizeBareHdrProbe) + ": whatever; then continue"
	if !hasFingerprint(in) {
		t.Fatalf("混合大小写的真实指纹未被预检命中")
	}
	out := sanitizeText(in)
	if strings.Contains(strings.ToLower(out), sanitizeBareHdrProbe) {
		t.Fatalf("指纹未被净化：out=%q", out)
	}
	if !strings.Contains(out, "run this first") {
		t.Fatalf("净化把无关正文一起删掉了：out=%q", out)
	}
}

// TestSanitizeUnrelatedUntouched 预检不中时必须逐字节原样返回（零分配快速路径）。
func TestSanitizeUnrelatedUntouched(t *testing.T) {
	in := "  普通的用户输入，没有指纹，前后有空格  "
	if out := sanitizeText(in); out != in {
		t.Fatalf("预检不中却改写了内容：%q -> %q", in, out)
	}
}

// 正则形态变化（有人给 sanitizeBareHdrRe 加了限定）时必须自动回落，不得静默漏检。
func TestBareLiteralProbeRejectsRealRegexShapes(t *testing.T) {
	for _, expr := range []string{`(?i)abc\d+`, `(?i)abc{2}`, `(?i)[a]bc`, `(?i)abc$`} {
		if p := bareLiteralProbe(expr); p != "" {
			t.Fatalf("%q 不是纯字面量却被当成了快路径字面量 %q", expr, p)
		}
		if _, err := regexp.Compile(expr); err != nil {
			t.Fatalf("测试用例本身非法：%q", expr)
		}
	}
}
