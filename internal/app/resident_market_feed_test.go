package app

import (
	"io"
	"log"
	"testing"
)

func TestResidentFeedSubscriptionKey(t *testing.T) {
	if got := residentFeedSubscriptionKey(nil, nil); got != "" {
		t.Fatalf("empty sets should yield empty key, got %q", got)
	}
	// 顺序无关：同一集合不同顺序应得到相同 key。
	a := residentFeedSubscriptionKey([]string{"ETHUSDT", "BTCUSDT"}, []string{"SOLUSDT", "BTCUSDT"})
	b := residentFeedSubscriptionKey([]string{"BTCUSDT", "ETHUSDT"}, []string{"BTCUSDT", "SOLUSDT"})
	if a != b {
		t.Fatalf("key should be order-independent: %q vs %q", a, b)
	}
	// 集合不同则 key 不同。
	c := residentFeedSubscriptionKey([]string{"BTCUSDT"}, []string{"BTCUSDT"})
	if a == c {
		t.Fatalf("different sets should yield different keys, both = %q", a)
	}
}

func TestBookTickerMid(t *testing.T) {
	cases := []struct {
		name string
		tk   BookTicker
		want float64
	}{
		{"both_sides", BookTicker{BidPrice: "100", AskPrice: "102"}, 101},
		{"bid_only", BookTicker{BidPrice: "100"}, 100},
		{"ask_only", BookTicker{AskPrice: "102"}, 102},
		{"none", BookTicker{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bookTickerMid(tc.tk); got != tc.want {
				t.Fatalf("bookTickerMid(%+v) = %v, want %v", tc.tk, got, tc.want)
			}
		})
	}
}

func TestHeldMarketSubscriptionSymbols(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	engine.activeOpenHedges = []OpenHedge{
		{Target: FundingCandidate{Symbol: "ETHUSDT", Pair: "ETHUSDT", SpotBaseAsset: "ETH"}},
		{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}},
	}

	spot, futures := engine.heldMarketSubscriptionSymbols()
	if len(spot) != 2 || spot[0] != "BTCUSDT" || spot[1] != "ETHUSDT" {
		t.Fatalf("expected sorted deduped spot pairs, got %v", spot)
	}
	if len(futures) != 2 || futures[0] != "BTCUSDT" || futures[1] != "ETHUSDT" {
		t.Fatalf("expected sorted deduped futures symbols, got %v", futures)
	}
}

func TestHeldMarketSubscriptionSymbolsEmptyWhenFlat(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	spot, futures := engine.heldMarketSubscriptionSymbols()
	if len(spot) != 0 || len(futures) != 0 {
		t.Fatalf("flat exposure should yield no subscriptions, got spot=%v futures=%v", spot, futures)
	}
}

func TestHeldMidReadsResidentCache(t *testing.T) {
	engine := &FundingRateSniperEngine{
		logger:                 log.New(io.Discard, "", 0),
		holdSpotTickerCache:    NewBookTickerCache(),
		holdFuturesTickerCache: NewBookTickerCache(),
	}

	if _, ok := engine.heldFuturesMid("BTCUSDT"); ok {
		t.Fatal("empty cache should report no price")
	}

	engine.holdFuturesTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "60000", AskPrice: "60010"})
	mid, ok := engine.heldFuturesMid("btcusdt")
	if !ok || mid != 60005 {
		t.Fatalf("expected mid 60005 from resident cache, got %v ok=%v", mid, ok)
	}
}
