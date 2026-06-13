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

// venue_funding_positions_store.go 单所正费率执行器持仓台账(records/<venue>/funding_positions.json)。

const venueFundingPositionsFileName = "funding_positions.json"

// VenueFundingPosition 是一笔已开的 delta-neutral 持仓(正 carry:空永续+多现货)。
type VenueFundingPosition struct {
	Symbol           venue.Symbol `json:"symbol"`
	BaseQty          float64      `json:"base_qty"`
	SpotEntryPrice   float64      `json:"spot_entry_price"`
	PerpEntryPrice   float64      `json:"perp_entry_price"`
	SpotNotional     float64      `json:"spot_notional"`
	PerpNotional     float64      `json:"perp_notional"`
	FundingRate      float64      `json:"funding_rate"`
	NextFundingTime  time.Time    `json:"next_funding_time"`
	OpenedAt         time.Time    `json:"opened_at"`
	RunID            string       `json:"run_id"`
	IdemPrefix       string       `json:"idem_prefix"`
	SettlementsHeld  int          `json:"settlements_held"`
	ForceClose       bool         `json:"force_close,omitempty"`
	OpenPerpFee      float64      `json:"open_perp_fee,omitempty"`
	OpenPerpFeeAsset string       `json:"open_perp_fee_asset,omitempty"`
	OpenSpotFee      float64      `json:"open_spot_fee,omitempty"`
	OpenSpotFeeAsset string       `json:"open_spot_fee_asset,omitempty"`
}

type venueFundingPositionsEnvelope struct {
	SchemaVersion int                    `json:"schema_version"`
	Checksum      string                 `json:"checksum"`
	UpdatedAt     string                 `json:"updated_at"`
	Positions     []VenueFundingPosition `json:"positions"`
}

func (e *venueFundingPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *venueFundingPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *venueFundingPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *venueFundingPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type venueFundingPositionStore struct {
	mu   sync.Mutex
	path string
}

func newVenueFundingPositionStore(recordDir string) (*venueFundingPositionStore, error) {
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return nil, err
	}
	return &venueFundingPositionStore{path: filepath.Join(recordDir, venueFundingPositionsFileName)}, nil
}

func (s *venueFundingPositionStore) load() ([]VenueFundingPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *venueFundingPositionStore) loadLocked() ([]VenueFundingPosition, error) {
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
	if trimmed[0] == '[' {
		var out []VenueFundingPosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	var env venueFundingPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("单所 funding 持仓台账", &env); err != nil {
		return nil, err
	}
	return env.Positions, nil
}

func (s *venueFundingPositionStore) saveLocked(ps []VenueFundingPosition) error {
	env := venueFundingPositionsEnvelope{
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
	if err := f.Sync(); err != nil {
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

func (s *venueFundingPositionStore) upsert(p VenueFundingPosition) error {
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

func (s *venueFundingPositionStore) remove(sym venue.Symbol) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]VenueFundingPosition, 0, len(ps))
	for _, p := range ps {
		if p.Symbol != sym {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}
