package app

import (
	"strings"
	"testing"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestPlanCapitalRoutingWithinVenueRebalance(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "sv-spot-perp-1",
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       "BTC-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: bybit.VenueName, Market: "spot", Side: "buy", Notional: 100},
			{Venue: bybit.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{bybit.VenueName: 75},
	}
	balances := VenueWalletBalances{
		bybit.VenueName: {
			WalletKindSpot:    20,
			WalletKindFutures: 200,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, DefaultCapitalRouterConfig())

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %d, want 1: %+v", len(plan.WithinVenueTransfers), plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.Venue != bybit.VenueName || tr.From != WalletKindFutures || tr.To != WalletKindSpot || tr.AmountUSDT != 80 {
		t.Fatalf("transfer = %+v, want bybit futures->spot 80", tr)
	}
	if len(plan.CrossVenueSuggestions) != 0 {
		t.Fatalf("cross-venue suggestions = %+v, want none", plan.CrossVenueSuggestions)
	}
	if len(plan.Unfundable) != 0 {
		t.Fatalf("unfundable = %#v, want none", plan.Unfundable)
	}
}

func TestPlanCapitalRoutingCrossVenueSuggestionOnly(t *testing.T) {
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
		okx.VenueName: {
			WalletKindSpot:    10,
			WalletKindFutures: 5,
		},
		bybit.VenueName: {
			WalletKindSpot:    5,
			WalletKindFutures: 200,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, DefaultCapitalRouterConfig())

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %+v, want one partial okx futures->spot", plan.WithinVenueTransfers)
	}
	if plan.WithinVenueTransfers[0].Venue != okx.VenueName || plan.WithinVenueTransfers[0].AmountUSDT != 5 {
		t.Fatalf("partial within-venue = %+v, want okx futures->spot 5", plan.WithinVenueTransfers[0])
	}
	if len(plan.CrossVenueSuggestions) != 1 {
		t.Fatalf("cross-venue suggestions = %d, want 1: %+v", len(plan.CrossVenueSuggestions), plan.CrossVenueSuggestions)
	}
	s := plan.CrossVenueSuggestions[0]
	if s.FromVenue != bybit.VenueName || s.ToVenue != okx.VenueName {
		t.Fatalf("suggestion = %+v, want bybit -> okx", s)
	}
	if s.AmountUSDT < 85-capitalRouterEpsilon || s.AmountUSDT > 85+capitalRouterEpsilon {
		t.Fatalf("suggestion amount = %.4f, want ~85 (okx need 100, have 15)", s.AmountUSDT)
	}
	if len(plan.Unfundable) != 0 {
		t.Fatalf("unfundable = %#v, want none (cross-venue covers okx gap)", plan.Unfundable)
	}
}

func TestPlanCapitalRoutingRespectsReserve(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "sv-spot-perp-reserve",
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       "SOL-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: bybit.VenueName, Market: "spot", Side: "buy", Notional: 50},
			{Venue: bybit.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{bybit.VenueName: 75},
	}
	balances := VenueWalletBalances{
		bybit.VenueName: {
			WalletKindSpot:    10,
			WalletKindFutures: 120,
		},
	}
	cfg := DefaultCapitalRouterConfig()
	cfg.ReserveUSDTByVenue = map[string]float64{bybit.VenueName: 30}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, cfg)

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %+v, want one futures->spot", plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.From != WalletKindFutures || tr.To != WalletKindSpot || tr.AmountUSDT != 25 {
		t.Fatalf("transfer = %+v, want futures->spot 25 (reserve locks 30 before rebalance)", tr)
	}
	if len(plan.CrossVenueSuggestions) != 0 {
		t.Fatalf("cross-venue suggestions = %+v, want none", plan.CrossVenueSuggestions)
	}
	if len(plan.Unfundable) != 1 {
		t.Fatalf("unfundable = %#v, want one (25 USDT venue shortfall after reserve)", plan.Unfundable)
	}
	if !strings.Contains(plan.Unfundable[0], "sv-spot-perp-reserve") {
		t.Fatalf("unfundable entry = %q, want opportunity id", plan.Unfundable[0])
	}
}

func TestPlanCapitalRoutingUnfundableWhenCrossVenueInsufficient(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "cv-spot-perp-unfundable",
		StrategyType: UnifiedStrategyCrossVenueSpotPerp,
		Symbol:       "BTC-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: okx.VenueName, Market: "spot", Side: "buy", Notional: 200},
			{Venue: bybit.VenueName, Market: "perp", Side: "short", Notional: 200},
		},
		CapitalRequiredByVenue: map[string]float64{
			okx.VenueName:   200,
			bybit.VenueName: 150,
		},
	}
	balances := VenueWalletBalances{
		okx.VenueName: {
			WalletKindSpot: 20,
		},
		bybit.VenueName: {
			WalletKindFutures: 160,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, DefaultCapitalRouterConfig())

	if len(plan.CrossVenueSuggestions) != 1 {
		t.Fatalf("cross-venue suggestions = %+v, want one partial suggestion", plan.CrossVenueSuggestions)
	}
	if plan.CrossVenueSuggestions[0].AmountUSDT < 10-capitalRouterEpsilon {
		t.Fatalf("expected small bybit surplus suggestion, got %+v", plan.CrossVenueSuggestions[0])
	}
	if len(plan.Unfundable) != 1 {
		t.Fatalf("unfundable = %#v, want one", plan.Unfundable)
	}
	if !strings.Contains(plan.Unfundable[0], "cv-spot-perp-unfundable") {
		t.Fatalf("unfundable entry = %q", plan.Unfundable[0])
	}
}

func TestPlanCapitalRoutingPerpSpotMarginWalletMapping(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "sv-perp-spot-1",
		StrategyType: UnifiedStrategySingleVenuePerpSpot,
		Symbol:       "DOGE-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: bybit.VenueName, Market: "perp", Side: "long", Notional: 100},
			{Venue: bybit.VenueName, Market: "spot", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{bybit.VenueName: 75},
	}
	balances := VenueWalletBalances{
		bybit.VenueName: {
			WalletKindFutures: 200,
			WalletKindMargin:  5,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, DefaultCapitalRouterConfig())

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %+v, want one", plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.From != WalletKindFutures || tr.To != WalletKindMargin || tr.AmountUSDT != 95 {
		t.Fatalf("transfer = %+v, want futures->margin 95", tr)
	}
}

func TestPlanCapitalRoutingTableDriven(t *testing.T) {
	tests := []struct {
		name             string
		chosen           []UnifiedOpportunity
		balances         VenueWalletBalances
		cfg              CapitalRouterConfig
		wantWithin       int
		wantCross        int
		wantUnfundable   int
		checkWithinFirst func(t *testing.T, tr WalletTransfer)
		checkCrossFirst  func(t *testing.T, s CrossVenueRebalanceSuggestion)
	}{
		{
			name: "within_venue_only",
			chosen: []UnifiedOpportunity{{
				ID: "t-within",
				Legs: []UnifiedOpportunityLeg{
					{Venue: "binance", Market: "spot", Side: "buy", Notional: 60},
					{Venue: "binance", Market: "perp", Side: "short", Notional: 60},
				},
				CapitalRequiredByVenue: map[string]float64{"binance": 45},
			}},
			balances: VenueWalletBalances{
				"binance": {WalletKindSpot: 10, WalletKindFutures: 100},
			},
			cfg:            DefaultCapitalRouterConfig(),
			wantWithin:     1,
			wantCross:      0,
			wantUnfundable: 0,
			checkWithinFirst: func(t *testing.T, tr WalletTransfer) {
				if tr.Venue != "binance" || tr.From != WalletKindFutures || tr.To != WalletKindSpot || tr.AmountUSDT != 50 {
					t.Fatalf("transfer = %+v", tr)
				}
			},
		},
		{
			name: "cross_venue_not_wallet_transfer",
			chosen: []UnifiedOpportunity{{
				ID: "t-cross",
				Legs: []UnifiedOpportunityLeg{
					{Venue: "gate", Market: "spot", Side: "buy", Notional: 80},
					{Venue: "binance", Market: "perp", Side: "short", Notional: 80},
				},
				CapitalRequiredByVenue: map[string]float64{"gate": 80, "binance": 60},
			}},
			balances: VenueWalletBalances{
				"gate":    {WalletKindSpot: 20},
				"binance": {WalletKindFutures: 140},
			},
			cfg:            DefaultCapitalRouterConfig(),
			wantWithin:     0,
			wantCross:      1,
			wantUnfundable: 0,
			checkCrossFirst: func(t *testing.T, s CrossVenueRebalanceSuggestion) {
				if s.FromVenue != "binance" || s.ToVenue != "gate" {
					t.Fatalf("suggestion = %+v", s)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanCapitalRouting(tt.chosen, tt.balances, tt.cfg)
			if len(plan.WithinVenueTransfers) != tt.wantWithin {
				t.Fatalf("within = %d want %d: %+v", len(plan.WithinVenueTransfers), tt.wantWithin, plan.WithinVenueTransfers)
			}
			if len(plan.CrossVenueSuggestions) != tt.wantCross {
				t.Fatalf("cross = %d want %d: %+v", len(plan.CrossVenueSuggestions), tt.wantCross, plan.CrossVenueSuggestions)
			}
			if len(plan.Unfundable) != tt.wantUnfundable {
				t.Fatalf("unfundable = %d want %d: %#v", len(plan.Unfundable), tt.wantUnfundable, plan.Unfundable)
			}
			if tt.checkWithinFirst != nil && len(plan.WithinVenueTransfers) > 0 {
				tt.checkWithinFirst(t, plan.WithinVenueTransfers[0])
			}
			if tt.checkCrossFirst != nil && len(plan.CrossVenueSuggestions) > 0 {
				tt.checkCrossFirst(t, plan.CrossVenueSuggestions[0])
			}
		})
	}
}
