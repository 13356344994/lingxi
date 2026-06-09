package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// residentOpenNoChurnBand 是开仓补位阶段喂给 rebalancePortfolio 的换仓滞后带:取 +Inf,使「已持常驻仓」在补位
// 阶段绝不被新候选替换(churn 抑制最强档)——补位只填空槽(plan.Add 受 L3 币数/名义双重封顶),已持仓的 keep/drop
// 仅由到期复核(evaluateRollDecision)决定。常驻仓「在 open 阶段被更优候选主动换出」属独立特性,经济性待 M-S4.5
// 单周期 vs 滚动续持回归验证后另设里程碑接入,本步不做(避免在未验证收益前关闭刚被判 keep 的仓位)。
const residentOpenNoChurnBand = math.MaxFloat64

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
		CycleID:                     e.cycleIDSnapshot(),
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

// setMarketNextFundings 缓存本轮扫描观测到的黄金币种下次结算时间(P2.4),供下一轮逐结算唤醒计算。
// 防御性复制入参,避免与调用方切片别名共享。
func (e *FundingRateSniperEngine) setMarketNextFundings(fundings []time.Time) {
	if e == nil {
		return
	}
	cp := append([]time.Time(nil), fundings...)
	e.platformRiskMu.Lock()
	e.marketNextFundings = cp
	e.platformRiskMu.Unlock()
}

// knownMarketNextFundings 返回上一轮缓存的市场下次结算时间副本(P2.4 唤醒计算用)。冷启动为空。
func (e *FundingRateSniperEngine) knownMarketNextFundings() []time.Time {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return append([]time.Time(nil), e.marketNextFundings...)
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
func (e *FundingRateSniperEngine) collectRollFundingSignals(ctx context.Context, heldSymbols map[string]struct{}) (map[string]float64, map[string]time.Time, error) {
	rates := make(map[string]float64, len(heldSymbols))
	nextFundings := make(map[string]time.Time, len(heldSymbols))
	if len(heldSymbols) == 0 {
		return rates, nextFundings, nil
	}
	items, err := e.fetchPremiumIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if _, ok := heldSymbols[symbol]; !ok {
			continue
		}
		estimatedRate, _, _, _, _ := estimateScanFundingSignal(item, e.liveFundingSignalConfig())
		rates[symbol] = estimatedRate
		// 记录交易所真实下次结算时间(逐币 4h/8h),供续持后按真实间隔推进时钟(修 FIX_PLAN #8),
		// 而非一律 +8h(4h 币会被推迟到错误时点)。
		if item.NextFundingTime > 0 {
			nextFundings[symbol] = time.UnixMilli(item.NextFundingTime).UTC()
		}
	}
	return rates, nextFundings, nil
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

// openResidentPositions 执行滚动持有的新开:复用「扫描→监控→开仓」段(openCyclePositions)开仓,但开后**不当轮平仓**,
// 而是保留为常驻组合——写入持仓标签 + activeOpenHedges + 本地台账(各币 NextFundingTime 已由 scan 注入 Target,
// 供 residentClocks 驱动后续 :55 复核决定 keep/drop)。返回本轮新开的常驻对冲;无可开仓时返回 (nil,nil)。
func (e *FundingRateSniperEngine) openResidentPositions(ctx context.Context, wakeAt time.Time) ([]OpenHedge, error) {
	openHedges, _, opened, err := e.openCyclePositions(ctx, wakeAt, e.admitResidentFillTargets)
	if err != nil {
		return nil, err
	}
	if !opened {
		return nil, nil
	}
	// 注:内存组合与台账已由开仓协议(executeSequence → setCurrentPositionsFromHedges / syncPersistedOpenHedges,
	// 均经 mergeResidentBaselineWith 并入既有常驻仓)落定为「既有 keep ∪ 本轮新开」的完整组合。此处不再用
	// 「仅新开」整体覆盖(那正是 #1 组合抹除的根因);仅推进生命周期与面板。
	_ = e.advanceLifecycleStage(LifecycleStageCloseMonitoring, fmt.Sprintf("滚动持有:新开 %d 仓保留为常驻组合", len(openHedges)))
	e.runtimeReporter.EmitWithReason(StageCloseMonitor, len(openHedges), fmt.Sprintf("滚动持有:常驻持仓数=%d", len(openHedges)))
	return openHedges, nil
}

// runRollingPortfolioCycle 是滚动持有(EnableRollingHold)下的常驻组合编排(M-S4.4c-iii):与单周期 runOneCycle
// 不同,它在迭代间保留常驻组合——复核窗口对到期持仓做 keep/drop、对空槽做新开补位。
//
// 接线分块推进。本骨架阶段(c-iii-1):尚无常驻持仓时退化为「仅新开」一轮,等价 runOneCycle;
// 「复核→keep/drop+settleKeptPositions+保留 keep」(c-iii-3)与「:57 空槽 rebalancePortfolio 补位」(c-iii-4)
// 在后续接入这里。故启用 flag 但无常驻持仓时,行为与单周期完全一致。
func (e *FundingRateSniperEngine) runRollingPortfolioCycle(ctx context.Context, wakeAt time.Time) error {
	due, untouched := classifyResidentsForRevalidation(e.snapshotActiveOpenHedges(), e.scheduler(), e.serverNow())
	if len(due) > 0 {
		if err := e.processResidentRevalidation(ctx, due, untouched); err != nil {
			return err
		}
	}

	// 空槽补位(L3):未满则尝试新开;开仓时机由 openCyclePositions 内的 ScanEligible 闸把关(非 :57 窗口扫描为空 → no-op)。
	if e.residentFreeSlots() != 0 {
		if _, err := e.openResidentPositions(ctx, wakeAt); err != nil {
			return err
		}
	}
	return nil
}

// admitResidentFillTargets 是开仓补位的目标筛选钩子(c-iii-4):把当前常驻持仓(held)与本轮有效候选(candidates)
// 喂给 rebalancePortfolio,在 L3 币数(MaxPortfolioSymbols)+ 名义(MaxPortfolioNotionalUSDT)双重上限内择优填空槽。
// 滞后带取 +Inf(residentOpenNoChurnBand)→ 已持仓绝不被换出,故 plan.Drop 恒空、plan.Add 即「可纳入空槽的新候选」。
// 返回 validTargets 中被 plan.Add 选中的子集(保持入参顺序,即 scan 评分序),空集表示无空槽/名义触顶 → 上层优雅收尾。
func (e *FundingRateSniperEngine) admitResidentFillTargets(validTargets []*MonitoredTarget) []*MonitoredTarget {
	if e == nil || len(validTargets) == 0 {
		return validTargets
	}
	candidates := make([]portfolioPosition, 0, len(validTargets))
	bySymbol := make(map[string]*MonitoredTarget, len(validTargets))
	for _, t := range validTargets {
		symbol := strings.ToUpper(strings.TrimSpace(t.Candidate.Symbol))
		if symbol == "" {
			continue
		}
		candidates = append(candidates, portfolioPosition{
			Symbol:       symbol,
			NetCarryRate: t.Candidate.EstimatedRate,
			NotionalUSDT: t.Candidate.OrderQuantity * t.Candidate.MarkPrice,
		})
		if _, dup := bySymbol[symbol]; !dup {
			bySymbol[symbol] = t
		}
	}
	plan := rebalancePortfolio(e.residentPortfolioPositions(), candidates, rebalanceLimits{
		MaxSymbols:         e.cfg.PortfolioConfig.MaxPortfolioSymbols,
		MaxNotionalUSDT:    e.effectiveMaxPortfolioNotionalUSDT(),
		HysteresisBandRate: residentOpenNoChurnBand,
	})
	admitSet := make(map[string]struct{}, len(plan.Add))
	for _, symbol := range plan.Add {
		admitSet[symbol] = struct{}{}
	}
	admitted := make([]*MonitoredTarget, 0, len(admitSet))
	for _, t := range validTargets {
		symbol := strings.ToUpper(strings.TrimSpace(t.Candidate.Symbol))
		if _, ok := admitSet[symbol]; ok {
			admitted = append(admitted, t)
		}
	}
	return admitted
}

// residentPortfolioPositions 把当前常驻持仓快照成 rebalancePortfolio 的 held 输入:名义取合约腿 HedgeQty×开仓均价
// (与 portfolioHeldNotionalUSDT 同口径,供 L3 名义封顶累加);NetCarryRate 在补位阶段无意义(滞后带 +Inf 下已持仓
// 恒不被换出),取 0 占位。持 platformRiskMu 读取 activeOpenHedges。
func (e *FundingRateSniperEngine) residentPortfolioPositions() []portfolioPosition {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	positions := make([]portfolioPosition, 0, len(e.activeOpenHedges))
	for i := range e.activeOpenHedges {
		symbol := strings.ToUpper(strings.TrimSpace(e.activeOpenHedges[i].Target.Symbol))
		if symbol == "" {
			continue
		}
		positions = append(positions, portfolioPosition{
			Symbol:       symbol,
			NetCarryRate: 0,
			NotionalUSDT: e.activeOpenHedges[i].HedgeQty * e.activeOpenHedges[i].OpenFuturesAvgPrice,
		})
	}
	return positions
}

// residentFreeSlots 返回当前组合的 L3 空槽数(-1 表示不限币数);持 platformRiskMu 读取。
func (e *FundingRateSniperEngine) residentFreeSlots() int {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.portfolioFreeSlots(e.cfg.PortfolioConfig.MaxPortfolioSymbols)
}

// hedgeSymbolSet 返回一组持仓的大写 symbol 集合(跳过空 symbol)。
func hedgeSymbolSet(hedges []OpenHedge) map[string]struct{} {
	set := make(map[string]struct{}, len(hedges))
	for _, h := range hedges {
		if s := strings.ToUpper(strings.TrimSpace(h.Target.Symbol)); s != "" {
			set[s] = struct{}{}
		}
	}
	return set
}

func symbolSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for s := range set {
		keys = append(keys, s)
	}
	return keys
}

// snapshotActiveOpenHedges 在 platformRiskMu 下复制当前常驻组合,供编排在临界区外安全遍历。
func (e *FundingRateSniperEngine) snapshotActiveOpenHedges() []OpenHedge {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return append([]OpenHedge(nil), e.activeOpenHedges...)
}

// classifyResidentsForRevalidation 把常驻持仓按 funding 时钟分为 due(已落入复核窗 [:55,:00) 或 funding 已过、
// 待结算)与 untouched(本轮不动)。纯函数:确定性、无副作用。
func classifyResidentsForRevalidation(residents []OpenHedge, sched cycleScheduler, now time.Time) (due, untouched []OpenHedge) {
	for _, h := range residents {
		nft := h.Target.NextFundingTime
		fundingPassed := !nft.IsZero() && !now.Before(nft)
		if sched.RevalidateDue(now, nft) || fundingPassed {
			due = append(due, h)
		} else {
			untouched = append(untouched, h)
		}
	}
	return due, untouched
}

// advanceResidentFundingClocks 在结算成功后把指定持仓(keep/新仓)的 NextFundingTime 推进一个 funding 间隔
// (保守默认 8h),并重持久化台账——避免本周期已处理的持仓在下一次唤醒被重复判为「到期」。持 platformRiskMu 改写。
func (e *FundingRateSniperEngine) advanceResidentFundingClocks(advanced []OpenHedge, nextFundingBySymbol map[string]time.Time) error {
	advancedSet := hedgeSymbolSet(advanced)
	if len(advancedSet) == 0 {
		return nil
	}
	now := time.Now().UTC()
	settledThroughMs := now.UnixMilli()
	e.platformRiskMu.Lock()
	for i := range e.activeOpenHedges {
		symbol := strings.ToUpper(strings.TrimSpace(e.activeOpenHedges[i].Target.Symbol))
		if _, ok := advancedSet[symbol]; !ok {
			continue
		}
		if nft := e.activeOpenHedges[i].Target.NextFundingTime; !nft.IsZero() {
			// 修 FIX_PLAN #8:优先用交易所真实下次结算时间(逐币 4h/8h);仅当交易所尚未翻进下一个结算
			// (real ≤ now,罕见的结算后短暂窗口)时,才退回保守 +8h(与原行为一致,下一轮复核会自校正)。
			if real, ok := nextFundingBySymbol[symbol]; ok && real.After(now) {
				e.activeOpenHedges[i].Target.NextFundingTime = real
			} else {
				e.activeOpenHedges[i].Target.NextFundingTime = nft.Add(defaultFundingIntervalHours * time.Hour)
			}
		}
		// 借币计息锚点推进到本次结算时点(修 FIX_PLAN #2):下一轮 keep 结算 / 最终平仓只计此后的增量利息。
		// 仅影响负 carry 计息口径;正 carry 的 settlementBorrowCost 恒返回 0,设此字段无副作用。
		e.activeOpenHedges[i].BorrowSettledThroughMs = settledThroughMs
	}
	hedgesCopy := append([]OpenHedge(nil), e.activeOpenHedges...)
	e.platformRiskMu.Unlock()
	return e.syncPersistedOpenHedges(hedgesCopy)
}

// processResidentRevalidation 对到期常驻持仓执行 keep/drop:续持判定 → 平 drop(保留 keep ∪ 未到期、放行 drop 资金费
// 归因)、结算 keep(funding-only)→ 推进 keep funding 时钟。drop 平仓的落盘/日亏/R8 投影刹车与单周期同口径。
func (e *FundingRateSniperEngine) processResidentRevalidation(ctx context.Context, due, untouched []OpenHedge) error {
	heldSymbols := hedgeSymbolSet(due)
	rates, nextFundings, err := e.collectRollFundingSignals(ctx, heldSymbols)
	if err != nil {
		return err
	}
	costs := e.marginalHoldCostRates(due)
	inputs := buildRollDecisionInputs(due, rates, costs, e.cfg.PortfolioConfig)
	keepSymbols := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		if evaluateRollDecision(in).Keep {
			keepSymbols[in.Symbol] = struct{}{}
		}
	}
	keep, drop := partitionHedgesByRoll(due, keepSymbols)

	// 常驻仓无「开仓前快照」,平仓/结算的账户口径用一份当下快照,其 FuturesPositionAmounts 含持仓量。
	beforeSnapshot, err := e.captureCycleAccountSnapshotFromHedges(ctx, due)
	if err != nil {
		return err
	}

	retained := append(append([]OpenHedge(nil), keep...), untouched...)
	if len(drop) > 0 {
		dropSymbols := hedgeSymbolSet(drop)
		closeResult, closeErr := e.ensureCloseProtocol().ExecuteSequence(ctx, drop, beforeSnapshot, retained, dropSymbols)
		if closeErr != nil {
			return closeErr
		}
		e.emitDomainEvents(closeResult.ProtocolResult.Events)
		settlement := closeResult.Settlement
		if e.reportStore != nil {
			if appendErr := e.reportStore.AppendSettlement(settlement); appendErr != nil && e.logger != nil {
				e.logger.Printf("滚动持有 drop 平仓结算落盘失败: %v", appendErr)
			}
		}
		if noteErr := e.noteDailySettlement(settlement.NetPnL); noteErr != nil {
			return noteErr
		}
		e.scheduleAsyncFundingSettlementWithRoll(settlement, drop, symbolSetKeys(dropSymbols))
		if brakeErr := e.checkCycleLossProjectionBrake(settlement); brakeErr != nil {
			return brakeErr
		}
		if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
			return fuseErr
		}
	} else {
		// 无 drop:不平仓,但仍把常驻态收敛到 retained(keep ∪ 未到期),口径与有 drop 路径一致。
		e.reconcileResidentPositionsAfterClose(retained)
		if ledgerErr := e.reconcileResidentLedgerAfterClose(beforeSnapshot, nil, retained); ledgerErr != nil {
			return ledgerErr
		}
	}

	if len(keep) > 0 {
		fundingTime := earliestFundingTimeFromHedges(keep)
		if _, settleErr := e.settleKeptPositions(keep, beforeSnapshot, fundingTime); settleErr != nil {
			return settleErr
		}
		if advanceErr := e.advanceResidentFundingClocks(keep, nextFundings); advanceErr != nil {
			return advanceErr
		}
	}
	return nil
}
