package app

import (
	"context"
	"log"
	"strings"

	"lingxi/internal/venue"
)

// crossVenueExecPhase 标记一次 IOC 在持仓生命周期中的阶段(用于归因累加)。
type crossVenueExecPhase string

const (
	cvExecOpenHigh        crossVenueExecPhase = "open_high"
	cvExecOpenLow         crossVenueExecPhase = "open_low"
	cvExecCloseHigh       crossVenueExecPhase = "close_high"
	cvExecCloseLow        crossVenueExecPhase = "close_low"
	cvExecRollbackHigh    crossVenueExecPhase = "rollback_high"
	cvExecRebalanceHigh   crossVenueExecPhase = "rebalance_high"
	cvExecRebalanceLow    crossVenueExecPhase = "rebalance_low"
	cvExecOrphanCloseHigh crossVenueExecPhase = "orphan_close_high"
	cvExecOrphanCloseLow  crossVenueExecPhase = "orphan_close_low"
)

func crossVenueBookMid(book venue.BookTicker) float64 {
	bid, ask := book.BidPrice, book.AskPrice
	if bid > 0 && ask > 0 {
		return (bid + ask) / 2
	}
	if bid > 0 {
		return bid
	}
	return ask
}

func crossVenueFeeToUSDT(fee float64, feeAsset string, avgPrice float64, logger *log.Logger) float64 {
	if fee <= 0 {
		return 0
	}
	asset := strings.ToUpper(strings.TrimSpace(feeAsset))
	switch asset {
	case "", "USDT", "USDC", "USD", "BUSD", "FDUSD", "DAI", "TUSD":
		return fee
	}
	if avgPrice > 0 {
		return fee * avgPrice
	}
	if logger != nil {
		logger.Printf("[跨所归因] 手续费无法折 USDT fee=%.8f asset=%s,按原值累加", fee, feeAsset)
	}
	return fee
}

func accumulateCrossVenueLegClose(leg *CrossVenueLegState, executedQty, avgPrice float64) {
	if leg == nil || executedQty <= executionQtyTolerance || avgPrice <= 0 {
		return
	}
	if leg.ClosedQty <= executionQtyTolerance {
		leg.ClosedQty = executedQty
		leg.ExitPrice = avgPrice
		return
	}
	prevQty := leg.ClosedQty
	leg.ClosedQty += executedQty
	leg.ExitPrice = (leg.ExitPrice*prevQty + avgPrice*executedQty) / leg.ClosedQty
}

func slippageIsBuyForPhase(phase crossVenueExecPhase) bool {
	switch phase {
	case cvExecOpenLow, cvExecCloseHigh, cvExecRollbackHigh, cvExecRebalanceHigh, cvExecOrphanCloseHigh:
		return true
	default:
		return false
	}
}

func crossVenueExecPhaseNeedsQtyNorm(phase crossVenueExecPhase) bool {
	switch phase {
	case cvExecCloseHigh, cvExecCloseLow, cvExecOrphanCloseHigh, cvExecOrphanCloseLow,
		cvExecRollbackHigh, cvExecRebalanceHigh, cvExecRebalanceLow:
		return true
	default:
		return false
	}
}

func crossVenueLegVenueForPhase(pos CrossVenuePosition, phase crossVenueExecPhase) string {
	switch phase {
	case cvExecCloseHigh, cvExecOrphanCloseHigh, cvExecRollbackHigh, cvExecRebalanceHigh:
		return pos.HighVenue
	case cvExecCloseLow, cvExecOrphanCloseLow, cvExecRebalanceLow:
		return pos.LowVenue
	default:
		return ""
	}
}

func crossVenuePeerBookQtyForPhase(pos CrossVenuePosition, phase crossVenueExecPhase) float64 {
	switch phase {
	case cvExecCloseHigh, cvExecOrphanCloseHigh, cvExecRollbackHigh, cvExecRebalanceHigh:
		if pos.LowLeg.Qty > executionQtyTolerance {
			return pos.LowLeg.Qty
		}
		return pos.HighLeg.Qty
	case cvExecCloseLow, cvExecOrphanCloseLow, cvExecRebalanceLow:
		if pos.HighLeg.Qty > executionQtyTolerance {
			return pos.HighLeg.Qty
		}
		return pos.LowLeg.Qty
	default:
		return 0
	}
}

// normalizeCrossVenueCloseExecutedQty 把平仓/回滚/再平衡成交的 ExecutedQty 归一化为币数量(与开仓侧 crossVenueLegExecutedCoinQty 对称)。
// gate/okx 张本位所 lookupInstrument 失败时标记 attribution_incomplete,禁止把张数当币数。
func (e *CrossVenueExecutor) normalizeCrossVenueCloseExecutedQty(ctx context.Context, pos *CrossVenuePosition, phase crossVenueExecPhase, res venue.OrderResult) float64 {
	if pos == nil || res.ExecutedQty <= executionQtyTolerance {
		return 0
	}
	venueName := crossVenueLegVenueForPhase(*pos, phase)
	if venueName == "" {
		return res.ExecutedQty
	}
	v := e.venueByName(venueName)
	if v == nil {
		return res.ExecutedQty
	}
	caps := v.Capabilities()
	sym := venue.Symbol(pos.Symbol)
	instr, ok := lookupVenueInstrument(ctx, v, sym)
	if caps.ContractBasedPerp {
		if !ok || instr.ContractSize <= 0 {
			pos.AttributionIncomplete = true
			if e.logger != nil {
				e.logger.Printf("[跨所归因] ContractSize lookup 失败 symbol=%s venue=%s phase=%s,标记 attribution_incomplete",
					pos.Symbol, venueName, phase)
			}
			return 0
		}
	}
	peerQty := crossVenuePeerBookQtyForPhase(*pos, phase)
	coinQty := crossVenueLegExecutedCoinQty(caps, instr, res, peerQty)
	if coinQty <= executionQtyTolerance && peerQty > executionQtyTolerance {
		pos.AttributionIncomplete = true
		if e.logger != nil {
			e.logger.Printf("[跨所归因] 成交量归一化失败 symbol=%s venue=%s phase=%s raw=%.6f peer=%.6f",
				pos.Symbol, venueName, phase, res.ExecutedQty, peerQty)
		}
		return 0
	}
	return coinQty
}

func (e *CrossVenueExecutor) venueByName(name string) venue.Venue {
	if e == nil || e.venues == nil {
		return nil
	}
	if v, ok := e.venues[name]; ok {
		return v
	}
	needle := strings.ToLower(strings.TrimSpace(name))
	for k, v := range e.venues {
		if strings.ToLower(strings.TrimSpace(k)) == needle {
			return v
		}
	}
	return nil
}

// recordCrossVenueExecution 在 placeCrossVenueIOC 成交后累加归因原始数据(费用/滑点/平仓价/回滚成本)。
// 返回账本口径的成交币数量(平仓/回滚/再平衡相位对 gate/okx 做张→币归一化;开仓相位返回原始 ExecutedQty)。
func (e *CrossVenueExecutor) recordCrossVenueExecution(ctx context.Context, pos *CrossVenuePosition, phase crossVenueExecPhase, res venue.OrderResult, refMid float64) float64 {
	if e == nil || pos == nil || res.ExecutedQty <= executionQtyTolerance {
		return 0
	}
	execQty := res.ExecutedQty
	if crossVenueExecPhaseNeedsQtyNorm(phase) {
		execQty = e.normalizeCrossVenueCloseExecutedQty(ctx, pos, phase, res)
		if execQty <= executionQtyTolerance {
			// 成交量不可信时不累加 ClosedQty,但仍记录费用/滑点观测。
			feeUSDT := crossVenueFeeToUSDT(res.Fee, res.FeeAsset, res.AvgPrice, e.logger)
			switch phase {
			case cvExecCloseHigh, cvExecCloseLow, cvExecOrphanCloseHigh, cvExecOrphanCloseLow:
				pos.CloseFeesUSDT += feeUSDT
			case cvExecRollbackHigh:
				pos.RollbackCostUSDT += feeUSDT
			case cvExecRebalanceHigh, cvExecRebalanceLow:
				pos.RebalanceCostUSDT += feeUSDT
			}
			return 0
		}
	}
	feeUSDT := crossVenueFeeToUSDT(res.Fee, res.FeeAsset, res.AvgPrice, e.logger)
	slip := CalculateSlippage(refMid, res.AvgPrice, execQty, slippageIsBuyForPhase(phase))

	switch phase {
	case cvExecOpenHigh, cvExecOpenLow:
		pos.OpenFeesUSDT += feeUSDT
		pos.OpenSlippageUSDT += slip
	case cvExecCloseHigh, cvExecCloseLow, cvExecOrphanCloseHigh, cvExecOrphanCloseLow:
		pos.CloseFeesUSDT += feeUSDT
		pos.CloseSlippageUSDT += slip
		switch phase {
		case cvExecCloseHigh, cvExecOrphanCloseHigh:
			accumulateCrossVenueLegClose(&pos.HighLeg, execQty, res.AvgPrice)
		case cvExecCloseLow, cvExecOrphanCloseLow:
			accumulateCrossVenueLegClose(&pos.LowLeg, execQty, res.AvgPrice)
		}
	case cvExecRollbackHigh:
		pos.RollbackCostUSDT += feeUSDT
		accumulateCrossVenueLegClose(&pos.HighLeg, execQty, res.AvgPrice)
	case cvExecRebalanceHigh, cvExecRebalanceLow:
		pos.RebalanceCostUSDT += feeUSDT
		switch phase {
		case cvExecRebalanceHigh:
			accumulateCrossVenueLegClose(&pos.HighLeg, execQty, res.AvgPrice)
		case cvExecRebalanceLow:
			accumulateCrossVenueLegClose(&pos.LowLeg, execQty, res.AvgPrice)
		}
	}
	return execQty
}
