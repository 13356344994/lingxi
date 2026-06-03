package app

import (
	"time"
)

// ExecutionCostRecord 记录单次执行的延迟和滑点成本
type ExecutionCostRecord struct {
	Symbol           string
	Action           string // open, close, recover
	TargetQuantity   float64
	ExecutedQuantity float64
	TargetPrice      float64       // 期望价格 (VWAP或参考价)
	ExecutedPrice    float64       // 实际成交均价
	SlippageUSDT     float64       // 滑点造成的实际法币损失/收益
	Latency          time.Duration // 从发单到最终确认的耗时
	Attempts         int           // 尝试次数(针对IOC重试或恢复)
}

// SettlementAttribution 记录单笔资金费结算的利润归因
type SettlementAttribution struct {
	Symbol           string
	FundingIncome    float64 // 资金费收入
	SpotFees         float64 // 现货手续费
	FuturesFees      float64 // 合约手续费
	BorrowCosts      float64 // 借币利息成本
	OpenSlippage     float64 // 开仓滑点成本
	CloseSlippage    float64 // 平仓滑点成本
	RecoveryFriction float64 // 恢复造成的摩擦成本
	NetPnL           float64 // 最终净利润
}

// CycleSettlement 表示一次完整狙击周期的落袋结果。
type CycleSettlement struct {
	TraceRecordMetadata
	RunID                 string
	CycleID               string
	Targets               int
	FundingIncome         float64
	ExpectedFundingIncome float64
	FundingIncomeDiff     float64
	TradingPnL            float64
	SpotTradingPnL        float64
	FuturesTradingPnL     float64
	TotalFees             float64
	TotalBorrowCosts      float64
	NetPnL                float64
	SnapshotNetPnL        float64
	ResidualSpotValue     float64
	SpotUSDTDelta         float64
	FuturesUSDTDelta      float64
	CloseExecutedAt       time.Time
	FundingTimePoint      time.Time
	BeforeSnapshot        CycleAccountSnapshot
	AfterSnapshot         CycleAccountSnapshot
	FundingIncomePending  bool
	FundingSettledAt      time.Time
	HasAlert              bool
	Alerts                []string
	Attributions          []SettlementAttribution
}

// CalculateSlippage 计算执行滑点造成的法币成本 (以 USDT 计价)
// 传入 direction 时，正代表做多(期望买的越低越好)，负代表做空(期望卖的越高越好)
func CalculateSlippage(targetPrice, executedPrice, quantity float64, isBuy bool) float64 {
	if targetPrice <= 0 || executedPrice <= 0 || quantity <= 0 {
		return 0
	}

	// 滑点 = (实际价格 - 目标价格) / 目标价格
	// 如果是买入，实际价格高于目标价格产生负滑点(成本)
	// 如果是卖出，实际价格低于目标价格产生负滑点(成本)
	if isBuy {
		return (targetPrice - executedPrice) * quantity
	}
	return (executedPrice - targetPrice) * quantity
}
