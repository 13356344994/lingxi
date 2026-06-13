package app

import (
	"math"

	"lingxi/internal/venue"
)

// cross_venue_qty.go 两腿张数/步进对齐:ContractBasedPerp 所内换算,取较小可成交量。

// alignCrossVenueQty 把目标名义折算为两腿一致的币数量(向下取整到各所步进/张数)。
func alignCrossVenueQty(notionalUSDT, refPrice float64, highInstr, lowInstr venue.Instrument, highCaps, lowCaps venue.Capabilities) float64 {
	if notionalUSDT <= 0 || refPrice <= 0 {
		return 0
	}
	rawQty := notionalUSDT / refPrice
	highQty := normalizeVenueQty(rawQty, highInstr, highCaps.ContractBasedPerp)
	lowQty := normalizeVenueQty(rawQty, lowInstr, lowCaps.ContractBasedPerp)
	qty := math.Min(highQty, lowQty)
	// 二次对齐:取 min 后再分别 floor,确保两边都能下
	qty = math.Min(normalizeVenueQty(qty, highInstr, highCaps.ContractBasedPerp), normalizeVenueQty(qty, lowInstr, lowCaps.ContractBasedPerp))
	if qty <= executionQtyTolerance {
		return 0
	}
	return qty
}

// normalizeVenueQty 按 instrument 步进/合约面值向下取整(币数量口径)。
// ContractBasedPerp 所:StepSize 为「张」步进(lotSz/OrderSizeRound),须先对张数取整再 ×ContractSize,
// 与 okx/gate PlaceOrder 内 qty→张 换算保持一致(不可把 StepSize 当币步进,否则对齐量大于实际可成交量)。
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

// crossVenueLegExecutedCoinQty 把 OrderResult.ExecutedQty 归一化为账本用的币数量。
// gate/okx 适配器在 lookupInstrument 失败时 mult 默认为 1,QueryOrder 可能把张数原样写入 ExecutedQty;
// 开仓计划 plan.Gate.{High,Low}Instr 来自闸门(ContractSize 可靠),用于补全 ×ContractSize 换算。
func crossVenueLegExecutedCoinQty(caps venue.Capabilities, instr venue.Instrument, res venue.OrderResult, peerCoinQty float64) float64 {
	q := res.ExecutedQty
	if q <= executionQtyTolerance {
		return 0
	}
	if !caps.ContractBasedPerp || instr.ContractSize <= 0 {
		return q
	}
	asCoin := normalizeVenueQty(q, instr, true)
	asFromContracts := normalizeVenueQty(q*instr.ContractSize, instr, true)
	if asFromContracts <= executionQtyTolerance {
		return asCoin
	}
	if asCoin <= executionQtyTolerance {
		return asFromContracts
	}
	if peerCoinQty > executionQtyTolerance {
		if qtyDeviation(asCoin, peerCoinQty) <= qtyDeviation(asFromContracts, peerCoinQty) {
			return asCoin
		}
		return asFromContracts
	}
	// 无 peer:张数通常小于 ContractSize(单张币面值),已换算的 coin 口径应明显更大。
	if q < instr.ContractSize && asFromContracts > asCoin+executionQtyTolerance {
		return asFromContracts
	}
	return asCoin
}

// reconcileCrossVenueOpenBookQty 开仓落盘时取两腿一致的币数量;优先信任币本位腿的 ExecutedQty。
func reconcileCrossVenueOpenBookQty(highCaps, lowCaps venue.Capabilities, highCoin, lowCoin float64) float64 {
	switch {
	case !highCaps.ContractBasedPerp && highCoin > executionQtyTolerance:
		return highCoin
	case !lowCaps.ContractBasedPerp && lowCoin > executionQtyTolerance:
		return lowCoin
	case highCoin > executionQtyTolerance && lowCoin > executionQtyTolerance:
		return math.Min(highCoin, lowCoin)
	case highCoin > executionQtyTolerance:
		return highCoin
	case lowCoin > executionQtyTolerance:
		return lowCoin
	default:
		return 0
	}
}

// crossVenueBookNotionalUSDT 由真实成交币数量 × 参考价得到落盘名义(与两腿 qty 口径一致)。
func crossVenueBookNotionalUSDT(bookQty, refPrice float64, highRes, lowRes venue.OrderResult) float64 {
	if bookQty <= executionQtyTolerance {
		return 0
	}
	if refPrice > 0 {
		return bookQty * refPrice
	}
	px := highRes.AvgPrice
	if px <= 0 {
		px = lowRes.AvgPrice
	}
	if px <= 0 {
		return 0
	}
	return bookQty * px
}
