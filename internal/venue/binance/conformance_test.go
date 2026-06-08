package binance

import (
	"testing"

	"lingxi/internal/venue"
)

// TestBinanceAdapterConformance 让币安适配器过 venue 离线契约一致性套件(P4 前置)。
// 用 nil 内核构造:套件只跑离线/纯函数检查(能力自洽、符号映射往返、调仓分配不变式、错误分类、
// 不支持能力的 ErrUnsupported 契约),不触碰 client,故安全。接 bybit/okx/gate 时复用同一套件即可。
func TestBinanceAdapterConformance(t *testing.T) {
	adapter := New(nil)
	venue.RunConformance(t, adapter, venue.ConformanceConfig{
		SampleSymbols: []venue.Symbol{"BTC-USDT", "ETH-USDT", "EDEN-USDT"},
		// 币安符号映射是纯字符串规则(BTC-USDT ↔ BTCUSDT),无需在线 instruments。
		SkipSymbolMapping: false,
		// nil 内核:不跑会解引用 client 的 used-weight 预算检查。
		CheckOrderBudget: false,
	})
}
