package okx

import (
	"testing"

	"lingxi/internal/venue"
)

// TestOKXAdapterConformance 让 OKX 适配器过离线契约一致性套件(纯函数/不触网)。
func TestOKXAdapterConformance(t *testing.T) {
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
