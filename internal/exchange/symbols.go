package exchange

import (
	"strings"
	"time"
)

// SpotExchangeInfoResponse 对应币安现货 exchangeInfo 的精简响应结构。
type SpotExchangeInfoResponse struct {
	Symbols []SpotSymbol `json:"symbols"`
}

// FuturesExchangeInfoResponse 对应币安 U 本位合约 exchangeInfo 的精简响应结构。
type FuturesExchangeInfoResponse struct {
	Symbols []FuturesSymbol `json:"symbols"`
}

// SpotSymbol 表示一个现货交易对的基础信息。
type SpotSymbol struct {
	Symbol             string           `json:"symbol"`
	Status             string           `json:"status"`
	BaseAsset          string           `json:"baseAsset"`
	QuoteAsset         string           `json:"quoteAsset"`
	BaseAssetPrecision int              `json:"baseAssetPrecision"`
	Filters            []ExchangeFilter `json:"filters"`
}

// FuturesSymbol 表示一个 U 本位永续/交割合约的基础信息。
type FuturesSymbol struct {
	Symbol            string           `json:"symbol"`
	Pair              string           `json:"pair"`
	Status            string           `json:"status"`
	ContractType      string           `json:"contractType"`
	DeliveryDate      int64            `json:"deliveryDate"`
	MarginAsset       string           `json:"marginAsset"`
	QuantityPrecision int              `json:"quantityPrecision"`
	Filters           []ExchangeFilter `json:"filters"`
}

// FundingRateHistoryItem 表示一条历史资金费率记录（含回测内部使用的派生字段）。
type FundingRateHistoryItem struct {
	Symbol               string `json:"symbol"`
	FundingRate          string `json:"fundingRate"`
	FundingTime          int64  `json:"fundingTime"`
	MarkPrice            string `json:"markPrice"`
	EstimatedFundingRate string `json:"-"`
	ScreeningScore       string `json:"-"`
}

// MarketKline 表示币安 K 线的一根蜡烛。
type MarketKline struct {
	OpenTime         int64
	Open             float64
	High             float64
	Low              float64
	Close            float64
	QuoteAssetVolume float64
}

// PremiumIndexItem 对应币安 U 本位合约 premiumIndex 接口的精简响应。
type PremiumIndexItem struct {
	Symbol               string `json:"symbol"`
	MarkPrice            string `json:"markPrice"`
	IndexPrice           string `json:"indexPrice"`
	EstimatedSettlePrice string `json:"estimatedSettlePrice"`
	LastFundingRate      string `json:"lastFundingRate"`
	InterestRate         string `json:"interestRate"`
	NextFundingTime      int64  `json:"nextFundingTime"`
	Time                 int64  `json:"time"`
}

// FundingInfoItem 是 GET /fapi/v1/fundingInfo 的单条回执:仅列出「资金费上下限/结算周期偏离默认值」
// 的 symbol。adjustedFundingRateCap / adjustedFundingRateFloor 即该 symbol 单次结算资金费的封顶/封底
// (如 0.02 / -0.02),撞限豁免据此判定某币实收费率是否已贴住交易所硬限。
type FundingInfoItem struct {
	Symbol                   string `json:"symbol"`
	AdjustedFundingRateCap   string `json:"adjustedFundingRateCap"`
	AdjustedFundingRateFloor string `json:"adjustedFundingRateFloor"`
	FundingIntervalHours     int    `json:"fundingIntervalHours"`
}

// ExchangeFilter 表示交易所返回的交易限制规则。
type ExchangeFilter struct {
	FilterType        string `json:"filterType"`
	MinQty            string `json:"minQty,omitempty"`
	MaxQty            string `json:"maxQty,omitempty"`
	StepSize          string `json:"stepSize,omitempty"`
	TickSize          string `json:"tickSize,omitempty"`
	MinNotional       string `json:"minNotional,omitempty"`
	Notional          string `json:"notional,omitempty"`
	MultiplierUp      string `json:"multiplierUp,omitempty"`
	MultiplierDown    string `json:"multiplierDown,omitempty"`
	BidMultiplierUp   string `json:"bidMultiplierUp,omitempty"`
	BidMultiplierDown string `json:"bidMultiplierDown,omitempty"`
	AskMultiplierUp   string `json:"askMultiplierUp,omitempty"`
	AskMultiplierDown string `json:"askMultiplierDown,omitempty"`
	AvgPriceMins      int    `json:"avgPriceMins,omitempty"`
}

// PreferredSpotQuoteAssets 解析优先计价币种列表（逗号分隔、去重、大写）。为空时返回默认顺序。
func PreferredSpotQuoteAssets(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		asset := strings.ToUpper(strings.TrimSpace(part))
		if asset == "" {
			continue
		}
		if _, duplicated := seen[asset]; duplicated {
			continue
		}
		seen[asset] = struct{}{}
		result = append(result, asset)
	}
	if len(result) == 0 {
		return []string{"FDUSD", "USDC", "USDT"}
	}
	return result
}

// BuildPreferredSpotSymbolsByBase 按优先计价币种为每个 base 资产选出唯一现货交易对。
// 同一 base 下优先级更高的计价币胜出；同级时按 symbol 字典序取较小者，保证确定性。
func BuildPreferredSpotSymbolsByBase(symbols []SpotSymbol, preferredQuotes []string) map[string]SpotSymbol {
	quoteRank := make(map[string]int, len(preferredQuotes))
	for idx, asset := range preferredQuotes {
		quoteRank[strings.ToUpper(strings.TrimSpace(asset))] = idx
	}

	result := make(map[string]SpotSymbol, len(symbols))
	bestRank := make(map[string]int, len(symbols))
	for _, symbol := range symbols {
		if !IsValidExchangeSymbol(symbol.Symbol) || symbol.Status != "TRADING" {
			continue
		}
		rank, ok := quoteRank[strings.ToUpper(strings.TrimSpace(symbol.QuoteAsset))]
		if !ok {
			continue
		}
		baseAsset := strings.ToUpper(strings.TrimSpace(symbol.BaseAsset))
		if baseAsset == "" {
			continue
		}
		current, exists := result[baseAsset]
		if !exists || rank < bestRank[baseAsset] || (rank == bestRank[baseAsset] && symbol.Symbol < current.Symbol) {
			result[baseAsset] = symbol
			bestRank[baseAsset] = rank
		}
	}
	return result
}

// FuturesBaseAsset 从合约 symbol/pair 推断 base 资产，优先剥离保证金币种，再尝试常见计价币种。
func FuturesBaseAsset(symbol FuturesSymbol) string {
	pair := strings.ToUpper(strings.TrimSpace(symbol.Pair))
	marginAsset := strings.ToUpper(strings.TrimSpace(symbol.MarginAsset))
	if pair == "" {
		pair = strings.ToUpper(strings.TrimSpace(symbol.Symbol))
	}
	if marginAsset != "" && strings.HasSuffix(pair, marginAsset) {
		baseAsset := strings.TrimSuffix(pair, marginAsset)
		if baseAsset != "" {
			return baseAsset
		}
	}
	for _, quoteAsset := range []string{"USDT", "FDUSD", "USDC"} {
		if strings.HasSuffix(pair, quoteAsset) {
			baseAsset := strings.TrimSuffix(pair, quoteAsset)
			if baseAsset != "" {
				return baseAsset
			}
		}
	}
	return pair
}

// BuildGoldenSymbolSpotPairs 为每个可交易的 U 本位永续合约匹配一个现货交易对，返回 合约->现货 映射。
func BuildGoldenSymbolSpotPairs(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol, preferredQuotes []string, now time.Time) map[string]string {
	spotByBase := BuildPreferredSpotSymbolsByBase(spotSymbols, preferredQuotes)
	perpetuals := FilterUSDTPERPTradingFutures(futuresSymbols, now)
	result := make(map[string]string, len(perpetuals))
	for _, symbol := range perpetuals {
		baseAsset := FuturesBaseAsset(symbol)
		if baseAsset == "" {
			continue
		}
		spotSymbol, ok := spotByBase[baseAsset]
		if !ok {
			continue
		}
		result[symbol.Symbol] = spotSymbol.Symbol
	}
	return result
}

// FilterUSDTPERPTradingFutures 过滤出 USDT 结尾、状态 TRADING、PERPETUAL、保证金 USDT 且未临近下市的永续合约。
func FilterUSDTPERPTradingFutures(symbols []FuturesSymbol, now time.Time) []FuturesSymbol {
	result := make([]FuturesSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		if !IsValidExchangeSymbol(symbol.Symbol) {
			continue
		}
		if !strings.HasSuffix(symbol.Symbol, "USDT") {
			continue
		}
		if symbol.Status != "TRADING" {
			continue
		}
		if symbol.ContractType != "PERPETUAL" {
			continue
		}
		if symbol.MarginAsset != "USDT" {
			continue
		}
		if HasUpcomingFuturesDelist(symbol, now) {
			continue
		}
		result = append(result, symbol)
	}
	return result
}

// HasUpcomingFuturesDelist 报告合约是否在未来一年内交割/下市（永续 DeliveryDate<=0 视为否）。
func HasUpcomingFuturesDelist(symbol FuturesSymbol, now time.Time) bool {
	if symbol.DeliveryDate <= 0 {
		return false
	}
	return symbol.DeliveryDate <= now.AddDate(1, 0, 0).UnixMilli()
}

// IsValidExchangeSymbol 报告 symbol 是否仅由大写字母与数字组成（基本格式校验）。
func IsValidExchangeSymbol(symbol string) bool {
	if strings.TrimSpace(symbol) == "" {
		return false
	}
	for _, r := range symbol {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
