package app

import (
	"time"

	"lingxi/internal/exchange"
)

// BinanceClient 及其全部方法（元数据、账户费率、历史抓取、限频/回退）已迁入
// internal/exchange（Gate 3：唯一摸网络的包）。这里仅保留同名别名与薄包装，
// 让既有的大量调用点无需改动即可继续编译。
type BinanceClient = exchange.BinanceClient

// 现货/合约元数据类型同样位于 internal/exchange，保留别名兼容历史引用。
type (
	SpotExchangeInfoResponse    = exchange.SpotExchangeInfoResponse
	FuturesExchangeInfoResponse = exchange.FuturesExchangeInfoResponse
	SpotSymbol                  = exchange.SpotSymbol
	FuturesSymbol               = exchange.FuturesSymbol
	ExchangeFilter              = exchange.ExchangeFilter
)

// NewBinanceClient 创建一个可复用的币安 HTTP 客户端。
func NewBinanceClient(timeout time.Duration) *BinanceClient {
	return exchange.NewBinanceClient(timeout)
}

func NewBinanceClientWithNetworkConfig(networkCfg NetworkConfig) (*BinanceClient, error) {
	return exchange.NewBinanceClientWithNetworkConfig(networkCfg)
}

// NewAuthenticatedBinanceClient 创建带认证和代理能力的客户端。
func NewAuthenticatedBinanceClient(secret SecretConfig, networkCfg NetworkConfig) (*BinanceClient, error) {
	return exchange.NewAuthenticatedBinanceClient(secret, networkCfg)
}

// 以下为 internal/exchange 中现货/合约筛选纯函数的薄委托包装，保持既有调用点零改动。
func preferredSpotQuoteAssets(raw string) []string {
	return exchange.PreferredSpotQuoteAssets(raw)
}

func futuresBaseAsset(symbol FuturesSymbol) string {
	return exchange.FuturesBaseAsset(symbol)
}

func buildGoldenSymbolSpotPairs(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol, preferredQuotes []string, now time.Time) map[string]string {
	return exchange.BuildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, preferredQuotes, now)
}
