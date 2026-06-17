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

// single_venue_perp_spot_position.go 同所 perp-spot 负费率持仓台账(records/single_venue_perp_spot/)。

const singleVenuePerpSpotPositionsFileName = "positions.json"

// SingleVenuePerpSpotStatus 是同所 perp-spot 负 carry 持仓状态机。
type SingleVenuePerpSpotStatus string

const (
	SVPSStatusOpeningSpot SingleVenuePerpSpotStatus = "opening_spot"
	SVPSStatusOpeningPerp SingleVenuePerpSpotStatus = "opening_perp"
	SVPSStatusOpen        SingleVenuePerpSpotStatus = "open"
	SVPSStatusClosing     SingleVenuePerpSpotStatus = "closing"
	SVPSStatusClosed      SingleVenuePerpSpotStatus = "closed"
	SVPSStatusOrphan      SingleVenuePerpSpotStatus = "orphan_single_leg"
)

// SingleVenuePerpSpotLegState 是一条腿的成交快照。
type SingleVenuePerpSpotLegState struct {
	Market     string   `json:"market"` // spot / perp
	Side       string   `json:"side"`   // long / short / buy / sell
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"`
	ClosedQty  float64  `json:"closed_qty,omitempty"`
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// SingleVenuePerpSpotAttribution 开/平/回滚/借币费用累加。
type SingleVenuePerpSpotAttribution struct {
	OpenFeesUSDT      float64 `json:"open_fees_usdt,omitempty"`
	CloseFeesUSDT     float64 `json:"close_fees_usdt,omitempty"`
	OpenSlippageUSDT  float64 `json:"open_slippage_usdt,omitempty"`
	CloseSlippageUSDT float64 `json:"close_slippage_usdt,omitempty"`
	RollbackCostUSDT  float64 `json:"rollback_cost_usdt,omitempty"`
	BorrowCostUSDT    float64 `json:"borrow_cost_usdt,omitempty"`
}

// SingleVenuePerpSpotPosition 是一笔同所多永续+借币空现货负费率持仓。
// 注意:空现货在逼空/强平场景下亏损可能穿仓,执行器保守杠杆并优先平现货腿。
type SingleVenuePerpSpotPosition struct {
	PositionKey          string                        `json:"position_key,omitempty"`
	Symbol               string                        `json:"symbol"`
	Venue                string                        `json:"venue"`
	SpotLeg              SingleVenuePerpSpotLegState   `json:"spot_leg"`
	PerpLeg              SingleVenuePerpSpotLegState   `json:"perp_leg"`
	OpportunityID        string                        `json:"opportunity_id,omitempty"`
	OpenExpectedNetEV    float64                       `json:"open_expected_net_ev,omitempty"`
	ExpectedHoldHours    float64                       `json:"expected_hold_hours,omitempty"`
	OpenFundingRate      float64                       `json:"open_funding_rate,omitempty"`
	FundingIntervalHours int                           `json:"funding_interval_hours,omitempty"`
	NextFundingTime      time.Time                     `json:"next_funding_time,omitempty"`
	RealizedFundingUSDT  float64                       `json:"realized_funding_usdt,omitempty"`
	EstFundingUSDT       float64                       `json:"est_funding_usdt,omitempty"`
	LastFundingSyncAt    time.Time                     `json:"last_funding_sync_at,omitempty"`
	NotionalUSDT         float64                       `json:"notional_usdt,omitempty"`
	Status               SingleVenuePerpSpotStatus     `json:"status"`
	TransitionState      string                        `json:"transition_state,omitempty"`
	IdemPrefix           string                        `json:"idem_prefix"`
	OpenedAt             time.Time                     `json:"opened_at"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	CloseReason          string                        `json:"close_reason,omitempty"`
	CloseAttempts        int                           `json:"close_attempts,omitempty"`
	SingleVenuePerpSpotAttribution
}

type singleVenuePerpSpotPositionsEnvelope struct {
	SchemaVersion int                           `json:"schema_version"`
	Checksum      string                        `json:"checksum"`
	UpdatedAt     string                        `json:"updated_at"`
	Positions     []SingleVenuePerpSpotPosition `json:"positions"`
}

func (e *singleVenuePerpSpotPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *singleVenuePerpSpotPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *singleVenuePerpSpotPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *singleVenuePerpSpotPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type singleVenuePerpSpotPositionStore struct {
	mu   sync.Mutex
	path string
}

func singleVenuePerpSpotRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "single_venue_perp_spot")
}

func newSingleVenuePerpSpotPositionStore(recordDir string) (*singleVenuePerpSpotPositionStore, error) {
	dir := singleVenuePerpSpotRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &singleVenuePerpSpotPositionStore{path: filepath.Join(dir, singleVenuePerpSpotPositionsFileName)}, nil
}

func (s *singleVenuePerpSpotPositionStore) load() ([]SingleVenuePerpSpotPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *singleVenuePerpSpotPositionStore) loadLocked() ([]SingleVenuePerpSpotPosition, error) {
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
		var out []SingleVenuePerpSpotPosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		normalizeSingleVenuePerpSpotPositions(out)
		return out, nil
	}
	var env singleVenuePerpSpotPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("同所perp-spot持仓台账", &env); err != nil {
		return nil, err
	}
	normalizeSingleVenuePerpSpotPositions(env.Positions)
	return env.Positions, nil
}

func (s *singleVenuePerpSpotPositionStore) saveLocked(ps []SingleVenuePerpSpotPosition) error {
	normalizeSingleVenuePerpSpotPositions(ps)
	env := singleVenuePerpSpotPositionsEnvelope{
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

func (s *singleVenuePerpSpotPositionStore) upsert(p SingleVenuePerpSpotPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	svpsEnsurePositionKey(&p)
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if svpsSamePosition(ps[i], p) {
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

func (s *singleVenuePerpSpotPositionStore) remove(positionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]SingleVenuePerpSpotPosition, 0, len(ps))
	positionKey = strings.TrimSpace(positionKey)
	for _, p := range ps {
		if svpsPositionKey(p) != positionKey {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}

func activeSingleVenuePerpSpotPositions(ps []SingleVenuePerpSpotPosition) []SingleVenuePerpSpotPosition {
	out := make([]SingleVenuePerpSpotPosition, 0, len(ps))
	for _, p := range ps {
		if p.Status != SVPSStatusClosed {
			out = append(out, p)
		}
	}
	return out
}

func normalizeSingleVenuePerpSpotPositions(ps []SingleVenuePerpSpotPosition) {
	for i := range ps {
		svpsEnsurePositionKey(&ps[i])
	}
}

func svpsEnsurePositionKey(pos *SingleVenuePerpSpotPosition) {
	if pos == nil {
		return
	}
	if strings.TrimSpace(pos.PositionKey) != "" {
		pos.PositionKey = strings.TrimSpace(pos.PositionKey)
		return
	}
	pos.PositionKey = svpsDerivePositionKey(*pos)
}

func svpsPositionKey(pos SingleVenuePerpSpotPosition) string {
	if key := strings.TrimSpace(pos.PositionKey); key != "" {
		return key
	}
	return svpsDerivePositionKey(pos)
}

func svpsDerivePositionKey(pos SingleVenuePerpSpotPosition) string {
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

func svpsSamePosition(a, b SingleVenuePerpSpotPosition) bool {
	return svpsPositionKey(a) == svpsPositionKey(b)
}

func svpsHasTrackedSymbolVenue(ps []SingleVenuePerpSpotPosition, venueName, symbol string) bool {
	wantVenue := strings.TrimSpace(venueName)
	wantSymbol := strings.TrimSpace(symbol)
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if p.Symbol == wantSymbol && strings.TrimSpace(p.Venue) == wantVenue {
			return true
		}
	}
	return false
}

func svpsIsOpeningStatus(st SingleVenuePerpSpotStatus) bool {
	return st == SVPSStatusOpeningSpot || st == SVPSStatusOpeningPerp
}

// SingleVenuePerpSpotSettlement 平仓结算记录(写入 settlements.jsonl,含借币成本)。
type SingleVenuePerpSpotSettlement struct {
	SettlementID    string    `json:"settlement_id,omitempty"`
	PositionKey     string    `json:"position_key,omitempty"`
	OpportunityID   string    `json:"opportunity_id,omitempty"`
	OpenTime        time.Time `json:"open_time"`
	CloseTime       time.Time `json:"close_time"`
	Symbol          string    `json:"symbol"`
	Venue           string    `json:"venue"`
	NotionalUSDT    float64   `json:"notional_usdt"`
	RealizedFunding float64   `json:"realized_funding"`
	TradePnL        float64   `json:"trade_pnl"`
	BorrowCost      float64   `json:"borrow_cost"`
	OpenFees        float64   `json:"open_fees"`
	CloseFees       float64   `json:"close_fees"`
	RollbackCost    float64   `json:"rollback_cost"`
	TotalFees       float64   `json:"total_fees"`
	NetPnL          float64   `json:"net_pnl"`
	CloseReason     string    `json:"close_reason,omitempty"`
}
