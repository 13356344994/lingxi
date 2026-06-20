package app

import (
	"context"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

func TestEconomicBaseParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sym      string
		wantBase string
		wantMult float64
		wantOK   bool
	}{
		{"1000PEPE-USDT", "PEPE", 1000, true},
		{"10000SATS-USDT", "SATS", 10000, true},
		{"1000000MOG-USDT", "MOG", 1000000, true},
		{"PEPE-USDT", "PEPE", 1, true},
		{"kPEPE-USDT", "PEPE", 1000, true},
		{"KPEPE-USDT", "PEPE", 1000, true},
		{"SHIB1000-USDT", "SHIB", 1000, true},
		{"FLOKI1000-USDT", "FLOKI", 1000, true},
		{"BTC-USDT", "BTC", 1, true},
		{"1000BONK-USDT", "BONK", 1000, true},
		{"", "", 0, false},
		{"WEIRD", "", 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.sym, func(t *testing.T) {
			t.Parallel()
			base, mult, ok := crossVenueEconomicBase(tc.sym)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if base != tc.wantBase || math.Abs(mult-tc.wantMult) > 1e-9 {
				t.Fatalf("got base=%q mult=%v want base=%q mult=%v", base, mult, tc.wantBase, tc.wantMult)
			}
		})
	}
}

func TestCrossDenomPairingGuardRejectsWhenRatioMismatch(t *testing.T) {
	t.Parallel()
	opp := CrossVenueOpportunity{
		Symbol:     "PEPE-USDT",
		HighSymbol: "1000PEPE-USDT",
		LowSymbol:  "PEPE-USDT",
		HighMult:   1000,
		LowMult:    1,
	}
	if crossVenueCrossDenomGuardOK(opp, 1.0, 0.0005) {
		t.Fatal("expected ratio mismatch to reject")
	}
	if !crossVenueCrossDenomGuardOK(opp, 1.0, 0.001) {
		t.Fatal("expected ratio≈1000 to pass guard")
	}
}

func TestCrossDenomPairingPairsPEPE(t *testing.T) {
	t.Parallel()
	entries := []symbolVenueFunding{
		{Venue: binance.VenueName, CanonicalSymbol: "1000PEPE-USDT", EconomicBase: "PEPE", Multiplier: 1000, MultKnown: true, RawAnnualizedAPR: 2.0, RawFundingRate: 0.001, IntervalHours: 8},
		{Venue: okx.VenueName, CanonicalSymbol: "PEPE-USDT", EconomicBase: "PEPE", Multiplier: 1, MultKnown: true, RawAnnualizedAPR: 0.1, RawFundingRate: 0.0001, IntervalHours: 8},
	}
	opp, ok := bestPairSpread("PEPE-USDT", entries)
	if !ok {
		t.Fatal("expected pairing")
	}
	if opp.HighSymbol != "1000PEPE-USDT" || opp.LowSymbol != "PEPE-USDT" {
		t.Fatalf("per-leg symbols=%q/%q", opp.HighSymbol, opp.LowSymbol)
	}
	if opp.HighVenue != binance.VenueName || opp.LowVenue != okx.VenueName {
		t.Fatalf("venues=%s/%s", opp.HighVenue, opp.LowVenue)
	}
}

func TestSameDenomPairingUnchanged(t *testing.T) {
	t.Parallel()
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: {{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 8}},
		bybit.VenueName:   {{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 8}},
	}
	opps := computeCrossVenueOpportunities(byVenue, 0.01, 5)
	if len(opps) != 1 {
		t.Fatalf("opps=%d want 1", len(opps))
	}
	opp := opps[0]
	if opp.Symbol != "BTC-USDT" {
		t.Fatalf("symbol=%q want BTC-USDT", opp.Symbol)
	}
	if opp.HighSymbol != "" && opp.HighSymbol != "BTC-USDT" {
		t.Fatalf("high symbol=%q", opp.HighSymbol)
	}
	if opp.LowSymbol != "" && opp.LowSymbol != "BTC-USDT" {
		t.Fatalf("low symbol=%q", opp.LowSymbol)
	}
	if opp.HighVenue != binance.VenueName || opp.LowVenue != bybit.VenueName {
		t.Fatalf("venues=%s/%s", opp.HighVenue, opp.LowVenue)
	}
	// 同计价 1000PEPE 跨 binance/bybit(配对按原始费率:binance 0.002 > bybit 0.0005 → 可配对)
	entries := []symbolVenueFunding{
		{Venue: binance.VenueName, CanonicalSymbol: "1000PEPE-USDT", EconomicBase: "PEPE", Multiplier: 1000, MultKnown: true, RawAnnualizedAPR: 2.0, RawFundingRate: 0.002},
		{Venue: bybit.VenueName, CanonicalSymbol: "1000PEPE-USDT", EconomicBase: "PEPE", Multiplier: 1000, MultKnown: true, RawAnnualizedAPR: 0.5, RawFundingRate: 0.0005},
	}
	opp2, ok := bestPairSpread("PEPE-USDT", entries)
	if !ok {
		t.Fatal("expected same-denom 1000PEPE pair")
	}
	if opp2.Symbol != "1000PEPE-USDT" {
		t.Fatalf("display symbol=%q want 1000PEPE-USDT", opp2.Symbol)
	}
}

func TestPerLegQtyScaling(t *testing.T) {
	t.Parallel()
	highInstr := venue.Instrument{StepSize: 1}
	lowInstr := venue.Instrument{StepSize: 1}
	caps := venue.Capabilities{}
	aligned := alignCrossVenueQtyPerLeg(100, 1.0, 0.001, 1000, 1, highInstr, lowInstr, caps, caps)
	if aligned.HighQty <= 0 || aligned.LowQty <= 0 {
		t.Fatalf("qty high=%v low=%v", aligned.HighQty, aligned.LowQty)
	}
	econHigh := aligned.HighQty * 1000
	econLow := aligned.LowQty * 1
	if math.Abs(econHigh-econLow)/math.Max(econHigh, econLow) > 0.01 {
		t.Fatalf("economic mismatch high=%v low=%v", econHigh, econLow)
	}
	// 同计价 backward compat
	legacy := alignCrossVenueQty(30, 1, highInstr, lowInstr, caps, caps)
	perLeg := alignCrossVenueQtyPerLeg(30, 1, 1, 1, 1, highInstr, lowInstr, caps, caps).UnifiedQty()
	if legacy != perLeg {
		t.Fatalf("legacy=%v perLeg unified=%v", legacy, perLeg)
	}
}

func TestCrossDenomOpenGateRejectsBadRatio(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	highV := newCrossVenueTestVenue(binance.VenueName, 0.01, 4, now)
	lowV := newCrossVenueTestVenue(okx.VenueName, 0.00001, 4, now)
	highV.instruments = []venue.Instrument{{Symbol: "1000PEPE-USDT", Market: venue.MarketPerp, StepSize: 1}}
	lowV.instruments = []venue.Instrument{{Symbol: "PEPE-USDT", Market: venue.MarketPerp, StepSize: 1}}
	venues := map[string]venue.Venue{binance.VenueName: highV, okx.VenueName: lowV}
	deps := crossVenueGateDeps{
		lookupInstrument: lookupVenueInstrument,
		fetchBookTicker: func(_ context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error) {
			if sym == "1000PEPE-USDT" {
				return venue.BookTicker{BidPrice: 1.0, AskPrice: 1.0}, nil
			}
			return venue.BookTicker{BidPrice: 0.0005, AskPrice: 0.0005}, nil // 偏离 1000x
		},
		fetchFunding: fetchVenueFundingForSymbol,
		fetchPerpAvailableUSDT: func(context.Context, venue.Venue) (float64, error) {
			return 10000, nil
		},
		venueCaps:     func(v venue.Venue) venue.Capabilities { return v.Capabilities() },
		allocatedUSDT: func(string) float64 { return 0 },
	}
	opp := CrossVenueOpportunity{
		Symbol: "PEPE-USDT", HighSymbol: "1000PEPE-USDT", LowSymbol: "PEPE-USDT",
		HighMult: 1000, LowMult: 1,
		HighVenue: binance.VenueName, LowVenue: okx.VenueName, SpreadAPR: 1.0,
	}
	res := evaluateCrossVenueOpenGate(context.Background(), CrossVenueExecutorConfig{MaxPositionUSDT: 50}, deps, venues, crossVenueGateInput{Opp: opp, Now: now})
	if res.OK {
		t.Fatal("expected cross-denom ratio guard to reject")
	}
	_ = gate.VenueName
	_ = bybit.VenueName
}
