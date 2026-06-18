package binance

import (
	"testing"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

func newTestAdapter() *Adapter {
	return New(exchange.NewClient(exchange.ClientConfig{}))
}

func TestSymbolMappingRoundTrip(t *testing.T) {
	a := newTestAdapter()
	cases := []struct {
		canonical venue.Symbol
		raw       string
	}{
		{"BTC-USDT", "BTCUSDT"},
		{"ETH-FDUSD", "ETHFDUSD"},
		{"SOL-USDC", "SOLUSDC"},
	}
	for _, c := range cases {
		got, err := a.ToVenue(c.canonical, venue.MarketPerp)
		if err != nil || got != c.raw {
			t.Fatalf("ToVenue(%s) = %q, %v; want %q", c.canonical, got, err, c.raw)
		}
		back, err := a.ToCanonical(c.raw, venue.MarketPerp)
		if err != nil || back != c.canonical {
			t.Fatalf("ToCanonical(%s) = %q, %v; want %s", c.raw, back, err, c.canonical)
		}
	}
}

func TestToCanonicalUnknownQuoteFallback(t *testing.T) {
	a := newTestAdapter()
	got, err := a.ToCanonical("WEIRDPAIR", venue.MarketSpot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "WEIRDPAIR" {
		t.Fatalf("expected raw passthrough, got %q", got)
	}
}

func TestParseFilters(t *testing.T) {
	tick, step, minNotional := parseFilters([]exchange.ExchangeFilter{
		{FilterType: "PRICE_FILTER", TickSize: "0.10"},
		{FilterType: "LOT_SIZE", StepSize: "0.001"},
		{FilterType: "NOTIONAL", Notional: "5"},
	})
	if tick != 0.10 || step != 0.001 || minNotional != 5 {
		t.Fatalf("parseFilters = %v %v %v", tick, step, minNotional)
	}
}

func TestCapabilities(t *testing.T) {
	caps := newTestAdapter().Capabilities()
	if !caps.SupportsNegativeCarry {
		t.Fatal("binance venue adapter should claim negative carry after BorrowRepay is implemented")
	}
	if caps.ContractBasedPerp {
		t.Fatal("binance USDT perp is coin-quantity, not contract-based")
	}
	if len(caps.CapitalWallets) != 4 {
		t.Fatalf("expected 4 binance wallets, got %d", len(caps.CapitalWallets))
	}
	if caps.UnifiedWallet {
		t.Fatal("binance should use separate wallets (UnifiedWallet=false)")
	}
}

func TestPendingMethodsReturnUnsupported(t *testing.T) {
	a := newTestAdapter()
	if _, _, err := a.CaptureMovableSnapshot(t.Context()); err != venue.ErrUnsupported {
		t.Fatalf("CaptureMovableSnapshot should be ErrUnsupported, got %v", err)
	}
	if _, err := a.PlaceBatch(t.Context(), nil); err != venue.ErrUnsupported {
		t.Fatalf("PlaceBatch should be ErrUnsupported, got %v", err)
	}
}

func TestBorrowRepayZeroAmountNoOp(t *testing.T) {
	a := newTestAdapter()
	if err := a.BorrowRepay(t.Context(), "USDT", 0, venue.BorrowDirBorrow); err != nil {
		t.Fatalf("BorrowRepay(amt=0) should be nil, got %v", err)
	}
}
