package app

import (
	"context"
	"fmt"
	"strings"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
)

// cross_venue_rotation_adapter.go 跨所 perp-perp 执行器与统一轮动 runner 的适配层(路线 B)。

// SetRotationManaged 标记统一轮动 runner 已绑定;tick 不再独立 open/manage(防双循环)。
func (e *CrossVenueExecutor) SetRotationManaged(managed bool) {
	if e == nil {
		return
	}
	e.rotationManaged.Store(managed)
}

// RotationManaged 报告是否由统一轮动 runner 接管 open/manage/reconcile。
func (e *CrossVenueExecutor) RotationManaged() bool {
	if e == nil {
		return false
	}
	return e.rotationManaged.Load()
}

// ReconcileOnStart 启动对账(薄包装,供统一 runner reconcileFn 调用)。
func (e *CrossVenueExecutor) ReconcileOnStart(ctx context.Context, live bool) {
	if e == nil {
		return
	}
	e.reconcileOnStart(ctx, live)
}

// ManageOpenPositions 管理在场持仓(薄包装,供统一 runner manageFn 调用)。
func (e *CrossVenueExecutor) ManageOpenPositions(ctx context.Context, live bool) {
	if e == nil {
		return
	}
	e.manageOpenPositions(ctx, live)
}

// crossVenuePositionOpportunityID 返回持仓的统一机会 ID;旧台账无 OpportunityID 时按 EntrySnapshotID 稳定派生。
func crossVenuePositionOpportunityID(pos CrossVenuePosition) string {
	if id := strings.TrimSpace(pos.OpportunityID); id != "" {
		return id
	}
	return unifiedOpportunityID(UnifiedStrategyPerpPerp, pos.Symbol, pos.EntrySnapshotID, pos.HighVenue, pos.LowVenue)
}

func parseUnifiedOpportunitySnapshotID(id string) string {
	parts := strings.SplitN(strings.TrimSpace(id), ":", 4)
	if len(parts) != 4 {
		return ""
	}
	return strings.TrimSpace(parts[3])
}

func parsePerpPerpLegs(opp UnifiedOpportunity) (highVenue, lowVenue string, err error) {
	for _, leg := range opp.Legs {
		mkt := strings.ToLower(strings.TrimSpace(leg.Market))
		if mkt != "perp" && mkt != string(venue.MarketPerp) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(leg.Side)) {
		case "short", "sell":
			highVenue = strings.TrimSpace(leg.Venue)
		case "long", "buy":
			lowVenue = strings.TrimSpace(leg.Venue)
		}
	}
	if highVenue == "" || lowVenue == "" {
		return "", "", fmt.Errorf("perp-perp 腿信息不完整(需一空一多永续腿)")
	}
	return highVenue, lowVenue, nil
}

// crossVenueOpportunityFromUnified 把 UnifiedOpportunity 反向映射为 CrossVenueOpportunity(供 gate/open 复用)。
func crossVenueOpportunityFromUnified(opp UnifiedOpportunity) (CrossVenueOpportunity, error) {
	if opp.StrategyType != UnifiedStrategyPerpPerp {
		return CrossVenueOpportunity{}, fmt.Errorf("非 perp-perp 机会 strategy=%s", opp.StrategyType)
	}
	highVenue, lowVenue, err := parsePerpPerpLegs(opp)
	if err != nil {
		return CrossVenueOpportunity{}, err
	}
	snapshotID := parseUnifiedOpportunitySnapshotID(opp.ID)
	notional := rotationMaxLegNotional(opp)
	return CrossVenueOpportunity{
		Symbol:              opp.Symbol,
		HighVenue:           highVenue,
		LowVenue:            lowVenue,
		SnapshotID:          snapshotID,
		SpreadAPR:           opp.SpreadAPR,
		HighFundingRate:     opp.HighFundingRate,
		LowFundingRate:      opp.LowFundingRate,
		HighIntervalHours:   opp.HighIntervalHours,
		LowIntervalHours:    opp.LowIntervalHours,
		HighNextFundingTime: unifiedOpportunityParseFundingTime(opp.HighNextFundingTime),
		LowNextFundingTime:  unifiedOpportunityParseFundingTime(opp.LowNextFundingTime),
		SpreadEV:            opp.ExpectedFundingUSDT,
		OpenCost:            opp.ExpectedOpenCostUSDT,
		CloseCost:           opp.ExpectedCloseCostUSDT,
		ExpectedSlippage:    opp.ExpectedSlippageUSDT,
		NetEV:               opp.ExpectedNetEVUSDT,
		ExpectedHoldHours:   opp.ExpectedHoldHours,
		TradableNotional:    notional,
		LiquidityScore:      opp.LiquidityScore,
		HistoryQualityScore: opp.DataQualityScore,
		ExecutionScore:      opp.ExecutionScore,
		CapitalEfficiencyEV: opp.CapitalEfficiencyScore,
		RankScore:           opp.CapitalEfficiencyScore,
	}, nil
}

func findCrossVenueHeldByOpportunityID(ps []CrossVenuePosition, opportunityID, symbol string) (CrossVenuePosition, bool) {
	for _, p := range activeCrossVenuePositions(ps) {
		id := crossVenuePositionOpportunityID(p)
		if id == opportunityID && (symbol == "" || p.Symbol == symbol) {
			return p, true
		}
	}
	return CrossVenuePosition{}, false
}

// executeOpen 统一 runner 派发开仓:UnifiedOpportunity → gate → executeCrossVenueOpen,成功写入 OpportunityID。
func (e *CrossVenueExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (CrossVenuePosition, error) {
	if e == nil {
		return CrossVenuePosition{}, fmt.Errorf("跨所执行器未初始化")
	}
	rawOpp, err := crossVenueOpportunityFromUnified(opp)
	if err != nil {
		return CrossVenuePosition{}, err
	}
	now := e.nowFn()
	snap := CrossVenueScanSnapshot{}
	if e.scanner != nil {
		snap = e.scanner.Snapshot()
	}
	if snap.SnapshotID == "" {
		if sid := parseUnifiedOpportunitySnapshotID(opp.ID); sid != "" && sid != "nosnap" {
			snap.SnapshotID = sid
		}
	}
	cvOpp := e.enrichOpportunityFromSnapshot(rawOpp, snap, now)
	cfg := e.cfg.CrossVenueExecutor
	// 统一 runner 决策 EV 优先,避免 enrich/estimate 用 gate 口径覆盖已择优候选。
	if opp.ExpectedNetEVUSDT > 0 {
		cvOpp.NetEV = opp.ExpectedNetEVUSDT
	}
	if opp.ExpectedFundingUSDT > 0 {
		cvOpp.SpreadEV = opp.ExpectedFundingUSDT
	}
	if opp.ExpectedOpenCostUSDT > 0 {
		cvOpp.OpenCost = opp.ExpectedOpenCostUSDT
	}
	if opp.ExpectedCloseCostUSDT > 0 {
		cvOpp.CloseCost = opp.ExpectedCloseCostUSDT
	}
	// 反向映射可能缺 SpreadAPR;优先从同轮 scanner 机会补齐 gate 所需字段。
	if cvOpp.SpreadAPR <= 0 {
		for _, raw := range snap.Opportunities {
			if raw.Symbol == cvOpp.Symbol && raw.HighVenue == cvOpp.HighVenue && raw.LowVenue == cvOpp.LowVenue && raw.SpreadAPR > 0 {
				cvOpp.SpreadAPR = raw.SpreadAPR
				break
			}
		}
	}
	if cvOpp.SpreadAPR <= 0 && opp.ExpectedNetEVUSDT <= 0 {
		cvOpp = crossVenueEstimateOpportunityMetrics(cfg, cvOpp, now)
	}
	if cvOpp.SpreadAPR <= 0 && (cvOpp.HighFundingRate != 0 || cvOpp.LowFundingRate != 0) {
		hi, lo := cvOpp.HighIntervalHours, cvOpp.LowIntervalHours
		if hi <= 0 {
			hi = 8
		}
		if lo <= 0 {
			lo = 8
		}
		cvOpp.SpreadAPR = fundingAnnualizedAPR(cvOpp.HighFundingRate, hi) - fundingAnnualizedAPR(cvOpp.LowFundingRate, lo)
	}

	deps := e.gateDeps(snap)
	decisionNotional := rotationMaxLegNotional(opp)
	if decisionNotional > 0 {
		cvOpp.TradableNotional = decisionNotional
	}

	gateInput := crossVenueGateInput{
		Opp:           cvOpp,
		Positions:     e.snapshotPositions(),
		BybitHeld:     func(sym venue.Symbol) bool { return isBybitSymbolHeld(e.cfg.NetworkConfig.RecordDir, sym) },
		BinanceHeld:   func(sym venue.Symbol) bool { return isBinanceSymbolHeld(e.cfg.NetworkConfig.RecordDir, sym) },
		FundingHeld:   func(sym venue.Symbol) bool { return isFundingSymbolHeldAnyVenue(e.cfg.NetworkConfig.RecordDir, sym) },
		HaltActive:    e.isHaltActive(),
		SymbolCooling: e.isSymbolCooling,
		Now:           now,
	}
	// 无副作用开仓闸须先于账户侧效应(保证金划转/bybit 协议签署),避免"进场窗未到"等时序拒绝仍触 API。
	gateRes := evaluateCrossVenueOpenGate(ctx, cfg, deps, e.venues, gateInput)
	if !gateRes.OK && gateRes.Reason == carryScannerRejectFundingWindowNotReached {
		return CrossVenuePosition{}, fmt.Errorf("开仓闸: %s", gateRes.Reason)
	}

	// 开仓前确保两腿期货钱包保证金充足。rotation 模式下统一 runner 的资金路由对 perp-perp 并不会把
	// 分仓所(binance)现货/全仓的钱搬进期货钱包,导致第二腿 -2019 保证金不足;故这里始终做幂等补足:
	//   - 所需按本单真实名义/杠杆计(crossVenuePerVenueMarginUSDT),留 5% 余量对齐执行闸;
	//   - 够则 no-op(统一账户所 okx/bybit/gate 期货可用通常已足,不触发划转);
	//   - 资金来源仅现货/全仓杠杆,绝不动资金账户(见各 venue TopUpPerpMarginUSDT)。
	if reqMargin := crossVenuePerVenueMarginUSDT(cfg, cvOpp.TradableNotional) * 1.05; reqMargin > 0 {
		ensureCrossVenuePerpMargin(ctx, e.logger, e.venues, cvOpp.HighVenue, cvOpp.LowVenue, reqMargin)
	} else if !e.RotationManaged() {
		ensureCrossVenuePerpMargin(ctx, e.logger, e.venues, cvOpp.HighVenue, cvOpp.LowVenue, conservativeCrossVenueRequiredMargin(cfg))
	}
	if crossVenueUsesBybit(cvOpp.HighVenue, cvOpp.LowVenue) {
		ensureBybitTradingAgreement(ctx, e.logger, e.venues[bybit.VenueName])
	}

	if !gateRes.OK {
		gateRes = evaluateCrossVenueOpenGate(ctx, cfg, deps, e.venues, gateInput)
	}
	if !gateRes.OK {
		return CrossVenuePosition{}, fmt.Errorf("开仓闸: %s", gateRes.Reason)
	}
	gateRes = crossVenueCapGateNotional(gateRes, cvOpp)
	if decisionNotional > 0 && gateRes.NotionalUSDT > decisionNotional {
		gateRes.NotionalUSDT = decisionNotional
	}

	highCaps := e.venues[cvOpp.HighVenue].Capabilities()
	lowCaps := e.venues[cvOpp.LowVenue].Capabilities()
	refPrice := (gateRes.HighMid + gateRes.LowMid) / 2
	qty := alignCrossVenueQty(gateRes.NotionalUSDT, refPrice, gateRes.HighInstr, gateRes.LowInstr, highCaps, lowCaps)
	if !crossVenueQtyTradable(qty, refPrice, gateRes.HighInstr) {
		return CrossVenuePosition{}, fmt.Errorf("qty 不可交易 qty=%.8f notional=%.2f", qty, gateRes.NotionalUSDT)
	}

	cycleID := crossVenueDecisionCycleID(now)
	plan := crossVenueOpenPlan{
		Opp:             cvOpp,
		Gate:            gateRes,
		Qty:             qty,
		RefPrice:        refPrice,
		IdemPrefix:      crossVenueIdemPrefix(venue.Symbol(cvOpp.Symbol), cvOpp.HighVenue),
		DecisionCycleID: cycleID,
		SnapshotID:      nonEmpty(snap.SnapshotID, cvOpp.SnapshotID),
		SnapshotTime:    snap.ScannedAt,
		SelectionMode:   crossVenueOpenStrict,
	}

	pos, err := e.executeCrossVenueOpen(ctx, plan, live)
	if err != nil {
		return pos, err
	}
	if pos.Symbol != "" {
		pos.OpportunityID = opp.ID
		_ = e.persistPosition(pos)
		appendAttributionPredictionBestEffort(e.recordDir, predictionRecordFromCrossVenuePosition(opp, pos, pos.OpenedAt))
	}
	return pos, nil
}

// CloseOnePositionVerified 核对交易所真实 perp 腿后平仓:无真实腿只清台账;有真实腿则 verified close。
func (e *CrossVenueExecutor) CloseOnePositionVerified(ctx context.Context, opportunityID, symbol, reason string) error {
	if e == nil {
		return fmt.Errorf("跨所执行器未初始化")
	}
	pos, ok := findCrossVenueHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
	if !ok {
		return fmt.Errorf("跨所 perp-perp 持仓未找到 opp=%s", opportunityID)
	}
	sym := venue.Symbol(pos.Symbol)
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]
	if highV == nil || lowV == nil {
		return fmt.Errorf("%s: venue 未就绪", pos.Symbol)
	}
	highQty := 0.0
	lowQty := 0.0
	if pp, perr := highV.FetchPositions(ctx); perr == nil {
		highQty = absQty(perpPositionQty(pp, sym))
	}
	if pp, perr := lowV.FetchPositions(ctx); perr == nil {
		lowQty = absQty(perpPositionQty(pp, sym))
	}
	highOpen := highQty > executionQtyTolerance
	lowOpen := lowQty > executionQtyTolerance
	if !highOpen && !lowOpen {
		pos.Status = CrossVenueStatusClosed
		if pos.CloseReason == "" {
			pos.CloseReason = reason
		}
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedCrossVenuePosition(pos, true)
		return nil
	}
	return e.closeCrossVenuePosition(ctx, pos, true, reason)
}

// CountRealHeldPositions 统计台账中有交易所真实 perp 敞口的持仓数。
func (e *CrossVenueExecutor) CountRealHeldPositions(ctx context.Context) int {
	if e == nil || e.store == nil {
		return 0
	}
	ps, err := e.store.load()
	if err != nil {
		return 0
	}
	count := 0
	for _, pos := range activeCrossVenuePositions(ps) {
		sym := venue.Symbol(pos.Symbol)
		highV := e.venues[pos.HighVenue]
		lowV := e.venues[pos.LowVenue]
		if highV == nil || lowV == nil {
			continue
		}
		highQty := 0.0
		lowQty := 0.0
		if pp, perr := highV.FetchPositions(ctx); perr == nil {
			highQty = absQty(perpPositionQty(pp, sym))
		}
		if pp, perr := lowV.FetchPositions(ctx); perr == nil {
			lowQty = absQty(perpPositionQty(pp, sym))
		}
		if highQty > executionQtyTolerance || lowQty > executionQtyTolerance {
			count++
		}
	}
	return count
}
