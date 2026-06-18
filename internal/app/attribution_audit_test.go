package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
)

func TestEvaluateAttributionGoNoGoInsufficientSample(t *testing.T) {
	start, end := postBoundaryGoNoGoWindow(5)
	samples := makeProfitSeekingRealizedSamples(10, start, end)
	res := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
		Strategy:             UnifiedStrategyCrossVenueSpotPerp,
		Realized:             samples,
		CapitalAllocatedUSDT: 1000,
	})
	if res.Verdict != GoNoGoVerdictInsufficientSample {
		t.Fatalf("verdict = %q, want INSUFFICIENT_SAMPLE", res.Verdict)
	}
	if !strings.Contains(res.Reasons[0], "样本数") {
		t.Fatalf("reasons = %v", res.Reasons)
	}
}

func TestEvaluateAttributionGoNoGoInsufficientPeriod(t *testing.T) {
	end := attributionFixBoundaryTime.Add(10 * 24 * time.Hour)
	start := end.Add(-2 * 24 * time.Hour)
	samples := makeProfitSeekingRealizedSamples(GoNoGoMinSampleCount, start, end)
	res := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
		Strategy:             UnifiedStrategyCrossVenueSpotPerp,
		Realized:             samples,
		CapitalAllocatedUSDT: 1000,
	})
	if res.Verdict != GoNoGoVerdictInsufficientSample {
		t.Fatalf("verdict = %q, want INSUFFICIENT_SAMPLE", res.Verdict)
	}
	if !strings.Contains(strings.Join(res.Reasons, " "), "周期") {
		t.Fatalf("reasons = %v", res.Reasons)
	}
}

func TestEvaluateAttributionGoNoGoNOGO(t *testing.T) {
	start, end := postBoundaryGoNoGoWindow(5)
	samples := makeProfitSeekingRealizedSamples(GoNoGoMinSampleCount, start, end)
	for i := range samples {
		samples[i].FinalNetPnLUSDT = -5
		samples[i].RealizedFundingUSDT = 1
		samples[i].RealizedTradePnLUSDT = -6
	}
	res := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
		Strategy:             UnifiedStrategyCrossVenueSpotPerp,
		Realized:             samples,
		CapitalAllocatedUSDT: 1000,
	})
	if res.Verdict != GoNoGoVerdictNOGO {
		t.Fatalf("verdict = %q, want NO-GO", res.Verdict)
	}
}

func TestEvaluateAttributionGoNoGoGO(t *testing.T) {
	start, end := postBoundaryGoNoGoWindow(5)
	samples := makeProfitSeekingRealizedSamples(GoNoGoMinSampleCount, start, end)
	predictions := map[string]AttributionPredictionRecord{}
	for i := range samples {
		samples[i].FinalNetPnLUSDT = 2
		samples[i].RealizedFundingUSDT = 2.5
		samples[i].RealizedTradePnLUSDT = 0.2
		samples[i].RealizedFeesUSDT = 0.5
		samples[i].NotionalUSDT = 100
		predictions[samples[i].OpportunityID] = AttributionPredictionRecord{
			OpportunityID:     samples[i].OpportunityID,
			ExpectedNetEVUSDT: 2.1,
		}
	}
	res := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
		Strategy:                 UnifiedStrategyCrossVenueSpotPerp,
		Realized:                 samples,
		PredictionsByOpportunity: predictions,
		CapitalAllocatedUSDT:     10000,
	})
	if res.Verdict != GoNoGoVerdictGO {
		t.Fatalf("verdict = %q, want GO, reasons=%v metrics=%+v", res.Verdict, res.Reasons, res)
	}
}

func TestEvaluateAttributionGoNoGoThresholdBoundary(t *testing.T) {
	start, end := postBoundaryGoNoGoWindow(5)
	samples := makeProfitSeekingRealizedSamples(GoNoGoMinSampleCount, start, end)
	res := EvaluateAttributionGoNoGo(AttributionGoNoGoStrategyInput{
		Strategy:             UnifiedStrategyCrossVenueSpotPerp,
		Realized:             samples,
		CapitalAllocatedUSDT: 1000,
	})
	if res.PeriodWeeks+1e-9 < float64(GoNoGoMinPeriodWeeks) {
		t.Fatalf("period weeks = %.2f, want >= %d", res.PeriodWeeks, GoNoGoMinPeriodWeeks)
	}
	if res.AlphaSampleCount != GoNoGoMinSampleCount {
		t.Fatalf("alpha sample count = %d, want %d", res.AlphaSampleCount, GoNoGoMinSampleCount)
	}
}

func TestInitAttributionRunModeFromStrategyMode(t *testing.T) {
	initAttributionRunMode(core.StrategyModeCollectData)
	if CurrentAttributionRunMode() != AttributionRunModeCollectData {
		t.Fatalf("collect_data => %q", CurrentAttributionRunMode())
	}
	initAttributionRunMode(core.StrategyModeCanaryProfit)
	if CurrentAttributionRunMode() != AttributionRunModeCanaryProfit {
		t.Fatalf("canary_profit => %q", CurrentAttributionRunMode())
	}
	initAttributionRunMode(core.StrategyModeProfitSeeking)
	if CurrentAttributionRunMode() != AttributionRunModeProfitSeeking {
		t.Fatalf("profit_seeking => %q", CurrentAttributionRunMode())
	}
}

func TestFilterAlphaRealizedSamplesExcludesRelaxedModes(t *testing.T) {
	end := attributionFixBoundaryTime.Add(48 * time.Hour)
	start := end.Add(-24 * time.Hour)
	profit := makeProfitSeekingRealizedSamples(5, start, end)
	collect := makeProfitSeekingRealizedSamples(5, start, end)
	for i := range collect {
		collect[i].RunMode = string(AttributionRunModeCollectData)
	}
	alpha := filterAlphaRealizedSamples(append(profit, collect...))
	if len(alpha) != 5 {
		t.Fatalf("alpha samples = %d, want 5 (collect_data 不应进 alpha 判定)", len(alpha))
	}
	for _, rec := range alpha {
		if rec.RunMode != string(AttributionRunModeProfitSeeking) {
			t.Fatalf("unexpected run_mode in alpha set: %q", rec.RunMode)
		}
	}
}

func TestBuildAttributionReconcileRowsDiff(t *testing.T) {
	open := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	closeT := open.Add(24 * time.Hour)
	predictions := []AttributionPredictionRecord{{
		OpportunityID:       "opp-1",
		Strategy:            UnifiedStrategySingleVenueSpotPerp,
		Symbol:              "BTC-USDT",
		Venues:              []string{"binance"},
		ExpectedFundingUSDT: 10,
		ExpectedNetEVUSDT:   8,
	}}
	realized := []AttributionRealizedRecord{{
		OpportunityID:       "opp-1",
		Strategy:            UnifiedStrategySingleVenueSpotPerp,
		Symbol:              "BTC-USDT",
		Venues:              []string{"binance"},
		OpenTime:            open,
		CloseTime:           closeT,
		RealizedFundingUSDT: 9,
		FinalNetPnLUSDT:     7,
		AttributionComplete: true,
		RunMode:             string(AttributionRunModeProfitSeeking),
	}}
	rows := BuildAttributionReconcileRows(predictions, realized)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 dimensions", len(rows))
	}
	var strategyRow *AttributionReconcileRow
	for i := range rows {
		if rows[i].DimensionKind == "strategy" {
			strategyRow = &rows[i]
			break
		}
	}
	if strategyRow == nil {
		t.Fatal("missing strategy row")
	}
	if strategyRow.NetDiffUSDT != 1 {
		t.Fatalf("net diff = %.4f, want 1", strategyRow.NetDiffUSDT)
	}
	if strategyRow.FundingDiffUSDT != 1 {
		t.Fatalf("funding diff = %.4f, want 1", strategyRow.FundingDiffUSDT)
	}
	if strategyRow.AttributionCompleteRate != 1 {
		t.Fatalf("complete rate = %.2f, want 1", strategyRow.AttributionCompleteRate)
	}
}

func TestAppendAttributionPredictionAndRealizedJSONL(t *testing.T) {
	dir := t.TempDir()
	opp := UnifiedOpportunity{
		ID:                  "opp-jsonl-1",
		StrategyType:        UnifiedStrategyCrossVenueSpotPerp,
		Symbol:              "ETH-USDT",
		Legs:                []UnifiedOpportunityLeg{{Venue: "binance"}, {Venue: "bybit"}},
		ExpectedFundingUSDT: 3,
		ExpectedNetEVUSDT:   2,
	}
	appendAttributionPredictionBestEffort(dir, predictionRecordFromOpportunity(opp, time.Now().UTC()))
	appendAttributionRealizedBestEffort(dir, AttributionRealizedRecord{
		OpportunityID:       opp.ID,
		Strategy:            opp.StrategyType,
		Symbol:              opp.Symbol,
		Venues:              []string{"binance", "bybit"},
		CloseTime:           time.Now().UTC(),
		RealizedFundingUSDT: 2.5,
		FinalNetPnLUSDT:     2,
		AttributionComplete: true,
		RunMode:             string(CurrentAttributionRunMode()),
	})
	preds, err := readJSONLFile[AttributionPredictionRecord](attributionPredictionsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(preds) != 1 || preds[0].OpportunityID != opp.ID {
		t.Fatalf("predictions = %+v", preds)
	}
	reals, err := readJSONLFile[AttributionRealizedRecord](attributionRealizedPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(reals) != 1 || reals[0].FinalNetPnLUSDT != 2 {
		t.Fatalf("realized = %+v", reals)
	}
	if err := WriteAttributionReport(dir); err != nil {
		t.Fatal(err)
	}
	reportPath := attributionReportPath(dir)
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Prediction vs Realized") {
		t.Fatalf("report missing header: %s", data)
	}
}

func TestOpenOpportunityAttributionWriteNoTradeSideEffect(t *testing.T) {
	dir := t.TempDir()
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.recordDir = dir
	opp := testRotationOpp("side-effect-opp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 5, 100)
	if err := r.openOpportunity(context.Background(), UnifiedStrategySingleVenueSpotPerp, opp, false); err != nil {
		t.Fatalf("openOpportunity: %v", err)
	}
	if len(svsp.opens) != 1 {
		t.Fatalf("opens = %d, want 1", len(svsp.opens))
	}
	preds, err := readJSONLFile[AttributionPredictionRecord](attributionPredictionsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(preds) != 1 {
		t.Fatalf("predictions = %d, want 1", len(preds))
	}

	failExec := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	failBinding := failExec.binding()
	failBinding.openFn = func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
		return assertAnError("forced open failure")
	}
	r.bindings[UnifiedStrategySingleVenueSpotPerp] = failBinding
	if err := r.openOpportunity(context.Background(), UnifiedStrategySingleVenueSpotPerp, opp, false); err == nil {
		t.Fatal("expected open failure")
	}
	predsAfter, err := readJSONLFile[AttributionPredictionRecord](attributionPredictionsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(predsAfter) != 1 {
		t.Fatalf("predictions after failed open = %d, want still 1", len(predsAfter))
	}
}

func TestRealizedRecordFromCrossVenueSettlementPreservesAttributionFlags(t *testing.T) {
	closeT := time.Now().UTC()
	rec := realizedRecordFromCrossVenueSettlement(CrossVenueSettlement{
		Symbol:                "BTC-USDT",
		HighVenue:             "okx",
		LowVenue:              "bybit",
		EntrySnapshotID:       "snap-1",
		OpenTime:              closeT.Add(-2 * time.Hour),
		CloseTime:             closeT,
		NotionalUSDT:          100,
		RealizedFunding:       1.2,
		TradePnL:              0.3,
		TotalFees:             0.4,
		OpenSlippage:          0.05,
		CloseSlippage:         0.05,
		NetPnL:                1.1,
		AttributionIncomplete: true,
	})
	if rec.AttributionComplete {
		t.Fatal("expected attribution_complete=false when settlement incomplete")
	}
	if rec.RealizedSlippageUSDT != 0.1 {
		t.Fatalf("slippage = %.4f, want 0.1", rec.RealizedSlippageUSDT)
	}
	wantID := unifiedOpportunityID(UnifiedStrategyPerpPerp, "BTC-USDT", "snap-1", "okx", "bybit")
	if rec.OpportunityID != wantID {
		t.Fatalf("opportunity_id = %q, want %q", rec.OpportunityID, wantID)
	}
}

func makeProfitSeekingRealizedSamples(n int, start, end time.Time) []AttributionRealizedRecord {
	if n <= 0 {
		return nil
	}
	minClose := attributionFixBoundaryTime.Add(time.Second)
	if start.Before(minClose) {
		start = minClose
	}
	if !end.After(start) {
		end = start.Add(35 * 24 * time.Hour)
	}
	out := make([]AttributionRealizedRecord, 0, n)
	step := end.Sub(start) / time.Duration(n-1)
	if n == 1 {
		step = 0
	}
	for i := 0; i < n; i++ {
		closeT := start.Add(step * time.Duration(i))
		openT := closeT.Add(-6 * time.Hour)
		out = append(out, AttributionRealizedRecord{
			OpportunityID:       fmt.Sprintf("opp-%d", i),
			Strategy:            UnifiedStrategyCrossVenueSpotPerp,
			Symbol:              "TEST-USDT",
			Venues:              []string{"binance", "bybit"},
			OpenTime:            openT,
			CloseTime:           closeT,
			NotionalUSDT:        100,
			RealizedFundingUSDT: 1,
			FinalNetPnLUSDT:     0.5,
			AttributionComplete: true,
			RunMode:             string(AttributionRunModeProfitSeeking),
		})
	}
	return out
}

func postBoundaryGoNoGoWindow(weeks int) (start, end time.Time) {
	end = attributionFixBoundaryTime.Add(time.Duration(weeks+1) * 7 * 24 * time.Hour)
	start = end.Add(-time.Duration(weeks) * 7 * 24 * time.Hour)
	if start.Before(attributionFixBoundaryTime) {
		start = attributionFixBoundaryTime.Add(time.Second)
	}
	return start, end
}

func assertAnError(msg string) error {
	return &attributionTestError{msg: msg}
}

type attributionTestError struct{ msg string }

func (e *attributionTestError) Error() string { return e.msg }
