package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// unified_rotation_runner.go 统一轮动 runner: 扫描 → 留换决策 → 资金路由 → 四执行器派发(默认 shadow)。

type rotationHeldRecord struct {
	HeldPosition
	ContinuationInputs
}

type rotationExecutorBinding struct {
	strategyType string
	openFn       func(ctx context.Context, opp UnifiedOpportunity, live bool) error
	closeFn      func(ctx context.Context, opportunityID, symbol, reason string, live bool) error
	heldFn       func() []rotationHeldRecord
}

// UnifiedRotationRunner 串联五类扫描候选、轮动决策、资金路由与四 spot/perp-spot 执行器。
type UnifiedRotationRunner struct {
	bindings map[string]*rotationExecutorBinding

	scanner   *CrossVenueScanner
	venues    map[string]venue.Venue
	recordDir string
	logger    *log.Logger
	nowFn     func() time.Time

	execCfg          CrossVenueExecutorConfig
	rotationCfg      RotationConfig
	capitalRouterCfg CapitalRouterConfig

	fetchDeps unifiedShadowPipelineFetchDeps

	scanCandidatesFn func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error)
	decideRotationFn func(held []HeldPosition, heldInputs map[string]ContinuationInputs, candidates []UnifiedOpportunity, available map[string]float64) RotationPlan

	lastSnapshotID string
}

// NewUnifiedRotationRunner 构造统一轮动 runner; 四执行器与 scanner 由调用方注入。
func NewUnifiedRotationRunner(
	execCfg CrossVenueExecutorConfig,
	rotationCfg RotationConfig,
	capitalRouterCfg CapitalRouterConfig,
	svsp *SingleVenueSpotPerpExecutor,
	cvsp *CrossVenueSpotPerpExecutor,
	svps *SingleVenuePerpSpotExecutor,
	cvps *CrossVenuePerpSpotExecutor,
	scanner *CrossVenueScanner,
	venues map[string]venue.Venue,
	recordDir string,
	logger *log.Logger,
) *UnifiedRotationRunner {
	if logger == nil {
		logger = log.Default()
	}
	if venues == nil {
		venues = map[string]venue.Venue{}
	}
	r := &UnifiedRotationRunner{
		bindings:         map[string]*rotationExecutorBinding{},
		scanner:          scanner,
		venues:           venues,
		recordDir:        strings.TrimSpace(recordDir),
		logger:           logger,
		nowFn:            time.Now,
		execCfg:          execCfg,
		rotationCfg:      rotationCfg,
		capitalRouterCfg: capitalRouterCfg,
	}
	if b := bindSingleVenueSpotPerpRotationExecutor(svsp); b != nil {
		r.bindings[b.strategyType] = b
	}
	if b := bindCrossVenueSpotPerpRotationExecutor(cvsp); b != nil {
		r.bindings[b.strategyType] = b
	}
	if b := bindSingleVenuePerpSpotRotationExecutor(svps); b != nil {
		r.bindings[b.strategyType] = b
	}
	if b := bindCrossVenuePerpSpotRotationExecutor(cvps); b != nil {
		r.bindings[b.strategyType] = b
	}
	return r
}

// Run 按 ticker 周期驱动 runOnce; live=false 时全程 shadow(不下真单/不真划转)。
func (r *UnifiedRotationRunner) Run(ctx context.Context, live bool) {
	if r == nil {
		return
	}
	if !live && r.logger != nil {
		r.logger.Printf("[统一轮动] 已启动 shadow 模式: 不下真单、不执行所内划转")
	}
	interval := r.tickerInterval()
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 周期 interval=%s live=%v", interval, live)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.runOnceSafe(ctx, live)
	for {
		select {
		case <-ctx.Done():
			if r.logger != nil {
				r.logger.Printf("[统一轮动] 已停止")
			}
			return
		case <-ticker.C:
			r.runOnceSafe(ctx, live)
		}
	}
}

func (r *UnifiedRotationRunner) tickerInterval() time.Duration {
	interval := 60 * time.Second
	if r != nil && r.scanner != nil {
		scannerInterval := time.Duration(r.scanner.cfg.ScanIntervalMinutes) * time.Minute
		if scannerInterval > interval {
			interval = scannerInterval
		}
	}
	return interval
}

func (r *UnifiedRotationRunner) runOnceSafe(ctx context.Context, live bool) {
	defer func() {
		if rec := recover(); rec != nil && r != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] panic recovered: %v", rec)
		}
	}()
	if err := r.runOnce(ctx, live); err != nil && r.logger != nil {
		r.logger.Printf("[统一轮动] 本轮失败(已吞掉): %v", err)
	}
}

// runOnce 单轮闭环: 产候选 → 收持仓 → 决策 → 资金路由 → 派发。
func (r *UnifiedRotationRunner) runOnce(ctx context.Context, live bool) error {
	if r == nil {
		return nil
	}
	snap := CrossVenueScanSnapshot{}
	if r.scanner != nil {
		snap = r.scanner.Snapshot()
	}
	snapshotID := strings.TrimSpace(snap.SnapshotID)
	if snapshotID != "" && snapshotID == r.lastSnapshotID {
		return nil
	}
	if snapshotID != "" {
		r.lastSnapshotID = snapshotID
	}

	candidates, available, err := r.scanRotationCandidates(ctx, snap)
	if err != nil {
		return err
	}
	held, heldInputs := r.collectHeldPositions(candidates)
	freeAvailable := rotationFreeAvailableUSDT(available, held)
	r.runOnceDecisionAndDispatch(ctx, live, candidates, freeAvailable, held, heldInputs)
	return nil
}

// runOnceDecisionAndDispatch 纯决策+路由+派发(便于单测注入)。
func (r *UnifiedRotationRunner) runOnceDecisionAndDispatch(
	ctx context.Context,
	live bool,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
) {
	if r == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] decision/dispatch panic recovered: %v", rec)
		}
	}()

	plan := r.decideRotation(held, heldInputs, candidates, available)
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 决策完成: held=%d candidates=%d actions=%d live=%v",
			len(held), len(candidates), len(plan.Actions), live)
	}

	candidatesByID := indexUnifiedOpportunitiesByID(candidates)
	toFund := rotationOpportunitiesToFund(plan, candidatesByID)
	if len(toFund) > 0 {
		balances := rotationWalletBalancesFromAvailable(available, held)
		routingPlan := PlanCapitalRouting(toFund, balances, r.capitalRouterCfg)
		if r.logger != nil && len(routingPlan.WithinVenueTransfers) > 0 {
			r.logger.Printf("[统一轮动] 资金路由: %d 笔所内划转 live=%v", len(routingPlan.WithinVenueTransfers), live)
		}
		executed, errs := ExecuteCapitalRouting(ctx, routingPlan, r.venues, live, r.logger)
		if r.logger != nil {
			r.logger.Printf("[统一轮动] 资金路由执行: executed=%d errs=%d live=%v", executed, len(errs), live)
		}
		for _, e := range errs {
			if r.logger != nil {
				r.logger.Printf("[统一轮动] 资金路由错误(已吞掉): %v", e)
			}
		}
		unfundable := rotationUnfundableSet(routingPlan.Unfundable)
		if len(unfundable) > 0 && r.logger != nil {
			r.logger.Printf("[统一轮动] 无法备资机会: %v", routingPlan.Unfundable)
		}
		r.dispatchRotationPlan(ctx, live, plan, candidatesByID, unfundable)
		return
	}
	r.dispatchRotationPlan(ctx, live, plan, candidatesByID, nil)
}

func (r *UnifiedRotationRunner) decideRotation(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
) RotationPlan {
	if r != nil && r.decideRotationFn != nil {
		return r.decideRotationFn(held, heldInputs, candidates, available)
	}
	return DecideRotation(held, heldInputs, candidates, available, r.rotationCfg)
}

func (r *UnifiedRotationRunner) scanRotationCandidates(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
	if r != nil && r.scanCandidatesFn != nil {
		return r.scanCandidatesFn(ctx, snap)
	}
	return r.defaultScanRotationCandidates(ctx, snap)
}

func (r *UnifiedRotationRunner) defaultScanRotationCandidates(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
	deps := r.fetchDeps
	if deps.fetchMarkets == nil {
		deps.fetchMarkets = defaultUnifiedShadowFetchMarketsByVenue
	}
	if deps.fetchAvailableUSDT == nil {
		deps.fetchAvailableUSDT = defaultUnifiedShadowFetchAvailableUSDTByVenue
	}

	now := time.Now().UTC()
	if r.nowFn != nil {
		now = r.nowFn().UTC()
	}
	if !snap.ScannedAt.IsZero() {
		now = snap.ScannedAt.UTC()
	}
	snapshotID := strings.TrimSpace(snap.SnapshotID)
	if snapshotID == "" {
		snapshotID = "rotation-nosnap"
	}

	fundingByVenue := unifiedShadowFundingByVenueFromSnapshot(snap.FundingByVenue)
	symbols := crossVenueSpotPerpCollectSymbols(fundingByVenue, nil)
	marketsByVenue := deps.fetchMarkets(ctx, r.venues, symbols)
	availableUSDTByVenue := deps.fetchAvailableUSDT(ctx, r.venues, crossVenueExecutorVenues)
	borrowDaily, borrowLimits := rotationBorrowMapsFromProbes(snap.BorrowProbes)

	singleSpotPerp := ScanSingleVenueSpotPerpOpportunities(SingleVenueSpotPerpScanInput{
		SnapshotID:           snapshotID,
		Now:                  now,
		Symbols:              symbols,
		Venues:               crossVenueExecutorVenues,
		FundingByVenue:       fundingByVenue,
		MarketsByVenue:       marketsByVenue,
		AvailableUSDTByVenue: availableUSDTByVenue,
		Config:               DefaultSingleVenueSpotPerpScannerConfig(r.execCfg),
	})
	crossSpotPerp := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID:           snapshotID,
		Now:                  now,
		Symbols:              symbols,
		FundingByVenue:       fundingByVenue,
		MarketsByVenue:       marketsByVenue,
		AvailableUSDTByVenue: availableUSDTByVenue,
		Config:               DefaultCrossVenueSpotPerpScannerConfig(r.execCfg),
	})
	singlePerpSpot := ScanSingleVenuePerpSpotOpportunities(SingleVenuePerpSpotScanInput{
		SnapshotID:             snapshotID,
		Now:                    now,
		Symbols:                symbols,
		Venues:                 crossVenueExecutorVenues,
		FundingByVenue:         fundingByVenue,
		MarketsByVenue:         marketsByVenue,
		AvailableUSDTByVenue:   availableUSDTByVenue,
		BorrowDailyRateByAsset: borrowDaily,
		BorrowLimitUSDTByAsset: borrowLimits,
		Config:                 DefaultSingleVenuePerpSpotScannerConfig(r.execCfg),
	})
	crossPerpSpot := ScanCrossVenuePerpSpotOpportunities(CrossVenuePerpSpotScanInput{
		SnapshotID:             snapshotID,
		Now:                    now,
		Symbols:                symbols,
		FundingByVenue:         fundingByVenue,
		MarketsByVenue:         marketsByVenue,
		AvailableUSDTByVenue:   availableUSDTByVenue,
		BorrowDailyRateByAsset: borrowDaily,
		BorrowLimitUSDTByAsset: borrowLimits,
		Config:                 DefaultCrossVenuePerpSpotScannerConfig(r.execCfg),
	})

	candidates := make([]UnifiedOpportunity, 0, len(singleSpotPerp)+len(crossSpotPerp)+len(singlePerpSpot)+len(crossPerpSpot))
	candidates = append(candidates, singleSpotPerp...)
	candidates = append(candidates, crossSpotPerp...)
	candidates = append(candidates, singlePerpSpot...)
	candidates = append(candidates, crossPerpSpot...)

	if r.logger != nil {
		r.logger.Printf("[统一轮动] 扫描完成 snapshot=%s candidates=%d (svsp=%d cvsp=%d svps=%d cvps=%d)",
			snapshotID, len(candidates), len(singleSpotPerp), len(crossSpotPerp), len(singlePerpSpot), len(crossPerpSpot))
	}
	return candidates, availableUSDTByVenue, nil
}

func (r *UnifiedRotationRunner) collectHeldPositions(candidates []UnifiedOpportunity) ([]HeldPosition, map[string]ContinuationInputs) {
	held := make([]HeldPosition, 0)
	inputs := map[string]ContinuationInputs{}
	candidatesByID := indexUnifiedOpportunitiesByID(candidates)

	for _, binding := range r.bindings {
		if binding == nil || binding.heldFn == nil {
			continue
		}
		for _, rec := range binding.heldFn() {
			pos := rec.HeldPosition
			if strings.TrimSpace(pos.OpportunityID) == "" {
				continue
			}
			cont := rec.ContinuationInputs
			if cont == (ContinuationInputs{}) {
				cont = rotationContinuationFromCandidate(pos, candidatesByID[pos.OpportunityID], r.execCfg)
			}
			held = append(held, pos)
			inputs[pos.OpportunityID] = cont
		}
	}
	return held, inputs
}

func (r *UnifiedRotationRunner) dispatchRotationPlan(
	ctx context.Context,
	live bool,
	plan RotationPlan,
	candidatesByID map[string]UnifiedOpportunity,
	unfundable map[string]struct{},
) {
	for _, act := range plan.Actions {
		switch act.Kind {
		case RotationActionHold:
			if r.logger != nil {
				r.logger.Printf("[统一轮动] HOLD %s %s reason=%s", act.StrategyType, act.Symbol, act.Reason)
			}
		case RotationActionClose:
			if err := r.closeHeld(ctx, act.StrategyType, act.OpportunityID, act.Symbol, live, act.Reason); err != nil && r.logger != nil {
				r.logger.Printf("[统一轮动] CLOSE 失败(已吞掉) %s: %v", act.OpportunityID, err)
			}
		case RotationActionSwitch:
			if err := r.closeHeld(ctx, act.StrategyType, act.OpportunityID, act.Symbol, live, act.Reason); err != nil && r.logger != nil {
				r.logger.Printf("[统一轮动] SWITCH 平旧失败(已吞掉) %s: %v", act.OpportunityID, err)
			}
			if _, skip := unfundable[act.ReplaceOpportunityID]; skip {
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 跳过开新(无法备资) %s", act.ReplaceOpportunityID)
				}
				continue
			}
			if opp, ok := candidatesByID[act.ReplaceOpportunityID]; ok {
				if err := r.openOpportunity(ctx, act.ReplaceStrategyType, opp, live); err != nil && r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 开新失败(已吞掉) %s: %v", act.ReplaceOpportunityID, err)
				}
			}
		case RotationActionOpen:
			if _, skip := unfundable[act.OpportunityID]; skip {
				if r.logger != nil {
					r.logger.Printf("[统一轮动] OPEN 跳过(无法备资) %s", act.OpportunityID)
				}
				continue
			}
			if opp, ok := candidatesByID[act.OpportunityID]; ok {
				if err := r.openOpportunity(ctx, act.StrategyType, opp, live); err != nil && r.logger != nil {
					r.logger.Printf("[统一轮动] OPEN 失败(已吞掉) %s: %v", act.OpportunityID, err)
				}
			}
		}
	}
}

func (r *UnifiedRotationRunner) openOpportunity(ctx context.Context, strategyType string, opp UnifiedOpportunity, live bool) error {
	binding := r.bindingForStrategy(strategyType)
	if binding == nil || binding.openFn == nil {
		return fmt.Errorf("无执行器绑定 strategy_type=%s", strategyType)
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] OPEN %s %s live=%v", strategyType, opp.Symbol, live)
	}
	return binding.openFn(ctx, opp, live)
}

func (r *UnifiedRotationRunner) closeHeld(ctx context.Context, strategyType, opportunityID, symbol string, live bool, reason string) error {
	binding := r.bindingForStrategy(strategyType)
	if binding == nil || binding.closeFn == nil {
		return fmt.Errorf("无执行器绑定 strategy_type=%s", strategyType)
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] CLOSE %s %s live=%v reason=%s", strategyType, symbol, live, reason)
	}
	return binding.closeFn(ctx, opportunityID, symbol, reason, live)
}

func (r *UnifiedRotationRunner) bindingForStrategy(strategyType string) *rotationExecutorBinding {
	if r == nil {
		return nil
	}
	if b := r.bindings[strategyType]; b != nil {
		return b
	}
	return nil
}

func bindSingleVenueSpotPerpRotationExecutor(e *SingleVenueSpotPerpExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategySingleVenueSpotPerp,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeFn: func(ctx context.Context, opportunityID, symbol, reason string, live bool) error {
			pos, ok := findSingleVenueSpotPerpHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
			if !ok {
				return fmt.Errorf("同所spot-perp 持仓未找到 opp=%s", opportunityID)
			}
			return e.closePosition(ctx, pos, live, reason)
		},
		heldFn: func() []rotationHeldRecord {
			return collectSingleVenueSpotPerpHeldRecords(e.snapshotPositions(), e.execCfg())
		},
	}
}

func bindCrossVenueSpotPerpRotationExecutor(e *CrossVenueSpotPerpExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategyCrossVenueSpotPerp,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeFn: func(ctx context.Context, opportunityID, symbol, reason string, live bool) error {
			pos, ok := findCrossVenueSpotPerpHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
			if !ok {
				return fmt.Errorf("跨所spot-perp 持仓未找到 opp=%s", opportunityID)
			}
			return e.closePosition(ctx, pos, live, reason)
		},
		heldFn: func() []rotationHeldRecord {
			return collectCrossVenueSpotPerpHeldRecords(e.snapshotPositions(), e.cfg.CrossVenueExecutor)
		},
	}
}

func bindSingleVenuePerpSpotRotationExecutor(e *SingleVenuePerpSpotExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategySingleVenuePerpSpot,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeFn: func(ctx context.Context, opportunityID, symbol, reason string, live bool) error {
			pos, ok := findSingleVenuePerpSpotHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
			if !ok {
				return fmt.Errorf("同所perp-spot 持仓未找到 opp=%s", opportunityID)
			}
			return e.closePosition(ctx, pos, live, reason)
		},
		heldFn: func() []rotationHeldRecord {
			return collectSingleVenuePerpSpotHeldRecords(e.snapshotPositions(), e.execCfg())
		},
	}
}

func bindCrossVenuePerpSpotRotationExecutor(e *CrossVenuePerpSpotExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategyCrossVenuePerpSpot,
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeFn: func(ctx context.Context, opportunityID, symbol, reason string, live bool) error {
			pos, ok := findCrossVenuePerpSpotHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
			if !ok {
				return fmt.Errorf("跨所perp-spot 持仓未找到 opp=%s", opportunityID)
			}
			return e.closePosition(ctx, pos, live, reason)
		},
		heldFn: func() []rotationHeldRecord {
			return collectCrossVenuePerpSpotHeldRecords(e.snapshotPositions(), e.cfg.CrossVenueExecutor)
		},
	}
}

func findSingleVenueSpotPerpHeldByOpportunityID(ps []SingleVenueSpotPerpPosition, opportunityID, symbol string) (SingleVenueSpotPerpPosition, bool) {
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if p.OpportunityID == opportunityID && (symbol == "" || p.Symbol == symbol) {
			return p, true
		}
	}
	return SingleVenueSpotPerpPosition{}, false
}

func findCrossVenueSpotPerpHeldByOpportunityID(ps []CrossVenueSpotPerpPosition, opportunityID, symbol string) (CrossVenueSpotPerpPosition, bool) {
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if p.OpportunityID == opportunityID && (symbol == "" || p.Symbol == symbol) {
			return p, true
		}
	}
	return CrossVenueSpotPerpPosition{}, false
}

func findSingleVenuePerpSpotHeldByOpportunityID(ps []SingleVenuePerpSpotPosition, opportunityID, symbol string) (SingleVenuePerpSpotPosition, bool) {
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if p.OpportunityID == opportunityID && (symbol == "" || p.Symbol == symbol) {
			return p, true
		}
	}
	return SingleVenuePerpSpotPosition{}, false
}

func findCrossVenuePerpSpotHeldByOpportunityID(ps []CrossVenuePerpSpotPosition, opportunityID, symbol string) (CrossVenuePerpSpotPosition, bool) {
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if p.OpportunityID == opportunityID && (symbol == "" || p.Symbol == symbol) {
			return p, true
		}
	}
	return CrossVenuePerpSpotPosition{}, false
}

func collectSingleVenueSpotPerpHeldRecords(ps []SingleVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateSingleVenueSpotPerpCapital(p, execCfg)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategySingleVenueSpotPerp,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
			},
			ContinuationInputs: rotationContinuationFromPosition(p.EstFundingUSDT, p.NotionalUSDT, execCfg, false),
		})
	}
	return out
}

func collectCrossVenueSpotPerpHeldRecords(ps []CrossVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateCrossVenueSpotPerpCapital(p, execCfg)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategyCrossVenueSpotPerp,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
			},
			ContinuationInputs: rotationContinuationFromPosition(p.EstFundingUSDT, p.NotionalUSDT, execCfg, false),
		})
	}
	return out
}

func collectSingleVenuePerpSpotHeldRecords(ps []SingleVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateSingleVenuePerpSpotCapital(p, execCfg)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategySingleVenuePerpSpot,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
			},
			ContinuationInputs: rotationContinuationFromPosition(p.EstFundingUSDT, p.NotionalUSDT, execCfg, true),
		})
	}
	return out
}

func collectCrossVenuePerpSpotHeldRecords(ps []CrossVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateCrossVenuePerpSpotCapital(p, execCfg)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategyCrossVenuePerpSpot,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
			},
			ContinuationInputs: rotationContinuationFromPosition(p.EstFundingUSDT, p.NotionalUSDT, execCfg, true),
		})
	}
	return out
}

func estimateSingleVenueSpotPerpCapital(pos SingleVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.SpotLeg.Qty * pos.SpotLeg.EntryPrice
	}
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	return map[string]float64{pos.Venue: notional + margin}
}

func estimateCrossVenueSpotPerpCapital(pos CrossVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.SpotLeg.Qty * pos.SpotLeg.EntryPrice
	}
	return map[string]float64{
		pos.SpotVenue: notional,
		pos.PerpVenue: crossVenuePerVenueMarginUSDT(execCfg, notional),
	}
}

func estimateSingleVenuePerpSpotCapital(pos SingleVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	return map[string]float64{pos.Venue: notional + margin}
}

func estimateCrossVenuePerpSpotCapital(pos CrossVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	return map[string]float64{
		pos.SpotVenue: notional,
		pos.PerpVenue: crossVenuePerVenueMarginUSDT(execCfg, notional),
	}
}

func rotationContinuationFromCandidate(pos HeldPosition, cand UnifiedOpportunity, execCfg CrossVenueExecutorConfig) ContinuationInputs {
	if strings.TrimSpace(cand.ID) != "" {
		totalCapital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
		holdingRisk := 0.0
		if totalCapital > 0 && cand.RiskScore > 0 {
			holdingRisk = cand.RiskScore * totalCapital * 0.01
		}
		return ContinuationInputs{
			ExpectedFundingUSDT:    cand.ExpectedFundingUSDT,
			ExpectedCloseCostUSDT:  cand.ExpectedCloseCostUSDT,
			HoldingRiskUSDT:        holdingRisk,
			ExpectedBorrowCostUSDT: cand.ExpectedBorrowCostUSDT,
		}
	}
	notional := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	return rotationContinuationFromPosition(0, notional, execCfg, pos.StrategyType == UnifiedStrategySingleVenuePerpSpot || pos.StrategyType == UnifiedStrategyCrossVenuePerpSpot)
}

func rotationContinuationFromPosition(estFunding, notional float64, execCfg CrossVenueExecutorConfig, negativeCarry bool) ContinuationInputs {
	closeCost := 0.0
	if notional > 0 {
		feeBps := execCfg.DefaultTakerFeeBps
		if feeBps <= 0 {
			feeBps = 4
		}
		closeCost = notional * feeBps / 10000 * 2
	}
	borrowCost := 0.0
	if negativeCarry && notional > 0 {
		borrowCost = notional * 0.0003
	}
	return ContinuationInputs{
		ExpectedFundingUSDT:    estFunding,
		ExpectedCloseCostUSDT:  closeCost,
		HoldingRiskUSDT:        notional * 0.001,
		ExpectedBorrowCostUSDT: borrowCost,
	}
}

func rotationBorrowMapsFromProbes(probes []CrossVenueBorrowProbe) (dailyRates map[string]float64, limits map[string]float64) {
	dailyRates = map[string]float64{}
	limits = map[string]float64{}
	for _, p := range probes {
		if !p.Available {
			continue
		}
		base := baseAssetFromCanonicalSymbol(p.Symbol)
		if base == "" {
			continue
		}
		if p.HourlyRateAPR > 0 {
			dailyRates[base] = p.HourlyRateAPR / 365.0
		}
		if p.MaxBorrowable > 0 {
			if cur, ok := limits[base]; !ok || p.MaxBorrowable > cur {
				limits[base] = p.MaxBorrowable
			}
		}
	}
	return dailyRates, limits
}

func indexUnifiedOpportunitiesByID(opps []UnifiedOpportunity) map[string]UnifiedOpportunity {
	out := make(map[string]UnifiedOpportunity, len(opps))
	for _, opp := range opps {
		if strings.TrimSpace(opp.ID) == "" {
			continue
		}
		out[opp.ID] = opp
	}
	return out
}

func rotationOpportunitiesToFund(plan RotationPlan, candidatesByID map[string]UnifiedOpportunity) []UnifiedOpportunity {
	chosen := make([]UnifiedOpportunity, 0)
	seen := map[string]struct{}{}
	for _, act := range plan.Actions {
		var id string
		switch act.Kind {
		case RotationActionOpen:
			id = act.OpportunityID
		case RotationActionSwitch:
			id = act.ReplaceOpportunityID
		default:
			continue
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if opp, ok := candidatesByID[id]; ok {
			chosen = append(chosen, opp)
			seen[id] = struct{}{}
		}
	}
	return chosen
}

func rotationUnfundableSet(unfundable []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, entry := range unfundable {
		id := entry
		if i := strings.Index(entry, ":"); i > 0 {
			id = strings.TrimSpace(entry[:i])
		}
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func rotationFreeAvailableUSDT(fetched map[string]float64, held []HeldPosition) map[string]float64 {
	free := cloneVenueCapitalMap(fetched)
	for _, pos := range held {
		for venue, amt := range pos.CapitalRequiredByVenue {
			if amt <= 0 {
				continue
			}
			free[venue] -= amt
			if free[venue] < 0 {
				free[venue] = 0
			}
		}
	}
	return free
}

func rotationWalletBalancesFromAvailable(fetched map[string]float64, held []HeldPosition) VenueWalletBalances {
	totals := cloneVenueCapitalMap(fetched)
	for _, pos := range held {
		rotationAddCapitalMap(totals, pos.CapitalRequiredByVenue)
	}
	out := VenueWalletBalances{}
	for venue, amt := range totals {
		if amt <= 0 {
			continue
		}
		out[venue] = map[WalletKind]float64{WalletKindFutures: amt}
	}
	return out
}
