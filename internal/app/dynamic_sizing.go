package app

import (
	"fmt"
	"math"
	"strings"
)

// dynamic_sizing.go 提供「市场驱动的单币容量定 size」纯函数(M3.1 / README §16.3):把多个流动性/风险天花板
// (盘口深度 / 近窗成交 / 未平仓 / 强平安全 / 绝对硬顶)取最小得到单币容量上限,再按「边际择优」(类分数 Kelly)
// 在容量内决定最终名义。纯函数:无网络、无引擎状态、确定性——市场量与份额闸由调用方先在 I/O 边界采集后传入。
//
// 本里程碑(M-S4.6)只交付纯函数与单测;接入开仓侧(替换 engine_cost_guard.go 的静态 PerSymbolQuoteMultiplier)
// 与「冲击成本入闸/计入 NetPnL」「L2 簇上限 / L3 sizing」分别留待 M-S4.7 / M-S4.8,且全程 behind DynamicSizingEnabled。

const (
	// defaultDynamicEdgeMin/Max 是边际择优(类分数 Kelly)的默认夹取区间(§16.3):预期净资金费率=入场阈值时
	// edgeScore=1(开 1 个基准单位);低至半阈值夹到下限、避免在薄边际上铺满;≥2 倍阈值封顶,不无限放大尾部风险。
	defaultDynamicEdgeMin = 0.5
	defaultDynamicEdgeMax = 2.0
	// defaultDepthCapDiscount 是 :57 抢费踩踏窗的深度容量打折(README §16.3 坑1)::55 看到的盘口名义到 :57 会蒸发,
	// 由 depth 推出的容量须重度打折,保守默认 0.5(大单仍须切片执行,见 P4 #16)。
	defaultDepthCapDiscount = 0.5
)

// dynamicPerSymbolCapInput 是单币容量定 size 的全部输入(均为名义额 USDT 或无量纲率)。各「原始市场量」<=0 表示
// 该维度数据缺失 → 不参与天花板 min;各份额闸 <=0 表示该维度不约束。所有可选维度缺省即「不收紧」,fail-safe 偏保守。
type dynamicPerSymbolCapInput struct {
	Symbol string

	// 各容量天花板的「原始市场量」(名义,USDT)。
	VisibleSpotNotionalUSDT    float64
	VisibleFuturesNotionalUSDT float64
	RecentSpotVolumeUSDT       float64
	RecentFuturesVolumeUSDT    float64
	OpenInterestNotionalUSDT   float64

	// 份额闸(StrategyConfig)× 分层乘子(symbolRiskProfile)。乘子 <=0 视为 1(不缩放)。
	MaxVisibleDepthShare float64
	MaxRecentVolumeShare float64
	MaxOpenInterestShare float64
	DepthShareMul        float64
	VolumeShareMul       float64
	OIShareMul           float64

	// DepthCapDiscount 是 :57 踩踏窗深度打折,取 (0,1];<=0 视为 defaultDepthCapDiscount(保守打折,非不打折)。
	DepthCapDiscount float64

	// L1 绝对硬顶与强平安全顶(USDT);<=0 表示该维度不约束。LiqSafeCap 暂为可选(R2 强平距离反推未实现,缺省即跳过)。
	HardCapUSDT    float64
	LiqSafeCapUSDT float64

	// 边际择优(类分数 Kelly):edgeScore = clamp(ExpectedNetFundingRate/EntryThresholdRate, EdgeMin, EdgeMax)。
	// EdgeMax<=0 或 EntryThresholdRate<=0 时关闭边际加权(edgeScore=1)。BaseUnitUSDT<=0 时退化为「直接用容量上限」。
	ExpectedNetFundingRate float64
	EntryThresholdRate     float64
	BaseUnitUSDT           float64
	EdgeMin                float64
	EdgeMax                float64
	MinNotionalUSDT        float64
}

// dynamicPerSymbolCapResult 是容量定 size 结果。Tradable=false 表示容量上限低于最小可下单名义(或无任何天花板),
// 此时 FinalNotionalUSDT=0,调用方应跳过该币。BindingCeiling 标出哪道天花板生效,供可观测与标定。
type dynamicPerSymbolCapResult struct {
	PerSymbolCapUSDT  float64
	FinalNotionalUSDT float64
	EdgeScore         float64
	BindingCeiling    string // depth|volume|oi|liqSafe|hardCap|none
	Tradable          bool
	Reason            string
}

// resolveDynamicPerSymbolCap 计算单币容量上限与最终下单名义。语义:
//   - perSymbolCap = min(depthCap, volumeCap, oiCap, liqSafeCap, hardCap)(仅取存在且为正的维度);
//   - edgeScore   = clamp(预期净资金费率 / 入场阈值, EdgeMin, EdgeMax);
//   - final       = clamp(BaseUnit × edgeScore, MinNotional, perSymbolCap)。
//
// 无任何天花板(缺市场数据且未设硬顶)→ 不可交易(fail-safe:拒绝在无容量约束下放大)。容量上限 < 最小可下单名义
// → 不可交易。纯函数、确定性。
func resolveDynamicPerSymbolCap(in dynamicPerSymbolCapInput) dynamicPerSymbolCapResult {
	depthDiscount := in.DepthCapDiscount
	if depthDiscount <= 0 {
		depthDiscount = defaultDepthCapDiscount
	}

	perSymbolCap := math.Inf(1)
	binding := "none"
	consider := func(name string, value float64) {
		if value <= 0 {
			return
		}
		if value < perSymbolCap {
			perSymbolCap = value
			binding = name
		}
	}

	if visibleMin := minPositiveFloat(in.VisibleSpotNotionalUSDT, in.VisibleFuturesNotionalUSDT); visibleMin > 0 && in.MaxVisibleDepthShare > 0 {
		consider("depth", visibleMin*in.MaxVisibleDepthShare*positiveMulOrOne(in.DepthShareMul)*depthDiscount)
	}
	if volMin := minPositiveFloat(in.RecentSpotVolumeUSDT, in.RecentFuturesVolumeUSDT); volMin > 0 && in.MaxRecentVolumeShare > 0 {
		consider("volume", volMin*in.MaxRecentVolumeShare*positiveMulOrOne(in.VolumeShareMul))
	}
	if in.OpenInterestNotionalUSDT > 0 && in.MaxOpenInterestShare > 0 {
		consider("oi", in.OpenInterestNotionalUSDT*in.MaxOpenInterestShare*positiveMulOrOne(in.OIShareMul))
	}
	consider("liqSafe", in.LiqSafeCapUSDT)
	consider("hardCap", in.HardCapUSDT)

	if math.IsInf(perSymbolCap, 1) {
		return dynamicPerSymbolCapResult{
			BindingCeiling: "none",
			Tradable:       false,
			Reason:         "无任何容量天花板(缺市场数据且未设绝对硬顶),拒绝在无容量约束下定 size",
		}
	}

	edgeScore := 1.0
	if in.EdgeMax > 0 && in.EntryThresholdRate > 0 {
		lo := maxFloat(in.EdgeMin, 0)
		edgeScore = clampFloat(in.ExpectedNetFundingRate/in.EntryThresholdRate, lo, in.EdgeMax)
	}

	desired := perSymbolCap
	if in.BaseUnitUSDT > 0 {
		desired = in.BaseUnitUSDT * edgeScore
	}
	final := math.Min(desired, perSymbolCap)
	if final < in.MinNotionalUSDT {
		final = in.MinNotionalUSDT
	}

	if final > perSymbolCap+notionalTolerance {
		// 最小可下单名义已超容量上限 → 该币吃不下最小单,不可交易。
		return dynamicPerSymbolCapResult{
			PerSymbolCapUSDT: perSymbolCap,
			EdgeScore:        edgeScore,
			BindingCeiling:   binding,
			Tradable:         false,
			Reason: fmt.Sprintf("容量上限 %.2f USDT(受 %s 约束)低于最小可下单名义 %.2f USDT,跳过",
				perSymbolCap, binding, in.MinNotionalUSDT),
		}
	}

	return dynamicPerSymbolCapResult{
		PerSymbolCapUSDT:  perSymbolCap,
		FinalNotionalUSDT: final,
		EdgeScore:         edgeScore,
		BindingCeiling:    binding,
		Tradable:          true,
		Reason: fmt.Sprintf("容量上限 %.2f USDT(受 %s 约束),edgeScore=%.3f → 下单名义 %.2f USDT",
			perSymbolCap, binding, edgeScore, final),
	}
}

// roundTripImpactReserveRate 估算一次「开+平」对冲往返的市场冲击成本率(占名义,M3.2 / README §16.3 实现要点2):
// 现货腿与合约腿各在开仓、平仓时吃一次冲击,故 = 2×spotImpact + 2×futuresImpact。流动性基数(成交额 / 可见深度名义)
// 越小、单笔名义越大,冲击率越高(撞 strategy.EstimateMarketImpactRate 的 0.0025 上限);流动性缺失时退回保守上限。
// 纯函数:确定性、无网络。用于入场闸扣除冲击(让薄边际 0.0055 不被冲击吃光),以及放大 size 的边际停止点。
func roundTripImpactReserveRate(notionalUSDT, spotLiquidityUSDT, futuresLiquidityUSDT float64) float64 {
	if notionalUSDT <= 0 {
		return 0
	}
	return 2*estimateMarketImpactRate(notionalUSDT, spotLiquidityUSDT) + 2*estimateMarketImpactRate(notionalUSDT, futuresLiquidityUSDT)
}

// impactAdjustedNetProfitRate 在基础净利率上扣除往返冲击储备——仅当 DynamicSizingEnabled 时生效(opt-in,默认关闭
// 对入场闸零影响)。返回(扣冲击后净利率, 本次冲击储备率)。这道扣除让入场闸把市场冲击纳入盈亏平衡(坑2:不扣冲击
// 就放大 size 只会放大隐性亏损)。纯函数。
func impactAdjustedNetProfitRate(baseNetProfitRate float64, dynamicSizingEnabled bool, notionalUSDT, spotLiquidityUSDT, futuresLiquidityUSDT float64) (float64, float64) {
	if !dynamicSizingEnabled {
		return baseNetProfitRate, 0
	}
	impact := roundTripImpactReserveRate(notionalUSDT, spotLiquidityUSDT, futuresLiquidityUSDT)
	return baseNetProfitRate - impact, impact
}

// resolveHardCapUSDT 实现 §16.7 文档化的单币绝对硬顶推导:显式配 PerSymbolHardCapUSDT 优先;否则当组合名义上限与
// 币数都已配时,均分为 MaxPortfolioNotionalUSDT / MaxPortfolioSymbols;两者皆缺则返回 0(无硬顶)。纯函数。
func resolveHardCapUSDT(perSymbolHardCapUSDT, maxPortfolioNotionalUSDT float64, maxPortfolioSymbols int) float64 {
	if perSymbolHardCapUSDT > 0 {
		return perSymbolHardCapUSDT
	}
	if maxPortfolioNotionalUSDT > 0 && maxPortfolioSymbols > 0 {
		return maxPortfolioNotionalUSDT / float64(maxPortfolioSymbols)
	}
	return 0
}

// clusterKeyForSymbol 返回某币的 L2 簇键(M3.3 / README §16.2.2「按 base 资产/板块聚类防高相关集中」)。当前以现货
// base 资产聚类作最小实现:同 base 资产视为同簇。这是 L2 簇划分的唯一注入点,后续可替换为板块/相关性映射而不动调用方。
func clusterKeyForSymbol(spotBaseAsset, symbol string) string {
	if k := strings.ToUpper(strings.TrimSpace(spotBaseAsset)); k != "" {
		return k
	}
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// admitByClusterCap 报告把 candidateNotionalUSDT 加入其所属簇后是否仍在 L2 簇上限内。clusterMaxUSDT<=0 表示不启用
// 簇上限(灰度旋钮未开,默认放行)。候选已在该簇内有持仓时累加(同簇加仓仍受簇上限约束)。返回是否放行与拒绝原因。纯函数。
func admitByClusterCap(clusterNotionalUSDT map[string]float64, candidateCluster string, candidateNotionalUSDT, clusterMaxUSDT float64) (bool, string) {
	if clusterMaxUSDT <= 0 {
		return true, ""
	}
	cluster := strings.ToUpper(strings.TrimSpace(candidateCluster))
	projected := clusterNotionalUSDT[cluster] + candidateNotionalUSDT
	if projected > clusterMaxUSDT+notionalTolerance {
		return false, fmt.Sprintf("簇 %s 名义 %.2f USDT 将超过 L2 簇上限 %.2f USDT", cluster, projected, clusterMaxUSDT)
	}
	return true, ""
}

// constrainedSizingNotionalUSDT 把动态容量上限(dynRes)与 L2 簇余量、L3 组合名义余量合成单币「可下单名义上限」(USDT,
// M-S4.8b / README §16.6)。语义:在 dynRes.FinalNotionalUSDT 上再被 L2 簇余量、L3 组合名义余量收紧;任一余量(含动态
// 容量本身)使上限低于最小可下单名义则该币不可纳入(返回 false,调用方跳过)。该上限由调用方与既有 L0 静态-容量数量
// 取 min——DynamicSizingEnabled 时只会更小、绝不更大(money-safe 首接;edge 放大尾部留待校准后另开)。纯函数。
func constrainedSizingNotionalUSDT(
	dynRes dynamicPerSymbolCapResult,
	clusterNotionalUSDT map[string]float64, clusterKey string, clusterMaxUSDT float64,
	heldNotionalUSDT, portfolioMaxNotionalUSDT float64,
	minNotionalUSDT float64,
) (float64, bool, string) {
	if !dynRes.Tradable {
		return 0, false, dynRes.Reason
	}
	capUSDT := dynRes.FinalNotionalUSDT
	binding := dynRes.BindingCeiling
	if clusterMaxUSDT > 0 {
		headroom := clusterMaxUSDT - clusterNotionalUSDT[strings.ToUpper(strings.TrimSpace(clusterKey))]
		if headroom < capUSDT {
			capUSDT, binding = headroom, "L2簇"
		}
	}
	if portfolioMaxNotionalUSDT > 0 {
		headroom := portfolioMaxNotionalUSDT - heldNotionalUSDT
		if headroom < capUSDT {
			capUSDT, binding = headroom, "L3组合"
		}
	}
	if capUSDT <= 0 || capUSDT+notionalTolerance < minNotionalUSDT {
		return 0, false, fmt.Sprintf("可下单名义 %.2f USDT(受 %s 约束)低于最小可下单名义 %.2f USDT,跳过", capUSDT, binding, minNotionalUSDT)
	}
	return capUSDT, true, fmt.Sprintf("可下单名义 %.2f USDT(受 %s 约束)", capUSDT, binding)
}

// positiveMulOrOne 把分层乘子归一:<=0(未配)视为 1.0,不缩放。
func positiveMulOrOne(mul float64) float64 {
	if mul <= 0 {
		return 1.0
	}
	return mul
}

// clampFloat 把 x 夹到 [lo, hi];lo>hi 时返回 lo(退化保护)。
func clampFloat(x, lo, hi float64) float64 {
	if hi < lo {
		return lo
	}
	return math.Max(lo, math.Min(x, hi))
}
