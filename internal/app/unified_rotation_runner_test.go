package app

import (
	"context"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

type fakeRotationExecutor struct {
	strategyType string
	opens        []UnifiedOpportunity
	closes       []string
	liveOpens    int32
	liveCloses   int32
	held         []rotationHeldRecord
}

func (f *fakeRotationExecutor) binding() *rotationExecutorBinding {
	if f == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: f.strategyType,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			f.opens = append(f.opens, opp)
			if live {
				atomic.AddInt32(&f.liveOpens, 1)
			}
			return nil
		},
		closeFn: func(ctx context.Context, opportunityID, symbol, reason string, live bool) error {
			f.closes = append(f.closes, opportunityID)
			if live {
				atomic.AddInt32(&f.liveCloses, 1)
			}
			return nil
		},
		heldFn: func() []rotationHeldRecord {
			if f.held == nil {
				return nil
			}
			out := make([]rotationHeldRecord, len(f.held))
			copy(out, f.held)
			return out
		},
	}
}

func testUnifiedRotationRunnerWithFakes(t *testing.T, fakes ...*fakeRotationExecutor) *UnifiedRotationRunner {
	t.Helper()
	r := &UnifiedRotationRunner{
		bindings:         map[string]*rotationExecutorBinding{},
		logger:           log.New(io.Discard, "", 0),
		nowFn:            func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		rotationCfg:      DefaultRotationConfig(),
		capitalRouterCfg: DefaultCapitalRouterConfig(),
	}
	for _, f := range fakes {
		if f == nil {
			continue
		}
		if b := f.binding(); b != nil {
			r.bindings[b.strategyType] = b
		}
	}
	return r
}

func testRotationCandidate(id, strategy, symbol string, netEV float64, capByVenue map[string]float64) UnifiedOpportunity {
	legs := make([]UnifiedOpportunityLeg, 0, len(capByVenue))
	for venue, notional := range capByVenue {
		legs = append(legs, UnifiedOpportunityLeg{Venue: venue, Market: "perp", Side: "short", Notional: notional})
	}
	return UnifiedOpportunity{
		ID:                     id,
		StrategyType:           strategy,
		Symbol:                 symbol,
		ExpectedNetEVUSDT:      netEV,
		ExpectedFundingUSDT:    netEV * 0.8,
		ExpectedOpenCostUSDT:   1,
		ExpectedCloseCostUSDT:  1,
		CapitalRequiredByVenue: capByVenue,
		Legs:                   legs,
	}
}

func TestUnifiedRotationRunner_DispatchOpenRoutesByStrategy(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	svps := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenuePerpSpot}
	cvps := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenuePerpSpot}
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp, svps, cvps)

	candidates := []UnifiedOpportunity{
		testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
		testRotationCandidate("open-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 10, map[string]float64{binance.VenueName: 25, bybit.VenueName: 25}),
		testRotationCandidate("open-svps", UnifiedStrategySingleVenuePerpSpot, "SOL-USDT", 10, map[string]float64{bybit.VenueName: 50}),
		testRotationCandidate("open-cvps", UnifiedStrategyCrossVenuePerpSpot, "AVAX-USDT", 10, map[string]float64{binance.VenueName: 25, bybit.VenueName: 25}),
	}
	available := map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
		{Kind: RotationActionOpen, OpportunityID: "open-cvsp", StrategyType: UnifiedStrategyCrossVenueSpotPerp, Symbol: "ETH-USDT"},
		{Kind: RotationActionOpen, OpportunityID: "open-svps", StrategyType: UnifiedStrategySingleVenuePerpSpot, Symbol: "SOL-USDT"},
		{Kind: RotationActionOpen, OpportunityID: "open-cvps", StrategyType: UnifiedStrategyCrossVenuePerpSpot, Symbol: "AVAX-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), false, candidates, available, nil, nil)

	if len(svsp.opens) != 1 || svsp.opens[0].ID != "open-svsp" {
		t.Fatalf("svsp opens = %+v, want open-svsp", svsp.opens)
	}
	if len(cvsp.opens) != 1 || cvsp.opens[0].ID != "open-cvsp" {
		t.Fatalf("cvsp opens = %+v, want open-cvsp", cvsp.opens)
	}
	if len(svps.opens) != 1 || svps.opens[0].ID != "open-svps" {
		t.Fatalf("svps opens = %+v, want open-svps", svps.opens)
	}
	if len(cvps.opens) != 1 || cvps.opens[0].ID != "open-cvps" {
		t.Fatalf("cvps opens = %+v, want open-cvps", cvps.opens)
	}
	if svsp.liveOpens != 0 || cvsp.liveOpens != 0 || svps.liveOpens != 0 || cvps.liveOpens != 0 {
		t.Fatalf("shadow mode should not live-open: svsp=%d cvsp=%d svps=%d cvps=%d",
			svsp.liveOpens, cvsp.liveOpens, svps.liveOpens, cvps.liveOpens)
	}
}

func TestUnifiedRotationRunner_DispatchCloseAndSwitch(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp)

	newOpp := testRotationCandidate("open-new", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 20,
		map[string]float64{binance.VenueName: 40, bybit.VenueName: 40})
	available := map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionClose, OpportunityID: "held-close", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT", Reason: "test close"},
		{
			Kind:                 RotationActionSwitch,
			OpportunityID:        "held-old",
			StrategyType:         UnifiedStrategySingleVenueSpotPerp,
			Symbol:               "BTC-USDT",
			ReplaceOpportunityID: "open-new",
			ReplaceStrategyType:  UnifiedStrategyCrossVenueSpotPerp,
			ReplaceSymbol:        "ETH-USDT",
			Reason:               "test switch",
		},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), false, []UnifiedOpportunity{newOpp}, available, nil, nil)

	if len(svsp.closes) != 2 {
		t.Fatalf("svsp closes = %v, want 2 (close + switch close-old)", svsp.closes)
	}
	if svsp.closes[0] != "held-close" || svsp.closes[1] != "held-old" {
		t.Fatalf("svsp close ids = %v", svsp.closes)
	}
	if len(cvsp.opens) != 1 || cvsp.opens[0].ID != "open-new" {
		t.Fatalf("cvsp opens = %+v, want open-new", cvsp.opens)
	}
	if svsp.liveCloses != 0 || cvsp.liveOpens != 0 {
		t.Fatalf("shadow mode should not live close/open")
	}
}

func TestUnifiedRotationRunner_ShadowModeNoLiveTransfer(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.venues = map[string]venue.Venue{
		bybit.VenueName: &rotationTransferCountVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}},
	}

	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10,
		map[string]float64{bybit.VenueName: 20})
	opp.Legs = []UnifiedOpportunityLeg{
		{Venue: bybit.VenueName, Market: "spot", Side: "buy", Notional: 20},
		{Venue: bybit.VenueName, Market: "perp", Side: "short", Notional: 20},
	}
	balances := VenueWalletBalances{
		bybit.VenueName: {WalletKindFutures: 200, WalletKindSpot: 0},
	}
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), false, []UnifiedOpportunity{opp}, map[string]float64{bybit.VenueName: 200}, nil, nil)

	v := r.venues[bybit.VenueName].(*rotationTransferCountVenue)
	if v.transferCount != 0 {
		t.Fatalf("shadow mode transferCount = %d, want 0", v.transferCount)
	}
	if len(svsp.opens) != 1 {
		t.Fatalf("expected shadow open dispatch, got %d", len(svsp.opens))
	}
	if svsp.liveOpens != 0 {
		t.Fatalf("shadow mode liveOpens = %d, want 0", svsp.liveOpens)
	}

	routingPlan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, DefaultCapitalRouterConfig())
	if len(routingPlan.WithinVenueTransfers) == 0 {
		t.Fatal("expected within-venue transfer in routing plan for sanity check")
	}
	executed, _ := ExecuteCapitalRouting(context.Background(), routingPlan, r.venues, false, r.logger)
	if executed != 0 {
		t.Fatalf("shadow ExecuteCapitalRouting executed = %d, want 0", executed)
	}
	if v.transferCount != 0 {
		t.Fatalf("shadow ExecuteCapitalRouting should not call TransferWallet")
	}
}

type rotationTransferCountVenue struct {
	fakeFundingVenue
	transferCount int32
}

func (v *rotationTransferCountVenue) TransferWallet(context.Context, string, string, float64) error {
	atomic.AddInt32(&v.transferCount, 1)
	return nil
}

func TestUnifiedRotationRunner_RunOnceSkipsDuplicateSnapshot(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.scanner = &CrossVenueScanner{
		lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-dup", ScannedAt: time.Now().UTC()},
	}
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		svsp.opens = append(svsp.opens, UnifiedOpportunity{ID: snap.SnapshotID})
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}

	if err := r.runOnce(context.Background(), false); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if err := r.runOnce(context.Background(), false); err != nil {
		t.Fatalf("runOnce duplicate: %v", err)
	}
	if len(svsp.opens) != 1 {
		t.Fatalf("scan calls = %d, want 1 (duplicate snapshot skipped)", len(svsp.opens))
	}
}

func TestUnifiedRotationRunner_DecideRotationIntegration(t *testing.T) {
	svsp := &fakeRotationExecutor{
		strategyType: UnifiedStrategySingleVenueSpotPerp,
		held: []rotationHeldRecord{{
			HeldPosition: HeldPosition{
				OpportunityID:          "held-btc",
				StrategyType:           UnifiedStrategySingleVenueSpotPerp,
				Symbol:                 "BTC-USDT",
				CapitalRequiredByVenue: map[string]float64{binance.VenueName: 80},
			},
			ContinuationInputs: ContinuationInputs{
				ExpectedFundingUSDT:   1,
				ExpectedCloseCostUSDT: 5,
			},
		}},
	}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp)

	newOpp := testRotationCandidate("new-eth", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 30,
		map[string]float64{binance.VenueName: 40, bybit.VenueName: 40})
	available := map[string]float64{binance.VenueName: 200, bybit.VenueName: 200}

	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return []UnifiedOpportunity{newOpp}, available, nil
	}
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-int", ScannedAt: time.Now().UTC()}}
	if err := r.runOnce(context.Background(), false); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if len(svsp.closes) == 0 {
		t.Fatal("expected close from DecideRotation continuation_score<0 path")
	}
	if len(cvsp.opens) == 0 {
		t.Fatal("expected open for new candidate from DecideRotation")
	}
}

func TestUnifiedRotationRunner_ScanCandidatesOffline(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	r := &UnifiedRotationRunner{
		bindings:    map[string]*rotationExecutorBinding{},
		logger:      log.New(io.Discard, "", 0),
		nowFn:       func() time.Time { return now },
		recordDir:   dir,
		execCfg:     testCrossVenueExecutorConfig().CrossVenueExecutor,
		rotationCfg: DefaultRotationConfig(),
		fetchDeps: unifiedShadowPipelineFetchDeps{
			fetchMarkets: func(context.Context, map[string]venue.Venue, []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
				return map[string]map[string]CrossVenueSpotPerpMarketSnap{
					bybit.VenueName: {
						"BTC-USDT": {SpotMid: 100000, PerpMid: 100050, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
					binance.VenueName: {
						"BTC-USDT": {SpotMid: 100010, PerpMid: 100080, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
				}
			},
			fetchAvailableUSDT: func(context.Context, map[string]venue.Venue, []string) map[string]float64 {
				return map[string]float64{binance.VenueName: 200, bybit.VenueName: 200}
			},
		},
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "offline-snap",
		ScannedAt:  now,
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {
				"BTC-USDT": {Rate: 0.0003, IntervalHours: 8, NextFundingTime: nextFunding},
			},
			binance.VenueName: {
				"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
			},
		},
	}
	candidates, available, err := r.defaultScanRotationCandidates(context.Background(), snap)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(available) == 0 {
		t.Fatal("expected available balances from fake fetch")
	}
	if len(candidates) == 0 {
		t.Fatal("expected candidates from offline scan")
	}
	strategies := map[string]int{}
	for _, c := range candidates {
		strategies[c.StrategyType]++
	}
	if strategies[UnifiedStrategySingleVenueSpotPerp] == 0 && strategies[UnifiedStrategyCrossVenueSpotPerp] == 0 {
		t.Fatalf("expected spot-perp candidates, got %v", strategies)
	}
}

func TestUnifiedRotationRunner_TickerInterval(t *testing.T) {
	r := &UnifiedRotationRunner{scanner: &CrossVenueScanner{cfg: CrossVenueScannerConfig{ScanIntervalMinutes: 5}}}
	if got := r.tickerInterval(); got != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", got)
	}
	r.scanner = nil
	if got := r.tickerInterval(); got != 60*time.Second {
		t.Fatalf("default interval = %v, want 60s", got)
	}
}

func TestRotationBorrowMapsFromProbes(t *testing.T) {
	daily, limits := rotationBorrowMapsFromProbes([]CrossVenueBorrowProbe{
		{Symbol: "BTC-USDT", Available: true, HourlyRateAPR: 0.365, MaxBorrowable: 1000},
		{Symbol: "BTC-USDT", Available: true, HourlyRateAPR: 0.730, MaxBorrowable: 2000},
	})
	if daily["BTC"] <= 0 {
		t.Fatalf("daily rate = %v", daily["BTC"])
	}
	if limits["BTC"] != 2000 {
		t.Fatalf("limit = %v, want 2000", limits["BTC"])
	}
}

func TestRotationFreeAvailableUSDT(t *testing.T) {
	free := rotationFreeAvailableUSDT(
		map[string]float64{"binance": 200},
		[]HeldPosition{{
			CapitalRequiredByVenue: map[string]float64{"binance": 80},
		}},
	)
	if free["binance"] != 120 {
		t.Fatalf("free = %v, want 120", free["binance"])
	}
}
