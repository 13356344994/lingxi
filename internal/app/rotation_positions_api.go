package app

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

// rotation_positions_api.go 五轮动策略持仓/结算只读视图,供 /api/rotation/positions 与 /api/rotation/settlements。

const rotationPositionPriceFetchTimeout = 3 * time.Second

// RotationPositionLegView 统一描述一条腿。
type RotationPositionLegView struct {
	Venue                string   `json:"venue"`
	Market               string   `json:"market"`
	MarketType           string   `json:"market_type"` // spot / perp
	Side                 string   `json:"side"`
	Qty                  float64  `json:"qty"`
	EntryPrice           float64  `json:"entry_price"`
	CurrentPrice         float64  `json:"current_price,omitempty"`
	LegUnrealizedPnLUSDT *float64 `json:"leg_unrealized_pnl_usdt,omitempty"`
	PriceUnavailable     bool     `json:"price_unavailable,omitempty"`
	ExitPrice            float64  `json:"exit_price,omitempty"`
	OrderIDs             []string `json:"order_ids,omitempty"`
}

// RotationPositionView 五轮动策略持仓的统一视图。
type RotationPositionView struct {
	StrategyType          string                  `json:"strategy_type"`
	PositionKey           string                  `json:"position_key,omitempty"`
	OpportunityID         string                  `json:"opportunity_id,omitempty"`
	Symbol                string                  `json:"symbol"`
	Venues                []string                `json:"venues,omitempty"`
	Status                string                  `json:"status"`
	SpotLeg               RotationPositionLegView `json:"spot_leg"`
	PerpLeg               RotationPositionLegView `json:"perp_leg"`
	NotionalUSDT          float64                 `json:"notional_usdt"`
	OpenFeesUSDT          float64                 `json:"open_fees_usdt"`
	RealizedCloseFeesUSDT float64                 `json:"realized_close_fees_usdt,omitempty"`
	ExpectedCloseFeesUSDT float64                 `json:"expected_close_fees_usdt"`
	ExpectedCloseCostUSDT float64                 `json:"expected_close_cost_usdt"`
	ExpectedNetEVUSDT     float64                 `json:"expected_net_ev_usdt"`
	ExpectedFundingUSDT   float64                 `json:"expected_funding_usdt"`
	AccruedFundingUSDT    float64                 `json:"accrued_funding_usdt"`
	TotalFeesUSDT         float64                 `json:"total_fees_usdt"`
	TotalPnLUSDT          *float64                `json:"total_pnl_usdt,omitempty"`
	PnLRatio              *float64                `json:"pnl_ratio,omitempty"`
	OccupiedCapitalUSDT   float64                 `json:"occupied_capital_usdt,omitempty"`
	PriceUnavailable      bool                    `json:"price_unavailable,omitempty"`
	OpenedAt              time.Time               `json:"opened_at"`
	UpdatedAt             time.Time               `json:"updated_at,omitempty"`
	ClosedAt              time.Time               `json:"closed_at,omitempty"`
	CloseReason           string                  `json:"close_reason,omitempty"`
	TransitionState       string                  `json:"transition_state,omitempty"`
	SwitchLineage         string                  `json:"switch_lineage,omitempty"`
	HoldDurationSec       float64                 `json:"hold_duration_sec,omitempty"`
}

// RotationSettlementView 五轮动策略结算的统一视图(历史/已平仓)。
type RotationSettlementView struct {
	StrategyType    string    `json:"strategy_type"`
	SettlementID    string    `json:"settlement_id,omitempty"`
	PositionKey     string    `json:"position_key,omitempty"`
	OpportunityID   string    `json:"opportunity_id,omitempty"`
	Symbol          string    `json:"symbol"`
	Venues          []string  `json:"venues,omitempty"`
	OpenTime        time.Time `json:"open_time"`
	CloseTime       time.Time `json:"close_time"`
	NotionalUSDT    float64   `json:"notional_usdt"`
	NetPnL          float64   `json:"net_pnl"`
	RealizedFunding float64   `json:"realized_funding"`
	OpenFees        float64   `json:"open_fees"`
	CloseFees       float64   `json:"close_fees"`
	TotalFees       float64   `json:"total_fees"`
	PnLRatio        float64   `json:"pnl_ratio,omitempty"`
	BorrowCost      float64   `json:"borrow_cost,omitempty"`
	RollbackCost    float64   `json:"rollback_cost,omitempty"`
	CloseReason     string    `json:"close_reason,omitempty"`
	SwitchExit      bool      `json:"switch_exit,omitempty"`
	SwitchSequence  string    `json:"switch_sequence,omitempty"`
	HoldDurationSec float64   `json:"hold_duration_sec,omitempty"`
}

// RotationPositionsAPIResponse GET /api/rotation/positions 响应。
type RotationPositionsAPIResponse struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Status      string                 `json:"status_filter"`
	Strategy    string                 `json:"strategy_filter,omitempty"`
	Count       int                    `json:"count"`
	Positions   []RotationPositionView `json:"positions"`
}

// RotationSettlementsAPIResponse GET /api/rotation/settlements 响应。
type RotationSettlementsAPIResponse struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Since       time.Time                `json:"since,omitempty"`
	Limit       int                      `json:"limit"`
	Count       int                      `json:"count"`
	Settlements []RotationSettlementView `json:"settlements"`
	Summary     StrategyPnLSummary       `json:"summary"`
}

func normalizeRotationStatus(rawStatus, closeReason, transitionState string) string {
	reason := strings.ToLower(strings.TrimSpace(closeReason))
	if strings.Contains(reason, "dust") {
		return "dust-closed"
	}
	st := strings.ToLower(strings.TrimSpace(string(rawStatus)))
	switch {
	case st == "closed":
		return "closed"
	case strings.Contains(st, "orphan"):
		return "orphan"
	case strings.Contains(st, "closing"):
		return "closing"
	case st == "open" || strings.Contains(st, "opening"):
		return "open"
	case st != "":
		return st
	}
	if ts := strings.TrimSpace(transitionState); ts != "" {
		return ts
	}
	return "unknown"
}

func rotationSwitchLineage(closeReason, transitionState string) string {
	parts := make([]string, 0, 2)
	if ts := strings.TrimSpace(transitionState); ts != "" {
		parts = append(parts, ts)
	}
	reason := strings.ToLower(strings.TrimSpace(closeReason))
	if strings.Contains(reason, "switch") || strings.Contains(reason, "rotation") || strings.Contains(reason, "replace") {
		parts = append(parts, strings.TrimSpace(closeReason))
	}
	return strings.Join(parts, " | ")
}

func holdDurationSec(opened, ref time.Time) float64 {
	if opened.IsZero() || ref.IsZero() || ref.Before(opened) {
		return 0
	}
	return ref.Sub(opened).Seconds()
}

func viewFromSVSP(pos SingleVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig, now time.Time) RotationPositionView {
	cont := rotationContinuationFromPosition(pos.EstFundingUSDT, pos.NotionalUSDT, execCfg, false)
	venues := uniqueNonEmptyStrings(pos.Venue)
	return RotationPositionView{
		StrategyType:          UnifiedStrategySingleVenueSpotPerp,
		PositionKey:           svspPositionKey(pos),
		OpportunityID:         pos.OpportunityID,
		Symbol:                pos.Symbol,
		Venues:                venues,
		Status:                normalizeRotationStatus(string(pos.Status), pos.CloseReason, pos.TransitionState),
		SpotLeg:               rotationSpotLegView(pos.Venue, pos.SpotLeg.Market, pos.SpotLeg.Side, pos.SpotLeg.Qty, pos.SpotLeg.EntryPrice, pos.SpotLeg.ExitPrice, pos.SpotLeg.OrderIDs),
		PerpLeg:               rotationPerpLegView(pos.Venue, pos.PerpLeg.Market, pos.PerpLeg.Side, pos.PerpLeg.Qty, pos.PerpLeg.EntryPrice, pos.PerpLeg.ExitPrice, pos.PerpLeg.OrderIDs),
		NotionalUSDT:          pos.NotionalUSDT,
		OpenFeesUSDT:          pos.OpenFeesUSDT,
		RealizedCloseFeesUSDT: pos.CloseFeesUSDT,
		ExpectedCloseFeesUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedCloseCostUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedNetEVUSDT:     pos.OpenExpectedNetEV,
		ExpectedFundingUSDT:   pos.EstFundingUSDT,
		AccruedFundingUSDT:    pos.RealizedFundingUSDT,
		OpenedAt:              pos.OpenedAt,
		UpdatedAt:             pos.UpdatedAt,
		CloseReason:           pos.CloseReason,
		TransitionState:       pos.TransitionState,
		SwitchLineage:         rotationSwitchLineage(pos.CloseReason, pos.TransitionState),
		HoldDurationSec:       holdDurationSec(pos.OpenedAt, now),
	}
}

func viewFromCVSP(pos CrossVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig, now time.Time) RotationPositionView {
	cont := rotationContinuationFromPosition(pos.EstFundingUSDT, pos.NotionalUSDT, execCfg, false)
	return RotationPositionView{
		StrategyType:          UnifiedStrategyCrossVenueSpotPerp,
		PositionKey:           cvspPositionKey(pos),
		OpportunityID:         pos.OpportunityID,
		Symbol:                pos.Symbol,
		Venues:                uniqueNonEmptyStrings(pos.SpotVenue, pos.PerpVenue),
		Status:                normalizeRotationStatus(string(pos.Status), pos.CloseReason, pos.TransitionState),
		SpotLeg:               rotationSpotLegView(pos.SpotVenue, pos.SpotLeg.Market, pos.SpotLeg.Side, pos.SpotLeg.Qty, pos.SpotLeg.EntryPrice, pos.SpotLeg.ExitPrice, pos.SpotLeg.OrderIDs),
		PerpLeg:               rotationPerpLegView(pos.PerpVenue, pos.PerpLeg.Market, pos.PerpLeg.Side, pos.PerpLeg.Qty, pos.PerpLeg.EntryPrice, pos.PerpLeg.ExitPrice, pos.PerpLeg.OrderIDs),
		NotionalUSDT:          pos.NotionalUSDT,
		OpenFeesUSDT:          pos.OpenFeesUSDT,
		RealizedCloseFeesUSDT: pos.CloseFeesUSDT,
		ExpectedCloseFeesUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedCloseCostUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedNetEVUSDT:     pos.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:   pos.ExpectedFundingUSDT,
		AccruedFundingUSDT:    pos.PerpLegFundingUSDT,
		OpenedAt:              pos.OpenedAt,
		UpdatedAt:             pos.UpdatedAt,
		CloseReason:           pos.CloseReason,
		TransitionState:       pos.TransitionState,
		SwitchLineage:         rotationSwitchLineage(pos.CloseReason, pos.TransitionState),
		HoldDurationSec:       holdDurationSec(pos.OpenedAt, now),
	}
}

func viewFromSVPS(pos SingleVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig, now time.Time) RotationPositionView {
	cont := rotationContinuationFromPosition(pos.EstFundingUSDT, pos.NotionalUSDT, execCfg, true)
	venues := uniqueNonEmptyStrings(pos.Venue)
	return RotationPositionView{
		StrategyType:          UnifiedStrategySingleVenuePerpSpot,
		PositionKey:           svpsPositionKey(pos),
		OpportunityID:         pos.OpportunityID,
		Symbol:                pos.Symbol,
		Venues:                venues,
		Status:                normalizeRotationStatus(string(pos.Status), pos.CloseReason, pos.TransitionState),
		SpotLeg:               rotationSpotLegView(pos.Venue, pos.SpotLeg.Market, pos.SpotLeg.Side, pos.SpotLeg.Qty, pos.SpotLeg.EntryPrice, pos.SpotLeg.ExitPrice, pos.SpotLeg.OrderIDs),
		PerpLeg:               rotationPerpLegView(pos.Venue, pos.PerpLeg.Market, pos.PerpLeg.Side, pos.PerpLeg.Qty, pos.PerpLeg.EntryPrice, pos.PerpLeg.ExitPrice, pos.PerpLeg.OrderIDs),
		NotionalUSDT:          pos.NotionalUSDT,
		OpenFeesUSDT:          pos.OpenFeesUSDT,
		RealizedCloseFeesUSDT: pos.CloseFeesUSDT,
		ExpectedCloseFeesUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedCloseCostUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedNetEVUSDT:     pos.OpenExpectedNetEV,
		ExpectedFundingUSDT:   pos.EstFundingUSDT,
		AccruedFundingUSDT:    pos.RealizedFundingUSDT,
		OpenedAt:              pos.OpenedAt,
		UpdatedAt:             pos.UpdatedAt,
		CloseReason:           pos.CloseReason,
		TransitionState:       pos.TransitionState,
		SwitchLineage:         rotationSwitchLineage(pos.CloseReason, pos.TransitionState),
		HoldDurationSec:       holdDurationSec(pos.OpenedAt, now),
	}
}

func viewFromCVPS(pos CrossVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig, now time.Time) RotationPositionView {
	cont := rotationContinuationFromPosition(pos.EstFundingUSDT, pos.NotionalUSDT, execCfg, true)
	return RotationPositionView{
		StrategyType:          UnifiedStrategyCrossVenuePerpSpot,
		PositionKey:           cvpsPositionKey(pos),
		OpportunityID:         pos.OpportunityID,
		Symbol:                pos.Symbol,
		Venues:                uniqueNonEmptyStrings(pos.SpotVenue, pos.PerpVenue),
		Status:                normalizeRotationStatus(string(pos.Status), pos.CloseReason, pos.TransitionState),
		SpotLeg:               rotationSpotLegView(pos.SpotVenue, pos.SpotLeg.Market, pos.SpotLeg.Side, pos.SpotLeg.Qty, pos.SpotLeg.EntryPrice, pos.SpotLeg.ExitPrice, pos.SpotLeg.OrderIDs),
		PerpLeg:               rotationPerpLegView(pos.PerpVenue, pos.PerpLeg.Market, pos.PerpLeg.Side, pos.PerpLeg.Qty, pos.PerpLeg.EntryPrice, pos.PerpLeg.ExitPrice, pos.PerpLeg.OrderIDs),
		NotionalUSDT:          pos.NotionalUSDT,
		OpenFeesUSDT:          pos.OpenFeesUSDT,
		RealizedCloseFeesUSDT: pos.CloseFeesUSDT,
		ExpectedCloseFeesUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedCloseCostUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedNetEVUSDT:     pos.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:   pos.ExpectedFundingUSDT,
		AccruedFundingUSDT:    pos.PerpLegFundingUSDT,
		OpenedAt:              pos.OpenedAt,
		UpdatedAt:             pos.UpdatedAt,
		CloseReason:           pos.CloseReason,
		TransitionState:       pos.TransitionState,
		SwitchLineage:         rotationSwitchLineage(pos.CloseReason, pos.TransitionState),
		HoldDurationSec:       holdDurationSec(pos.OpenedAt, now),
	}
}

// viewFromCrossVenuePerpPerp 把 CrossVenueExecutor 在场 perp-perp 持仓映射为统一 rotation 视图。
// spot_leg/perp_leg 槽位均填永续腿(高费所空腿→spot_leg,低费所多腿→perp_leg),与 carry 四策略同 JSON 结构。
func viewFromCrossVenuePerpPerp(pos CrossVenuePosition, execCfg CrossVenueExecutorConfig, now time.Time) RotationPositionView {
	notional := crossVenuePositionNotionalUSDT(&pos)
	cont := rotationContinuationFromPosition(pos.EstFundingUSDT, notional, execCfg, false)
	estCloseFees := crossVenueEstCloseFeeUSDT(execCfg, &pos)
	return RotationPositionView{
		StrategyType:          UnifiedStrategyPerpPerp,
		PositionKey:           crossVenuePositionKey(pos),
		OpportunityID:         crossVenuePositionOpportunityID(pos),
		Symbol:                pos.Symbol,
		Venues:                uniqueNonEmptyStrings(pos.HighVenue, pos.LowVenue),
		Status:                normalizeRotationStatus(string(pos.Status), pos.CloseReason, pos.TransitionState),
		SpotLeg:               crossVenuePerpLegView(pos.HighVenue, pos.HighLeg),
		PerpLeg:               crossVenuePerpLegView(pos.LowVenue, pos.LowLeg),
		NotionalUSDT:          notional,
		OpenFeesUSDT:          pos.OpenFeesUSDT,
		RealizedCloseFeesUSDT: pos.CloseFeesUSDT,
		ExpectedCloseFeesUSDT: estCloseFees,
		ExpectedCloseCostUSDT: cont.ExpectedCloseCostUSDT,
		ExpectedNetEVUSDT:     pos.CurrentFutureEV,
		ExpectedFundingUSDT:   pos.EstFundingUSDT,
		AccruedFundingUSDT:    money.SumUSDT(pos.HighLegFundingUSDT, pos.LowLegFundingUSDT),
		OpenedAt:              pos.OpenedAt,
		UpdatedAt:             pos.UpdatedAt,
		CloseReason:           pos.CloseReason,
		TransitionState:       pos.TransitionState,
		SwitchLineage:         rotationSwitchLineage(pos.CloseReason, pos.TransitionState),
		HoldDurationSec:       holdDurationSec(pos.OpenedAt, now),
	}
}

func crossVenuePerpLegView(venueName string, leg CrossVenueLegState) RotationPositionLegView {
	v := strings.TrimSpace(venueName)
	if v == "" {
		v = strings.TrimSpace(leg.Venue)
	}
	return rotationPerpLegView(v, "perp", leg.Side, leg.Qty, leg.EntryPrice, leg.ExitPrice, leg.OrderIDs)
}

// listCrossVenuePositionsForAPI 按 status 过滤跨所 perp-perp 台账(只读,供 rotation positions 聚合)。
func listCrossVenuePositionsForAPI(e *CrossVenueExecutor, statusFilter string) []CrossVenuePosition {
	if e == nil {
		return nil
	}
	ps := e.PositionsSnapshot().Positions
	switch strings.ToLower(strings.TrimSpace(statusFilter)) {
	case "closed":
		out := make([]CrossVenuePosition, 0)
		for _, p := range ps {
			if p.Status == CrossVenueStatusClosed {
				out = append(out, p)
			}
		}
		return out
	case "all":
		return ps
	default:
		return activeCrossVenuePositions(ps)
	}
}

func settlementViewFromRecord(rec strategySettlementRecord) RotationSettlementView {
	view := RotationSettlementView{
		StrategyType:    rec.StrategyType,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpportunityID:   rec.OpportunityID,
		Symbol:          rec.Symbol,
		Venues:          rec.Venues,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		BorrowCost:      rec.BorrowCost,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
		SwitchExit:      rec.SwitchExit,
		SwitchSequence:  rec.SwitchSequence,
		HoldDurationSec: holdDurationSec(rec.OpenTime, rec.CloseTime),
	}
	if rec.NotionalUSDT > 0 {
		view.PnLRatio = rec.NetPnL / rec.NotionalUSDT
	}
	return view
}

func rotationSpotLegView(venueName, market, side string, qty, entryPrice, exitPrice float64, orderIDs []string) RotationPositionLegView {
	return rotationPositionLegView(venueName, market, "spot", side, qty, entryPrice, exitPrice, orderIDs)
}

func rotationPerpLegView(venueName, market, side string, qty, entryPrice, exitPrice float64, orderIDs []string) RotationPositionLegView {
	return rotationPositionLegView(venueName, market, "perp", side, qty, entryPrice, exitPrice, orderIDs)
}

func rotationPositionLegView(venueName, market, marketType, side string, qty, entryPrice, exitPrice float64, orderIDs []string) RotationPositionLegView {
	return RotationPositionLegView{
		Venue:      venueName,
		Market:     market,
		MarketType: marketType,
		Side:       side,
		Qty:        qty,
		EntryPrice: entryPrice,
		ExitPrice:  exitPrice,
		OrderIDs:   orderIDs,
	}
}

func rotationLegUnrealizedPnLUSDT(side string, qty, entryPrice, currentPrice float64) float64 {
	if qty <= 0 || entryPrice <= 0 || currentPrice <= 0 {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy", "long":
		return (currentPrice - entryPrice) * qty
	case "sell", "short":
		return (entryPrice - currentPrice) * qty
	default:
		return 0
	}
}

func rotationPositionOccupiedCapital(notional float64) float64 {
	if notional > 0 {
		return notional
	}
	return 0
}

func rotationPositionTotalPnLUSDT(spotPnL, perpPnL, accruedFunding, openFees, expectedCloseFees float64) float64 {
	return money.SumUSDT(spotPnL, perpPnL, accruedFunding, -openFees, -expectedCloseFees)
}

func rotationPositionPnLRatio(totalPnL, occupiedCapital float64) *float64 {
	if occupiedCapital <= 0 {
		return nil
	}
	ratio := totalPnL / occupiedCapital
	return &ratio
}

func isOpenRotationPositionForPricing(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "open" || strings.Contains(status, "opening") || status == "closing" || status == "orphan"
}

func rotationPositionNeedsLivePrices(statusFilter string) bool {
	switch strings.ToLower(strings.TrimSpace(statusFilter)) {
	case "", "all", "open":
		return true
	default:
		return false
	}
}

func collectRotationDisplayVenues(s *DashboardServer) map[string]venue.Venue {
	out := make(map[string]venue.Venue)
	if s == nil {
		return out
	}
	mergeVenueMap := func(src map[string]venue.Venue) {
		for name, v := range src {
			if v == nil {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" || out[key] != nil {
				continue
			}
			out[key] = v
		}
	}
	if s.crossVenueExecutor != nil {
		mergeVenueMap(s.crossVenueExecutor.venues)
	}
	if s.svspExec != nil {
		mergeVenueMap(s.svspExec.venues)
	}
	if s.cvspExec != nil {
		mergeVenueMap(s.cvspExec.venues)
	}
	if s.svpsExec != nil {
		mergeVenueMap(s.svpsExec.venues)
	}
	if s.cvpsExec != nil {
		mergeVenueMap(s.cvpsExec.venues)
	}
	if s.orchestrator != nil {
		for _, name := range crossVenueNames {
			key := strings.ToLower(strings.TrimSpace(name))
			if out[key] != nil {
				continue
			}
			if adp := crossVenueAdapterFromOrchestrator(s.orchestrator, name); adp != nil {
				out[key] = adp
			}
		}
	}
	return out
}

func enrichOpenRotationPositionViewsWithLivePrices(ctx context.Context, s *DashboardServer, views []RotationPositionView) {
	venues := collectRotationDisplayVenues(s)
	if len(venues) == 0 {
		return
	}
	for i := range views {
		if !isOpenRotationPositionForPricing(views[i].Status) {
			continue
		}
		enrichOneRotationPositionWithLivePrices(ctx, venues, &views[i])
	}
}

func enrichOneRotationPositionWithLivePrices(ctx context.Context, venues map[string]venue.Venue, view *RotationPositionView) {
	if view == nil {
		return
	}
	view.TotalFeesUSDT = money.SumUSDT(view.OpenFeesUSDT, view.ExpectedCloseFeesUSDT)
	view.OccupiedCapitalUSDT = rotationPositionOccupiedCapital(view.NotionalUSDT)

	var wg sync.WaitGroup
	enrichLeg := func(leg *RotationPositionLegView) {
		if leg == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := venues[strings.ToLower(strings.TrimSpace(leg.Venue))]
			mid, ok := unifiedShadowFetchLegMidPrice(ctx, v, leg.MarketType, view.Symbol)
			if !ok {
				leg.PriceUnavailable = true
				view.PriceUnavailable = true
				return
			}
			leg.CurrentPrice = mid
			pnl := rotationLegUnrealizedPnLUSDT(leg.Side, leg.Qty, leg.EntryPrice, mid)
			leg.LegUnrealizedPnLUSDT = &pnl
		}()
	}
	enrichLeg(&view.SpotLeg)
	enrichLeg(&view.PerpLeg)
	wg.Wait()

	if view.SpotLeg.LegUnrealizedPnLUSDT == nil || view.PerpLeg.LegUnrealizedPnLUSDT == nil {
		return
	}
	total := rotationPositionTotalPnLUSDT(
		*view.SpotLeg.LegUnrealizedPnLUSDT,
		*view.PerpLeg.LegUnrealizedPnLUSDT,
		view.AccruedFundingUSDT,
		view.OpenFeesUSDT,
		view.ExpectedCloseFeesUSDT,
	)
	view.TotalPnLUSDT = &total
	view.PnLRatio = rotationPositionPnLRatio(total, view.OccupiedCapitalUSDT)
}

func buildRotationPositionViews(s *DashboardServer, execCfg CrossVenueExecutorConfig, statusFilter, strategyFilter string) []RotationPositionView {
	now := time.Now().UTC()
	var out []RotationPositionView

	if s.svspExec != nil && strategyMatches(strategyFilter, UnifiedStrategySingleVenueSpotPerp) {
		for _, pos := range s.svspExec.listPositionsForAPI(statusFilter) {
			out = append(out, viewFromSVSP(pos, execCfg, now))
		}
	}
	if s.cvspExec != nil && strategyMatches(strategyFilter, UnifiedStrategyCrossVenueSpotPerp) {
		for _, pos := range s.cvspExec.listPositionsForAPI(statusFilter) {
			out = append(out, viewFromCVSP(pos, execCfg, now))
		}
	}
	if s.svpsExec != nil && strategyMatches(strategyFilter, UnifiedStrategySingleVenuePerpSpot) {
		for _, pos := range s.svpsExec.listPositionsForAPI(statusFilter) {
			out = append(out, viewFromSVPS(pos, execCfg, now))
		}
	}
	if s.cvpsExec != nil && strategyMatches(strategyFilter, UnifiedStrategyCrossVenuePerpSpot) {
		for _, pos := range s.cvpsExec.listPositionsForAPI(statusFilter) {
			out = append(out, viewFromCVPS(pos, execCfg, now))
		}
	}
	if s.crossVenueExecutor != nil && strategyMatches(strategyFilter, UnifiedStrategyPerpPerp) {
		for _, pos := range listCrossVenuePositionsForAPI(s.crossVenueExecutor, statusFilter) {
			out = append(out, viewFromCrossVenuePerpPerp(pos, execCfg, now))
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].OpenedAt.After(out[j].OpenedAt)
	})
	return out
}

func strategyMatches(filter, strategyType string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	return strings.EqualFold(filter, strategyType)
}

func positionStatusMatches(filter, status string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	status = strings.ToLower(strings.TrimSpace(status))
	switch filter {
	case "", "all":
		return true
	case "open":
		return status == "open" || strings.Contains(status, "opening") || status == "closing" || status == "orphan"
	case "closed":
		return status == "closed" || status == "dust-closed"
	default:
		return filter == status
	}
}

func (s *DashboardServer) handleRotationPositionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if statusFilter == "" {
		statusFilter = "open"
	}
	strategyFilter := strings.TrimSpace(r.URL.Query().Get("strategy"))
	views := buildRotationPositionViews(s, rotationExecCfg(s), statusFilter, strategyFilter)
	if statusFilter != "all" {
		filtered := make([]RotationPositionView, 0, len(views))
		for _, v := range views {
			if positionStatusMatches(statusFilter, v.Status) {
				filtered = append(filtered, v)
			}
		}
		views = filtered
	}
	if rotationPositionNeedsLivePrices(statusFilter) {
		ctx, cancel := context.WithTimeout(r.Context(), rotationPositionPriceFetchTimeout)
		defer cancel()
		enrichOpenRotationPositionViewsWithLivePrices(ctx, s, views)
	}
	writeJSON(w, RotationPositionsAPIResponse{
		GeneratedAt: time.Now().UTC(),
		Status:      statusFilter,
		Strategy:    strategyFilter,
		Count:       len(views),
		Positions:   views,
	})
}

func parseSinceQuery(r *http.Request) time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func (s *DashboardServer) handleRotationSettlementsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	since := parseSinceQuery(r)
	limit := parseLimit(r, 100)
	now := time.Now().UTC()

	records, err := loadRotationStrategySettlementRecords(s, since, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]RotationSettlementView, 0, len(records))
	for _, rec := range records {
		views = append(views, settlementViewFromRecord(rec))
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].CloseTime.After(views[j].CloseTime)
	})

	summary := aggregateStrategySettlementsInRange(records, since, now)
	writeJSON(w, RotationSettlementsAPIResponse{
		GeneratedAt: now,
		Since:       since,
		Limit:       limit,
		Count:       len(views),
		Settlements: views,
		Summary:     summary,
	})
}

func loadRotationStrategySettlementRecords(s *DashboardServer, since time.Time, limit int) ([]strategySettlementRecord, error) {
	dir := resolveStrategyRecordDir(s)
	var out []strategySettlementRecord

	if s.svspExec != nil {
		recs, err := s.svspExec.ListRecentSettlements(limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, svspSettlementToRecord(rec))
		}
	} else {
		recs, err := loadTailJSONLFile[SingleVenueSpotPerpSettlement](filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName), since, limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, svspSettlementToRecord(rec))
		}
	}

	if s.cvspExec != nil {
		recs, err := s.cvspExec.ListRecentSettlements(limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, cvspSettlementToRecord(rec))
		}
	} else {
		recs, err := loadTailJSONLFile[CrossVenueSpotPerpSettlement](filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile), since, limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, cvspSettlementToRecord(rec))
		}
	}

	if s.svpsExec != nil {
		recs, err := s.svpsExec.ListRecentSettlements(limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, svpsSettlementToRecord(rec))
		}
	} else {
		recs, err := loadTailJSONLFile[SingleVenuePerpSpotSettlement](filepath.Join(singleVenuePerpSpotRecordDir(dir), svpsSettlementsLogName), since, limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, svpsSettlementToRecord(rec))
		}
	}

	if s.cvpsExec != nil {
		recs, err := s.cvpsExec.ListRecentSettlements(limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, cvpsSettlementToRecord(rec))
		}
	} else {
		recs, err := loadTailJSONLFile[CrossVenuePerpSpotSettlement](filepath.Join(crossVenuePerpSpotRecordDir(dir), crossVenuePerpSpotSettlementsFile), since, limit)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, cvpsSettlementToRecord(rec))
		}
	}

	if since.IsZero() {
		return tailStrategyRecords(out, limit), nil
	}
	filtered := make([]strategySettlementRecord, 0, len(out))
	for _, rec := range out {
		if rec.CloseTime.Before(since) {
			continue
		}
		filtered = append(filtered, rec)
	}
	return tailStrategyRecords(filtered, limit), nil
}

func tailStrategyRecords(records []strategySettlementRecord, limit int) []strategySettlementRecord {
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func sumRotationOpenNotionalUSDT(s *DashboardServer) float64 {
	var total float64
	for _, v := range buildRotationPositionViews(s, rotationExecCfg(s), "open", "") {
		total = money.SumUSDT(total, v.NotionalUSDT)
	}
	return total
}
