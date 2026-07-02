package app

import (
	"context"
	"log"
	"math"
	"strings"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

// quantizePriceToTick 把限价对齐到 tick: BUY 向上(ceil)保 IOC 可成交; SELL 向下(floor)。
func quantizePriceToTick(price, tick float64, side venue.Side) float64 {
	if tick <= 0 {
		return price
	}
	if side == venue.SideBuy {
		return execution.CeilToStep(price, tick)
	}
	return execution.FloorToStep(price, tick)
}

func logLiveOrderLeg(logger *log.Logger, venueName string, mkt venue.Market, sym string, side venue.Side, price, tick, qty, step float64) {
	if logger == nil {
		return
	}
	tickStr := "0"
	if tick > 0 {
		tickStr = formatPriceForTick(tick, tick)
	}
	logger.Printf("[下单] venue=%s market=%s %s side=%s price=%s tick=%s qty=%s",
		venueName, mkt, sym, side,
		formatPriceForTick(price, tick),
		tickStr,
		formatQuantityForRules(qty, SymbolTradeRules{StepSize: step}, 8),
	)
}

func traderVenueName(tr interface{}) string {
	if tr == nil {
		return "unknown"
	}
	if v, ok := tr.(interface{ Name() string }); ok {
		if name := strings.TrimSpace(v.Name()); name != "" {
			return name
		}
	}
	return "unknown"
}

// quantizeQtyToStep 把数量向下对齐到 step 倍数。
func quantizeQtyToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return execution.FloorToStep(qty, step)
}

type legInstrumentMeta struct {
	tick, step, minNotional float64
}

func legInstrumentFields(tick, step, minNotional float64) legInstrumentMeta {
	return legInstrumentMeta{tick: tick, step: step, minNotional: minNotional}
}

func quantizeOrderPriceQty(price, qty float64, side venue.Side, instr venue.Instrument, contractBased bool, logger *log.Logger, sym string) (float64, float64) {
	if logger != nil {
		if price > 0 && instr.TickSize <= 0 {
			logger.Printf("[量化] %s 缺 TickSize,价格未对齐", sym)
		}
		if qty > 0 && instr.StepSize <= 0 && !contractBased {
			logger.Printf("[量化] %s 缺 StepSize,数量未对齐", sym)
		}
	}
	qPrice := quantizePriceToTick(price, instr.TickSize, side)
	qQty := qty
	if contractBased && instr.ContractSize > 0 {
		qQty = normalizeVenueQty(qty, instr, true)
	} else if instr.StepSize > 0 {
		qQty = quantizeQtyToStep(qty, instr.StepSize)
	}
	return qPrice, qQty
}

func instrumentHasQtyRules(instr venue.Instrument) bool {
	return instr.StepSize > 0 || instr.MinNotional > 0 || instr.ContractSize > 0
}

func openLegTradableAfterQuantize(qty, refPrice float64, instr venue.Instrument) bool {
	if qty <= executionQtyTolerance {
		return false
	}
	if !instrumentHasQtyRules(instr) {
		return true
	}
	return crossVenueQtyTradable(qty, refPrice, instr)
}

func alignOpenQtyFromInstruments(planQty float64, highInstr, lowInstr venue.Instrument, highCaps, lowCaps venue.Capabilities) float64 {
	if !instrumentHasQtyRules(highInstr) && !instrumentHasQtyRules(lowInstr) {
		return planQty
	}
	highQty := planQty
	lowQty := planQty
	if instrumentHasQtyRules(highInstr) {
		highQty = normalizeVenueQty(planQty, highInstr, highCaps.ContractBasedPerp)
	}
	if instrumentHasQtyRules(lowInstr) {
		lowQty = normalizeVenueQty(planQty, lowInstr, lowCaps.ContractBasedPerp)
	}
	return math.Min(highQty, lowQty)
}

func stringsEqualMarket(a, b string) bool {
	a = stringsTrimLower(a)
	b = stringsTrimLower(b)
	if a == b {
		return true
	}
	if (a == "perp" || a == "swap" || a == "futures") && (b == "perp" || b == "swap" || b == "futures") {
		return true
	}
	return false
}

func stringsTrimLower(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// lookupVenueInstrumentForMarket 从 venue 已缓存的 FetchInstruments 查单腿元数据(非逐单 HTTP)。
func lookupVenueInstrumentForMarket(ctx context.Context, v venue.Venue, sym venue.Symbol, mkt venue.Market) (venue.Instrument, bool) {
	if v == nil {
		return venue.Instrument{}, false
	}
	instrs, err := v.FetchInstruments(ctx)
	if err != nil {
		return venue.Instrument{}, false
	}
	for _, in := range instrs {
		if in.Symbol == sym && in.Market == mkt {
			return in, true
		}
	}
	return venue.Instrument{}, false
}
