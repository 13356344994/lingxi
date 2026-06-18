package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

func TestBuildUnifiedCapitalSnapshot_FreeReservedOccupied(t *testing.T) {
	cfg := defaultCapitalRouterConfigForTests()
	cfg.ReserveUSDTByVenue = map[string]float64{binance.VenueName: 10}
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 2, MarginSafetyFactor: 1.5}

	held := []HeldPosition{{
		OpportunityID:          "held-svsp",
		StrategyType:           UnifiedStrategySingleVenueSpotPerp,
		Symbol:                 "BTC-USDT",
		CapitalRequiredByVenue: map[string]float64{binance.VenueName: 100},
	}}
	fetched := map[string]float64{binance.VenueName: 200}

	snap := BuildUnifiedCapitalSnapshot(CapitalSnapshotInput{
		FetchedByVenue: fetched,
		Held:           held,
		RouterCfg:      cfg,
		ExecCfg:        execCfg,
	})

	if len(snap.Venues) != 1 {
		t.Fatalf("venues=%d want 1: %+v", len(snap.Venues), snap.Venues)
	}
	venueRow := snap.Venues[0]
	if venueRow.Venue != binance.VenueName {
		t.Fatalf("venue=%q", venueRow.Venue)
	}
	if venueRow.UnifiedWallet {
		t.Fatal("binance should not be unified wallet in test cfg")
	}

	var spotRow, futuresRow *VenueWalletCapital
	for i := range venueRow.Wallets {
		switch venueRow.Wallets[i].Wallet {
		case WalletKindSpot:
			spotRow = &venueRow.Wallets[i]
		case WalletKindFutures:
			futuresRow = &venueRow.Wallets[i]
		}
	}
	if spotRow == nil || futuresRow == nil {
		t.Fatalf("wallet rows=%+v want spot+futures", venueRow.Wallets)
	}

	occSpot := spotRow.OccupiedByStrategy[UnifiedStrategySingleVenueSpotPerp]
	occFut := futuresRow.OccupiedByStrategy[UnifiedStrategySingleVenueSpotPerp]
	if occSpot <= 0 || occFut <= 0 {
		t.Fatalf("occupied spot=%v futures=%v", occSpot, occFut)
	}
	if spotRow.ReservedUSDT+futuresRow.ReservedUSDT < 9.9 || spotRow.ReservedUSDT+futuresRow.ReservedUSDT > 10.1 {
		t.Fatalf("reserved total=%v+%v want ~10", spotRow.ReservedUSDT, futuresRow.ReservedUSDT)
	}
	if spotRow.FreeUSDT < 0 || futuresRow.FreeUSDT < 0 {
		t.Fatalf("free must be non-negative: spot=%v futures=%v", spotRow.FreeUSDT, futuresRow.FreeUSDT)
	}
}

func TestBuildUnifiedCapitalSnapshot_PendingAndUnfundable(t *testing.T) {
	cfg := defaultCapitalRouterConfigForTests()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 2, MarginSafetyFactor: 1.5}

	opp := UnifiedOpportunity{
		ID:           "cv-spot-perp-1",
		StrategyType: UnifiedStrategyCrossVenueSpotPerp,
		Symbol:       "ETH-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: okx.VenueName, Market: "spot", Side: "buy", Notional: 100},
			{Venue: bybit.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{
			okx.VenueName:   100,
			bybit.VenueName: 75,
		},
	}
	balances := VenueWalletBalances{
		okx.VenueName:   {WalletKindSpot: 10, WalletKindFutures: 5},
		bybit.VenueName: {WalletKindSpot: 5, WalletKindFutures: 200},
	}
	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, cfg)

	snap := BuildUnifiedCapitalSnapshot(CapitalSnapshotInput{
		FetchedByVenue: map[string]float64{
			okx.VenueName:   15,
			bybit.VenueName: 205,
		},
		PendingOpportunities: []UnifiedOpportunity{opp},
		RoutingPlan:          plan,
		RouterCfg:            cfg,
		ExecCfg:              execCfg,
	})

	if len(snap.PendingOpportunities) != 1 {
		t.Fatalf("pending=%d want 1", len(snap.PendingOpportunities))
	}
	if snap.PendingOpportunities[0].OpportunityID != "cv-spot-perp-1" {
		t.Fatalf("pending id=%q", snap.PendingOpportunities[0].OpportunityID)
	}
	if snap.PendingOpportunities[0].TotalRequiredUSDT <= 0 {
		t.Fatalf("pending total=%v", snap.PendingOpportunities[0].TotalRequiredUSDT)
	}
	if len(snap.Unfundable) != 1 {
		t.Fatalf("unfundable=%d want 1: %+v", len(snap.Unfundable), snap.Unfundable)
	}
	u := snap.Unfundable[0]
	if u.OpportunityID != "cv-spot-perp-1" || u.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
		t.Fatalf("unfundable=%+v", u)
	}
	if !u.CrossVenueCanResolve {
		t.Fatalf("cross-venue should help: %+v suggestions=%+v", u, snap.CrossVenueSuggestions)
	}
	if len(snap.CrossVenueSuggestions) != 1 {
		t.Fatalf("cross suggestions=%+v", snap.CrossVenueSuggestions)
	}
}

func TestBuildUnifiedCapitalSnapshot_OmitsPendingWhenEmpty(t *testing.T) {
	snap := BuildUnifiedCapitalSnapshot(CapitalSnapshotInput{
		FetchedByVenue: map[string]float64{gate.VenueName: 50},
		RouterCfg:      defaultCapitalRouterConfigForTests(),
		ExecCfg:        CrossVenueExecutorConfig{MaxEffectiveLeverage: 2, MarginSafetyFactor: 1.5},
	})
	if snap.PendingOpportunities != nil {
		t.Fatalf("pending should be omitted, got %+v", snap.PendingOpportunities)
	}
	if snap.Unfundable != nil {
		t.Fatalf("unfundable should be omitted, got %+v", snap.Unfundable)
	}
}

func TestCapitalSnapshotReadOnly_NoTransferSideEffects(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	r.venues = map[string]venue.Venue{}
	r.fetchDeps.fetchAvailableUSDT = func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
		return map[string]float64{binance.VenueName: 500}
	}

	opp := UnifiedOpportunity{
		ID:           "open-svsp",
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       "BTC-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: binance.VenueName, Market: "spot", Side: "buy", Notional: 100},
			{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{binance.VenueName: 75},
	}
	balances := VenueWalletBalances{
		binance.VenueName: {WalletKindSpot: 20, WalletKindFutures: 200},
	}
	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())
	r.storeLastCapitalSnapshotContext([]UnifiedOpportunity{opp}, plan)

	beforeOpens := len(svsp.opens)
	snap := r.CapitalSnapshotReadOnly(context.Background())
	if len(svsp.opens) != beforeOpens {
		t.Fatalf("CapitalSnapshotReadOnly must not open positions: opens=%d", len(svsp.opens))
	}
	if len(snap.Venues) == 0 {
		t.Fatal("expected venue breakdown")
	}
	if len(snap.PendingOpportunities) != 1 {
		t.Fatalf("pending=%+v", snap.PendingOpportunities)
	}
}

func TestRotationCapitalSnapshotAPI_ReadOnly(t *testing.T) {
	r := testUnifiedRotationRunnerWithFakes(t)
	r.fetchDeps.fetchAvailableUSDT = func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
		return map[string]float64{bybit.VenueName: 100}
	}
	s := &DashboardServer{rotationRunner: r, logger: log.New(io.Discard, "", 0)}

	req := httptest.NewRequest(http.MethodGet, "/api/rotation/capital-snapshot", nil)
	rec := httptest.NewRecorder()
	s.handleRotationCapitalSnapshotAPI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "generated_at") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
