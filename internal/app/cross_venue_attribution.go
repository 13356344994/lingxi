package app

import (
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

// recordCrossVenueExecution 在 placeCrossVenueIOC 成交后累加归因原始数据(费用/滑点/平仓价/回滚成本)。
func (e *CrossVenueExecutor) recordCrossVenueExecution(pos *CrossVenuePosition, phase crossVenueExecPhase, res venue.OrderResult, refMid float64) {
	if e == nil || pos == nil || res.ExecutedQty <= executionQtyTolerance {
		return
	}
	feeUSDT := crossVenueFeeToUSDT(res.Fee, res.FeeAsset, res.AvgPrice, e.logger)
	slip := CalculateSlippage(refMid, res.AvgPrice, res.ExecutedQty, slippageIsBuyForPhase(phase))

	switch phase {
	case cvExecOpenHigh, cvExecOpenLow:
		pos.OpenFeesUSDT += feeUSDT
		pos.OpenSlippageUSDT += slip
	case cvExecCloseHigh, cvExecCloseLow, cvExecOrphanCloseHigh, cvExecOrphanCloseLow:
		pos.CloseFeesUSDT += feeUSDT
		pos.CloseSlippageUSDT += slip
		switch phase {
		case cvExecCloseHigh, cvExecOrphanCloseHigh:
			accumulateCrossVenueLegClose(&pos.HighLeg, res.ExecutedQty, res.AvgPrice)
		case cvExecCloseLow, cvExecOrphanCloseLow:
			accumulateCrossVenueLegClose(&pos.LowLeg, res.ExecutedQty, res.AvgPrice)
		}
	case cvExecRollbackHigh:
		// 回滚成本 = 回滚单手续费(仅手续费)。第一腿(空)从开到回滚的价格 PnL 不在此累加:
		// accumulateCrossVenueLegClose 会把 ExitPrice/ClosedQty 填进 HighLeg,价格 PnL 由
		// computeCrossVenueTradePnL 以 (entry−exit)×qty 统一计入 trade_pnl(口径与 rebalance 一致)。
		// 【关键】若此处再把 pricePnL 计进 rollback_cost,会与 trade_pnl 重复计数(net_pnl 里 +pricePnL
		// 与 −pricePnL 抵消,真实裸腿盈亏被抹掉),见 cost_accounting.go 第 64-74 行同款"勿重复计数"说明。
		pos.RollbackCostUSDT += feeUSDT
		accumulateCrossVenueLegClose(&pos.HighLeg, res.ExecutedQty, res.AvgPrice)
	case cvExecRebalanceHigh, cvExecRebalanceLow:
		pos.RebalanceCostUSDT += feeUSDT
		switch phase {
		case cvExecRebalanceHigh:
			accumulateCrossVenueLegClose(&pos.HighLeg, res.ExecutedQty, res.AvgPrice)
		case cvExecRebalanceLow:
			accumulateCrossVenueLegClose(&pos.LowLeg, res.ExecutedQty, res.AvgPrice)
		}
	}
}
