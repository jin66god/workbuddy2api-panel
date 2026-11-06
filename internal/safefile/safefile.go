// Package safefile 原子、掉电安全地替换状态文件，并在文件损坏时回退到上一份好副本。
//
// 为什么 tmp + rename 不够：rename 只保证读者看到的是一个完整文件，不保证内容已经
// 落盘。掉电时可能留下「长度正确、内容全 0」的文件（ext4/XFS 上 rename 的日志先提交、
// 数据块后刷盘）。所以临时文件必须先 fsync 再 rename，并把替换前的有效内容留成
// <path>.bak —— 坏文件被读成「空」之后，下一次写入就会把它彻底抹掉，而这份 .bak
// 是唯一的退路。
//
// 与上游实现的取舍：这里不引入独立的 fsync 包装层，也不做「目录项 fsync 失败即报错」
// （Windows 上打开目录同步会直接失败，而我们的发布物包含 windows/amd64）；目录同步
// 尽力而为，数据同步是硬要求。
package safefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BackupSuffix 备份文件后缀。固定值便于运维在故障现场直接找。
const BackupSuffix = ".bak"

// Write 原子、掉电安全地替换 path。
//
// 步骤：同目录建临时文件 → 写入 → chmod → fsync → close → 备份旧内容到 .bak →
// rename 覆盖 → 目录同步（尽力）。任一步失败都不会让 path 变成半截文件。
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	abort := func(e error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return e
	}
	if _, err := f.Write(data); err != nil {
		return abort(err)
	}
	if err := f.Chmod(perm); err != nil {
		return abort(err)
	}
	// 数据落盘必须在 rename 之前：rename 只保证原子可见性，不保证内容已持久化。
	if err := f.Sync(); err != nil {
		return abort(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// 备份失败不阻塞主写：主写成功比备份重要，.bak 只是下一轮损坏时的退路。
	_ = backup(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// backup 把 path 当前内容复制到 path.bak（自身 fsync）。文件不存在或为空时跳过——
// 空文件没有备份价值，覆盖掉上一份好的 .bak 反而更糟。
func backup(path string) error {
	old, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(old) == 0 {
		return nil
	}
	dst := path + BackupSuffix
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(old); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir 尽力同步目录项。Windows 上打开目录做 Sync 会失败，忽略即可：数据已落盘，
// 这里只是让 rename 本身也持久化，属锦上添花。
func syncDir(dir string) {
	if dir == "" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// ReadWithBackup 读 path；内容为空、或 valid 判定为损坏时回退到 path.bak。
//
// valid 用于识别「长度正确但内容已损坏」（全 0 填充的 JSON 就是这种情况），传 nil
// 表示只按空文件判断。返回 usedBackup 便于调用方打一条恢复日志。
func ReadWithBackup(path string, valid func([]byte) bool) (raw []byte, usedBackup bool, err error) {
	ok := func(b []byte) bool {
		if len(b) == 0 {
			return false
		}
		return valid == nil || valid(b)
	}
	raw, err = os.ReadFile(path)
	if err == nil && ok(raw) {
		return raw, false, nil
	}
	primaryErr := err
	if alt, aerr := os.ReadFile(path + BackupSuffix); aerr == nil && ok(alt) {
		return alt, true, nil
	}
	if primaryErr != nil {
		return nil, false, primaryErr
	}
	return nil, false, fmt.Errorf("safefile: %s 与其备份均不可用", path)
}
