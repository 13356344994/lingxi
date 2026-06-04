package app

import "strings"

// portfolioManager 把 God object(FundingRateSniperEngine)中「当前组合 / 受管敞口」这一族高内聚状态
// 收拢为独立、命名清晰的子结构(M-S2.3,R10/R21):持仓对冲、持仓/监控标签、逐币冷却、ExposureManager 锁。
//
// 它以匿名嵌入并入引擎,字段经提升(field promotion)对既有 ~40 处访问点完全透明——零改动、零行为变更。
// 字段仍由引擎的 platformRiskMu 守护:与风控/报告状态保持同一临界区的原子性不变。给 portfolioManager
// 一把独立锁会破坏这种跨状态原子性(R4 级竞争风险),因此显式留作后续受控步骤(M-S2.4),本步不拆锁。
//
// 纯查询方法(无锁;调用方须持有 platformRiskMu,或在单测中独占该结构)让组合状态可独立单测,
// 满足 README-S2「组合独立可测」。
type portfolioManager struct {
	activeOpenHedges      []OpenHedge
	currentPositions      []string
	currentMonitorTargets []string
	symbolCooldowns       map[string]symbolCooldownState
	exposureLocks         map[string]exposureLockState
}

// managedExposureSymbols 汇总当前所有受管敞口 symbol(持仓对冲 ∪ ExposureManager 锁),大写归一化。
// 它是「持仓期常驻监控 / 撤离判定该盯哪些 symbol」的 SSOT。纯函数,调用方须持有 platformRiskMu。
func (p *portfolioManager) managedExposureSymbols() map[string]struct{} {
	symbols := make(map[string]struct{}, len(p.activeOpenHedges)+len(p.exposureLocks))
	for _, hedge := range p.activeOpenHedges {
		if s := strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol)); s != "" {
			symbols[s] = struct{}{}
		}
	}
	for symbol := range p.exposureLocks {
		if s := strings.ToUpper(strings.TrimSpace(symbol)); s != "" {
			symbols[s] = struct{}{}
		}
	}
	return symbols
}

// hasNegativeCarryHedge 报告当前持仓对冲中是否存在负 carry(全仓杠杆借币空头)腿。
// 纯函数,调用方须持有 platformRiskMu。
func (p *portfolioManager) hasNegativeCarryHedge() bool {
	for i := range p.activeOpenHedges {
		if p.activeOpenHedges[i].carryDirection() == fundingCarryDirectionNegative {
			return true
		}
	}
	return false
}
