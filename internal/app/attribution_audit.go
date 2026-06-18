package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/core"
)

const (
	attributionPredictionsFileName = "attribution_predictions.jsonl"
	attributionRealizedFileName    = "attribution_realized.jsonl"
	attributionReportFileName      = "attribution_report.md"
)

// AttributionRunMode 是研究归因用的运行模式标记(供 go/no-go 过滤,不参与交易决策)。
type AttributionRunMode string

const (
	AttributionRunModeCollectData   AttributionRunMode = "collect_data"
	AttributionRunModeCanaryProfit  AttributionRunMode = "canary_profit"
	AttributionRunModeProfitSeeking AttributionRunMode = "profit_seeking"
)

// 预注册 go/no-go 阈值 — 事后不得按回测/实盘结果修改,仅允许独立校准流程更新常量。
const (
	GoNoGoMinSampleCount                 = 200
	GoNoGoMinPeriodWeeks                 = 4
	GoNoGoMinCapitalWeightedNetAnnReturn = 0.10 // 占用资金加权净年化 GO 线 > +10%
	GoNoGoMaxDrawdownRatio               = 0.10 // 最大回撤 < 策略分配资金 10%
	GoNoGoMinAttributionCompleteRatio    = 0.95
	GoNoGoMaxUnexplainedPnLRatio         = 0.05 // unexplained_pnl < 毛 PnL 5%
)

// GoNoGoVerdict 是单策略 go/no-go 判定结果。
type GoNoGoVerdict string

const (
	GoNoGoVerdictGO                 GoNoGoVerdict = "GO"
	GoNoGoVerdictNOGO               GoNoGoVerdict = "NO-GO"
	GoNoGoVerdictInsufficientSample GoNoGoVerdict = "INSUFFICIENT_SAMPLE"
)

// currentAttributionRunMode 当前归因运行模式(由 loadEngineConfig → initAttributionRunMode 从 strategy_mode 注入)。
var currentAttributionRunMode = AttributionRunModeCollectData

// initAttributionRunMode 从统一策略模式设置归因标签(与 core.StrategyMode 一一对应)。
func initAttributionRunMode(mode core.StrategyMode) {
	switch mode {
	case core.StrategyModeCanaryProfit:
		currentAttributionRunMode = AttributionRunModeCanaryProfit
	case core.StrategyModeProfitSeeking:
		currentAttributionRunMode = AttributionRunModeProfitSeeking
	default:
		currentAttributionRunMode = AttributionRunModeCollectData
	}
}

// CurrentAttributionRunMode 返回当前归因运行模式标记。
func CurrentAttributionRunMode() AttributionRunMode {
	return currentAttributionRunMode
}

func auditRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "audit")
}

func attributionPredictionsPath(recordDir string) string {
	return filepath.Join(auditRecordDir(recordDir), attributionPredictionsFileName)
}

func attributionRealizedPath(recordDir string) string {
	return filepath.Join(auditRecordDir(recordDir), attributionRealizedFileName)
}

func attributionReportPath(recordDir string) string {
	return filepath.Join(auditRecordDir(recordDir), attributionReportFileName)
}

// AttributionPredictionRecord 是机会实际开仓时的 prediction 归因记录。
type AttributionPredictionRecord struct {
	RecordedAt            time.Time `json:"recorded_at"`
	OpportunityID         string    `json:"opportunity_id"`
	Strategy              string    `json:"strategy"`
	Symbol                string    `json:"symbol"`
	Venues                []string  `json:"venues,omitempty"`
	OpenTime              time.Time `json:"open_time"`
	ExpectedFundingUSDT   float64   `json:"expected_funding_usdt,omitempty"`
	ExpectedOpenFeesUSDT  float64   `json:"expected_open_fees_usdt,omitempty"`
	ExpectedCloseFeesUSDT float64   `json:"expected_close_fees_usdt,omitempty"`
	ExpectedSlippageUSDT  float64   `json:"expected_slippage_usdt,omitempty"`
	ExpectedBorrowUSDT    float64   `json:"expected_borrow_usdt,omitempty"`
	ExpectedNetEVUSDT     float64   `json:"expected_net_ev_usdt,omitempty"`
	RunMode               string    `json:"run_mode"`
}

// AttributionRealizedRecord 是平仓 finalize 时的 realized 归因记录。
type AttributionRealizedRecord struct {
	RecordedAt           time.Time `json:"recorded_at"`
	OpportunityID        string    `json:"opportunity_id"`
	Strategy             string    `json:"strategy"`
	Symbol               string    `json:"symbol"`
	Venues               []string  `json:"venues,omitempty"`
	CloseTime            time.Time `json:"close_time"`
	RealizedFundingUSDT  float64   `json:"realized_funding_usdt"`
	RealizedTradePnLUSDT float64   `json:"realized_trade_pnl_usdt,omitempty"`
	RealizedFeesUSDT     float64   `json:"realized_fees_usdt"`
	RealizedSlippageUSDT float64   `json:"realized_slippage_usdt,omitempty"`
	RealizedBorrowUSDT   float64   `json:"realized_borrow_usdt,omitempty"`
	FinalNetPnLUSDT      float64   `json:"final_net_pnl_usdt"`
	FundingTruthStatus   string    `json:"funding_truth_status,omitempty"`
	AttributionComplete  bool      `json:"attribution_complete"`
	RunMode              string    `json:"run_mode"`
	NotionalUSDT         float64   `json:"notional_usdt,omitempty"`
	OpenTime             time.Time `json:"open_time,omitempty"`
}

func venuesFromUnifiedOpportunity(opp UnifiedOpportunity) []string {
	venues := make([]string, 0, len(opp.Legs))
	seen := make(map[string]struct{}, len(opp.Legs))
	for _, leg := range opp.Legs {
		v := strings.TrimSpace(leg.Venue)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		venues = append(venues, v)
	}
	if len(venues) == 0 {
		return nil
	}
	return venues
}

func predictionRecordFromOpportunity(opp UnifiedOpportunity, openTime time.Time) AttributionPredictionRecord {
	if openTime.IsZero() {
		openTime = time.Now().UTC()
	}
	return AttributionPredictionRecord{
		RecordedAt:            time.Now().UTC(),
		OpportunityID:         opp.ID,
		Strategy:              opp.StrategyType,
		Symbol:                opp.Symbol,
		Venues:                venuesFromUnifiedOpportunity(opp),
		OpenTime:              openTime,
		ExpectedFundingUSDT:   opp.ExpectedFundingUSDT,
		ExpectedOpenFeesUSDT:  opp.ExpectedOpenCostUSDT,
		ExpectedCloseFeesUSDT: opp.ExpectedCloseCostUSDT,
		ExpectedSlippageUSDT:  opp.ExpectedSlippageUSDT,
		ExpectedBorrowUSDT:    opp.ExpectedBorrowCostUSDT,
		ExpectedNetEVUSDT:     opp.ExpectedNetEVUSDT,
		RunMode:               string(CurrentAttributionRunMode()),
	}
}

func appendAttributionPredictionBestEffort(recordDir string, rec AttributionPredictionRecord) {
	if strings.TrimSpace(rec.OpportunityID) == "" {
		return
	}
	_ = appendJSONLRecord(attributionPredictionsPath(recordDir), rec)
}

func appendAttributionRealizedBestEffort(recordDir string, rec AttributionRealizedRecord) {
	if strings.TrimSpace(rec.OpportunityID) == "" {
		return
	}
	_ = appendJSONLRecord(attributionRealizedPath(recordDir), rec)
}

func realizedRecordFromCrossVenueSettlement(rec CrossVenueSettlement) AttributionRealizedRecord {
	oppID := unifiedOpportunityID(UnifiedStrategyPerpPerp, rec.Symbol, rec.EntrySnapshotID, rec.HighVenue, rec.LowVenue)
	slippage := rec.OpenSlippage + rec.CloseSlippage
	return AttributionRealizedRecord{
		RecordedAt:           time.Now().UTC(),
		OpportunityID:        oppID,
		Strategy:             UnifiedStrategyPerpPerp,
		Symbol:               rec.Symbol,
		Venues:               []string{rec.HighVenue, rec.LowVenue},
		CloseTime:            rec.CloseTime,
		OpenTime:             rec.OpenTime,
		NotionalUSDT:         rec.NotionalUSDT,
		RealizedFundingUSDT:  rec.RealizedFunding,
		RealizedTradePnLUSDT: rec.TradePnL,
		RealizedFeesUSDT:     rec.TotalFees,
		RealizedSlippageUSDT: slippage,
		FinalNetPnLUSDT:      rec.NetPnL,
		FundingTruthStatus:   rec.FundingTruthStatus,
		AttributionComplete:  !rec.AttributionIncomplete,
		RunMode:              string(CurrentAttributionRunMode()),
	}
}

func realizedRecordFromSVSPSettlement(rec SingleVenueSpotPerpSettlement) AttributionRealizedRecord {
	return AttributionRealizedRecord{
		RecordedAt:           time.Now().UTC(),
		OpportunityID:        rec.OpportunityID,
		Strategy:             UnifiedStrategySingleVenueSpotPerp,
		Symbol:               rec.Symbol,
		Venues:               venueList(rec.Venue),
		CloseTime:            rec.CloseTime,
		OpenTime:             rec.OpenTime,
		NotionalUSDT:         rec.NotionalUSDT,
		RealizedFundingUSDT:  rec.RealizedFunding,
		RealizedTradePnLUSDT: rec.TradePnL,
		RealizedFeesUSDT:     rec.TotalFees,
		FinalNetPnLUSDT:      rec.NetPnL,
		AttributionComplete:  rec.OpportunityID != "",
		RunMode:              string(CurrentAttributionRunMode()),
	}
}

func realizedRecordFromSVPSSettlement(rec SingleVenuePerpSpotSettlement) AttributionRealizedRecord {
	return AttributionRealizedRecord{
		RecordedAt:           time.Now().UTC(),
		OpportunityID:        rec.OpportunityID,
		Strategy:             UnifiedStrategySingleVenuePerpSpot,
		Symbol:               rec.Symbol,
		Venues:               venueList(rec.Venue),
		CloseTime:            rec.CloseTime,
		OpenTime:             rec.OpenTime,
		NotionalUSDT:         rec.NotionalUSDT,
		RealizedFundingUSDT:  rec.RealizedFunding,
		RealizedTradePnLUSDT: rec.TradePnL,
		RealizedFeesUSDT:     rec.TotalFees,
		RealizedBorrowUSDT:   rec.BorrowCost,
		FinalNetPnLUSDT:      rec.NetPnL,
		AttributionComplete:  rec.OpportunityID != "",
		RunMode:              string(CurrentAttributionRunMode()),
	}
}

func realizedRecordFromCVSPSettlement(rec CrossVenueSpotPerpSettlement) AttributionRealizedRecord {
	return AttributionRealizedRecord{
		RecordedAt:          time.Now().UTC(),
		OpportunityID:       rec.OpportunityID,
		Strategy:            UnifiedStrategyCrossVenueSpotPerp,
		Symbol:              rec.Symbol,
		Venues:              []string{rec.SpotVenue, rec.PerpVenue},
		CloseTime:           rec.CloseTime,
		OpenTime:            rec.OpenTime,
		NotionalUSDT:        rec.NotionalUSDT,
		RealizedFundingUSDT: rec.RealizedFunding,
		RealizedFeesUSDT:    rec.TotalFees,
		FinalNetPnLUSDT:     rec.NetPnL,
		AttributionComplete: rec.OpportunityID != "",
		RunMode:             string(CurrentAttributionRunMode()),
	}
}

func realizedRecordFromCVPSSettlement(rec CrossVenuePerpSpotSettlement) AttributionRealizedRecord {
	return AttributionRealizedRecord{
		RecordedAt:          time.Now().UTC(),
		OpportunityID:       rec.OpportunityID,
		Strategy:            UnifiedStrategyCrossVenuePerpSpot,
		Symbol:              rec.Symbol,
		Venues:              []string{rec.PerpVenue, rec.SpotVenue},
		CloseTime:           rec.CloseTime,
		OpenTime:            rec.OpenTime,
		NotionalUSDT:        rec.NotionalUSDT,
		RealizedFundingUSDT: rec.RealizedFunding,
		RealizedFeesUSDT:    rec.TotalFees,
		FinalNetPnLUSDT:     rec.NetPnL,
		AttributionComplete: rec.OpportunityID != "",
		RunMode:             string(CurrentAttributionRunMode()),
	}
}

func venueList(venueName string) []string {
	v := strings.TrimSpace(venueName)
	if v == "" {
		return nil
	}
	return []string{v}
}

// AttributionReconcileRow 是按维度聚合的 prediction vs realized 对账行。
type AttributionReconcileRow struct {
	DimensionKind           string  `json:"dimension_kind"`
	DimensionValue          string  `json:"dimension_value"`
	SampleCount             int     `json:"sample_count"`
	PredictedNetEVUSDT      float64 `json:"predicted_net_ev_usdt"`
	RealizedNetPnLUSDT      float64 `json:"realized_net_pnl_usdt"`
	NetDiffUSDT             float64 `json:"net_diff_usdt"`
	PredictedFundingUSDT    float64 `json:"predicted_funding_usdt"`
	RealizedFundingUSDT     float64 `json:"realized_funding_usdt"`
	FundingDiffUSDT         float64 `json:"funding_diff_usdt"`
	AttributionCompleteRate float64 `json:"attribution_complete_rate"`
}

// AttributionGoNoGoStrategyInput 是单策略 go/no-go 评估输入(纯函数,不含 IO)。
type AttributionGoNoGoStrategyInput struct {
	Strategy                 string
	Realized                 []AttributionRealizedRecord
	PredictionsByOpportunity map[string]AttributionPredictionRecord
	CapitalAllocatedUSDT     float64
}

// AttributionGoNoGoStrategyResult 是单策略 go/no-go 评估输出。
type AttributionGoNoGoStrategyResult struct {
	Strategy                 string        `json:"strategy"`
	Verdict                  GoNoGoVerdict `json:"verdict"`
	Reasons                  []string      `json:"reasons"`
	AlphaSampleCount         int           `json:"alpha_sample_count"`
	PeriodWeeks              float64       `json:"period_weeks"`
	CapitalWeightedNetAnnRet float64       `json:"capital_weighted_net_ann_return"`
	MaxDrawdownRatio         float64       `json:"max_drawdown_ratio"`
	AttributionCompleteRatio float64       `json:"attribution_complete_ratio"`
	UnexplainedPnLRatio      float64       `json:"unexplained_pnl_ratio"`
}

func isAlphaAttributionRunMode(mode string) bool {
	return AttributionRunMode(strings.TrimSpace(mode)) == AttributionRunModeProfitSeeking
}

func filterAlphaRealizedSamples(records []AttributionRealizedRecord) []AttributionRealizedRecord {
	out := make([]AttributionRealizedRecord, 0, len(records))
	for _, rec := range records {
		if !isAlphaAttributionRunMode(rec.RunMode) {
			continue
		}
		if !rec.AttributionComplete {
			continue
		}
		// 与 settlement_trust 口径一致:legacy 边界 + 异常 |trade_pnl| 不进 alpha/go-no-go。
		if !isTrustedAttributionRealized(rec) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func grossPnLUSDT(rec AttributionRealizedRecord) float64 {
	return math.Abs(rec.RealizedFundingUSDT) + math.Abs(rec.RealizedTradePnLUSDT)
}

func explainedNetPnLUSDT(rec AttributionRealizedRecord) float64 {
	return rec.RealizedFundingUSDT + rec.RealizedTradePnLUSDT - rec.RealizedFeesUSDT -
		rec.RealizedSlippageUSDT - rec.RealizedBorrowUSDT
}

func holdYears(openTime, closeTime time.Time) float64 {
	if openTime.IsZero() || closeTime.IsZero() || !closeTime.After(openTime) {
		return 1.0 / 365.0
	}
	return closeTime.Sub(openTime).Hours() / (24 * 365)
}

func capitalWeightedNetAnnReturn(samples []AttributionRealizedRecord) float64 {
	var weightedSum, totalWeight float64
	for _, rec := range samples {
		capital := rec.NotionalUSDT
		if capital <= 0 {
			continue
		}
		years := holdYears(rec.OpenTime, rec.CloseTime)
		ann := (rec.FinalNetPnLUSDT / capital) / years
		weightedSum += ann * capital
		totalWeight += capital
	}
	if totalWeight <= 0 {
		return 0
	}
	return weightedSum / totalWeight
}

func maxDrawdownRatio(samples []AttributionRealizedRecord, capitalAllocated float64) float64 {
	if capitalAllocated <= 0 {
		return 0
	}
	sorted := append([]AttributionRealizedRecord(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CloseTime.Before(sorted[j].CloseTime)
	})
	peak := 0.0
	equity := 0.0
	maxDD := 0.0
	for _, rec := range sorted {
		equity += rec.FinalNetPnLUSDT
		if equity > peak {
			peak = equity
		}
		dd := peak - equity
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD / capitalAllocated
}

func periodWeeks(samples []AttributionRealizedRecord) float64 {
	if len(samples) == 0 {
		return 0
	}
	minT, maxT := samples[0].CloseTime, samples[0].CloseTime
	for _, rec := range samples[1:] {
		if rec.CloseTime.Before(minT) {
			minT = rec.CloseTime
		}
		if rec.CloseTime.After(maxT) {
			maxT = rec.CloseTime
		}
	}
	if maxT.Before(minT) {
		return 0
	}
	return maxT.Sub(minT).Hours() / (24 * 7)
}

func attributionCompleteRatio(samples []AttributionRealizedRecord) float64 {
	if len(samples) == 0 {
		return 0
	}
	complete := 0
	for _, rec := range samples {
		if rec.AttributionComplete {
			complete++
		}
	}
	return float64(complete) / float64(len(samples))
}

func unexplainedPnLRatio(samples []AttributionRealizedRecord, predictions map[string]AttributionPredictionRecord) float64 {
	var gross, unexplained float64
	for _, rec := range samples {
		gross += grossPnLUSDT(rec)
		pred, ok := predictions[rec.OpportunityID]
		if ok {
			unexplained += math.Abs(pred.ExpectedNetEVUSDT - rec.FinalNetPnLUSDT)
			continue
		}
		unexplained += math.Abs(rec.FinalNetPnLUSDT - explainedNetPnLUSDT(rec))
	}
	if gross <= 0 {
		return 0
	}
	return unexplained / gross
}

// EvaluateAttributionGoNoGo 对单策略样本做预注册 go/no-go 判定(纯函数,不触发交易行为)。
func EvaluateAttributionGoNoGo(in AttributionGoNoGoStrategyInput) AttributionGoNoGoStrategyResult {
	alpha := filterAlphaRealizedSamples(in.Realized)
	res := AttributionGoNoGoStrategyResult{
		Strategy:         in.Strategy,
		AlphaSampleCount: len(alpha),
	}
	if len(alpha) < GoNoGoMinSampleCount {
		res.Verdict = GoNoGoVerdictInsufficientSample
		res.Reasons = append(res.Reasons, fmt.Sprintf("alpha 样本数 %d < 预注册最小样本数 %d", len(alpha), GoNoGoMinSampleCount))
		return res
	}
	res.PeriodWeeks = periodWeeks(alpha)
	if res.PeriodWeeks < float64(GoNoGoMinPeriodWeeks) {
		res.Verdict = GoNoGoVerdictInsufficientSample
		res.Reasons = append(res.Reasons, fmt.Sprintf("样本周期 %.1f 周 < 预注册最短周期 %d 周", res.PeriodWeeks, GoNoGoMinPeriodWeeks))
		return res
	}
	res.CapitalWeightedNetAnnRet = capitalWeightedNetAnnReturn(alpha)
	res.MaxDrawdownRatio = maxDrawdownRatio(alpha, in.CapitalAllocatedUSDT)
	res.AttributionCompleteRatio = attributionCompleteRatio(alpha)
	res.UnexplainedPnLRatio = unexplainedPnLRatio(alpha, in.PredictionsByOpportunity)

	var failReasons []string
	if res.CapitalWeightedNetAnnRet <= GoNoGoMinCapitalWeightedNetAnnReturn {
		failReasons = append(failReasons, fmt.Sprintf("占用资金加权净年化 %.2f%% <= GO 线 %.0f%%", res.CapitalWeightedNetAnnRet*100, GoNoGoMinCapitalWeightedNetAnnReturn*100))
	}
	if in.CapitalAllocatedUSDT > 0 && res.MaxDrawdownRatio >= GoNoGoMaxDrawdownRatio {
		failReasons = append(failReasons, fmt.Sprintf("最大回撤占比 %.2f%% >= 上限 %.0f%%", res.MaxDrawdownRatio*100, GoNoGoMaxDrawdownRatio*100))
	}
	if res.AttributionCompleteRatio < GoNoGoMinAttributionCompleteRatio {
		failReasons = append(failReasons, fmt.Sprintf("attribution_complete 比例 %.2f%% < 下限 %.0f%%", res.AttributionCompleteRatio*100, GoNoGoMinAttributionCompleteRatio*100))
	}
	if res.UnexplainedPnLRatio >= GoNoGoMaxUnexplainedPnLRatio {
		failReasons = append(failReasons, fmt.Sprintf("unexplained_pnl 占比 %.2f%% >= 上限 %.0f%%", res.UnexplainedPnLRatio*100, GoNoGoMaxUnexplainedPnLRatio*100))
	}
	if len(failReasons) > 0 {
		res.Verdict = GoNoGoVerdictNOGO
		res.Reasons = failReasons
		return res
	}
	res.Verdict = GoNoGoVerdictGO
	res.Reasons = []string{"全部预注册 alpha 判据通过"}
	return res
}

func aggregateAttributionByDimension(
	kind string,
	keyFn func(AttributionRealizedRecord, map[string]AttributionPredictionRecord) string,
	realized []AttributionRealizedRecord,
	predictions map[string]AttributionPredictionRecord,
) []AttributionReconcileRow {
	type acc struct {
		row AttributionReconcileRow
	}
	buckets := map[string]*acc{}
	for _, rec := range realized {
		key := keyFn(rec, predictions)
		if key == "" {
			continue
		}
		b, ok := buckets[key]
		if !ok {
			b = &acc{row: AttributionReconcileRow{
				DimensionKind:  kind,
				DimensionValue: key,
			}}
			buckets[key] = b
		}
		b.row.SampleCount++
		b.row.RealizedNetPnLUSDT += rec.FinalNetPnLUSDT
		b.row.RealizedFundingUSDT += rec.RealizedFundingUSDT
		if rec.AttributionComplete {
			b.row.AttributionCompleteRate += 1
		}
		if pred, ok := predictions[rec.OpportunityID]; ok {
			b.row.PredictedNetEVUSDT += pred.ExpectedNetEVUSDT
			b.row.PredictedFundingUSDT += pred.ExpectedFundingUSDT
		}
	}
	out := make([]AttributionReconcileRow, 0, len(buckets))
	for _, b := range buckets {
		if b.row.SampleCount > 0 {
			b.row.AttributionCompleteRate /= float64(b.row.SampleCount)
		}
		b.row.NetDiffUSDT = b.row.PredictedNetEVUSDT - b.row.RealizedNetPnLUSDT
		b.row.FundingDiffUSDT = b.row.PredictedFundingUSDT - b.row.RealizedFundingUSDT
		out = append(out, b.row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DimensionKind != out[j].DimensionKind {
			return out[i].DimensionKind < out[j].DimensionKind
		}
		return out[i].DimensionValue < out[j].DimensionValue
	})
	return out
}

// BuildAttributionReconcileRows 聚合 prediction vs realized(纯函数)。
func BuildAttributionReconcileRows(
	predictions []AttributionPredictionRecord,
	realized []AttributionRealizedRecord,
) []AttributionReconcileRow {
	predByID := make(map[string]AttributionPredictionRecord, len(predictions))
	for _, pred := range predictions {
		if pred.OpportunityID == "" {
			continue
		}
		predByID[pred.OpportunityID] = pred
	}
	var rows []AttributionReconcileRow
	rows = append(rows, aggregateAttributionByDimension("strategy", func(rec AttributionRealizedRecord, _ map[string]AttributionPredictionRecord) string {
		return rec.Strategy
	}, realized, predByID)...)
	rows = append(rows, aggregateAttributionByDimension("symbol", func(rec AttributionRealizedRecord, _ map[string]AttributionPredictionRecord) string {
		return rec.Symbol
	}, realized, predByID)...)
	rows = append(rows, aggregateAttributionByDimension("venue", func(rec AttributionRealizedRecord, _ map[string]AttributionPredictionRecord) string {
		if len(rec.Venues) == 0 {
			return ""
		}
		return strings.Join(rec.Venues, "+")
	}, realized, predByID)...)
	return rows
}

// WriteAttributionReport 读取 jsonl 并生成 attribution_report.md(非热路径,可手动/周期触发)。
func WriteAttributionReport(recordDir string) error {
	predictions, err := readJSONLFile[AttributionPredictionRecord](attributionPredictionsPath(recordDir))
	if err != nil {
		return err
	}
	realized, err := readJSONLFile[AttributionRealizedRecord](attributionRealizedPath(recordDir))
	if err != nil {
		return err
	}
	rows := BuildAttributionReconcileRows(predictions, realized)
	predByID := make(map[string]AttributionPredictionRecord, len(predictions))
	for _, pred := range predictions {
		if pred.OpportunityID != "" {
			predByID[pred.OpportunityID] = pred
		}
	}
	byStrategy := map[string][]AttributionRealizedRecord{}
	for _, rec := range realized {
		byStrategy[rec.Strategy] = append(byStrategy[rec.Strategy], rec)
	}
	var goNoGoLines []string
	for strategy, samples := range byStrategy {
		result := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
			Strategy:                 strategy,
			Realized:                 samples,
			PredictionsByOpportunity: predByID,
			CapitalAllocatedUSDT:     1000,
		})
		goNoGoLines = append(goNoGoLines, fmt.Sprintf("- **%s**: %s (%s)", strategy, result.Verdict, strings.Join(result.Reasons, "; ")))
	}
	sort.Strings(goNoGoLines)

	var b strings.Builder
	b.WriteString("# Attribution 对账报表\n\n")
	b.WriteString(fmt.Sprintf("生成时间: %s\n\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString("## Prediction vs Realized\n\n")
	b.WriteString("| 维度 | 值 | 样本 | 预测净EV | 实现净PnL | 差 | 预测Funding | 实现Funding | Funding差 | attribution_complete |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("| %s | %s | %d | %.4f | %.4f | %.4f | %.4f | %.4f | %.4f | %.1f%% |\n",
			row.DimensionKind, row.DimensionValue, row.SampleCount,
			row.PredictedNetEVUSDT, row.RealizedNetPnLUSDT, row.NetDiffUSDT,
			row.PredictedFundingUSDT, row.RealizedFundingUSDT, row.FundingDiffUSDT,
			row.AttributionCompleteRate*100))
	}
	b.WriteString("\n## Go/No-Go (预注册阈值,仅 profit_seeking+attribution_complete 进 alpha 判定)\n\n")
	for _, line := range goNoGoLines {
		b.WriteString(line + "\n")
	}
	return writeReportFile(attributionReportPath(recordDir), b.String())
}

func writeReportFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
