package okx

import (
	"testing"

	"lingxi/internal/venue"
)

func TestSymbolMappingRoundTrip(t *testing.T) {
	a := &Adapter{}
	cases := []venue.Symbol{"BTC-USDT", "ETH-USDT", "HOME-USDT", "1000PEPE-USDT"}
	for _, m := range []venue.Market{venue.MarketSpot, venue.MarketPerp} {
		for _, c := range cases {
			raw, err := a.ToVenue(c, m)
			if err != nil {
				t.Fatalf("ToVenue(%s,%s) 报错: %v", c, m, err)
			}
			if raw == "" {
				t.Fatalf("ToVenue(%s) 返回空", c)
			}
			if m == venue.MarketPerp && raw != string(c)+"-SWAP" {
				t.Errorf("永续 ToVenue: got %q want %s-SWAP", raw, c)
			}
			if m == venue.MarketSpot && raw != string(c) {
				t.Errorf("现货 ToVenue: got %q want %s", raw, c)
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

func TestToCanonicalSwapSuffix(t *testing.T) {
	a := &Adapter{}
	got, _ := a.ToCanonical("HOME-USDT-SWAP", venue.MarketPerp)
	if got != "HOME-USDT" {
		t.Errorf("SWAP 去后缀错误: got %q", got)
	}
}

func TestToVenueSpotKeepsDash(t *testing.T) {
	a := &Adapter{}
	got, _ := a.ToVenue("BTC-USDT", venue.MarketSpot)
	if got != "BTC-USDT" {
		t.Errorf("现货应保留连字符: got %q", got)
	}
}
