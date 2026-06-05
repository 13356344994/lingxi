package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSingleWriterLeaseExcludesSecond 钉死:第一个租约持有时,第二个抢锁立即失败;释放后可再次获取。
func TestSingleWriterLeaseExcludesSecond(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireSingleWriterLease(dir)
	if err != nil {
		t.Fatalf("首个租约应成功: %v", err)
	}

	if second, err := acquireSingleWriterLease(dir); err == nil {
		_ = second.Release()
		t.Fatal("第二个租约应被拒绝(防双开)")
	} else if !strings.Contains(err.Error(), "双开") {
		t.Fatalf("错误信息应解释防双开, got %v", err)
	}

	// 锁文件应记录持有者诊断信息。
	data, _ := os.ReadFile(filepath.Join(dir, singleWriterLeaseFileName))
	if !strings.Contains(string(data), "pid=") {
		t.Fatalf("锁文件应含持有者 pid, got %q", string(data))
	}

	if err := first.Release(); err != nil {
		t.Fatalf("释放租约失败: %v", err)
	}

	// 释放后应可重新获取。
	third, err := acquireSingleWriterLease(dir)
	if err != nil {
		t.Fatalf("释放后应可重新获取: %v", err)
	}
	_ = third.Release()
}

// TestSingleWriterLeaseReleaseNilSafe 钉死 Release 对 nil/重复调用安全。
func TestSingleWriterLeaseReleaseNilSafe(t *testing.T) {
	var l *singleWriterLease
	if err := l.Release(); err != nil {
		t.Fatalf("nil Release 应安全: %v", err)
	}
	lease, err := acquireSingleWriterLease(t.TempDir())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("首次 Release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("重复 Release 应安全: %v", err)
	}
}
