package app

import (
	"fmt"
	"strings"
	"time"
)

// trade_detail_log.go 在每次结算时为每个标的打一条**结构化、可 grep/脚本解析**的逐笔成交明细行，
// 把成交价口径的执行质量（资金费实收/预期、双腿手续费、开/平滑点、借币利息、恢复摩擦、单标的净利）
// 摊到一行 key=value，便于实盘后用脚本聚合分析（如 `grep TRADE_DETAIL server.log | ...`）。
//
// 这是只写的可观测性旁路：不改变任何结算/风控口径，纯粹把已算好的归因（SettlementAttribution）落到日志。
// 行首固定 token `TRADE_DETAIL` 便于过滤；funding_pending=true 表示资金费尚未实收（funding 字段为预期值，
// 最终净利以异步结算后的 settlements.json / 周期收益通知为准）。

const tradeDetailLogToken = "TRADE_DETAIL"

// formatSettlementAttributionLog 把单标的的结算归因渲染成一条结构化日志行。纯函数，便于单测。
// fundingPending=true 时 funding 字段是「预期资金费」（尚未实收），用 funding_pending 标志显式区分。
func formatSettlementAttributionLog(runID, cycleID string, settledAt time.Time, a SettlementAttribution, fundingPending bool) string {
	settledText := "-"
	if !settledAt.IsZero() {
		settledText = settledAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"%s run=%s cycle=%s symbol=%s settled_at=%s funding_pending=%t funding=%.6f spot_fee=%.6f fut_fee=%.6f borrow=%.6f open_slip=%.6f close_slip=%.6f recovery_friction=%.6f net_pnl=%.6f",
		tradeDetailLogToken,
		nonEmpty(strings.TrimSpace(runID), "-"),
		nonEmpty(strings.TrimSpace(cycleID), "-"),
		nonEmpty(strings.ToUpper(strings.TrimSpace(a.Symbol)), "-"),
		settledText,
		fundingPending,
		a.FundingIncome,
		a.SpotFees,
		a.FuturesFees,
		a.BorrowCosts,
		a.OpenSlippage,
		a.CloseSlippage,
		a.RecoveryFriction,
		a.NetPnL,
	)
}

// logSettlementTradeDetails 为本周期每个标的打一条 TRADE_DETAIL 明细行（nil-safe，纯旁路，不影响交易/结算）。
func (e *FundingRateSniperEngine) logSettlementTradeDetails(settlement CycleSettlement) {
	if e == nil || e.logger == nil || len(settlement.Attributions) == 0 {
		return
	}
	for _, attribution := range settlement.Attributions {
		e.logger.Print(formatSettlementAttributionLog(
			settlement.RunID, settlement.CycleID, settlement.CloseExecutedAt,
			attribution, settlement.FundingIncomePending,
		))
	}
}
