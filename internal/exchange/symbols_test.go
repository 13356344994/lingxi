package exchange

import "testing"

func TestExplicitNonTradingSpotStatus(t *testing.T) {
	if ExplicitNonTradingSpotStatus("TRADING") {
		t.Fatal("TRADING should be tradable")
	}
	if !ExplicitNonTradingSpotStatus("BREAK") {
		t.Fatal("BREAK should be non-trading")
	}
	if ExplicitNonTradingSpotStatus("") {
		t.Fatal("empty status should fail-open")
	}
}

func TestExplicitNonTradingBybitStatus(t *testing.T) {
	if ExplicitNonTradingBybitStatus("Trading") {
		t.Fatal("Trading should be tradable")
	}
	if !ExplicitNonTradingBybitStatus("Delivering") {
		t.Fatal("Delivering should be non-trading")
	}
}

func TestExplicitNonTradingGateSpotStatus(t *testing.T) {
	if ExplicitNonTradingGateSpotStatus("tradable") {
		t.Fatal("tradable should be tradable")
	}
	if !ExplicitNonTradingGateSpotStatus("untradable") {
		t.Fatal("untradable should be non-trading")
	}
}
