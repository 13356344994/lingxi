package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

type fakeRotationExecutor struct {
	strategyType      string
	opens             []UnifiedOpportunity
	closes            []string
	liveOpens         int32
	verifiedCloses    int32
	held              []rotationHeldRecord
	reconcileCalls    int32
	manageCalls       int32
	lastReconcileLive bool
	lastManageLive    bool
	closeErr          error
	openErr           error
	onOpen            func()
}

func (f *fakeRotationExecutor) binding() *rotationExecutorBinding {
	if f == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: f.strategyType,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			if f.openErr != nil {
				return f.openErr
			}
			if f.onOpen != nil {
				f.onOpen()
			}
			f.opens = append(f.opens, opp)
			if live {
				atomic.AddInt32(&f.liveOpens, 1)
			}
			return nil
		},
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			if f.closeErr != nil {
				return f.closeErr
			}
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
	if svsp.liveOpens != 1 || cvsp.liveOpens != 1 || svps.liveOpens != 1 || cvps.liveOpens != 1 {
		t.Fatalf("active runner should live-open: svsp=%d cvsp=%d svps=%d cvps=%d",
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
		t.Fatalf("OFF mode should still verified-close, got %d", svsp.verifiedCloses)
	}
	if cvsp.liveOpens != 1 {
		t.Fatalf("active runner should live-open switch target, got %d", cvsp.liveOpens)
	}
}

func testRotationSwitchPlan() (RotationPlan, UnifiedOpportunity) {
	newOpp := testRotationCandidate("open-new", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 20,
		map[string]float64{binance.VenueName: 40, bybit.VenueName: 40})
	plan := RotationPlan{Actions: []RotationAction{{
		Kind:                 RotationActionSwitch,
		OpportunityID:        "held-old",
		StrategyType:         UnifiedStrategySingleVenueSpotPerp,
		Symbol:               "BTC-USDT",
		ReplaceOpportunityID: "open-new",
		ReplaceStrategyType:  UnifiedStrategyCrossVenueSpotPerp,
		ReplaceSymbol:        "ETH-USDT",
		Reason:               "test switch",
	}}}
	return plan, newOpp
}

func readRotationSwitchDomainEvents(t *testing.T, recordDir string) []DomainEvent {
	t.Helper()
	path := filepath.Join(recordDir, domainEventJSONLName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read domain events: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	events := make([]DomainEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event DomainEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal domain event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func TestUnifiedRotationRunner_SwitchAbortedOnCloseFailed(t *testing.T) {
	svsp := &fakeRotationExecutor{
		strategyType: UnifiedStrategySingleVenueSpotPerp,
		closeErr:     fmt.Errorf("close rejected"),
	}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp)
	r.recordDir = dir

	plan, newOpp := testRotationSwitchPlan()
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{newOpp},
		map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}, nil, nil)

	if len(svsp.closes) != 0 {
		t.Fatalf("close should not succeed, closes = %v", svsp.closes)
	}
	if len(cvsp.opens) != 0 {
		t.Fatalf("open must not be called after close failure, opens = %+v", cvsp.opens)
	}
	events := readRotationSwitchDomainEvents(t, dir)
	if len(events) != 1 {
		t.Fatalf("domain events = %+v, want 1", events)
	}
	if events[0].Type != rotationSwitchAbortedCloseFailed {
		t.Fatalf("event type = %q, want %q", events[0].Type, rotationSwitchAbortedCloseFailed)
	}
	if !strings.Contains(events[0].Reason, "held-old") || !strings.Contains(events[0].Reason, "close rejected") {
		t.Fatalf("event reason = %q", events[0].Reason)
	}
}

func TestUnifiedRotationRunner_SwitchFailedOpenRecorded(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{
		strategyType: UnifiedStrategyCrossVenueSpotPerp,
		openErr:      fmt.Errorf("open rejected"),
	}
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp)
	r.recordDir = dir

	plan, newOpp := testRotationSwitchPlan()
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{newOpp},
		map[string]float64{binance.VenueName: 500, bybit.VenueName: 500}, nil, nil)

	if len(svsp.closes) != 1 || svsp.closes[0] != "held-old" {
		t.Fatalf("svsp closes = %v, want [held-old]", svsp.closes)
	}
	if len(cvsp.opens) != 0 {
		t.Fatalf("open failed, should not record success, opens = %+v", cvsp.opens)
	}
	events := readRotationSwitchDomainEvents(t, dir)
	if len(events) != 1 {
		t.Fatalf("domain events = %+v, want 1", events)
	}
	if events[0].Type != rotationSwitchFailedOpen {
		t.Fatalf("event type = %q, want %q", events[0].Type, rotationSwitchFailedOpen)
	}
	if events[0].Level != EventLevelCritical {
		t.Fatalf("event level = %q, want critical", events[0].Level)
	}
	if !strings.Contains(events[0].Reason, "held-old") || !strings.Contains(events[0].Reason, "open rejected") {
		t.Fatalf("event reason = %q", events[0].Reason)
	}
}

func TestUnifiedRotationRunner_SwitchFailedOpenUnfundableRecorded(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp}
	dir := t.TempDir()
	r := testUnifiedRotationRunnerWithFakes(t, svsp, cvsp)
	r.recordDir = dir
	r.active.Store(true)

	plan, newOpp := testRotationSwitchPlan()
	unfundable := map[string]struct{}{"open-new": {}}
	r.dispatchRotationPlan(context.Background(), plan,
		map[string]UnifiedOpportunity{"open-new": newOpp}, unfundable)

	if len(svsp.closes) != 1 {
		t.Fatalf("close should succeed before unfundable skip, closes = %v", svsp.closes)
	}
	if len(cvsp.opens) != 0 {
		t.Fatalf("unfundable must skip open, opens = %+v", cvsp.opens)
	}
	events := readRotationSwitchDomainEvents(t, dir)
	if len(events) != 1 || events[0].Type != rotationSwitchFailedOpen {
		t.Fatalf("domain events = %+v, want switch_failed_open", events)
	}
	if !strings.Contains(events[0].Reason, "无法备资") {
		t.Fatalf("event reason = %q", events[0].Reason)
	}
}

func TestUnifiedRotationRunner_StoppedNoLiveTransfer(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetActive(false)
	r.venues = map[string]venue.Venue{
		binance.VenueName: &rotationTransferCountVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}},
	}

	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10,
		map[string]float64{binance.VenueName: 20})
	opp.Legs = []UnifiedOpportunityLeg{
		{Venue: binance.VenueName, Market: "spot", Side: "buy", Notional: 20},
		{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: 20},
	}
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 200}, nil, nil)

	v := r.venues[binance.VenueName].(*rotationTransferCountVenue)
	if v.transferCount != 0 {
		t.Fatalf("stopped runner transferCount = %d, want 0 (P2-10: openLive 复查跳过资金路由)", v.transferCount)
	}
	if len(svsp.opens) != 0 {
		t.Fatalf("stopped runner should skip OPEN dispatch (openLive=false), got %d", len(svsp.opens))
	}
	if svsp.liveOpens != 0 {
		t.Fatalf("stopped runner liveOpens = %d, want 0", svsp.liveOpens)
	}

	balances := VenueWalletBalances{
		binance.VenueName: {WalletKindFutures: 200, WalletKindSpot: 0},
	}
	cfg := defaultCapitalRouterConfigForTests()
	routingPlan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, cfg)
	if len(routingPlan.WithinVenueTransfers) == 0 {
		t.Fatal("expected within-venue transfer in routing plan for binance sanity check")
	}
	execResult := ExecuteCapitalRouting(context.Background(), routingPlan, r.venues, false, r.logger)
	if execResult.Executed != 0 {
		t.Fatalf("OFF ExecuteCapitalRouting executed = %d, want 0", execResult.Executed)
	}
	if v.transferCount != 0 {
		t.Fatalf("OFF ExecuteCapitalRouting should not call TransferWallet")
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

type rotationFailingTransferVenue struct {
	rotationTransferCountVenue
}

func (v *rotationFailingTransferVenue) TransferWallet(ctx context.Context, from, to string, amount float64) error {
	v.rotationTransferCountVenue.TransferWallet(ctx, from, to, amount)
	return fmt.Errorf("transfer rejected")
}

func TestUnifiedRotationRunner_RunOnceReevaluatesDuplicateSnapshot(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r.nowFn = func() time.Time { return now }
	r.scanner = &CrossVenueScanner{
		lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-dup", ScannedAt: now.Add(-5 * time.Minute)},
	}
	var scanCalls int32
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce duplicate: %v", err)
	}
	if scanCalls != 2 {
		t.Fatalf("scanCalls = %d, want 2 (same snapshot ID re-evaluated each tick)", scanCalls)
	}
	if r.lastSnapshotID != "snap-dup" {
		t.Fatalf("lastSnapshotID = %q, want snap-dup", r.lastSnapshotID)
	}
}

func TestUnifiedRotationRunner_RunOnceSkipsStaleSnapshot(t *testing.T) {
	r := testUnifiedRotationRunnerWithFakes(t)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r.nowFn = func() time.Time { return now }
	r.scanner = &CrossVenueScanner{
		lastSnapshot: CrossVenueScanSnapshot{
			SnapshotID: "snap-stale",
			ScannedAt:  now.Add(-31 * time.Minute),
		},
	}
	var scanCalls int32
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if scanCalls != 0 {
		t.Fatalf("scanCalls = %d, want 0 for stale snapshot", scanCalls)
	}
	st := r.Status()
	if !strings.Contains(st.LastError, "stale") {
		t.Fatalf("LastError = %q, want stale skip reason", st.LastError)
	}
	if r.lastSnapshotID != "" {
		t.Fatalf("lastSnapshotID = %q, want empty (stale cycle skipped before update)", r.lastSnapshotID)
	}
}

func TestUnifiedRotationRunner_RunOnceProceedsWithZeroOrFreshScannedAt(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("zero ScannedAt", func(t *testing.T) {
		r := testUnifiedRotationRunnerWithFakes(t)
		r.nowFn = func() time.Time { return now }
		r.scanner = &CrossVenueScanner{
			lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-zero-age"},
		}
		var scanCalls int32
		r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
			atomic.AddInt32(&scanCalls, 1)
			return nil, map[string]float64{binance.VenueName: 100}, nil
		}
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatalf("runOnce: %v", err)
		}
		if scanCalls != 1 {
			t.Fatalf("scanCalls = %d, want 1 for zero ScannedAt", scanCalls)
		}
	})

	t.Run("fresh ScannedAt", func(t *testing.T) {
		r := testUnifiedRotationRunnerWithFakes(t)
		r.nowFn = func() time.Time { return now }
		r.scanner = &CrossVenueScanner{
			lastSnapshot: CrossVenueScanSnapshot{
				SnapshotID: "snap-fresh",
				ScannedAt:  now.Add(-29 * time.Minute),
			},
		}
		var scanCalls int32
		r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
			atomic.AddInt32(&scanCalls, 1)
			return nil, map[string]float64{binance.VenueName: 100}, nil
		}
		if err := r.runOnce(context.Background()); err != nil {
			t.Fatalf("runOnce: %v", err)
		}
		if scanCalls != 1 {
			t.Fatalf("scanCalls = %d, want 1 for fresh snapshot", scanCalls)
		}
	})
}

func TestUnifiedRotationRunner_DecideRotationIntegration(t *testing.T) {
	openedAt := time.Now().Add(-2 * time.Hour)
	svsp := &fakeRotationExecutor{
		strategyType: UnifiedStrategySingleVenueSpotPerp,
		held: []rotationHeldRecord{{
			HeldPosition: HeldPosition{
				OpportunityID:          "held-btc",
				StrategyType:           UnifiedStrategySingleVenueSpotPerp,
				Symbol:                 "BTC-USDT",
				CapitalRequiredByVenue: map[string]float64{binance.VenueName: 80},
				OpenedAt:               openedAt,
			},
			ContinuationInputs: ContinuationInputs{
				ExpectedFundingUSDT:   0,
				ExpectedCloseCostUSDT: 2,
				HoldingRiskUSDT:       10,
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
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-int", ScannedAt: r.nowFn().UTC()}}
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
	nextFunding := scannerTestCarryNextFunding(now)
	r := &UnifiedRotationRunner{
		bindings:    map[string]*rotationExecutorBinding{},
		logger:      log.New(io.Discard, "", 0),
		nowFn:       func() time.Time { return now },
		recordDir:   dir,
		execCfg:     testCrossVenueExecutorConfig().CrossVenueExecutor,
		rotationCfg: DefaultRotationConfig(),
		scanner: scannerTestCarryScannerWithVolHistory(bybit.VenueName, "BTC-USDT", []float64{
			0.0020, 0.0024, 0.0022,
		}),
		fetchDeps: unifiedMarketPipelineFetchDeps{
			fetchMarkets: func(context.Context, map[string]venue.Venue, []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
				return scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
					bybit.VenueName: {
						"BTC-USDT": {SpotMid: 100000, PerpMid: 100050, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
					binance.VenueName: {
						"BTC-USDT": {SpotMid: 100010, PerpMid: 100080, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
				})
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
				"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: nextFunding},
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
	tradeable := 0
	strategies := map[string]int{}
	for _, c := range candidates {
		if len(c.RejectReasons) > 0 {
			continue
		}
		tradeable++
		strategies[c.StrategyType]++
	}
	if strategies[UnifiedStrategySingleVenueSpotPerp] == 0 && strategies[UnifiedStrategyCrossVenueSpotPerp] == 0 {
		t.Fatalf("expected tradeable spot-perp candidates, got tradeable=%d strategies=%v", tradeable, strategies)
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
	nextFunding := scannerTestCarryNextFunding(now)
	snap := CrossVenueScanSnapshot{
		SnapshotID: "runonce-snap",
		ScannedAt:  now,
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {
				"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: nextFunding},
			},
			binance.VenueName: {
				"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
			},
		},
	}
	sc := scannerTestCarryScannerWithVolHistory(bybit.VenueName, "BTC-USDT", []float64{
		0.0020, 0.0024, 0.0022,
	})
	sc.lastSnapshot = snap
	r := &UnifiedRotationRunner{
		bindings:    map[string]*rotationExecutorBinding{},
		logger:      log.New(io.Discard, "", 0),
		nowFn:       func() time.Time { return now },
		recordDir:   dir,
		execCfg:     testCrossVenueExecutorConfig().CrossVenueExecutor,
		rotationCfg: DefaultRotationConfig(),
		scanner:     sc,
		fetchDeps: unifiedMarketPipelineFetchDeps{
			fetchMarkets: func(context.Context, map[string]venue.Venue, []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
				return scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
					bybit.VenueName: {
						"BTC-USDT": {SpotMid: 100000, PerpMid: 100050, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
					binance.VenueName: {
						"BTC-USDT": {SpotMid: 100010, PerpMid: 100080, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true},
					},
				})
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
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-active", ScannedAt: r.nowFn().UTC()}}

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
	waitForRotationAsyncScan(t, &scanCalls, &decideCalls, 1, 1)
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

func waitForRotationAsyncScan(t *testing.T, scanCalls, decideCalls *int32, wantScan, wantDecide int32) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(scanCalls) >= wantScan && atomic.LoadInt32(decideCalls) >= wantDecide {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("async scan did not complete: scanCalls=%d want>=%d decideCalls=%d want>=%d",
		atomic.LoadInt32(scanCalls), wantScan, atomic.LoadInt32(decideCalls), wantDecide)
}

func TestUnifiedRotationRunner_RunCycleSkipsScanWhenInProgress(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	var scanCalls int32
	scanStarted := make(chan struct{})
	scanRelease := make(chan struct{})
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		close(scanStarted)
		<-scanRelease
		return nil, map[string]float64{binance.VenueName: 100}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{}
	}
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-block", ScannedAt: r.nowFn().UTC()}}

	r.runCycle(context.Background())
	select {
	case <-scanStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("first async scan did not start")
	}
	r.runCycle(context.Background())
	if atomic.LoadInt32(&scanCalls) != 1 {
		t.Fatalf("scanCalls = %d, want 1 (second tick must skip while scan in progress)", scanCalls)
	}
	if svsp.manageCalls != 2 {
		t.Fatalf("manageCalls = %d, want 2 (manage must run each tick)", svsp.manageCalls)
	}
	close(scanRelease)
	waitForRotationAsyncScan(t, &scanCalls, new(int32), 1, 0)
}

func TestRotationScanFilterSymbols(t *testing.T) {
	funding := map[string]map[string]CrossVenueSpotPerpFundingSnap{
		binance.VenueName: {},
		bybit.VenueName:   {},
	}
	for i := 0; i < 300; i++ {
		sym := fmt.Sprintf("SYM%03d-USDT", i)
		rate := 0.0001 + float64(i)*0.000001 // 0.0001 .. 0.000299
		funding[binance.VenueName][sym] = CrossVenueSpotPerpFundingSnap{Rate: rate}
		funding[bybit.VenueName][sym] = CrossVenueSpotPerpFundingSnap{Rate: -rate * 0.5}
	}
	funding[binance.VenueName]["LOW-USDT"] = CrossVenueSpotPerpFundingSnap{Rate: 0.00001}
	funding[bybit.VenueName]["LOW-USDT"] = CrossVenueSpotPerpFundingSnap{Rate: -0.00001}

	got := rotationScanFilterSymbols(funding, nil)
	if len(got) != rotationScanMaxSymbolsFundingOnly {
		t.Fatalf("len = %d, want %d (funding-only fallback cap)", len(got), rotationScanMaxSymbolsFundingOnly)
	}
	for _, sym := range got {
		if sym == "LOW-USDT" {
			t.Fatal("LOW-USDT below floor must be excluded")
		}
	}
	if got[0] != "SYM299-USDT" {
		t.Fatalf("first symbol = %q, want SYM299-USDT (highest max|rate|)", got[0])
	}
	if got[1] != "SYM298-USDT" {
		t.Fatalf("second symbol = %q, want SYM298-USDT", got[1])
	}
	tieFunding := map[string]map[string]CrossVenueSpotPerpFundingSnap{
		binance.VenueName: {
			"TIE-B-USDT": {Rate: 0.0005},
			"TIE-A-USDT": {Rate: 0.0005},
		},
	}
	tieGot := rotationScanFilterSymbols(tieFunding, nil)
	if len(tieGot) != 2 || tieGot[0] != "TIE-A-USDT" || tieGot[1] != "TIE-B-USDT" {
		t.Fatalf("tie-break order = %v, want [TIE-A-USDT TIE-B-USDT]", tieGot)
	}
}

func TestRotationScanFilterSymbolsIntervalNormalized(t *testing.T) {
	sameRate := 0.0002
	funding := map[string]map[string]CrossVenueSpotPerpFundingSnap{
		binance.VenueName: {
			"H1-USDT": {Rate: sameRate, IntervalHours: 1},
			"H8-USDT": {Rate: sameRate, IntervalHours: 8},
		},
	}
	got := rotationScanFilterSymbols(funding, nil)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0] != "H1-USDT" {
		t.Fatalf("1h coin should rank first on per-hour basis, got %v", got)
	}
	perHour1h := rotationFundingRatePerHour(sameRate, 1)
	perHour8h := rotationFundingRatePerHour(sameRate, 8)
	if perHour1h <= perHour8h {
		t.Fatalf("1h per-hour=%v should exceed 8h per-hour=%v", perHour1h, perHour8h)
	}
}

func TestUnifiedRotationRunner_ReconcileManageUseRunnerActive(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetActive(false)

	r.runReconcileAll(context.Background())
	if svsp.lastReconcileLive {
		t.Fatal("stopped runner must pass live=false to reconcile (仍绑定 active)")
	}

	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("stopped runner must pass live=true to manage (与 active 解耦)")
	}

	r.SetActive(true)
	r.runReconcileAll(context.Background())
	if !svsp.lastReconcileLive {
		t.Fatal("active runner must pass live=true to reconcile")
	}
	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("active runner must pass live=true to manage")
	}
}

func TestUnifiedRotationRunner_OffReconcileDoesNotFlattenUntracked(t *testing.T) {
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
	r.SetActive(false)
	r.runReconcileAll(context.Background())

	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("stopped reconcile must not flatten untracked position, got %d orders", atomic.LoadInt32(v.placeCount))
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
	go r.Run(ctx)
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
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-manage", ScannedAt: r.nowFn().UTC()}}
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

func TestUnifiedRotationRunner_ActiveAffectsDispatch(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50})
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.SetActive(false)
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if svsp.liveOpens != 0 {
		t.Fatalf("stopped liveOpens = %d, want 0", svsp.liveOpens)
	}

	r.SetActive(true)
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if svsp.liveOpens != 1 {
		t.Fatalf("active liveOpens = %d, want 1", svsp.liveOpens)
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

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	st := r.Status()
	if !st.Active {
		t.Fatalf("status active = %v, want true", st.Active)
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

	r.SetActive(true)

	r2 := testUnifiedRotationRunnerWithFakes(t)
	r2.recordDir = dir
	r2.InitState(false)

	if !r2.active.Load() {
		t.Fatal("active should come from control file (true)")
	}

	st, ok, err := loadRotationControlState(dir)
	if err != nil || !ok {
		t.Fatalf("load control: ok=%v err=%v", ok, err)
	}
	if !st.Active || st.UpdatedAt == "" {
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
	r.InitState(true)

	if !r.active.Load() {
		t.Fatal("active should use default true")
	}
}

func TestUnifiedRotationRunner_StopHaltsOpensManageStillRuns(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetActive(false)
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-stop", ScannedAt: r.nowFn().UTC()}}
	r.scanCandidatesFn = func(context.Context, CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		return []UnifiedOpportunity{
			testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
		}, map[string]float64{binance.VenueName: 500}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{Actions: []RotationAction{
			{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
		}}
	}

	r.runCycle(context.Background())
	if svsp.manageCalls != 1 {
		t.Fatalf("manageCalls = %d, want 1 while stopped", svsp.manageCalls)
	}
	if len(svsp.opens) != 0 {
		t.Fatalf("opens = %d, want 0 while stopped", len(svsp.opens))
	}
}

func TestUnifiedRotationRunner_StoppedCloseRealHeldVerified(t *testing.T) {
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
	r.SetActive(false)

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

	// 真实持仓: stopped 下 CLOSE 仍应下单(CloseOnePositionVerified 强制 verify-real)
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
		t.Fatalf("real close while stopped should place orders, got %d", atomic.LoadInt32(v.placeCount))
	}

	// OPEN 在 stopped 下不下真单
	r.SetActive(false)
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
		t.Fatalf("stopped OPEN must not place orders")
	}
}

func TestUnifiedRotationRunner_StatusReportsRealHeldWhenStopped(t *testing.T) {
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
		bindings:  map[string]*rotationExecutorBinding{},
		logger:    log.New(io.Discard, "", 0),
		recordDir: dir,
	}
	if b := bindSingleVenueSpotPerpRotationExecutor(svspExec); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.InitState(false)

	st := r.Status()
	if st.RealHeldManaged != 1 {
		t.Fatalf("RealHeldManaged = %d, want 1", st.RealHeldManaged)
	}
	if st.Active {
		t.Fatal("should be stopped")
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
		fetchDeps: unifiedMarketPipelineFetchDeps{
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
			// 无 binding 时仍为 CandidateOnly(仅对比);有 binding 时可执行。
			if !c.CandidateOnly {
				t.Fatal("perp-perp candidate should be CandidateOnly without executor binding")
			}
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
			binance.VenueName: 100 + rotationPerpMarginWithBuffer(execCfg, 100),
		},
	}}
	balances := rotationWalletBalancesFromAvailable(
		map[string]float64{binance.VenueName: 0},
		held,
		execCfg,
		map[string]venue.Venue{binance.VenueName: &fakeFundingVenue{name: binance.VenueName}},
		nil,
	)
	w := balances[binance.VenueName]
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

func TestRotationWalletBalancesFromAvailableUnifiedVenueSinglePool(t *testing.T) {
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
		map[string]venue.Venue{bybit.VenueName: &fakeFundingVenue{name: bybit.VenueName}},
		nil,
	)
	w := balances[bybit.VenueName]
	if w[WalletKindSpot] <= 0 {
		t.Fatalf("spot wallet = %.4f, want >0 unified pool", w[WalletKindSpot])
	}
	if w[WalletKindFutures] != 0 {
		t.Fatalf("futures wallet = %.4f, want 0 for unified venue", w[WalletKindFutures])
	}
}

// rotationSplitWalletVenue 模拟非统一账户 spot/perp 分仓及所内划转。
type rotationSplitWalletVenue struct {
	fakeFundingVenue
	unified     bool
	spotUSDT    float64
	futuresUSDT float64
	transfers   []walletTransferCall
}

func (v *rotationSplitWalletVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{UnifiedWallet: v.unified}
}

func (v *rotationSplitWalletVenue) FetchPerpAvailableUSDT(context.Context) (float64, error) {
	if v.unified {
		return v.spotUSDT + v.futuresUSDT, nil
	}
	return v.futuresUSDT, nil
}

func (v *rotationSplitWalletVenue) FetchSpotAvailableUSDT(context.Context) (float64, error) {
	if v.unified {
		return v.spotUSDT + v.futuresUSDT, nil
	}
	return v.spotUSDT, nil
}

func (v *rotationSplitWalletVenue) TransferWallet(_ context.Context, from, to string, amount float64) error {
	v.transfers = append(v.transfers, walletTransferCall{from: from, to: to, amount: amount})
	switch {
	case from == "spot" && to == "futures":
		move := math.Min(amount, v.spotUSDT)
		v.spotUSDT -= move
		v.futuresUSDT += move
	case from == "futures" && to == "spot":
		move := math.Min(amount, v.futuresUSDT)
		v.futuresUSDT -= move
		v.spotUSDT += move
	}
	return nil
}

func (v *rotationSplitWalletVenue) TopUpPerpMarginUSDT(_ context.Context, requiredUSDT float64) (float64, error) {
	if v.futuresUSDT+1e-9 >= requiredUSDT {
		return 0, nil
	}
	need := requiredUSDT - v.futuresUSDT
	move := math.Min(need, v.spotUSDT)
	v.spotUSDT -= move
	v.futuresUSDT += move
	return move, nil
}

func TestPerpPerpRoutingTopsUpPerpWallet(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	notional := 150.0
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	if math.Abs(margin-50) > 1e-9 {
		t.Fatalf("margin=%.4f want 50", margin)
	}

	now := time.Now().UTC()
	cvOpp := CrossVenueOpportunity{
		Symbol: "H-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
		SpreadAPR: 10, HighFundingRate: 0.006, LowFundingRate: 0.0001,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: now.Add(2 * time.Hour), LowNextFundingTime: now.Add(time.Hour),
		TradableNotional: notional, NetEV: 5,
	}
	opp := rotationAlignPerpPerpCapital(UnifiedOpportunityFromCrossVenue(execCfg, cvOpp, nil, notional, 0), execCfg)

	binanceV := &rotationSplitWalletVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, spotUSDT: 100, futuresUSDT: 0}
	bybitV := &rotationSplitWalletVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}, unified: true, spotUSDT: 100}
	venues := map[string]venue.Venue{binance.VenueName: binanceV, bybit.VenueName: bybitV}
	available := map[string]float64{binance.VenueName: 100, bybit.VenueName: 100}

	routerCfg := rotationCapitalRouterConfig(execCfg, DefaultCapitalRouterConfig(), venues)
	balances := rotationWalletBalancesFromAvailable(available, nil, execCfg, venues, []UnifiedOpportunity{opp}, routerCfg)
	if balances[binance.VenueName][WalletKindSpot] != 100 || balances[binance.VenueName][WalletKindFutures] != 0 {
		t.Fatalf("pre-route binance wallets=%+v want spot=100 futures=0", balances[binance.VenueName])
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, routerCfg)
	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("transfers=%+v want 1 binance spot→futures", plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.Venue != binance.VenueName || tr.From != WalletKindSpot || tr.To != WalletKindFutures {
		t.Fatalf("transfer=%+v want binance spot→futures", tr)
	}
	if math.Abs(tr.AmountUSDT-margin) > 1e-6 {
		t.Fatalf("transfer amount=%.4f want margin %.4f", tr.AmountUSDT, margin)
	}

	result := ExecuteCapitalRouting(context.Background(), plan, venues, true, log.New(io.Discard, "", 0))
	if len(result.Errs) != 0 {
		t.Fatalf("routing errs=%v", result.Errs)
	}
	if binanceV.futuresUSDT+1e-9 < margin {
		t.Fatalf("binance perp after route=%.4f want >= %.4f", binanceV.futuresUSDT, margin)
	}

	perpAvail, err := fetchVenuePerpAvailableUSDT(context.Background(), binanceV)
	if err != nil || perpAvail+1e-9 < margin {
		t.Fatalf("fetch perp=%.4f err=%v want >= %.4f", perpAvail, err, margin)
	}
}

func TestRotationWalletBalancesPerpPerpIdleInSpot(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	opp := rotationAlignPerpPerpCapital(UnifiedOpportunityFromCrossVenue(execCfg, CrossVenueOpportunity{
		Symbol: "H-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
	}, nil, 150, 0), execCfg)
	binanceV := &rotationSplitWalletVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}}
	venues := map[string]venue.Venue{binance.VenueName: binanceV}
	routerCfg := rotationCapitalRouterConfig(execCfg, DefaultCapitalRouterConfig(), venues)
	balances := rotationWalletBalancesFromAvailable(
		map[string]float64{binance.VenueName: 100}, nil, execCfg, venues, []UnifiedOpportunity{opp}, routerCfg,
	)
	if balances[binance.VenueName][WalletKindSpot] != 100 || balances[binance.VenueName][WalletKindFutures] != 0 {
		t.Fatalf("perp-perp idle split=%+v want all in spot", balances[binance.VenueName])
	}
}

func TestUnifiedRotationRunner_LiveTransferFailureBlocksOpen(t *testing.T) {
	pp := &fakeRotationExecutor{strategyType: UnifiedStrategyPerpPerp}
	r := testUnifiedRotationRunnerWithFakes(t, pp)
	r.SetActive(true)
	r.execCfg = CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	r.venues = map[string]venue.Venue{
		binance.VenueName: &rotationFailingTransferVenue{
			rotationTransferCountVenue: rotationTransferCountVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}},
		},
		bybit.VenueName: &fakeFundingVenue{name: bybit.VenueName},
	}

	opp := rotationAlignPerpPerpCapital(UnifiedOpportunityFromCrossVenue(r.execCfg, CrossVenueOpportunity{
		Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
	}, nil, 150, 0), r.execCfg)
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: opp.ID, StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}

	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 200, bybit.VenueName: 200}, nil, nil)

	if len(pp.opens) != 0 {
		t.Fatalf("opens = %+v, want none after failed required transfer", pp.opens)
	}
	if pp.liveOpens != 0 {
		t.Fatalf("liveOpens = %d, want 0", pp.liveOpens)
	}
	v := r.venues[binance.VenueName].(*rotationFailingTransferVenue)
	if v.transferCount != 1 {
		t.Fatalf("transferCount = %d, want 1 attempted transfer", v.transferCount)
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
		nil, nil, nil, nil, nil,
		nil, nil, "", log.New(io.Discard, "", 0),
		DefaultDelistGuardConfig(),
	)
	if r.active.Load() {
		t.Fatal("constructor default active should be false")
	}
}

func TestUnifiedRotationRunner_StoppedManageLiveWithControlPlane(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	r.SetControlPlane(cp)
	r.SetActive(false)

	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("paused + rotation_live_enabled + allow_manage_orders → manage live=true")
	}
	if svsp.manageCalls != 1 {
		t.Fatalf("manageCalls = %d, want 1", svsp.manageCalls)
	}

	// 新开仓路径仍被 active 闸住
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-cp-stop", ScannedAt: r.nowFn().UTC()}}
	var scanCalls int32
	r.scanCandidatesFn = func(context.Context, CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return []UnifiedOpportunity{
			testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
		}, map[string]float64{binance.VenueName: 500}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{Actions: []RotationAction{
			{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
		}}
	}
	r.runCycle(context.Background())
	if scanCalls != 0 {
		t.Fatalf("paused runCycle scanCalls = %d, want 0", scanCalls)
	}
	if len(svsp.opens) != 0 {
		t.Fatalf("paused must not open, got %d opens", len(svsp.opens))
	}
}

func TestUnifiedRotationRunner_RotationOffManageDryRun(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	if err := cp.setPersisted(func(st *ControlPlanePersistedState) {
		st.RotationLiveEnabled = false
	}); err != nil {
		t.Fatal(err)
	}
	r.SetControlPlane(cp)
	r.SetActive(true)

	r.runManageAll(context.Background())
	if svsp.lastManageLive {
		t.Fatal("rotation_live_enabled=false (OFF) → manage live=false")
	}
}

func TestUnifiedRotationRunner_ActiveOpenAndManageBothLive(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	r.SetControlPlane(cp)
	r.SetActive(true)

	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("active + default control plane → manage live=true")
	}

	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50})
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if svsp.liveOpens != 1 {
		t.Fatalf("active open liveOpens = %d, want 1", svsp.liveOpens)
	}
}

func TestUnifiedRotationRunner_PausedVerifiedCloseStillLive(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	r.SetControlPlane(cp)
	r.SetActive(false)

	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionClose, OpportunityID: "held-close", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT", Reason: "paused close"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), nil, map[string]float64{binance.VenueName: 500}, nil, nil)

	if svsp.verifiedCloses != 1 {
		t.Fatalf("paused + closeLive → verified close still dispatched, got %d", svsp.verifiedCloses)
	}
	if len(svsp.closes) != 1 || svsp.closes[0] != "held-close" {
		t.Fatalf("closes = %v", svsp.closes)
	}
}

func TestUnifiedRotationRunner_RotationOffSkipsVerifiedClose(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	if err := cp.setPersisted(func(st *ControlPlanePersistedState) {
		st.RotationLiveEnabled = false
	}); err != nil {
		t.Fatal(err)
	}
	r.SetControlPlane(cp)
	r.SetActive(true)

	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionClose, OpportunityID: "held-close", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT", Reason: "off close"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), nil, map[string]float64{binance.VenueName: 500}, nil, nil)

	if svsp.verifiedCloses != 0 {
		t.Fatalf("rotation OFF must skip verified close, got %d", svsp.verifiedCloses)
	}
}

func TestUnifiedRotationRunner_NilControlPlaneManageLiveWhenStopped(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetActive(false)

	if !r.manageLive() {
		t.Fatal("nil control_plane: manageLive should be true (策略已部署即真钱)")
	}
	if !r.closeLive() {
		t.Fatal("nil control_plane: closeLive should be true")
	}
	if r.openLive() {
		t.Fatal("nil control_plane: openLive should be false when active=false")
	}

	r.runManageAll(context.Background())
	if !svsp.lastManageLive {
		t.Fatal("nil control_plane stopped: manage must receive live=true")
	}
}

func TestUnifiedRotationRunner_OpenFailureRecordsCooldown(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	cvsp := &fakeRotationExecutor{strategyType: UnifiedStrategyCrossVenueSpotPerp, openErr: fmt.Errorf("open rollback")}
	r := testUnifiedRotationRunnerWithFakes(t, cvsp)
	r.nowFn = func() time.Time { return now }
	r.openCooldownStore = newRotationOpenCooldownStore("", 15*time.Minute)
	opp := testRotationCandidate("bel", UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", 10, map[string]float64{binance.VenueName: 40, "gate": 40})
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{Actions: []RotationAction{{Kind: RotationActionOpen, OpportunityID: "bel", StrategyType: UnifiedStrategyCrossVenueSpotPerp, Symbol: "BEL-USDT"}}}
	}
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500, "gate": 500}, nil, nil)
	if !r.openCooldownStore.IsBlocked(UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", "binance+gate", now.Add(time.Minute)) {
		t.Fatal("expected cooldown after open failure")
	}
}

func TestUnifiedRotationRunner_EnableRollingHoldWarningOnce(t *testing.T) {
	buf := &strings.Builder{}
	r := testUnifiedRotationRunnerWithFakes(t)
	r.logger = log.New(buf, "", 0)
	r.SetPortfolioEnableRollingHold(true)
	r.maybeWarnEnableRollingHoldNoOp()
	r.maybeWarnEnableRollingHoldNoOp()
	if strings.Count(buf.String(), "enable_rolling_hold=true") != 1 {
		t.Fatalf("unexpected warning: %s", buf.String())
	}
}

func TestRotationScanDepthFetchMaxSymbolsMatchesScanCap(t *testing.T) {
	if rotationScanDepthFetchMaxSymbols != 150 {
		t.Fatalf("depth cap=%d, want 150", rotationScanDepthFetchMaxSymbols)
	}
	if rotationScanDepthFetchMaxSymbols != rotationScanMaxSymbols {
		t.Fatalf("depth cap=%d != scan cap=%d", rotationScanDepthFetchMaxSymbols, rotationScanMaxSymbols)
	}
}

func TestRotationScanFilterSymbolsCompositePrefersLiquidity(t *testing.T) {
	rate := 0.0002
	funding := map[string]map[string]CrossVenueSpotPerpFundingSnap{
		binance.VenueName: {
			"HIGH-FUND-USDT": {Rate: rate},
			"MID-FUND-USDT":  {Rate: rate * 0.5},
		},
	}
	markets := map[string]map[string]CrossVenueSpotPerpMarketSnap{
		binance.VenueName: {
			"HIGH-FUND-USDT": {Volume24hSpotUSDT: 1e4, Volume24hPerpUSDT: 1e4},
			"MID-FUND-USDT":  {Volume24hSpotUSDT: 1e9, Volume24hPerpUSDT: 1e9},
		},
	}
	got := rotationScanFilterSymbols(funding, markets)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0] != "MID-FUND-USDT" {
		t.Fatalf("composite should rank mid funding + high liquidity first, got %v", got)
	}
	if rotationScanLiquidityProxyUSDT(markets, "MID-FUND-USDT") != 1e9 {
		t.Fatalf("liquidity proxy should use min(spot,perp) 24h volume")
	}
}

func TestUnifiedRotationRunner_SkipsCapitalRoutingOnHaltRecheck(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	if err := cp.SetGlobalHalt(true); err != nil {
		t.Fatal(err)
	}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetControlPlane(cp)
	r.active.Store(true)

	opp := testRotationCandidate("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50})
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-svsp", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{binance.VenueName: 500}, nil, nil)
	if len(svsp.opens) != 0 {
		t.Fatalf("halt recheck should skip opens, got %d", len(svsp.opens))
	}
}

func TestUnifiedRotationRunner_HaltMidDispatchStopsSubsequentOpens(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.SetControlPlane(cp)
	r.active.Store(true)

	var haltOnce sync.Once
	svsp.onOpen = func() {
		haltOnce.Do(func() { _ = cp.SetGlobalHalt(true) })
	}

	candidates := []UnifiedOpportunity{
		testRotationCandidate("open-a", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 10, map[string]float64{binance.VenueName: 50}),
		testRotationCandidate("open-b", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 10, map[string]float64{binance.VenueName: 50}),
	}
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: "open-a", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BTC-USDT"},
		{Kind: RotationActionOpen, OpportunityID: "open-b", StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "ETH-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), candidates, map[string]float64{binance.VenueName: 500}, nil, nil)
	if len(svsp.opens) != 1 {
		t.Fatalf("second open should be blocked by mid-dispatch halt recheck, opens=%d", len(svsp.opens))
	}
}

func TestUnifiedRotationRunner_ConsecutiveZeroCVSPCVPSMajorAlert(t *testing.T) {
	buf := &strings.Builder{}
	r := testUnifiedRotationRunnerWithFakes(t)
	r.logger = log.New(buf, "", 0)
	for i := 0; i < rotationZeroCVSPCVPSAlertThreshold; i++ {
		r.trackConsecutiveZeroCVSPCVPS(RotationCandidateCounts{CVSP: 0, CVPS: 0, SVSP: 1})
	}
	if !strings.Contains(buf.String(), "MAJOR") || !strings.Contains(buf.String(), "cvsp=0") {
		t.Fatalf("expected throttled major alert, got: %s", buf.String())
	}
	before := buf.String()
	r.trackConsecutiveZeroCVSPCVPS(RotationCandidateCounts{CVSP: 0, CVPS: 0})
	if buf.String() != before {
		t.Fatal("alert should be throttled within ticker interval")
	}
	r.trackConsecutiveZeroCVSPCVPS(RotationCandidateCounts{CVSP: 1, CVPS: 0})
	r.trackConsecutiveZeroCVSPCVPS(RotationCandidateCounts{CVSP: 0, CVPS: 0})
	if strings.Count(buf.String(), "MAJOR") != 1 {
		t.Fatalf("streak reset should suppress repeat until threshold again, log=%s", buf.String())
	}
}
