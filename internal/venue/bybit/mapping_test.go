package bybit

import (
	"testing"

	"lingxi/internal/venue"
)

func TestSymbolMappingRoundTrip(t *testing.T) {
	a := &Adapter{}
	cases := []venue.Symbol{"BTC-USDT", "ETH-USDT", "EDEN-USDT", "1000PEPE-USDT"}
	for _, m := range []venue.Market{venue.MarketSpot, venue.MarketPerp} {
		for _, c := range cases {
			raw, err := a.ToVenue(c, m)
			if err != nil {
				t.Fatalf("ToVenue(%s) 报错: %v", c, err)
			}
			if raw == "" {
				t.Fatalf("ToVenue(%s) 返回空", c)
			}
			back, err := a.ToCanonical(raw, m)
			if err != nil {
				t.Fatalf("ToCanonical(%q) 报错: %v", raw, err)
			}
			if back != c {
				t.Errorf("往返不稳定 %s -> %q -> %s", c, raw, back)
			}
		}
	}
}

func TestToVenueDropsDash(t *testing.T) {
	a := &Adapter{}
	got, _ := a.ToVenue("BTC-USDT", venue.MarketPerp)
	if got != "BTCUSDT" {
		t.Errorf("ToVenue: got %q want BTCUSDT", got)
	}
}

func TestToCanonicalQuoteSplit(t *testing.T) {
	a := &Adapter{}
	got, _ := a.ToCanonical("BTCUSDC", venue.MarketSpot)
	if got != "BTC-USDC" {
		t.Errorf("USDC 拆分错误: got %q want BTC-USDC", got)
	}
	// USDT 不应被 USD 误拆。
	got2, _ := a.ToCanonical("ETHUSDT", venue.MarketPerp)
	if got2 != "ETH-USDT" {
		t.Errorf("USDT 拆分错误: got %q want ETH-USDT", got2)
	}
}
