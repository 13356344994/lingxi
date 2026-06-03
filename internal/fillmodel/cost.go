// Package fillmodel 抽象「成交/费用」模型，供回测与实盘共用同一套口径，
// 从结构上根除「回测和实盘对不上」的漂移问题（Gate 3）。
//
// 当前阶段先落地 CostModel 中最关键的一块：残腿 / 部分成交 / 单腿拒单的期望成本
// （Gate 2 H2 的 monthlyResidualFillCost 升级为可注入的接口实现）。后续会把实盘
// open/close 协议与回测主路径的滑点/冲击/费用/借币口径逐步并入本包。
package fillmodel

import "math"

// RoundTrip 描述一次往返交易（开仓+平仓）在成本模型眼中的输入快照。
// 所有 *Rate 均为相对名义的比率，*Notional 均为 USDT 名义。
type RoundTrip struct {
	OpenNotional      float64
	CloseNotional     float64
	OpenFrictionRate  float64
	CloseFrictionRate float64
	OpenBasisRate     float64
	CloseBasisRate    float64
}

// ResidualParams 控制残腿 / 部分成交 haircut 模型的保守程度。
type ResidualParams struct {
	Enabled                  bool
	ExpectedLegFillRatio     float64
	LegRejectProbability     float64
	EmergencyHedgeMultiplier float64
}

// ResidualBreakdown 拆解一笔往返交易的残腿 / 部分成交期望成本。
type ResidualBreakdown struct {
	HaircutUSDT         float64
	PartialFillCostUSDT float64
	LegRejectCostUSDT   float64
	OpenResidualUSDT    float64
	CloseResidualUSDT   float64
}

// CostModel 抽象成本计提。回测与实盘各自注入实现，但共享同一套口径，
// 从而保证回测与实盘对得上。
type CostModel interface {
	// ResidualFillCost 计提一次往返交易因「无法 100% 同量成交 / 单腿被拒」而
	// 产生的残腿期望成本。
	ResidualFillCost(rt RoundTrip) ResidualBreakdown
}

// ResidualCostModel 是基于保守、确定性期望成本近似的 CostModel 默认实现。
type ResidualCostModel struct {
	Params ResidualParams
}

// ResidualFillCost 实现 CostModel。
func (m ResidualCostModel) ResidualFillCost(rt RoundTrip) ResidualBreakdown {
	return ResidualCost(m.Params, rt)
}

// ResidualCost 用保守、确定性的期望成本近似，刻画「双腿无法 100% 同量成交」与
// 「单腿被拒后被迫紧急对冲」两类残腿风险，从而消除「完美对冲、100% 成交、零残腿」
// 的乐观假设。它是纯函数，可脱离网络单测。
//
// 模型分两部分：
//  1. 部分成交残腿：每条腿期望只成交 ExpectedLegFillRatio，未匹配名义需在更差价格
//     紧急补腿，成本 = 残腿名义 ×(单边摩擦率 × 紧急乘数 + |基差|)。
//  2. 单腿拒单：以 LegRejectProbability 的概率某条腿完全未成交，已成交腿被迫紧急
//     平掉，开仓、平仓两侧各计提一次整条腿名义的期望成本。
func ResidualCost(params ResidualParams, rt RoundTrip) ResidualBreakdown {
	if !params.Enabled {
		return ResidualBreakdown{}
	}
	fillRatio := params.ExpectedLegFillRatio
	if fillRatio <= 0 || fillRatio > 1 {
		fillRatio = 1
	}
	rejectProb := params.LegRejectProbability
	if rejectProb < 0 {
		rejectProb = 0
	}
	if rejectProb > 1 {
		rejectProb = 1
	}
	emgMult := params.EmergencyHedgeMultiplier
	if emgMult < 1 {
		emgMult = 1
	}
	openNotional := math.Max(0, rt.OpenNotional)
	closeNotional := math.Max(0, rt.CloseNotional)
	openFrictionRate := math.Max(0, rt.OpenFrictionRate)
	closeFrictionRate := math.Max(0, rt.CloseFrictionRate)
	openBasis := math.Abs(rt.OpenBasisRate)
	closeBasis := math.Abs(rt.CloseBasisRate)

	openResidual := openNotional * (1 - fillRatio)
	closeResidual := closeNotional * (1 - fillRatio)
	partialFill := emgMult*(openResidual*openFrictionRate+closeResidual*closeFrictionRate) +
		openResidual*openBasis + closeResidual*closeBasis
	legReject := rejectProb * (openNotional*(emgMult*openFrictionRate+openBasis) +
		closeNotional*(emgMult*closeFrictionRate+closeBasis))
	return ResidualBreakdown{
		HaircutUSDT:         partialFill + legReject,
		PartialFillCostUSDT: partialFill,
		LegRejectCostUSDT:   legReject,
		OpenResidualUSDT:    openResidual,
		CloseResidualUSDT:   closeResidual,
	}
}
