package app

import (
	"path/filepath"
	"testing"

	"lingxi/internal/lifecycle"
	"lingxi/internal/venue"
)

func TestFilterTargetsByCrossVenueConflict(t *testing.T) {
	dir := t.TempDir()
	cvStore, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = cvStore.upsert(CrossVenuePosition{Symbol: "BTC-USDT", Status: CrossVenueStatusOpen})

	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	engine.cfg.NetworkConfig.RecordDir = dir
	targets := []*MonitoredTarget{
		{Candidate: FundingCandidate{Symbol: "BTCUSDT"}},
		{Candidate: FundingCandidate{Symbol: "ETHUSDT"}},
	}
	filtered := engine.filterTargetsByCrossVenueConflict(targets)
	if len(filtered) != 1 || filtered[0].Candidate.Symbol != "ETHUSDT" {
		t.Fatalf("filtered=%+v", filtered)
	}
}

func TestNormalizeConflictSymbol(t *testing.T) {
	if got := normalizeConflictSymbol(venue.Symbol("BTCUSDT")); got != "BTC-USDT" {
		t.Fatalf("got=%q want BTC-USDT", got)
	}
	if got := normalizeConflictSymbol(venue.Symbol("BTC-USDT")); got != "BTC-USDT" {
		t.Fatalf("got=%q want BTC-USDT", got)
	}
}

func TestIsBinanceSymbolHeldIgnoresTerminal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, liveOpenHedgesStateName)
	if err := writePersistedOpenHedgeState(path, persistedOpenHedgeState{
		Hedges: []persistedOpenHedgeEntry{{Symbol: "BTCUSDT", LifecycleState: lifecycle.StateClosed}},
	}); err != nil {
		t.Fatal(err)
	}
	if isBinanceSymbolHeld(dir, venue.Symbol("BTC-USDT")) {
		t.Fatal("terminal hedge should not block cross venue")
	}
}
