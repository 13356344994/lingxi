package app

import (
	"context"
	"strings"
	"time"
)

// rolling_hold_engine.go 收拢「跨周期滚动持有」的引擎侧(有状态)方法,与 rolling_hold.go 的纯函数式决策核心区分。
// 当前仅承载 keep 仓的「结算/会计」一侧(M-S4.4c-ii);持仓保留 / NextFundingTime 刷新 / 生命周期接线由后续
// live 循环接入(c-iii/c-iv)负责。全部 behind EnableRollingHold,接线前不影响单周期路径。

// settleKeptPositions 为滚动续持(keep)的持仓发射「funding-only 结算」并入账:
//   - 只记本周期资金费(异步实收)与本周期真实借币利息,不平仓、不重计开仓费/平仓盈亏(均在开仓周期已入账);
//   - 按 pre-funding NetPnL(= −本周期借币)推进当日已实现盈亏(与单周期同口径,funding 实收后由 worker 补 delta);
//   - 调度异步资金费 worker,并标记 RollingHoldSymbols 放行 keep 仓「开仓前已有持仓」的资金费归因(M-S4.4b)。
//
// 本方法只负责 keep 仓的「结算/会计」一侧:不动 currentPositions/台账/生命周期——持仓的保留与各币 NextFundingTime
// 刷新由 live 循环接线(c-iii)负责。故 behind EnableRollingHold 且接线前不会被调用。返回构造的 funding-only 结算。
func (e *FundingRateSniperEngine) settleKeptPositions(keepHedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingTime time.Time) (CycleSettlement, error) {
	if e == nil || len(keepHedges) == 0 {
		return CycleSettlement{}, nil
	}

	symbols := make([]string, 0, len(keepHedges))
	expectedBySymbol := make(map[string]float64, len(keepHedges))
	borrowBySymbol := make(map[string]float64, len(keepHedges))
	// keep 周期借币按「真实经过时长」计息(决策3):用本次复核时刻做结算锚点,正向 carry 应为 0。
	borrowAnchor := time.Now().UTC()
	for _, hedge := range keepHedges {
		symbol := strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol))
		if symbol == "" {
			continue
		}
		if _, seen := expectedBySymbol[symbol]; !seen {
			symbols = append(symbols, symbol)
		}
		expectedBySymbol[symbol] += estimateExpectedFundingIncome(hedge)
		borrowBySymbol[symbol] += e.settlementBorrowCost(hedge, borrowAnchor)
	}

	settlement := buildFundingOnlySettlement(fundingOnlySettlementInput{
		RunID:                       e.runID,
		CycleID:                     e.currentCycleID,
		Symbols:                     symbols,
		ExpectedFundingBySymbol:     expectedBySymbol,
		BorrowCostThisCycleBySymbol: borrowBySymbol,
		FundingTimePoint:            fundingTime,
		BeforeSnapshot:              beforeSnapshot,
	})
	settlement.TraceRecordMetadata = e.recordMetadata()

	if e.reportStore != nil {
		if err := e.reportStore.AppendSettlement(settlement); err != nil && e.logger != nil {
			e.logger.Printf("keep 仓 funding-only 结算落盘失败: %v", err)
		}
	}
	// pre-funding 入账(与单周期 close 路径一致):funding 实收后由 worker 补 deltaFunding 修正到真实净利。
	if err := e.noteDailySettlement(settlement.NetPnL); err != nil {
		return CycleSettlement{}, err
	}
	e.scheduleAsyncFundingSettlementWithRoll(settlement, keepHedges, symbols)
	return settlement, nil
}

// residentClocks 读取常驻组合各持仓的逐币 funding 时钟(symbol + NextFundingTime),供 NextPortfolioWake 计算
// 下一次「复核(:55)或新开(:57)」唤醒点。无持仓(或未填 NextFundingTime)时返回空 → NextPortfolioWake 退化为
// 全局 :57 新开唤醒。持有 platformRiskMu 读取 activeOpenHedges,保持与风控/报告状态同一临界区。
func (e *FundingRateSniperEngine) residentClocks() []symbolFundingClock {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	clocks := make([]symbolFundingClock, 0, len(e.activeOpenHedges))
	for i := range e.activeOpenHedges {
		symbol := strings.ToUpper(strings.TrimSpace(e.activeOpenHedges[i].Target.Symbol))
		if symbol == "" {
			continue
		}
		clocks = append(clocks, symbolFundingClock{
			Symbol:          symbol,
			NextFundingTime: e.activeOpenHedges[i].Target.NextFundingTime,
		})
	}
	return clocks
}

// collectRollFundingSignals 为续持复核拉取持仓币的「保守预测费率」,口径与开仓 scan 完全一致(fetchPremiumIndex +
// estimateScanFundingSignal,R7 符号冲突保守化),使续持门槛(HoldRollThresholdRate)与入场门槛可比(方案 3)。
// 返回 symbol(大写)→ 估计费率;无持仓时返回空 map。
func (e *FundingRateSniperEngine) collectRollFundingSignals(ctx context.Context, heldSymbols map[string]struct{}) (map[string]float64, error) {
	rates := make(map[string]float64, len(heldSymbols))
	if len(heldSymbols) == 0 {
		return rates, nil
	}
	items, err := e.fetchPremiumIndex(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if _, ok := heldSymbols[symbol]; !ok {
			continue
		}
		estimatedRate, _, _, _, _ := estimateScanFundingSignal(item, e.cfg.RiskConfig.ConservativeFundingOnSignConflictEnabled)
		rates[symbol] = estimatedRate
	}
	return rates, nil
}

// marginalHoldCostRates 计算各持仓「多持一个 funding 周期」的边际成本率:正向 carry 无借币 → 0;负向 carry 取
// 该资产时薪借币利率 × 下一 funding 间隔小时数(撞利率阶梯前的近似),除以名义即成本率。供 buildRollDecisionInputs。
func (e *FundingRateSniperEngine) marginalHoldCostRates(held []OpenHedge) map[string]float64 {
	rates := make(map[string]float64, len(held))
	for _, hedge := range held {
		symbol := strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol))
		if symbol == "" {
			continue
		}
		if hedge.carryDirection() != fundingCarryDirectionNegative {
			rates[symbol] = 0
			continue
		}
		rates[symbol] = e.hourlyBorrowInterestRateForAsset(hedge.Target.SpotBaseAsset) * defaultFundingIntervalHours
	}
	return rates
}

// defaultFundingIntervalHours 是边际持有成本估算所用的 funding 间隔保守默认(8h)。台账只持久化单点 NextFundingTime、
// 不含间隔,逐币真实间隔(4h/8h/可变)的标定留作后续;8h 对借币成本是偏大(保守)估计,符合 fail-safe 偏向平仓。
const defaultFundingIntervalHours = 8.0

// runRollingPortfolioCycle 是滚动持有(EnableRollingHold)下的常驻组合编排(M-S4.4c-iii):与单周期 runOneCycle
// 不同,它在迭代间保留常驻组合——复核窗口对到期持仓做 keep/drop、对空槽做新开补位。
//
// 接线分块推进。本骨架阶段(c-iii-1):尚无常驻持仓时退化为「仅新开」一轮,等价 runOneCycle;
// 「复核→keep/drop+settleKeptPositions+保留 keep」(c-iii-3)与「:57 空槽 rebalancePortfolio 补位」(c-iii-4)
// 在后续接入这里。故启用 flag 但无常驻持仓时,行为与单周期完全一致。
func (e *FundingRateSniperEngine) runRollingPortfolioCycle(ctx context.Context, wakeAt time.Time) error {
	return e.runOneCycle(ctx, wakeAt)
}
