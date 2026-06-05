package app

import (
	"fmt"
	"strings"
)

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

// portfolioL3Limits 是常驻组合的 L3 上限(来自 PortfolioConfig,§16.6):同时持仓币数 ≤ MaxSymbols 且
// 总名义 ≤ MaxNotionalUSDT。任一维度 <=0 表示该维度不约束(MaxNotionalUSDT=0 即灰度旋钮未启用组合名义上限)。
type portfolioL3Limits struct {
	MaxSymbols      int
	MaxNotionalUSDT float64
}

// heldSymbolSet 返回当前常驻组合实际持仓(activeOpenHedges)的大写 symbol 集合,跳过空 symbol。
// 与 managedExposureSymbols 的区别:后者并入 ExposureManager 锁,用于「该盯哪些 symbol」;本集合只含真实持仓,
// 是滚动持有 L3 币数/补位槽计算的 SSOT。纯查询,调用方须持有 platformRiskMu。
func (p *portfolioManager) heldSymbolSet() map[string]struct{} {
	set := make(map[string]struct{}, len(p.activeOpenHedges))
	for i := range p.activeOpenHedges {
		if s := strings.ToUpper(strings.TrimSpace(p.activeOpenHedges[i].Target.Symbol)); s != "" {
			set[s] = struct{}{}
		}
	}
	return set
}

// portfolioHeldSymbolCount 返回当前常驻组合的持仓币数(去重大写)。纯查询,调用方须持有 platformRiskMu。
func (p *portfolioManager) portfolioHeldSymbolCount() int {
	return len(p.heldSymbolSet())
}

// portfolioHeldNotionalUSDT 汇总当前常驻组合的总名义(L3 口径):每币取合约腿名义 HedgeQty×OpenFuturesAvgPrice,
// 即对冲仓位规模,与「10 币 × 1万 = 10W 总名义」的设计口径一致(§16.2.2)。纯查询,调用方须持有 platformRiskMu。
func (p *portfolioManager) portfolioHeldNotionalUSDT() float64 {
	total := 0.0
	for i := range p.activeOpenHedges {
		total += p.activeOpenHedges[i].HedgeQty * p.activeOpenHedges[i].OpenFuturesAvgPrice
	}
	return total
}

// portfolioClusterNotionalUSDT 按 L2 簇键(clusterKeyForSymbol,当前以 base 资产聚类)汇总当前常驻组合各簇的合约腿
// 名义(与 portfolioHeldNotionalUSDT 同口径)。供 admitByClusterCap 在开仓/补位侧做高相关集中度封顶。纯查询,
// 调用方须持有 platformRiskMu。
func (p *portfolioManager) portfolioClusterNotionalUSDT(clusterMap map[string]string) map[string]float64 {
	byCluster := make(map[string]float64, len(p.activeOpenHedges))
	for i := range p.activeOpenHedges {
		key := clusterKeyForSymbol(clusterMap, p.activeOpenHedges[i].Target.SpotBaseAsset, p.activeOpenHedges[i].Target.Symbol)
		if key == "" {
			continue
		}
		byCluster[key] += p.activeOpenHedges[i].HedgeQty * p.activeOpenHedges[i].OpenFuturesAvgPrice
	}
	return byCluster
}

// portfolioFreeSlots 返回 :57 找币补位的可用槽位数 = MaxSymbols − 当前持仓币数(下界 0)。MaxSymbols<=0 表示不限,
// 返回 -1 约定为「无币数上限」。纯查询,调用方须持有 platformRiskMu。
func (p *portfolioManager) portfolioFreeSlots(maxSymbols int) int {
	if maxSymbols <= 0 {
		return -1
	}
	free := maxSymbols - p.portfolioHeldSymbolCount()
	if free < 0 {
		return 0
	}
	return free
}

// admitCandidateToPortfolio 在不破坏 L3 上限的前提下,判定能否把名义为 candidateNotional 的某币纳入常驻组合。
// 候选已是持仓(如续持/同币加仓)时不增加币数占用,但仍校验名义维度。返回是否放行与拒绝原因。
// 纯查询,调用方须持有 platformRiskMu。
func (p *portfolioManager) admitCandidateToPortfolio(candidateSymbol string, candidateNotional float64, limits portfolioL3Limits) (bool, string) {
	symbol := strings.ToUpper(strings.TrimSpace(candidateSymbol))
	held := p.heldSymbolSet()
	_, already := held[symbol]
	if limits.MaxSymbols > 0 && !already && len(held) >= limits.MaxSymbols {
		return false, fmt.Sprintf("组合持仓币数已达 L3 上限 %d(当前 %d 币)", limits.MaxSymbols, len(held))
	}
	if limits.MaxNotionalUSDT > 0 {
		projected := p.portfolioHeldNotionalUSDT() + candidateNotional
		if projected > limits.MaxNotionalUSDT+notionalTolerance {
			return false, fmt.Sprintf("组合总名义 %.6f USDT 将超过 L3 上限 %.6f USDT", projected, limits.MaxNotionalUSDT)
		}
	}
	return true, ""
}
