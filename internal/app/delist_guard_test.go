package app

import (
	"context"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

type delistTestVenue struct {
	venue.Venue
	instrs []venue.Instrument
}

func (d *delistTestVenue) Name() string { return "test" }

func (d *delistTestVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return d.instrs, nil
}

func TestDelistGuard_OpenGateRejectsDelistingSymbol(t *testing.T) {
	v := &delistTestVenue{instrs: []venue.Instrument{
		{Symbol: "DELIST-USDT", Market: venue.MarketPerp, NonTrading: true, RawTradingStatus: "SETTLING"},
		{Symbol: "DELIST-USDT", Market: venue.MarketSpot, NonTrading: false, RawTradingStatus: "TRADING"},
		{Symbol: "OK-USDT", Market: venue.MarketPerp, NonTrading: false, RawTradingStatus: "TRADING"},
		{Symbol: "OK-USDT", Market: venue.MarketSpot, NonTrading: false, RawTradingStatus: "TRADING"},
	}}
	guard := NewDelistGuard(map[string]venue.Venue{"binance": v}, DefaultDelistGuardConfig(), log.New(io.Discard, "", 0))
	guard.Refresh(context.Background())

	positive := UnifiedOpportunity{
		ID:                "svsp:DELIST-USDT:binance+binance:snap",
		StrategyType:      UnifiedStrategySingleVenueSpotPerp,
		Symbol:            "DELIST-USDT",
		ExpectedNetEVUSDT: 10,
		Legs: []UnifiedOpportunityLeg{
			{Venue: "binance", Market: "spot", Side: "buy", Notional: 100},
			{Venue: "binance", Market: "perp", Side: "sell", Notional: 100},
		},
	}
	got := applyDelistOpenGateToCandidates(guard, []UnifiedOpportunity{positive})
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	if got[0].ExpectedNetEVUSDT != 0 {
		t.Fatalf("expected net EV zeroed, got %.2f", got[0].ExpectedNetEVUSDT)
	}
	if !containsReject(got[0].RejectReasons, rotationRejectDelisting) {
		t.Fatalf("reject_reasons = %#v, want %q", got[0].RejectReasons, rotationRejectDelisting)
	}
}

func TestDelistGuard_UnknownStatusDoesNotBlock(t *testing.T) {
	v := &delistTestVenue{instrs: []venue.Instrument{
		{Symbol: "MYSTERY-USDT", Market: venue.MarketPerp, NonTrading: false, RawTradingStatus: "TRADING"},
	}}
	guard := NewDelistGuard(map[string]venue.Venue{"binance": v}, DefaultDelistGuardConfig(), log.New(io.Discard, "", 0))
	guard.Refresh(context.Background())

	positive := UnifiedOpportunity{
		ID:                "svsp:UNKNOWN-USDT:binance+binance:snap",
		StrategyType:      UnifiedStrategySingleVenueSpotPerp,
		Symbol:            "UNKNOWN-USDT",
		ExpectedNetEVUSDT: 10,
		Legs: []UnifiedOpportunityLeg{
			{Venue: "binance", Market: "spot", Side: "buy", Notional: 100},
			{Venue: "binance", Market: "perp", Side: "sell", Notional: 100},
		},
	}
	got := applyDelistOpenGateToCandidates(guard, []UnifiedOpportunity{positive})
	if got[0].ExpectedNetEVUSDT != 10 {
		t.Fatalf("unknown symbol must fail-open, got net EV %.2f", got[0].ExpectedNetEVUSDT)
	}
	if len(got[0].RejectReasons) > 0 {
		t.Fatalf("unexpected reject reasons: %#v", got[0].RejectReasons)
	}
}

func TestDelistGuard_HeldPositionAlwaysVerifiedClose(t *testing.T) {
	v := &delistTestVenue{instrs: []venue.Instrument{
		{Symbol: "DELIST-USDT", Market: venue.MarketPerp, NonTrading: true, RawTradingStatus: "in_delisting"},
		{Symbol: "DELIST-USDT", Market: venue.MarketSpot, NonTrading: false, RawTradingStatus: "tradable"},
	}}
	guard := NewDelistGuard(map[string]venue.Venue{"binance": v}, DefaultDelistGuardConfig(), log.New(io.Discard, "", 0))
	guard.Refresh(context.Background())

	var closeCalls int32
	binding := &rotationExecutorBinding{
		strategyType: UnifiedStrategySingleVenueSpotPerp,
		heldFn: func() []rotationHeldRecord {
			return []rotationHeldRecord{{
				HeldPosition: HeldPosition{
					OpportunityID: "opp-delist",
					StrategyType:  UnifiedStrategySingleVenueSpotPerp,
					Symbol:        "DELIST-USDT",
				},
				delistLegs: []rotationDelistLeg{
					{Venue: "binance", Market: venue.MarketSpot},
					{Venue: "binance", Market: venue.MarketPerp},
				},
			}}
		},
		closeVerifiedFn: func(context.Context, string, string, string) error {
			atomic.AddInt32(&closeCalls, 1)
			return nil
		},
	}

	guard.manageBindingDelistCloses(context.Background(), binding, false)
	if atomic.LoadInt32(&closeCalls) != 1 {
		t.Fatalf("delist close calls = %d, want 1 even when runner inactive", closeCalls)
	}

	guard.manageBindingDelistCloses(context.Background(), binding, true)
	if atomic.LoadInt32(&closeCalls) != 2 {
		t.Fatalf("delist close calls = %d, want 2", closeCalls)
	}
}

func TestExchangeExplicitNonTradingStatuses(t *testing.T) {
	if !exchange.ExplicitNonTradingFuturesStatus("SETTLING", "") {
		t.Fatal("SETTLING should be non-trading")
	}
	if exchange.ExplicitNonTradingFuturesStatus("TRADING", "TRADING") {
		t.Fatal("TRADING/TRADING should be tradable")
	}
	if !exchange.ExplicitNonTradingFuturesStatus("TRADING", "PRE_DELIVERING") {
		t.Fatal("PRE_DELIVERING contractStatus should be non-trading")
	}
	if !exchange.ExplicitNonTradingOKXState("suspend") {
		t.Fatal("okx suspend should be non-trading")
	}
	if exchange.ExplicitNonTradingOKXState("live") {
		t.Fatal("okx live should be tradable")
	}
}

func TestDelistGuardCacheTTL(t *testing.T) {
	v := &delistTestVenue{instrs: []venue.Instrument{
		{Symbol: "A-USDT", Market: venue.MarketPerp, NonTrading: false},
	}}
	guard := NewDelistGuard(map[string]venue.Venue{"binance": v}, DelistGuardConfig{
		Enabled:  true,
		CacheTTL: time.Millisecond,
	}, log.New(io.Discard, "", 0))
	now := time.Now()
	guard.nowFn = func() time.Time { return now }
	guard.Refresh(context.Background())
	if !guard.venueFresh("binance", now) {
		t.Fatal("cache should be fresh immediately after refresh")
	}
	now = now.Add(2 * time.Millisecond)
	if guard.venueFresh("binance", now) {
		t.Fatal("cache should expire after TTL")
	}
}
