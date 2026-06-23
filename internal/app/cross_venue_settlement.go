package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/money"
)

const crossVenueSettlementsFileName = "settlements.jsonl"

// beijingCST 固定 UTC+8,不依赖服务器本地时区(生产机为 UTC)。
var beijingCST = time.FixedZone("CST", 8*3600)

// beijingCSTDayStart 返回 now 所在北京时间自然日的 00:00(带 +08:00 偏移,可转 UTC 比较)。
func beijingCSTDayStart(now time.Time) time.Time {
	cst := now.In(beijingCST)
	y, m, d := cst.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, beijingCST)
}

// CrossVenueCSTDayEarnings 北京时间自然日已实现净利汇总(面板「今日收益」)。
type CrossVenueCSTDayEarnings struct {
	DayStartCST time.Time          `json:"day_start_cst"`
	GeneratedAt time.Time          `json:"generated_at"`
	TotalNetPnL float64            `json:"total_net_pnl"`
	TradeCount  int                `json:"trade_count"`
	BySymbol    map[string]float64 `json:"by_symbol_net_pnl"`
}

// CrossVenueCSTDayVenueEarnings 北京时间自然日按交易所(跨所结算腿)归因的已实现净利。
type CrossVenueCSTDayVenueEarnings struct {
	DayStartCST time.Time          `json:"day_start_cst"`
	GeneratedAt time.Time          `json:"generated_at"`
	TotalNetPnL float64            `json:"total_net_pnl"`
	TradeCount  int                `json:"trade_count"`
	ByVenue     map[string]float64 `json:"by_venue_net_pnl"`
}

// CrossVenueSettlement 跨所 perp-perp 单仓净利润归因结算记录(平仓时写入 settlements.jsonl)。
type CrossVenueSettlement struct {
	SettlementID            string    `json:"settlement_id,omitempty"`
	PositionKey             string    `json:"position_key,omitempty"`
	EntryDecisionCycleID    string    `json:"entry_decision_cycle_id,omitempty"`
	EntrySnapshotID         string    `json:"entry_snapshot_id,omitempty"`
	OpportunityID           string    `json:"opportunity_id,omitempty"`
	EntryOpenMode           string    `json:"entry_open_mode,omitempty"`
	UsedFallback            bool      `json:"used_fallback,omitempty"`
	CurrentSnapshotID       string    `json:"current_snapshot_id,omitempty"`
	SwitchSequence          string    `json:"switch_sequence,omitempty"`
	SwitchExit              bool      `json:"switch_exit,omitempty"`
	OpenTime                time.Time `json:"open_time"`
	CloseTime               time.Time `json:"close_time"`
	Symbol                  string    `json:"symbol"`
	HighVenue               string    `json:"high_venue"`
	LowVenue                string    `json:"low_venue"`
	HighIntervalHours       int       `json:"high_interval_hours,omitempty"`
	LowIntervalHours        int       `json:"low_interval_hours,omitempty"`
	NotionalUSDT            float64   `json:"notional_usdt"`
	HighLegFunding          float64   `json:"high_leg_funding"`
	LowLegFunding           float64   `json:"low_leg_funding"`
	EstimatedFunding        float64   `json:"estimated_funding,omitempty"`
	RealizedFunding         float64   `json:"realized_funding"`
	RealizedFundingComplete bool      `json:"realized_funding_complete,omitempty"`
	FundingTruthStatus      string    `json:"funding_truth_status"`
	AttributionIncomplete   bool      `json:"attribution_incomplete,omitempty"`
	TradePnL                float64   `json:"trade_pnl"`
	OpenFees                float64   `json:"open_fees"`
	CloseFees               float64   `json:"close_fees"`
	RollbackCost            float64   `json:"rollback_cost"`
	RebalanceCost           float64   `json:"rebalance_cost"`
	TotalFees               float64   `json:"total_fees"`
	OpenSlippage            float64   `json:"open_slippage"`
	CloseSlippage           float64   `json:"close_slippage"`
	NetPnL                  float64   `json:"net_pnl"`
	CloseReason             string    `json:"close_reason,omitempty"`
}

// CrossVenueSettlementsSummary 跨所结算汇总(只读 API 用)。
type CrossVenueSettlementsSummary struct {
	TotalNetPnL    float64            `json:"total_net_pnl"`
	TotalFees      float64            `json:"total_fees"`
	TotalFunding   float64            `json:"total_funding"`
	ByVenueNetPnL  map[string]float64 `json:"by_venue_net_pnl"`
	BySymbolNetPnL map[string]float64 `json:"by_symbol_net_pnl"`
}

// CrossVenueSettlementsAPIResponse GET /api/cross-venue/settlements 响应体。
type CrossVenueSettlementsAPIResponse struct {
	Records []CrossVenueSettlement       `json:"records"`
	Summary CrossVenueSettlementsSummary `json:"summary"`
	Limit   int                          `json:"limit"`
}

func (e *CrossVenueExecutor) settlementsLedgerPath() string {
	return filepath.Join(crossVenueRecordDir(e.recordDir), crossVenueSettlementsFileName)
}

func computeCrossVenueTradePnL(pos CrossVenuePosition) float64 {
	var total float64
	if pos.HighLeg.ClosedQty > executionQtyTolerance && pos.HighLeg.ExitPrice > 0 {
		total += (pos.HighLeg.EntryPrice - pos.HighLeg.ExitPrice) * pos.HighLeg.ClosedQty
	}
	if pos.LowLeg.ClosedQty > executionQtyTolerance && pos.LowLeg.ExitPrice > 0 {
		total += (pos.LowLeg.ExitPrice - pos.LowLeg.EntryPrice) * pos.LowLeg.ClosedQty
	}
	return total
}

// buildCrossVenueSettlement 从持仓快照计算单仓结算归因。
func buildCrossVenueSettlement(pos CrossVenuePosition, closeTime time.Time) CrossVenueSettlement {
	attr := pos.CrossVenueAttribution
	openFees := attr.OpenFeesUSDT
	closeFees := attr.CloseFeesUSDT
	totalFees := money.SumUSDT(openFees, closeFees)
	realizedFunding := money.SumUSDT(pos.HighLegFundingUSDT, pos.LowLegFundingUSDT)
	tradePnL := computeCrossVenueTradePnL(pos)

	fundingStatus := strings.TrimSpace(pos.FundingTruthStatus)
	if fundingStatus == "" {
		fundingStatus = "unknown"
	}
	realizedFundingComplete := pos.RealizedFundingComplete
	switch {
	case crossVenueSettlementShouldUseEstimatedFunding(pos, fundingStatus, closeTime):
		realizedFunding = pos.EstFundingUSDT
		fundingStatus = "estimated"
		realizedFundingComplete = false
	case fundingStatus == "partial" && pos.EstFundingUSDT != 0:
		// FIX(P1):partial = 一腿真实、另一腿真实资费拉取失败。原先直接用两腿之和,缺失腿恒为 0 →
		// 少计资金费。改为给缺失腿补上其"整仓估算"份额(与 missing 全缺失分支同一 EstFundingUSDT 基准),
		// 真实腿保持原值不重复计数(见 crossVenuePartialRealizedFunding 注释)。状态仍标 partial,
		// RealizedFundingComplete=false 已表明非全真,下游可据此审计。
		realizedFunding = crossVenuePartialRealizedFunding(pos)
		realizedFundingComplete = false
	}

	// 净利润口径对齐 cost_accounting.go SettlementAttribution / CycleSettlement:
	// net_pnl = realized_funding + trade_pnl − total_fees − rollback_cost − rebalance_cost
	// 【关键】开/平仓滑点已隐含在 trade_pnl(由实际成交价算出); OpenSlippage/CloseSlippage 是相对参考价的
	// 执行质量量,绝不可再从 net_pnl 扣减(否则重复计数,见 cost_accounting.go 第 64-74 行注释)。
	netPnL := money.SumUSDT(realizedFunding, tradePnL, -totalFees, -attr.RollbackCostUSDT, -attr.RebalanceCostUSDT)

	return CrossVenueSettlement{
		SettlementID:            crossVenueSettlementID(pos),
		PositionKey:             crossVenuePositionKey(pos),
		OpportunityID:           crossVenuePositionOpportunityID(pos),
		EntryDecisionCycleID:    pos.EntryDecisionCycleID,
		EntrySnapshotID:         pos.EntrySnapshotID,
		EntryOpenMode:           pos.EntryOpenMode,
		UsedFallback:            pos.UsedFallback,
		CurrentSnapshotID:       pos.CurrentSnapshotID,
		SwitchSequence:          pos.PendingSwitchSequence,
		SwitchExit:              crossVenuePositionIsSwitchExit(pos),
		OpenTime:                pos.OpenedAt,
		CloseTime:               closeTime,
		Symbol:                  pos.Symbol,
		HighVenue:               pos.HighVenue,
		LowVenue:                pos.LowVenue,
		HighIntervalHours:       pos.HighIntervalHours,
		LowIntervalHours:        pos.LowIntervalHours,
		NotionalUSDT:            pos.NotionalUSDT,
		HighLegFunding:          pos.HighLegFundingUSDT,
		LowLegFunding:           pos.LowLegFundingUSDT,
		EstimatedFunding:        pos.EstFundingUSDT,
		RealizedFunding:         realizedFunding,
		RealizedFundingComplete: realizedFundingComplete,
		FundingTruthStatus:      fundingStatus,
		AttributionIncomplete:   pos.AttributionIncomplete,
		TradePnL:                tradePnL,
		OpenFees:                openFees,
		CloseFees:               closeFees,
		RollbackCost:            attr.RollbackCostUSDT,
		RebalanceCost:           attr.RebalanceCostUSDT,
		TotalFees:               totalFees,
		OpenSlippage:            attr.OpenSlippageUSDT,
		CloseSlippage:           attr.CloseSlippageUSDT,
		NetPnL:                  netPnL,
		CloseReason:             pos.CloseReason,
	}
}

// crossVenuePartialRealizedFunding 计算 partial(一腿真实、一腿缺失)时的资金费:
// 保留已取真实腿原值,缺失腿用"整仓估算"减去真实腿份额后补齐,从而:
//
//	realized = 真实腿之和 + (EstFundingUSDT − 真实腿之和) = EstFundingUSDT
//
// 由 cross_venue_manage.go 维护:EstFundingUSDT = HighLegFundingUSDT + LowLegFundingUSDT + 轮间估算增量,
// 其中真实腿份额即 HighLegFundingUSDT/LowLegFundingUSDT。因此真实腿被原样保留、绝不重复计数,
// 缺失腿取其在整仓估算中的份额(而非 0)。调用方已确保 EstFundingUSDT != 0(否则无估算可用,保持原两腿之和)。
func crossVenuePartialRealizedFunding(pos CrossVenuePosition) float64 {
	realLegSum := money.SumUSDT(pos.HighLegFundingUSDT, pos.LowLegFundingUSDT)
	missingLegEstimate := pos.EstFundingUSDT - realLegSum
	return money.SumUSDT(realLegSum, missingLegEstimate)
}

// crossVenueSettlementShouldUseEstimatedFunding 真实资费不可用时,若持仓跨越≥1次结算则用 EstFundingUSDT 兜底。
func crossVenueSettlementShouldUseEstimatedFunding(pos CrossVenuePosition, fundingStatus string, closeTime time.Time) bool {
	if pos.EstFundingUSDT == 0 {
		return false
	}
	switch fundingStatus {
	case "complete", "partial", "real":
		return false
	case "estimated":
		return true
	}
	return crossVenueHeldThroughSettlement(pos, closeTime)
}

func crossVenueHeldThroughSettlement(pos CrossVenuePosition, closeTime time.Time) bool {
	if pos.OpenedAt.IsZero() || closeTime.IsZero() || !closeTime.After(pos.OpenedAt) {
		return false
	}
	target := rotationEffectiveTargetFirstFundingTime(
		pos.OpenedAt, pos.TargetFirstFundingTime, pos.HighNextFundingTime, pos.LowNextFundingTime,
	)
	if !target.IsZero() && closeTime.After(target) {
		return true
	}
	interval := pos.HighIntervalHours
	if interval <= 0 {
		interval = defaultFundingCaptureIntervalHours
	}
	if pos.LowIntervalHours > 0 && pos.LowIntervalHours < interval {
		interval = pos.LowIntervalHours
	}
	return settlementHeldThroughFirstFunding(pos.OpenedAt, closeTime, interval)
}

func crossVenuePositionIsSwitchExit(pos CrossVenuePosition) bool {
	if strings.TrimSpace(pos.PendingSwitchSequence) != "" {
		return true
	}
	reason := strings.TrimSpace(pos.CloseReason)
	return strings.Contains(reason, "换仓")
}

func crossVenueSettlementID(pos CrossVenuePosition) string {
	if strings.TrimSpace(pos.IdemPrefix) != "" {
		return pos.IdemPrefix
	}
	return pos.Symbol + "|" + pos.OpenedAt.UTC().Format(time.RFC3339Nano)
}

func (e *CrossVenueExecutor) appendCrossVenueSettlement(pos CrossVenuePosition) {
	if e == nil {
		return
	}
	rec := buildCrossVenueSettlement(pos, e.nowFn())
	data, err := json.Marshal(rec)
	if err != nil {
		if e.logger != nil {
			e.logger.Printf("[跨所结算] 序列化失败 %s: %v", pos.Symbol, err)
		}
		return
	}

	e.settlementsMu.Lock()
	defer e.settlementsMu.Unlock()

	path := e.settlementsLedgerPath()
	if settlementExists(path, rec.SettlementID) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		if e.logger != nil {
			e.logger.Printf("[跨所结算] 创建目录失败 path=%s: %v", path, err)
		}
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			if e.logger != nil {
				e.logger.Printf("[跨所结算] 打开台账失败 path=%s: %v", path, err)
			}
			return
		}
		if _, err := f.Write(append(data, '\n')); err == nil {
			_ = f.Close()
			break
		} else if e.logger != nil {
			e.logger.Printf("[跨所结算] 写入台账失败 attempt=%d path=%s: %v", attempt, path, err)
		}
		_ = f.Close()
	}

	if e.logger != nil {
		e.logger.Printf("[跨所结算] %s net=%.4f funding=%.4f trade=%.4f fees=%.4f rollback=%.4f rebalance=%.4f open_slip=%.4f close_slip=%.4f",
			rec.Symbol, rec.NetPnL, rec.RealizedFunding, rec.TradePnL, rec.TotalFees,
			rec.RollbackCost, rec.RebalanceCost, rec.OpenSlippage, rec.CloseSlippage)
	}
	appendAttributionRealizedBestEffort(e.recordDir, realizedRecordFromCrossVenueSettlement(rec))
}

func settlementExists(path, settlementID string) bool {
	if settlementID == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec CrossVenueSettlement
		if json.Unmarshal([]byte(line), &rec) == nil && rec.SettlementID == settlementID {
			return true
		}
	}
	if err := scanner.Err(); err != nil {
		return false
	}
	return false
}

// finalizeClosedCrossVenuePosition 平仓收尾:写资费台账 + 写结算归因 + 删持仓台账(best-effort,不影响主流程)。
func (e *CrossVenueExecutor) finalizeClosedCrossVenuePosition(pos CrossVenuePosition, live bool) {
	if e == nil || !realLedgerWritesAllowed(live) {
		return
	}
	e.appendRealizedEarning(pos)
	e.appendCrossVenueSettlement(pos)
	_ = e.removeCrossVenuePosition(pos)
}

// attributeCrossVenueSettlementToVenues 把单笔跨所结算按 high/low 腿归因到交易所。
//
// 口径(与 net_pnl 严格守恒,Σ两腿 = net_pnl):
//   - 各腿先取 leg_funding;
//   - total_fees / rollback_cost / rebalance_cost / trade_pnl 在结算记录中无法逐腿拆账,均摊到两腿;
//   - 即 high = high_leg_funding + trade_pnl/2 − fees/2 − rollback/2 − rebalance/2, low 对称。
func attributeCrossVenueSettlementToVenues(rec CrossVenueSettlement) (highVenue, lowVenue string, highPnL, lowPnL float64) {
	highVenue = rec.HighVenue
	lowVenue = rec.LowVenue
	half := 0.5
	shared := money.SumUSDT(rec.TradePnL*half, -rec.TotalFees*half, -rec.RollbackCost*half, -rec.RebalanceCost*half)
	highPnL = money.SumUSDT(rec.HighLegFunding, shared)
	lowPnL = money.SumUSDT(rec.LowLegFunding, shared)
	return highVenue, lowVenue, highPnL, lowPnL
}

func aggregateCrossVenueSettlementsByVenueInRange(records []CrossVenueSettlement, since, until time.Time) CrossVenueCSTDayVenueEarnings {
	sum := CrossVenueCSTDayVenueEarnings{
		DayStartCST: since,
		GeneratedAt: until,
		ByVenue:     make(map[string]float64),
	}
	for _, rec := range records {
		if rec.CloseTime.Before(since) || rec.CloseTime.After(until) {
			continue
		}
		sum.TotalNetPnL = money.SumUSDT(sum.TotalNetPnL, rec.NetPnL)
		sum.TradeCount++
		highVenue, lowVenue, highPnL, lowPnL := attributeCrossVenueSettlementToVenues(rec)
		if highVenue != "" {
			sum.ByVenue[highVenue] = money.SumUSDT(sum.ByVenue[highVenue], highPnL)
		}
		if lowVenue != "" {
			sum.ByVenue[lowVenue] = money.SumUSDT(sum.ByVenue[lowVenue], lowPnL)
		}
	}
	return sum
}

func aggregateCrossVenueSettlementsInRange(records []CrossVenueSettlement, since, until time.Time) CrossVenueCSTDayEarnings {
	sum := CrossVenueCSTDayEarnings{
		DayStartCST: since,
		GeneratedAt: until,
		BySymbol:    make(map[string]float64),
	}
	for _, rec := range records {
		if rec.CloseTime.Before(since) || rec.CloseTime.After(until) {
			continue
		}
		sum.TotalNetPnL = money.SumUSDT(sum.TotalNetPnL, rec.NetPnL)
		sum.TradeCount++
		if rec.Symbol != "" {
			sum.BySymbol[rec.Symbol] = money.SumUSDT(sum.BySymbol[rec.Symbol], rec.NetPnL)
		}
	}
	return sum
}

// TodayCSTEarnings 汇总 close_time ∈ [北京时间当日 00:00, now] 的 settlements net_pnl。
func (e *CrossVenueExecutor) TodayCSTEarnings(now time.Time) (CrossVenueCSTDayEarnings, error) {
	if e == nil {
		return CrossVenueCSTDayEarnings{}, nil
	}
	records, err := e.loadAllCrossVenueSettlements()
	if err != nil {
		return CrossVenueCSTDayEarnings{}, err
	}
	dayStart := beijingCSTDayStart(now)
	return aggregateCrossVenueSettlementsInRange(records, dayStart, now), nil
}

func (e *CrossVenueExecutor) loadAllCrossVenueSettlements() ([]CrossVenueSettlement, error) {
	return e.loadCrossVenueSettlements(-1)
}

func (e *CrossVenueExecutor) loadCrossVenueSettlements(limit int) ([]CrossVenueSettlement, error) {
	if e == nil {
		return nil, nil
	}
	loadAll := limit < 0
	if limit <= 0 && !loadAll {
		limit = 100
	}
	e.settlementsMu.Lock()
	defer e.settlementsMu.Unlock()

	path := e.settlementsLedgerPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	start := 0
	if !loadAll && len(lines) > limit {
		start = len(lines) - limit
	}
	out := make([]CrossVenueSettlement, 0, len(lines)-start)
	for _, line := range lines[start:] {
		var rec CrossVenueSettlement
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func summarizeCrossVenueSettlements(records []CrossVenueSettlement) CrossVenueSettlementsSummary {
	sum := CrossVenueSettlementsSummary{
		ByVenueNetPnL:  make(map[string]float64),
		BySymbolNetPnL: make(map[string]float64),
	}
	for _, rec := range records {
		sum.TotalNetPnL = money.SumUSDT(sum.TotalNetPnL, rec.NetPnL)
		sum.TotalFees = money.SumUSDT(sum.TotalFees, rec.TotalFees)
		sum.TotalFunding = money.SumUSDT(sum.TotalFunding, rec.RealizedFunding)
		highVenue, lowVenue, highPnL, lowPnL := attributeCrossVenueSettlementToVenues(rec)
		if highVenue != "" {
			sum.ByVenueNetPnL[highVenue] = money.SumUSDT(sum.ByVenueNetPnL[highVenue], highPnL)
		}
		if lowVenue != "" {
			sum.ByVenueNetPnL[lowVenue] = money.SumUSDT(sum.ByVenueNetPnL[lowVenue], lowPnL)
		}
		sum.BySymbolNetPnL[rec.Symbol] = money.SumUSDT(sum.BySymbolNetPnL[rec.Symbol], rec.NetPnL)
	}
	return sum
}

// SettlementsSnapshot 返回最近 N 条结算 + 汇总(供 HTTP API)。
func (e *CrossVenueExecutor) SettlementsSnapshot(limit int) (CrossVenueSettlementsAPIResponse, error) {
	records, err := e.loadCrossVenueSettlements(limit)
	if err != nil {
		return CrossVenueSettlementsAPIResponse{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	return CrossVenueSettlementsAPIResponse{
		Records: records,
		Summary: summarizeCrossVenueSettlements(records),
		Limit:   limit,
	}, nil
}
