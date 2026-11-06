package safefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReplacesAndKeepsNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := Write(p, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != `{"v":1}` {
		t.Fatalf("内容不符: %q err=%v", got, err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("权限=%v want 0600", fi.Mode().Perm())
	}
}

// 第二次写入必须把第一次的内容留成 .bak —— 这是掉电后唯一的退路。
func TestWriteKeepsPreviousAsBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := Write(p, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + BackupSuffix)
	if err != nil || string(bak) != `{"v":1}` {
		t.Fatalf(".bak=%q err=%v（应为上一版）", bak, err)
	}
}

// 空文件不该覆盖掉上一份好的 .bak。
func TestBackupNotClobberedByEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := Write(p, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte(`{"v":2}`), 0o600); err != nil { // .bak = v1
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil { // 模拟坏文件
		t.Fatal(err)
	}
	if err := Write(p, []byte(`{"v":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, _ := os.ReadFile(p + BackupSuffix)
	if string(bak) != `{"v":1}` {
		t.Fatalf(".bak=%q want {\"v\":1}（空文件不应覆盖备份）", bak)
	}
}

// 全 0 填充的 JSON（掉电后的典型坏文件）必须被 valid 判死并回退到 .bak。
func TestReadWithBackupFallsBackOnZeroFilledFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	good := []byte(`{"accounts":{"u1":{}}}`)
	if err := Write(p, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte(`{"accounts":{}}`), 0o600); err != nil { // .bak = good
		t.Fatal(err)
	}
	// 长度正确、内容全 0
	if err := os.WriteFile(p, make([]byte, len(`{"accounts":{}}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := func(b []byte) bool { return json.Valid(b) }
	raw, used, err := ReadWithBackup(p, valid)
	if err != nil {
		t.Fatalf("应回退成功: %v", err)
	}
	if !used || string(raw) != string(good) {
		t.Fatalf("usedBackup=%v raw=%q want %q", used, raw, good)
	}
}

func TestReadWithBackupPrimaryHealthy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := Write(p, []byte(`{"v":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, used, err := ReadWithBackup(p, json.Valid)
	if err != nil || used || string(raw) != `{"v":9}` {
		t.Fatalf("raw=%q used=%v err=%v", raw, used, err)
	}
}
