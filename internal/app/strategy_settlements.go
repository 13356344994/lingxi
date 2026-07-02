package app

// 账户风控消费的统一结算记录模型与 JSONL 读取工具。
// carry/五策略删除后,当前唯一生产来源是各所方向引擎的 settlements.jsonl。

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	"lingxi/internal/metrics"
	"lingxi/internal/money"
)

// strategySettlementRecord 账户风控/收益统计消费的统一结算记录。
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
	// 消费侧可信判据(方向结算 realized 为交易所实扣口径,恒为 trusted)。
	FundingTruthStatus    string
	AttributionIncomplete bool
	TradePnL              float64
}

// isTrustedStrategySettlementRecord 方向结算 realized_usdt 来自交易所实扣,恒可信;
// 仅当显式标注非 trusted(legacy 迁移数据)时排除。
func isTrustedStrategySettlementRecord(rec strategySettlementRecord) bool {
	st := strings.TrimSpace(strings.ToLower(rec.FundingTruthStatus))
	return st == "" || st == "trusted"
}

// loadTailJSONLFile 通用 JSONL 尾部读取:limit>0 只取末尾 limit 行;since 非零时按 close time 过滤。
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
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 8*1024*1024)
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
	case strategySettlementRecord:
		return rec.CloseTime
	case okxDirectionalSettlement:
		return rec.Time
	default:
		return time.Time{}
	}
}

// StrategyPnLSummary 汇总一段 close_time 窗口内的已实现损益(面板收益页消费,JSON 形状与旧版保持一致)。
type StrategyPnLSummary struct {
	DayStartCST         time.Time          `json:"day_start_cst,omitempty"`
	GeneratedAt         time.Time          `json:"generated_at"`
	WindowStart         time.Time          `json:"window_start,omitempty"`
	WindowEnd           time.Time          `json:"window_end,omitempty"`
	TotalNetPnL         float64            `json:"total_net_pnl"`
	UntrustedNetPnL     float64            `json:"untrusted_net_pnl"`
	TotalFunding        float64            `json:"total_funding"`
	TotalFees           float64            `json:"total_fees"`
	TradeCount          int                `json:"trade_count"`
	UntrustedTradeCount int                `json:"untrusted_trade_count"`
	ByStrategy          map[string]float64 `json:"by_strategy_net_pnl"`
	BySymbol            map[string]float64 `json:"by_symbol_net_pnl"`
	ByVenue             map[string]float64 `json:"by_venue_net_pnl"`
}

// cstDayKey 返回北京时间自然日的 YYYY-MM-DD 键(绩效序列用,与 earnings 日界一致)。
func cstDayKey(t time.Time) string {
	cst := t.In(beijingCST)
	y, m, d := cst.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, beijingCST).Format("2006-01-02")
}

func settlementRecordMatchesVenue(rec strategySettlementRecord, venueName string) bool {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
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
		if !isTrustedStrategySettlementRecord(rec) {
			sum.UntrustedNetPnL = money.SumUSDT(sum.UntrustedNetPnL, rec.NetPnL)
			sum.UntrustedTradeCount++
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

func loadAllStrategySettlementRecords(s *DashboardServer) ([]strategySettlementRecord, error) {
	dir := "records"
	if s != nil && strings.TrimSpace(s.recordDir) != "" {
		dir = strings.TrimSpace(s.recordDir)
	}
	return loadStrategySettlementRecordsFromDir(dir)
}

// TodayCSTStrategyPnL 汇总方向结算 close_time ∈ [北京时间当日 00:00, now] 的已实现净利。
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

// AllTimeStrategyPnL 汇总方向结算全部已实现净利。
func AllTimeStrategyPnL(s *DashboardServer, now time.Time) (StrategyPnLSummary, error) {
	records, err := loadAllStrategySettlementRecords(s)
	if err != nil {
		return StrategyPnLSummary{}, err
	}
	return aggregateStrategySettlementsInRange(records, time.Time{}, now), nil
}

func strategyRecordsDailyPnLCST(records []strategySettlementRecord) []metrics.DailyPnL {
	byDay := make(map[string]float64)
	for _, rec := range records {
		if rec.CloseTime.IsZero() || !isTrustedStrategySettlementRecord(rec) {
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
