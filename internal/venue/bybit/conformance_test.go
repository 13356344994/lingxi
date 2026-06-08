package bybit

import (
	"testing"

	"lingxi/internal/venue"
)

// TestBybitAdapterConformance 让 Bybit 适配器过离线契约一致性套件(纯函数/不触网)。
// 符号映射为纯字符串(不依赖在线 instruments),故 SkipSymbolMapping=false;
// CheckOrderBudget=false(Stage 1 ReserveOrderBudget 恒放行,无内核可解引用,设 false 即可)。
func TestBybitAdapterConformance(t *testing.T) {
	a, err := New(venue.Config{Name: VenueName})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	venue.RunConformance(t, a, venue.ConformanceConfig{
		SampleSymbols:     []venue.Symbol{"BTC-USDT", "ETH-USDT", "EDEN-USDT"},
		SkipSymbolMapping: false,
		CheckOrderBudget:  false,
	})
}
