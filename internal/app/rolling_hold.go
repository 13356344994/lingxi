package app

import (
	"fmt"
	"sort"
	"strings"
)

// notionalTolerance 名义额比较容差(USDT):避免浮点连加在「恰好触顶」边界上误判。
const notionalTolerance = 1e-6

// rolling_hold.go 收拢「跨周期滚动持有」(README §16.4/§16.5,M4)的纯函数式决策核心:
// 续持判定(evaluateRollDecision)与后续的组合再平衡(rebalancePortfolio,M-S4.2)。
// 这些函数确定性、无网络、无引擎状态、无锁,可独立单测;由引擎在 RevalidateLead(:55)复核时调用。
//
// 设计要点(§16.4「续持判定阈值 ≠ 入场阈值」——省钱的核心):
//   - 新开必须覆盖完整往返 ≈ FeeReserveRate + ExitSpreadReserveRate + MinNetProfitRate ≈ 0.0055。
//   - 续持只需覆盖「多持一周期的边际成本」≈ 负向 carry 借币利息(正向 carry≈0)+ 持有期对冲漂移风险。
//   - 故用远低于入场门槛的 HoldRollThresholdRate(续持门槛)与 RollSafetyBufferRate(安全垫):
//     仅当「下期保守预期净资金费明显覆盖边际成本且为正」才续持,贴近 0 即果断平仓。

// rollDecisionInput 是续持判定的全部输入,均为「率」(无量纲),由调用方按持仓方向归一后传入。
type rollDecisionInput struct {
	Symbol string
	// ExpectedNextNetFundingRate 下期「保守」预期净资金费率,已按持仓 carry 方向归一为「对我有利为正」。
	// 必须用保守估计(规避 R7:premium/last rate 符号冲突时退回乐观 lastFundingRate);不确定时应取 0 或负,
	// 从而触发 drop——续持的举证责任在「证明下期仍明显盈利」,而非「无法证明不盈利就继续持有」。
	ExpectedNextNetFundingRate float64
	// MarginalHoldCostRate 多持一周期的边际成本率(≥0):负向 carry 为该周期借币利息率;正向 carry≈0。
	// 注意是「边际」——只算多持这一周期新增的成本,不含已沉没的入场往返成本(滚动续持正是为了省这部分)。
	MarginalHoldCostRate float64
	// HoldRollThresholdRate 续持门槛率:边际净 carry 须 ≥ 此值才续持(< 入场门槛,与边际持有成本自洽)。
	HoldRollThresholdRate float64
	// RollSafetyBufferRate 续持安全垫率:边际净 carry 须 > 此值(明显为正)才续持,避免在贴近 0 的薄边际上续持。
	RollSafetyBufferRate float64
}

// rollDecision 是续持判定结果。Keep=true 表示滚动续持;false 表示到期平仓、释放槽位。
type rollDecision struct {
	Keep bool
	// NetHoldCarryRate 边际净 carry 率 = ExpectedNextNetFundingRate − MarginalHoldCostRate,
	// 即「多持一周期」的预期净收益率,是判定与可观测性的核心量。
	NetHoldCarryRate float64
	Reason           string
}

// evaluateRollDecision 在结算前复核(RevalidateLead/:55)判定某持仓是否滚动续持。
//
// 续持需同时满足两道闸(任一不过即果断平仓,fail-safe 偏向退出):
//  1. 门槛闸:边际净 carry ≥ HoldRollThresholdRate(多持一周期至少要赚回边际持有成本之上的门槛);
//  2. 安全垫闸:边际净 carry > RollSafetyBufferRate(明显为正,不在贴近 0 的薄边际上硬续)。
//
// 纯函数:无副作用、无网络、确定性。负的边际成本被防御性夹为 0(成本不应为负)。
func evaluateRollDecision(in rollDecisionInput) rollDecision {
	holdCost := in.MarginalHoldCostRate
	if holdCost < 0 {
		holdCost = 0
	}
	netHoldCarry := in.ExpectedNextNetFundingRate - holdCost

	switch {
	case netHoldCarry < in.HoldRollThresholdRate:
		return rollDecision{
			Keep:             false,
			NetHoldCarryRate: netHoldCarry,
			Reason: fmt.Sprintf("边际净 carry %.6f < 续持门槛 %.6f（下期预期净资金费 %.6f − 边际持有成本 %.6f），到期平仓",
				netHoldCarry, in.HoldRollThresholdRate, in.ExpectedNextNetFundingRate, holdCost),
		}
	case netHoldCarry <= in.RollSafetyBufferRate:
		return rollDecision{
			Keep:             false,
			NetHoldCarryRate: netHoldCarry,
			Reason: fmt.Sprintf("边际净 carry %.6f 未明显超过安全垫 %.6f（薄边际，避免 churn 风险），到期平仓",
				netHoldCarry, in.RollSafetyBufferRate),
		}
	default:
		return rollDecision{
			Keep:             true,
			NetHoldCarryRate: netHoldCarry,
			Reason: fmt.Sprintf("边际净 carry %.6f ≥ 门槛 %.6f 且 > 安全垫 %.6f，滚动续持省往返成本",
				netHoldCarry, in.HoldRollThresholdRate, in.RollSafetyBufferRate),
		}
	}
}

// portfolioPosition 是组合再平衡里一个「位」的最小可比表示——已持仓(held)或新候选(candidate)通用。
type portfolioPosition struct {
	Symbol string
	// NetCarryRate 预期下期净 carry 率(无量纲),held 与 candidate 同口径可比:
	// held 用其续持后的边际净 carry(evaluateRollDecision.NetHoldCarryRate);candidate 用其预期净 carry。
	// 注意:用「毛」净 carry 比较,换仓的摩擦成本由 rebalanceLimits.HysteresisBandRate 这道滞后带承担。
	NetCarryRate float64
	// NotionalUSDT 该位的名义额(USDT),用于 L3 组合名义上限约束。
	NotionalUSDT float64
}

// rebalanceLimits 是组合再平衡的约束:L3 币数/名义上限 + 防 churn 滞后带。
type rebalanceLimits struct {
	// MaxSymbols 组合最大同时持仓币数(L3)。<=0 视为不限币数。
	MaxSymbols int
	// MaxNotionalUSDT 组合最大总名义(L3,灰度旋钮)。<=0 视为不限名义。
	MaxNotionalUSDT float64
	// HysteresisBandRate 换仓滞后带(率):仅当新候选净 carry 超过被替换持仓「> 此带宽」才换仓。
	// 由调用方计算 = (入场往返成本 + 退出往返成本) × RebalanceHysteresisCostMultiplier(§16.5)。
	HysteresisBandRate float64
}

// rebalancePlan 是再平衡结果:保留 / 丢弃(释放槽位) / 新增。三者 symbol 互斥,均大写归一。
type rebalancePlan struct {
	Keep []string
	Drop []string
	Add  []string
}

// rebalancePortfolio 在结算边界把「已续持持仓」与「新候选」放进同一排序,在 L3 上限内按净 carry 边际择优,
// 但用滞后带防 churn(§16.5):已持仓默认保留;新候选优先填空槽;槽位/名义触顶后,只有当某候选净 carry
// 超过「最弱持仓 + 滞后带」时才替换,否则保持持仓不动——避免被手续费来回摩擦磨光利润。
//
// 纯函数:确定性(按 NetCarryRate 降序、同分按 symbol 升序)、无副作用、无网络。调用方须保证传入的 candidates
// 已通过入场门槛(本函数只负责择优/防 churn/限额,不重复做入场经济性判定)。
func rebalancePortfolio(held, candidates []portfolioPosition, limits rebalanceLimits) rebalancePlan {
	// 当前保留集合(已续持持仓默认全保留),按确定性顺序。
	keep := normalizePositions(held)
	keptBySymbol := make(map[string]portfolioPosition, len(keep))
	keptOrder := make([]string, 0, len(keep))
	usedNotional := 0.0
	for _, p := range keep {
		keptBySymbol[p.Symbol] = p
		keptOrder = append(keptOrder, p.Symbol)
		usedNotional += p.NotionalUSDT
	}

	plan := rebalancePlan{Drop: []string{}, Add: []string{}}
	dropped := make(map[string]struct{})

	for _, c := range normalizePositions(candidates) {
		if _, already := keptBySymbol[c.Symbol]; already {
			continue // 已持有同 symbol,跳过(防重复)
		}
		symbolCount := len(keptBySymbol)
		// 1) 有空槽且名义放得下 → 直接新增。
		if (limits.MaxSymbols <= 0 || symbolCount < limits.MaxSymbols) && notionalFits(usedNotional+c.NotionalUSDT, limits.MaxNotionalUSDT) {
			keptBySymbol[c.Symbol] = c
			usedNotional += c.NotionalUSDT
			plan.Add = append(plan.Add, c.Symbol)
			continue
		}
		// 2) 槽位/名义触顶 → 滞后带替换:找当前最弱持仓,仅当候选净 carry 明显更优(> 滞后带)且替换后名义合规才换。
		weakSymbol, weak, ok := weakestKept(keptBySymbol)
		if !ok {
			continue
		}
		if c.NetCarryRate-weak.NetCarryRate <= limits.HysteresisBandRate {
			continue // 优势未超过滞后带 → 保持持仓不动(防 churn)
		}
		newNotional := usedNotional - weak.NotionalUSDT + c.NotionalUSDT
		if !notionalFits(newNotional, limits.MaxNotionalUSDT) {
			continue // 换仓后名义仍超顶 → 不换
		}
		delete(keptBySymbol, weakSymbol)
		dropped[weakSymbol] = struct{}{}
		plan.Drop = append(plan.Drop, weakSymbol)
		keptBySymbol[c.Symbol] = c
		plan.Add = append(plan.Add, c.Symbol)
		usedNotional = newNotional
	}

	// Keep = 原持仓中未被 drop 的,保持传入的确定性顺序。
	for _, symbol := range keptOrder {
		if _, gone := dropped[symbol]; !gone {
			plan.Keep = append(plan.Keep, symbol)
		}
	}
	sort.Strings(plan.Drop)
	sort.Strings(plan.Add)
	return plan
}

// normalizePositions 大写归一 symbol、丢弃空 symbol,并按 NetCarryRate 降序(同分按 symbol 升序)确定性排序。
func normalizePositions(in []portfolioPosition) []portfolioPosition {
	out := make([]portfolioPosition, 0, len(in))
	for _, p := range in {
		symbol := strings.ToUpper(strings.TrimSpace(p.Symbol))
		if symbol == "" {
			continue
		}
		p.Symbol = symbol
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NetCarryRate != out[j].NetCarryRate {
			return out[i].NetCarryRate > out[j].NetCarryRate
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// weakestKept 返回当前保留集合里净 carry 最低(同分 symbol 最大)的持仓,作为滞后带替换的候选被替换者。
func weakestKept(kept map[string]portfolioPosition) (string, portfolioPosition, bool) {
	var weakSymbol string
	var weak portfolioPosition
	found := false
	for symbol, p := range kept {
		if !found || p.NetCarryRate < weak.NetCarryRate || (p.NetCarryRate == weak.NetCarryRate && symbol > weakSymbol) {
			weakSymbol, weak, found = symbol, p, true
		}
	}
	return weakSymbol, weak, found
}

// notionalFits 报告给定总名义是否在上限内(limit<=0 表示不限)。
func notionalFits(total, limit float64) bool {
	if limit <= 0 {
		return true
	}
	return total <= limit+notionalTolerance
}
