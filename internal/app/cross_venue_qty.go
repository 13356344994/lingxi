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
func normalizeVenueQty(qty float64, instr venue.Instrument, contractBased bool) float64 {
	if qty <= 0 {
		return 0
	}
	if contractBased && instr.ContractSize > 0 {
		// 张数取整 → 币数量
		contracts := math.Floor(qty/instr.ContractSize + 1e-12)
		if contracts <= 0 {
			return 0
		}
		qty = contracts * instr.ContractSize
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
