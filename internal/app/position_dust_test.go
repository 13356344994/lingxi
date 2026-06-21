package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestApplyCarryDustResidualCost(t *testing.T) {
	if got := applyCarryDustResidualCost(0.45); got != 0.45 {
		t.Fatalf("dust cost = %v, want 0.45", got)
	}
	if got := applyCarryDustResidualCost(-1); got != 0 {
		t.Fatalf("negative notional dust cost = %v, want 0", got)
	}
}

func TestEffectiveDustThresholdUsesExchangeMinNotional(t *testing.T) {
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	if got := effectiveDustThresholdUSDT(cfg, 3.0); got != 3.0 {
		t.Fatalf("threshold = %v, want 3 (exchange min)", got)
	}
	if got := effectiveDustThresholdUSDT(cfg, 0.5); got != 1.0 {
		t.Fatalf("threshold = %v, want 1 (config floor)", got)
	}
}

func TestEvaluatePositiveCarryOrphanDustVsNaked(t *testing.T) {
	ctx := context.Background()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	venues := map[string]venue.Venue{
		"gate": &cvspTestVenue{
			name:     "gate",
			bookSpot: venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041},
		},
		"binance": newCVSPTestVenue("binance"),
	}

	dust := evaluatePositiveCarryOrphan(ctx, cfg, venues, "PHA-USDT", "gate", "binance", true, false, 1.01, 0, 0.04)
	if !dust.IsOrphan || !dust.IsDustOrphan || dust.NakedRisk {
		t.Fatalf("expected dust orphan, got %+v", dust)
	}
	if dust.RemainingNotionalUSDT >= 1.0 {
		t.Fatalf("dust notional = %v, want <1", dust.RemainingNotionalUSDT)
	}

	naked := evaluatePositiveCarryOrphan(ctx, cfg, venues, "PHA-USDT", "gate", "binance", false, true, 0, 10, 0)
	if !naked.IsOrphan || !naked.NakedRisk || naked.IsDustOrphan {
		t.Fatalf("expected naked perp orphan, got %+v", naked)
	}
}

func TestEvaluateNegativeCarryOrphanSpotShortDustVsNaked(t *testing.T) {
	ctx := context.Background()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	venues := map[string]venue.Venue{
		"binance": &svpsTestVenue{
			fakeFundingVenue: fakeFundingVenue{name: "binance"},
			book:             venue.BookTicker{BidPrice: 0.047, AskPrice: 0.049},
		},
	}

	dust := evaluateNegativeCarryOrphan(ctx, cfg, venues, "FLUX-USDT", "binance", "binance", true, false, 0.10459, 0, 0.05, 0)
	if !dust.IsOrphan || !dust.IsDustOrphan || dust.NakedRisk {
		t.Fatalf("expected spot-short dust orphan, got %+v", dust)
	}
	if dust.RemainingNotionalUSDT >= 1.0 {
		t.Fatalf("dust notional = %v, want <1", dust.RemainingNotionalUSDT)
	}

	nakedVenue := &svpsTestVenue{
		fakeFundingVenue: fakeFundingVenue{name: "binance"},
		book:             venue.BookTicker{BidPrice: 99, AskPrice: 101},
	}
	naked := evaluateNegativeCarryOrphan(ctx, cfg, map[string]venue.Venue{"binance": nakedVenue}, "BTC-USDT", "binance", "binance", true, false, 0.5, 0, 100, 0)
	if !naked.IsOrphan || !naked.NakedRisk || naked.IsDustOrphan {
		t.Fatalf("expected naked spot-short orphan, got %+v", naked)
	}

	perpDust := evaluateNegativeCarryOrphan(ctx, cfg, venues, "BTC-USDT", "binance", "binance", false, true, 0, 0.00008, 0, 100)
	if !perpDust.IsOrphan || !perpDust.IsDustOrphan || perpDust.NakedRisk {
		t.Fatalf("expected perp dust orphan, got %+v", perpDust)
	}
}

func TestIsNotionalFilterError(t *testing.T) {
	if isNotionalFilterError(nil) {
		t.Fatal("nil should not match")
	}
	if !isNotionalFilterError(errors.New("HTTP 400 code=-1013 msg=Filter failure: NOTIONAL")) {
		t.Fatal("expected binance NOTIONAL reject to match")
	}
	if isNotionalFilterError(errors.New("HTTP 400 code=-1013 msg=Filter failure: PERCENT_PRICE")) {
		t.Fatal("percent price reject should not match NOTIONAL")
	}
}

func TestDustCloseDeferUntilFunding(t *testing.T) {
	next := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)
	before := next.Add(-time.Minute)
	after := next.Add(time.Minute)
	if !dustCloseDeferUntilFunding(before, next) {
		t.Fatal("expected defer before settlement")
	}
	if dustCloseDeferUntilFunding(after, next) {
		t.Fatal("should not defer after settlement")
	}
	if !dustCloseDeferUntilFunding(before, time.Time{}) {
		t.Fatal("zero nextFunding should conservative defer")
	}
}

func TestCarryDeferDustOrphanDecision(t *testing.T) {
	next := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)
	now := next.Add(-time.Hour)
	dust := dustOrphanDecision{IsOrphan: true, IsDustOrphan: true}
	if !carryDeferDustOrphanDecision(now, next, dust) {
		t.Fatal("expected dust defer before funding")
	}
	naked := dustOrphanDecision{IsOrphan: true, IsDustOrphan: true, NakedRisk: true}
	if carryDeferDustOrphanDecision(now, next, naked) {
		t.Fatal("naked risk must not defer")
	}
}

func TestCarryEVWeakFundingCloseAllowed(t *testing.T) {
	next := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		now    time.Time
		next   time.Time
		syncAt time.Time
		want   bool
	}{
		{"before funding", next.Add(-time.Hour), next, time.Time{}, false},
		{"after funding no sync", next.Add(time.Hour), next, time.Time{}, false},
		{"after funding stale sync", next.Add(time.Hour), next, next.Add(-time.Minute), false},
		{"after funding post-sync", next.Add(time.Hour), next, next.Add(time.Minute), true},
		{"zero next funding", next.Add(-time.Hour), time.Time{}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := carryEVWeakFundingCloseAllowed(tc.now, tc.next, tc.syncAt); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestCarryScanUntrackedSVSPPerpLeg(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	logger := testSVSPLogger()
	v := newSVSPTestVenue("binance")
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{"binance": v}, nil)
	if len(legs) != 1 || legs[0].Market != venue.MarketPerp {
		t.Fatalf("expected 1 untracked perp leg, got %+v", legs)
	}
	store, err := newSingleVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = store.upsert(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: "binance", Status: SVSPStatusOpen,
		OpenedAt: now, UpdatedAt: now, IdemPrefix: "tracked",
	})
	ps, _ := store.load()
	legs2 := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{"binance": v}, ps)
	if len(legs2) != 0 {
		t.Fatalf("tracked symbol should be skipped, got %+v", legs2)
	}
}

func TestCarryScanUntrackedSVSPFiltersDustSpotLeg(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	logger := testSVSPLogger()
	v := newSVSPTestVenue("bybit")
	v.book = venue.BookTicker{BidPrice: 0.49, AskPrice: 0.51}
	v.spotBalances["MNT"] = 0.001 // notional ≈ $0.0005 < $1 dust threshold
	legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{"bybit": v}, nil)
	if len(legs) != 0 {
		t.Fatalf("dust spot leg should be filtered, got %+v", legs)
	}
	ledgerPath := filepath.Join(dir, "dust_ledger.json")
	records, err := readJSONFile[DustLedgerRecord](ledgerPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected dust ledger record, err=%v records=%+v", err, records)
	}
	if records[0].Reason != "carry_untracked_dust" {
		t.Fatalf("ledger reason = %q, want carry_untracked_dust", records[0].Reason)
	}

	v.spotBalances["MNT"] = 18.43 // MNT≈$9.7 material
	legs = carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{"bybit": v}, nil)
	if len(legs) != 1 || legs[0].Symbol != "MNT-USDT" {
		t.Fatalf("material MNT leg should remain, got %+v", legs)
	}
}

func TestCarrySingleVenueOpenQtyAlign(t *testing.T) {
	spot := venue.Instrument{StepSize: 0.01, MinNotional: 5}
	perp := venue.Instrument{StepSize: 0.001, MinNotional: 5}
	caps := venue.Capabilities{}
	got, err := carrySingleVenueOpenQtyAlign(0.1234, 100, spot, perp, caps)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if got != 0.123 {
		t.Fatalf("qty = %v want 0.123 (min of step floors)", got)
	}
	_, err = carrySingleVenueOpenQtyAlign(0.0001, 100, spot, perp, caps)
	if err == nil {
		t.Fatal("expected below min notional error")
	}
}

type flattenVerifySpotVenue struct {
	fakeFundingVenue
	spotQty float64
}

func (v *flattenVerifySpotVenue) FetchBalances(context.Context) (venue.Balances, error) {
	byAsset := map[string]venue.Balance{
		"USDT": {Asset: "USDT", Free: 10000},
	}
	if v.spotQty > 0 {
		byAsset["MNT"] = venue.Balance{Asset: "MNT", Free: v.spotQty}
	}
	return venue.Balances{ByAsset: byAsset}, nil
}

func (v *flattenVerifySpotVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return nil, nil
}

func TestFlattenSecondVerification(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := testSVSPLogger()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}

	leg := carryUntrackedLeg{
		Venue: "bybit", Symbol: "MNT-USDT", Market: venue.MarketSpot,
		Qty: 18.43, Side: venue.SideSell, ReduceOnly: false,
	}
	v := &flattenVerifySpotVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}, spotQty: 18.43}
	venues := map[string]venue.Venue{"bybit": v}

	carryAfterUntrackedFlatten(ctx, dir, logger, venues, leg, venue.OrderResult{
		ClientOrderID: "test-flat-1",
		ExecutedQty:   0,
		Status:        "canceled",
	}, errors.New("IOC 未成交"))

	ledgerPath := filepath.Join(dir, "dust_ledger.json")
	records, err := readJSONFile[DustLedgerRecord](ledgerPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected unresolved_orphan dust ledger, err=%v records=%+v", err, records)
	}
	if records[0].Reason != unresolvedOrphanReason {
		t.Fatalf("ledger reason = %q, want %q", records[0].Reason, unresolvedOrphanReason)
	}

	legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, venues, nil)
	if len(legs) != 0 {
		t.Fatalf("unresolved orphan should not re-enter untracked scan, got %+v", legs)
	}
	if !carryShouldSkipUntrackedFlatten(dir, logger, leg) {
		t.Fatal("expected skip repeat flatten for unresolved orphan")
	}

	v.spotQty = 0
	carryAfterUntrackedFlatten(ctx, dir, logger, venues, leg, venue.OrderResult{
		ClientOrderID: "test-flat-2",
		ExecutedQty:   leg.Qty,
		Status:        "filled",
	}, nil)
	if !recentlyFlattened(dir, recentFlattenKeyFromLeg(leg), recentFlattenTTL) {
		t.Fatal("expected recent flatten registry after verified clear")
	}
}

func TestRecentFlattenRegistryDedup(t *testing.T) {
	dir := t.TempDir()
	leg := carryUntrackedLeg{
		Venue: "bybit", Symbol: "MNT-USDT", Market: venue.MarketSpot,
		Qty: 18.43, Side: venue.SideSell,
	}
	markRecentFlatten(dir, recentFlattenKeyFromLeg(leg))
	if !carryShouldSkipUntrackedFlatten(dir, nil, leg) {
		t.Fatal("expected skip after recent flatten mark")
	}
}

func TestFlattenIOCUnfilledMarksUnresolved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := testSVSPLogger()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}

	leg := carryUntrackedLeg{
		Venue: "bybit", Symbol: "MNT-USDT", Market: venue.MarketSpot,
		Qty: 18.43, Side: venue.SideSell,
	}
	v := &flattenVerifySpotVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}, spotQty: 18.43}
	venues := map[string]venue.Venue{"bybit": v}

	carryAfterUntrackedFlatten(ctx, dir, logger, venues, leg, venue.OrderResult{
		ClientOrderID: "ioc-unfilled",
		Status:        "canceled",
		ExecutedQty:   0,
	}, errors.New("IOC 未成交"))

	records, err := readJSONFile[DustLedgerRecord](filepath.Join(dir, "dust_ledger.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("expected unresolved ledger, err=%v records=%+v", err, records)
	}
	if records[0].Reason != unresolvedOrphanReason {
		t.Fatalf("reason = %q, want %q", records[0].Reason, unresolvedOrphanReason)
	}
	if legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, venues, nil); len(legs) != 0 {
		t.Fatalf("unresolved orphan should not re-scan, got %+v", legs)
	}
	if !carryShouldSkipUntrackedFlatten(dir, logger, leg) {
		t.Fatal("expected skip repeat flatten after IOC unfilled")
	}
}

func TestFlattenSubmitFailedPersists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := testSVSPLogger()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}

	leg := carryUntrackedLeg{
		Venue: "bybit", Symbol: "MNT-USDT", Market: venue.MarketSpot,
		Qty: 18.43, Side: venue.SideSell,
	}
	v := &flattenVerifySpotVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}, spotQty: 18.43}
	venues := map[string]venue.Venue{"bybit": v}

	carryAfterUntrackedFlatten(ctx, dir, logger, venues, leg, venue.OrderResult{Status: "rejected"}, errors.New("simulated spot fail"))

	records, err := readJSONFile[DustLedgerRecord](filepath.Join(dir, "dust_ledger.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("expected flatten_submit_failed ledger, err=%v records=%+v", err, records)
	}
	if records[0].Reason != flattenSubmitFailedReason {
		t.Fatalf("reason = %q, want %q", records[0].Reason, flattenSubmitFailedReason)
	}
	if legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, venues, nil); len(legs) != 0 {
		t.Fatalf("submit-failed orphan should not re-scan, got %+v", legs)
	}
	if !carryShouldSkipUntrackedFlatten(dir, logger, leg) {
		t.Fatal("expected skip repeat flatten after submit failure")
	}
}

func TestFlattenCrossStrategySameStartupDedup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := testSVSPLogger()

	leg := carryUntrackedLeg{
		Venue: "bybit", Symbol: "MNT-USDT", Market: venue.MarketSpot,
		Qty: 18.43, Side: venue.SideSell,
	}
	v := &flattenVerifySpotVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}, spotQty: 18.43}
	venues := map[string]venue.Venue{"bybit": v}

	carryAfterUntrackedFlatten(ctx, dir, logger, venues, leg, venue.OrderResult{
		ClientOrderID: "svsp-flat",
		Status:        "canceled",
		ExecutedQty:   0,
	}, errors.New("IOC 未成交"))

	if !carryShouldSkipUntrackedFlatten(dir, logger, leg) {
		t.Fatal("CVSP should skip same leg after SVSP flatten attempt")
	}

	cvspV := newCVSPTestVenue("bybit")
	cvspV.balances["MNT"] = 18.43
	cvspExec := NewCrossVenueSpotPerpExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{"bybit": cvspV}, dir, logger)
	before := atomic.LoadInt32(cvspV.placeCount)
	cvspExec.flattenUntrackedLeg(ctx, leg, true)
	after := atomic.LoadInt32(cvspV.placeCount)
	if after != before {
		t.Fatalf("CVSP should not place order after SVSP marked leg, placeCount %d->%d", before, after)
	}
}
