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

	"lingxi/internal/venue"
)

// cross_venue_positions_store.go 跨所对冲持仓台账原子落盘(records/crossvenue/positions.json),
// 用于跨重启 reconcile 与状态机追踪。

const crossVenuePositionsFileName = "positions.json"

// CrossVenuePositionStatus 是跨所持仓状态机。
type CrossVenuePositionStatus string

const (
	CrossVenueStatusOpeningFirstLeg  CrossVenuePositionStatus = "opening_first_leg"
	CrossVenueStatusOpeningSecondLeg CrossVenuePositionStatus = "opening_second_leg"
	CrossVenueStatusOpen             CrossVenuePositionStatus = "open"
	CrossVenueStatusClosing          CrossVenuePositionStatus = "closing"
	CrossVenueStatusClosed           CrossVenuePositionStatus = "closed"
	CrossVenueStatusOrphanSingleLeg  CrossVenuePositionStatus = "orphan_single_leg"
)

// CrossVenueLegState 是一条腿的成交快照。
type CrossVenueLegState struct {
	Venue      string   `json:"venue"`
	Side       string   `json:"side"` // buy / sell
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"` // 平仓加权均价(生命周期内累加)
	ClosedQty  float64  `json:"closed_qty,omitempty"` // 累计平仓成交量(reduce/close 累加)
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// CrossVenueAttribution 跨所持仓生命周期内的净利润归因原始累加(随 positions.json 持久化)。
type CrossVenueAttribution struct {
	OpenFeesUSDT      float64 `json:"open_fees_usdt,omitempty"`
	CloseFeesUSDT     float64 `json:"close_fees_usdt,omitempty"`
	OpenSlippageUSDT  float64 `json:"open_slippage_usdt,omitempty"`
	CloseSlippageUSDT float64 `json:"close_slippage_usdt,omitempty"`
	RollbackCostUSDT  float64 `json:"rollback_cost_usdt,omitempty"`
	RebalanceCostUSDT float64 `json:"rebalance_cost_usdt,omitempty"`
}

// CrossVenuePosition 是一笔跨所 perp-perp 对冲持仓(空高费所、多低费所)。
type CrossVenuePosition struct {
	PositionKey          string             `json:"position_key,omitempty"`
	Symbol               string             `json:"symbol"`
	HighVenue            string             `json:"high_venue"`
	LowVenue             string             `json:"low_venue"`
	HighLeg              CrossVenueLegState `json:"high_leg"`
	LowLeg               CrossVenueLegState `json:"low_leg"`
	DecisionCycleID      string             `json:"decision_cycle_id,omitempty"`
	EntryDecisionCycleID string             `json:"entry_decision_cycle_id,omitempty"`
	EntrySnapshotID      string             `json:"entry_snapshot_id,omitempty"`
	// OpportunityID 统一轮动机会 ID(开仓时写入);旧台账无此字段时由 crossVenuePositionOpportunityID 稳定派生。
	OpportunityID        string  `json:"opportunity_id,omitempty"`
	CurrentSnapshotID    string  `json:"current_snapshot_id,omitempty"`
	OpenSpreadAPR        float64 `json:"open_spread_apr"`
	EstFundingUSDT       float64 `json:"est_funding_usdt"`
	EstimatedFundingUSDT float64 `json:"estimated_funding_usdt,omitempty"`
	// HighLegFundingUSDT 高费腿(空头)真实已结算资金费(正);LowLegFundingUSDT 低费腿(多头)真实已结算资金费(负)。
	// 由 syncRealizedFunding 从交易所流水覆盖写入;EstFundingUSDT = 两腿真实值 + 轮间估算展示增量。
	HighLegFundingUSDT            float64                  `json:"high_leg_funding_usdt,omitempty"`
	LowLegFundingUSDT             float64                  `json:"low_leg_funding_usdt,omitempty"`
	RealizedFundingComplete       bool                     `json:"realized_funding_complete,omitempty"`
	FundingTruthStatus            string                   `json:"funding_truth_status,omitempty"`
	HighFundingRate               float64                  `json:"high_funding_rate,omitempty"`
	LowFundingRate                float64                  `json:"low_funding_rate,omitempty"`
	HighIntervalHours             int                      `json:"high_interval_hours,omitempty"`
	LowIntervalHours              int                      `json:"low_interval_hours,omitempty"`
	HighNextFundingTime           time.Time                `json:"high_next_funding_time,omitempty"`
	LowNextFundingTime            time.Time                `json:"low_next_funding_time,omitempty"`
	CurrentComparableWindowHours  float64                  `json:"current_comparable_window_hours,omitempty"`
	CurrentFutureEV               float64                  `json:"current_future_ev,omitempty"`
	CurrentCapitalEfficiencyEV    float64                  `json:"current_capital_efficiency_ev,omitempty"`
	CurrentRiskAdjustedEVPerHour  float64                  `json:"current_risk_adjusted_ev_per_hour,omitempty"`
	CurrentRiskScore              float64                  `json:"current_risk_score,omitempty"`
	EstimatedExitCost             float64                  `json:"estimated_exit_cost,omitempty"`
	EstimatedSwitchCost           float64                  `json:"estimated_switch_cost,omitempty"`
	FreshnessAgeSeconds           float64                  `json:"freshness_age_seconds,omitempty"`
	HistoryQualityScore           float64                  `json:"history_quality_score,omitempty"`
	LiquidityScore                float64                  `json:"liquidity_score,omitempty"`
	StabilityScore                float64                  `json:"stability_score,omitempty"`
	ExecutionScore                float64                  `json:"execution_score,omitempty"`
	RecoveryQualityScore          float64                  `json:"recovery_quality_score,omitempty"`
	RankScore                     float64                  `json:"rank_score,omitempty"`
	SizeMultiplier                float64                  `json:"size_multiplier,omitempty"`
	EntryOpenMode                 string                   `json:"entry_open_mode,omitempty"`
	UsedFallback                  bool                     `json:"used_fallback,omitempty"`
	ChurnStreak                   int                      `json:"churn_streak,omitempty"`
	CloseFailStreak               int                      `json:"close_fail_streak,omitempty"`
	SwitchCooldownUntil           time.Time                `json:"switch_cooldown_until,omitempty"`
	PendingSwitchDecisionCycleID  string                   `json:"pending_switch_decision_cycle_id,omitempty"`
	PendingSwitchSnapshotID       string                   `json:"pending_switch_snapshot_id,omitempty"`
	PendingSwitchHighVenue        string                   `json:"pending_switch_high_venue,omitempty"`
	PendingSwitchLowVenue         string                   `json:"pending_switch_low_venue,omitempty"`
	PendingSwitchSequence         string                   `json:"pending_switch_sequence,omitempty"`
	PendingSwitchReason           string                   `json:"pending_switch_reason,omitempty"`
	PendingSwitchExpectedGainUSDT float64                  `json:"pending_switch_expected_gain_usdt,omitempty"`
	PartialSwitchPhase            string                   `json:"partial_switch_phase,omitempty"`
	TransitionState               string                   `json:"transition_state,omitempty"`
	Priority                      float64                  `json:"priority,omitempty"`
	RecoveryAction                string                   `json:"recovery_action,omitempty"`
	Status                        CrossVenuePositionStatus `json:"status"`
	OpenedAt                      time.Time                `json:"opened_at"`
	UpdatedAt                     time.Time                `json:"updated_at"`
	CloseSpreadStreak             int                      `json:"close_spread_streak,omitempty"`
	CloseHardNegativeSpreadStreak int                      `json:"close_hard_negative_spread_streak,omitempty"`
	IdemPrefix                    string                   `json:"idem_prefix"`
	NotionalUSDT                  float64                  `json:"notional_usdt,omitempty"`
	// LastAccrualAt 上次资金费记账时刻;每轮按 now−LastAccrualAt 的真实小时数折算累加(零值=自 OpenedAt 起算)。
	LastAccrualAt time.Time `json:"last_accrual_at,omitempty"`
	// LastRealizedFundingAt 上次成功拉取真实结算资费的时刻;节流 RealizedFundingPollMinutes 或跨结算周期再拉。
	LastRealizedFundingAt time.Time `json:"last_realized_funding_at,omitempty"`
	// CloseAttempts 平仓尝试轮数(IOC 未平干净时保持 closing 状态跨轮重试;≥3 轮起 critical 告警,退避推送)。
	CloseAttempts int `json:"close_attempts,omitempty"`
	// CloseReason 首次触发平仓的原因(跨轮重试时沿用,日志/告警可追溯)。
	CloseReason string `json:"close_reason,omitempty"`
	// AttributionIncomplete 平仓/回滚/再平衡成交量无法可靠归一化为币数量(如 gate/okx ContractSize lookup 失败)。
	AttributionIncomplete bool `json:"attribution_incomplete,omitempty"`
	// CrossVenueAttribution 开/平/回滚/再平衡阶段的费用与滑点观测累加(供平仓结算归因)。
	// 【关键】必须匿名内嵌(而非命名字段):命名的结构体值字段 omitempty 对 struct 无效,零值会
	// 序列化成 "attribution":{},令旧台账(无该键)重算 state_envelope 校验和失配而拒绝自动接管在场实盘仓;
	// 内嵌则把子字段平铺进本结构,各自 omitempty 生效,旧仓零值全省略 → 与旧文件字节一致 → 校验和匹配。
	CrossVenueAttribution
}

// crossVenuePositionsEnvelope 是带信封头的台账。
type crossVenuePositionsEnvelope struct {
	SchemaVersion int                  `json:"schema_version"`
	Checksum      string               `json:"checksum"`
	UpdatedAt     string               `json:"updated_at"`
	Positions     []CrossVenuePosition `json:"positions"`
}

func (e *crossVenuePositionsEnvelope) getSchemaVersion() int  { return e.SchemaVersion }
func (e *crossVenuePositionsEnvelope) setSchemaVersion(v int) { e.SchemaVersion = v }
func (e *crossVenuePositionsEnvelope) getChecksum() string    { return e.Checksum }
func (e *crossVenuePositionsEnvelope) setChecksum(c string)   { e.Checksum = c }

type crossVenuePositionStore struct {
	mu   sync.Mutex
	path string
}

func crossVenueRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "crossvenue")
}

func newCrossVenuePositionStore(recordDir string) (*crossVenuePositionStore, error) {
	dir := crossVenueRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &crossVenuePositionStore{path: filepath.Join(dir, crossVenuePositionsFileName)}, nil
}

func (s *crossVenuePositionStore) load() ([]CrossVenuePosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *crossVenuePositionStore) loadLocked() ([]CrossVenuePosition, error) {
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
		var out []CrossVenuePosition
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		normalizeCrossVenuePositions(out)
		return out, nil
	}
	var env crossVenuePositionsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("跨所持仓台账", &env); err != nil {
		return nil, err
	}
	normalizeCrossVenuePositions(env.Positions)
	return env.Positions, nil
}

func (s *crossVenuePositionStore) saveLocked(ps []CrossVenuePosition) error {
	normalizeCrossVenuePositions(ps)
	env := crossVenuePositionsEnvelope{
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

func (s *crossVenuePositionStore) saveAll(ps []CrossVenuePosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(ps)
}

func (s *crossVenuePositionStore) upsert(p CrossVenuePosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	crossVenueEnsurePositionKey(&p)
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range ps {
		if crossVenueSamePosition(ps[i], p) {
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

func (s *crossVenuePositionStore) remove(positionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]CrossVenuePosition, 0, len(ps))
	positionKey = strings.TrimSpace(positionKey)
	for _, p := range ps {
		if crossVenuePositionKey(p) != positionKey {
			out = append(out, p)
		}
	}
	return s.saveLocked(out)
}

// activeCrossVenuePositions 返回非 closed 状态的持仓。
func activeCrossVenuePositions(ps []CrossVenuePosition) []CrossVenuePosition {
	out := make([]CrossVenuePosition, 0, len(ps))
	for _, p := range ps {
		if p.Status != CrossVenueStatusClosed {
			out = append(out, p)
		}
	}
	return out
}

func crossVenuePositionBySymbol(ps []CrossVenuePosition, sym venue.Symbol) (CrossVenuePosition, bool) {
	for _, p := range ps {
		if p.Symbol == string(sym) {
			return p, true
		}
	}
	return CrossVenuePosition{}, false
}

func normalizeCrossVenuePositions(ps []CrossVenuePosition) {
	for i := range ps {
		crossVenueEnsurePositionKey(&ps[i])
	}
}

func crossVenueEnsurePositionKey(pos *CrossVenuePosition) {
	if pos == nil {
		return
	}
	if strings.TrimSpace(pos.PositionKey) != "" {
		pos.PositionKey = strings.TrimSpace(pos.PositionKey)
		return
	}
	pos.PositionKey = crossVenueDerivePositionKey(*pos)
}

func crossVenuePositionKey(pos CrossVenuePosition) string {
	if key := strings.TrimSpace(pos.PositionKey); key != "" {
		return key
	}
	return crossVenueDerivePositionKey(pos)
}

func crossVenueDerivePositionKey(pos CrossVenuePosition) string {
	symbol := strings.TrimSpace(pos.Symbol)
	if symbol == "" {
		return ""
	}
	if idem := strings.TrimSpace(pos.IdemPrefix); idem != "" {
		return symbol + "|" + idem
	}
	if !pos.OpenedAt.IsZero() {
		return fmt.Sprintf("%s|%s|%s|%d", symbol, strings.TrimSpace(pos.HighVenue), strings.TrimSpace(pos.LowVenue), pos.OpenedAt.UTC().UnixNano())
	}
	if cycleID := strings.TrimSpace(pos.EntryDecisionCycleID); cycleID != "" {
		return symbol + "|" + strings.TrimSpace(pos.HighVenue) + "|" + strings.TrimSpace(pos.LowVenue) + "|" + cycleID
	}
	return symbol + "|" + strings.TrimSpace(pos.HighVenue) + "|" + strings.TrimSpace(pos.LowVenue)
}

func crossVenueSamePosition(a, b CrossVenuePosition) bool {
	return crossVenuePositionKey(a) == crossVenuePositionKey(b)
}

func crossVenueHasTrackedLeg(ps []CrossVenuePosition, venueName string, sym venue.Symbol) bool {
	wantVenue := strings.TrimSpace(venueName)
	for _, p := range activeCrossVenuePositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.TrimSpace(p.HighVenue) == wantVenue || strings.TrimSpace(p.LowVenue) == wantVenue {
			return true
		}
	}
	return false
}

func crossVenueHasActivePositionTarget(ps []CrossVenuePosition, symbol, highVenue, lowVenue, excludeKey string) bool {
	excludeKey = strings.TrimSpace(excludeKey)
	for _, p := range activeCrossVenuePositions(ps) {
		if excludeKey != "" && crossVenuePositionKey(p) == excludeKey {
			continue
		}
		if !strings.EqualFold(p.Symbol, symbol) {
			continue
		}
		if !strings.EqualFold(p.HighVenue, highVenue) || !strings.EqualFold(p.LowVenue, lowVenue) {
			continue
		}
		return true
	}
	return false
}
