package app

import (
	"math"

	"lingxi/internal/venue"
)

// cross_venue_qty.go 两腿张数/步进对齐:ContractBasedPerp 所内换算,取较小可成交量。

// crossVenueAlignedLegQty 两腿各自对齐后的下单数量(per-leg symbol/mid/倍数)。
type crossVenueAlignedLegQty struct {
	HighQty float64
	LowQty  float64
}

// alignCrossVenueQtyPerLeg 每腿用自身 mid 折算名义,取 min 名义后回算各腿 qty(跨计价时 highMult/lowMult 吸收 1000x 缩放)。
func alignCrossVenueQtyPerLeg(
	notionalUSDT, highMid, lowMid, highMult, lowMult float64,
	highInstr, lowInstr venue.Instrument,
	highCaps, lowCaps venue.Capabilities,
) crossVenueAlignedLegQty {
	out := crossVenueAlignedLegQty{}
	if notionalUSDT <= 0 || highMid <= 0 || lowMid <= 0 {
		return out
	}
	if highMult <= 0 {
		highMult = 1
	}
	if lowMult <= 0 {
		lowMult = 1
	}
	highQty := normalizeVenueQty(notionalUSDT/highMid, highInstr, highCaps.ContractBasedPerp)
	lowQty := normalizeVenueQty(notionalUSDT/lowMid, lowInstr, lowCaps.ContractBasedPerp)
	highNotional := highQty * highMid
	lowNotional := lowQty * lowMid
	minNotional := math.Min(highNotional, lowNotional)
	if minNotional <= 0 {
		return out
	}
	highQty = normalizeVenueQty(minNotional/highMid, highInstr, highCaps.ContractBasedPerp)
	lowQty = normalizeVenueQty(minNotional/lowMid, lowInstr, lowCaps.ContractBasedPerp)
	// 二次对齐:取 min 后再分别 floor,确保两边都能下(同计价对时与改造前逐位一致)
	if math.Abs(highMid-lowMid) < 1e-12 && math.Abs(highMult-lowMult) < 1e-12 {
		qty := math.Min(highQty, lowQty)
		qty = math.Min(normalizeVenueQty(qty, highInstr, highCaps.ContractBasedPerp), normalizeVenueQty(qty, lowInstr, lowCaps.ContractBasedPerp))
		if qty <= executionQtyTolerance {
			return out
		}
		out.HighQty = qty
		out.LowQty = qty
		return out
	}
	if highQty <= executionQtyTolerance || lowQty <= executionQtyTolerance {
		return out
	}
	out.HighQty = highQty
	out.LowQty = lowQty
	return out
}

// UnifiedQty 同计价向后兼容:返回改造前 alignCrossVenueQty 的单 qty 口径(两腿相同时取 min 对齐)。
func (a crossVenueAlignedLegQty) UnifiedQty() float64 {
	if a.HighQty <= 0 && a.LowQty <= 0 {
		return 0
	}
	if a.LowQty <= 0 {
		return a.HighQty
	}
	if a.HighQty <= 0 {
		return a.LowQty
	}
	return math.Min(a.HighQty, a.LowQty)
}

// alignCrossVenueQty 把目标名义折算为两腿一致的币数量(向下取整到各所步进/张数)。
// 同计价对(单 refPrice、倍数=1)行为与改造前逐位一致;跨计价请用 alignCrossVenueQtyPerLeg。
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

// crossVenueClampNotionalPerLeg 将名义 cap 并按 per-leg qty/步进对齐;不可交易则返回 0。
func crossVenueClampNotionalPerLeg(
	n float64,
	opp CrossVenueOpportunity,
	tradableCap, highMid, lowMid float64,
	highInstr, lowInstr venue.Instrument,
	highCaps, lowCaps venue.Capabilities,
) float64 {
	if n <= 0 {
		return 0
	}
	if tradableCap > 0 && n > tradableCap+1e-9 {
		n = tradableCap
	}
	if highMid <= 0 || lowMid <= 0 {
		return n
	}
	aligned := alignCrossVenueQtyPerLeg(n, highMid, lowMid, crossVenueOppHighMult(opp), crossVenueOppLowMult(opp), highInstr, lowInstr, highCaps, lowCaps)
	qty := aligned.UnifiedQty()
	if qty <= 0 {
		return 0
	}
	refMid := (highMid + lowMid) / 2
	if !crossVenueQtyTradable(qty, refMid, highInstr) || !crossVenueQtyTradable(qty, refMid, lowInstr) {
		if crossVenueIsCrossDenomPair(opp) {
			if aligned.HighQty <= 0 || aligned.LowQty <= 0 {
				return 0
			}
			if !crossVenueQtyTradable(aligned.HighQty, highMid, highInstr) || !crossVenueQtyTradable(aligned.LowQty, lowMid, lowInstr) {
				return 0
			}
			return math.Min(aligned.HighQty*highMid, aligned.LowQty*lowMid)
		}
		return 0
	}
	return qty * refMid
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
	// 无 peer:ExecutedQty 可能是张数(≥1 且 coin 换算≈q×ContractSize)或已是币数。
	if asFromContracts > asCoin+executionQtyTolerance {
		if q < instr.ContractSize {
			return asFromContracts
		}
		if instr.ContractSize > 0 && asFromContracts/q+1e-9 >= instr.ContractSize {
			return asFromContracts
		}
	}
	return asCoin
}

// reconcileCrossVenueOpenBookQty 开仓落盘时取两腿一致的币数量;两腿均有成交时统一记 min,避免平仓留残腿。
// 当高费腿为张本位且低费腿以「张数」误跟单(币量约为高腿 1/ContractSize)时,以高腿归一化币量为准。
func reconcileCrossVenueOpenBookQty(highCaps, lowCaps venue.Capabilities, highCoin, lowCoin float64) float64 {
	if highCoin <= executionQtyTolerance && lowCoin <= executionQtyTolerance {
		return 0
	}
	if highCoin <= executionQtyTolerance {
		return lowCoin
	}
	if lowCoin <= executionQtyTolerance {
		return highCoin
	}
	if highCaps.ContractBasedPerp && !lowCaps.ContractBasedPerp &&
		highCoin > lowCoin+executionQtyTolerance {
		ratio := highCoin / lowCoin
		if ratio >= 2 && math.Abs(ratio-math.Round(ratio)) < 1e-6 {
			return highCoin
		}
	}
	return math.Min(highCoin, lowCoin)
}

// reconcileCrossVenueOpenBookQtyPerLeg 跨计价开仓落盘:按经济 base 对齐后回写各腿 native qty。
func reconcileCrossVenueOpenBookQtyPerLeg(highCoin, lowCoin, highMult, lowMult float64) (bookHigh, bookLow float64) {
	if highMult <= 0 {
		highMult = 1
	}
	if lowMult <= 0 {
		lowMult = 1
	}
	if highCoin <= executionQtyTolerance && lowCoin <= executionQtyTolerance {
		return 0, 0
	}
	if math.Abs(highMult-lowMult) < 1e-9 {
		qty := reconcileCrossVenueOpenBookQty(venue.Capabilities{}, venue.Capabilities{}, highCoin, lowCoin)
		return qty, qty
	}
	if highCoin <= executionQtyTolerance {
		return 0, lowCoin
	}
	if lowCoin <= executionQtyTolerance {
		return highCoin, 0
	}
	econHigh := highCoin * highMult
	econLow := lowCoin * lowMult
	econMin := math.Min(econHigh, econLow)
	return econMin / highMult, econMin / lowMult
}

func carryLegCoinStep(instr venue.Instrument, contractBased bool) float64 {
	if contractBased && instr.ContractSize > 0 && instr.StepSize > 0 {
		return instr.StepSize * instr.ContractSize
	}
	if instr.StepSize > 0 {
		return instr.StepSize
	}
	return 0
}

func carrySpotPerpCanAlign(spotInstr, perpInstr venue.Instrument, perpCaps venue.Capabilities) bool {
	if perpCaps.ContractBasedPerp && perpInstr.ContractSize <= 0 {
		return false
	}
	return instrumentHasQtyRules(spotInstr) || instrumentHasQtyRules(perpInstr)
}

func carryAlignJointCoinQty(coinQty float64, spotInstr, perpInstr venue.Instrument, spotCaps, perpCaps venue.Capabilities) float64 {
	jointStep := 0.0
	for _, step := range []float64{
		carryLegCoinStep(spotInstr, false),
		carryLegCoinStep(perpInstr, perpCaps.ContractBasedPerp),
	} {
		if step <= 0 {
			continue
		}
		if jointStep <= 0 || step < jointStep {
			jointStep = step
		}
	}
	if jointStep <= 0 {
		return coinQty
	}
	return floorToStep(coinQty, jointStep)
}

// carryAlignSpotPerpOpenQty 把目标币数量向下对齐到 spot/perp 两腿共同可成交量(含张本位永续换算)。
func carryAlignSpotPerpOpenQty(coinQty, refPrice float64, spotInstr, perpInstr venue.Instrument, spotCaps, perpCaps venue.Capabilities) float64 {
	if coinQty <= executionQtyTolerance {
		return 0
	}
	if !carrySpotPerpCanAlign(spotInstr, perpInstr, perpCaps) {
		return coinQty
	}
	aligned := carryAlignJointCoinQty(coinQty, spotInstr, perpInstr, spotCaps, perpCaps)
	if aligned <= executionQtyTolerance {
		return 0
	}
	perpNorm := normalizeVenueQty(aligned, perpInstr, perpCaps.ContractBasedPerp)
	if perpNorm+executionQtyTolerance < aligned {
		spotNorm := normalizeVenueQty(aligned, spotInstr, false)
		aligned = math.Min(spotNorm, perpNorm)
		if aligned <= executionQtyTolerance {
			return 0
		}
		return aligned
	}
	spotNorm := normalizeVenueQty(aligned, spotInstr, false)
	if math.Min(spotNorm, perpNorm) <= executionQtyTolerance {
		return 0
	}
	_ = refPrice
	return aligned
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
