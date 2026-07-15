package binancereadonly

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"

	"lingxi/internal/historydownload"
)

const atRemoveDir = 0x200

type authority struct {
	lock *historydownload.RootLock
	root *os.File
}

func acquireAuthority(root string) (*authority, error) {
	lock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		return nil, err
	}
	file, err := lock.OpenRootDir()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &authority{lock: lock, root: file}, nil
}

func (a *authority) close() {
	if a == nil {
		return
	}
	if a.root != nil {
		_ = a.root.Close()
	}
	if a.lock != nil {
		_ = a.lock.Close()
	}
}

func mkdirAt(parent *os.File, name string) (*os.File, error) {
	if !safeName(name) {
		return nil, errors.New("binancereadonly: unsafe authority name")
	}
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, err
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		_ = file.Close()
		return nil, errors.New("binancereadonly: unsafe authority directory")
	}
	return file, nil
}

func readAt(parent *os.File, name string, capBytes int64) ([]byte, error) {
	if !safeName(name) {
		return nil, errors.New("binancereadonly: unsafe authority name")
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > capBytes {
		return nil, errors.New("binancereadonly: unsafe authority file")
	}
	return io.ReadAll(io.LimitReader(file, capBytes+1))
}

func atomicAt(parent *os.File, name string, raw []byte) error {
	if !safeName(name) {
		return errors.New("binancereadonly: unsafe authority name")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "." + name + ".tmp-" + hex.EncodeToString(nonce[:])
	fd, err := syscall.Openat(int(parent.Fd()), temp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			_ = unlinkAt(int(parent.Fd()), temp, 0)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syscall.Renameat(int(parent.Fd()), temp, int(parent.Fd()), name); err != nil {
		return err
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	published = true
	return nil
}

func existsAt(parent *os.File, name string) (bool, error) {
	if !safeName(name) {
		return false, errors.New("binancereadonly: unsafe authority name")
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false, errors.New("binancereadonly: authority entry is not regular")
	}
	return true, nil
}

func loadHMAC(root *os.File) ([]byte, error) {
	raw, err := readAt(root, "identity.hmac", 32)
	if err == nil {
		if len(raw) != 32 {
			return nil, errors.New("binancereadonly: invalid HMAC key")
		}
		return raw, nil
	}
	if !errors.Is(err, syscall.ENOENT) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(root.Fd()), "identity.hmac", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "identity.hmac")
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := root.Sync(); err != nil {
		return nil, err
	}
	return key, nil
}

func idToken(key []byte, raw string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func usageAt(directory *os.File) (int64, error) {
	fd, err := syscall.Openat(int(directory.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	clone := os.NewFile(uintptr(fd), ".")
	defer clone.Close()
	entries, err := clone.Readdir(-1)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("binancereadonly: symlink inside managed root")
		}
		if entry.IsDir() {
			childFD, err := syscall.Openat(int(directory.Fd()), entry.Name(), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return 0, err
			}
			child := os.NewFile(uintptr(childFD), entry.Name())
			size, err := usageAt(child)
			_ = child.Close()
			if err != nil {
				return 0, err
			}
			total += size
		} else if entry.Mode().IsRegular() {
			total += entry.Size()
		} else {
			return 0, errors.New("binancereadonly: unsupported managed-root entry")
		}
	}
	return total, nil
}

func requireCapacity(root *os.File, adding int64) error {
	if adding < 0 {
		return errors.New("binancereadonly: invalid capacity request")
	}
	used, err := usageAt(root)
	if err != nil {
		return err
	}
	if used+adding > PilotRootCap {
		return errors.New("binancereadonly: root cap exceeded")
	}
	return nil
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 {
			return false
		}
	}
	return true
}

func unlinkAt(fd int, name string, flags int) error {
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(fd), uintptr(unsafe.Pointer(pointer)), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}
