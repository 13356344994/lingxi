package historyevents

import (
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/hierarchicalrouter"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/backward"
)

func TestFundingPostSettlementEventsAreDecisionTimeSafeAndCosted(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var prices []priceBar
	for i := 0; i < 40; i++ {
		prices = append(prices, priceBar{
			Time: base.Add(time.Duration(i) * time.Hour),
			Open: 100 + float64(i), Close: 100.5 + float64(i), QuoteVolume: 1_000_000,
		})
	}
	funding := []fundingPoint{
		{Time: base.Add(25 * time.Hour), Rate: -0.002},
		{Time: base.Add(26 * time.Hour), Rate: 0.002},
	}
	events := buildEvents("BTC-USDT", prices, funding)
	if len(events) != 2 {
		t.Fatalf("events=%d", len(events))
	}
	if !events[0].FeatureAsOf.Before(events[0].DecisionTime) {
		t.Fatal("feature_as_of must precede decision_time")
	}
	if events[0].Direction != mechanism.DirectionShort || events[1].Direction != mechanism.DirectionLong {
		t.Fatalf("direction rule drifted: %s %s", events[0].Direction, events[1].Direction)
	}
	rawShort := -(prices[29].Open/prices[25].Open - 1) * 10000
	if got, want := events[0].NetOutcomeBPS, rawShort-hierarchicalrouter.BackwardDiscoveryCostBPS; got != want {
		t.Fatalf("net outcome=%v want=%v", got, want)
	}
}

func TestEventManifestSignatureRejectsForgery(t *testing.T) {
	catalog := historydata.HistoryCatalog{
		DatasetID: "history.test", DataHash: hierarchicalrouter.HistoryDataHash, SchemaHash: "schema",
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{{Path: "okx_funding/BTC-USDT-SWAP.jsonl", FileHash: "fund", Size: 1}}},
			{Name: "okx_1h", Files: []historydata.FileStats{{Path: "okx_1h/BTC-USDT-SWAP.jsonl", FileHash: "price", Size: 1}}},
		},
	}
	event := backward.Event{
		ID: "e", Symbol: "BTC-USDT", Cluster: "BTC-USDT", Stratum: "s",
		DecisionTime: time.Now(), FeatureAsOf: time.Now().Add(-time.Second),
		Payer: mechanism.PayerFundingReceivers, Direction: mechanism.DirectionLong,
		Volatility: 1, Liquidity: 1, Features: map[string]float64{"funding_rate": 0.001},
		NetOutcomeBPS: 1,
	}
	manifest, err := NewManifest(
		[]backward.Event{event}, catalog,
		hierarchicalrouter.BackwardSmokeMaxSymbols,
		hierarchicalrouter.BackwardSmokeMaxFundingLines,
		hierarchicalrouter.BackwardSmokeMaxPriceLines,
	)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	manifest, err = SignManifest(manifest, key, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifestSignature(manifest, key); err != nil {
		t.Fatal(err)
	}
	manifest.EventCount++
	if err := VerifyManifestSignature(manifest, key); err == nil {
		t.Fatal("tampered signed manifest must fail")
	}
}
