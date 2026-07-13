package validation

import (
	"reflect"
	"testing"
)

func TestFilterEligibleSymbolsUsesOutcomeBlindFingerprintGate(t *testing.T) {
	got := filterEligibleSymbols(
		[]string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "AI-USDT-SWAP"},
		map[string]bool{"BTC-USDT": true, "AI-USDT": true},
	)
	want := []string{"BTC-USDT-SWAP", "AI-USDT-SWAP"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eligible symbols=%v want %v", got, want)
	}
}
