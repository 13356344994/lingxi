package app

import (
	"time"

	"lingxi/internal/money"
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

	// R5 对账口径(M-S3.0 起观测,S4 收尾审定·纯观测、零行为变更、不参与任何熔断/风控):
	// 【审定结论】开/平滑点已隐含在权威 NetPnL —— TradingPnL 由**实际成交价**算出(现货用成交均价、合约用交易所实现盈亏),
	// 成交劣于参考价的滑点已计入;恢复摩擦的真实成本(额外成交 + 手续费)亦已折进 hedge 的平仓名义与 Fee 字段。
	// 因此 Total*SlippageUSDT 是「相对参考价的偏离」执行质量量,**不可再从 NetPnL 扣一次(否则重复计数)**。
	// NetPnLAfterFrictions 经语义重定为「零滑点理想净利基准」= NetPnL − Σ滑点(滑点带符号、负=成本,减法即把成本加回);
	// gap = NetPnL − 基准 = Σ滑点 ≤ 0,量化执行滑点拖累(已在 NetPnL 内,此处仅度量大小)。RecoveryFriction 当前结构性恒 0
	// (恢复路径以成交均价自比),真正漏算项经审查为无;若日后接入「相对恢复前参考价」的真实度量再单列。
	TotalOpenSlippageUSDT     float64
	TotalCloseSlippageUSDT    float64
	TotalRecoveryFrictionUSDT float64
	NetPnLAfterFrictions      float64

	// M-S3.1 对账诚实化(R6):开仓期同源费率模型对本轮手续费的预期值,以及本轮总名义。对账(reconcile)
	// 用 ExpectedTotalFees 作为成本侧的「模型预期」(而非抄实测),令成本侧模型误差不再恒为 0;
	// CycleNotionalUSDT 作为相对偏差的名义分母。两者均为只读对账输入,不参与任何熔断/风控判定。
	ExpectedTotalFees float64
	CycleNotionalUSDT float64
}

// applyFrictionObservation 聚合各 symbol 执行摩擦,并算出「零滑点理想净利基准」NetPnLAfterFrictions(R5 审定语义)。
// 【关键】开/平滑点已隐含在权威 NetPnL(TradingPnL 用实际成交价),故基准 = NetPnL − Σ滑点(滑点带符号、负=成本,
// 减法把成本加回 → 基准 ≥ 实际净利,代表「若零滑点能拿到的理想净利」);gap = NetPnL − 基准 = Σ滑点 ≤ 0 = 执行拖累。
// 这修复了 M-S3.0「NetPnL + 滑点」的重复计数(那样会把已在 NetPnL 的滑点再扣一次)。纯观测:不改 NetPnL 等既有口径,
// 不参与任何熔断;NetPnL 在资金费到账后会变,故 close 与异步结算阶段都应调用本方法重算。
func (s *CycleSettlement) applyFrictionObservation() {
	var openSlip, closeSlip, recovery float64
	for _, attr := range s.Attributions {
		openSlip += attr.OpenSlippage
		closeSlip += attr.CloseSlippage
		recovery += attr.RecoveryFriction
	}
	s.TotalOpenSlippageUSDT = openSlip
	s.TotalCloseSlippageUSDT = closeSlip
	s.TotalRecoveryFrictionUSDT = recovery
	s.NetPnLAfterFrictions = money.SumUSDT(s.NetPnL, -openSlip, -closeSlip, -recovery)
}

// expectedHedgeCostObservation 按与策略 breakeven 同源的配置 taker 费率,估算一组对冲的「模型预期手续费」
// 与本轮总名义(M-S3.1,R6)。模型费 = Σ 各腿(开+平)名义 × 配置费率;它与实测手续费(可能含 BNB 抵扣/
// 挂单返佣/部分成交)的差,正是对账要暴露的成本侧模型误差。借币侧当前实测值本身即由同一利率模型推导,
// 无独立「实测 vs 模型」可比口径,故对账沿用实测借币,待接入交易所真实利息后再单列(诚实标注)。纯只读。
func (e *FundingRateSniperEngine) expectedHedgeCostObservation(hedges []OpenHedge) (expectedFees, notional float64) {
	spotFeeRate := e.cfg.HistoricalConfig.SpotTakerFeeRate
	if spotFeeRate <= 0 {
		spotFeeRate = 0.0010
	}
	futuresFeeRate := e.cfg.HistoricalConfig.FuturesTakerFeeRate
	if futuresFeeRate <= 0 {
		futuresFeeRate = 0.0005
	}
	var feesM, notionalM money.Micros
	for _, hedge := range hedges {
		openSpotNotional := hedge.OpenSpotQuoteQty
		closeSpotNotional := hedge.CloseSpotQuoteQty
		openFuturesNotional := hedge.HedgeQty * hedge.OpenFuturesAvgPrice
		closeFuturesQty := hedge.CloseFuturesExecutedQty
		if closeFuturesQty <= 0 {
			closeFuturesQty = hedge.HedgeQty
		}
		closeFuturesNotional := closeFuturesQty * hedge.CloseFuturesAvgPrice
		feesM = feesM.Add(money.FromUSDT((openSpotNotional + closeSpotNotional) * spotFeeRate))
		feesM = feesM.Add(money.FromUSDT((openFuturesNotional + closeFuturesNotional) * futuresFeeRate))
		notionalM = notionalM.Add(money.FromUSDT(openSpotNotional + openFuturesNotional))
	}
	return feesM.USDT(), notionalM.USDT()
}

// logFrictionObservation 打印 R5 执行质量观测行:权威 NetPnL(已含滑点)vs 零滑点理想净利基准,
// gap = NetPnL − 基准 = Σ滑点 ≤ 0 = 执行滑点拖累(已在 NetPnL 内,此处量化大小)。纯观测,供实盘前度量执行质量。
func (e *FundingRateSniperEngine) logFrictionObservation(s CycleSettlement, phase string) {
	if e == nil || e.logger == nil {
		return
	}
	executionDrag := s.NetPnL - s.NetPnLAfterFrictions
	e.logger.Printf(
		"执行质量观测[%s] cycle=%s NetPnL(已含滑点)=%.4f 零滑点理想净利=%.4f 执行拖累=%.4f 开仓滑点=%.4f 平仓滑点=%.4f 恢复摩擦=%.4f",
		phase, s.CycleID, s.NetPnL, s.NetPnLAfterFrictions, executionDrag,
		s.TotalOpenSlippageUSDT, s.TotalCloseSlippageUSDT, s.TotalRecoveryFrictionUSDT,
	)
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
