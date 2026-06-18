package app

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/money"
)

// rotation_positions_api.go 四轮动策略持仓/结算只读视图,供 /api/rotation/positions 与 /api/rotation/settlements。

// RotationPositionLegView 统一描述一条腿。
type RotationPositionLegView struct {
	Venue      string   `json:"venue"`
	Market     string   `json:"market"`
	Side       string   `json:"side"`
	Qty        float64  `json:"qty"`
	EntryPrice float64  `json:"entry_price"`
	ExitPrice  float64  `json:"exit_price,omitempty"`
	OrderIDs   []string `json:"order_ids,omitempty"`
}

// RotationPositionView 四轮动策略持仓的统一视图。
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
	OpenedAt              time.Time               `json:"opened_at"`
	UpdatedAt             time.Time               `json:"updated_at,omitempty"`
	ClosedAt              time.Time               `json:"closed_at,omitempty"`
	CloseReason           string                  `json:"close_reason,omitempty"`
	TransitionState       string                  `json:"transition_state,omitempty"`
	SwitchLineage         string                  `json:"switch_lineage,omitempty"`
	HoldDurationSec       float64                 `json:"hold_duration_sec,omitempty"`
}

// RotationSettlementView 四轮动策略结算的统一视图(历史/已平仓)。
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
		SpotLeg:               RotationPositionLegView{Venue: pos.Venue, Market: pos.SpotLeg.Market, Side: pos.SpotLeg.Side, Qty: pos.SpotLeg.Qty, EntryPrice: pos.SpotLeg.EntryPrice, ExitPrice: pos.SpotLeg.ExitPrice, OrderIDs: pos.SpotLeg.OrderIDs},
		PerpLeg:               RotationPositionLegView{Venue: pos.Venue, Market: pos.PerpLeg.Market, Side: pos.PerpLeg.Side, Qty: pos.PerpLeg.Qty, EntryPrice: pos.PerpLeg.EntryPrice, ExitPrice: pos.PerpLeg.ExitPrice, OrderIDs: pos.PerpLeg.OrderIDs},
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
		SpotLeg:               RotationPositionLegView{Venue: pos.SpotVenue, Market: pos.SpotLeg.Market, Side: pos.SpotLeg.Side, Qty: pos.SpotLeg.Qty, EntryPrice: pos.SpotLeg.EntryPrice, ExitPrice: pos.SpotLeg.ExitPrice, OrderIDs: pos.SpotLeg.OrderIDs},
		PerpLeg:               RotationPositionLegView{Venue: pos.PerpVenue, Market: pos.PerpLeg.Market, Side: pos.PerpLeg.Side, Qty: pos.PerpLeg.Qty, EntryPrice: pos.PerpLeg.EntryPrice, ExitPrice: pos.PerpLeg.ExitPrice, OrderIDs: pos.PerpLeg.OrderIDs},
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
		SpotLeg:               RotationPositionLegView{Venue: pos.Venue, Market: pos.SpotLeg.Market, Side: pos.SpotLeg.Side, Qty: pos.SpotLeg.Qty, EntryPrice: pos.SpotLeg.EntryPrice, ExitPrice: pos.SpotLeg.ExitPrice, OrderIDs: pos.SpotLeg.OrderIDs},
		PerpLeg:               RotationPositionLegView{Venue: pos.Venue, Market: pos.PerpLeg.Market, Side: pos.PerpLeg.Side, Qty: pos.PerpLeg.Qty, EntryPrice: pos.PerpLeg.EntryPrice, ExitPrice: pos.PerpLeg.ExitPrice, OrderIDs: pos.PerpLeg.OrderIDs},
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
		SpotLeg:               RotationPositionLegView{Venue: pos.SpotVenue, Market: pos.SpotLeg.Market, Side: pos.SpotLeg.Side, Qty: pos.SpotLeg.Qty, EntryPrice: pos.SpotLeg.EntryPrice, ExitPrice: pos.SpotLeg.ExitPrice, OrderIDs: pos.SpotLeg.OrderIDs},
		PerpLeg:               RotationPositionLegView{Venue: pos.PerpVenue, Market: pos.PerpLeg.Market, Side: pos.PerpLeg.Side, Qty: pos.PerpLeg.Qty, EntryPrice: pos.PerpLeg.EntryPrice, ExitPrice: pos.PerpLeg.ExitPrice, OrderIDs: pos.PerpLeg.OrderIDs},
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
	return view
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
