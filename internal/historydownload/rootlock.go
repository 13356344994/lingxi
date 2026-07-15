package historydownload

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrRootLocked = errors.New("historydownload: root is locked by another process")

type RootLock struct {
	root string
	file *os.File
	dev  uint64
	ino  uint64
}

func AcquireRootLock(root string) (*RootLock, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	original, err := os.Lstat(absolute)
	if err != nil || original.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("historydownload: root symlink rejected")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if canonical != filepath.Clean(absolute) {
		return nil, errors.New("historydownload: root path contains symlink")
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 ||
		stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("historydownload: root must be owner-controlled non-symlink directory mode 0700")
	}
	lockPath := filepath.Join(canonical, ".historydownload.lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), lockPath)
	closeOnError := func(err error) (*RootLock, error) {
		_ = file.Close()
		return nil, err
	}
	lockInfo, err := file.Stat()
	if err != nil {
		return closeOnError(err)
	}
	lockStat, ok := lockInfo.Sys().(*syscall.Stat_t)
	if !ok || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 ||
		lockStat.Uid != uint32(os.Geteuid()) {
		return closeOnError(errors.New("historydownload: invalid root lock file"))
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return closeOnError(ErrRootLocked)
		}
		return closeOnError(err)
	}
	return &RootLock{root: canonical, file: file, dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func (l *RootLock) Root() string {
	if l == nil {
		return ""
	}
	return l.root
}

// OpenRootDir returns a held descriptor for the exact directory inode bound by
// this lock. Callers can use fd-relative operations without trusting the root
// pathname again. Closing the returned file does not release the lock.
func (l *RootLock) OpenRootDir() (*os.File, error) {
	if l == nil || l.file == nil {
		return nil, errors.New("historydownload: held root lock required")
	}
	fd, err := syscall.Open(l.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), l.root)
	info, err := root.Stat()
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != l.dev || stat.Ino != l.ino ||
		!info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		_ = root.Close()
		return nil, errors.New("historydownload: locked root identity changed")
	}
	if _, err := l.file.Stat(); err != nil {
		_ = root.Close()
		return nil, errors.New("historydownload: root lock is closed")
	}
	return root, nil
}

func (l *RootLock) Validate(root string) error {
	if l == nil || l.file == nil {
		return errors.New("historydownload: held root lock required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != l.root {
		return errors.New("historydownload: root lock mismatch")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("historydownload: locked root changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != l.dev || stat.Ino != l.ino ||
		info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("historydownload: locked root identity changed")
	}
	if _, err := l.file.Stat(); err != nil {
		return errors.New("historydownload: root lock is closed")
	}
	return nil
}

func (l *RootLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	fd := int(l.file.Fd())
	unlockErr := syscall.Flock(fd, syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
