package app

import (
	"fmt"
	"lingxi/internal/execution"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载交易对/数量/价格的纯计算与格式化工具
// （金标的交集、交易规则解析、步进取整、精度格式化、ID 生成等，同包搬移、行为不变），
// 向单文件 ≤ 800 行门禁靠拢。

func intersectGoldenSymbols(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol, preferredQuotes []string) ([]string, map[string]string) {
	pairByFuture := buildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, preferredQuotes, time.Now().UTC())
	result := make([]string, 0, len(pairByFuture))
	for symbol := range pairByFuture {
		result = append(result, symbol)
	}
	sort.Strings(result)
	return result, pairByFuture
}

func extractTradeRules(filters []ExchangeFilter) SymbolTradeRules {
	rules := SymbolTradeRules{}
	for _, filter := range filters {
		switch filter.FilterType {
		case "PRICE_FILTER":
			if rules.PriceTick == 0 {
				rules.PriceTick = parseFloat(filter.TickSize)
			}
		case "LOT_SIZE", "MARKET_LOT_SIZE":
			if rules.MinQty == 0 {
				rules.MinQty = parseFloat(filter.MinQty)
			}
			if rules.StepSize == 0 {
				rules.StepSize = parseFloat(filter.StepSize)
			}
		case "MIN_NOTIONAL":
			if rules.MinNotional == 0 {
				rules.MinNotional = parseFloat(filter.MinNotional)
			}
		case "NOTIONAL":
			if rules.MinNotional == 0 {
				rules.MinNotional = parseFloat(nonEmpty(filter.MinNotional, filter.Notional))
			}
		case "PERCENT_PRICE":
			if rules.PercentPriceUp == 0 {
				rules.PercentPriceUp = parseFloat(filter.MultiplierUp)
			}
			if rules.PercentPriceDown == 0 {
				rules.PercentPriceDown = parseFloat(filter.MultiplierDown)
			}
			if rules.PercentPriceAvgMins == 0 {
				rules.PercentPriceAvgMins = filter.AvgPriceMins
			}
		case "PERCENT_PRICE_BY_SIDE":
			if rules.BidPercentPriceUp == 0 {
				rules.BidPercentPriceUp = parseFloat(filter.BidMultiplierUp)
			}
			if rules.BidPercentPriceDown == 0 {
				rules.BidPercentPriceDown = parseFloat(filter.BidMultiplierDown)
			}
			if rules.AskPercentPriceUp == 0 {
				rules.AskPercentPriceUp = parseFloat(filter.AskMultiplierUp)
			}
			if rules.AskPercentPriceDown == 0 {
				rules.AskPercentPriceDown = parseFloat(filter.AskMultiplierDown)
			}
			if rules.PercentPriceAvgMins == 0 {
				rules.PercentPriceAvgMins = filter.AvgPriceMins
			}
		}
	}

	return rules
}

func normalizeQuantity(raw float64, rules SymbolTradeRules, fallbackPrecision int) float64 {
	if raw <= 0 {
		return 0
	}
	if rules.StepSize > 0 {
		return floorToStep(raw, rules.StepSize)
	}

	return roundDownQuantity(raw, fallbackPrecision)
}

func isQuantityTradable(quantity float64, referencePrice float64, rules SymbolTradeRules) bool {
	if quantity <= 0 {
		return false
	}
	if rules.MinQty > 0 && quantity+1e-12 < rules.MinQty {
		return false
	}
	if referencePrice > 0 && rules.MinNotional > 0 && quantity*referencePrice+1e-12 < rules.MinNotional {
		return false
	}

	return true
}

// floorToStep/ceilToStep 是对 internal/execution 纯步进对齐内核的同包薄封装：保留原签名，
// 避免触动 app 内既有调用点，同时让定价对齐逻辑只有一份实现（执行域包内）。
func floorToStep(value float64, step float64) float64 {
	return execution.FloorToStep(value, step)
}

func ceilToStep(value float64, step float64) float64 {
	return execution.CeilToStep(value, step)
}

func roundDownQuantity(value float64, precision int) float64 {
	if precision < 0 {
		precision = 0
	}
	factor := math.Pow10(precision)
	return math.Floor(value*factor) / factor
}

func precisionFromIncrement(increment float64, fallbackPrecision int) int {
	if increment <= 0 {
		if fallbackPrecision < 0 {
			return 0
		}
		return fallbackPrecision
	}
	text := strconv.FormatFloat(increment, 'f', -1, 64)
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		return len(strings.TrimRight(text[dot+1:], "0"))
	}
	return 0
}

func formatFloatWithPrecision(value float64, precision int) string {
	if precision < 0 {
		precision = 0
	}
	return strconv.FormatFloat(roundDownQuantity(value, precision), 'f', precision, 64)
}

func formatQuantityForRules(quantity float64, rules SymbolTradeRules, fallbackPrecision int) string {
	normalized := normalizeQuantity(quantity, rules, fallbackPrecision)
	precision := precisionFromIncrement(rules.StepSize, fallbackPrecision)
	return formatFloatWithPrecision(normalized, precision)
}

func formatPriceForTick(price float64, tickSize float64) string {
	precision := precisionFromIncrement(tickSize, 0)
	return formatFloatWithPrecision(price, precision)
}

func formatRequestAmount(value float64) string {
	text := formatFloatWithPrecision(value, 12)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		return "0"
	}
	return text
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func parseFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newClientOrderID(prefix string, symbol string) string {
	symbol = strings.ToLower(symbol)
	symbol = strings.ReplaceAll(symbol, "_", "")
	symbol = strings.ReplaceAll(symbol, "-", "")
	if len(symbol) > 10 {
		symbol = symbol[:10]
	}

	return fmt.Sprintf("%s-%s-%d", prefix, symbol, time.Now().UnixNano()%1_000_000_000_000)
}
