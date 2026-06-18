package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/metrics"
	"lingxi/internal/money"
)

// rotation_pnl_api.go 五策略结算台账聚合(四轮动 + legacy perp-perp),供 /api/earnings 与 /api/performance 使用。
// 日界口径:北京时间自然日(CST/UTC+8),与 TodayCST* / beijingCSTDayStart 一致。

// strategySettlementRecord 是五策略结算台账的统一视图(只读聚合,不参与下单)。
type strategySettlementRecord struct {
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
}

// StrategyPnLSummary 汇总一段 close_time 窗口内的已实现损益。
type StrategyPnLSummary struct {
	DayStartCST  time.Time          `json:"day_start_cst,omitempty"`
	GeneratedAt  time.Time          `json:"generated_at"`
	WindowStart  time.Time          `json:"window_start,omitempty"`
	WindowEnd    time.Time          `json:"window_end,omitempty"`
	TotalNetPnL  float64            `json:"total_net_pnl"`
	TotalFunding float64            `json:"total_funding"`
	TotalFees    float64            `json:"total_fees"`
	TradeCount   int                `json:"trade_count"`
	ByStrategy   map[string]float64 `json:"by_strategy_net_pnl"`
	BySymbol     map[string]float64 `json:"by_symbol_net_pnl"`
	ByVenue      map[string]float64 `json:"by_venue_net_pnl"`
}

// cstDayKey 返回北京时间自然日的 YYYY-MM-DD 键(绩效序列用,与 earnings 日界一致)。
func cstDayKey(t time.Time) string {
	cst := t.In(beijingCST)
	y, m, d := cst.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, beijingCST).Format("2006-01-02")
}

func resolveStrategyRecordDir(s *DashboardServer) string {
	if s == nil {
		return "records"
	}
	if s.crossVenueExecutor != nil && strings.TrimSpace(s.crossVenueExecutor.recordDir) != "" {
		return strings.TrimSpace(s.crossVenueExecutor.recordDir)
	}
	if s.store != nil {
		if dir := strings.TrimSpace(s.store.RecordDir()); dir != "" {
			return dir
		}
	}
	return "records"
}

func loadTailJSONLFile[T any](path string, since time.Time, limit int) ([]T, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
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
	if limit > 0 && len(lines) > limit {
		start = len(lines) - limit
	}
	out := make([]T, 0, len(lines)-start)
	for _, line := range lines[start:] {
		var rec T
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if since.IsZero() {
		return out, nil
	}
	filtered := make([]T, 0, len(out))
	for _, rec := range out {
		if closeTimeFromSettlement(rec).Before(since) {
			continue
		}
		filtered = append(filtered, rec)
	}
	return filtered, nil
}

func closeTimeFromSettlement(v any) time.Time {
	switch rec := v.(type) {
	case CrossVenueSettlement:
		return rec.CloseTime
	case SingleVenueSpotPerpSettlement:
		return rec.CloseTime
	case CrossVenueSpotPerpSettlement:
		return rec.CloseTime
	case SingleVenuePerpSpotSettlement:
		return rec.CloseTime
	case CrossVenuePerpSpotSettlement:
		return rec.CloseTime
	case strategySettlementRecord:
		return rec.CloseTime
	default:
		return time.Time{}
	}
}

func crossVenueSettlementToRecord(rec CrossVenueSettlement) strategySettlementRecord {
	venues := []string{}
	if v := strings.TrimSpace(rec.HighVenue); v != "" {
		venues = append(venues, v)
	}
	if v := strings.TrimSpace(rec.LowVenue); v != "" && v != rec.HighVenue {
		venues = append(venues, v)
	}
	return strategySettlementRecord{
		StrategyType:    UnifiedStrategyPerpPerp,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		Symbol:          rec.Symbol,
		Venues:          venues,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
		SwitchExit:      rec.SwitchExit,
		SwitchSequence:  rec.SwitchSequence,
	}
}

func svspSettlementToRecord(rec SingleVenueSpotPerpSettlement) strategySettlementRecord {
	v := strings.TrimSpace(rec.Venue)
	venues := []string{}
	if v != "" {
		venues = append(venues, v)
	}
	return strategySettlementRecord{
		StrategyType:    UnifiedStrategySingleVenueSpotPerp,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpportunityID:   rec.OpportunityID,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		Symbol:          rec.Symbol,
		Venues:          venues,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
	}
}

func cvspSettlementToRecord(rec CrossVenueSpotPerpSettlement) strategySettlementRecord {
	venues := uniqueNonEmptyStrings(rec.SpotVenue, rec.PerpVenue)
	return strategySettlementRecord{
		StrategyType:    UnifiedStrategyCrossVenueSpotPerp,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpportunityID:   rec.OpportunityID,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		Symbol:          rec.Symbol,
		Venues:          venues,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
	}
}

func svpsSettlementToRecord(rec SingleVenuePerpSpotSettlement) strategySettlementRecord {
	v := strings.TrimSpace(rec.Venue)
	venues := []string{}
	if v != "" {
		venues = append(venues, v)
	}
	return strategySettlementRecord{
		StrategyType:    UnifiedStrategySingleVenuePerpSpot,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpportunityID:   rec.OpportunityID,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		Symbol:          rec.Symbol,
		Venues:          venues,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		BorrowCost:      rec.BorrowCost,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
	}
}

func cvpsSettlementToRecord(rec CrossVenuePerpSpotSettlement) strategySettlementRecord {
	venues := uniqueNonEmptyStrings(rec.SpotVenue, rec.PerpVenue)
	return strategySettlementRecord{
		StrategyType:    UnifiedStrategyCrossVenuePerpSpot,
		SettlementID:    rec.SettlementID,
		PositionKey:     rec.PositionKey,
		OpportunityID:   rec.OpportunityID,
		OpenTime:        rec.OpenTime,
		CloseTime:       rec.CloseTime,
		Symbol:          rec.Symbol,
		Venues:          venues,
		NotionalUSDT:    rec.NotionalUSDT,
		NetPnL:          rec.NetPnL,
		RealizedFunding: rec.RealizedFunding,
		OpenFees:        rec.OpenFees,
		CloseFees:       rec.CloseFees,
		TotalFees:       rec.TotalFees,
		BorrowCost:      rec.BorrowCost,
		RollbackCost:    rec.RollbackCost,
		CloseReason:     rec.CloseReason,
	}
}

func uniqueNonEmptyStrings(vals ...string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func loadAllStrategySettlementRecords(s *DashboardServer) ([]strategySettlementRecord, error) {
	dir := resolveStrategyRecordDir(s)
	var out []strategySettlementRecord

	if s != nil && s.crossVenueExecutor != nil {
		recs, err := s.crossVenueExecutor.loadAllCrossVenueSettlements()
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, crossVenueSettlementToRecord(rec))
		}
	} else {
		recs, err := loadTailJSONLFile[CrossVenueSettlement](filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName), time.Time{}, 0)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, crossVenueSettlementToRecord(rec))
		}
	}

	svspRecs, err := loadTailJSONLFile[SingleVenueSpotPerpSettlement](filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svspRecs {
		out = append(out, svspSettlementToRecord(rec))
	}

	cvspRecs, err := loadTailJSONLFile[CrossVenueSpotPerpSettlement](filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvspRecs {
		out = append(out, cvspSettlementToRecord(rec))
	}

	svpsRecs, err := loadTailJSONLFile[SingleVenuePerpSpotSettlement](filepath.Join(singleVenuePerpSpotRecordDir(dir), svpsSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svpsRecs {
		out = append(out, svpsSettlementToRecord(rec))
	}

	cvpsRecs, err := loadTailJSONLFile[CrossVenuePerpSpotSettlement](filepath.Join(crossVenuePerpSpotRecordDir(dir), crossVenuePerpSpotSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvpsRecs {
		out = append(out, cvpsSettlementToRecord(rec))
	}

	return out, nil
}

func settlementRecordMatchesVenue(rec strategySettlementRecord, venueName string) bool {
	venueName = strings.TrimSpace(strings.ToLower(venueName))
	if venueName == "" || venueName == venueAllSelector {
		return true
	}
	for _, v := range rec.Venues {
		if strings.EqualFold(strings.TrimSpace(v), venueName) {
			return true
		}
	}
	return false
}

func filterSettlementRecordsByVenue(records []strategySettlementRecord, venueName string) []strategySettlementRecord {
	if venueName == "" || venueName == venueAllSelector {
		return records
	}
	out := make([]strategySettlementRecord, 0, len(records))
	for _, rec := range records {
		if settlementRecordMatchesVenue(rec, venueName) {
			out = append(out, rec)
		}
	}
	return out
}

func aggregateStrategySettlementsInRange(records []strategySettlementRecord, since, until time.Time) StrategyPnLSummary {
	sum := StrategyPnLSummary{
		WindowStart: since,
		WindowEnd:   until,
		GeneratedAt: until,
		ByStrategy:  make(map[string]float64),
		BySymbol:    make(map[string]float64),
		ByVenue:     make(map[string]float64),
	}
	for _, rec := range records {
		if !since.IsZero() && rec.CloseTime.Before(since) {
			continue
		}
		if !until.IsZero() && rec.CloseTime.After(until) {
			continue
		}
		sum.TotalNetPnL = money.SumUSDT(sum.TotalNetPnL, rec.NetPnL)
		sum.TotalFunding = money.SumUSDT(sum.TotalFunding, rec.RealizedFunding)
		sum.TotalFees = money.SumUSDT(sum.TotalFees, rec.TotalFees)
		sum.TradeCount++
		if rec.StrategyType != "" {
			sum.ByStrategy[rec.StrategyType] = money.SumUSDT(sum.ByStrategy[rec.StrategyType], rec.NetPnL)
		}
		if rec.Symbol != "" {
			sum.BySymbol[rec.Symbol] = money.SumUSDT(sum.BySymbol[rec.Symbol], rec.NetPnL)
		}
		for _, v := range rec.Venues {
			if v == "" {
				continue
			}
			share := rec.NetPnL
			if len(rec.Venues) > 1 {
				share = rec.NetPnL / float64(len(rec.Venues))
			}
			sum.ByVenue[v] = money.SumUSDT(sum.ByVenue[v], share)
		}
	}
	return sum
}

// TodayCSTStrategyPnL 汇总五策略台账 close_time ∈ [北京时间当日 00:00, now] 的已实现净利。
func TodayCSTStrategyPnL(s *DashboardServer, now time.Time) (StrategyPnLSummary, error) {
	records, err := loadAllStrategySettlementRecords(s)
	if err != nil {
		return StrategyPnLSummary{}, err
	}
	dayStart := beijingCSTDayStart(now)
	sum := aggregateStrategySettlementsInRange(records, dayStart, now)
	sum.DayStartCST = dayStart
	return sum, nil
}

// AllTimeStrategyPnL 汇总五策略台账全部已实现净利。
func AllTimeStrategyPnL(s *DashboardServer, now time.Time) (StrategyPnLSummary, error) {
	records, err := loadAllStrategySettlementRecords(s)
	if err != nil {
		return StrategyPnLSummary{}, err
	}
	sum := aggregateStrategySettlementsInRange(records, time.Time{}, now)
	return sum, nil
}

func strategyRecordsDailyPnLCST(records []strategySettlementRecord) []metrics.DailyPnL {
	byDay := make(map[string]float64)
	for _, rec := range records {
		if rec.CloseTime.IsZero() {
			continue
		}
		day := cstDayKey(rec.CloseTime)
		byDay[day] = money.SumUSDT(byDay[day], rec.NetPnL)
	}
	var entries []metrics.DailyPnL
	for day, pnl := range byDay {
		entries = append(entries, metrics.DailyPnL{Day: day, PnL: pnl})
	}
	return entries
}

func mergedStrategyPortfolioReturns(s *DashboardServer, venueName string) (returns []float64, days []string, capital float64, err error) {
	records, err := loadAllStrategySettlementRecords(s)
	if err != nil {
		return nil, nil, 0, err
	}
	records = filterSettlementRecordsByVenue(records, venueName)
	entries := strategyRecordsDailyPnLCST(records)
	series := metrics.BuildDailySeries(entries)

	if s != nil && s.orchestrator != nil {
		if venueName == "" || venueName == venueAllSelector {
			for _, rt := range s.orchestrator.All() {
				if rt != nil {
					capital = money.SumUSDT(capital, rt.allocatedCapitalUSDT)
				}
			}
		} else if rt, ok := s.orchestrator.Get(venueName); ok && rt != nil {
			capital = rt.allocatedCapitalUSDT
		}
	}

	returns = metrics.ReturnsFromDaily(series, capital)
	days = make([]string, len(series))
	for i, d := range series {
		days[i] = d.Day
	}
	return returns, days, capital, nil
}

func sumOpenAccruedFundingUSDT(s *DashboardServer) float64 {
	var total float64
	if s == nil {
		return 0
	}
	views := buildRotationPositionViews(s, rotationExecCfg(s), "open", "")
	for _, v := range views {
		total = money.SumUSDT(total, v.AccruedFundingUSDT)
	}
	return total
}

func rotationExecCfg(s *DashboardServer) CrossVenueExecutorConfig {
	if s == nil {
		return CrossVenueExecutorConfig{}
	}
	if s.crossVenueExecutor != nil {
		return s.crossVenueExecutor.cfg.CrossVenueExecutor
	}
	if s.svspExec != nil {
		return s.svspExec.execCfg()
	}
	if s.cvspExec != nil {
		return s.cvspExec.cfg.CrossVenueExecutor
	}
	if s.svpsExec != nil {
		return s.svpsExec.execCfg()
	}
	if s.cvpsExec != nil {
		return s.cvpsExec.cfg.CrossVenueExecutor
	}
	return CrossVenueExecutorConfig{}
}
