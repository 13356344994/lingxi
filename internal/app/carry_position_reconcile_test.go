package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
)

func TestVenueSymbolsMatchUnderscoreHyphen(t *testing.T) {
	if !venueSymbolsMatch("BEL_USDT", "BEL-USDT") {
		t.Fatal("expected BEL_USDT to match BEL-USDT")
	}
	if !venueSymbolsMatch("btc-usdt", "BTC-USDT") {
		t.Fatal("expected case-insensitive match")
	}
	if venueSymbolsMatch("ETH-USDT", "BTC-USDT") {
		t.Fatal("expected different symbols to not match")
	}
}

func TestPerpPositionQtySymbolNormalization(t *testing.T) {
	positions := []venue.Position{
		{Symbol: "BEL_USDT", PositionAmt: -59},
	}
	got := absQty(perpPositionQty(positions, "BEL-USDT"))
	if got != 59 {
		t.Fatalf("perpPositionQty = %.6f, want 59", got)
	}
}

func TestCarryStrategyOwnsSpotLegCVSP(t *testing.T) {
	dir := t.TempDir()
	cvspStore, err := newCrossVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	_ = cvspStore.upsert(CrossVenueSpotPerpPosition{
		Symbol: "MNT-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
		Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "mnt-test",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 18.43},
	})
	if !carryStrategyOwnsSpotLeg(dir, bybit.VenueName, "MNT-USDT") {
		t.Fatal("expected bybit MNT spot owned by CVSP")
	}
	if carryStrategyOwnsSpotLeg(dir, gate.VenueName, "MNT-USDT") {
		t.Fatal("gate is perp venue, not spot owner")
	}
	if carryStrategyOwnsSpotLeg(dir, bybit.VenueName, "BTC-USDT") {
		t.Fatal("unexpected ownership for unrelated symbol")
	}
}

func TestSpotOrphanCrossStrategyDedup(t *testing.T) {
	ctx := context.Background()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	logger := testSVSPLogger()
	now := time.Date(2026, 6, 19, 19, 9, 0, 0, time.UTC)

	t.Run("SVSP skips CVSP-owned spot orphan", func(t *testing.T) {
		dir := t.TempDir()
		v := newSVSPTestVenue(bybit.VenueName)
		v.book = venue.BookTicker{BidPrice: 0.49, AskPrice: 0.51}
		v.spotBalances["MNT"] = 18.43

		cvspStore, err := newCrossVenueSpotPerpPositionStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		_ = cvspStore.upsert(CrossVenueSpotPerpPosition{
			Symbol: "MNT-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
			Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "mnt-cvsp",
			SpotLeg: CrossVenueSpotPerpLegState{Qty: 18.43},
		})

		legs := carryScanUntrackedSVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{bybit.VenueName: v}, nil)
		if len(legs) != 0 {
			t.Fatalf("SVSP should skip CVSP-owned bybit MNT spot orphan, got %+v", legs)
		}
	})

	t.Run("CVSP skips SVSP-owned spot orphan", func(t *testing.T) {
		dir := t.TempDir()
		v := newCVSPTestVenue(bybit.VenueName)
		v.bookSpot = venue.BookTicker{BidPrice: 0.49, AskPrice: 0.51}
		v.balances["MNT"] = 18.43

		svspStore, err := newSingleVenueSpotPerpPositionStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		_ = svspStore.upsert(SingleVenueSpotPerpPosition{
			Symbol: "MNT-USDT", Venue: bybit.VenueName, Status: SVSPStatusOpen,
			OpenedAt: now, UpdatedAt: now, IdemPrefix: "mnt-svsp",
			SpotLeg: SingleVenueSpotPerpLegState{Qty: 18.43},
		})

		legs := carryScanUntrackedCVSPLegs(ctx, cfg, dir, logger, map[string]venue.Venue{bybit.VenueName: v}, nil)
		if len(legs) != 0 {
			t.Fatalf("CVSP should skip SVSP-owned bybit MNT spot orphan, got %+v", legs)
		}
	})
}

func TestCarryStrategyOwnsPerpLegCVSP(t *testing.T) {
	dir := t.TempDir()
	cvspStore, err := newCrossVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	_ = cvspStore.upsert(CrossVenueSpotPerpPosition{
		Symbol: "BEL-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
		Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "bel-test",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 59},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 59},
	})
	if !carryStrategyOwnsPerpLeg(dir, gate.VenueName, "BEL-USDT") {
		t.Fatal("expected gate BEL perp owned by CVSP")
	}
	if carryStrategyOwnsPerpLeg(dir, bybit.VenueName, "BEL-USDT") {
		t.Fatal("bybit is spot venue, not perp owner")
	}
	if carryStrategyOwnsPerpLeg(dir, gate.VenueName, "BTC-USDT") {
		t.Fatal("unexpected ownership for unrelated symbol")
	}
}

func TestCrossVenueReconcileIgnoresCarryOwnedPerpLeg(t *testing.T) {
	dir := t.TempDir()
	ppStore := mustCrossVenueStore(t, dir)
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	gateV := newCrossVenueTestVenue(gate.VenueName, 0.001, 8, now.Add(2*time.Hour))
	gateV.positions = []venue.Position{{Symbol: "BEL_USDT", PositionAmt: -59, MarkPrice: 0.106, EntryPrice: 0.106}}

	cvspStore, err := newCrossVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = cvspStore.upsert(CrossVenueSpotPerpPosition{
		Symbol: "BEL-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
		Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "bel-carry",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 59},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 59},
	})

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.UntrackedExposureAction = "halt"
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: gateV},
		store:     ppStore,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.openingsEnabled.Store(true)
	e.reconcileOnStart(context.Background(), true)
	if !e.OpeningsEnabled() {
		t.Fatal("perp-perp should not latch halt when carry owns gate perp leg")
	}
	if atomic.LoadInt32(gateV.placeCount) != 0 {
		t.Fatal("perp-perp must not place orders on carry-owned leg")
	}
}

func TestCrossVenueSpotPerpReconcileReadoptsStalePerpLegAndCloses(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(bybit.VenueName)
	perp := newCVSPTestVenue(gate.VenueName)
	spot.balances["BEL"] = 59
	spot.balances["USDT"] = 10000
	perp.positions = []venue.Position{{Symbol: "BEL_USDT", PositionAmt: -59, EntryPrice: 0.10627}}

	e := testCVSPExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true

	pos := CrossVenueSpotPerpPosition{
		Symbol: "BEL-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "bel-readopt",
		SpotLeg: CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "spot", Side: "buy", Qty: 59},
		PerpLeg: CrossVenueSpotPerpLegState{Venue: gate.VenueName, Market: "perp", Side: "short", Qty: 0},
	}
	_ = e.persistPosition(pos)

	e.reconcileOnStart(context.Background(), true)

	loaded, err := e.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("positions = %d, want 1", len(loaded))
	}
	if absQty(loaded[0].PerpLeg.Qty-59) > executionQtyTolerance {
		t.Fatalf("perp_leg.qty = %.6f, want 59 re-adopted", loaded[0].PerpLeg.Qty)
	}

	_ = e.closePosition(context.Background(), loaded[0], true, "test flatten")
	var sawPerpClose bool
	for _, req := range perp.placeRequests {
		if req.Market == venue.MarketPerp && req.Side == venue.SideBuy && req.ReduceOnly {
			sawPerpClose = true
		}
	}
	if !sawPerpClose {
		t.Fatalf("expected gate perp reduce-only buy close, requests=%+v", perp.placeRequests)
	}
}

func TestCrossVenueSpotPerpReconcileReadoptsStalePerpLegQty(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(bybit.VenueName)
	perp := newCVSPTestVenue(gate.VenueName)
	spot.balances["BEL"] = 59
	perp.positions = []venue.Position{{Symbol: "BEL-USDT", PositionAmt: -59}}

	e := testCVSPExecutor(t, dir, spot, perp)
	pos := CrossVenueSpotPerpPosition{
		Symbol: "BEL-USDT", SpotVenue: bybit.VenueName, PerpVenue: gate.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "bel-qty",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 59},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 0},
	}
	_ = e.persistPosition(pos)
	e.reconcileOnStart(context.Background(), false)

	loaded, _ := e.store.load()
	if len(loaded) != 1 || absQty(loaded[0].PerpLeg.Qty-59) > executionQtyTolerance {
		t.Fatalf("ledger not re-adopted: %+v", loaded)
	}
}
