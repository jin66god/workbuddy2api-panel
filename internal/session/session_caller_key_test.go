package session

import "testing"

// TestDerivedKeySeparatesCallers 同一段对话内容、不同下游用户必须分槽。
// 键只由 system + 首条 user 决定时，两个用户的开场白相同（"你好"/"hi"/同一套
// 脚手架模板）会派生同一个键：第一个用户绑到账号 X 后，第二个也被钉在 X 上，
// 缓存/配额/上下文全串在一起。
func TestDerivedKeySeparatesCallers(t *testing.T) {
	base := `{"model":"m","messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"你好"}]}`
	a := `{"model":"m","metadata":{"user_id":"alice"},"messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"你好"}]}`
	b := `{"model":"m","metadata":{"user_id":"bob"},"messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"你好"}]}`

	kNone := DeriveCacheKey([]byte(base))
	kA := DeriveCacheKey([]byte(a))
	kB := DeriveCacheKey([]byte(b))

	if kNone == "" || kA == "" || kB == "" {
		t.Fatalf("派生键为空: none=%q a=%q b=%q", kNone, kA, kB)
	}
	if kA == kB {
		t.Fatal("不同 user_id 撞到同一个键（会被钉在同一账号上）")
	}
	if kA == kNone || kB == kNone {
		t.Fatal("带 user_id 与不带 user_id 的键不应相同")
	}
	// 同一调用方的同一段对话必须稳定（逐轮不变），否则每轮都会换号。
	if got := DeriveCacheKey([]byte(a)); got != kA {
		t.Fatalf("同一请求两次派生不一致: %q vs %q", got, kA)
	}
}

// TestDerivedKeyTopLevelUserID 顶层 user_id 与 metadata.user_id 同等有效。
func TestDerivedKeyTopLevelUserID(t *testing.T) {
	top := `{"model":"m","user_id":"alice","messages":[{"role":"user","content":"hi"}]}`
	meta := `{"model":"m","metadata":{"user_id":"alice"},"messages":[{"role":"user","content":"hi"}]}`
	if DeriveCacheKey([]byte(top)) != DeriveCacheKey([]byte(meta)) {
		t.Fatal("顶层 user_id 与 metadata.user_id 应得到同一个键")
	}
}

// TestDerivedKeyNumericUserID 数字型 user_id 必须与字符串型同等有效：
// 只认字符串会让用数字 ID 的 SDK 退回「不掺入」，跨用户撞键的洞重新出现。
func TestDerivedKeyNumericUserID(t *testing.T) {
	num := `{"model":"m","user_id":12345,"messages":[{"role":"user","content":"hi"}]}`
	other := `{"model":"m","user_id":67890,"messages":[{"role":"user","content":"hi"}]}`
	str := `{"model":"m","user_id":"12345","messages":[{"role":"user","content":"hi"}]}`
	if DeriveCacheKey([]byte(num)) == DeriveCacheKey([]byte(other)) {
		t.Fatal("不同数字 user_id 撞到同一个键")
	}
	if DeriveCacheKey([]byte(num)) != DeriveCacheKey([]byte(str)) {
		t.Fatal("数字 12345 与字符串 \"12345\" 应得到同一个键")
	}
}
