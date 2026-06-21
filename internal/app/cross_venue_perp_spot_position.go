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

	"lingxi/internal/money"
)

// cross_venue_perp_spot_position.go 跨所 perp-spot 负费率持仓台账(records/cross_venue_perp_spot/)。

const (
	crossVenuePerpSpotPositionsFile   = "positions.json"
	crossVenuePerpSpotSettlementsFile = "settlements.jsonl"
)

// CrossVenuePerpSpotPositionStatus 是跨所 perp-spot 负 carry 持仓状态机。
type CrossVenuePerpSpotPositionStatus string

const (
	CVPSStatusOpeningFirstLeg  CrossVenuePerpSpotPositionStatus = "opening_first_leg"
	CVPSStatusOpeningSecondLeg CrossVenuePerpSpotPositionStatus = "opening_second_leg"
	CVPSStatusOpen             CrossVenuePerpSpotPositionStatus = "open"
	CVPSStatusClosing          CrossVenuePerpSpotPositionStatus = "closing"
	CVPSStatusClosed           CrossVenuePerpSpotPositionStatus = "closed"
	CVPSStatusOrphanSingleLeg  CrossVenuePerpSpotPositionStatus = "orphan_single_leg"
)

// CrossVenuePerpSpotLegState 是一条腿的成交快照。
type CrossVenuePerpSpotLegState struct {
	Venue      string   `json:"venue"`
	Market     string   `json:"market"` // spot / perp
	Side       string   `json:"side"`   // long / short / buy / sell
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"`
	ClosedQty  float64  `json:"closed_qty,omitempty"`
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// CrossVenuePerpSpotPosition 是一笔跨所 perp-spot 负费率持仓(A 所借币空现货 + B 所多永续)。
// 强平缓冲:空现货在逼空/强平场景下亏损可能穿仓,执行器保守杠杆并优先平现货腿。
type CrossVenuePerpSpotPosition struct {
	PositionKey          string                           `json:"position_key,omitempty"`
	Symbol               string                           `json:"symbol"`
	SpotVenue            string                           `json:"spot_venue"`
	PerpVenue            string                           `json:"perp_venue"`
	SpotLeg              CrossVenuePerpSpotLegState       `json:"spot_leg"`
	PerpLeg              CrossVenuePerpSpotLegState       `json:"perp_leg"`
	OpportunityID        string                           `json:"opportunity_id,omitempty"`
	ExpectedFundingUSDT  float64                          `json:"expected_funding_usdt,omitempty"`
	ExpectedNetEVUSDT    float64                          `json:"expected_net_ev_usdt,omitempty"`
	ExpectedHoldHours    float64                          `json:"expected_hold_hours,omitempty"`
	PerpFundingRate      float64                          `json:"perp_funding_rate,omitempty"`
	PerpIntervalHours    int                              `json:"perp_interval_hours,omitempty"`
	PerpNextFundingTime  time.Time                        `json:"perp_next_funding_time,omitempty"`
	PerpLegFundingUSDT   float64                          `json:"perp_leg_funding_usdt,omitempty"`
	EstFundingUSDT       float64                          `json:"est_funding_usdt,omitempty"`
	Status               CrossVenuePerpSpotPositionStatus `json:"status"`
	TransitionState      string                           `json:"transition_state,omitempty"`
	IdemPrefix           string                           `json:"idem_prefix"`
	NotionalUSDT         float64                          `json:"notional_usdt,omitempty"`
	OpenedAt             time.Time                        `json:"opened_at"`
	UpdatedAt            time.Time                        `json:"updated_at"`
	LastAccrualAt        time.Time                        `json:"last_accrual_at,omitempty"`
	CloseReason          string                           `json:"close_reason,omitempty"`
	CloseAttempts        int                              `json:"close_attempts,omitempty"`
	LastHedgeRebalanceAt time.Time                        `json:"last_hedge_rebalance_at,omitempty"`
	OpenFeesUSDT         float64                          `json:"open_fees_usdt,omitempty"`
	CloseFeesUSDT        float64                          `json:"close_fees_usdt,omitempty"`
	RollbackCostUSDT     float64                          `json:"rollback_cost_usdt,omitempty"`
	BorrowCostUSDT       float64                          `json:"borrow_cost_usdt,omitempty"`
	DustResidualCostUSDT float64                          `json:"dust_residual_cost_usdt,omitempty"`
}

// CrossVenuePerpSpotSettlement 是单仓平仓结算记录(含借币成本)。
type CrossVenuePerpSpotSettlement struct {
	SettlementID        string    `json:"settlement_id,omitempty"`
	PositionKey         string    `json:"position_key,omitempty"`
	OpportunityID       string    `json:"opportunity_id,omitempty"`
	OpenTime            time.Time `json:"open_time"`
	CloseTime           time.Time `json:"close_time"`
	Symbol              string    `json:"symbol"`
	SpotVenue           string    `json:"spot_venue"`
	PerpVenue           string    `json:"perp_venue"`
	NotionalUSDT        float64   `json:"notional_usdt"`
	ExpectedFundingUSDT float64   `json:"expected_funding_usdt,omitempty"`
	PerpIntervalHours   int       `json:"perp_interval_hours,omitempty"`
	PerpLegFunding      float64   `json:"perp_leg_funding"`
	RealizedFunding     float64   `json:"realized_funding"`
	BorrowCost          float64   `json:"borrow_cost"`
	OpenFees            float64   `json:"open_fees"`
	CloseFees           float64   `json:"close_fees"`
	RollbackCost        float64   `json:"rollback_cost"`
	DustResidualCost    float64   `json:"dust_residual_cost,omitempty"`
	TradePnL            float64   `json:"trade_pnl"`
	TotalFees           float64   `json:"total_fees"`
	NetPnL              float64   `json:"net_pnl"`
	CloseReason         string    `json:"close_reason,omitempty"`
}

type crossVenuePerpSpotPositionsEnvelope struct {
	SchemaVersion int                          `json:"schema_version"`
	Checksum      string                       `json:"checksum"`
	UpdatedAt     string                       `json:"updated_at"`
	Positions     []CrossVenuePerpSpotPosition `json:"positions"`
}

func (e *crossVenuePerpSpotPositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *crossVenuePerpSpotPositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *crossVenuePerpSpotPositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *crossVenuePerpSpotPositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type crossVenuePerpSpotPositionStore struct {
	mu   sync.Mutex
	dir  string
	path string
}

func crossVenuePerpSpotRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "cross_venue_perp_spot")
}

func newCrossVenuePerpSpotPositionStore(recordDir string) (*crossVenuePerpSpotPositionStore, error) {
	dir := crossVenuePerpSpotRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &crossVenuePerpSpotPositionStore{
		dir:  dir,
		path: filepath.Join(dir, crossVenuePerpSpotPositionsFile),
	}, nil
}

func (s *crossVenuePerpSpotPositionStore) load() ([]CrossVenuePerpSpotPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *crossVenuePerpSpotPositionStore) loadLocked() ([]CrossVenuePerpSpotPosition, error) {
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
		var out []CrossVenuePerpSpotPosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		normalizeCrossVenuePerpSpotPositions(out)
		return out, nil
	}
	var env crossVenuePerpSpotPositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("跨所perp-spot持仓台账", &env); err != nil {
		return nil, err
	}
	normalizeCrossVenuePerpSpotPositions(env.Positions)
	return env.Positions, nil
}

func (s *crossVenuePerpSpotPositionStore) saveLocked(ps []CrossVenuePerpSpotPosition) error {
	normalizeCrossVenuePerpSpotPositions(ps)
	env := crossVenuePerpSpotPositionsEnvelope{
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

func (s *crossVenuePerpSpotPositionStore) upsert(p CrossVenuePerpSpotPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cvpsEnsurePositionKey(&p)
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if cvpsSamePosition(ps[i], p) {
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

func (s *crossVenuePerpSpotPositionStore) remove(positionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]CrossVenuePerpSpotPosition, 0, len(ps))
	positionKey = strings.TrimSpace(positionKey)
	for _, p := range ps {
		if cvpsPositionKey(p) != positionKey {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}

func (s *crossVenuePerpSpotPositionStore) appendSettlement(rec CrossVenuePerpSpotSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, crossVenuePerpSpotSettlementsFile)
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

func activeCrossVenuePerpSpotPositions(ps []CrossVenuePerpSpotPosition) []CrossVenuePerpSpotPosition {
	out := make([]CrossVenuePerpSpotPosition, 0, len(ps))
	for _, p := range ps {
		if p.Status != CVPSStatusClosed {
			out = append(out, p)
		}
	}
	return out
}

func normalizeCrossVenuePerpSpotPositions(ps []CrossVenuePerpSpotPosition) {
	for i := range ps {
		cvpsEnsurePositionKey(&ps[i])
		if ps[i].PerpIntervalHours <= 0 {
			ps[i].PerpIntervalHours = 8
		}
	}
}

func cvpsEnsurePositionKey(pos *CrossVenuePerpSpotPosition) {
	if pos == nil {
		return
	}
	if strings.TrimSpace(pos.PositionKey) != "" {
		pos.PositionKey = strings.TrimSpace(pos.PositionKey)
		return
	}
	pos.PositionKey = cvpsDerivePositionKey(*pos)
}

func cvpsPositionKey(pos CrossVenuePerpSpotPosition) string {
	if key := strings.TrimSpace(pos.PositionKey); key != "" {
		return key
	}
	return cvpsDerivePositionKey(pos)
}

func cvpsDerivePositionKey(pos CrossVenuePerpSpotPosition) string {
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

func cvpsSamePosition(a, b CrossVenuePerpSpotPosition) bool {
	return cvpsPositionKey(a) == cvpsPositionKey(b)
}

func cvpsHasTrackedLeg(ps []CrossVenuePerpSpotPosition, venueName, symbol string) bool {
	wantVenue := strings.TrimSpace(venueName)
	wantSymbol := strings.TrimSpace(symbol)
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if p.Symbol != wantSymbol {
			continue
		}
		if strings.TrimSpace(p.SpotVenue) == wantVenue || strings.TrimSpace(p.PerpVenue) == wantVenue {
			return true
		}
	}
	return false
}

func computeCrossVenuePerpSpotTradePnL(pos CrossVenuePerpSpotPosition) float64 {
	var total float64
	if pos.SpotLeg.ClosedQty > executionQtyTolerance && pos.SpotLeg.ExitPrice > 0 {
		total += (pos.SpotLeg.EntryPrice - pos.SpotLeg.ExitPrice) * pos.SpotLeg.ClosedQty
	}
	if pos.PerpLeg.ClosedQty > executionQtyTolerance && pos.PerpLeg.ExitPrice > 0 {
		total += (pos.PerpLeg.ExitPrice - pos.PerpLeg.EntryPrice) * pos.PerpLeg.ClosedQty
	}
	return total
}

func buildCrossVenuePerpSpotSettlement(pos CrossVenuePerpSpotPosition, closeTime time.Time) CrossVenuePerpSpotSettlement {
	openFees := pos.OpenFeesUSDT
	closeFees := pos.CloseFeesUSDT
	totalFees := money.SumUSDT(openFees, closeFees)
	realizedFunding := pos.PerpLegFundingUSDT
	tradePnL := computeCrossVenuePerpSpotTradePnL(pos)
	// net_pnl 口径对齐 cross_venue_settlement.go / cost_accounting.go:滑点已隐含在 trade_pnl,不得再扣。
	netPnL := money.SumUSDT(realizedFunding, tradePnL, -totalFees, -pos.RollbackCostUSDT, -pos.BorrowCostUSDT, -pos.DustResidualCostUSDT)
	return CrossVenuePerpSpotSettlement{
		SettlementID:        fmt.Sprintf("cvps-%s-%d", pos.Symbol, closeTime.UTC().UnixNano()),
		PositionKey:         cvpsPositionKey(pos),
		OpportunityID:       pos.OpportunityID,
		OpenTime:            pos.OpenedAt,
		CloseTime:           closeTime,
		Symbol:              pos.Symbol,
		SpotVenue:           pos.SpotVenue,
		PerpVenue:           pos.PerpVenue,
		NotionalUSDT:        pos.NotionalUSDT,
		ExpectedFundingUSDT: pos.ExpectedFundingUSDT,
		PerpIntervalHours:   pos.PerpIntervalHours,
		PerpLegFunding:      pos.PerpLegFundingUSDT,
		RealizedFunding:     realizedFunding,
		BorrowCost:          pos.BorrowCostUSDT,
		OpenFees:            openFees,
		CloseFees:           closeFees,
		RollbackCost:        pos.RollbackCostUSDT,
		DustResidualCost:    pos.DustResidualCostUSDT,
		TradePnL:            tradePnL,
		TotalFees:           totalFees,
		NetPnL:              netPnL,
		CloseReason:         pos.CloseReason,
	}
}
