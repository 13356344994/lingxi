package risk

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	microUSDTScale int64 = 1_000_000

	// EquityTruthMatchWindow 限制资金流和权益变化只能在相近事件时间内配对。
	EquityTruthMatchWindow = 15 * time.Minute
	// EquityTruthMinCandidate 是权益变化进入异步候选队列的最小绝对金额。
	// 小于该值的普通逐 tick PnL 立即按交易权益处理，不制造永久 pending。
	EquityTruthMinCandidate MicroUSDT = MicroUSDT(5 * microUSDTScale)
)

// MicroUSDT 是 1e-6 USDT 的定点金额单位。
type MicroUSDT int64

// MicroUSDTFromFloat64 把浮点 USDT 金额转换为定点微 USDT(四舍五入)。
func MicroUSDTFromFloat64(v float64) MicroUSDT {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return MicroUSDT(math.Round(v * float64(microUSDTScale)))
}

// Float64 把定点微 USDT 金额转换回浮点 USDT。
func (m MicroUSDT) Float64() float64 {
	return float64(m) / float64(microUSDTScale)
}

func (m MicroUSDT) abs() MicroUSDT {
	if m < 0 {
		return -m
	}
	return m
}

// FundFlowEvent 是充提等资金流事件(充值为正,提现为负)。
type FundFlowEvent struct {
	EventID      string    `json:"event_id"`
	Venue        string    `json:"venue,omitempty"`
	Amount       MicroUSDT `json:"amount_micro_usdt"`
	ExchangeTime time.Time `json:"exchange_time,omitempty"`
	ReceivedAt   time.Time `json:"received_at,omitempty"`
}

// EquityObservation 是交易所权益观测(原始现金口径)。
type EquityObservation struct {
	ObservationID string    `json:"observation_id"`
	Venue         string    `json:"venue,omitempty"`
	RawEquity     MicroUSDT `json:"raw_equity_micro_usdt"`
	ExchangeTime  time.Time `json:"exchange_time,omitempty"`
	ReceivedAt    time.Time `json:"received_at,omitempty"`
}

// PendingFlow 表示尚未被权益变化确认的资金流。
type PendingFlow struct {
	Flow      FundFlowEvent `json:"flow"`
	Remaining MicroUSDT     `json:"remaining_micro_usdt"`
}

// PendingEquity 表示尚未被资金流解释的权益变化。
type PendingEquity struct {
	Observation    EquityObservation `json:"observation"`
	RemainingDelta MicroUSDT         `json:"remaining_delta_micro_usdt"`
}

// EquityTruthSnapshot 是账本的可持久化状态。
type EquityTruthSnapshot struct {
	Venue                     string          `json:"venue,omitempty"`
	Initialized               bool            `json:"initialized"`
	LastRawEquity             MicroUSDT       `json:"last_raw_equity_micro_usdt"`
	ConfirmedFlowTotal        MicroUSDT       `json:"confirmed_flow_total_micro_usdt"`
	HighWaterCleanFrontier    MicroUSDT       `json:"high_water_clean_frontier_micro_usdt"`
	FrontierInitialized       bool            `json:"frontier_initialized"`
	PendingFlows              []PendingFlow   `json:"pending_flows,omitempty"`
	PendingEquityObservations []PendingEquity `json:"pending_equity_observations,omitempty"`
	SeenFlowIDs               []string        `json:"seen_flow_ids,omitempty"`
	SeenObservationIDs        []string        `json:"seen_observation_ids,omitempty"`
}

// EquityTruthView 是当前权益真相视图。
type EquityTruthView struct {
	Venue                  string          `json:"venue,omitempty"`
	Initialized            bool            `json:"initialized"`
	RawEquity              MicroUSDT       `json:"raw_equity_micro_usdt"`
	TradingEquity          MicroUSDT       `json:"trading_equity_micro_usdt"`
	SolvencyEquity         MicroUSDT       `json:"solvency_equity_micro_usdt"`
	ConfirmedFlowTotal     MicroUSDT       `json:"confirmed_flow_total_micro_usdt"`
	PendingOutflowReserve  MicroUSDT       `json:"pending_outflow_reserve_micro_usdt"`
	HighWaterCleanFrontier MicroUSDT       `json:"high_water_clean_frontier_micro_usdt"`
	DrawdownFromFrontier   MicroUSDT       `json:"drawdown_from_frontier_micro_usdt"`
	PendingFlowCount       int             `json:"pending_flow_count"`
	PendingEquityCount     int             `json:"pending_equity_count"`
	HasPending             bool            `json:"has_pending"`
	PendingFlows           []PendingFlow   `json:"pending_flows,omitempty"`
	PendingEquity          []PendingEquity `json:"pending_equity,omitempty"`
}

// EquityTruthLedger 维护资金流与权益观测的双向异步匹配账本。
type EquityTruthLedger struct {
	venue              string
	initialized        bool
	lastRawEquity      MicroUSDT
	confirmedFlowTotal MicroUSDT

	highWaterCleanFrontier MicroUSDT
	frontierInitialized    bool

	pendingFlows  []PendingFlow
	pendingEquity []PendingEquity

	seenFlowIDs map[string]struct{}
	seenObsIDs  map[string]struct{}
}

// NewEquityTruthLedger 创建一个空账本。
func NewEquityTruthLedger(venue string) *EquityTruthLedger {
	return &EquityTruthLedger{
		venue:       strings.TrimSpace(venue),
		seenFlowIDs: make(map[string]struct{}),
		seenObsIDs:  make(map[string]struct{}),
	}
}

// Restore 用快照恢复账本状态。
func (l *EquityTruthLedger) Restore(s EquityTruthSnapshot) {
	if l == nil {
		return
	}
	if strings.TrimSpace(s.Venue) != "" {
		l.venue = strings.TrimSpace(s.Venue)
	}
	l.initialized = s.Initialized
	l.lastRawEquity = s.LastRawEquity
	l.confirmedFlowTotal = s.ConfirmedFlowTotal
	l.highWaterCleanFrontier = s.HighWaterCleanFrontier
	l.frontierInitialized = s.FrontierInitialized
	l.seenFlowIDs = make(map[string]struct{}, len(s.SeenFlowIDs)+len(s.PendingFlows))
	for _, id := range s.SeenFlowIDs {
		if id = strings.TrimSpace(id); id != "" {
			l.seenFlowIDs[id] = struct{}{}
		}
	}
	l.seenObsIDs = make(map[string]struct{}, len(s.SeenObservationIDs)+len(s.PendingEquityObservations))
	for _, id := range s.SeenObservationIDs {
		if id = strings.TrimSpace(id); id != "" {
			l.seenObsIDs[id] = struct{}{}
		}
	}

	l.pendingFlows = l.pendingFlows[:0]
	for _, p := range s.PendingFlows {
		if p.Remaining == 0 || !l.acceptsVenue(p.Flow.Venue) {
			continue
		}
		l.pendingFlows = append(l.pendingFlows, p)
		if id := strings.TrimSpace(p.Flow.EventID); id != "" {
			l.seenFlowIDs[id] = struct{}{}
		}
	}

	l.pendingEquity = l.pendingEquity[:0]
	for _, p := range s.PendingEquityObservations {
		if p.RemainingDelta == 0 || !l.acceptsVenue(p.Observation.Venue) {
			continue
		}
		l.pendingEquity = append(l.pendingEquity, p)
		if id := strings.TrimSpace(p.Observation.ObservationID); id != "" {
			l.seenObsIDs[id] = struct{}{}
		}
	}
}

// Snapshot 导出账本状态用于落盘。
func (l *EquityTruthLedger) Snapshot() EquityTruthSnapshot {
	if l == nil {
		return EquityTruthSnapshot{}
	}
	seenFlowIDs := mapKeysSorted(l.seenFlowIDs)
	seenObservationIDs := mapKeysSorted(l.seenObsIDs)
	return EquityTruthSnapshot{
		Venue:                     l.venue,
		Initialized:               l.initialized,
		LastRawEquity:             l.lastRawEquity,
		ConfirmedFlowTotal:        l.confirmedFlowTotal,
		HighWaterCleanFrontier:    l.highWaterCleanFrontier,
		FrontierInitialized:       l.frontierInitialized,
		PendingFlows:              append([]PendingFlow(nil), l.pendingFlows...),
		PendingEquityObservations: append([]PendingEquity(nil), l.pendingEquity...),
		SeenFlowIDs:               seenFlowIDs,
		SeenObservationIDs:        seenObservationIDs,
	}
}

// EnsureCleanFrontierAtLeast 用于兼容旧状态迁移，避免高水位倒退。
func (l *EquityTruthLedger) EnsureCleanFrontierAtLeast(v MicroUSDT) {
	if l == nil {
		return
	}
	if !l.frontierInitialized || v > l.highWaterCleanFrontier {
		l.highWaterCleanFrontier = v
		l.frontierInitialized = true
	}
}

// ReanchorCleanFrontier 在已确认的观测盲区后把交易净值峰值重锚到当前干净前沿。
// 它只改变绩效峰值，不改变原始权益、已确认资金流或偿付权益。
func (l *EquityTruthLedger) ReanchorCleanFrontier(v MicroUSDT) {
	if l == nil {
		return
	}
	l.highWaterCleanFrontier = v
	l.frontierInitialized = true
}

// ApplyFundFlow 录入资金流事件(充值正,提现负)。
func (l *EquityTruthLedger) ApplyFundFlow(ev FundFlowEvent) EquityTruthView {
	if l == nil {
		return EquityTruthView{}
	}
	ev.Venue = l.normalizeVenue(ev.Venue)
	if !l.acceptsVenue(ev.Venue) {
		return l.view()
	}
	ev.EventID = strings.TrimSpace(ev.EventID)
	if ev.EventID != "" {
		if _, ok := l.seenFlowIDs[ev.EventID]; ok {
			return l.view()
		}
		l.seenFlowIDs[ev.EventID] = struct{}{}
	}
	if ev.Amount == 0 {
		return l.view()
	}

	eventTime := effectiveEventTime(ev.ExchangeTime, ev.ReceivedAt)
	l.expirePendingBefore(eventTime)
	remaining := ev.Amount
	for i := 0; i < len(l.pendingEquity) && remaining != 0; {
		if !sameVenue(ev.Venue, l.pendingEquity[i].Observation.Venue) ||
			!withinMatchWindow(eventTime, effectiveEventTime(
				l.pendingEquity[i].Observation.ExchangeTime,
				l.pendingEquity[i].Observation.ReceivedAt,
			)) {
			i++
			continue
		}
		match := signedMatch(remaining, l.pendingEquity[i].RemainingDelta)
		if match == 0 {
			i++
			continue
		}
		l.confirmedFlowTotal += match
		remaining -= match
		l.pendingEquity[i].RemainingDelta -= match
		if l.pendingEquity[i].RemainingDelta == 0 {
			l.pendingEquity = append(l.pendingEquity[:i], l.pendingEquity[i+1:]...)
			continue
		}
		if l.pendingEquity[i].RemainingDelta.abs() < EquityTruthMinCandidate {
			l.pendingEquity = append(l.pendingEquity[:i], l.pendingEquity[i+1:]...)
			continue
		}
		i++
	}
	if remaining != 0 {
		l.pendingFlows = append(l.pendingFlows, PendingFlow{
			Flow:      ev,
			Remaining: remaining,
		})
	}
	return l.view()
}

// ApplyEquityObservation 录入权益观测(原始现金口径)。
func (l *EquityTruthLedger) ApplyEquityObservation(obs EquityObservation) EquityTruthView {
	if l == nil {
		return EquityTruthView{}
	}
	obs.Venue = l.normalizeVenue(obs.Venue)
	if !l.acceptsVenue(obs.Venue) {
		return l.view()
	}
	obs.ObservationID = strings.TrimSpace(obs.ObservationID)
	if obs.ObservationID != "" {
		if _, ok := l.seenObsIDs[obs.ObservationID]; ok {
			return l.view()
		}
		l.seenObsIDs[obs.ObservationID] = struct{}{}
	}
	eventTime := effectiveEventTime(obs.ExchangeTime, obs.ReceivedAt)
	l.expirePendingBefore(eventTime)

	if !l.initialized {
		l.initialized = true
		l.lastRawEquity = obs.RawEquity
		return l.view()
	}

	delta := obs.RawEquity - l.lastRawEquity
	l.lastRawEquity = obs.RawEquity
	if delta == 0 {
		return l.view()
	}

	remaining := delta
	for i := 0; i < len(l.pendingFlows) && remaining != 0; {
		if !sameVenue(obs.Venue, l.pendingFlows[i].Flow.Venue) ||
			!withinMatchWindow(eventTime, effectiveEventTime(
				l.pendingFlows[i].Flow.ExchangeTime,
				l.pendingFlows[i].Flow.ReceivedAt,
			)) {
			i++
			continue
		}
		match := signedMatch(remaining, l.pendingFlows[i].Remaining)
		if match == 0 {
			i++
			continue
		}
		l.confirmedFlowTotal += match
		remaining -= match
		l.pendingFlows[i].Remaining -= match
		if l.pendingFlows[i].Remaining == 0 {
			l.pendingFlows = append(l.pendingFlows[:i], l.pendingFlows[i+1:]...)
			continue
		}
		i++
	}
	if remaining.abs() >= EquityTruthMinCandidate && !eventTime.IsZero() {
		l.pendingEquity = append(l.pendingEquity, PendingEquity{
			Observation:    obs,
			RemainingDelta: remaining,
		})
	}
	return l.view()
}

// ExpirePending 显式推进账本时间，使超出匹配窗的候选不再保护回撤或参与未来配对。
func (l *EquityTruthLedger) ExpirePending(now time.Time) EquityTruthView {
	if l == nil {
		return EquityTruthView{}
	}
	l.expirePendingBefore(now)
	return l.view()
}

// View 返回当前视图，不修改输入事件序列。
func (l *EquityTruthLedger) View() EquityTruthView {
	return l.view()
}

func (l *EquityTruthLedger) view() EquityTruthView {
	if l == nil {
		return EquityTruthView{}
	}
	raw := MicroUSDT(0)
	if l.initialized {
		raw = l.lastRawEquity
	}
	trading := raw - l.confirmedFlowTotal
	if !l.frontierInitialized && l.initialized {
		l.highWaterCleanFrontier = trading
		l.frontierInitialized = true
	}
	hasPending := len(l.pendingFlows) > 0 || len(l.pendingEquity) > 0
	if !hasPending && l.frontierInitialized && trading > l.highWaterCleanFrontier {
		l.highWaterCleanFrontier = trading
	}
	drawdown := MicroUSDT(0)
	if l.frontierInitialized && trading < l.highWaterCleanFrontier {
		drawdown = l.highWaterCleanFrontier - trading
	}

	pendingOutflow := MicroUSDT(0)
	for _, p := range l.pendingFlows {
		if p.Remaining < 0 {
			pendingOutflow += (-p.Remaining)
		}
	}
	solvency := raw - pendingOutflow

	return EquityTruthView{
		Venue:                  l.venue,
		Initialized:            l.initialized,
		RawEquity:              raw,
		TradingEquity:          trading,
		SolvencyEquity:         solvency,
		ConfirmedFlowTotal:     l.confirmedFlowTotal,
		PendingOutflowReserve:  pendingOutflow,
		HighWaterCleanFrontier: l.highWaterCleanFrontier,
		DrawdownFromFrontier:   drawdown,
		PendingFlowCount:       len(l.pendingFlows),
		PendingEquityCount:     len(l.pendingEquity),
		HasPending:             hasPending,
		PendingFlows:           append([]PendingFlow(nil), l.pendingFlows...),
		PendingEquity:          append([]PendingEquity(nil), l.pendingEquity...),
	}
}

func (l *EquityTruthLedger) expirePendingBefore(now time.Time) {
	if l == nil || now.IsZero() {
		return
	}
	l.pendingFlows = retainFreshPendingFlows(l.pendingFlows, now)
	l.pendingEquity = retainFreshPendingEquity(l.pendingEquity, now)
}

func retainFreshPendingFlows(pending []PendingFlow, now time.Time) []PendingFlow {
	out := pending[:0]
	for _, p := range pending {
		ts := effectiveEventTime(p.Flow.ExchangeTime, p.Flow.ReceivedAt)
		if ts.IsZero() || now.Sub(ts) > EquityTruthMatchWindow {
			continue
		}
		out = append(out, p)
	}
	return out
}

func retainFreshPendingEquity(pending []PendingEquity, now time.Time) []PendingEquity {
	out := pending[:0]
	for _, p := range pending {
		ts := effectiveEventTime(p.Observation.ExchangeTime, p.Observation.ReceivedAt)
		if ts.IsZero() || now.Sub(ts) > EquityTruthMatchWindow {
			continue
		}
		out = append(out, p)
	}
	return out
}

func effectiveEventTime(exchangeTime, receivedAt time.Time) time.Time {
	if !exchangeTime.IsZero() {
		return exchangeTime.UTC()
	}
	if !receivedAt.IsZero() {
		return receivedAt.UTC()
	}
	return time.Time{}
}

func withinMatchWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= EquityTruthMatchWindow
}

func sameVenue(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a != "" && a == b
}

func (l *EquityTruthLedger) normalizeVenue(venue string) string {
	venue = strings.TrimSpace(venue)
	if venue == "" {
		return l.venue
	}
	if l.venue == "" {
		l.venue = venue
	}
	return venue
}

func (l *EquityTruthLedger) acceptsVenue(venue string) bool {
	venue = strings.TrimSpace(venue)
	return venue != "" && (l.venue == "" || venue == l.venue)
}

func mapKeysSorted(src map[string]struct{}) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, 0, len(src))
	for id := range src {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func signedMatch(a, b MicroUSDT) MicroUSDT {
	if a == 0 || b == 0 {
		return 0
	}
	if (a > 0 && b < 0) || (a < 0 && b > 0) {
		return 0
	}
	ma := a.abs()
	mb := b.abs()
	if ma > mb {
		ma = mb
	}
	if a < 0 {
		return -ma
	}
	return ma
}
