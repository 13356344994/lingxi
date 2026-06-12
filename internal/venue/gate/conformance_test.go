package gate

import (
	"testing"

	"lingxi/internal/venue"
)

func TestGateAdapterConformance(t *testing.T) {
	a, err := New(venue.Config{Name: VenueName})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	venue.RunConformance(t, a, venue.ConformanceConfig{
		SampleSymbols:     []venue.Symbol{"BTC-USDT", "ETH-USDT", "HOME-USDT"},
		SkipSymbolMapping: false,
		CheckOrderBudget:  false,
	})
}
