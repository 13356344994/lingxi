package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMonthlyFundingEscalatedPauseTreats202AsThrottleSignal(t *testing.T) {
	got := monthlyFundingEscalatedPause(map[string]int{"http_202": 3})
	if got != monthlyFundingPause202 {
		t.Fatalf("expected 202 pause=%s, got %s", monthlyFundingPause202, got)
	}

	got = monthlyFundingEscalatedPause(map[string]int{"http_429": 1})
	if got != monthlyFundingPause429 {
		t.Fatalf("expected 429 pause=%s, got %s", monthlyFundingPause429, got)
	}

	got = monthlyFundingEscalatedPause(map[string]int{"http_202": 3, "http_403": 1})
	if got != monthlyFundingPause403 {
		t.Fatalf("expected 403 to override 202 pause, got %s", got)
	}
}

func TestShouldAbortMonthlyFundingFetchAfterConsecutive202Batches(t *testing.T) {
	consecutive202 := 0
	consecutive429 := 0
	consecutive403 := 0
	consecutive418 := 0

	if shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 10}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect first 202-heavy batch to abort immediately")
	}
	if consecutive202 != 1 {
		t.Fatalf("expected first 202-heavy batch to increment counter, got %d", consecutive202)
	}
	if !shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 12}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("expected second consecutive 202-heavy batch to abort")
	}
}

func TestMonthlyFundingPause202StrongerThanNormalBatchPause(t *testing.T) {
	if monthlyFundingPause202 <= monthlyFundingFetchBatchPause {
		t.Fatalf("expected 202 pause to exceed normal batch pause, got 202=%s normal=%s", monthlyFundingPause202, monthlyFundingFetchBatchPause)
	}
	if monthlyFundingPause202 <= 0*time.Second {
		t.Fatalf("expected positive 202 pause, got %s", monthlyFundingPause202)
	}
}

func TestAdaptiveMonthlyFundingBatchPauseExpandsUnderPressure(t *testing.T) {
	got := adaptiveMonthlyFundingBatchPause(map[string]int{"http_202": 10}, 20)
	if got <= monthlyFundingPause202 {
		t.Fatalf("expected 202-heavy batch pause to exceed base 202 pause, got %s", got)
	}

	got429 := adaptiveMonthlyFundingBatchPause(map[string]int{"http_429": 10}, 20)
	if got429 <= got {
		t.Fatalf("expected 429-heavy batch pause to exceed 202-heavy batch pause, got 429=%s 202=%s", got429, got)
	}
}

func TestAdaptiveMonthlyFundingFetchConcurrencyDropsAndRecovers(t *testing.T) {
	got := adaptiveMonthlyFundingFetchConcurrency(20, 20, 20, map[string]int{"http_202": 12})
	if got >= 20 {
		t.Fatalf("expected 202-heavy batch to reduce concurrency, got %d", got)
	}

	got = adaptiveMonthlyFundingFetchConcurrency(20, 12, 20, map[string]int{"http_429": 10})
	if got != 6 {
		t.Fatalf("expected 429-heavy batch to halve concurrency from 12 to 6, got %d", got)
	}

	got = adaptiveMonthlyFundingFetchConcurrency(20, 6, 20, map[string]int{})
	if got != 7 {
		t.Fatalf("expected calm batch to recover concurrency by one, got %d", got)
	}
}

func TestShouldAbortMonthlyFundingFetchAfterConsecutive429Batches(t *testing.T) {
	consecutive202 := 0
	consecutive429 := 0
	consecutive403 := 0
	consecutive418 := 0

	if shouldAbortMonthlyFundingFetch(map[string]int{"http_429": 10}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect first 429-heavy batch to abort immediately")
	}
	if !shouldAbortMonthlyFundingFetch(map[string]int{"http_429": 11}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("expected second consecutive 429-heavy batch to abort")
	}
}

func TestTrimMonthlyFundingGroupItemsKeepsTop20Only(t *testing.T) {
	items := make([]FundingRateHistoryItem, 25)
	for i := range items {
		items[i] = FundingRateHistoryItem{Symbol: "SYM" + strconv.Itoa(i)}
	}

	trimmed := trimMonthlyFundingGroupItems(items, monthlyFundingTopSymbolsPerTime)
	if len(trimmed) != monthlyFundingTopSymbolsPerTime {
		t.Fatalf("expected trimmed size=%d, got %d", monthlyFundingTopSymbolsPerTime, len(trimmed))
	}
	if trimmed[0].Symbol != items[0].Symbol || trimmed[len(trimmed)-1].Symbol != items[monthlyFundingTopSymbolsPerTime-1].Symbol {
		t.Fatalf("expected trim to keep leading items, got first=%s last=%s", trimmed[0].Symbol, trimmed[len(trimmed)-1].Symbol)
	}

	underLimit := trimMonthlyFundingGroupItems(items[:12], monthlyFundingTopSymbolsPerTime)
	if len(underLimit) != 12 {
		t.Fatalf("expected under-limit slice to remain unchanged, got %d", len(underLimit))
	}
}

func TestMonthlyFundingTimelineUses8HourSteps(t *testing.T) {
	start := time.Date(2026, 5, 1, 1, 30, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)

	got := monthlyFundingTimeline(start, end)
	if len(got) != 4 {
		t.Fatalf("expected 4 funding checkpoints, got %d", len(got))
	}

	expected := []time.Time{
		time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
	}
	for i := range expected {
		if !got[i].Equal(expected[i]) {
			t.Fatalf("checkpoint[%d] expected %s, got %s", i, expected[i].Format(time.RFC3339), got[i].Format(time.RFC3339))
		}
	}
}

func TestValidateMonthlyActualFunding(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	reason, _ := runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0100,
		FundingRate:          0.0400,
	})
	if reason != "funding_rate_extreme" {
		t.Fatalf("expected funding_rate_extreme, got %s", reason)
	}

	reason, _ = runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0010,
		FundingRate:          0.0200,
	})
	if reason != "funding_jump_too_large" {
		t.Fatalf("expected funding_jump_too_large, got %s", reason)
	}

	reason, _ = runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0050,
		FundingRate:          0.0060,
	})
	if reason != "" {
		t.Fatalf("expected no rejection, got %s", reason)
	}
}

func TestValidateMonthlyActualFundingRejectsDirectionReversal(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	reason, _ := runner.validateMonthlyActualFunding(monthlyCandidate{
		Direction:            fundingCarryDirectionNegative,
		EstimatedFundingRate: -0.0060,
		FundingRate:          0.0010,
	})
	if reason != "funding_direction_reversed" {
		t.Fatalf("expected funding_direction_reversed, got %s", reason)
	}
}

func TestConservativeHistoricalFundingRateUsesHaircutAndAgreement(t *testing.T) {
	if got := conservativeHistoricalFundingRate(0.0010, 0.0010); got != 0.0002 {
		t.Fatalf("expected confirmed equal sources to haircut to 0.0002, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0.0010, 0); got != 0.0001 {
		t.Fatalf("expected single source haircut to 0.0001, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0.0010, 0.0002); got != 0.00002 {
		t.Fatalf("expected low agreement estimate 0.00002, got %.6f", got)
	}
}

func TestConservativeHistoricalFundingRateSupportsNegativeCarry(t *testing.T) {
	if got := conservativeHistoricalFundingRate(-0.0010, -0.0010); got != -0.0002 {
		t.Fatalf("expected equal negative sources to haircut to -0.0002, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(-0.0010, 0.0010); got != 0 {
		t.Fatalf("expected conflicting signs to collapse to 0, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0, -0.0010); got != -0.0001 {
		t.Fatalf("expected single negative source haircut to -0.0001, got %.6f", got)
	}
}

func TestHistoricalFundingDirectionHelpersSupportNegativeCarry(t *testing.T) {
	income := historicalFundingIncome(fundingCarryDirectionNegative, 2, 100, -0.01, 1)
	if income != 2 {
		t.Fatalf("expected negative carry funding income 2, got %.6f", income)
	}
	spotPnL := fundingCarryDirectionNegative.spotGrossPnL(2, 100, 90)
	if spotPnL != 20 {
		t.Fatalf("expected negative carry spot pnl 20, got %.6f", spotPnL)
	}
	futuresPnL := fundingCarryDirectionNegative.futuresGrossPnL(2, 100, 110)
	if futuresPnL != 20 {
		t.Fatalf("expected negative carry futures pnl 20, got %.6f", futuresPnL)
	}
}

func TestHistoricalBorrowInterestHoursAndCostForNegativeCarry(t *testing.T) {
	entryTime := time.Date(2026, 5, 1, 23, 55, 0, 0, time.UTC)
	exitTime := time.Date(2026, 5, 2, 0, 5, 0, 0, time.UTC)

	hours := historicalBorrowInterestHours(fundingCarryDirectionNegative, entryTime, exitTime, 1)
	if hours != 1 {
		t.Fatalf("expected billed hours 1, got %d", hours)
	}
	hours = historicalBorrowInterestHours(fundingCarryDirectionNegative, entryTime, exitTime.Add(8*time.Hour), 1)
	if hours != 9 {
		t.Fatalf("expected billed hours 9, got %d", hours)
	}
	cost := historicalBorrowInterestCost(fundingCarryDirectionNegative, 100, 0.001, 3)
	if math.Abs(cost-0.3) > 1e-9 {
		t.Fatalf("expected borrow interest cost 0.3, got %.6f", cost)
	}
	if got := historicalBorrowInterestCost(fundingCarryDirectionPositive, 100, 0.001, 3); got != 0 {
		t.Fatalf("expected positive carry borrow interest cost 0, got %.6f", got)
	}
}

func TestHistoricalSpotImpactMultiplierUsesNegativeCarryOverrides(t *testing.T) {
	hcfg := historicalConfig{
		NegativeCarrySpotEntryImpactMultiplier: 2.5,
		NegativeCarrySpotExitImpactMultiplier:  3.0,
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionPositive, true); got != 1 {
		t.Fatalf("expected positive carry entry multiplier 1, got %.2f", got)
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionNegative, true); got != 2.5 {
		t.Fatalf("expected negative carry entry multiplier 2.5, got %.2f", got)
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionNegative, false); got != 3.0 {
		t.Fatalf("expected negative carry exit multiplier 3.0, got %.2f", got)
	}
}

func TestCompleteMonthlyCandidateProxyConvertsCrossStableSpotIntoUSDT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/klines":
			switch r.URL.Query().Get("symbol") {
			case "BTCFDUSD":
				_, _ = w.Write([]byte(`[[0,"100","101","99","100","0",0,"120000",0,0,0,0]]`))
			case "FDUSDUSDT":
				_, _ = w.Write([]byte(`[[0,"0.9995","1","0.9990","0.9995","0",0,"200000",0,0,0,0]]`))
			default:
				t.Fatalf("unexpected spot symbol: %s", r.URL.Query().Get("symbol"))
			}
		case "/fapi/v1/klines":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("unexpected futures symbol: %s", r.URL.Query().Get("symbol"))
			}
			_, _ = w.Write([]byte(`[[0,"100.5","101","100","100.5","0",0,"150000",0,0,0,0]]`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewBinanceClient(time.Second)
	client.httpClient = server.Client()
	client.spotBaseURL = server.URL
	client.futuresBaseURL = server.URL
	client.historicalSpotPairByFuture = map[string]string{"BTCUSDT": "BTCFDUSD"}
	client.historicalSpotMeta = map[string]SpotSymbol{
		"BTCUSDT": {Symbol: "BTCFDUSD", QuoteAsset: "FDUSD"},
	}
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	candidate := &monthlyCandidate{
		Symbol:               "BTCUSDT",
		FundingTime:          time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC),
		ExitTime:             time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC),
		EstimatedFundingRate: 0.01,
		FundingRate:          0.01,
		HoldingCycles:        1,
	}

	if err := runner.completeMonthlyCandidateProxy(context.Background(), client, newMonthlyKlineCache(), candidate); err != nil {
		t.Fatalf("completeMonthlyCandidateProxy() error = %v", err)
	}
	if candidate.Reason == "cross_quote_deviation_too_high" || candidate.Reason == "cross_quote_fx_unavailable" {
		t.Fatalf("expected candidate to pass cross quote checks, got reason=%s notes=%v", candidate.Reason, candidate.Notes)
	}
	if math.Abs(candidate.SpotEntryPrice-99.95) > 1e-6 {
		t.Fatalf("expected spot entry price converted to USDT, got %.8f", candidate.SpotEntryPrice)
	}
	if math.Abs(candidate.SpotEntryQuoteVolume-119940) > 1e-3 {
		t.Fatalf("expected spot quote volume converted to USDT, got %.8f", candidate.SpotEntryQuoteVolume)
	}
	if !strings.Contains(strings.Join(candidate.Notes, " | "), "spotQuoteAsset=FDUSD") {
		t.Fatalf("expected candidate notes to include cross quote metadata, got %v", candidate.Notes)
	}
}

func TestCompleteMonthlyCandidateProxyRejectsCrossStableDepeg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/klines":
			switch r.URL.Query().Get("symbol") {
			case "BTCFDUSD":
				_, _ = w.Write([]byte(`[[0,"100","101","99","100","0",0,"120000",0,0,0,0]]`))
			case "FDUSDUSDT":
				_, _ = w.Write([]byte(`[[0,"0.9970","1","0.9965","0.9970","0",0,"200000",0,0,0,0]]`))
			default:
				t.Fatalf("unexpected spot symbol: %s", r.URL.Query().Get("symbol"))
			}
		case "/fapi/v1/klines":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("unexpected futures symbol: %s", r.URL.Query().Get("symbol"))
			}
			_, _ = w.Write([]byte(`[[0,"100.5","101","100","100.5","0",0,"150000",0,0,0,0]]`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewBinanceClient(time.Second)
	client.httpClient = server.Client()
	client.spotBaseURL = server.URL
	client.futuresBaseURL = server.URL
	client.historicalSpotPairByFuture = map[string]string{"BTCUSDT": "BTCFDUSD"}
	client.historicalSpotMeta = map[string]SpotSymbol{
		"BTCUSDT": {Symbol: "BTCFDUSD", QuoteAsset: "FDUSD"},
	}
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	candidate := &monthlyCandidate{
		Symbol:               "BTCUSDT",
		FundingTime:          time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC),
		ExitTime:             time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC),
		EstimatedFundingRate: 0.01,
		FundingRate:          0.01,
		HoldingCycles:        1,
	}

	if err := runner.completeMonthlyCandidateProxy(context.Background(), client, newMonthlyKlineCache(), candidate); err != nil {
		t.Fatalf("completeMonthlyCandidateProxy() error = %v", err)
	}
	if candidate.Reason != "cross_quote_deviation_too_high" {
		t.Fatalf("expected depeg rejection, got reason=%s notes=%v", candidate.Reason, candidate.Notes)
	}
	if !strings.Contains(strings.Join(candidate.Notes, " | "), "crossStableQuoteMaxDeviationRate=") {
		t.Fatalf("expected candidate notes to include deviation gate, got %v", candidate.Notes)
	}
}

func TestHistoricalFundingDirectionModeValidation(t *testing.T) {
	if !isValidHistoricalFundingDirectionMode("both") {
		t.Fatal("expected both to be valid")
	}
	if isValidHistoricalFundingDirectionMode("garbage") {
		t.Fatal("expected garbage mode to be invalid")
	}
	if normalizeHistoricalFundingDirectionMode("") != historicalFundingModeBoth {
		t.Fatalf("expected blank mode to normalize to %s", historicalFundingModeBoth)
	}
}

func TestMonthlyExitTimeUsesConfiguredHoldingCycles(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	runner.cfg.HistoricalConfig.MaxFundingCyclesHeld = 2
	fundingTime := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

	got := runner.monthlyExitTime(fundingTime)
	want := fundingTime.Add(8 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("expected exit time %s, got %s", want.Format(time.RFC3339), got.Format(time.RFC3339))
	}
}

func TestHistoricalFundingPhaseLeadDefaultsToMonitorStopLead(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.FundingPhaseLead = 0
	cfg.StrategyConfig.MonitorStopLead = 45 * time.Second

	got := (&BacktestRunner{cfg: cfg}).historicalConfig().FundingPhaseLead
	if got != 45*time.Second {
		t.Fatalf("expected funding phase lead to follow monitor stop lead, got %s", got)
	}
}

func TestMonthlyScenarioNameEmbedsExperimentParameters(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	runner.cfg.HistoricalConfig.SpotTakerFeeRate = 0
	runner.cfg.HistoricalConfig.FuturesTakerFeeRate = 0.0002
	runner.cfg.HistoricalConfig.PerLegNotionalUSDT = 5
	runner.cfg.HistoricalConfig.MaxFundingCyclesHeld = 2
	runner.cfg.HistoricalConfig.FundingDirectionMode = historicalFundingModeNegative

	got := runner.monthlyScenarioName("MonthlyMarketReplay")
	expectedParts := []string{
		"MonthlyMarketReplay",
		"mode=negative_only",
		"spot=0.0000",
		"futures=0.0002",
		"per_leg=5.00",
		"hold=2",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("expected scenario %q to contain %q", got, part)
		}
	}
}

func TestMonthlyExperimentNotesIncludeNegativeCarryCostAssumptions(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	runner.cfg.HistoricalConfig.AssumedHourlyBorrowInterestRate = 0.0015
	runner.cfg.HistoricalConfig.BorrowInterestMinBillingHours = 2
	runner.cfg.HistoricalConfig.NegativeCarrySpotEntryImpactMultiplier = 2.5
	runner.cfg.HistoricalConfig.NegativeCarrySpotExitImpactMultiplier = 3.0

	notes := strings.Join(runner.monthlyExperimentNotes(), " | ")
	for _, part := range []string{
		"assumedHourlyBorrowInterestRate=0.0015",
		"borrowInterestMinBillingHours=2",
		"negativeCarrySpotEntryImpactMultiplier=2.50",
		"negativeCarrySpotExitImpactMultiplier=3.00",
	} {
		if !strings.Contains(notes, part) {
			t.Fatalf("expected notes %q to contain %q", notes, part)
		}
	}
}

func TestMonthlyExperimentPlansDefaultBatchProducesTwelveCombos(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	plans := runner.monthlyExperimentPlans()
	if len(plans) != 12 {
		t.Fatalf("expected 12 batch experiment plans, got %d", len(plans))
	}
	if plans[0].Index != 1 || plans[0].Total != 12 {
		t.Fatalf("unexpected first plan indexing: %+v", plans[0])
	}
	if plans[len(plans)-1].Index != 12 || plans[len(plans)-1].Total != 12 {
		t.Fatalf("unexpected last plan indexing: %+v", plans[len(plans)-1])
	}

	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		key := runnerScenarioKey(plan.Cfg.HistoricalConfig.SpotTakerFeeRate, plan.Cfg.HistoricalConfig.PerLegNotionalUSDT, plan.Cfg.HistoricalConfig.MaxFundingCyclesHeld)
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate experiment plan: %s", key)
		}
		seen[key] = struct{}{}
	}
}

func TestMonthlyExperimentPlanForTwentyUSDTPreservesEffectiveNotional(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	var plan monthlyExperimentPlan
	found := false
	for _, candidate := range runner.monthlyExperimentPlans() {
		if candidate.Cfg.HistoricalConfig.PerLegNotionalUSDT == 20 && candidate.Cfg.HistoricalConfig.MaxFundingCyclesHeld == 1 {
			plan = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected to find 20 USDT experiment plan")
	}
	effective := (&BacktestRunner{cfg: plan.Cfg}).monthlyPerLegNotional()
	if effective != 20 {
		t.Fatalf("expected effective per-leg notional 20, got %.2f", effective)
	}
}

func TestMonthlyExperimentPlansCanBeDisabled(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.BatchExperimentEnabled = false
	runner := &BacktestRunner{cfg: cfg}

	plans := runner.monthlyExperimentPlans()
	if len(plans) != 1 {
		t.Fatalf("expected single experiment plan when disabled, got %d", len(plans))
	}
}

func runnerScenarioKey(spotFee float64, perLeg float64, hold int) string {
	return strconv.FormatFloat(spotFee, 'f', 4, 64) + "|" + strconv.FormatFloat(perLeg, 'f', 2, 64) + "|" + strconv.Itoa(hold)
}
