package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func captureCrossVenueCarryFunnelLog(t *testing.T, fn func()) string {
	t.Helper()
	buf := &strings.Builder{}
	orig := crossVenueCarryFunnelLogFn
	crossVenueCarryFunnelLogFn = func(stats crossVenueCarryFunnelStats) {
		fmt.Fprintf(buf,
			"[carry-funnel] %s symbols=%d no_spot_venues=%d no_perp_venues=%d no_best_perp=%d pair_skip_same_venue=%d pair_attempts=%d "+
				"build_market=%d build_instrument=%d build_mid_dev=%d build_notional=%d build_margin_stress=%d build_hold_hours=%d build_settlements=%d "+
				"build_funding_window=%d build_funding_ev=%d build_conf_sizing=%d build_not_borrowable=%d emitted=%d emitted_rejected=%d emitted_tradable=%d\n",
			stats.Tag,
			stats.SymbolsSeen, stats.NoSpotVenues, stats.NoPerpVenues, stats.NoBestPerp, stats.PairSkipSameVenue, stats.PairAttempts,
			stats.BuildMarketInvalid, stats.BuildInstrument, stats.BuildMidDev, stats.BuildNotional, stats.BuildMarginStress,
			stats.BuildHoldHours, stats.BuildSettlements,
			stats.BuildFundingWindow, stats.BuildFundingEV, stats.BuildConfSizing, stats.BuildNotBorrowable,
			stats.Emitted, stats.EmittedRejected, stats.EmittedTradable,
		)
	}
	t.Cleanup(func() { crossVenueCarryFunnelLogFn = orig })
	fn()
	return buf.String()
}

func testCrossVenueSpotPerpScanInput(now time.Time) CrossVenueSpotPerpScanInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	cfg.PeakFundingHaircut = 1.0
	nextFunding := scannerTestCarryNextFunding(now)

	return CrossVenueSpotPerpScanInput{
		SnapshotID: "cvsp-snap-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": scannerTestEstablishedFundingSnap(scannerTestCarryNearFundingRate, nextFunding),
			},
			"okx": {
				"BTC-USDT": scannerTestEstablishedFundingSnap(0.0001, nextFunding),
			},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
			binance.VenueName: {
				"BTC-USDT": {
					SpotMid:       100000,
					PerpMid:       100050,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
			bybit.VenueName: {
				"BTC-USDT": {
					SpotMid:       100010,
					PerpMid:       100080,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
			"okx": {
				"BTC-USDT": {
					SpotMid:       100005,
					PerpMid:       100070,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
		}),
		AvailableUSDTByVenue: map[string]float64{
			binance.VenueName: 200,
			bybit.VenueName:   200,
			"okx":             200,
		},
		Config:                       cfg,
		FundingRateVolPerHourByVenue: scannerTestCarryLowFundingVol(bybit.VenueName, "BTC-USDT"),
	}
}

func TestScanCrossVenueSpotPerpOpportunitiesHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	opps := ScanCrossVenueSpotPerpOpportunities(in)

	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 (binance+bybit, okx+bybit)", len(opps))
	}
	for _, opp := range opps {
		if opp.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
			t.Fatalf("strategy_type = %q", opp.StrategyType)
		}
		if len(opp.Legs) != 2 {
			t.Fatalf("legs = %d", len(opp.Legs))
		}
		if opp.Legs[0].Market != "spot" || opp.Legs[0].Side != "buy" {
			t.Fatalf("spot leg = %+v", opp.Legs[0])
		}
		if opp.Legs[1].Market != "perp" || opp.Legs[1].Side != "short" || opp.Legs[1].Venue != bybit.VenueName {
			t.Fatalf("perp leg = %+v", opp.Legs[1])
		}
		if opp.ExpectedFundingUSDT <= 0 {
			t.Fatalf("expected_funding_usdt = %.6f, want >0", opp.ExpectedFundingUSDT)
		}
		if opp.CapitalRequiredByVenue[opp.Legs[0].Venue] != opp.Legs[0].Notional {
			t.Fatalf("spot capital = %.2f, want %.2f", opp.CapitalRequiredByVenue[opp.Legs[0].Venue], opp.Legs[0].Notional)
		}
		perpMargin := crossVenueSpotPerpPerpMarginRequired(in.Config, opp.Legs[1].Notional)
		if opp.CapitalRequiredByVenue[bybit.VenueName] != perpMargin {
			t.Fatalf("perp margin = %.4f, want %.4f", opp.CapitalRequiredByVenue[bybit.VenueName], perpMargin)
		}
		if opp.RequiresRebalance {
			t.Fatalf("unexpected requires_rebalance for funded venues")
		}
	}
}

func TestScanCrossVenueSpotPerpRequiresRebalanceWhenVenueShort(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[binance.VenueName] = 500
	in.AvailableUSDTByVenue[bybit.VenueName] = 3

	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if !opportunityHasTradableLegs(opp) {
			continue
		}
		if opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("expected no binance spot opportunity when perp venue cannot support 5U floor, got %+v", opp)
		}
	}
}

func TestCrossVenueSpotPerpRequiresRebalanceHelper(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	available := map[string]float64{binance.VenueName: 10, bybit.VenueName: 100}
	if !crossVenueSpotPerpRequiresRebalance(binance.VenueName, bybit.VenueName, 50, 10, available, cfg) {
		t.Fatal("expected requires_rebalance when spot cash exceeds available")
	}
	if crossVenueSpotPerpRequiresRebalance(binance.VenueName, bybit.VenueName, 5, 4, available, cfg) {
		t.Fatal("unexpected requires_rebalance when capital covers legs")
	}
}

func TestCrossVenueSpotPerpEffectiveFundingRateNoHistoryLiveRate(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0, 0.5, 1.0)
	want := 0.001
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("no-history effective rate = %.8f, want live %.8f", got, want)
	}
	gotHaircut := crossVenueSpotPerpEffectiveFundingRate(0.001, 0, 0.5, 0.5)
	if math.Abs(gotHaircut-0.0005) > 1e-12 {
		t.Fatalf("configured no-history haircut = %.8f, want 0.0005", gotHaircut)
	}
}

func TestScanCrossVenueSpotPerpExcludesNegativeFunding(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = CrossVenueSpotPerpFundingSnap{
		Rate:            -0.0002,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.0001,
	}
	in.FundingByVenue["okx"]["BTC-USDT"] = CrossVenueSpotPerpFundingSnap{
		Rate:            -0.0001,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.00008,
	}
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) != 0 {
		t.Fatalf("opportunities = %d, want 0 for negative funding", len(opps))
	}
}

func TestScanCrossVenueSpotPerpExcludesMidDeviationTooLarge(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       105000,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("unexpected opportunity with large mid deviation: %+v", opp)
		}
	}
}

func TestScanCrossVenueSpotPerpExcludesInsufficientDepth(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if opportunityHasTradableLegs(opp) && len(opp.Legs) >= 2 && opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("unexpected tradable opportunity with insufficient depth: notional=%.2f", opp.Legs[0].Notional)
		}
	}
	binanceOpp := findCrossVenueSpotPerpOpp(opps, "BTC-USDT", binance.VenueName)
	if binanceOpp == nil {
		t.Fatal("expected rejected row for insufficient depth")
	}
	if !containsStringReject(binanceOpp.RejectReasons, singleVenueSpotPerpRejectInsufficientDepth) {
		t.Fatalf("reject_reasons = %#v, want %q", binanceOpp.RejectReasons, singleVenueSpotPerpRejectInsufficientDepth)
	}
}

func TestScanCrossVenueSpotPerpExcludesMarginStressLiquidation(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.Config.Exec.MaxEffectiveLeverage = 20
	in.Config.PerpWalletBufferRatio = 0.01
	in.Config.PerpMaintenanceMarginRate = 0.05
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 rejected rows", len(opps))
	}
	for _, opp := range opps {
		if !containsStringReject(opp.RejectReasons, singleVenueSpotPerpRejectMarginStress) {
			t.Fatalf("reject_reasons = %#v, want %q", opp.RejectReasons, singleVenueSpotPerpRejectMarginStress)
		}
	}
}

func TestCrossVenueSpotPerpEffectiveFundingRateSpikeDecay(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0.0004, 0.5, 1.0)
	want := 0.0004 + 0.5*(0.001-0.0004)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("effective rate = %.8f, want %.8f", got, want)
	}
}

func TestCrossVenueSpotPerpFundingSettlementCountAlignsGridBeforeNextFunding(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	next := now.Add(2 * time.Hour)
	// interval 1h、next 在 2h 后:应对齐到 now+1h 并计入 now+1h 与 now+2h 两次结算。
	count := crossVenueSpotPerpFundingSettlementCount(now, 2, 1, next)
	if count != 2 {
		t.Fatalf("settlements = %d, want 2 when hourly grid has two points within 2h hold", count)
	}
}

func TestCrossVenueSpotPerpFundingSettlementCount(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	next := now.Add(2 * time.Hour)
	count := crossVenueSpotPerpFundingSettlementCount(now, 6, 8, next)
	if count != 1 {
		t.Fatalf("settlements = %d, want 1", count)
	}
	count = crossVenueSpotPerpFundingSettlementCount(now, 14, 8, next)
	if count != 2 {
		t.Fatalf("settlements = %d, want 2", count)
	}
}

func TestCrossVenueSpotPerpPerpWalletLiquidated(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	if crossVenueSpotPerpPerpWalletLiquidated(cfg, 50, 0.20) {
		t.Fatal("expected no liquidation at +20% with default conservative params")
	}
	cfg.PerpWalletBufferRatio = 0
	cfg.Exec.MaxEffectiveLeverage = 20
	if !crossVenueSpotPerpPerpWalletLiquidated(cfg, 50, 0.20) {
		t.Fatal("expected liquidation at +20% with tiny buffer/high leverage")
	}
}

func TestAppendCrossVenueSpotPerpCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 25
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected at least one opportunity")
	}

	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	AppendCrossVenueSpotPerpCandidates(dir, "cvsp-cycle-test", execCfg, opps)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(opps) {
		t.Fatalf("lines = %d, want %d", len(lines), len(opps))
	}
	var rec UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
		t.Fatalf("strategy_type = %q", rec.StrategyType)
	}
	if rec.CycleID != "cvsp-cycle-test" {
		t.Fatalf("cycle_id = %q", rec.CycleID)
	}
	wantExec := len(opps[0].RejectReasons) == 0
	if rec.WouldExecute != wantExec {
		t.Fatalf("would_execute = %v, want %v (reject_reasons=%v)", rec.WouldExecute, wantExec, opps[0].RejectReasons)
	}
}

func TestScanCrossVenueSpotPerpExampleOutputSample(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[binance.VenueName] = 500
	in.AvailableUSDTByVenue[bybit.VenueName] = 20
	opps := ScanCrossVenueSpotPerpOpportunities(in)

	var sample UnifiedOpportunity
	for _, opp := range opps {
		if opp.Legs[0].Venue == binance.VenueName && !opp.RequiresRebalance {
			sample = opp
			break
		}
	}
	if sample.ID == "" {
		t.Fatal("missing capital-sized sample opportunity")
	}

	t.Logf("sample opportunity id=%s symbol=%s requires_rebalance=%v net_ev=%.4f funding=%.4f basis_rollback=%.4f capital=%+v legs=%+v reject=%v",
		sample.ID, sample.Symbol, sample.RequiresRebalance, sample.ExpectedNetEVUSDT,
		sample.ExpectedFundingUSDT, sample.ExpectedRollbackCostUSDT,
		sample.CapitalRequiredByVenue, sample.Legs, sample.RejectReasons)

	if sample.ID != "cross_venue_spot_perp:BTC-USDT:binance+bybit:cvsp-snap-test" {
		t.Fatalf("id = %q", sample.ID)
	}
	perpMargin := crossVenueSpotPerpPerpMarginRequired(in.Config, sample.Legs[1].Notional)
	if perpMargin > in.AvailableUSDTByVenue[bybit.VenueName]+1e-9 {
		t.Fatalf("perp margin %.4f should fit %.2fU available", perpMargin, in.AvailableUSDTByVenue[bybit.VenueName])
	}
}

func TestScanCrossVenueSpotPerpDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	beforeFunding := in.FundingByVenue[bybit.VenueName]["BTC-USDT"]
	beforeMarket := in.MarketsByVenue[binance.VenueName]["BTC-USDT"]
	_ = ScanCrossVenueSpotPerpOpportunities(in)
	if in.FundingByVenue[bybit.VenueName]["BTC-USDT"] != beforeFunding {
		t.Fatal("scanner mutated funding input")
	}
	if in.MarketsByVenue[binance.VenueName]["BTC-USDT"] != beforeMarket {
		t.Fatal("scanner mutated market input")
	}
}

func TestCarryScannerExpectedFundingUSDTAppliesCaptureProbability(t *testing.T) {
	full := carryScannerExpectedFundingUSDT(100, 0.01, 1, 1.0)
	half := carryScannerExpectedFundingUSDT(100, 0.01, 1, 0.5)
	if math.Abs(full-1.0) > 1e-9 || math.Abs(half-0.5) > 1e-9 {
		t.Fatalf("capture scaling full=%.6f half=%.6f", full, half)
	}
}

func TestCrossVenueSpotPerpExpectedBasisPnLPositiveCarryCostOnly(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	got := crossVenueSpotPerpExpectedBasisPnL(2.5, 100, cfg)
	if got >= 0 {
		t.Fatalf("positive carry basis pnl = %.6f, want cost-only <=0", got)
	}
}

func TestCarryScannerDustResidualCostUSDT(t *testing.T) {
	// 残腿成本 = 较大腿 min-notional × 0.05(偶发遗留,非两腿全额)。
	if got := carryScannerDustResidualCostUSDT([]float64{5, 10}, false); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("carry dust cost = %.6f, want 0.5", got)
	}
	// perp-perp 残腿成本现按名义比例(0.08%)计,封顶旧固定值 0.125、下限 0.01。
	// 名义未知/无仓(<=0)时应为 0(不收幻影成本,避免污染 EV)。
	if got := carryScannerPerpPerpDustResidualCostUSDT(0); math.Abs(got-0) > 1e-9 {
		t.Fatalf("perp-perp dust cost (notional<=0) = %.6f, want 0 (no phantom cost)", got)
	}
	// $25 名义:25 × 8bps = 0.02,小仓被大幅摊薄(远低于旧 0.125)。
	if got := carryScannerPerpPerpDustResidualCostUSDT(25); math.Abs(got-0.02) > 1e-9 {
		t.Fatalf("perp-perp dust cost ($25) = %.6f, want 0.02", got)
	}
	// 大仓封顶:$1000 × 8bps = 0.8 → 封顶 0.125。
	if got := carryScannerPerpPerpDustResidualCostUSDT(1000); math.Abs(got-0.125) > 1e-9 {
		t.Fatalf("perp-perp dust cost ($1000) = %.6f, want ceiling 0.125", got)
	}
	// 残腿成本应远小于单腿 min-notional,避免压垮小仓 EV。
	if carryScannerDustResidualCostUSDT([]float64{5, 10}, false) >= 5 {
		t.Fatal("dust cost should be far below a single leg min-notional")
	}
}

func TestScanCrossVenueSpotPerpZeroCandidateDiagnostics(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	in := CrossVenueSpotPerpScanInput{
		Now: now, Symbols: []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			bybit.VenueName: {"BTC-USDT": scannerTestEstablishedFundingSnap(0.001, nextFunding)},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
			bybit.VenueName: {"BTC-USDT": {SpotMid: 100000, PerpMid: 100050, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true}},
		}),
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 500},
		Config:               DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor),
	}
	if len(ScanCrossVenueSpotPerpOpportunities(in)) != 0 {
		t.Fatal("same-venue spot+perp must produce 0 cross-venue candidates")
	}
}

func TestScanCrossVenueSpotPerpFunnelRollupSameVenueOnly(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	in := CrossVenueSpotPerpScanInput{
		Now: now, Symbols: []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			bybit.VenueName: {"BTC-USDT": scannerTestEstablishedFundingSnap(0.001, nextFunding)},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
			bybit.VenueName: {"BTC-USDT": {SpotMid: 100000, PerpMid: 100050, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true}},
		}),
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 500},
		Config:               DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor),
	}
	out := captureCrossVenueCarryFunnelLog(t, func() {
		_ = ScanCrossVenueSpotPerpOpportunities(in)
	})
	if !strings.Contains(out, "[carry-funnel] cvsp") {
		t.Fatalf("missing funnel rollup log: %s", out)
	}
	if !strings.Contains(out, "pair_skip_same_venue=1") || !strings.Contains(out, "pair_attempts=0") {
		t.Fatalf("expected same-venue skip funnel, got: %s", out)
	}
}

func TestScanCrossVenueSpotPerpFunnelRollupHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	out := captureCrossVenueCarryFunnelLog(t, func() {
		_ = ScanCrossVenueSpotPerpOpportunities(testCrossVenueSpotPerpScanInput(now))
	})
	if !strings.Contains(out, "pair_attempts=2") || !strings.Contains(out, "emitted=2") {
		t.Fatalf("expected happy-path funnel counts, got: %s", out)
	}
}

func findCrossVenueSpotPerpOpp(opps []UnifiedOpportunity, symbol, spotVenue string) *UnifiedOpportunity {
	for i := range opps {
		if opps[i].Symbol != symbol {
			continue
		}
		if strings.Contains(opps[i].ID, spotVenue) {
			return &opps[i]
		}
	}
	return nil
}

func containsStringReject(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func TestScanCrossVenueSpotPerpBasisAndNetEV(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100200,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected opportunities")
	}
	if opps[0].ExpectedFundingUSDT <= 0 {
		t.Fatalf("expected_funding_usdt = %.4f, want >0", opps[0].ExpectedFundingUSDT)
	}
	entryBasis := crossVenueSpotPerpEntryBasisUSDT(100000, 100200, opps[0].Legs[0].Notional)
	if entryBasis <= 0 {
		t.Fatalf("entry basis = %.4f, want >0 for perp premium", entryBasis)
	}
	expectedBasis := crossVenueSpotPerpExpectedBasisPnL(entryBasis, opps[0].Legs[0].Notional, in.Config)
	if expectedBasis >= 0 {
		t.Fatalf("positive carry basis contribution = %.6f, want cost-only <=0", expectedBasis)
	}
}

func TestScanCrossVenueSpotPerpAppendMultipleLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	AppendCrossVenueSpotPerpCandidates(dir, "cycle", testCrossVenueExecutorConfig().CrossVenueExecutor, opps)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		count++
	}
	if count != len(opps) {
		t.Fatalf("lines = %d, want %d", count, len(opps))
	}
}
