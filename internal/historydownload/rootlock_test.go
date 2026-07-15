package historydownload

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRootLockExcludesSecondHolder(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireRootLock(root); !errors.Is(err, ErrRootLocked) {
		t.Fatalf("second holder entered critical section: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireRootLock(root)
	if err != nil {
		t.Fatalf("lock unavailable after release: %v", err)
	}
	_ = second.Close()
}

func TestRootLockRejectsUnsafeLockFile(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".historydownload.lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireRootLock(root); err == nil {
			t.Fatal("0644 lock file accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, ".historydownload.lock")); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireRootLock(root); err == nil {
			t.Fatal("symlink lock file accepted")
		}
	})
}

func TestRootLockRejectsSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireRootLock(link); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestRootLockOpenRootDirBindsOriginalInode(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	held, err := lock.OpenRootDir()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	moved := root + ".moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	beforeStat := before.Sys().(*syscall.Stat_t)
	afterStat := after.Sys().(*syscall.Stat_t)
	if beforeStat.Dev != afterStat.Dev || beforeStat.Ino != afterStat.Ino {
		t.Fatal("held root descriptor changed inode")
	}
	if _, err := lock.OpenRootDir(); err == nil {
		t.Fatal("replacement root pathname was rebound to held lock")
	}
}

func TestRootLockExcludesOtherProcess(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "helper.ready")
	release := filepath.Join(root, "helper.release")
	command := exec.Command(os.Args[0], "-test.run=^TestRootLockProcessHelper$")
	command.Env = append(os.Environ(),
		"HISTORYDOWNLOAD_LOCK_HELPER=1",
		"HISTORYDOWNLOAD_LOCK_ROOT="+root,
		"HISTORYDOWNLOAD_LOCK_READY="+ready,
		"HISTORYDOWNLOAD_LOCK_RELEASE="+release,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = command.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := AcquireRootLock(root); !errors.Is(err, ErrRootLocked) {
		t.Fatalf("other process lock did not exclude caller: %v", err)
	}
}

func TestRootLockProcessHelper(t *testing.T) {
	if os.Getenv("HISTORYDOWNLOAD_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	lock, err := AcquireRootLock(os.Getenv("HISTORYDOWNLOAD_LOCK_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := os.WriteFile(os.Getenv("HISTORYDOWNLOAD_LOCK_READY"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("HISTORYDOWNLOAD_LOCK_RELEASE")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("release signal timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDownloadRequiresHeldMatchingRootLock(t *testing.T) {
	root := t.TempDir()
	cfg := testDownloadConfig(t, root)
	if err := cfg.RootLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		return Page{}, nil
	}); err == nil {
		t.Fatal("closed root lock accepted")
	}
}
