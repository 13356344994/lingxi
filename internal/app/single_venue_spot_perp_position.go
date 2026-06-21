package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// single_venue_spot_perp_position.go 同所 spot-perp 持仓台账(独立于 crossvenue 目录)。

const singleVenueSpotPerpPositionsFileName = "positions.json"

// SingleVenueSpotPerpStatus 是同所 spot-perp 持仓状态机。
type SingleVenueSpotPerpStatus string

const (
	SVSPStatusOpeningSpot SingleVenueSpotPerpStatus = "opening_spot"
	SVSPStatusOpeningPerp SingleVenueSpotPerpStatus = "opening_perp"
	SVSPStatusOpen        SingleVenueSpotPerpStatus = "open"
	SVSPStatusClosing     SingleVenueSpotPerpStatus = "closing"
	SVSPStatusClosed      SingleVenueSpotPerpStatus = "closed"
	SVSPStatusOrphan      SingleVenueSpotPerpStatus = "orphan_single_leg"
)

// SingleVenueSpotPerpLegState 是一条腿的成交快照。
type SingleVenueSpotPerpLegState struct {
	Market     string   `json:"market"` // spot / perp
	Side       string   `json:"side"`   // buy / sell / short
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"`
	ClosedQty  float64  `json:"closed_qty,omitempty"`
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// SingleVenueSpotPerpAttribution 开/平/回滚费用与滑点累加。
type SingleVenueSpotPerpAttribution struct {
	OpenFeesUSDT      float64 `json:"open_fees_usdt,omitempty"`
	CloseFeesUSDT     float64 `json:"close_fees_usdt,omitempty"`
	OpenSlippageUSDT  float64 `json:"open_slippage_usdt,omitempty"`
	CloseSlippageUSDT float64 `json:"close_slippage_usdt,omitempty"`
	RollbackCostUSDT  float64 `json:"rollback_cost_usdt,omitempty"`
}

// SingleVenueSpotPerpPosition 是一笔同所买现货+空永续正费率持仓。
type SingleVenueSpotPerpPosition struct {
	PositionKey          string                      `json:"position_key,omitempty"`
	Symbol               string                      `json:"symbol"`
	Venue                string                      `json:"venue"`
	SpotLeg              SingleVenueSpotPerpLegState `json:"spot_leg"`
	PerpLeg              SingleVenueSpotPerpLegState `json:"perp_leg"`
	OpportunityID        string                      `json:"opportunity_id,omitempty"`
	OpenExpectedNetEV    float64                     `json:"open_expected_net_ev,omitempty"`
	ExpectedHoldHours    float64                     `json:"expected_hold_hours,omitempty"`
	OpenFundingRate      float64                     `json:"open_funding_rate,omitempty"`
	FundingIntervalHours int                         `json:"funding_interval_hours,omitempty"`
	NextFundingTime      time.Time                   `json:"next_funding_time,omitempty"`
	RealizedFundingUSDT  float64                     `json:"realized_funding_usdt,omitempty"`
	EstFundingUSDT       float64                     `json:"est_funding_usdt,omitempty"`
	LastFundingSyncAt    time.Time                   `json:"last_funding_sync_at,omitempty"`
	NotionalUSDT         float64                     `json:"notional_usdt,omitempty"`
	Status               SingleVenueSpotPerpStatus   `json:"status"`
	TransitionState      string                      `json:"transition_state,omitempty"`
	IdemPrefix           string                      `json:"idem_prefix"`
	OpenedAt             time.Time                   `json:"opened_at"`
	UpdatedAt            time.Time                   `json:"updated_at"`
	CloseReason          string                      `json:"close_reason,omitempty"`
	CloseAttempts        int                         `json:"close_attempts,omitempty"`
	LastHedgeRebalanceAt time.Time                   `json:"last_hedge_rebalance_at,omitempty"`
	DustResidualCostUSDT float64                     `json:"dust_residual_cost_usdt,omitempty"`
	SingleVenueSpotPerpAttribution
}

type singleVenueSpotPerpPositionsEnvelope struct {
	SchemaVersion int                           `json:"schema_version"`
	Checksum      string                        `json:"checksum"`
	UpdatedAt     string                        `json:"updated_at"`
	Positions     []SingleVenueSpotPerpPosition `json:"positions"`
}

func (e *singleVenueSpotPerpPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *singleVenueSpotPerpPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *singleVenueSpotPerpPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *singleVenueSpotPerpPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type singleVenueSpotPerpPositionStore struct {
	mu   sync.Mutex
	path string
}

func singleVenueSpotPerpRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "single_venue_spot_perp")
}

func newSingleVenueSpotPerpPositionStore(recordDir string) (*singleVenueSpotPerpPositionStore, error) {
	dir := singleVenueSpotPerpRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &singleVenueSpotPerpPositionStore{path: filepath.Join(dir, singleVenueSpotPerpPositionsFileName)}, nil
}

func (s *singleVenueSpotPerpPositionStore) load() ([]SingleVenueSpotPerpPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *singleVenueSpotPerpPositionStore) loadLocked() ([]SingleVenueSpotPerpPosition, error) {
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
		var out []SingleVenueSpotPerpPosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		normalizeSingleVenueSpotPerpPositions(out)
		return out, nil
	}
	var env singleVenueSpotPerpPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("同所spot-perp持仓台账", &env); err != nil {
		return nil, err
	}
	normalizeSingleVenueSpotPerpPositions(env.Positions)
	return env.Positions, nil
}

func (s *singleVenueSpotPerpPositionStore) saveLocked(ps []SingleVenueSpotPerpPosition) error {
	normalizeSingleVenueSpotPerpPositions(ps)
	env := singleVenueSpotPerpPositionsEnvelope{
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

func (s *singleVenueSpotPerpPositionStore) saveAll(ps []SingleVenueSpotPerpPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(ps)
}

func (s *singleVenueSpotPerpPositionStore) upsert(p SingleVenueSpotPerpPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	svspEnsurePositionKey(&p)
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if svspSamePosition(ps[i], p) {
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

func (s *singleVenueSpotPerpPositionStore) remove(positionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]SingleVenueSpotPerpPosition, 0, len(ps))
	positionKey = strings.TrimSpace(positionKey)
	for _, p := range ps {
		if svspPositionKey(p) != positionKey {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}

func activeSingleVenueSpotPerpPositions(ps []SingleVenueSpotPerpPosition) []SingleVenueSpotPerpPosition {
	out := make([]SingleVenueSpotPerpPosition, 0, len(ps))
	for _, p := range ps {
		if p.Status != SVSPStatusClosed {
			out = append(out, p)
		}
	}
	return out
}

func normalizeSingleVenueSpotPerpPositions(ps []SingleVenueSpotPerpPosition) {
	for i := range ps {
		svspEnsurePositionKey(&ps[i])
	}
}

func svspEnsurePositionKey(pos *SingleVenueSpotPerpPosition) {
	if pos == nil {
		return
	}
	if strings.TrimSpace(pos.PositionKey) != "" {
		pos.PositionKey = strings.TrimSpace(pos.PositionKey)
		return
	}
	pos.PositionKey = svspDerivePositionKey(*pos)
}

func svspPositionKey(pos SingleVenueSpotPerpPosition) string {
	if key := strings.TrimSpace(pos.PositionKey); key != "" {
		return key
	}
	return svspDerivePositionKey(pos)
}

func svspDerivePositionKey(pos SingleVenueSpotPerpPosition) string {
	symbol := strings.TrimSpace(pos.Symbol)
	if symbol == "" {
		return ""
	}
	if idem := strings.TrimSpace(pos.IdemPrefix); idem != "" {
		return symbol + "|" + strings.TrimSpace(pos.Venue) + "|" + idem
	}
	if !pos.OpenedAt.IsZero() {
		return fmt.Sprintf("%s|%s|%d", symbol, strings.TrimSpace(pos.Venue), pos.OpenedAt.UTC().UnixNano())
	}
	return symbol + "|" + strings.TrimSpace(pos.Venue)
}

func svspSamePosition(a, b SingleVenueSpotPerpPosition) bool {
	return svspPositionKey(a) == svspPositionKey(b)
}

func svspHasTrackedSymbolVenue(ps []SingleVenueSpotPerpPosition, venueName, symbol string) bool {
	wantVenue := strings.TrimSpace(venueName)
	wantSymbol := strings.TrimSpace(symbol)
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if p.Symbol == wantSymbol && strings.TrimSpace(p.Venue) == wantVenue {
			return true
		}
	}
	return false
}

func svspIsOpeningStatus(st SingleVenueSpotPerpStatus) bool {
	return st == SVSPStatusOpeningSpot || st == SVSPStatusOpeningPerp
}

// SingleVenueSpotPerpSettlement 平仓结算记录(写入 settlements.jsonl)。
type SingleVenueSpotPerpSettlement struct {
	SettlementID         string    `json:"settlement_id,omitempty"`
	PositionKey          string    `json:"position_key,omitempty"`
	OpportunityID        string    `json:"opportunity_id,omitempty"`
	OpenTime             time.Time `json:"open_time"`
	CloseTime            time.Time `json:"close_time"`
	Symbol               string    `json:"symbol"`
	Venue                string    `json:"venue"`
	NotionalUSDT         float64   `json:"notional_usdt"`
	ExpectedFundingUSDT  float64   `json:"expected_funding_usdt,omitempty"`
	FundingIntervalHours int       `json:"funding_interval_hours,omitempty"`
	RealizedFunding      float64   `json:"realized_funding"`
	TradePnL             float64   `json:"trade_pnl"`
	OpenFees             float64   `json:"open_fees"`
	CloseFees            float64   `json:"close_fees"`
	RollbackCost         float64   `json:"rollback_cost"`
	DustResidualCost     float64   `json:"dust_residual_cost,omitempty"`
	TotalFees            float64   `json:"total_fees"`
	NetPnL               float64   `json:"net_pnl"`
	CloseReason          string    `json:"close_reason,omitempty"`
}
