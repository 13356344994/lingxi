package app

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// BacktestRunner 负责从本地历史样本生成回测汇总结果。
type BacktestRunner struct {
	store                       *ReportStore
	logger                      *log.Logger
	cfg                         EngineConfig
	progressHook                func(stage EngineStage, message string, current int, total int)
	borrowHourlyInterestByAsset map[string]float64
}

// BacktestTradeSample 表示一条历史样本交易记录。
type BacktestTradeSample struct {
	Scenario      string
	Symbol        string
	Period        string
	TradeID       string
	FundingIncome float64
	TradingPnL    float64
	TotalFees     float64
	Win           bool
	Drawdown      float64
	Notes         string
}

// BacktestReplaySample 表示某个历史时点的市场快照，用于回放是否出手。
type BacktestReplaySample struct {
	Scenario        string
	Symbol          string
	Period          string
	Timestamp       string
	FundingRate     float64
	BasisRate       float64
	EntrySpreadRate float64
	ExitSpreadRate  float64
	FeeRate         float64
	NotionalUSDT    float64
	DrawdownRate    float64
	LiquidityOK     bool
	Notes           string
}

// BacktestTimelineSample 表示按模块时序排列的历史事件。
type BacktestTimelineSample struct {
	Scenario             string
	Symbol               string
	Period               string
	Phase                string
	Timestamp            string
	FundingRate          float64
	BasisRate            float64
	SpreadRate           float64
	FeeRate              float64
	NotionalUSDT         float64
	DrawdownRate         float64
	LiquidityOK          bool
	ScanScoreRate        float64
	FuturesLegSpreadRate float64
	SpotLegSpreadRate    float64
	FuturesLegOK         bool
	SpotLegOK            bool
	FuturesFillRatio     float64
	SpotFillRatio        float64
	Notes                string
}

type backtestAggregate struct {
	Scenario      string
	Symbol        string
	Period        string
	Trades        int
	Wins          int
	FundingIncome float64
	TradingPnL    float64
	TotalFees     float64
	MaxDrawdown   float64
	Notes         []string
}

// NewBacktestRunner 创建最小可执行回测器。
func NewBacktestRunner(store *ReportStore, logger *log.Logger, cfg EngineConfig) *BacktestRunner {
	return &BacktestRunner{
		store:  store,
		logger: logger,
		cfg:    cfg,
	}
}

func (r *BacktestRunner) reportProgress(stage EngineStage, message string, current int, total int) {
	if r.progressHook != nil {
		r.progressHook(stage, message, current, total)
	}
}

// RunFromCSV 读取交易样本 CSV，聚合后写入 backtest_records.csv。
func (r *BacktestRunner) RunFromCSV(inputPath string) ([]BacktestCSVRecord, error) {
	samples, err := readBacktestTradeSamples(inputPath)
	if err != nil {
		return nil, err
	}
	records := buildBacktestRecords(r.cfg, samples)
	if r.store != nil {
		if err := r.store.ReplaceBacktestRecords(records); err != nil {
			return nil, err
		}
		if err := r.store.AppendBacktestRunSummaries("csv_trade", records); err != nil {
			return nil, err
		}
		if err := r.store.ReplaceResidualRiskRecords(nil); err != nil {
			return nil, err
		}
	}

	if r.logger != nil {
		r.logger.Printf("回测执行完成，输入=%s 输出记录数=%d", inputPath, len(records))
	}
	return records, nil
}

// RunReplayFromCSV 读取历史时点快照并回放生成回测结果。
func (r *BacktestRunner) RunReplayFromCSV(inputPath string) ([]BacktestCSVRecord, error) {
	replaySamples, err := readBacktestReplaySamples(inputPath)
	if err != nil {
		return nil, err
	}

	tradeSamples := replaySamplesToTradeSamples(replaySamples)
	records := buildBacktestRecords(r.cfg, tradeSamples)
	if r.store != nil {
		if err := r.store.ReplaceBacktestRecords(records); err != nil {
			return nil, err
		}
		if err := r.store.AppendBacktestRunSummaries("csv_replay", records); err != nil {
			return nil, err
		}
		if err := r.store.ReplaceResidualRiskRecords(nil); err != nil {
			return nil, err
		}
	}
	if r.logger != nil {
		r.logger.Printf("历史回放完成，输入=%s 输出记录数=%d", inputPath, len(records))
	}
	return records, nil
}

// RunTimelineFromCSV 读取按模块时序排列的事件流并回放生成回测结果。
func (r *BacktestRunner) RunTimelineFromCSV(inputPath string) ([]BacktestCSVRecord, error) {
	timelineSamples, err := readBacktestTimelineSamples(inputPath)
	if err != nil {
		return nil, err
	}

	replayResult := r.timelineSamplesToTradeSamples(timelineSamples)
	records := buildBacktestRecords(r.cfg, replayResult.Trades)
	if r.store != nil {
		if err := r.store.ReplaceBacktestRecords(records); err != nil {
			return nil, err
		}
		if err := r.store.AppendBacktestRunSummaries("csv_timeline", records); err != nil {
			return nil, err
		}
		if err := r.store.ReplaceResidualRiskRecords(replayResult.ResidualRisks); err != nil {
			return nil, err
		}
	}
	if r.logger != nil {
		r.logger.Printf("时序回放完成，输入=%s 输出记录数=%d 残腿风险数=%d", inputPath, len(records), len(replayResult.ResidualRisks))
	}
	return records, nil
}

func replaySamplesToTradeSamples(replaySamples []BacktestReplaySample) []BacktestTradeSample {
	tradeSamples := make([]BacktestTradeSample, 0, len(replaySamples))
	for _, sample := range replaySamples {
		if sample.NotionalUSDT <= 0 {
			continue
		}

		entryCostRate := math.Abs(sample.EntrySpreadRate)
		exitCostRate := math.Abs(sample.ExitSpreadRate)
		basisPenaltyRate := math.Abs(sample.BasisRate) * 0.50
		feeCostRate := math.Abs(sample.FeeRate)
		expectedNetRate := sample.FundingRate - entryCostRate - exitCostRate - basisPenaltyRate - feeCostRate

		if !sample.LiquidityOK || expectedNetRate <= 0 {
			continue
		}

		fundingIncome := sample.NotionalUSDT * sample.FundingRate
		tradingPnL := -sample.NotionalUSDT * (entryCostRate + exitCostRate + basisPenaltyRate)
		totalFees := sample.NotionalUSDT * feeCostRate
		netPnL := fundingIncome + tradingPnL - totalFees

		noteParts := make([]string, 0, 2)
		noteParts = append(noteParts, fmt.Sprintf("ts=%s netRate=%.6f", sample.Timestamp, expectedNetRate))
		if sample.Notes != "" {
			noteParts = append(noteParts, sample.Notes)
		}

		tradeSamples = append(tradeSamples, BacktestTradeSample{
			Scenario:      sample.Scenario,
			Symbol:        sample.Symbol,
			Period:        sample.Period,
			TradeID:       sample.Timestamp,
			FundingIncome: fundingIncome,
			TradingPnL:    tradingPnL,
			TotalFees:     totalFees,
			Win:           netPnL > 0,
			Drawdown:      -sample.NotionalUSDT * math.Abs(sample.DrawdownRate),
			Notes:         strings.Join(noteParts, " | "),
		})
	}
	return tradeSamples
}

type timelineReplayState struct {
	Scenario       string
	Symbol         string
	Period         string
	Selected       bool
	ScanPassed     bool
	Opened         bool
	Invalidated    bool
	PartialOpen    bool
	BreachCount    int
	MonitorState   string
	NotionalUSDT   float64
	EntryCostUSDT  float64
	ExitCostUSDT   float64
	TotalFeesUSDT  float64
	FundingIncome  float64
	MaxDrawdown    float64
	Notes          []string
	EntryTimestamp string
}

type timelineReplayResult struct {
	Trades        []BacktestTradeSample
	ResidualRisks []ResidualRiskCSVRecord
}

func (r *BacktestRunner) timelineSamplesToTradeSamples(samples []BacktestTimelineSample) timelineReplayResult {
	states := make(map[string]*timelineReplayState)
	order := make([]string, 0)
	selectedScanKeys := r.selectTimelineTopTargets(samples)
	residualRisks := make([]ResidualRiskCSVRecord, 0)

	for _, sample := range samples {
		key := strings.Join([]string{sample.Scenario, sample.Symbol, sample.Period}, "|")
		state, ok := states[key]
		if !ok {
			state = &timelineReplayState{
				Scenario:     sample.Scenario,
				Symbol:       sample.Symbol,
				Period:       sample.Period,
				MonitorState: "watching",
				MaxDrawdown:  0,
				Selected:     selectedScanKeys[key],
			}
			states[key] = state
			order = append(order, key)
		}

		if sample.NotionalUSDT > 0 {
			state.NotionalUSDT = sample.NotionalUSDT
		}
		drawdown := -math.Abs(sample.DrawdownRate) * sample.NotionalUSDT
		if drawdown < state.MaxDrawdown {
			state.MaxDrawdown = drawdown
		}

		switch strings.ToLower(sample.Phase) {
		case "scan":
			r.handleTimelineScan(state, sample)
		case "monitor":
			r.handleTimelineMonitor(state, sample)
		case "open":
			r.handleTimelineOpen(state, sample, &residualRisks)
		case "close":
			r.handleTimelineClose(state, sample, &residualRisks)
		}
	}

	trades := make([]BacktestTradeSample, 0, len(order))
	for _, key := range order {
		state := states[key]
		if !state.Opened || state.Invalidated || state.FundingIncome == 0 {
			continue
		}
		tradingPnL := -(state.EntryCostUSDT + state.ExitCostUSDT)
		netPnL := state.FundingIncome + tradingPnL - state.TotalFeesUSDT
		trades = append(trades, BacktestTradeSample{
			Scenario:      state.Scenario,
			Symbol:        state.Symbol,
			Period:        state.Period,
			TradeID:       nonEmpty(state.EntryTimestamp, state.Period),
			FundingIncome: state.FundingIncome,
			TradingPnL:    tradingPnL,
			TotalFees:     state.TotalFeesUSDT,
			Win:           netPnL > 0,
			Drawdown:      state.MaxDrawdown,
			Notes:         strings.Join(state.Notes, " | "),
		})
	}
	return timelineReplayResult{
		Trades:        trades,
		ResidualRisks: residualRisks,
	}
}

func (r *BacktestRunner) handleTimelineScan(state *timelineReplayState, sample BacktestTimelineSample) {
	if !state.Selected {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("scan_skip_topn@%s score=%.6f", sample.Timestamp, timelineScanScore(sample)))
		return
	}
	if sample.FundingRate < r.cfg.StrategyConfig.MinEstimatedFundingRate {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("scan_skip@%s 费率 %.6f 低于阈值", sample.Timestamp, sample.FundingRate))
		return
	}
	if math.Abs(sample.BasisRate) > r.cfg.StrategyConfig.MaxScanBasisRate {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("scan_skip@%s 基差 %.6f 超阈值", sample.Timestamp, sample.BasisRate))
		return
	}
	state.ScanPassed = true
	state.Notes = append(state.Notes, fmt.Sprintf("scan_pass@%s rate=%.6f basis=%.6f score=%.6f", sample.Timestamp, sample.FundingRate, sample.BasisRate, timelineScanScore(sample)))
}

func (r *BacktestRunner) handleTimelineMonitor(state *timelineReplayState, sample BacktestTimelineSample) {
	if !state.ScanPassed || state.Invalidated {
		return
	}
	if !sample.LiquidityOK {
		risk := EvaluateExecutionCoverageRisk("depth", 0)
		state.Invalidated = true
		state.MonitorState = "invalidated"
		state.Notes = append(state.Notes, fmt.Sprintf("monitor_invalidate@%s 深度不足 category=%s severity=%s action=%s", sample.Timestamp, risk.Category, risk.Severity, risk.Action))
		return
	}

	hcfg := r.historicalConfig()
	roundTripFeeRate := 2 * (hcfg.SpotTakerFeeRate + hcfg.FuturesTakerFeeRate)
	netProfitRate := estimateReservedNetProfitRate(sample.FundingRate, math.Abs(sample.SpreadRate), roundTripFeeRate, r.cfg.StrategyConfig.ExitSpreadReserveRate, 0)

	strategy := NewDefaultFundingStrategy()
	profile := GetSymbolRiskProfile(sample.Symbol)
	action, detail := strategy.EvaluateMonitorTick(FundingCandidate{Symbol: sample.Symbol}, netProfitRate, profile, r.cfg, state.BreachCount)

	if action == StrategyActionReject {
		state.Invalidated = true
		state.MonitorState = "invalidated"
		state.Notes = append(state.Notes, fmt.Sprintf("monitor_invalidate@%s net=%.6f detail=%s", sample.Timestamp, netProfitRate, detail))
		return
	}
	if action == StrategyActionObserve {
		state.BreachCount++
		state.MonitorState = "observing"
		state.Notes = append(state.Notes, fmt.Sprintf("monitor_observe@%s breach=%d net=%.6f detail=%s", sample.Timestamp, state.BreachCount, netProfitRate, detail))
		if r.cfg.RiskConfig.MonitorMaxConsecutiveBreach > 0 && state.BreachCount >= r.cfg.RiskConfig.MonitorMaxConsecutiveBreach {
			state.Invalidated = true
			state.MonitorState = "invalidated"
			state.Notes = append(state.Notes, fmt.Sprintf("monitor_max_breach@%s", sample.Timestamp))
		}
		return
	}

	state.BreachCount = 0
	state.MonitorState = "healthy"
	state.Notes = append(state.Notes, fmt.Sprintf("monitor_healthy@%s net=%.6f", sample.Timestamp, netProfitRate))
}

func (r *BacktestRunner) handleTimelineOpen(state *timelineReplayState, sample BacktestTimelineSample, residualRisks *[]ResidualRiskCSVRecord) {
	if !state.ScanPassed || state.Invalidated || state.Opened {
		return
	}
	if state.MonitorState != "healthy" {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("open_blocked@%s 当前状态=%s", sample.Timestamp, nonEmpty(state.MonitorState, "unknown")))
		return
	}
	if !sample.LiquidityOK {
		risk := EvaluateExecutionCoverageRisk("liquidity", 0)
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("open_invalidate@%s 开仓流动性不足 category=%s severity=%s action=%s", sample.Timestamp, risk.Category, risk.Severity, risk.Action))
		return
	}
	notional := effectiveNotional(state.NotionalUSDT, sample.NotionalUSDT)
	state.NotionalUSDT = notional
	futuresFilledUSDT := notional * clampFillRatio(sample.FuturesFillRatio)
	spotFilledUSDT := notional * clampFillRatio(sample.SpotFillRatio)
	residualUSDT := math.Abs(futuresFilledUSDT - spotFilledUSDT)
	if !sample.FuturesLegOK {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("open_futures_reject@%s", sample.Timestamp))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "open", 0, 0, notional, "合约腿拒单", "开仓阶段合约腿未成交"))
		return
	}
	state.EntryCostUSDT += futuresFilledUSDT * math.Abs(sample.FuturesLegSpreadRate)
	state.TotalFeesUSDT += futuresFilledUSDT * math.Abs(sample.FeeRate)
	if !sample.SpotLegOK {
		state.PartialOpen = true
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("open_spot_reject@%s 触发先合约后现货残腿风险", sample.Timestamp))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "open", futuresFilledUSDT, 0, futuresFilledUSDT, "现货腿拒单", "先合约成交后现货拒单"))
		return
	}
	if residualUSDT > 1e-9 {
		risk := EvaluateExecutionCoverageRisk("order_uncertainty", math.Min(clampFillRatio(sample.FuturesFillRatio), clampFillRatio(sample.SpotFillRatio)))
		state.PartialOpen = true
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("open_mismatch@%s fut=%.2f spot=%.2f residual=%.2f category=%s severity=%s action=%s", sample.Timestamp, futuresFilledUSDT, spotFilledUSDT, residualUSDT, risk.Category, risk.Severity, risk.Action))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "open", futuresFilledUSDT, spotFilledUSDT, residualUSDT, "开仓双腿数量不一致", "先合约后现货成交数量不一致"))
		return
	}
	state.EntryCostUSDT += spotFilledUSDT * math.Abs(sample.SpotLegSpreadRate)
	state.TotalFeesUSDT += spotFilledUSDT * math.Abs(sample.FeeRate)
	state.Opened = true
	state.EntryTimestamp = sample.Timestamp
	state.Notes = append(state.Notes, fmt.Sprintf("open@%s fut=%.6f spot=%.6f qty=%.2f", sample.Timestamp, sample.FuturesLegSpreadRate, sample.SpotLegSpreadRate, futuresFilledUSDT))
}

func (r *BacktestRunner) handleTimelineClose(state *timelineReplayState, sample BacktestTimelineSample, residualRisks *[]ResidualRiskCSVRecord) {
	if !state.Opened || state.Invalidated || state.PartialOpen {
		return
	}
	if !sample.LiquidityOK {
		risk := EvaluateExecutionCoverageRisk("liquidity", 0)
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("close_invalidate@%s 平仓流动性不足 category=%s severity=%s action=%s", sample.Timestamp, risk.Category, risk.Severity, risk.Action))
		return
	}
	notional := effectiveNotional(state.NotionalUSDT, sample.NotionalUSDT)
	state.NotionalUSDT = notional
	futuresFilledUSDT := notional * clampFillRatio(sample.FuturesFillRatio)
	spotFilledUSDT := notional * clampFillRatio(sample.SpotFillRatio)
	residualUSDT := math.Abs(futuresFilledUSDT - spotFilledUSDT)
	if !sample.FuturesLegOK {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("close_futures_reject@%s", sample.Timestamp))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "close", 0, 0, notional, "平仓合约腿拒单", "平仓阶段合约腿未成交"))
		return
	}
	state.ExitCostUSDT += futuresFilledUSDT * math.Abs(sample.FuturesLegSpreadRate)
	state.TotalFeesUSDT += futuresFilledUSDT * math.Abs(sample.FeeRate)
	if !sample.SpotLegOK {
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("close_spot_reject@%s", sample.Timestamp))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "close", futuresFilledUSDT, 0, futuresFilledUSDT, "平仓现货腿拒单", "平仓阶段现货腿未成交"))
		return
	}
	if residualUSDT > 1e-9 {
		risk := EvaluateExecutionCoverageRisk("order_uncertainty", math.Min(clampFillRatio(sample.FuturesFillRatio), clampFillRatio(sample.SpotFillRatio)))
		state.Invalidated = true
		state.Notes = append(state.Notes, fmt.Sprintf("close_mismatch@%s fut=%.2f spot=%.2f residual=%.2f category=%s severity=%s action=%s", sample.Timestamp, futuresFilledUSDT, spotFilledUSDT, residualUSDT, risk.Category, risk.Severity, risk.Action))
		*residualRisks = append(*residualRisks, buildResidualRiskRecord(state, sample, "close", futuresFilledUSDT, spotFilledUSDT, residualUSDT, "平仓双腿数量不一致", "平仓阶段双腿成交数量不一致"))
		return
	}
	state.ExitCostUSDT += spotFilledUSDT * math.Abs(sample.SpotLegSpreadRate)
	state.TotalFeesUSDT += spotFilledUSDT * math.Abs(sample.FeeRate)
	state.FundingIncome = futuresFilledUSDT * sample.FundingRate
	state.Notes = append(state.Notes, fmt.Sprintf("close@%s funding=%.6f fut=%.6f spot=%.6f qty=%.2f", sample.Timestamp, sample.FundingRate, sample.FuturesLegSpreadRate, sample.SpotLegSpreadRate, futuresFilledUSDT))
}

func effectiveNotional(current float64, incoming float64) float64 {
	if current > 0 {
		return current
	}
	return incoming
}

func phaseOrder(phase string) int {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "scan":
		return 0
	case "monitor":
		return 1
	case "open":
		return 2
	case "close":
		return 3
	default:
		return 4
	}
}

func (r *BacktestRunner) selectTimelineTopTargets(samples []BacktestTimelineSample) map[string]bool {
	type rankedSample struct {
		key   string
		score float64
	}

	grouped := make(map[string][]rankedSample)
	for _, sample := range samples {
		if strings.ToLower(strings.TrimSpace(sample.Phase)) != "scan" {
			continue
		}
		groupKey := strings.Join([]string{sample.Scenario, sample.Period, sample.Timestamp}, "|")
		targetKey := strings.Join([]string{sample.Scenario, sample.Symbol, sample.Period}, "|")
		grouped[groupKey] = append(grouped[groupKey], rankedSample{
			key:   targetKey,
			score: timelineScanScore(sample),
		})
	}

	selected := make(map[string]bool)
	for _, group := range grouped {
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].score == group[j].score {
				return group[i].key < group[j].key
			}
			return group[i].score > group[j].score
		})
		limit := r.cfg.StrategyConfig.MaxTargets
		if limit <= 0 || limit > len(group) {
			limit = len(group)
		}
		for idx := 0; idx < limit; idx++ {
			selected[group[idx].key] = true
		}
	}
	return selected
}

func timelineScanScore(sample BacktestTimelineSample) float64 {
	if sample.ScanScoreRate != 0 {
		return sample.ScanScoreRate
	}
	return sample.FundingRate - math.Abs(sample.BasisRate) - math.Abs(sample.SpreadRate) - math.Abs(sample.FeeRate)
}

func buildResidualRiskRecord(state *timelineReplayState, sample BacktestTimelineSample, phase string, futuresFilledUSDT float64, spotFilledUSDT float64, residualUSDT float64, reason string, detail string) ResidualRiskCSVRecord {
	emergencyHedgeCost, emergencyHedgeFee, emergencyHedgePnL := estimateEmergencyHedgePnL(sample, residualUSDT)
	severity := classifyResidualRiskSeverity(residualUSDT, emergencyHedgePnL)
	notes := make([]string, 0, 2)
	if strings.TrimSpace(detail) != "" {
		notes = append(notes, strings.TrimSpace(detail))
	}
	if strings.TrimSpace(sample.Notes) != "" {
		notes = append(notes, strings.TrimSpace(sample.Notes))
	}
	return ResidualRiskCSVRecord{
		RecordedAt:         time.Now().Format(time.RFC3339),
		Scenario:           sample.Scenario,
		Symbol:             sample.Symbol,
		Period:             sample.Period,
		Phase:              phase,
		EventTime:          sample.Timestamp,
		MonitorState:       nonEmpty(state.MonitorState, "unknown"),
		FuturesLegOK:       sample.FuturesLegOK,
		SpotLegOK:          sample.SpotLegOK,
		FuturesFillRatio:   formatFloat(clampFillRatio(sample.FuturesFillRatio)),
		SpotFillRatio:      formatFloat(clampFillRatio(sample.SpotFillRatio)),
		FuturesFilledUSDT:  formatFloat(futuresFilledUSDT),
		SpotFilledUSDT:     formatFloat(spotFilledUSDT),
		ResidualUSDT:       formatFloat(residualUSDT),
		EmergencyHedgeCost: formatFloat(emergencyHedgeCost),
		EmergencyHedgeFee:  formatFloat(emergencyHedgeFee),
		EmergencyHedgePnL:  formatFloat(emergencyHedgePnL),
		Severity:           severity,
		Reason:             reason,
		Notes:              strings.Join(notes, " | "),
	}
}

func estimateEmergencyHedgePnL(sample BacktestTimelineSample, residualUSDT float64) (float64, float64, float64) {
	if residualUSDT <= 0 {
		return 0, 0, 0
	}
	emergencySpreadRate := math.Max(math.Abs(sample.FuturesLegSpreadRate), math.Abs(sample.SpotLegSpreadRate)) + math.Abs(sample.SpreadRate)
	emergencyHedgeCost := residualUSDT * emergencySpreadRate
	emergencyHedgeFee := residualUSDT * math.Abs(sample.FeeRate)
	return emergencyHedgeCost, emergencyHedgeFee, -(emergencyHedgeCost + emergencyHedgeFee)
}

func classifyResidualRiskSeverity(residualUSDT float64, emergencyHedgePnL float64) string {
	switch {
	case residualUSDT >= 200 || emergencyHedgePnL <= -0.20:
		return "high"
	case residualUSDT >= 50 || emergencyHedgePnL <= -0.05:
		return "medium"
	default:
		return "low"
	}
}

func clampFillRatio(raw float64) float64 {
	switch {
	case raw <= 0:
		return 0
	case raw > 1:
		return 1
	default:
		return raw
	}
}

func defaultFillRatio(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 1
	}
	return clampFillRatio(parseFloat(text))
}

func buildBacktestRecords(cfg EngineConfig, samples []BacktestTradeSample) []BacktestCSVRecord {
	aggregates := make(map[string]*backtestAggregate)
	for _, sample := range samples {
		key := strings.Join([]string{sample.Scenario, sample.Symbol, sample.Period}, "|")
		aggregate, ok := aggregates[key]
		if !ok {
			aggregate = &backtestAggregate{
				Scenario:    sample.Scenario,
				Symbol:      sample.Symbol,
				Period:      sample.Period,
				MaxDrawdown: 0,
			}
			aggregates[key] = aggregate
		}

		aggregate.Trades++
		if sample.Win {
			aggregate.Wins++
		}
		aggregate.FundingIncome += sample.FundingIncome
		aggregate.TradingPnL += sample.TradingPnL
		aggregate.TotalFees += sample.TotalFees
		if sample.Drawdown < aggregate.MaxDrawdown {
			aggregate.MaxDrawdown = sample.Drawdown
		}
		if sample.Notes != "" {
			aggregate.Notes = append(aggregate.Notes, sample.Notes)
		}
	}

	keys := make([]string, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	records := make([]BacktestCSVRecord, 0, len(keys))
	recordedAt := time.Now().Format(time.RFC3339)
	metadata := buildTraceRecordMetadata(cfg)
	for _, key := range keys {
		aggregate := aggregates[key]
		winRate := 0.0
		if aggregate.Trades > 0 {
			winRate = float64(aggregate.Wins) / float64(aggregate.Trades)
		}
		netPnL := aggregate.FundingIncome + aggregate.TradingPnL - aggregate.TotalFees
		records = append(records, BacktestCSVRecord{
			TraceRecordMetadata: metadata,
			RecordedAt:          recordedAt,
			Scenario:            aggregate.Scenario,
			Symbol:              aggregate.Symbol,
			Period:              aggregate.Period,
			Trades:              aggregate.Trades,
			WinRate:             formatFloat(winRate),
			FundingIncome:       formatFloat(aggregate.FundingIncome),
			TradingPnL:          formatFloat(aggregate.TradingPnL),
			TotalFees:           formatFloat(aggregate.TotalFees),
			NetPnL:              formatFloat(netPnL),
			MaxDrawdown:         formatFloat(aggregate.MaxDrawdown),
			Notes:               strings.Join(aggregate.Notes, " | "),
		})
	}
	return records
}
