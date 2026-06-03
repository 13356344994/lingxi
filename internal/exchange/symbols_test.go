package exchange

import (
	"testing"
	"time"
)

func TestIsValidExchangeSymbol(t *testing.T) {
	cases := map[string]bool{
		"BTCUSDT":      true,
		"1000PEPEUSDT": true,
		"":             false,
		"  ":           false,
		"btcusdt":      false,
		"BTC-USDT":     false,
		"BTC/USDT":     false,
	}
	for symbol, want := range cases {
		if got := IsValidExchangeSymbol(symbol); got != want {
			t.Errorf("IsValidExchangeSymbol(%q)=%v want %v", symbol, got, want)
		}
	}
}

func TestFuturesBaseAsset(t *testing.T) {
	cases := []struct {
		name   string
		symbol FuturesSymbol
		want   string
	}{
		{"strip margin asset", FuturesSymbol{Pair: "BTCUSDT", MarginAsset: "USDT"}, "BTC"},
		{"fallback to symbol", FuturesSymbol{Symbol: "ETHUSDT"}, "ETH"},
		{"quote suffix fallback", FuturesSymbol{Pair: "SOLFDUSD"}, "SOL"},
	}
	for _, tc := range cases {
		if got := FuturesBaseAsset(tc.symbol); got != tc.want {
			t.Errorf("%s: FuturesBaseAsset=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestFilterUSDTPERPTradingFutures(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	symbols := []FuturesSymbol{
		{Symbol: "BTCUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT"},
		{Symbol: "ETHUSDT", Status: "BREAK", ContractType: "PERPETUAL", MarginAsset: "USDT"},
		{Symbol: "BNBUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT", DeliveryDate: now.AddDate(0, 1, 0).UnixMilli()},
		{Symbol: "ADAUSDC", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDC"},
		{Symbol: "SOLUSDT", Status: "TRADING", ContractType: "CURRENT_QUARTER", MarginAsset: "USDT"},
	}
	got := FilterUSDTPERPTradingFutures(symbols, now)
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" {
		t.Fatalf("expected only BTCUSDT, got %+v", got)
	}
}

func TestBuildPreferredSpotSymbolsByBasePrefersHigherRankQuote(t *testing.T) {
	spots := []SpotSymbol{
		{Symbol: "BTCUSDT", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "USDT"},
		{Symbol: "BTCFDUSD", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "FDUSD"},
	}
	byBase := BuildPreferredSpotSymbolsByBase(spots, []string{"FDUSD", "USDT"})
	if byBase["BTC"].Symbol != "BTCFDUSD" {
		t.Fatalf("expected FDUSD to win, got %q", byBase["BTC"].Symbol)
	}
}

func TestBuildGoldenSymbolSpotPairs(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	spots := []SpotSymbol{{Symbol: "BTCUSDT", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "USDT"}}
	futures := []FuturesSymbol{{Symbol: "BTCUSDT", Pair: "BTCUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT"}}
	pairs := BuildGoldenSymbolSpotPairs(spots, futures, []string{"USDT"}, now)
	if pairs["BTCUSDT"] != "BTCUSDT" {
		t.Fatalf("expected BTCUSDT->BTCUSDT, got %+v", pairs)
	}
}

func TestPreferredSpotQuoteAssetsDefaults(t *testing.T) {
	got := PreferredSpotQuoteAssets("  ")
	want := []string{"FDUSD", "USDC", "USDT"}
	if len(got) != len(want) {
		t.Fatalf("default quotes mismatch: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default quotes mismatch at %d: %+v", i, got)
		}
	}
	dedup := PreferredSpotQuoteAssets("usdt, USDT ,fdusd")
	if len(dedup) != 2 || dedup[0] != "USDT" || dedup[1] != "FDUSD" {
		t.Fatalf("expected deduped [USDT FDUSD], got %+v", dedup)
	}
}
