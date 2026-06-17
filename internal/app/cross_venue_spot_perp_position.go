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

// cross_venue_spot_perp_position.go 跨所 spot-perp 持仓台账与结算落盘(records/cross_venue_spot_perp/)。

const (
	crossVenueSpotPerpPositionsFile   = "positions.json"
	crossVenueSpotPerpSettlementsFile = "settlements.jsonl"
)

// CrossVenueSpotPerpPositionStatus 是跨所 spot-perp 持仓状态机。
type CrossVenueSpotPerpPositionStatus string

const (
	CVSPStatusOpeningFirstLeg  CrossVenueSpotPerpPositionStatus = "opening_first_leg"
	CVSPStatusOpeningSecondLeg CrossVenueSpotPerpPositionStatus = "opening_second_leg"
	CVSPStatusOpen             CrossVenueSpotPerpPositionStatus = "open"
	CVSPStatusClosing          CrossVenueSpotPerpPositionStatus = "closing"
	CVSPStatusClosed           CrossVenueSpotPerpPositionStatus = "closed"
	CVSPStatusOrphanSingleLeg  CrossVenueSpotPerpPositionStatus = "orphan_single_leg"
)

// CrossVenueSpotPerpLegState 是一条腿的成交快照。
type CrossVenueSpotPerpLegState struct {
	Venue      string   `json:"venue"`
	Market     string   `json:"market"` // spot / perp
	Side       string   `json:"side"`   // buy / sell / short / long
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"`
	ClosedQty  float64  `json:"closed_qty,omitempty"`
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// CrossVenueSpotPerpPosition 是一笔跨所 spot-perp 正费率持仓(A 所买现货 + B 所空永续)。
type CrossVenueSpotPerpPosition struct {
	PositionKey          string                           `json:"position_key,omitempty"`
	Symbol               string                           `json:"symbol"`
	SpotVenue            string                           `json:"spot_venue"`
	PerpVenue            string                           `json:"perp_venue"`
	SpotLeg              CrossVenueSpotPerpLegState       `json:"spot_leg"`
	PerpLeg              CrossVenueSpotPerpLegState       `json:"perp_leg"`
	OpportunityID        string                           `json:"opportunity_id,omitempty"`
	ExpectedFundingUSDT  float64                          `json:"expected_funding_usdt,omitempty"`
	ExpectedNetEVUSDT    float64                          `json:"expected_net_ev_usdt,omitempty"`
	ExpectedHoldHours    float64                          `json:"expected_hold_hours,omitempty"`
	PerpFundingRate      float64                          `json:"perp_funding_rate,omitempty"`
	PerpLegFundingUSDT   float64                          `json:"perp_leg_funding_usdt,omitempty"`
	EstFundingUSDT       float64                          `json:"est_funding_usdt,omitempty"`
	Status               CrossVenueSpotPerpPositionStatus `json:"status"`
	TransitionState      string                           `json:"transition_state,omitempty"`
	IdemPrefix           string                           `json:"idem_prefix"`
	NotionalUSDT         float64                          `json:"notional_usdt,omitempty"`
	OpenedAt             time.Time                        `json:"opened_at"`
	UpdatedAt            time.Time                        `json:"updated_at"`
	LastAccrualAt        time.Time                        `json:"last_accrual_at,omitempty"`
	CloseReason          string                           `json:"close_reason,omitempty"`
	CloseAttempts        int                              `json:"close_attempts,omitempty"`
	OpenFeesUSDT         float64                          `json:"open_fees_usdt,omitempty"`
	CloseFeesUSDT        float64                          `json:"close_fees_usdt,omitempty"`
	RollbackCostUSDT     float64                          `json:"rollback_cost_usdt,omitempty"`
}

// CrossVenueSpotPerpSettlement 是单仓平仓结算记录。
type CrossVenueSpotPerpSettlement struct {
	SettlementID        string    `json:"settlement_id,omitempty"`
	PositionKey         string    `json:"position_key,omitempty"`
	OpportunityID       string    `json:"opportunity_id,omitempty"`
	OpenTime            time.Time `json:"open_time"`
	CloseTime           time.Time `json:"close_time"`
	Symbol              string    `json:"symbol"`
	SpotVenue           string    `json:"spot_venue"`
	PerpVenue           string    `json:"perp_venue"`
	NotionalUSDT        float64   `json:"notional_usdt"`
	PerpLegFunding      float64   `json:"perp_leg_funding"`
	RealizedFunding     float64   `json:"realized_funding"`
	OpenFees            float64   `json:"open_fees"`
	CloseFees           float64   `json:"close_fees"`
	RollbackCost        float64   `json:"rollback_cost"`
	TotalFees           float64   `json:"total_fees"`
	NetPnL              float64   `json:"net_pnl"`
	CloseReason         string    `json:"close_reason,omitempty"`
}

type crossVenueSpotPerpPositionsEnvelope struct {
	SchemaVersion int                          `json:"schema_version"`
	Checksum      string                       `json:"checksum"`
	UpdatedAt     string                       `json:"updated_at"`
	Positions     []CrossVenueSpotPerpPosition `json:"positions"`
}

func (e *crossVenueSpotPerpPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *crossVenueSpotPerpPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *crossVenueSpotPerpPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *crossVenueSpotPerpPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type crossVenueSpotPerpPositionStore struct {
	mu   sync.Mutex
	dir  string
	path string
}

func crossVenueSpotPerpRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "cross_venue_spot_perp")
}

func newCrossVenueSpotPerpPositionStore(recordDir string) (*crossVenueSpotPerpPositionStore, error) {
	dir := crossVenueSpotPerpRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &crossVenueSpotPerpPositionStore{
		dir:  dir,
		path: filepath.Join(dir, crossVenueSpotPerpPositionsFile),
	}, nil
}

func (s *crossVenueSpotPerpPositionStore) load() ([]CrossVenueSpotPerpPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *crossVenueSpotPerpPositionStore) loadLocked() ([]CrossVenueSpotPerpPosition, error) {
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
		var out []CrossVenueSpotPerpPosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		normalizeCrossVenueSpotPerpPositions(out)
		return out, nil
	}
	var env crossVenueSpotPerpPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("跨所spot-perp持仓台账", &env); err != nil {
		return nil, err
	}
	normalizeCrossVenueSpotPerpPositions(env.Positions)
	return env.Positions, nil
}

func (s *crossVenueSpotPerpPositionStore) saveLocked(ps []CrossVenueSpotPerpPosition) error {
	normalizeCrossVenueSpotPerpPositions(ps)
	env := crossVenueSpotPerpPositionsEnvelope{
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

func (s *crossVenueSpotPerpPositionStore) upsert(p CrossVenueSpotPerpPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cvspEnsurePositionKey(&p)
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if cvspSamePosition(ps[i], p) {
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

func (s *crossVenueSpotPerpPositionStore) remove(positionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]CrossVenueSpotPerpPosition, 0, len(ps))
	positionKey = strings.TrimSpace(positionKey)
	for _, p := range ps {
		if cvspPositionKey(p) != positionKey {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}

func (s *crossVenueSpotPerpPositionStore) appendSettlement(rec CrossVenueSpotPerpSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, crossVenueSpotPerpSettlementsFile)
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func activeCrossVenueSpotPerpPositions(ps []CrossVenueSpotPerpPosition) []CrossVenueSpotPerpPosition {
	out := make([]CrossVenueSpotPerpPosition, 0, len(ps))
	for _, p := range ps {
		if p.Status != CVSPStatusClosed {
			out = append(out, p)
		}
	}
	return out
}

func normalizeCrossVenueSpotPerpPositions(ps []CrossVenueSpotPerpPosition) {
	for i := range ps {
		cvspEnsurePositionKey(&ps[i])
	}
}

func cvspEnsurePositionKey(pos *CrossVenueSpotPerpPosition) {
	if pos == nil {
		return
	}
	if strings.TrimSpace(pos.PositionKey) != "" {
		pos.PositionKey = strings.TrimSpace(pos.PositionKey)
		return
	}
	pos.PositionKey = cvspDerivePositionKey(*pos)
}

func cvspPositionKey(pos CrossVenueSpotPerpPosition) string {
	if key := strings.TrimSpace(pos.PositionKey); key != "" {
		return key
	}
	return cvspDerivePositionKey(pos)
}

func cvspDerivePositionKey(pos CrossVenueSpotPerpPosition) string {
	symbol := strings.TrimSpace(pos.Symbol)
	if symbol == "" {
		return ""
	}
	if idem := strings.TrimSpace(pos.IdemPrefix); idem != "" {
		return symbol + "|" + idem
	}
	if !pos.OpenedAt.IsZero() {
		return fmt.Sprintf("%s|%s|%s|%d", symbol, strings.TrimSpace(pos.SpotVenue), strings.TrimSpace(pos.PerpVenue), pos.OpenedAt.UTC().UnixNano())
	}
	return symbol + "|" + strings.TrimSpace(pos.SpotVenue) + "|" + strings.TrimSpace(pos.PerpVenue)
}

func cvspSamePosition(a, b CrossVenueSpotPerpPosition) bool {
	return cvspPositionKey(a) == cvspPositionKey(b)
}

func cvspHasTrackedLeg(ps []CrossVenueSpotPerpPosition, venueName, symbol string) bool {
	wantVenue := strings.TrimSpace(venueName)
	wantSymbol := strings.TrimSpace(symbol)
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if p.Symbol != wantSymbol {
			continue
		}
		if strings.TrimSpace(p.SpotVenue) == wantVenue || strings.TrimSpace(p.PerpVenue) == wantVenue {
			return true
		}
	}
	return false
}

func buildCrossVenueSpotPerpSettlement(pos CrossVenueSpotPerpPosition, closeTime time.Time) CrossVenueSpotPerpSettlement {
	openFees := pos.OpenFeesUSDT
	closeFees := pos.CloseFeesUSDT
	totalFees := openFees + closeFees
	realizedFunding := pos.PerpLegFundingUSDT
	netPnL := realizedFunding - totalFees - pos.RollbackCostUSDT
	return CrossVenueSpotPerpSettlement{
		SettlementID:    fmt.Sprintf("cvsp-%s-%d", pos.Symbol, closeTime.UTC().UnixNano()),
		PositionKey:     cvspPositionKey(pos),
		OpportunityID:   pos.OpportunityID,
		OpenTime:        pos.OpenedAt,
		CloseTime:       closeTime,
		Symbol:          pos.Symbol,
		SpotVenue:       pos.SpotVenue,
		PerpVenue:       pos.PerpVenue,
		NotionalUSDT:    pos.NotionalUSDT,
		PerpLegFunding:  pos.PerpLegFundingUSDT,
		RealizedFunding: realizedFunding,
		OpenFees:        openFees,
		CloseFees:       closeFees,
		RollbackCost:    pos.RollbackCostUSDT,
		TotalFees:       totalFees,
		NetPnL:          netPnL,
		CloseReason:     pos.CloseReason,
	}
}
