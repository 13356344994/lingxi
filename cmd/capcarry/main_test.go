package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCarryStateRoundTrip(t *testing.T) {
	old := statePath
	statePath = filepath.Join(t.TempDir(), "state.json")
	t.Cleanup(func() { statePath = old })

	want := carryState{
		Status: "active", Symbol: "BTC-USDT", Direction: "pos",
		PerpQty: 0.01, SpotQty: 0.01, LegUSDT: 5, EquityUSDT: 100,
		EntryFunding: 0.01, EntryBasisBps: 12, OpenedAt: time.Now().UTC(),
	}
	if err := saveState(want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadState()
	if !ok || got.Version != 1 || got.Symbol != want.Symbol || got.PerpQty != want.PerpQty || got.SpotQty != want.SpotQty {
		t.Fatalf("state round-trip: %+v ok=%v", got, ok)
	}
	if err := clearState(); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadState(); ok {
		t.Fatal("clear 后仍读到状态")
	}
}
