package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"lingxi/internal/venue"
)

// bybit_positions_store.go 把 Bybit 在场 delta-neutral 持仓原子落盘(records/<venue>/bybit_positions.json),
// 用于**跨重启 reconcile**:进程崩溃/重启后能恢复在场持仓并按结算时间平仓,绝不留裸腿。
// 写采用 tmp+rename 原子替换,0600 权限;按 Symbol 唯一(同一标的同一时间只持一笔)。

const bybitPositionsFileName = "bybit_positions.json"

type bybitPositionStore struct {
	mu   sync.Mutex
	path string
}

func newBybitPositionStore(recordDir string) (*bybitPositionStore, error) {
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return nil, err
	}
	return &bybitPositionStore{path: filepath.Join(recordDir, bybitPositionsFileName)}, nil
}

func (s *bybitPositionStore) load() ([]BybitLivePosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *bybitPositionStore) loadLocked() ([]BybitLivePosition, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []BybitLivePosition
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *bybitPositionStore) saveLocked(ps []BybitLivePosition) error {
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// upsert 增/改一笔持仓(按 Symbol 唯一)。
func (s *bybitPositionStore) upsert(p BybitLivePosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if ps[i].Symbol == p.Symbol {
			ps[i] = p
			found = true
			break
		}
	}
	if !found {
		ps = append(ps, p)
	}
	return s.saveLocked(ps)
}

// remove 删除某 symbol 的持仓(平仓完成后调用)。
func (s *bybitPositionStore) remove(sym venue.Symbol) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]BybitLivePosition, 0, len(ps))
	for _, p := range ps {
		if p.Symbol != sym {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}
