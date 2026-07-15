package reportcenter

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const atRemovedir = 0x200

type directory struct {
	file *os.File
	dev  uint64
	ino  uint64
}

func heldDirectory(file *os.File) (*directory, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("reportcenter: unsafe held directory")
	}
	return &directory{file: file, dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func openDirAt(parent *directory, name string, create bool) (*directory, error) {
	if !safeName(name) {
		return nil, errors.New("reportcenter: unsafe directory name")
	}
	if create {
		if err := syscall.Mkdirat(int(parent.file.Fd()), name, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, err
		}
	}
	fd, err := syscall.Openat(int(parent.file.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	held, err := heldDirectory(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return held, nil
}

func (d *directory) close() {
	if d != nil && d.file != nil {
		_ = d.file.Close()
	}
}

func (d *directory) validate() error {
	var stat syscall.Stat_t
	if d == nil || d.file == nil || syscall.Fstat(int(d.file.Fd()), &stat) != nil ||
		uint64(stat.Dev) != d.dev || stat.Ino != d.ino ||
		stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Mode&0o777 != 0o700 ||
		stat.Uid != uint32(os.Geteuid()) {
		return errors.New("reportcenter: held directory identity changed")
	}
	return nil
}

type fileSnapshot struct {
	dev, ino, size uint64
	mtimeSec       int64
	mtimeNSec      int64
	ctimeSec       int64
	ctimeNSec      int64
}

func openRegularAt(dir *directory, name string, flags int, mode os.FileMode) (*os.File, fileSnapshot, error) {
	if !safeName(name) {
		return nil, fileSnapshot{}, errors.New("reportcenter: unsafe file name")
	}
	fd, err := syscall.Openat(int(dir.file.Fd()), name, flags|syscall.O_NOFOLLOW, uint32(mode))
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	snapshot, err := snapshotFile(file, mode)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	return file, snapshot, nil
}

func snapshotFile(file *os.File, mode os.FileMode) (fileSnapshot, error) {
	info, err := file.Stat()
	if err != nil {
		return fileSnapshot{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode || stat.Uid != uint32(os.Geteuid()) {
		return fileSnapshot{}, errors.New("reportcenter: file must be owner regular with required mode")
	}
	return fileSnapshot{
		dev: uint64(stat.Dev), ino: stat.Ino, size: uint64(stat.Size),
		mtimeSec: stat.Mtim.Sec, mtimeNSec: stat.Mtim.Nsec,
		ctimeSec: stat.Ctim.Sec, ctimeNSec: stat.Ctim.Nsec,
	}, nil
}

func sameSnapshot(a, b fileSnapshot) bool {
	return a == b
}

func atomicWriteAt(dir *directory, name string, raw []byte, mode os.FileMode) error {
	if err := dir.validate(); err != nil {
		return err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".%s.tmp-%x", name, random[:])
	file, _, err := openRegularAt(dir, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = unlinkAt(int(dir.file.Fd()), tmp, 0)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syscall.Renameat(int(dir.file.Fd()), tmp, int(dir.file.Fd()), name); err != nil {
		return err
	}
	if err := dir.file.Sync(); err != nil {
		return err
	}
	published = true
	return nil
}

func createStage(generations *directory) (string, *directory, error) {
	for attempt := 0; attempt < 20; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := fmt.Sprintf(".staging-%x", random[:])
		if err := syscall.Mkdirat(int(generations.file.Fd()), name, 0o700); err != nil {
			if errors.Is(err, syscall.EEXIST) {
				continue
			}
			return "", nil, err
		}
		dir, err := openDirAt(generations, name, false)
		return name, dir, err
	}
	return "", nil, errors.New("reportcenter: cannot allocate staging directory")
}

func removeStage(generations *directory, name string, stage *directory) {
	if stage != nil {
		clone, err := reopenDir(stage)
		if err == nil {
			entries, _ := clone.file.Readdir(-1)
			for _, entry := range entries {
				if entry.Mode().IsRegular() {
					_ = unlinkAt(int(stage.file.Fd()), entry.Name(), 0)
				}
			}
			clone.close()
		}
		stage.close()
	}
	_ = unlinkAt(int(generations.file.Fd()), name, atRemovedir)
}

func reopenDir(dir *directory) (*directory, error) {
	fd, err := syscall.Openat(int(dir.file.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), ".")
	held, err := heldDirectory(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return held, nil
}

func listDir(dir *directory) ([]os.FileInfo, error) {
	clone, err := reopenDir(dir)
	if err != nil {
		return nil, err
	}
	defer clone.close()
	return clone.file.Readdir(-1)
}

func hashFileAt(dir *directory, name string, mode os.FileMode) (string, int64, error) {
	file, snapshot, err := openRegularAt(dir, name, syscall.O_RDONLY, mode)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), int64(snapshot.size), nil
}

func readFileAt(dir *directory, name string, mode os.FileMode, capBytes int64) ([]byte, error) {
	file, snapshot, err := openRegularAt(dir, name, syscall.O_RDONLY, mode)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if int64(snapshot.size) > capBytes {
		return nil, errors.New("reportcenter: file exceeds read cap")
	}
	return io.ReadAll(io.LimitReader(file, capBytes+1))
}

func rootUsage(root *directory) (int64, error) {
	return directoryUsage(root)
}

func directoryUsage(dir *directory) (int64, error) {
	entries, err := listDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("reportcenter: symlink inside managed root")
		}
		if entry.IsDir() {
			child, err := openDirAt(dir, entry.Name(), false)
			if err != nil {
				return 0, err
			}
			size, err := directoryUsage(child)
			child.close()
			if err != nil {
				return 0, err
			}
			total += size
			continue
		}
		if entry.Mode().IsRegular() {
			file, snapshot, err := openRegularAt(dir, entry.Name(), syscall.O_RDONLY, entry.Mode().Perm())
			if err != nil {
				return 0, err
			}
			_ = file.Close()
			total += int64(snapshot.size)
			continue
		}
		return 0, errors.New("reportcenter: unsupported root entry")
	}
	return total, nil
}

type tokenizer struct{ key []byte }

func loadTokenizer(root *directory) (tokenizer, error) {
	file, _, err := openRegularAt(root, "identity.hmac", syscall.O_RDONLY, 0o600)
	if errors.Is(err, syscall.ENOENT) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return tokenizer{}, err
		}
		file, _, err = openRegularAt(root, "identity.hmac", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0o600)
		if err != nil {
			return tokenizer{}, err
		}
		if _, err := file.Write(key); err != nil {
			_ = file.Close()
			return tokenizer{}, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return tokenizer{}, err
		}
		_ = file.Close()
		if err := root.file.Sync(); err != nil {
			return tokenizer{}, err
		}
		return tokenizer{key: key}, nil
	}
	if err != nil {
		return tokenizer{}, err
	}
	defer file.Close()
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(key) != 32 {
		return tokenizer{}, errors.New("reportcenter: invalid identity HMAC key")
	}
	return tokenizer{key: key}, nil
}

func (t tokenizer) token(raw string) (string, error) {
	if raw == "" || len(t.key) != 32 {
		return "", errors.New("reportcenter: invalid identity token input")
	}
	h := hmac.New(sha256.New, t.key)
	_, _ = h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !containsSlash(name)
}

func containsSlash(name string) bool {
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 {
			return true
		}
	}
	return false
}

func unlinkAt(dirFD int, name string, flags int) error {
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dirFD), uintptr(unsafe.Pointer(pointer)), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}
