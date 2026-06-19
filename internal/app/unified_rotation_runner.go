package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lingxi/internal/venue"
)

const (
	rotationSnapshotMaxAge        = 30 * time.Minute
	rotationScanMinAbsFundingRate = 0.00005 // 0.5 bps per 8h-interval 基准;粗筛按 per-hour 归一比较
	rotationScanMaxSymbols        = 150
	// rotationScanDepthFetchMaxSymbols funding 粗筛后二次拉深度上限;深度 API 成本高,仅对决策候选前 N 个拉取。
	rotationScanDepthFetchMaxSymbols = 50

	// 统一轮动 SWITCH 事务性失败事件类型(domain_events.jsonl Type 字段,可检索)。
	rotationSwitchAbortedCloseFailed EventType = "switch_aborted_close_failed"
	rotationSwitchFailedOpen         EventType = "switch_failed_open"
)

// unified_rotation_runner.go 统一轮动 runner: 扫描 → 留换决策 → 资金路由 → 四执行器派发(active 即实盘)。

type rotationHeldRecord struct {
	HeldPosition
	ContinuationInputs
	delistLegs []rotationDelistLeg
}

type rotationExecutorBinding struct {
	strategyType    string
	openFn          func(ctx context.Context, opp UnifiedOpportunity, live bool) error
	openBlockedFn   func(symbol string) bool
	closeVerifiedFn func(ctx context.Context, opportunityID, symbol, reason string) error
	realHeldCountFn func(ctx context.Context) int
	heldFn          func() []rotationHeldRecord
	reconcileFn     func(ctx context.Context, live bool)
	manageFn        func(ctx context.Context, live bool)
}

// RotationCandidateCounts 五策略候选数量(JSON 友好)。
type RotationCandidateCounts struct {
	SVSP     int `json:"svsp"`
	CVSP     int `json:"cvsp"`
	SVPS     int `json:"svps"`
	CVPS     int `json:"cvps"`
	PerpPerp int `json:"perp_perp"`
	Total    int `json:"total"`
}

// RotationActionCounts 本轮轮动动作计数(JSON 友好)。
type RotationActionCounts struct {
	Open   int `json:"open"`
	Close  int `json:"close"`
	Switch int `json:"switch"`
	Hold   int `json:"hold"`
}

// RotationStatus 统一轮动运行时快照(JSON 友好)。
type RotationStatus struct {
	Active          bool                    `json:"active"`
	IntervalSeconds int                     `json:"interval_seconds"`
	LastCycleAt     string                  `json:"last_cycle_at"`
	LastSnapshotID  string                  `json:"last_snapshot_id"`
	Candidates      RotationCandidateCounts `json:"candidates"`
	Held            int                     `json:"held"`
	RealHeldManaged int                     `json:"real_held_managed"`
	Actions         RotationActionCounts    `json:"actions"`
	LastError       string                  `json:"last_error"`
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

	includePerpPerpCandidates bool

	fetchDeps   unifiedMarketPipelineFetchDeps
	delistGuard *DelistGuard

	scanCandidatesFn func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error)
	decideRotationFn func(held []HeldPosition, heldInputs map[string]ContinuationInputs, candidates []UnifiedOpportunity, available map[string]float64) RotationPlan

	lastSnapshotID string

	active   atomic.Bool
	scanning atomic.Bool

	statusMu               sync.RWMutex
	status                 RotationStatus
	lastPausedLogMu        sync.Mutex
	lastPausedLogAt        time.Time
	lastStaleSnapshotLogMu sync.Mutex
	lastStaleSnapshotLogAt time.Time

	lastCapitalSnapshotMu sync.RWMutex
	lastCapitalSnapshot   rotationCapitalSnapshotContext
	// controlPlane 统一控制面附加闸(nil=仅沿用 active 等既有语义)。
	controlPlane *ControlPlane
}

// rotationCapitalSnapshotContext 最近一轮资金路由只读上下文(供 capital snapshot API)。
type rotationCapitalSnapshotContext struct {
	pending     []UnifiedOpportunity
	routingPlan CapitalRoutingPlan
}

func (r *UnifiedRotationRunner) storeLastCapitalSnapshotContext(pending []UnifiedOpportunity, plan CapitalRoutingPlan) {
	if r == nil {
		return
	}
	r.lastCapitalSnapshotMu.Lock()
	r.lastCapitalSnapshot = rotationCapitalSnapshotContext{
		pending:     append([]UnifiedOpportunity(nil), pending...),
		routingPlan: plan,
	}
	r.lastCapitalSnapshotMu.Unlock()
}

// CapitalSnapshotReadOnly 拉取当前余额与持仓并聚合资金快照;不触发划转/下单/平仓。
func (r *UnifiedRotationRunner) CapitalSnapshotReadOnly(ctx context.Context) UnifiedCapitalSnapshot {
	if r == nil {
		return UnifiedCapitalSnapshot{}
	}
	deps := r.fetchDeps
	if deps.fetchAvailableUSDT == nil {
		deps.fetchAvailableUSDT = defaultUnifiedMarketFetchAvailableUSDTByVenue
	}
	fetched := deps.fetchAvailableUSDT(ctx, r.venues, crossVenueExecutorVenues)
	held, _ := r.collectHeldPositions(nil, nil)

	r.lastCapitalSnapshotMu.RLock()
	ctxSnap := r.lastCapitalSnapshot
	r.lastCapitalSnapshotMu.RUnlock()

	routerCfg := CapitalRouterConfigWithVenues(r.capitalRouterCfg, r.venues)
	nowFn := r.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	return BuildUnifiedCapitalSnapshot(CapitalSnapshotInput{
		FetchedByVenue:       fetched,
		Held:                 held,
		PendingOpportunities: ctxSnap.pending,
		RoutingPlan:          ctxSnap.routingPlan,
		RouterCfg:            routerCfg,
		ExecCfg:              r.execCfg,
		Venues:               r.venues,
		GeneratedAt:          nowFn().UTC(),
	})
}

// NewUnifiedRotationRunner 构造统一轮动 runner; 五执行器与 scanner 由调用方注入(perp-perp 可为 nil)。
func NewUnifiedRotationRunner(
	execCfg CrossVenueExecutorConfig,
	rotationCfg RotationConfig,
	capitalRouterCfg CapitalRouterConfig,
	svsp *SingleVenueSpotPerpExecutor,
	cvsp *CrossVenueSpotPerpExecutor,
	svps *SingleVenuePerpSpotExecutor,
	cvps *CrossVenuePerpSpotExecutor,
	cvpp *CrossVenueExecutor,
	scanner *CrossVenueScanner,
	venues map[string]venue.Venue,
	recordDir string,
	logger *log.Logger,
	delistCfg DelistGuardConfig,
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
	r.delistGuard = NewDelistGuard(venues, delistCfg, logger)
	r.includePerpPerpCandidates = true
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
	if b := bindCrossVenuePerpPerpRotationExecutor(cvpp); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.active.Store(false)
	return r
}

// SetControlPlane 注入统一控制面(启动装配时调用)。
func (r *UnifiedRotationRunner) SetControlPlane(cp *ControlPlane) {
	if r == nil {
		return
	}
	r.controlPlane = cp
}

// InitState 按默认 active 初始化;若 rotation_control.json 存在则用文件 active 覆盖(legacy live 字段忽略)。
func (r *UnifiedRotationRunner) InitState(defaultActive bool) {
	if r == nil {
		return
	}
	r.active.Store(defaultActive)
	if r.recordDir == "" {
		return
	}
	st, ok, err := loadRotationControlState(r.recordDir)
	if err != nil && r.logger != nil {
		r.logger.Printf("[统一轮动] 读取 rotation_control.json 失败,使用默认 active=%v: %v",
			defaultActive, err)
		return
	}
	if !ok {
		return
	}
	r.active.Store(st.Active)
}

// SetActive 运行时启停轮动执行(线程安全);active=true 即实盘下单,状态变更落盘 rotation_control.json。
func (r *UnifiedRotationRunner) SetActive(on bool) {
	if r == nil {
		return
	}
	r.active.Store(on)
	_ = saveRotationControlState(r.recordDir, on)
}

// Status 返回最近一次 runOnce 快照与当前 active(线程安全)。
func (r *UnifiedRotationRunner) Status() RotationStatus {
	if r == nil {
		return emptyRotationStatus()
	}
	r.statusMu.RLock()
	st := r.status
	r.statusMu.RUnlock()
	st.Active = r.active.Load()
	st.IntervalSeconds = int(r.tickerInterval().Seconds())
	st.RealHeldManaged = r.countRealHeldPositions(context.Background())
	return st
}

func emptyRotationStatus() RotationStatus {
	return RotationStatus{
		Active: false,
	}
}

// isLive 启动 reconcile 是否真钱(仍绑定 active;冷启动 OFF 不 flatten 未登记仓)。
func (r *UnifiedRotationRunner) isLive() bool {
	if r == nil {
		return false
	}
	return r.active.Load()
}

// openLive 新开仓/资金路由是否真钱(active + 控制面闸;与 manage 解耦)。
func (r *UnifiedRotationRunner) openLive() bool {
	if r == nil || !r.active.Load() {
		return false
	}
	if r.controlPlane == nil {
		return true
	}
	if r.controlPlane.IsGlobalHaltActive() {
		return false
	}
	if !r.controlPlane.AllowNewOpens() {
		return false
	}
	if !r.controlPlane.RotationLiveEnabled() {
		return false
	}
	return true
}

// manageLive 在场仓安全管理是否真钱(与 active 解耦;OFF 模式或 manage 闸关闭则 OFF)。
func (r *UnifiedRotationRunner) manageLive() bool {
	if r == nil {
		return false
	}
	if r.controlPlane == nil {
		// 无控制面(测试/旧路径):策略已部署即真钱,不因 STOP 误走 OFF manage。
		return true
	}
	if !r.controlPlane.RotationLiveEnabled() {
		return false
	}
	return r.controlPlane.AllowManageOrders()
}

// closeLive 轮动 verified-close 是否真钱(与 active 解耦;OFF 模式或 close 闸关闭则跳过)。
func (r *UnifiedRotationRunner) closeLive() bool {
	if r == nil {
		return false
	}
	if r.controlPlane == nil {
		return true
	}
	if !r.controlPlane.RotationLiveEnabled() {
		return false
	}
	return r.controlPlane.AllowVerifiedCloses()
}

func (r *UnifiedRotationRunner) commitStatus(update RotationStatus) {
	if r == nil {
		return
	}
	r.statusMu.Lock()
	r.status = update
	r.statusMu.Unlock()
}

// Run 按 ticker 周期驱动 runOnce; active 由 InitState/SetActive 控制。
func (r *UnifiedRotationRunner) Run(ctx context.Context) {
	if r == nil {
		return
	}
	active := r.active.Load()
	interval := r.tickerInterval()
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 周期 interval=%s active=%v", interval, active)
	}
	r.runReconcileAll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.runCycle(ctx)
	for {
		select {
		case <-ctx.Done():
			if r.logger != nil {
				r.logger.Printf("[统一轮动] 已停止")
			}
			return
		case <-ticker.C:
			r.runCycle(ctx)
		}
	}
}

func (r *UnifiedRotationRunner) runCycle(ctx context.Context) {
	if r == nil {
		return
	}
	// 暂停时仍管理在场真仓; verified-close 走 closeVerifiedFn(交易所真仓时仍下单)。
	r.runManageAll(ctx)
	if !r.active.Load() {
		r.maybeLogPaused()
		return
	}
	if r.controlPlane != nil && r.controlPlane.IsGlobalHaltActive() {
		r.maybeLogControlPlaneHalt()
		return
	}
	if r.controlPlane != nil && !r.controlPlane.AllowNewOpens() {
		return
	}
	if r.controlPlane != nil && !r.controlPlane.RotationLiveEnabled() {
		return
	}
	// 扫描/decision 可能耗时数分钟; 异步执行以免阻塞 60s 安全管理循环。
	if !r.scanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.scanning.Store(false)
		r.runOnceSafe(ctx)
	}()
}

// runReconcileAll 启动时对各执行器调一次 reconcileOnStart,恢复在场真仓与断腿状态。
func (r *UnifiedRotationRunner) runReconcileAll(ctx context.Context) {
	if r == nil {
		return
	}
	for _, binding := range r.bindings {
		if binding == nil || binding.reconcileFn == nil {
			continue
		}
		r.runReconcileBinding(ctx, binding)
	}
}

func (r *UnifiedRotationRunner) runReconcileBinding(ctx context.Context, binding *rotationExecutorBinding) {
	defer func() {
		if rec := recover(); rec != nil && r != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] reconcile panic recovered strategy=%s: %v", binding.strategyType, rec)
		}
	}()
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 启动恢复: strategy=%s", binding.strategyType)
	}
	binding.reconcileFn(ctx, r.isLive())
}

// runManageAll 每轮安全管理:到期/强平距离/孤腿/断腿恢复等硬安全平仓。
// DecideRotation 负责轮动换仓(CLOSE/SWITCH);二者可能同时平同一仓,
// 依赖执行器 closePosition/CloseOnePositionVerified 的 verify-real 幂等(交易所已平则空转,不重复下单)。
func (r *UnifiedRotationRunner) runManageAll(ctx context.Context) {
	if r == nil {
		return
	}
	if r.controlPlane != nil && !r.controlPlane.AllowManageOrders() {
		return
	}
	for _, binding := range r.bindings {
		if binding == nil || binding.manageFn == nil {
			continue
		}
		r.runManageBinding(ctx, binding)
	}
}

func (r *UnifiedRotationRunner) runManageBinding(ctx context.Context, binding *rotationExecutorBinding) {
	defer func() {
		if rec := recover(); rec != nil && r != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] 安全管理 panic recovered strategy=%s: %v", binding.strategyType, rec)
		}
	}()
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 安全管理: strategy=%s", binding.strategyType)
	}
	binding.manageFn(ctx, r.manageLive())
	if r.delistGuard != nil {
		r.delistGuard.Refresh(ctx)
		r.delistGuard.manageBindingDelistCloses(ctx, binding, r.manageLive())
	}
}

func (r *UnifiedRotationRunner) maybeLogPaused() {
	if r == nil || r.logger == nil {
		return
	}
	r.lastPausedLogMu.Lock()
	defer r.lastPausedLogMu.Unlock()
	now := time.Now()
	interval := r.tickerInterval()
	if !r.lastPausedLogAt.IsZero() && now.Sub(r.lastPausedLogAt) < interval {
		return
	}
	r.logger.Printf("[统一轮动] 已暂停(active=false)")
	r.lastPausedLogAt = now
}

func (r *UnifiedRotationRunner) maybeLogControlPlaneHalt() {
	if r == nil || r.logger == nil {
		return
	}
	r.lastPausedLogMu.Lock()
	defer r.lastPausedLogMu.Unlock()
	now := time.Now()
	interval := r.tickerInterval()
	if !r.lastPausedLogAt.IsZero() && now.Sub(r.lastPausedLogAt) < interval {
		return
	}
	r.logger.Printf("[统一轮动] 控制面 global_halt 生效,跳过新开仓(在场持仓管理照常)")
	r.lastPausedLogAt = now
}

func (r *UnifiedRotationRunner) tickerInterval() time.Duration {
	return 60 * time.Second
}

func (r *UnifiedRotationRunner) snapshotStale(scannedAt, now time.Time) bool {
	if scannedAt.IsZero() {
		return false
	}
	return now.Sub(scannedAt.UTC()) > rotationSnapshotMaxAge
}

func (r *UnifiedRotationRunner) maybeLogStaleSnapshot(scannedAt, now time.Time) {
	if r == nil || r.logger == nil {
		return
	}
	r.lastStaleSnapshotLogMu.Lock()
	defer r.lastStaleSnapshotLogMu.Unlock()
	interval := r.tickerInterval()
	if !r.lastStaleSnapshotLogAt.IsZero() && now.Sub(r.lastStaleSnapshotLogAt) < interval {
		return
	}
	age := crossVenueSnapshotAgeSeconds(scannedAt, now)
	r.logger.Printf("[统一轮动] scanner 快照过旧 age=%.0fs, 跳过本轮", age)
	r.lastStaleSnapshotLogAt = now
}

func (r *UnifiedRotationRunner) runOnceSafe(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil && r != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] panic recovered: %v", rec)
		}
	}()
	if err := r.runOnce(ctx); err != nil && r.logger != nil {
		r.logger.Printf("[统一轮动] 本轮失败(已吞掉): %v", err)
	}
}

// runOnce 单轮闭环: 产候选 → 收持仓 → 决策 → 资金路由 → 派发。
func (r *UnifiedRotationRunner) runOnce(ctx context.Context) error {
	if r == nil {
		return nil
	}
	cycleAt := time.Now().UTC()
	if r.nowFn != nil {
		cycleAt = r.nowFn().UTC()
	}
	statusUpdate := RotationStatus{
		LastCycleAt: cycleAt.Format(time.RFC3339),
	}
	defer func() {
		statusUpdate.Active = r.active.Load()
		statusUpdate.IntervalSeconds = int(r.tickerInterval().Seconds())
		r.commitStatus(statusUpdate)
	}()

	snap := CrossVenueScanSnapshot{}
	if r.scanner != nil {
		snap = r.scanner.Snapshot()
	}
	snapshotID := strings.TrimSpace(snap.SnapshotID)
	statusUpdate.LastSnapshotID = snapshotID
	if snapshotID == "" {
		return nil
	}
	if r.snapshotStale(snap.ScannedAt, cycleAt) {
		age := crossVenueSnapshotAgeSeconds(snap.ScannedAt, cycleAt)
		statusUpdate.LastError = fmt.Sprintf("scanner snapshot stale: age=%.0fs > max=%.0fs", age, rotationSnapshotMaxAge.Seconds())
		r.maybeLogStaleSnapshot(snap.ScannedAt, cycleAt)
		return nil
	}
	r.lastSnapshotID = snapshotID

	candidates, available, err := r.scanRotationCandidates(ctx, snap)
	if err != nil {
		statusUpdate.LastError = err.Error()
		return err
	}
	statusUpdate.Candidates = countRotationCandidates(candidates)
	borrowDaily, _ := rotationBorrowMapsFromProbes(snap.BorrowProbes)
	held, heldInputs := r.collectHeldPositions(candidates, borrowDaily)
	statusUpdate.Held = len(held)
	statusUpdate.RealHeldManaged = r.countRealHeldPositions(ctx)
	freeAvailable := rotationFreeAvailableUSDT(available, held)
	plan := r.runOnceDecisionAndDispatch(ctx, candidates, freeAvailable, held, heldInputs)
	statusUpdate.Actions = countRotationActions(plan)
	return nil
}

// runOnceDecisionAndDispatch 纯决策+路由+派发(便于单测注入)。
func (r *UnifiedRotationRunner) runOnceDecisionAndDispatch(
	ctx context.Context,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
) RotationPlan {
	if r == nil {
		return RotationPlan{}
	}
	defer func() {
		if rec := recover(); rec != nil && r.logger != nil {
			r.logger.Printf("[统一轮动] decision/dispatch panic recovered: %v", rec)
		}
	}()

	live := r.openLive()
	plan := r.decideRotation(held, heldInputs, candidates, available)
	if r.logger != nil {
		r.logger.Printf("[统一轮动] 决策完成: held=%d candidates=%d actions=%d live=%v",
			len(held), len(candidates), len(plan.Actions), live)
	}

	candidatesByID := indexUnifiedOpportunitiesByID(candidates)
	toFund := rotationOpportunitiesToFund(plan, candidatesByID)
	if len(toFund) > 0 {
		balances := rotationWalletBalancesFromAvailable(available, held, r.execCfg, r.venues, nil)
		routerCfg := CapitalRouterConfigWithVenues(r.capitalRouterCfg, r.venues)
		routingPlan := PlanCapitalRouting(toFund, balances, routerCfg)
		r.storeLastCapitalSnapshotContext(toFund, routingPlan)
		if r.logger != nil && len(routingPlan.WithinVenueTransfers) > 0 {
			r.logger.Printf("[统一轮动] 资金路由: %d 笔所内划转 live=%v", len(routingPlan.WithinVenueTransfers), live)
		}
		execResult := ExecuteCapitalRouting(ctx, routingPlan, r.venues, live, r.logger)
		if r.logger != nil {
			r.logger.Printf("[统一轮动] 资金路由执行: executed=%d errs=%d live=%v", execResult.Executed, len(execResult.Errs), live)
		}
		unfundable := rotationUnfundableSet(routingPlan.Unfundable)
		for _, e := range execResult.Errs {
			if r.logger != nil {
				r.logger.Printf("[统一轮动] 资金路由错误(阻断相关开仓): %v", e)
			}
		}
		for _, id := range execResult.FailedTransferOpportunityIDs {
			unfundable[id] = struct{}{}
		}
		if len(execResult.FailedTransferOpportunityIDs) > 0 && r.logger != nil {
			r.logger.Printf("[统一轮动] 划转失败阻断开仓: %v", execResult.FailedTransferOpportunityIDs)
		}
		if len(routingPlan.Unfundable) > 0 && r.logger != nil {
			r.logger.Printf("[统一轮动] 无法备资机会: %v", routingPlan.Unfundable)
		}
		r.dispatchRotationPlan(ctx, live, plan, candidatesByID, unfundable)
		return plan
	}
	r.storeLastCapitalSnapshotContext(nil, CapitalRoutingPlan{})
	r.dispatchRotationPlan(ctx, live, plan, candidatesByID, nil)
	return plan
}

func countRotationCandidates(candidates []UnifiedOpportunity) RotationCandidateCounts {
	counts := RotationCandidateCounts{Total: len(candidates)}
	for _, c := range candidates {
		switch c.StrategyType {
		case UnifiedStrategySingleVenueSpotPerp:
			counts.SVSP++
		case UnifiedStrategyCrossVenueSpotPerp:
			counts.CVSP++
		case UnifiedStrategySingleVenuePerpSpot:
			counts.SVPS++
		case UnifiedStrategyCrossVenuePerpSpot:
			counts.CVPS++
		case UnifiedStrategyPerpPerp:
			counts.PerpPerp++
		}
	}
	return counts
}

func countRotationActions(plan RotationPlan) RotationActionCounts {
	counts := RotationActionCounts{}
	for _, act := range plan.Actions {
		switch act.Kind {
		case RotationActionOpen:
			counts.Open++
		case RotationActionClose:
			counts.Close++
		case RotationActionSwitch:
			counts.Switch++
		case RotationActionHold:
			counts.Hold++
		}
	}
	return counts
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
	equityUSDT := 0.0
	if r != nil && strings.TrimSpace(r.recordDir) != "" {
		current, _, _ := computeRealEquityWaterMark(r.recordDir)
		equityUSDT = current
	}
	return DecideRotationWithBlocked(held, heldInputs, candidates, available, r.rotationCfg, r.rotationOpenBlockedPredicate(), equityUSDT)
}

func (r *UnifiedRotationRunner) rotationOpenBlockedPredicate() RotationOpenBlocked {
	if r == nil {
		return nil
	}
	return func(strategyType, symbol string) bool {
		b := r.bindingForStrategy(strategyType)
		if b == nil || b.openBlockedFn == nil {
			return false
		}
		return b.openBlockedFn(symbol)
	}
}

// rotationScanFilterSymbols 从 funding 快照中筛出 max|rate/h| 达标的 symbol, 按 per-hour 降序取前 rotationScanMaxSymbols 个。
func rotationScanFilterSymbols(fundingByVenue map[string]map[string]CrossVenueSpotPerpFundingSnap) []string {
	allSymbols := crossVenueSpotPerpCollectSymbols(fundingByVenue, nil)
	if len(allSymbols) == 0 {
		return nil
	}
	minPerHour := rotationFundingRatePerHour(rotationScanMinAbsFundingRate, 8)
	maxPerHourBySym := make(map[string]float64, len(allSymbols))
	for _, sym := range allSymbols {
		maxPerHour := 0.0
		for _, bySym := range fundingByVenue {
			entry, ok := bySym[sym]
			if !ok {
				continue
			}
			perHour := rotationFundingRatePerHour(entry.Rate, entry.IntervalHours)
			if perHour > maxPerHour {
				maxPerHour = perHour
			}
		}
		if maxPerHour >= minPerHour {
			maxPerHourBySym[sym] = maxPerHour
		}
	}
	filtered := make([]string, 0, len(maxPerHourBySym))
	for sym := range maxPerHourBySym {
		filtered = append(filtered, sym)
	}
	sort.Slice(filtered, func(i, j int) bool {
		ri, rj := maxPerHourBySym[filtered[i]], maxPerHourBySym[filtered[j]]
		if ri != rj {
			return ri > rj
		}
		return filtered[i] < filtered[j]
	})
	if len(filtered) > rotationScanMaxSymbols {
		filtered = filtered[:rotationScanMaxSymbols]
	}
	return filtered
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
		deps.fetchMarkets = defaultUnifiedMarketFetchMarketsByVenue
	}
	if deps.fetchAvailableUSDT == nil {
		deps.fetchAvailableUSDT = defaultUnifiedMarketFetchAvailableUSDTByVenue
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
	writeCycleID := strings.TrimSpace(snap.SnapshotID)
	if writeCycleID == "" {
		writeCycleID = crossVenueDecisionCycleID(now)
	}

	fundingByVenue := unifiedMarketFundingByVenueFromSnapshot(snap.FundingByVenue)
	symbols := rotationScanFilterSymbols(fundingByVenue)
	if r.delistGuard != nil {
		r.delistGuard.Refresh(ctx)
	}
	marketsByVenue := deps.fetchMarkets(ctx, r.venues, symbols)
	// 决策期流动性自适应:对粗筛 symbol 二次拉深度/24h 量,填 MarketSnap.Depth/Volume 字段。
	unifiedMarketEnrichMarketsWithDepthVolume(ctx, r.venues, marketsByVenue, symbols)
	availableUSDTByVenue := deps.fetchAvailableUSDT(ctx, r.venues, crossVenueExecutorVenues)
	borrowDaily, borrowLimits := rotationBorrowMapsFromProbes(snap.BorrowProbes)

	// 扫描器 sizing 必须扣掉与 allocator(rotationCanOpenOpportunity)一致的每所储备金,否则
	// 扫描器会把仓位撑到 100% 可用资金,而 allocator 留 reserve 后就再也无法注资 → 净正候选永远开不出来。
	// 这里给扫描器一份"扣掉储备金后的可用"用于定量;返回给 allocator 的仍是全额(allocator 自行扣储备)。
	scannerAvailableUSDT := make(map[string]float64, len(availableUSDTByVenue))
	for venueName, amt := range availableUSDTByVenue {
		eff := amt - rotationReserveUSDT(r.rotationCfg, venueName)
		if eff < 0 {
			eff = 0
		}
		scannerAvailableUSDT[venueName] = eff
	}

	singleSpotPerp := ScanSingleVenueSpotPerpOpportunities(SingleVenueSpotPerpScanInput{
		SnapshotID:           snapshotID,
		Now:                  now,
		Symbols:              symbols,
		Venues:               crossVenueExecutorVenues,
		FundingByVenue:       fundingByVenue,
		MarketsByVenue:       marketsByVenue,
		AvailableUSDTByVenue: scannerAvailableUSDT,
		Config:               DefaultSingleVenueSpotPerpScannerConfig(r.execCfg),
	})
	crossSpotPerp := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID:           snapshotID,
		Now:                  now,
		Symbols:              symbols,
		FundingByVenue:       fundingByVenue,
		MarketsByVenue:       marketsByVenue,
		AvailableUSDTByVenue: scannerAvailableUSDT,
		Config:               DefaultCrossVenueSpotPerpScannerConfig(r.execCfg),
	})
	singlePerpSpot := ScanSingleVenuePerpSpotOpportunities(SingleVenuePerpSpotScanInput{
		SnapshotID:             snapshotID,
		Now:                    now,
		Symbols:                symbols,
		Venues:                 crossVenueExecutorVenues,
		FundingByVenue:         fundingByVenue,
		MarketsByVenue:         marketsByVenue,
		AvailableUSDTByVenue:   scannerAvailableUSDT,
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
		AvailableUSDTByVenue:   scannerAvailableUSDT,
		BorrowDailyRateByAsset: borrowDaily,
		BorrowLimitUSDTByAsset: borrowLimits,
		Config:                 DefaultCrossVenuePerpSpotScannerConfig(r.execCfg),
	})

	candidates := make([]UnifiedOpportunity, 0, len(singleSpotPerp)+len(crossSpotPerp)+len(singlePerpSpot)+len(crossPerpSpot))
	candidates = append(candidates, singleSpotPerp...)
	candidates = append(candidates, crossSpotPerp...)
	candidates = append(candidates, singlePerpSpot...)
	candidates = append(candidates, crossPerpSpot...)
	if r.delistGuard != nil {
		candidates = appendDelistRejectedCandidates(r.delistGuard, snapshotID, symbols, candidates)
		candidates = applyDelistOpenGateToCandidates(r.delistGuard, candidates)
	}

	if r.includePerpPerpCandidates {
		for _, opp := range snap.Opportunities {
			unified := rotationAlignPerpPerpCapital(
				UnifiedOpportunityFromCrossVenue(r.execCfg, opp, nil, 0, 0),
				r.execCfg,
			)
			// 无 perp-perp binding 时仅作候选对比;有 binding 时可由统一 runner OPEN/SWITCH。
			if r.bindingForStrategy(UnifiedStrategyPerpPerp) == nil {
				unified.CandidateOnly = true
			}
			candidates = append(candidates, unified)
		}
	}

	if strings.TrimSpace(r.recordDir) != "" {
		AppendSingleVenueSpotPerpCandidates(r.recordDir, writeCycleID, r.execCfg, singleSpotPerp)
		AppendCrossVenueSpotPerpCandidates(r.recordDir, writeCycleID, r.execCfg, crossSpotPerp)
		AppendSingleVenuePerpSpotCandidates(r.recordDir, writeCycleID, r.execCfg, singlePerpSpot)
		AppendCrossVenuePerpSpotCandidates(r.recordDir, writeCycleID, r.execCfg, crossPerpSpot)
	}

	if r.logger != nil {
		perpPerpN := 0
		if r.includePerpPerpCandidates {
			perpPerpN = len(snap.Opportunities)
		}
		r.logger.Printf("[统一轮动] 扫描完成 snapshot=%s funding_symbols=%d candidates=%d (svsp=%d cvsp=%d svps=%d cvps=%d perp_perp=%d)",
			snapshotID, len(symbols), len(candidates), len(singleSpotPerp), len(crossSpotPerp), len(singlePerpSpot), len(crossPerpSpot), perpPerpN)
	}
	return candidates, availableUSDTByVenue, nil
}

func (r *UnifiedRotationRunner) collectHeldPositions(candidates []UnifiedOpportunity, borrowDailyByAsset map[string]float64) ([]HeldPosition, map[string]ContinuationInputs) {
	held := make([]HeldPosition, 0)
	inputs := map[string]ContinuationInputs{}
	candidatesByID := indexUnifiedOpportunitiesByID(candidates)
	now := time.Now().UTC()
	if r != nil && r.nowFn != nil {
		now = r.nowFn().UTC()
	}

	for _, binding := range r.bindings {
		if binding == nil || binding.heldFn == nil {
			continue
		}
		for _, rec := range binding.heldFn() {
			pos := rec.HeldPosition
			if strings.TrimSpace(pos.OpportunityID) == "" {
				continue
			}
			notional := rotationContinuationNotionalFromCloseCost(rec.ExpectedCloseCostUSDT, r.execCfg)
			cont := rotationBuildContinuationInputs(
				pos, rec.ContinuationInputs, candidatesByID[pos.OpportunityID],
				r.execCfg, borrowDailyByAsset, now, notional,
			)
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
			if !r.closeLive() {
				continue
			}
			if err := r.closeHeldVerified(ctx, act.StrategyType, act.OpportunityID, act.Symbol, act.Reason); err != nil && r.logger != nil {
				r.logger.Printf("[统一轮动] CLOSE 失败(已吞掉) %s: %v", act.OpportunityID, err)
			}
		case RotationActionSwitch:
			if !r.closeLive() {
				continue
			}
			// 事务性换仓: 平旧失败则中止,禁止开新;平旧成功但开新失败须显式落盘(旧仓已平、新仓未开)。
			if err := r.closeHeldVerified(ctx, act.StrategyType, act.OpportunityID, act.Symbol, act.Reason); err != nil {
				r.recordRotationSwitchIncident(act, rotationSwitchAbortedCloseFailed, err.Error())
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 中止(平旧失败) %s symbol=%s: %v",
						act.OpportunityID, act.Symbol, err)
				}
				continue
			}
			if _, skip := unfundable[act.ReplaceOpportunityID]; skip {
				r.recordRotationSwitchIncident(act, rotationSwitchFailedOpen, "无法备资")
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 开新失败(旧仓已平,无法备资) old=%s replace=%s symbol=%s",
						act.OpportunityID, act.ReplaceOpportunityID, act.Symbol)
				}
				continue
			}
			opp, ok := candidatesByID[act.ReplaceOpportunityID]
			if !ok {
				r.recordRotationSwitchIncident(act, rotationSwitchFailedOpen, "候选缺失")
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 开新失败(旧仓已平,候选缺失) old=%s replace=%s",
						act.OpportunityID, act.ReplaceOpportunityID)
				}
				continue
			}
			if err := r.openOpportunity(ctx, act.ReplaceStrategyType, opp, live); err != nil {
				r.recordRotationSwitchIncident(act, rotationSwitchFailedOpen, err.Error())
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 开新失败(旧仓已平) old=%s replace=%s symbol=%s: %v",
						act.OpportunityID, act.ReplaceOpportunityID, act.ReplaceSymbol, err)
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
		// 防御兜底: 决策层应已通过 CandidateOnly 排除不可执行候选，正常流程不应走到此处。
		if r.logger != nil {
			r.logger.Printf("[统一轮动] OPEN 防御跳过(无执行器绑定) %s strategy_type=%s", opp.ID, strategyType)
		}
		return nil
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] OPEN %s %s live=%v", strategyType, opp.Symbol, live)
	}
	if err := binding.openFn(ctx, opp, live); err != nil {
		return err
	}
	appendAttributionPredictionBestEffort(r.recordDir, predictionRecordFromOpportunity(opp, time.Now().UTC()))
	return nil
}

// recordRotationSwitchIncident 把 SWITCH 事务失败写入 domain_events.jsonl(失败静默,不阻断轮动)。
func (r *UnifiedRotationRunner) recordRotationSwitchIncident(act RotationAction, kind EventType, detail string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	if r.nowFn != nil {
		now = r.nowFn().UTC()
	}
	level := EventLevelWarning
	if kind == rotationSwitchFailedOpen {
		level = EventLevelCritical
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] SWITCH 事件 kind=%s old=%s replace=%s old_symbol=%s replace_symbol=%s detail=%s",
			kind, act.OpportunityID, act.ReplaceOpportunityID, act.Symbol, act.ReplaceSymbol, detail)
	}
	if strings.TrimSpace(r.recordDir) == "" {
		return
	}
	event := DomainEvent{
		Type:       kind,
		Level:      level,
		OccurredAt: now,
		CycleID:    strings.TrimSpace(r.lastSnapshotID),
		Protocol:   "unified_rotation.switch",
		Successful: false,
		Reason: fmt.Sprintf("old=%s replace=%s old_symbol=%s replace_symbol=%s strategy=%s replace_strategy=%s detail=%s",
			act.OpportunityID, act.ReplaceOpportunityID, act.Symbol, act.ReplaceSymbol,
			act.StrategyType, act.ReplaceStrategyType, strings.TrimSpace(detail)),
	}
	_ = appendJSONLRecord(filepath.Join(r.recordDir, domainEventJSONLName), event)
}

func (r *UnifiedRotationRunner) closeHeldVerified(ctx context.Context, strategyType, opportunityID, symbol, reason string) error {
	binding := r.bindingForStrategy(strategyType)
	if binding == nil || binding.closeVerifiedFn == nil {
		return fmt.Errorf("无执行器绑定 strategy_type=%s", strategyType)
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] CLOSE(verified) %s %s reason=%s",
			strategyType, symbol, reason)
	}
	return binding.closeVerifiedFn(ctx, opportunityID, symbol, reason)
}

func (r *UnifiedRotationRunner) countRealHeldPositions(ctx context.Context) int {
	if r == nil {
		return 0
	}
	total := 0
	for _, binding := range r.bindings {
		if binding == nil || binding.realHeldCountFn == nil {
			continue
		}
		total += binding.realHeldCountFn(ctx)
	}
	return total
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
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			return e.CloseOnePositionVerified(ctx, opportunityID, symbol, reason)
		},
		realHeldCountFn: e.CountRealHeldPositions,
		heldFn: func() []rotationHeldRecord {
			return collectSingleVenueSpotPerpHeldRecords(e.snapshotPositions(), e.execCfg())
		},
		reconcileFn: func(ctx context.Context, live bool) {
			e.ReconcileOnStart(ctx, live)
		},
		manageFn: e.ManageOpenPositions,
	}
}

func bindCrossVenueSpotPerpRotationExecutor(e *CrossVenueSpotPerpExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategyCrossVenueSpotPerp,
		openBlockedFn: func(symbol string) bool {
			return e.OpenBlockedForSymbol(symbol)
		},
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			return e.CloseOnePositionVerified(ctx, opportunityID, symbol, reason)
		},
		realHeldCountFn: e.CountRealHeldPositions,
		heldFn: func() []rotationHeldRecord {
			return collectCrossVenueSpotPerpHeldRecords(e.snapshotPositions(), e.cfg.CrossVenueExecutor)
		},
		reconcileFn: func(ctx context.Context, live bool) {
			e.ReconcileOnStart(ctx, live)
		},
		manageFn: e.ManageOpenPositions,
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
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			return e.CloseOnePositionVerified(ctx, opportunityID, symbol, reason)
		},
		realHeldCountFn: e.CountRealHeldPositions,
		heldFn: func() []rotationHeldRecord {
			return collectSingleVenuePerpSpotHeldRecords(e.snapshotPositions(), e.execCfg())
		},
		reconcileFn: func(ctx context.Context, live bool) {
			e.ReconcileOnStart(ctx, live)
		},
		manageFn: e.ManageOpenPositions,
	}
}

func bindCrossVenuePerpSpotRotationExecutor(e *CrossVenuePerpSpotExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategyCrossVenuePerpSpot,
		openBlockedFn: func(symbol string) bool {
			return e.OpenBlockedForSymbol(symbol)
		},
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			return e.CloseOnePositionVerified(ctx, opportunityID, symbol, reason)
		},
		realHeldCountFn: e.CountRealHeldPositions,
		heldFn: func() []rotationHeldRecord {
			return collectCrossVenuePerpSpotHeldRecords(e.snapshotPositions(), e.cfg.CrossVenueExecutor)
		},
		reconcileFn: func(ctx context.Context, live bool) {
			e.ReconcileOnStart(ctx, live)
		},
		manageFn: e.ManageOpenPositions,
	}
}

func bindCrossVenuePerpPerpRotationExecutor(e *CrossVenueExecutor) *rotationExecutorBinding {
	if e == nil {
		return nil
	}
	return &rotationExecutorBinding{
		strategyType: UnifiedStrategyPerpPerp,
		openBlockedFn: func(symbol string) bool {
			return e.OpenBlockedForSymbol(symbol)
		},
		openFn: func(ctx context.Context, opp UnifiedOpportunity, live bool) error {
			_, err := e.executeOpen(ctx, opp, live)
			return err
		},
		closeVerifiedFn: func(ctx context.Context, opportunityID, symbol, reason string) error {
			return e.CloseOnePositionVerified(ctx, opportunityID, symbol, reason)
		},
		realHeldCountFn: e.CountRealHeldPositions,
		heldFn: func() []rotationHeldRecord {
			return collectCrossVenueHeldRecords(e.snapshotPositions(), e.cfg.CrossVenueExecutor)
		},
		reconcileFn: func(ctx context.Context, live bool) {
			e.ReconcileOnStart(ctx, live)
		},
		manageFn: e.ManageOpenPositions,
	}
}

func collectCrossVenueHeldRecords(ps []CrossVenuePosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	now := time.Now().UTC()
	for _, p := range activeCrossVenuePositions(ps) {
		oppID := crossVenuePositionOpportunityID(p)
		if strings.TrimSpace(oppID) == "" {
			continue
		}
		notional := crossVenuePositionNotionalUSDT(&p)
		margin := rotationPerpMarginWithBuffer(execCfg, notional)
		capital := map[string]float64{
			p.HighVenue: margin,
			p.LowVenue:  margin,
		}
		windowFunding := rotationPerpPerpWindowFundingUSDT(
			notional, p.HighFundingRate, p.HighIntervalHours, p.LowFundingRate, p.LowIntervalHours,
			p.HighNextFundingTime, p.LowNextFundingTime, now,
		)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          oppID,
				StrategyType:           UnifiedStrategyPerpPerp,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
				OpenedAt:               p.OpenedAt,
				NextFundingTime:        minTime(p.HighNextFundingTime, p.LowNextFundingTime),
			},
			ContinuationInputs: rotationContinuationFromPosition(windowFunding, notional, execCfg, false, rotationContinuationOpts{}),
			delistLegs: []rotationDelistLeg{
				{Venue: p.HighVenue, Market: venue.MarketPerp},
				{Venue: p.LowVenue, Market: venue.MarketPerp},
			},
		})
	}
	return out
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
	now := time.Now().UTC()
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateSingleVenueSpotPerpCapital(p, execCfg)
		interval := p.FundingIntervalHours
		if interval <= 0 {
			interval = 8
		}
		windowFunding := rotationSpotPerpWindowFundingUSDT(p.NotionalUSDT, p.OpenFundingRate, interval, p.NextFundingTime, now, false)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategySingleVenueSpotPerp,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
				OpenedAt:               p.OpenedAt,
				NextFundingTime:        p.NextFundingTime,
			},
			ContinuationInputs: rotationContinuationFromPosition(windowFunding, p.NotionalUSDT, execCfg, false, rotationContinuationOpts{baseAsset: baseAssetFromCanonicalSymbol(p.Symbol)}),
			delistLegs: []rotationDelistLeg{
				{Venue: p.Venue, Market: venue.MarketSpot},
				{Venue: p.Venue, Market: venue.MarketPerp},
			},
		})
	}
	return out
}

func collectCrossVenueSpotPerpHeldRecords(ps []CrossVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	now := time.Now().UTC()
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateCrossVenueSpotPerpCapital(p, execCfg)
		interval := p.PerpIntervalHours
		if interval <= 0 {
			interval = 8
		}
		windowFunding := rotationSpotPerpWindowFundingUSDT(p.NotionalUSDT, p.PerpFundingRate, interval, p.PerpNextFundingTime, now, false)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategyCrossVenueSpotPerp,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
				OpenedAt:               p.OpenedAt,
				NextFundingTime:        p.PerpNextFundingTime,
			},
			ContinuationInputs: rotationContinuationFromPosition(windowFunding, p.NotionalUSDT, execCfg, false, rotationContinuationOpts{baseAsset: baseAssetFromCanonicalSymbol(p.Symbol)}),
			delistLegs: []rotationDelistLeg{
				{Venue: p.SpotVenue, Market: venue.MarketSpot},
				{Venue: p.PerpVenue, Market: venue.MarketPerp},
			},
		})
	}
	return out
}

func collectSingleVenuePerpSpotHeldRecords(ps []SingleVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	now := time.Now().UTC()
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateSingleVenuePerpSpotCapital(p, execCfg)
		interval := p.FundingIntervalHours
		if interval <= 0 {
			interval = 8
		}
		windowFunding := rotationSpotPerpWindowFundingUSDT(p.NotionalUSDT, p.OpenFundingRate, interval, p.NextFundingTime, now, true)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategySingleVenuePerpSpot,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
				OpenedAt:               p.OpenedAt,
				NextFundingTime:        p.NextFundingTime,
			},
			ContinuationInputs: rotationContinuationFromPosition(windowFunding, p.NotionalUSDT, execCfg, true, rotationContinuationOpts{baseAsset: baseAssetFromCanonicalSymbol(p.Symbol)}),
			delistLegs: []rotationDelistLeg{
				{Venue: p.Venue, Market: venue.MarketSpot},
				{Venue: p.Venue, Market: venue.MarketPerp},
			},
		})
	}
	return out
}

func collectCrossVenuePerpSpotHeldRecords(ps []CrossVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) []rotationHeldRecord {
	out := make([]rotationHeldRecord, 0)
	now := time.Now().UTC()
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if strings.TrimSpace(p.OpportunityID) == "" {
			continue
		}
		capital := estimateCrossVenuePerpSpotCapital(p, execCfg)
		interval := p.PerpIntervalHours
		if interval <= 0 {
			interval = 8
		}
		windowFunding := rotationSpotPerpWindowFundingUSDT(p.NotionalUSDT, p.PerpFundingRate, interval, p.PerpNextFundingTime, now, true)
		out = append(out, rotationHeldRecord{
			HeldPosition: HeldPosition{
				OpportunityID:          p.OpportunityID,
				StrategyType:           UnifiedStrategyCrossVenuePerpSpot,
				Symbol:                 p.Symbol,
				CapitalRequiredByVenue: capital,
				OpenedAt:               p.OpenedAt,
				NextFundingTime:        p.PerpNextFundingTime,
			},
			ContinuationInputs: rotationContinuationFromPosition(windowFunding, p.NotionalUSDT, execCfg, true, rotationContinuationOpts{baseAsset: baseAssetFromCanonicalSymbol(p.Symbol)}),
			delistLegs: []rotationDelistLeg{
				{Venue: p.SpotVenue, Market: venue.MarketSpot},
				{Venue: p.PerpVenue, Market: venue.MarketPerp},
			},
		})
	}
	return out
}

func estimateSingleVenueSpotPerpCapital(pos SingleVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.SpotLeg.Qty * pos.SpotLeg.EntryPrice
	}
	margin := rotationPerpMarginWithBuffer(execCfg, notional)
	return map[string]float64{pos.Venue: notional + margin}
}

func estimateCrossVenueSpotPerpCapital(pos CrossVenueSpotPerpPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.SpotLeg.Qty * pos.SpotLeg.EntryPrice
	}
	return map[string]float64{
		pos.SpotVenue: notional,
		pos.PerpVenue: rotationPerpMarginWithBuffer(execCfg, notional),
	}
}

func estimateSingleVenuePerpSpotCapital(pos SingleVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	margin := rotationPerpMarginWithBuffer(execCfg, notional)
	return map[string]float64{pos.Venue: notional + margin}
}

func estimateCrossVenuePerpSpotCapital(pos CrossVenuePerpSpotPosition, execCfg CrossVenueExecutorConfig) map[string]float64 {
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	return map[string]float64{
		pos.SpotVenue: notional,
		pos.PerpVenue: rotationPerpMarginWithBuffer(execCfg, notional),
	}
}

func rotationPerpMarginWithBuffer(execCfg CrossVenueExecutorConfig, notional float64) float64 {
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	bufferRatio := scannerCfg.PerpWalletBufferRatio
	if bufferRatio <= 0 {
		bufferRatio = crossVenueSpotPerpDefaultPerpWalletBuffer
	}
	return margin + notional*bufferRatio
}

func rotationAlignPerpPerpCapital(opp UnifiedOpportunity, execCfg CrossVenueExecutorConfig) UnifiedOpportunity {
	if opp.StrategyType != UnifiedStrategyPerpPerp {
		return opp
	}
	notional := rotationMaxLegNotional(opp)
	if notional <= 0 {
		total := unifiedOpportunityTotalCapital(opp)
		if len(opp.Legs) > 0 {
			notional = total / float64(len(opp.Legs))
		} else {
			notional = total / 2
		}
	}
	margin := rotationPerpMarginWithBuffer(execCfg, notional)
	capByVenue := map[string]float64{}
	for _, leg := range opp.Legs {
		if leg.Venue != "" {
			capByVenue[leg.Venue] = margin
		}
	}
	if len(capByVenue) > 0 {
		opp.CapitalRequiredByVenue = capByVenue
	}
	return opp
}

func rotationInferNotionalFromTotalCapital(execCfg CrossVenueExecutorConfig, total float64) float64 {
	if total <= 0 {
		return 0
	}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	lev := execCfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	factor := execCfg.MarginSafetyFactor
	if factor <= 0 {
		factor = 1.5
	}
	buffer := scannerCfg.PerpWalletBufferRatio
	if buffer <= 0 {
		buffer = crossVenueSpotPerpDefaultPerpWalletBuffer
	}
	return total / (1 + factor/lev + buffer)
}

func rotationHeldWalletRequirements(pos HeldPosition, execCfg CrossVenueExecutorConfig) map[string]venueWalletRequirements {
	out := map[string]venueWalletRequirements{}
	switch pos.StrategyType {
	case UnifiedStrategyCrossVenueSpotPerp:
		rotationAssignCrossVenueWalletSplit(out, pos.CapitalRequiredByVenue, WalletKindSpot, WalletKindFutures)
	case UnifiedStrategyCrossVenuePerpSpot:
		rotationAssignCrossVenueWalletSplit(out, pos.CapitalRequiredByVenue, WalletKindMargin, WalletKindFutures)
	case UnifiedStrategySingleVenueSpotPerp:
		for venue, total := range pos.CapitalRequiredByVenue {
			notional := rotationInferNotionalFromTotalCapital(execCfg, total)
			perpMargin := rotationPerpMarginWithBuffer(execCfg, notional)
			spotCash := total - perpMargin
			if spotCash < 0 {
				spotCash = 0
			}
			req := venueWalletRequirements{}
			addWalletRequirement(req, WalletKindSpot, spotCash)
			addWalletRequirement(req, WalletKindFutures, perpMargin)
			out[venue] = req
		}
	case UnifiedStrategySingleVenuePerpSpot:
		for venue, total := range pos.CapitalRequiredByVenue {
			notional := rotationInferNotionalFromTotalCapital(execCfg, total)
			perpMargin := rotationPerpMarginWithBuffer(execCfg, notional)
			spotMargin := total - perpMargin
			if spotMargin < 0 {
				spotMargin = 0
			}
			req := venueWalletRequirements{}
			addWalletRequirement(req, WalletKindMargin, spotMargin)
			addWalletRequirement(req, WalletKindFutures, perpMargin)
			out[venue] = req
		}
	case UnifiedStrategyPerpPerp:
		for venue, amt := range pos.CapitalRequiredByVenue {
			req := venueWalletRequirements{}
			addWalletRequirement(req, WalletKindFutures, amt)
			out[venue] = req
		}
	default:
		for venue, amt := range pos.CapitalRequiredByVenue {
			req := venueWalletRequirements{}
			addWalletRequirement(req, WalletKindFutures, amt)
			out[venue] = req
		}
	}
	return out
}

func rotationAssignCrossVenueWalletSplit(out map[string]venueWalletRequirements, capitalByVenue map[string]float64, spotLike, perpLike WalletKind) {
	if len(capitalByVenue) == 0 {
		return
	}
	type venueAmt struct {
		venue string
		amt   float64
	}
	entries := make([]venueAmt, 0, len(capitalByVenue))
	for venue, amt := range capitalByVenue {
		entries = append(entries, venueAmt{venue: venue, amt: amt})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].amt == entries[j].amt {
			return entries[i].venue < entries[j].venue
		}
		return entries[i].amt > entries[j].amt
	})
	if len(entries) == 1 {
		req := venueWalletRequirements{}
		addWalletRequirement(req, spotLike, entries[0].amt)
		out[entries[0].venue] = req
		return
	}
	spotVenue := entries[0]
	perpVenue := entries[1]
	spotReq := venueWalletRequirements{}
	addWalletRequirement(spotReq, spotLike, spotVenue.amt)
	out[spotVenue.venue] = spotReq
	perpReq := venueWalletRequirements{}
	addWalletRequirement(perpReq, perpLike, perpVenue.amt)
	out[perpVenue.venue] = perpReq
}

func rotationWalletBalancesFromAvailable(fetched map[string]float64, held []HeldPosition, execCfg CrossVenueExecutorConfig, venues map[string]venue.Venue, pending []UnifiedOpportunity) VenueWalletBalances {
	unified := UnifiedWalletVenuesFromVenues(venues)
	totals := cloneVenueCapitalMap(fetched)
	for _, pos := range held {
		rotationAddCapitalMap(totals, pos.CapitalRequiredByVenue)
	}

	walletNeed := map[string]venueWalletRequirements{}
	for _, pos := range held {
		for venue, wallets := range rotationHeldWalletRequirements(pos, execCfg) {
			if walletNeed[venue] == nil {
				walletNeed[venue] = venueWalletRequirements{}
			}
			for w, amt := range wallets {
				addWalletRequirement(walletNeed[venue], w, amt)
			}
		}
	}
	for _, opp := range pending {
		reqByVenue := aggregateOpportunityRequirements(opp, DefaultCapitalRouterConfig())
		for venue, wallets := range reqByVenue {
			if walletNeed[venue] == nil {
				walletNeed[venue] = venueWalletRequirements{}
			}
			for w, amt := range wallets {
				addWalletRequirement(walletNeed[venue], w, amt)
			}
		}
	}

	out := VenueWalletBalances{}
	for venue, total := range totals {
		if total <= 0 {
			continue
		}
		if unified[venue] {
			out[venue] = map[WalletKind]float64{WalletKindSpot: total}
			continue
		}
		needs := walletNeed[venue]
		needTotal := venueTotalRequired(needs)
		if needTotal <= capitalRouterEpsilon {
			out[venue] = map[WalletKind]float64{WalletKindFutures: total}
			continue
		}
		wallets := map[WalletKind]float64{}
		for _, w := range capitalRouterWalletKinds {
			share := needs[w] / needTotal
			if share > capitalRouterEpsilon {
				wallets[w] = total * share
			}
		}
		out[venue] = wallets
	}
	return out
}

func rotationContinuationNotionalFromCloseCost(closeCost float64, execCfg CrossVenueExecutorConfig) float64 {
	if closeCost <= 0 {
		return 0
	}
	feeBps := execCfg.DefaultTakerFeeBps
	if feeBps <= 0 {
		feeBps = 4
	}
	denom := feeBps / 10000 * 2
	if denom <= 0 {
		return 0
	}
	return closeCost / denom
}

// rotationContinuationOpts 续持经济系数可选参数(零值=默认/回退)。
type rotationContinuationOpts struct {
	riskScore          float64
	totalCapital       float64
	baseAsset          string
	borrowDailyByAsset map[string]float64
	holdHours          float64
}

func rotationContinuationWindowHours(pos HeldPosition, now time.Time) float64 {
	if pos.NextFundingTime.IsZero() {
		return 24
	}
	h := crossVenueComparableWindowHours(now, pos.NextFundingTime, pos.NextFundingTime)
	if h <= 0 {
		return 24
	}
	return h
}

func rotationContinuationBorrowCostUSDT(notional float64, negativeCarry bool, baseAsset string, holdHours float64, borrowDailyByAsset map[string]float64) float64 {
	if !negativeCarry || notional <= 0 {
		return 0
	}
	if borrowDailyByAsset != nil && holdHours > 0 {
		if cost := perpSpotBorrowCostUSDT(baseAsset, notional, holdHours, borrowDailyByAsset); cost > 0 {
			return cost
		}
	}
	return notional * rotationContinuationBorrowFallbackDailyRate
}

func rotationBuildContinuationInputs(
	pos HeldPosition,
	base ContinuationInputs,
	cand UnifiedOpportunity,
	execCfg CrossVenueExecutorConfig,
	borrowDailyByAsset map[string]float64,
	now time.Time,
	notionalOverride float64,
) ContinuationInputs {
	negativeCarry := pos.StrategyType == UnifiedStrategySingleVenuePerpSpot || pos.StrategyType == UnifiedStrategyCrossVenuePerpSpot
	totalCapital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	notional := notionalOverride
	if notional <= 0 {
		notional = totalCapital
	}
	holdHours := rotationContinuationWindowHours(pos, now)
	baseAsset := baseAssetFromCanonicalSymbol(pos.Symbol)

	if strings.TrimSpace(cand.ID) != "" {
		borrowCost := 0.0
		if negativeCarry {
			borrowCost = rotationContinuationBorrowCostUSDT(notional, true, baseAsset, holdHours, borrowDailyByAsset)
		}
		return ContinuationInputs{
			ExpectedFundingUSDT:    cand.ExpectedFundingUSDT,
			ExpectedCloseCostUSDT:  cand.ExpectedCloseCostUSDT,
			HoldingRiskUSDT:        holdingRiskUSDT(cand.RiskScore, totalCapital),
			ExpectedBorrowCostUSDT: borrowCost,
		}
	}

	opts := rotationContinuationOpts{
		totalCapital:       totalCapital,
		baseAsset:          baseAsset,
		borrowDailyByAsset: borrowDailyByAsset,
		holdHours:          holdHours,
	}
	return rotationContinuationFromPosition(base.ExpectedFundingUSDT, notional, execCfg, negativeCarry, opts)
}

func rotationContinuationFromPosition(windowFundingUSDT, notional float64, execCfg CrossVenueExecutorConfig, negativeCarry bool, opts rotationContinuationOpts) ContinuationInputs {
	closeCost := 0.0
	if notional > 0 {
		feeBps := execCfg.DefaultTakerFeeBps
		if feeBps <= 0 {
			feeBps = 4
		}
		closeCost = notional * feeBps / 10000 * 2
	}
	capital := opts.totalCapital
	if capital <= 0 {
		capital = notional
	}
	riskScore := opts.riskScore
	if riskScore <= 0 {
		riskScore = rotationContinuationDefaultRiskScore
	}
	holdHours := opts.holdHours
	if holdHours <= 0 {
		holdHours = 24
	}
	borrowCost := rotationContinuationBorrowCostUSDT(notional, negativeCarry, opts.baseAsset, holdHours, opts.borrowDailyByAsset)
	return ContinuationInputs{
		ExpectedFundingUSDT:    windowFundingUSDT,
		ExpectedCloseCostUSDT:  closeCost,
		HoldingRiskUSDT:        holdingRiskUSDT(riskScore, capital),
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
