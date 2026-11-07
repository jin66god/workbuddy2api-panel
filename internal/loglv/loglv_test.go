package loglv

import "testing"

func TestLevelFromEnv(t *testing.T) {
	cases := map[string]Level{
		"":      LevelInfo,
		"info":  LevelInfo,
		"debug": LevelDebug,
		"WARN":  LevelWarn,
		"error": LevelError,
		"bogus": LevelInfo,
	}
	for env, want := range cases {
		t.Setenv("WB2A_LOG_LEVEL", env)
		t.Setenv("LOG_LEVEL", "")
		resetForTest()
		if got := current(); got != want {
			t.Fatalf("env=%q: level=%v want %v", env, got, want)
		}
	}
}

func TestDebugfGated(t *testing.T) {
	t.Setenv("WB2A_LOG_LEVEL", "info")
	resetForTest()
	if Enabled(LevelDebug) {
		t.Fatal("info 级别不应输出 debug")
	}
	t.Setenv("WB2A_LOG_LEVEL", "debug")
	resetForTest()
	if !Enabled(LevelDebug) {
		t.Fatal("debug 级别应输出 debug")
	}
}
