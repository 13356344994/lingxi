package gate

import (
	"testing"

	"lingxi/internal/venue"
)

func TestSymbolMappingRoundTrip(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		canonical venue.Symbol
		raw       string
	}{
		{"BTC-USDT", "BTC_USDT"},
		{"ETH-USDT", "ETH_USDT"},
		{"HOME-USDT", "HOME_USDT"},
	}
	for _, m := range []venue.Market{venue.MarketSpot, venue.MarketPerp} {
		for _, c := range cases {
			raw, err := a.ToVenue(c.canonical, m)
			if err != nil {
				t.Fatalf("ToVenue(%s,%s) 报错: %v", c.canonical, m, err)
			}
			if raw != c.raw {
				t.Errorf("ToVenue(%s,%s) got %q want %q", c.canonical, m, raw, c.raw)
			}
			back, err := a.ToCanonical(raw, m)
			if err != nil {
				t.Fatalf("ToCanonical(%q) 报错: %v", raw, err)
			}
			if back != c.canonical {
				t.Errorf("往返不稳定 %s -> %q -> %s", c.canonical, raw, back)
			}
		}
	}
}

func TestToCanonicalUnderscore(t *testing.T) {
	a := &Adapter{}
	got, _ := a.ToCanonical("HOME_USDT", venue.MarketPerp)
	if got != "HOME-USDT" {
		t.Errorf("下划线→连字符错误: got %q", got)
	}
}

func TestToVenueSpotAndPerpSameRaw(t *testing.T) {
	a := &Adapter{}
	spot, _ := a.ToVenue("HOME-USDT", venue.MarketSpot)
	perp, _ := a.ToVenue("HOME-USDT", venue.MarketPerp)
	if spot != perp || spot != "HOME_USDT" {
		t.Errorf("现货/永续同形符号: spot=%q perp=%q", spot, perp)
	}
}
