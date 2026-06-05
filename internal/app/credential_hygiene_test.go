package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRedactSecretPath 钉死凭证路径脱敏为仅文件名。
func TestRedactSecretPath(t *testing.T) {
	if got := redactSecretPath("/home/op/deploy/lingxi/secret.yaml"); got != "secret.yaml" {
		t.Fatalf("应脱敏为文件名, got %q", got)
	}
	if got := redactSecretPath("  "); got != "(unset)" {
		t.Fatalf("空路径应返回 (unset), got %q", got)
	}
}

// TestPersistControlToken 钉死令牌以 0600 写入文件,内容含令牌且不在返回路径之外泄露。
func TestPersistControlToken(t *testing.T) {
	dir := t.TempDir()
	store, err := NewReportStore(dir)
	if err != nil {
		t.Fatalf("建 store: %v", err)
	}
	s := &DashboardServer{store: store, controlToken: "secrettoken123"}

	tokenPath, err := s.persistControlToken()
	if err != nil {
		t.Fatalf("persistControlToken: %v", err)
	}
	if tokenPath != filepath.Join(dir, dashboardControlTokenFileName) {
		t.Fatalf("令牌路径不符: %s", tokenPath)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("令牌文件权限应为 0600, got %o", perm)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if string(data) != "secrettoken123\n" {
		t.Fatalf("令牌文件内容不符: %q", string(data))
	}
}
