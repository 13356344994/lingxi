package app

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
	client.SetHTTPClient(server.Client())
	client.SetSpotBaseURL(server.URL)
	client.SetFuturesBaseURL(server.URL)
	client.SetHistoricalSpotPairByFuture(map[string]string{"BTCUSDT": "BTCFDUSD"})
	client.SetHistoricalSpotMeta(map[string]SpotSymbol{
		"BTCUSDT": {Symbol: "BTCFDUSD", QuoteAsset: "FDUSD"},
	})
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
				_, _ = w.Write([]byte(`[[0,"0.9940","1","0.9935","0.9940","0",0,"200000",0,0,0,0]]`))
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
	client.SetHTTPClient(server.Client())
	client.SetSpotBaseURL(server.URL)
	client.SetFuturesBaseURL(server.URL)
	client.SetHistoricalSpotPairByFuture(map[string]string{"BTCUSDT": "BTCFDUSD"})
	client.SetHistoricalSpotMeta(map[string]SpotSymbol{
		"BTCUSDT": {Symbol: "BTCFDUSD", QuoteAsset: "FDUSD"},
	})
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

// TestMonthlyResidualFillHaircutChargesExecutedTrades 钉死 H2：月度主路径下，一笔已成交候选的
// 实现净收益必须扣除残腿/部分成交期望成本（修正前该成本为 0，回测会高估收益）。
func TestMonthlyResidualFillHaircutChargesExecutedTrades(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	hcfg := runner.historicalConfig()
	if !hcfg.ResidualFillHaircutEnabled {
		t.Fatal("expected residual fill haircut to be enabled by default")
	}
	candidate := &monthlyCandidate{
		SpotEntrySlipRate:      0.0005,
		FuturesEntrySlipRate:   0.0005,
		SpotExitSlipRate:       0.0005,
		FuturesExitSlipRate:    0.0005,
		SpotEntryImpactRate:    0.0005,
		FuturesEntryImpactRate: 0.0005,
		SpotExitImpactRate:     0.0005,
		FuturesExitImpactRate:  0.0005,
		OpenBasisRate:          0.001,
		CloseBasisRate:         0.001,
	}

	breakdown := runner.monthlyResidualFillHaircut(hcfg, candidate, 1000, 1000)
	if breakdown.HaircutUSDT <= 0 {
		t.Fatalf("expected positive residual fill haircut on an executed trade, got %.6f", breakdown.HaircutUSDT)
	}

	hcfg.ResidualFillHaircutEnabled = false
	if zeroed := runner.monthlyResidualFillHaircut(hcfg, candidate, 1000, 1000); zeroed.HaircutUSDT != 0 {
		t.Fatalf("expected disabling haircut to remove the cost, got %.6f", zeroed.HaircutUSDT)
	}
}

// TestMonthlyResidualFillHaircutMateriallyLowersPnL 钉死 Gate 2 门禁：启用 haircut 后净收益必须
// 显著低于「完美对冲」假设，否则说明残腿建模未进入主路径。
func TestMonthlyResidualFillHaircutMateriallyLowersPnL(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	hcfg := runner.historicalConfig()
	candidate := &monthlyCandidate{
		SpotEntrySlipRate:      0.0005,
		FuturesEntrySlipRate:   0.0005,
		SpotExitSlipRate:       0.0005,
		FuturesExitSlipRate:    0.0005,
		SpotEntryImpactRate:    0.0005,
		FuturesEntryImpactRate: 0.0005,
		SpotExitImpactRate:     0.0005,
		FuturesExitImpactRate:  0.0005,
		OpenBasisRate:          0.001,
		CloseBasisRate:         0.001,
		EstimatedFundingIncome: 5.0,
	}
	basePnL := candidate.EstimatedFundingIncome
	withHaircut := runner.monthlyResidualFillHaircut(hcfg, candidate, 1000, 1000)
	if withHaircut.HaircutUSDT <= 0 {
		t.Fatalf("expected positive haircut, got %.6f", withHaircut.HaircutUSDT)
	}
	hcfgOff := hcfg
	hcfgOff.ResidualFillHaircutEnabled = false
	withoutHaircut := runner.monthlyResidualFillHaircut(hcfgOff, candidate, 1000, 1000)
	if withoutHaircut.HaircutUSDT != 0 {
		t.Fatalf("expected zero haircut when disabled, got %.6f", withoutHaircut.HaircutUSDT)
	}
	adjusted := basePnL - withHaircut.HaircutUSDT
	if adjusted >= basePnL*0.99 {
		t.Fatalf("haircut should materially lower net pnl: base=%.6f adjusted=%.6f haircut=%.6f", basePnL, adjusted, withHaircut.HaircutUSDT)
	}
}

func TestMonthlyPerLegNotionalUsesStrategyQuoteWhenNotBatching(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.BatchExperimentEnabled = false
	cfg.HistoricalConfig.PerLegNotionalUSDT = 10
	cfg.StrategyConfig.PerSymbolQuoteUSDT = 100
	cfg.HistoricalConfig.InitialEquityUSDT = 500
	cfg.HistoricalConfig.MaxPerLegEquityShare = 0.5
	runner := &BacktestRunner{cfg: cfg}
	if got := runner.monthlyPerLegNotional(); got != 100 {
		t.Fatalf("expected per-leg notional aligned to strategy quote 100, got %.2f", got)
	}
	runner.cfg.HistoricalConfig.BatchExperimentEnabled = true
	runner.cfg.HistoricalConfig.PerLegNotionalUSDT = 20
	if got := runner.monthlyPerLegNotional(); got != 20 {
		t.Fatalf("batch experiment should keep configured per-leg 20, got %.2f", got)
	}
}

func TestMonthlyHedgeQtyRejectsBelowMinNotional(t *testing.T) {
	rules := SymbolTradeRules{StepSize: 0.001, MinQty: 0.001, MinNotional: 1000}
	qty, reason := monthlyHedgeQty(50, 100, 100, rules, rules)
	if qty != 0 || reason != "hedge_qty_below_min_notional" {
		t.Fatalf("expected min-notional rejection, got qty=%.8f reason=%q", qty, reason)
	}
	qty, reason = monthlyHedgeQty(2000, 100, 100, rules, rules)
	if qty <= 0 || reason != "" {
		t.Fatalf("expected tradable qty, got qty=%.8f reason=%q", qty, reason)
	}
}

// TestDefaultEngineConfigEnablesResidualFillHaircut 保证回测默认就是「诚实」的，
// 不会悄悄退回完美对冲假设。
func TestDefaultEngineConfigEnablesResidualFillHaircut(t *testing.T) {
	cfg := DefaultEngineConfig()
	if !cfg.HistoricalConfig.ResidualFillHaircutEnabled {
		t.Fatal("expected default config to enable residual fill haircut")
	}
	if cfg.HistoricalConfig.ExpectedLegFillRatio <= 0 || cfg.HistoricalConfig.ExpectedLegFillRatio >= 1 {
		t.Fatalf("expected default expected leg fill ratio in (0,1), got %.4f", cfg.HistoricalConfig.ExpectedLegFillRatio)
	}
	if cfg.HistoricalConfig.LegRejectProbability <= 0 {
		t.Fatalf("expected positive default leg reject probability, got %.4f", cfg.HistoricalConfig.LegRejectProbability)
	}
	if cfg.HistoricalConfig.EmergencyHedgeSpreadMultiplier < 1 {
		t.Fatalf("expected emergency hedge multiplier >= 1, got %.2f", cfg.HistoricalConfig.EmergencyHedgeSpreadMultiplier)
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

func TestSumMonthlyFundingIncomeUsesActualFundingSchedule(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	runner.cfg.HistoricalConfig.MaxFundingCyclesHeld = 3
	cache := newMonthlyKlineCache()
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cache.storeFundingHistory("BTCUSDT", []FundingRateHistoryItem{
		{Symbol: "BTCUSDT", FundingRate: "0.0010", FundingTime: base.UnixMilli()},
		{Symbol: "BTCUSDT", FundingRate: "0.0020", FundingTime: base.Add(time.Hour).UnixMilli()},
		{Symbol: "BTCUSDT", FundingRate: "-0.0010", FundingTime: base.Add(5 * time.Hour).UnixMilli()},
	})

	got, err := runner.sumMonthlyFundingIncome(context.Background(), nil, cache, "BTCUSDT", fundingCarryDirectionPositive, 1, 100, base)
	if err != nil {
		t.Fatalf("sumMonthlyFundingIncome() error = %v", err)
	}
	if math.Abs(got-0.2) > 1e-9 {
		t.Fatalf("expected actual funding income 0.2 from 1h/4h mixed cadence, got %.6f", got)
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
		"fundingTimeline=actual_history",
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

func TestMonthlyExperimentPlansDefaultProducesSinglePlan(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	plans := runner.monthlyExperimentPlans()
	if len(plans) != 1 {
		t.Fatalf("expected single default experiment plan, got %d", len(plans))
	}
	if plans[0].Index != 1 || plans[0].Total != 1 {
		t.Fatalf("unexpected default plan indexing: %+v", plans[0])
	}
}

func TestMonthlyExperimentPlansExplicitBatchProducesSixCombos(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.BatchExperimentEnabled = true
	runner := &BacktestRunner{cfg: cfg}

	plans := runner.monthlyExperimentPlans()
	if len(plans) != 6 {
		t.Fatalf("expected 6 batch experiment plans, got %d", len(plans))
	}
	if plans[0].Index != 1 || plans[0].Total != 6 {
		t.Fatalf("unexpected first plan indexing: %+v", plans[0])
	}
	if plans[len(plans)-1].Index != 6 || plans[len(plans)-1].Total != 6 {
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
	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.BatchExperimentEnabled = true
	runner := &BacktestRunner{cfg: cfg}

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
