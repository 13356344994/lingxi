package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

type fakeRotationExecutor struct {
	strategyType   string
	opens          []UnifiedOpportunity
	closes         []string
	liveOpens      int32
	verifiedCloses int32
	held           []rotationHeldRecord
	reconcileCalls int32
	manageCalls    int32
	lastReconcileLive bool
	lastManageLive    bool
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
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			f.closes = append(f.closes, opportunityID)
			atomic.AddInt32(&f.verifiedCloses, 1)
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
		reconcileFn: func(ctx context.Context, live bool) {
			atomic.AddInt32(&f.reconcileCalls, 1)
			f.lastReconcileLive = live
		},
		manageFn: func(ctx context.Context, live bool) {
			atomic.AddInt32(&f.manageCalls, 1)
			f.lastManageLive = live
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
	r.active.Store(true)
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

	r.runOnceDecisionAndDispatch(context.Background(), candidates, available, nil, nil)

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

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{newOpp}, available, nil, nil)

	if len(svsp.closes) != 2 {
		t.Fatalf("svsp closes = %v, want 2 (close + switch close-old)", svsp.closes)
	}
	if svsp.closes[0] != "held-close" || svsp.closes[1] != "held-old" {
		t.Fatalf("svsp close ids = %v", svsp.closes)
	}
	if len(cvsp.opens) != 1 || cvsp.opens[0].ID != "open-new" {
		t.Fatalf("cvsp opens = %+v, want open-new", cvsp.opens)
	}
	if svsp.verifiedCloses != 2 {
		t.Fatalf("shadow mode should still verified-close, got %d", svsp.verifiedCloses)
	}
	if cvsp.liveOpens != 0 {
		t.Fatalf("shadow mode should not live open, got %d", cvsp.liveOpens)
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

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{bybit.VenueName: 200}, nil, nil)

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

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if err := r.runOnce(context.Background()); err != nil {
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
	if err := r.runOnce(context.Background()); err != nil {
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

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unified_candidates.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("expected unified_candidates.jsonl lines after scan")
	}
	var rec UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.CycleID != "offline-snap" {
		t.Fatalf("cycle_id = %q, want offline-snap", rec.CycleID)
	}
}

func TestUnifiedRotationRunner_TickerInterval(t *testing.T) {
	r := &UnifiedRotationRunner{scanner: &CrossVenueScanner{cfg: CrossVenueScannerConfig{ScanIntervalMinutes: 5}}}
	if got := r.tickerInterval(); got != 60*time.Second {
		t.Fatalf("interval = %v, want 60s (fixed poll, independent of scanner)", got)
	}
	r.scanner = nil
	if got := r.tickerInterval(); got != 60*time.Second {
		t.Fatalf("default interval = %v, want 60s", got)
	}
}

func TestUnifiedRotationRunner_RunOnceWritesCandidates(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	snap := CrossVenueScanSnapshot{
		SnapshotID: "runonce-snap",
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
	r := &UnifiedRotationRunner{
		bindings:    map[string]*rotationExecutorBinding{},
		logger:      log.New(io.Discard, "", 0),
		nowFn:       func() time.Time { return now },
		recordDir:   dir,
		execCfg:     testCrossVenueExecutorConfig().CrossVenueExecutor,
		rotationCfg: DefaultRotationConfig(),
		scanner:     &CrossVenueScanner{lastSnapshot: snap},
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
	r.active.Store(true)
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{}
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unified_candidates.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("expected unified_candidates.jsonl lines after runOnce")
	}
	st := r.Status()
	if st.Candidates.Total == 0 {
		t.Fatalf("status candidates = %+v, want >0", st.Candidates)
	}
	for _, line := range lines {
		var rec UnifiedCandidateLogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if rec.CycleID != "runonce-snap" {
			t.Fatalf("cycle_id = %q, want runonce-snap", rec.CycleID)
		}
	}
}

func TestUnifiedRotationRunner_RunOnceSkipsEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	var scanCalls int32
	r := testUnifiedRotationRunnerWithFakes(t)
	r.recordDir = dir
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return []UnifiedOpportunity{
			testRotationCandidate("c1", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
		}, map[string]float64{binance.VenueName: 100}, nil
	}
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: ""}}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce empty snap: %v", err)
	}
	if scanCalls != 0 {
		t.Fatalf("scanCalls = %d, want 0 for empty snapshot", scanCalls)
	}
	if r.lastSnapshotID != "" {
		t.Fatalf("lastSnapshotID = %q, want empty", r.lastSnapshotID)
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

func TestUnifiedRotationRunner_SetActiveSkipsRunCycle(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	var scanCalls int32
	var decideCalls int32
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		atomic.AddInt32(&decideCalls, 1)
		return RotationPlan{}
	}
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-active", ScannedAt: time.Now().UTC()}}

	r.SetActive(false)
	r.runCycle(context.Background())
	if scanCalls != 0 {
		t.Fatalf("paused runCycle scanCalls = %d, want 0", scanCalls)
	}
	if decideCalls != 0 {
		t.Fatalf("paused runCycle decideCalls = %d, want 0", decideCalls)
	}
	if svsp.manageCalls != 1 {
		t.Fatalf("paused runCycle manageCalls = %d, want 1", svsp.manageCalls)
	}

	r.SetActive(true)
	r.runCycle(context.Background())
	if scanCalls != 1 {
		t.Fatalf("active runCycle scanCalls = %d, want 1", scanCalls)
	}
	if decideCalls != 1 {
		t.Fatalf("active runCycle decideCalls = %d, want 1", decideCalls)
	}
	if svsp.manageCalls != 2 {
		t.Fatalf("active runCycle manageCalls = %d, want 2", svsp.manageCalls)
	}
}

func TestUnifiedRotationRunner_ReconcileManageUseRunnerLive(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetLive(false)

	r.runReconcileAll(context.Background())
	if svsp.lastReconcileLive {
		t.Fatal("shadow runner must pass live=false to reconcile")
	}

	r.runManageAll(context.Background())
	if svsp.lastManageLive {
		t.Fatal("shadow runner must pass live=false to manage")
	}

	r.SetLive(true)
	r.runReconcileAll(context.Background())
	if !svsp.lastReconcileLive {
		t.Fatal("live runner must pass live=true to reconcile")
	}
	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("live runner must pass live=true to manage")
	}
}

func TestUnifiedRotationRunner_ShadowReconcileDoesNotFlattenUntracked(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.004, MarkPrice: 110000, EntryPrice: 110000, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	svspExec := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	svspExec.nowFn = func() time.Time { return now }

	r := testUnifiedRotationRunnerWithFakes(t)
	if b := bindSingleVenueSpotPerpRotationExecutor(svspExec); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.SetLive(false)
	r.runReconcileAll(context.Background())

	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("shadow reconcile must not flatten untracked position, got %d orders", atomic.LoadInt32(v.placeCount))
	}
}

func TestUnifiedRotationRunner_RunReconcileOnStart(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	svps := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenuePerpSpot}
	cvps := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenuePerpSpot}
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp, svps, cvps)
	r.scanCandidatesFn = func(context.Context, CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return nil, map[string]float64{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx, false)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&svsp.reconcileCalls) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	for _, f := range []*fakeRotationExecutor{svsp, cvsp, svps, cvps} {
		if f.reconcileCalls != 1 {
			t.Fatalf("%s reconcileCalls = %d, want 1", f.strategyType, f.reconcileCalls)
		}
	}
}

func TestUnifiedRotationRunner_RunCycleCallsManageEachBinding(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	svps := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenuePerpSpot}
	cvps := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenuePerpSpot}
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp, svps, cvps)
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-manage", ScannedAt: time.Now().UTC()}}
	r.scanCandidatesFn = func(context.Context, CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}

	r.runCycle(context.Background())
	for _, f := range []*fakeRotationExecutor{svsp, cvsp, svps, cvps} {
		if f.manageCalls != 1 {
			t.Fatalf("%s manageCalls = %d, want 1", f.strategyType, f.manageCalls)
		}
	}
}

func TestUnifiedRotationRunner_SetLiveAffectsDispatch(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50})
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.SetLive(false)
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if svsp.liveOpens != 0 {
		t.Fatalf("shadow liveOpens = %d, want 0", svsp.liveOpens)
	}

	r.SetLive(true)
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if svsp.liveOpens != 1 {
		t.Fatalf("live liveOpens = %d, want 1", svsp.liveOpens)
	}
}

func TestUnifiedRotationRunner_StatusReflectsLastCycle(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	cycleAt := time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC)
	r.nowFn = func() time.Time { return cycleAt }
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-status", ScannedAt: cycleAt}}
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return []UnifiedOpportunity{
			testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
			testRotationCandidate("open-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 10, map[string]float64{binance.VenueName: 25, bybit.VenueName: 25}),
		}, map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{Actions: []RotationAction{
			{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
			{Kind: RotationActionHold, OpportunityID: "held-1", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
		}}
	}

	r.SetLive(true)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	st := r.Status()
	if !st.Active || !st.Live {
		t.Fatalf("status active/live = %v/%v, want true/true", st.Active, st.Live)
	}
	if st.LastSnapshotID != "snap-status" {
		t.Fatalf("LastSnapshotID = %q", st.LastSnapshotID)
	}
	if st.LastCycleAt != cycleAt.Format(time.RFC3339) {
		t.Fatalf("LastCycleAt = %q, want %q", st.LastCycleAt, cycleAt.Format(time.RFC3339))
	}
	if st.Candidates.Total != 2 || st.Candidates.SVSP != 1 || st.Candidates.CVSP != 1 {
		t.Fatalf("candidates = %+v", st.Candidates)
	}
	if st.Actions.Open != 1 || st.Actions.Hold != 1 {
		t.Fatalf("actions = %+v", st.Actions)
	}
}

func TestUnifiedRotationRunner_InitStateFromControlFile(t *testing.T) {
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t)
	r.recordDir = dir

	r.SetLive(true)
	r.SetActive(true)

	r2 := testUnifiedRotationRunnerWithFakes(t)
	r2.recordDir = dir
	r2.InitState(false, false)

	if !r2.active.Load() {
		t.Fatal("active should come from control file (true)")
	}
	if !r2.live.Load() {
		t.Fatal("live should come from control file (true)")
	}

	st, ok, err := loadRotationControlState(dir)
	if err != nil || !ok {
		t.Fatalf("load control: ok=%v err=%v", ok, err)
	}
	if !st.Active || !st.Live || st.UpdatedAt == "" {
		t.Fatalf("control file = %+v", st)
	}
	info, err := os.Stat(rotationControlFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestUnifiedRotationRunner_InitStateDefaultsWithoutFile(t *testing.T) {
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t)
	r.recordDir = dir
	r.InitState(true, false)

	if !r.active.Load() {
		t.Fatal("active should use default true")
	}
	if r.live.Load() {
		t.Fatal("live should use default false")
	}
}

func TestUnifiedRotationRunner_RunUsesInitStateNotLiveParam(t *testing.T) {
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t)
	r.recordDir = dir
	r.InitState(true, true)
	r.scanCandidatesFn = func(context.Context, CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return nil, map[string]float64{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx, false)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.Status().Live {
			cancel()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("Run(false) should not override InitState live=true")
}

func TestUnifiedRotationRunner_ShadowCloseRealHeldVerified(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	svspExec := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	svspExec.nowFn = func() time.Time { return now }

	// 合成台账: 交易所无真实持仓
	_ = svspExec.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName, OpportunityID: "syn-opp",
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "syn",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})

	r := testUnifiedRotationRunnerWithFakes(t, &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp})
	if b := bindSingleVenueSpotPerpRotationExecutor(svspExec); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.SetLive(false)

	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionClose, OpportunityID: "syn-opp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT", Reason: "test"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), nil, map[string]float64{binance.VenueName: 500}, nil, nil)

	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("synthetic close must not place orders, got %d", atomic.LoadInt32(v.placeCount))
	}
	ps, _ := svspExec.store.load()
	if len(activeSingleVenueSpotPerpPositions(ps)) != 0 {
		t.Fatal("synthetic ledger should be cleared")
	}

	// 真实持仓: shadow 下 CLOSE 仍应下单
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	_ = svspExec.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName, OpportunityID: "live-opp",
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "live",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})
	plan = RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionClose, OpportunityID: "live-opp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT", Reason: "test"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), nil, map[string]float64{binance.VenueName: 500}, nil, nil)

	if atomic.LoadInt32(v.placeCount) < 2 {
		t.Fatalf("real close in shadow mode should place orders, got %d", atomic.LoadInt32(v.placeCount))
	}

	// OPEN 在 shadow 下不下真单
	openOpp := testRotationCandidate("open-new", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 10, map[string]float64{binance.VenueName: 50})
	plan = RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-new", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "ETH-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	placesBefore := atomic.LoadInt32(v.placeCount)
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{openOpp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if atomic.LoadInt32(v.placeCount) != placesBefore {
		t.Fatalf("shadow OPEN must not place orders")
	}
}

func TestUnifiedRotationRunner_ShadowStatusReportsRealHeld(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	svspExec := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	svspExec.nowFn = func() time.Time { return now }
	_ = svspExec.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName, OpportunityID: "live-opp",
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "live",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})

	r := &UnifiedRotationRunner{
		bindings: map[string]*rotationExecutorBinding{},
		logger:   log.New(io.Discard, "", 0),
		recordDir: dir,
	}
	if b := bindSingleVenueSpotPerpRotationExecutor(svspExec); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.InitState(true, false)

	st := r.Status()
	if st.RealHeldManaged != 1 {
		t.Fatalf("RealHeldManaged = %d, want 1", st.RealHeldManaged)
	}
	if st.Live {
		t.Fatal("should be shadow mode")
	}
}

func TestDefaultScanRotationCandidatesIncludesPerpPerp(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r := &UnifiedRotationRunner{
		bindings:                  map[string]*rotationExecutorBinding{},
		logger:                    log.New(io.Discard, "", 0),
		nowFn:                     func() time.Time { return now },
		execCfg:                   testCrossVenueExecutorConfig().CrossVenueExecutor,
		includePerpPerpCandidates: true,
		fetchDeps: unifiedShadowPipelineFetchDeps{
			fetchMarkets: func(context.Context, map[string]venue.Venue, []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
				return map[string]map[string]CrossVenueSpotPerpMarketSnap{}
			},
			fetchAvailableUSDT: func(context.Context, map[string]venue.Venue, []string) map[string]float64 {
				return map[string]float64{binance.VenueName: 100}
			},
		},
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "pp-snap",
		ScannedAt:  now,
		Opportunities: []CrossVenueOpportunity{{
			Symbol:     "BTC-USDT",
			HighVenue:  binance.VenueName,
			LowVenue:   bybit.VenueName,
			SnapshotID: "pp-snap",
			NetEV:      5,
			SpreadEV:   10,
			OpenCost:   1,
			CloseCost:  1,
		}},
	}
	candidates, _, err := r.defaultScanRotationCandidates(context.Background(), snap)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	found := false
	for _, c := range candidates {
		if c.StrategyType == UnifiedStrategyPerpPerp {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected perp-perp candidate from snap.Opportunities")
	}
}

func TestUnifiedRotationRunner_OpenSkipsWithoutBinding(t *testing.T) {
	r := testUnifiedRotationRunnerWithFakes(t)
	perpPerp := testRotationCandidate("pp-open", UnifiedStrategyPerpPerp, "BTC-USDT", 10,
		map[string]float64{binance.VenueName: 50, bybit.VenueName: 50})
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "pp-open", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{perpPerp}, map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}, nil, nil)
}

func TestRotationWalletBalancesFromAvailableSplitsSpotPerp(t *testing.T) {
	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	held := []HeldPosition{{
		OpportunityID: "held-svsp",
		StrategyType:  UnifiedStrategySingleVenueSpotPerp,
		CapitalRequiredByVenue: map[string]float64{
			bybit.VenueName: 100 + rotationPerpMarginWithBuffer(execCfg, 100),
		},
	}}
	balances := rotationWalletBalancesFromAvailable(
		map[string]float64{bybit.VenueName: 0},
		held,
		execCfg,
	)
	w := balances[bybit.VenueName]
	if w[WalletKindSpot] <= 0 {
		t.Fatalf("spot wallet = %.4f, want >0 for spot-perp held", w[WalletKindSpot])
	}
	if w[WalletKindFutures] <= 0 {
		t.Fatalf("futures wallet = %.4f, want >0 for spot-perp held", w[WalletKindFutures])
	}
	if w[WalletKindSpot]+w[WalletKindFutures] < 100 {
		t.Fatalf("total split = %.4f, want ~held total", w[WalletKindSpot]+w[WalletKindFutures])
	}
}

func TestRotationPerpMarginWithBufferMatchesScanner(t *testing.T) {
	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	notional := 100.0
	got := rotationPerpMarginWithBuffer(execCfg, notional)
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	cvCfg := singleVenueSpotPerpAsCrossVenueConfig(scannerCfg)
	want := crossVenueSpotPerpPerpMarginRequired(cvCfg, notional)
	if got != want {
		t.Fatalf("rotation margin = %.6f, scanner margin = %.6f", got, want)
	}
	if got <= crossVenuePerVenueMarginUSDT(execCfg, notional) {
		t.Fatalf("buffered margin %.6f should exceed bare margin %.6f", got, crossVenuePerVenueMarginUSDT(execCfg, notional))
	}
}

func TestNewUnifiedRotationRunner_DefaultActiveFalse(t *testing.T) {
	r := NewUnifiedRotationRunner(
		CrossVenueExecutorConfig{},
		DefaultRotationConfig(),
		DefaultCapitalRouterConfig(),
		nil, nil, nil, nil,
		nil, nil, "", log.New(io.Discard, "", 0),
	)
	if r.active.Load() {
		t.Fatal("constructor default active should be false")
	}
	if r.live.Load() {
		t.Fatal("constructor default live should be false")
	}
}
