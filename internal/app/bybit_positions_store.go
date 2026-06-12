package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lingxi/internal/venue"
)

// bybit_positions_store.go 把 Bybit 在场 delta-neutral 持仓原子落盘(records/<venue>/bybit_positions.json),
// 用于**跨重启 reconcile**:进程崩溃/重启后能恢复在场持仓并按结算时间平仓,绝不留裸腿。
//
// 持久化契约(与币安 live 台账同口径,R18):
//   - 信封头:schema_version + SHA256 checksum(stampStateEnvelope / guardStateEnvelope),
//     校验和不匹配(损坏/篡改)或版本超前 → 拒绝自动接管,要求人工核对(绝不在不可信状态上自动恢复)。
//   - 原子写:tmp + **fsync** + rename(防写半截 + 防 rename 后内容未落盘);0600 权限。
//   - 向后兼容:旧版本写的「裸 JSON 数组」(以 '[' 开头)按 legacy 直接放行,零迁移负担。
//   - 按 Symbol 唯一(同一标的同一时间只持一笔)。

const bybitPositionsFileName = "bybit_positions.json"

// bybitPositionsEnvelope 是带信封头的 Bybit 持仓台账(满足 checksummableState)。
type bybitPositionsEnvelope struct {
	SchemaVersion int                 `json:"schema_version"`
	Checksum      string              `json:"checksum"`
	UpdatedAt     string              `json:"updated_at"`
	Positions     []BybitLivePosition `json:"positions"`
}

func (e *bybitPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *bybitPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *bybitPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *bybitPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

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
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	// 向后兼容:旧版本写的裸 JSON 数组(以 '[' 开头)无信封,按 legacy 直接放行。
	if trimmed[0] == '[' {
		var out []BybitLivePosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return normalizeBybitPositions(out), nil
	}
	// 新格式:带信封头。校验完整性/版本后才放行(不可信状态拒绝自动接管)。
	var env bybitPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("Bybit 持仓台账", &env); err != nil {
		return nil, err
	}
	return normalizeBybitPositions(env.Positions), nil
}

// bybitLegacyForceCloseSentinel:旧版本用 SettlementsHeld=1<<30 当「立即到期待平」哨兵,
// 导致平仓日志/结算台账出现「持有 1073741825 次结算」「期望资金费上亿」的荒谬数字。
const bybitLegacyForceCloseSentinel = 1 << 30

// normalizeBybitPositions 把旧哨兵记录收编为显式 ForceClose 语义(加载时一次性迁移,行为不变:
// 同样不续持、直接平,但结算次数回归真实口径 0)。
func normalizeBybitPositions(ps []BybitLivePosition) []BybitLivePosition {
	for i := range ps {
		if ps[i].SettlementsHeld >= bybitLegacyForceCloseSentinel {
			ps[i].SettlementsHeld = 0
			ps[i].ForceClose = true
		}
	}
	return ps
}

func (s *bybitPositionStore) saveLocked(ps []BybitLivePosition) error {
	env := bybitPositionsEnvelope{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Positions: ps,
	}
	if err := stampStateEnvelope(&env); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&env, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil { // 落盘后再 rename,防 rename 成功但内容未落盘(崩溃丢数据)
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
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
