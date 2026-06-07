package app

import "math"

// economic_params.go 落地多所重构 §6「经济闸每所实例化」的载体类型(P2)。
//
// 现状(单所):进场闸 / 净利闸 / 撞限豁免都直接读全局 `cfg`。多所后每个引擎持有**该所自己的**
// EconomicParams(实拉费率 + 该所配置派生),策略 / 风控算法保持 venue-neutral(只吃数值),
// 喂进去的数值来自「当前所」。
//
// 落地分两步,降低对实盘经济闸的风险:
//   - P2.1(本文件):建类型 + 把盈亏平衡公式收敛为单一归属;值 **1:1 镜像** 全局 cfg,
//     `BreakEven()` 与旧 `coherentMinFundingRate` 逐位等价(加锚定单测),**零行为变更**。
//   - P2.2:把 `FeeReserveRate` 改为实拉 `CostProfile`(2*SpotTaker + 2*FuturesTaker 四腿往返)、
//     `MaxEstimatedRate` 由该所 cap 派生(§6.1 方案 B),并加「派生值 vs 旧手填值 diff < ε」等价性锚定。
//
// 注:借币储备 borrowReserveRate **不在**本结构——它每币每刻按该所实拉小时利率实时算
// (liveBorrowReserveRate),不要折叠进 FeeReserveRate。
type EconomicParams struct {
	// SpotTaker/FuturesTaker:实拉单边 taker 费率(该所抵扣已计入)。P2.1 暂为 0(未接实拉),
	// P2.2 起由 CostProfile 填充并据此派生 FeeReserveRate。
	SpotTaker    float64
	FuturesTaker float64

	// FeeReserveRate:往返手续费预留。P2.1 镜像 cfg.StrategyConfig.FeeReserveRate;
	// P2.2 改为 2*SpotTaker + 2*FuturesTaker(开+平 × 现货+合约 四腿)。
	FeeReserveRate float64
	// ExitSpreadReserveRate / MinNetProfitRate:该所配置(后者对应 RiskConfig.MinNetProfitRate)。
	ExitSpreadReserveRate float64
	MinNetProfitRate      float64
	// MaxEstimatedRate:异常高费率粗筛上界。P2.1 镜像 cfg.StrategyConfig.MaxEstimatedFundingRate;
	// P2.2 由该所典型 cap 派生(逐币精筛仍交给 FundingSnapshot.RateCap / 撞限豁免)。
	MaxEstimatedRate float64
}

// BreakEven 返回「值得进入监控阶段」的最低预估资金费率(单周期对冲往返真实盈亏平衡线 + 健康净利门):
//
//	往返费用预留 + 退出价差预留 + 健康净利门
//
// 与旧 coherentMinFundingRate 逐位等价(各项同样做了下限 0 截断)。
func (p EconomicParams) BreakEven() float64 {
	return maxFloat(p.FeeReserveRate, 0) +
		maxFloat(p.ExitSpreadReserveRate, 0) +
		maxFloat(p.MinNetProfitRate, 0)
}

// economicParamsFromConfig 从全局 cfg 1:1 派生 EconomicParams(P2.1 默认路径,零行为变更)。
// P2.2 会在引擎 initialize 阶段用实拉 CostProfile 覆盖 FeeReserveRate / Spot/FuturesTaker / MaxEstimatedRate。
func economicParamsFromConfig(cfg EngineConfig) EconomicParams {
	return EconomicParams{
		SpotTaker:             cfg.HistoricalConfig.SpotTakerFeeRate,
		FuturesTaker:          cfg.HistoricalConfig.FuturesTakerFeeRate,
		FeeReserveRate:        cfg.StrategyConfig.FeeReserveRate,
		ExitSpreadReserveRate: cfg.StrategyConfig.ExitSpreadReserveRate,
		MinNetProfitRate:      cfg.RiskConfig.MinNetProfitRate,
		MaxEstimatedRate:      cfg.StrategyConfig.MaxEstimatedFundingRate,
	}
}

// deriveFeeReserveRate 由实拉**单边** taker 费率派生往返手续费预留:开 + 平 × 现货 + 合约 = 4 腿。
// 平台币抵扣(BNB)已计入实拉费率,故此处无需再单独处理(口径见 §6)。
func deriveFeeReserveRate(spotTaker, futuresTaker float64) float64 {
	return 2*spotTaker + 2*futuresTaker
}

// applyDerivedEconomicParams 在 initialize 拉到账户真实 CostProfile 后调用:按实拉单边 taker 费率把
// FeeReserveRate 重算为 4 腿往返(P2.2)。为避免「策略层读 cfg、引擎读 econ」两侧口径漂移,派生值
// 写回 e.cfg.StrategyConfig.FeeReserveRate(与既有把实拉 taker 费率写回 HistoricalConfig 同一模式),
// 再以此刷新 e.econ。
//
// fail-safe:开关关闭、或任一实拉 taker 费率 ≤ 0(拉取失败/异常)时,保持手填 FeeReserveRate 不变,
// 绝不把进场/净利闸塌成 0。
func (e *FundingRateSniperEngine) applyDerivedEconomicParams() {
	if e == nil {
		return
	}
	defer func() { e.econ = economicParamsFromConfig(e.cfg) }()

	if !e.cfg.StrategyConfig.AutoDeriveFeeReserveEnabled {
		return
	}
	spotTaker := e.cfg.HistoricalConfig.SpotTakerFeeRate
	futuresTaker := e.cfg.HistoricalConfig.FuturesTakerFeeRate
	if spotTaker <= 0 || futuresTaker <= 0 {
		if e.logger != nil {
			e.logger.Printf("[econ] 实拉 taker 费率不可用(spot=%.6f futures=%.6f),FeeReserveRate 保持手填 %.6f", spotTaker, futuresTaker, e.cfg.StrategyConfig.FeeReserveRate)
		}
		return
	}
	derived := deriveFeeReserveRate(spotTaker, futuresTaker)
	old := e.cfg.StrategyConfig.FeeReserveRate
	e.cfg.StrategyConfig.FeeReserveRate = derived
	if e.logger != nil && math.Abs(derived-old) > 1e-9 {
		e.logger.Printf("[econ] 按实拉费率派生 FeeReserveRate: %.6f → %.6f (spot_taker=%.6f futures_taker=%.6f);进场/净利经济闸已据实调整", old, derived, spotTaker, futuresTaker)
	}
}

// economicParams 返回引擎当前的经济闸参数。构造器会把 e.econ 实例化(FeeReserveRate>0),正常路径直接返回它;
// 对直接构造结构体、未走构造器的测试,则按需从 cfg 兜底派生,保证取值始终自洽(配置校验保证 FeeReserveRate>0)。
func (e *FundingRateSniperEngine) economicParams() EconomicParams {
	if e == nil {
		return EconomicParams{}
	}
	if e.econ.FeeReserveRate > 0 {
		return e.econ
	}
	return economicParamsFromConfig(e.cfg)
}
