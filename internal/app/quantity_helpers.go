package app

// 数量/价格量化与格式化助手(原散落于 engine_quantity.go / cross_venue_qty.go / cross_venue_maker.go /
// cross_venue_funding.go / cross_venue_rotation.go;遗留引擎与 carry 删除后,方向引擎所需部分在此存活)。

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

// executionQtyTolerance 数量比较容差(原 engine_types.go)。
const executionQtyTolerance = 1e-9

// SymbolTradeRules 交易规则(步进/最小数量/最小名义/价格 tick)。
type SymbolTradeRules struct {
	MinQty      float64
	StepSize    float64
	PriceTick   float64
	MinNotional float64
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

func floorToStep(value float64, step float64) float64 {
	return execution.FloorToStep(value, step)
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

func normalizeQuantity(raw float64, rules SymbolTradeRules, fallbackPrecision int) float64 {
	if raw <= 0 {
		return 0
	}
	if rules.StepSize > 0 {
		return floorToStep(raw, rules.StepSize)
	}
	return roundDownQuantity(raw, fallbackPrecision)
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

// normalizeVenueQty 把数量向下对齐到该所步进(张本位所先换算张数再回币量)。
func normalizeVenueQty(qty float64, instr venue.Instrument, contractBased bool) float64 {
	if qty <= 0 {
		return 0
	}
	if contractBased && instr.ContractSize > 0 {
		contracts := qty / instr.ContractSize
		if instr.StepSize > 0 {
			contracts = math.Floor(contracts/instr.StepSize+1e-12) * instr.StepSize
		} else {
			contracts = math.Floor(contracts + 1e-12)
		}
		if contracts <= 0 {
			return 0
		}
		return contracts * instr.ContractSize
	}
	if instr.StepSize > 0 {
		qty = floorToStep(qty, instr.StepSize)
	} else if instr.QtyPrecision >= 0 {
		qty = roundDownQuantity(qty, instr.QtyPrecision)
	}
	return qty
}

func instrumentRulesFromVenue(instr venue.Instrument) SymbolTradeRules {
	return SymbolTradeRules{
		MinQty:      instr.StepSize,
		StepSize:    instr.StepSize,
		MinNotional: instr.MinNotional,
	}
}

func crossVenueQtyTradable(qty, refPrice float64, instr venue.Instrument) bool {
	return isQuantityTradable(qty, refPrice, instrumentRulesFromVenue(instr))
}

// iocLimitPrice IOC 限价:按滑点预算在对手价上加/减 bps。
func iocLimitPrice(side venue.Side, book venue.BookTicker, slippageBps float64) float64 {
	bps := slippageBps / 10000
	if side == venue.SideBuy {
		return book.AskPrice * (1 + bps)
	}
	return book.BidPrice * (1 - bps)
}

// makerPassivePrice 被动挂单价:买挂 bid、卖挂 ask(post-only 不吃单)。
func makerPassivePrice(side venue.Side, book venue.BookTicker) float64 {
	if side == venue.SideBuy {
		return book.BidPrice
	}
	return book.AskPrice
}

// mergeCrossVenueOrderResults 合并同腿两次成交(如 maker 部分成交 + taker 兜底)的订单结果。
func mergeCrossVenueOrderResults(a, b venue.OrderResult) venue.OrderResult {
	if a.ExecutedQty <= executionQtyTolerance {
		return b
	}
	if b.ExecutedQty <= executionQtyTolerance {
		return a
	}
	out := a
	total := a.ExecutedQty + b.ExecutedQty
	out.AvgPrice = (a.AvgPrice*a.ExecutedQty + b.AvgPrice*b.ExecutedQty) / total
	out.ExecutedQty = total
	out.CumQuoteUSDT = a.CumQuoteUSDT + b.CumQuoteUSDT
	out.Fee = a.Fee + b.Fee
	if out.FeeAsset == "" {
		out.FeeAsset = b.FeeAsset
	}
	return out
}

// fetchVenuePerpAvailableUSDT 拉取该所永续账户可用保证金 USDT。
func fetchVenuePerpAvailableUSDT(ctx context.Context, v venue.Venue) (float64, error) {
	p, ok := v.(venue.PerpMarginProvider)
	if !ok {
		return 0, fmt.Errorf("venue %q 未实现 PerpMarginProvider", v.Name())
	}
	avail, err := p.FetchPerpAvailableUSDT(ctx)
	if err != nil {
		return 0, err
	}
	return math.Max(avail, 0), nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
