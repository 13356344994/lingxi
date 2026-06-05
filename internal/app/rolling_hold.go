package app

import "fmt"

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
