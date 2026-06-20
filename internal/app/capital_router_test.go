package app

import (
	"strings"
	"testing"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

func defaultCapitalRouterConfigForTests() CapitalRouterConfig {
	cfg := DefaultCapitalRouterConfig()
	cfg.UnifiedWalletVenues = map[string]bool{
		binance.VenueName: false,
		bybit.VenueName:   true,
		okx.VenueName:     true,
		gate.VenueName:    true,
	}
	return cfg
}

func TestPlanCapitalRoutingWithinVenueRebalance(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "sv-spot-perp-1",
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       "BTC-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: binance.VenueName, Market: "spot", Side: "buy", Notional: 100},
			{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{binance.VenueName: 75},
	}
	balances := VenueWalletBalances{
		binance.VenueName: {
			WalletKindSpot:    20,
			WalletKindFutures: 200,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %d, want 1: %+v", len(plan.WithinVenueTransfers), plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.Venue != binance.VenueName || tr.From != WalletKindFutures || tr.To != WalletKindSpot || tr.AmountUSDT != 80 {
		t.Fatalf("transfer = %+v, want binance futures->spot 80", tr)
	}
	if len(tr.OpportunityIDs) != 1 || tr.OpportunityIDs[0] != "sv-spot-perp-1" {
		t.Fatalf("transfer opportunity IDs = %v, want [sv-spot-perp-1]", tr.OpportunityIDs)
	}
	if len(plan.CrossVenueSuggestions) != 0 {
		t.Fatalf("cross-venue suggestions = %+v, want none", plan.CrossVenueSuggestions)
	}
	if len(plan.Unfundable) != 0 {
		t.Fatalf("unfundable = %#v, want none", plan.Unfundable)
	}
}

func TestPlanCapitalRoutingUnifiedVenueNoWithinVenueTransfer(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "cv-gate-binance",
		StrategyType: UnifiedStrategyCrossVenueSpotPerp,
		Symbol:       "PHA-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: gate.VenueName, Market: "spot", Side: "buy", Notional: 100},
			{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{
			gate.VenueName:    100,
			binance.VenueName: 75,
		},
	}
	balances := VenueWalletBalances{
		gate.VenueName: {
			WalletKindSpot: 200,
		},
		binance.VenueName: {
			WalletKindFutures: 200,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())

	for _, tr := range plan.WithinVenueTransfers {
		if tr.Venue == gate.VenueName {
			t.Fatalf("unified gate should not plan within-venue transfer, got %+v", tr)
		}
	}
	if len(plan.Unfundable) != 0 {
		t.Fatalf("unfundable = %#v, want none (gate unified pool covers spot leg)", plan.Unfundable)
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

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())

	if len(plan.WithinVenueTransfers) != 0 {
		t.Fatalf("within-venue transfers = %+v, want none for unified okx/bybit", plan.WithinVenueTransfers)
	}
	if len(plan.CrossVenueSuggestions) != 1 {
		t.Fatalf("cross-venue suggestions = %d, want 1: %+v", len(plan.CrossVenueSuggestions), plan.CrossVenueSuggestions)
	}
	s := plan.CrossVenueSuggestions[0]
	if s.FromVenue != bybit.VenueName || s.ToVenue != okx.VenueName {
		t.Fatalf("suggestion = %+v, want bybit -> okx", s)
	}
	if s.AmountUSDT < 85-capitalRouterEpsilon || s.AmountUSDT > 85+capitalRouterEpsilon {
		t.Fatalf("suggestion amount = %.4f, want ~85 (okx need 100, have 15 unified)", s.AmountUSDT)
	}
	if len(plan.Unfundable) != 1 {
		t.Fatalf("unfundable = %#v, want one (cross-venue gap not auto-funded)", plan.Unfundable)
	}
	if !strings.Contains(plan.Unfundable[0], "cv-spot-perp-1") {
		t.Fatalf("unfundable entry = %q", plan.Unfundable[0])
	}
	if !strings.Contains(plan.Unfundable[0], CapitalUnfundableReasonCrossVenueGap) {
		t.Fatalf("unfundable entry should include %q for stats: %q", CapitalUnfundableReasonCrossVenueGap, plan.Unfundable[0])
	}
	if !strings.Contains(plan.Unfundable[0], "bybit→okx:") {
		t.Fatalf("unfundable entry should include suggested transfer detail: %q", plan.Unfundable[0])
	}
}

func TestPlanCapitalRoutingRespectsReserve(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:           "sv-spot-perp-reserve",
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       "SOL-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: binance.VenueName, Market: "spot", Side: "buy", Notional: 50},
			{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{binance.VenueName: 75},
	}
	balances := VenueWalletBalances{
		binance.VenueName: {
			WalletKindSpot:    10,
			WalletKindFutures: 120,
		},
	}
	cfg := defaultCapitalRouterConfigForTests()
	cfg.ReserveUSDTByVenue = map[string]float64{binance.VenueName: 30}

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

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())

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
			{Venue: binance.VenueName, Market: "perp", Side: "long", Notional: 100},
			{Venue: binance.VenueName, Market: "spot", Side: "short", Notional: 100},
		},
		CapitalRequiredByVenue: map[string]float64{binance.VenueName: 75},
	}
	balances := VenueWalletBalances{
		binance.VenueName: {
			WalletKindFutures: 200,
			WalletKindMargin:  5,
		},
	}

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, defaultCapitalRouterConfigForTests())

	if len(plan.WithinVenueTransfers) != 1 {
		t.Fatalf("within-venue transfers = %+v, want one", plan.WithinVenueTransfers)
	}
	tr := plan.WithinVenueTransfers[0]
	if tr.From != WalletKindFutures || tr.To != WalletKindMargin || tr.AmountUSDT != 95 {
		t.Fatalf("transfer = %+v, want futures->margin 95", tr)
	}
}

func TestRoutingSkipsTransferWhenTargetWalletSufficient(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	notional := 150.0
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	opp := rotationAlignPerpPerpCapital(UnifiedOpportunity{
		ID:           "pp-funded",
		StrategyType: UnifiedStrategyPerpPerp,
		Symbol:       "H-USDT",
		Legs: []UnifiedOpportunityLeg{
			{Venue: binance.VenueName, Market: "perp", Side: "short", Notional: notional},
			{Venue: bybit.VenueName, Market: "perp", Side: "long", Notional: notional},
		},
		CapitalRequiredByVenue: map[string]float64{
			binance.VenueName: margin,
			bybit.VenueName:   margin,
		},
	}, execCfg)
	balances := VenueWalletBalances{
		binance.VenueName: {WalletKindSpot: 0, WalletKindFutures: 68},
		bybit.VenueName:   {WalletKindSpot: 100},
	}
	cfg := defaultCapitalRouterConfigForTests()
	cfg.PerpLeverage = 3

	plan := PlanCapitalRouting([]UnifiedOpportunity{opp}, balances, cfg)
	for _, tr := range plan.WithinVenueTransfers {
		if tr.Venue == binance.VenueName {
			t.Fatalf("binance futures already funded, should not plan transfer: %+v", tr)
		}
	}
	if len(plan.Unfundable) != 0 {
		t.Fatalf("unfundable=%v want none", plan.Unfundable)
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
			cfg:            defaultCapitalRouterConfigForTests(),
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
			cfg:            defaultCapitalRouterConfigForTests(),
			wantWithin:     0,
			wantCross:      1,
			wantUnfundable: 1,
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
