package historydownload

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// TokenStore produces stable local opaque tokens without exposing raw IDs to
// research manifests. Its salt never leaves the private root.
type TokenStore struct{ SaltPath string }

type cachedSalt struct {
	mu     sync.Mutex
	loaded bool
	value  [32]byte
}

var saltCache sync.Map

func (s TokenStore) Identity() (string, error) {
	salt, err := s.salt()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(salt)
	return hex.EncodeToString(sum[:]), nil
}

func (s TokenStore) Token(rawID string) (string, error) {
	if rawID == "" {
		return "", errors.New("historydownload: raw ID required")
	}
	salt, err := s.salt()
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, salt)
	_, _ = h.Write([]byte(rawID))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s TokenStore) salt() ([]byte, error) {
	if s.SaltPath == "" {
		return nil, errors.New("historydownload: explicit token salt path required")
	}
	path, err := filepath.Abs(s.SaltPath)
	if err != nil {
		return nil, err
	}
	stateValue, _ := saltCache.LoadOrStore(path, &cachedSalt{})
	state := stateValue.(*cachedSalt)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.loaded {
		out := make([]byte, len(state.value))
		copy(out, state.value[:])
		return out, nil
	}
	value, err := loadOrCreateSalt(path)
	if err != nil {
		return nil, err
	}
	copy(state.value[:], value)
	state.loaded = true
	out := make([]byte, len(state.value))
	copy(out, state.value[:])
	return out, nil
}

func loadOrCreateSalt(path string) ([]byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		file := os.NewFile(uintptr(fd), path)
		if _, err := file.Write(value); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		validated, err := readValidatedSalt(path)
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		return validated, nil
	}
	if !errors.Is(err, syscall.EEXIST) {
		return nil, err
	}
	var readErr error
	for attempt := 0; attempt < 20; attempt++ {
		value, readErr = readValidatedSalt(path)
		if readErr == nil {
			return value, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, readErr
}

func readValidatedSalt(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("historydownload: token salt cannot be opened safely")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		stat.Uid != uint32(os.Geteuid()) || info.Size() != 32 {
		return nil, errors.New("historydownload: token salt must be owner-owned regular mode 0600 and exactly 32 bytes")
	}
	value := make([]byte, 33)
	n, err := io.ReadFull(file, value[:32])
	if err != nil || n != 32 {
		return nil, errors.New("historydownload: token salt read failed")
	}
	extra := make([]byte, 1)
	if n, err := file.Read(extra); err != io.EOF || n != 0 {
		return nil, errors.New("historydownload: token salt length changed during read")
	}
	return value[:32], nil
}
