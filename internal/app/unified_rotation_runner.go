package app

import (
	"context"
	"fmt"
	"log"
	"math"
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
	// rotationScanMaxSymbolsFundingOnly 粗筛阶段无 24h 量/market 快照时的 honest 回退上限(纯 |rate/h| 排序)。
	rotationScanMaxSymbolsFundingOnly = 250
	// rotationScanDepthFetchMaxSymbols funding 粗筛后二次拉深度上限;与 rotationScanMaxSymbols 对齐以覆盖全部粗筛 symbol。
	// 实际深度拉取的限流/并发由 unified_market_pipeline 适配(本常量仅设 N 上限)。
	rotationScanDepthFetchMaxSymbols = 150

	// 统一轮动 SWITCH 事务性失败事件类型(domain_events.jsonl Type 字段,可检索)。
	rotationSwitchAbortedCloseFailed EventType = "switch_aborted_close_failed"
	rotationSwitchFailedOpen         EventType = "switch_failed_open"

	rotationZeroCVSPCVPSAlertThreshold = 5

	// rotationAsyncScanTimeout 异步 carry 全量扫描卡死防护:超时后重置 scanning 并告警。
	rotationAsyncScanTimeout = 25 * time.Minute
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
	Active               bool                    `json:"active"`
	ConfigIntendedActive bool                    `json:"config_intended_active"`
	PausedAgainstConfig  bool                    `json:"paused_against_config"`
	IntervalSeconds      int                     `json:"interval_seconds"`
	LastCycleAt          string                  `json:"last_cycle_at"`
	LastSnapshotID       string                  `json:"last_snapshot_id"`
	Candidates           RotationCandidateCounts `json:"candidates"`
	Held                 int                     `json:"held"`
	RealHeldManaged      int                     `json:"real_held_managed"`
	Actions              RotationActionCounts    `json:"actions"`
	LastError            string                  `json:"last_error"`
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

	// rotationConfigIntendedActive config 层 rotation 实盘意图(InitState defaultActive);与持久化 active 解耦供告警。
	rotationConfigIntendedActive bool

	scanStartedMu sync.Mutex
	scanStartedAt time.Time

	statusMu               sync.RWMutex
	status                 RotationStatus
	lastPausedLogMu        sync.Mutex
	lastPausedLogAt        time.Time
	lastPausedCriticalMu   sync.Mutex
	lastPausedCriticalAt   time.Time
	lastStaleSnapshotLogMu sync.Mutex
	lastStaleSnapshotLogAt time.Time

	lastCapitalSnapshotMu sync.RWMutex
	lastCapitalSnapshot   rotationCapitalSnapshotContext
	// controlPlane 统一控制面附加闸(nil=仅沿用 active 等既有语义)。
	controlPlane *ControlPlane

	openCooldownStore *rotationOpenCooldownStore

	portfolioEnableRollingHold bool
	rollingHoldWarnOnce        sync.Once

	consecutiveZeroCVSPCVPS int
	lastZeroCVAlertMu       sync.Mutex
	lastZeroCVAlertAt       time.Time
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
	r.openCooldownStore = newRotationOpenCooldownStore(
		r.recordDir,
		time.Duration(normalizeRotationConfig(rotationCfg).OpenFailCooldownSeconds)*time.Second,
	)
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

func (r *UnifiedRotationRunner) SetPortfolioEnableRollingHold(enabled bool) {
	if r == nil {
		return
	}
	r.portfolioEnableRollingHold = enabled
}

func (r *UnifiedRotationRunner) maybeWarnEnableRollingHoldNoOp() {
	if r == nil || !r.portfolioEnableRollingHold || r.logger == nil {
		return
	}
	r.rollingHoldWarnOnce.Do(func() {
		r.logger.Printf("[统一轮动] WARNING: enable_rolling_hold=true 在统一轮动路径下当前为 no-op;续持由 min-hold + 结算闸控制")
	})
}

// InitState 按默认 active 初始化;若 rotation_control.json 存在则用文件 active 覆盖(legacy live 字段忽略)。
func (r *UnifiedRotationRunner) InitState(defaultActive bool) {
	if r == nil {
		return
	}
	r.rotationConfigIntendedActive = defaultActive
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
	st.ConfigIntendedActive = r.rotationConfigIntendedActive
	st.PausedAgainstConfig = !st.Active && st.ConfigIntendedActive
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
	r.maybeWarnEnableRollingHoldNoOp()
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
	r.maybeResetStuckAsyncScan()
	if !r.active.Load() {
		r.maybeLogPausedAgainstConfig()
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
	// perp-perp:每 cycle 用缓存快照同步决策(不占用 carry 慢扫描 scanning 闸)。
	r.runPerpPerpDecisionFast(ctx)
	// carry 四策略扫描/decision 可能耗时数分钟; 异步执行以免阻塞 60s 安全管理循环。
	if !r.scanning.CompareAndSwap(false, true) {
		return
	}
	r.markAsyncScanStarted()
	go func() {
		defer r.clearAsyncScanStarted()
		r.runOnceSafe(ctx)
	}()
}

// runReconcileAll 启动时对各执行器调一次 reconcileOnStart,恢复在场真仓与断腿状态。
// carry 策略先于 perp-perp 对账,以便重采腿 qty 后 perp-perp 不误判未登记持仓。
func (r *UnifiedRotationRunner) runReconcileAll(ctx context.Context) {
	if r == nil {
		return
	}
	reconcileOrder := []string{
		UnifiedStrategySingleVenueSpotPerp,
		UnifiedStrategyCrossVenueSpotPerp,
		UnifiedStrategySingleVenuePerpSpot,
		UnifiedStrategyCrossVenuePerpSpot,
		UnifiedStrategyPerpPerp,
	}
	seen := make(map[string]struct{}, len(r.bindings))
	for _, strategyType := range reconcileOrder {
		if binding := r.bindings[strategyType]; binding != nil {
			r.runReconcileBinding(ctx, binding)
			seen[strategyType] = struct{}{}
		}
	}
	for strategyType, binding := range r.bindings {
		if _, ok := seen[strategyType]; ok {
			continue
		}
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
	if r.nowFn != nil {
		now = r.nowFn()
	}
	interval := r.tickerInterval()
	if !r.lastPausedLogAt.IsZero() && now.Sub(r.lastPausedLogAt) < interval {
		return
	}
	r.logger.Printf("[统一轮动] 已暂停(active=false)")
	r.lastPausedLogAt = now
}

func (r *UnifiedRotationRunner) maybeLogPausedAgainstConfig() {
	if r == nil || !r.rotationConfigIntendedActive || r.active.Load() {
		return
	}
	msg := "rotation 配置意图为实盘开启但 runner active=false,交易已暂停未恢复"
	r.statusMu.Lock()
	r.status.LastError = msg
	r.statusMu.Unlock()
	if r.logger == nil {
		return
	}
	r.lastPausedCriticalMu.Lock()
	defer r.lastPausedCriticalMu.Unlock()
	now := time.Now()
	if r.nowFn != nil {
		now = r.nowFn()
	}
	interval := r.tickerInterval()
	if !r.lastPausedCriticalAt.IsZero() && now.Sub(r.lastPausedCriticalAt) < interval {
		return
	}
	r.logger.Printf("[统一轮动] CRITICAL: %s", msg)
	r.lastPausedCriticalAt = now
}

func (r *UnifiedRotationRunner) markAsyncScanStarted() {
	if r == nil {
		return
	}
	now := time.Now()
	if r.nowFn != nil {
		now = r.nowFn()
	}
	r.scanStartedMu.Lock()
	r.scanStartedAt = now
	r.scanStartedMu.Unlock()
}

func (r *UnifiedRotationRunner) clearAsyncScanStarted() {
	if r == nil {
		return
	}
	r.scanning.Store(false)
	r.scanStartedMu.Lock()
	r.scanStartedAt = time.Time{}
	r.scanStartedMu.Unlock()
}

func (r *UnifiedRotationRunner) asyncScanRunningFor() time.Duration {
	if r == nil || !r.scanning.Load() {
		return 0
	}
	r.scanStartedMu.Lock()
	started := r.scanStartedAt
	r.scanStartedMu.Unlock()
	if started.IsZero() {
		return 0
	}
	now := time.Now()
	if r.nowFn != nil {
		now = r.nowFn()
	}
	return now.Sub(started)
}

func (r *UnifiedRotationRunner) maybeResetStuckAsyncScan() {
	if r == nil || !r.scanning.Load() {
		return
	}
	elapsed := r.asyncScanRunningFor()
	if elapsed <= 0 || elapsed < rotationAsyncScanTimeout {
		return
	}
	r.scanning.Store(false)
	r.scanStartedMu.Lock()
	r.scanStartedAt = time.Time{}
	r.scanStartedMu.Unlock()
	msg := fmt.Sprintf("异步 carry 扫描超时 age=%.0fs > max=%.0fs,已重置 scanning 标志",
		elapsed.Seconds(), rotationAsyncScanTimeout.Seconds())
	r.statusMu.Lock()
	r.status.LastError = msg
	r.statusMu.Unlock()
	if r.logger != nil {
		r.logger.Printf("[统一轮动] CRITICAL: %s", msg)
	}
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

func (r *UnifiedRotationRunner) fetchRotationAvailableUSDT(ctx context.Context) map[string]float64 {
	if r == nil {
		return nil
	}
	deps := r.fetchDeps
	if deps.fetchAvailableUSDT == nil {
		deps.fetchAvailableUSDT = defaultUnifiedMarketFetchAvailableUSDTByVenue
	}
	return deps.fetchAvailableUSDT(ctx, r.venues, crossVenueExecutorVenues)
}

func (r *UnifiedRotationRunner) buildPerpPerpCandidatesFromSnapshot(
	snap CrossVenueScanSnapshot,
	availableUSDTByVenue map[string]float64,
) []UnifiedOpportunity {
	if r == nil || !r.includePerpPerpCandidates || len(snap.Opportunities) == 0 {
		return nil
	}
	perpPerpCandidates := make([]UnifiedOpportunity, 0, len(snap.Opportunities))
	for _, opp := range snap.Opportunities {
		unified := UnifiedOpportunityFromCrossVenue(r.execCfg, opp, nil, 0, 0)
		if r.rotationCfg.CapitalAwarePerpPerpSizingEnabled {
			unified = rotationCapPerpPerpOpportunityByCapital(
				unified, r.execCfg, normalizeRotationConfig(r.rotationCfg), availableUSDTByVenue,
			)
		} else {
			unified = rotationAlignPerpPerpCapital(unified, r.execCfg)
		}
		if r.bindingForStrategy(UnifiedStrategyPerpPerp) == nil {
			unified.CandidateOnly = true
		}
		if opp.HighSymbol != "" && opp.LowSymbol != "" && opp.HighSymbol != opp.LowSymbol {
			unified.CandidateOnly = true
		}
		perpPerpCandidates = append(perpPerpCandidates, unified)
	}
	return perpPerpCandidates
}

// runPerpPerpDecisionFast 每 60s cycle 基于 scanner 内存快照评估 perp-perp 开仓/换仓(与 carry 慢扫描解耦)。
func (r *UnifiedRotationRunner) runPerpPerpDecisionFast(ctx context.Context) {
	if r == nil || !r.includePerpPerpCandidates || r.bindingForStrategy(UnifiedStrategyPerpPerp) == nil {
		return
	}
	if r.scanner == nil {
		return
	}
	cycleAt := time.Now().UTC()
	if r.nowFn != nil {
		cycleAt = r.nowFn().UTC()
	}
	snap := r.scanner.Snapshot()
	snapshotID := strings.TrimSpace(snap.SnapshotID)
	if snapshotID == "" {
		return
	}
	if r.snapshotStale(snap.ScannedAt, cycleAt) {
		return
	}

	available := r.fetchRotationAvailableUSDT(ctx)
	perpCandidates := r.buildPerpPerpCandidatesFromSnapshot(snap, available)
	if len(perpCandidates) == 0 {
		return
	}
	recordEarningLegDiag(r.recordDir, "perp_perp_fast", snapshotID, snapshotID, perpCandidates)
	borrowDaily, _ := rotationBorrowMapsFromProbes(snap.BorrowProbes)
	allHeld, allHeldInputs := r.collectHeldPositions(perpCandidates, borrowDaily)
	freeAvailable := rotationFreeAvailableUSDT(available, allHeld)
	rotationCfg := normalizeRotationConfig(r.rotationCfg)
	if r.openCooldownStore != nil {
		rotationCfg.OpenCooldownBlocked = r.openCooldownStore.blockedPredicate(r.nowFn)
	}
	rotationCandidates := filterRotationCandidates(perpCandidates, rotationCfg)
	if len(rotationCandidates) == 0 {
		return
	}

	plan := r.decideRotation(allHeld, allHeldInputs, rotationCandidates, freeAvailable)
	plan.Actions = filterRotationPlanPerpPerpActions(plan.Actions)
	if len(plan.Actions) == 0 {
		return
	}
	if r.logger != nil {
		r.logger.Printf("[统一轮动] perp-perp 高频决策 snapshot=%s candidates=%d actions=%d live=%v",
			snapshotID, len(rotationCandidates), len(plan.Actions), r.openLive())
	}
	prevDecide := r.decideRotationFn
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	defer func() { r.decideRotationFn = prevDecide }()
	r.runOnceDecisionAndDispatch(ctx, rotationCandidates, freeAvailable, allHeld, allHeldInputs)
}

func filterRotationCandidatesWithoutStrategy(candidates []UnifiedOpportunity, strategyType string) []UnifiedOpportunity {
	if len(candidates) == 0 || strings.TrimSpace(strategyType) == "" {
		return candidates
	}
	out := make([]UnifiedOpportunity, 0, len(candidates))
	for _, c := range candidates {
		if c.StrategyType == strategyType {
			continue
		}
		out = append(out, c)
	}
	return out
}

func rotationActionAffectsPerpPerp(act RotationAction) bool {
	return act.StrategyType == UnifiedStrategyPerpPerp || act.ReplaceStrategyType == UnifiedStrategyPerpPerp
}

func filterRotationPlanPerpPerpActions(actions []RotationAction) []RotationAction {
	if len(actions) == 0 {
		return nil
	}
	out := make([]RotationAction, 0, len(actions))
	for _, act := range actions {
		if rotationActionAffectsPerpPerp(act) {
			out = append(out, act)
		}
	}
	return out
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
	r.trackConsecutiveZeroCVSPCVPS(statusUpdate.Candidates)
	borrowDaily, _ := rotationBorrowMapsFromProbes(snap.BorrowProbes)
	held, heldInputs := r.collectHeldPositions(candidates, borrowDaily)
	statusUpdate.Held = len(held)
	statusUpdate.RealHeldManaged = r.countRealHeldPositions(ctx)
	freeAvailable := rotationFreeAvailableUSDT(available, held)
	rotationCfg := normalizeRotationConfig(r.rotationCfg)
	if r.openCooldownStore != nil {
		rotationCfg.OpenCooldownBlocked = r.openCooldownStore.blockedPredicate(r.nowFn)
	}
	diagnosticCandidates := candidates
	rotationCandidates := filterRotationCandidatesWithoutStrategy(
		filterRotationCandidates(diagnosticCandidates, rotationCfg),
		UnifiedStrategyPerpPerp,
	)
	diag := rotationDiagnoseCandidates(diagnosticCandidates, freeAvailable, rotationCfg)
	diag.CycleID = snapshotID
	appendRotationDecisionDiag(r.recordDir, diag)
	plan := r.runOnceDecisionAndDispatch(ctx, rotationCandidates, freeAvailable, held, heldInputs, rotationDispatchDiag{diag: diag, rawTotal: len(diagnosticCandidates)})
	statusUpdate.Actions = countRotationActions(plan)
	return nil
}

// rotationDispatchDiag 决策日志附加上下文(可选,单测旧调用可省略)。
type rotationDispatchDiag struct {
	diag     RotationDecisionDiag
	rawTotal int
}

// runOnceDecisionAndDispatch 纯决策+路由+派发(便于单测注入)。
func (r *UnifiedRotationRunner) runOnceDecisionAndDispatch(
	ctx context.Context,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	diagCtx ...rotationDispatchDiag,
) RotationPlan {
	var diag RotationDecisionDiag
	rawTotal := 0
	if len(diagCtx) > 0 {
		diag = diagCtx[0].diag
		rawTotal = diagCtx[0].rawTotal
	}
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
		if rawTotal <= 0 {
			rawTotal = len(candidates)
		}
		r.logger.Printf("[统一轮动] 决策完成: held=%d raw=%d rotation_candidates=%d actions=%d blocked[net_ev=%d hard=%d balance=%d] top_reject=%q live=%v",
			len(held), rawTotal, len(candidates), len(plan.Actions),
			diag.BlockedByNetEV, diag.BlockedByHardReject, diag.BlockedByBalance,
			rotationDiagTopRejectReason(diag), live)
	}

	candidatesByID := indexUnifiedOpportunitiesByID(candidates)
	toFund := rotationOpportunitiesToFund(plan, candidatesByID)
	if len(toFund) > 0 {
		if !r.openLive() {
			if r.logger != nil {
				r.logger.Printf("[统一轮动] 控制面 halt/禁开,跳过资金路由与后续开仓")
			}
			r.storeLastCapitalSnapshotContext(toFund, CapitalRoutingPlan{})
			r.dispatchRotationPlan(ctx, plan, candidatesByID, nil)
			return plan
		}
		routerCfg := rotationCapitalRouterConfig(r.execCfg, r.capitalRouterCfg, r.venues)
		balances := rotationWalletBalancesFromAvailable(available, held, r.execCfg, r.venues, toFund, routerCfg)
		routingPlan := PlanCapitalRouting(toFund, balances, routerCfg)
		r.storeLastCapitalSnapshotContext(toFund, routingPlan)
		if r.logger != nil && len(routingPlan.WithinVenueTransfers) > 0 {
			r.logger.Printf("[统一轮动] 资金路由: %d 笔所内划转 live=%v", len(routingPlan.WithinVenueTransfers), r.openLive())
		}
		execResult := ExecuteCapitalRouting(ctx, routingPlan, r.venues, r.openLive(), r.logger)
		if r.logger != nil {
			r.logger.Printf("[统一轮动] 资金路由执行: executed=%d errs=%d live=%v", execResult.Executed, len(execResult.Errs), r.openLive())
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
		r.dispatchRotationPlan(ctx, plan, candidatesByID, unfundable)
		return plan
	}
	r.storeLastCapitalSnapshotContext(nil, CapitalRoutingPlan{})
	r.dispatchRotationPlan(ctx, plan, candidatesByID, nil)
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
	cfg := r.rotationCfg
	cfg = normalizeRotationConfig(cfg)
	if r.openCooldownStore != nil {
		cfg.OpenCooldownBlocked = r.openCooldownStore.blockedPredicate(r.nowFn)
	}
	return DecideRotationWithBlocked(held, heldInputs, candidates, available, cfg, r.rotationOpenBlockedPredicate(), equityUSDT)
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

// rotationScanFundingPerHourBySymbol 返回各 symbol 跨 venue 最大 |rate/h|(仅保留达 min 阈值的项)。
func rotationScanFundingPerHourBySymbol(fundingByVenue map[string]map[string]CrossVenueSpotPerpFundingSnap) map[string]float64 {
	allSymbols := crossVenueSpotPerpCollectSymbols(fundingByVenue, nil)
	if len(allSymbols) == 0 {
		return nil
	}
	minPerHour := rotationFundingRatePerHour(rotationScanMinAbsFundingRate, 8)
	out := make(map[string]float64, len(allSymbols))
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
			out[sym] = maxPerHour
		}
	}
	return out
}

// rotationScanLiquidityProxyUSDT 粗筛流动性代理:各所 max(min(24h spot vol, 24h perp vol));无 market 数据时返回 0。
func rotationScanLiquidityProxyUSDT(marketsByVenue map[string]map[string]CrossVenueSpotPerpMarketSnap, sym string) float64 {
	if len(marketsByVenue) == 0 {
		return 0
	}
	maxVol := 0.0
	for _, bySym := range marketsByVenue {
		mkt, ok := bySym[sym]
		if !ok {
			continue
		}
		vol := 0.0
		switch {
		case mkt.Volume24hSpotUSDT > 0 && mkt.Volume24hPerpUSDT > 0:
			vol = math.Min(mkt.Volume24hSpotUSDT, mkt.Volume24hPerpUSDT)
		case mkt.Volume24hSpotUSDT > 0:
			vol = mkt.Volume24hSpotUSDT
		case mkt.Volume24hPerpUSDT > 0:
			vol = mkt.Volume24hPerpUSDT
		}
		if vol > maxVol {
			maxVol = vol
		}
	}
	return maxVol
}

// rotationScanCompositeScore funding×流动性复合分; liquidityUSDT<=0 时 honest 回退为纯 fundingPerHour。
func rotationScanCompositeScore(fundingPerHour, liquidityUSDT float64) float64 {
	if fundingPerHour <= 0 {
		return 0
	}
	if liquidityUSDT <= 0 {
		return fundingPerHour
	}
	return fundingPerHour * math.Sqrt(liquidityUSDT)
}

// rotationScanFilterSymbols 从 funding 快照筛出 |rate/h| 达标的 symbol 并按复合分降序取 top-N。
// 流动性代理: market 快照 24h 成交额 USDT(各所 max(min(spot,perp))); markets 为空时 honest 回退为纯 funding 排序+更大上限。
func rotationScanFilterSymbols(
	fundingByVenue map[string]map[string]CrossVenueSpotPerpFundingSnap,
	marketsByVenue map[string]map[string]CrossVenueSpotPerpMarketSnap,
) []string {
	maxPerHourBySym := rotationScanFundingPerHourBySymbol(fundingByVenue)
	if len(maxPerHourBySym) == 0 {
		return nil
	}
	hasMarkets := len(marketsByVenue) > 0
	scoreBySym := make(map[string]float64, len(maxPerHourBySym))
	for sym, perHour := range maxPerHourBySym {
		scoreBySym[sym] = rotationScanCompositeScore(perHour, rotationScanLiquidityProxyUSDT(marketsByVenue, sym))
	}
	filtered := make([]string, 0, len(scoreBySym))
	for sym := range scoreBySym {
		filtered = append(filtered, sym)
	}
	sort.Slice(filtered, func(i, j int) bool {
		si, sj := scoreBySym[filtered[i]], scoreBySym[filtered[j]]
		if si != sj {
			return si > sj
		}
		return filtered[i] < filtered[j]
	})
	limit := rotationScanMaxSymbols
	if !hasMarkets {
		limit = rotationScanMaxSymbolsFundingOnly
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
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
	fundingQualified := rotationScanFundingPerHourBySymbol(fundingByVenue)
	preFilterSymbols := make([]string, 0, len(fundingQualified))
	for sym := range fundingQualified {
		preFilterSymbols = append(preFilterSymbols, sym)
	}
	sort.Strings(preFilterSymbols)
	if r.delistGuard != nil {
		r.delistGuard.Refresh(ctx)
	}
	marketsByVenue := deps.fetchMarkets(ctx, r.venues, preFilterSymbols)
	symbols := rotationScanFilterSymbols(fundingByVenue, marketsByVenue)
	// 决策期流动性自适应:对粗筛 symbol 二次拉深度/24h 量,填 MarketSnap.Depth/Volume 字段。
	unifiedMarketEnrichMarketsWithDepthVolume(ctx, r.venues, marketsByVenue, symbols)
	availableUSDTByVenue := deps.fetchAvailableUSDT(ctx, r.venues, crossVenueExecutorVenues)
	if r.logger != nil {
		// C 取证:对比 equity 快照,定位"有钱却判余额不足"的 venue 级可用读数(扣储备前)。
		r.logger.Printf("[统一轮动] 各所可用USDT(扣储备前)=%v", availableUSDTByVenue)
	}
	borrowDaily, borrowLimits := rotationBorrowMapsFromProbes(snap.BorrowProbes)
	var borrowKnown *BorrowKnownAvailableRegistry
	borrowDefaultDaily := 0.001
	borrowDisabled := CrossVenueScannerConfig{}.BorrowDisabledSet()
	if r.scanner != nil {
		borrowKnown = r.scanner.BorrowKnownRegistry()
		borrowDefaultDaily = crossVenueBorrowDefaultDailyRateWhenUnknown(r.scanner.cfg)
		borrowDisabled = r.scanner.cfg.BorrowDisabledSet()
	}
	if borrowKnown != nil {
		borrowKnown.ObserveProbes(snap.BorrowProbes, now)
	}

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

	volLookback := r.execCfg.CarryFundingEntryVolLookbackScans
	fundingVolByVenue := carryFundingVolatilityMap(r.scanner, fundingByVenue, volLookback)
	if r.logger != nil {
		for venue, bySym := range fundingByVenue {
			for sym := range bySym {
				if _, ok := carryFundingVolFromMap(fundingVolByVenue, venue, sym); !ok {
					r.logger.Printf("[carry-entry-gate] %s@%s 缺费率波动样本,使用保守缩窗", sym, venue)
				}
			}
		}
	}

	singleSpotPerp := ScanSingleVenueSpotPerpOpportunities(SingleVenueSpotPerpScanInput{
		SnapshotID:                   snapshotID,
		Now:                          now,
		Symbols:                      symbols,
		Venues:                       crossVenueExecutorVenues,
		FundingByVenue:               fundingByVenue,
		MarketsByVenue:               marketsByVenue,
		AvailableUSDTByVenue:         scannerAvailableUSDT,
		Config:                       DefaultSingleVenueSpotPerpScannerConfig(r.execCfg),
		FundingRateVolPerHourByVenue: fundingVolByVenue,
	})
	crossSpotPerp := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID:                   snapshotID,
		Now:                          now,
		Symbols:                      symbols,
		FundingByVenue:               fundingByVenue,
		MarketsByVenue:               marketsByVenue,
		AvailableUSDTByVenue:         scannerAvailableUSDT,
		Config:                       DefaultCrossVenueSpotPerpScannerConfig(r.execCfg),
		FundingRateVolPerHourByVenue: fundingVolByVenue,
	})
	singlePerpSpot := ScanSingleVenuePerpSpotOpportunities(SingleVenuePerpSpotScanInput{
		SnapshotID:                        snapshotID,
		Now:                               now,
		Symbols:                           symbols,
		Venues:                            crossVenueExecutorVenues,
		FundingByVenue:                    fundingByVenue,
		MarketsByVenue:                    marketsByVenue,
		AvailableUSDTByVenue:              scannerAvailableUSDT,
		BorrowDailyRateByAsset:            borrowDaily,
		BorrowLimitUSDTByAsset:            borrowLimits,
		BorrowKnownAvailable:              borrowKnown,
		BorrowDefaultDailyRateWhenUnknown: borrowDefaultDaily,
		BorrowDisabledVenues:              borrowDisabled,
		Config:                            DefaultSingleVenuePerpSpotScannerConfig(r.execCfg),
		FundingRateVolPerHourByVenue:      fundingVolByVenue,
	})
	crossPerpSpot := ScanCrossVenuePerpSpotOpportunities(CrossVenuePerpSpotScanInput{
		SnapshotID:                        snapshotID,
		Now:                               now,
		Symbols:                           symbols,
		FundingByVenue:                    fundingByVenue,
		MarketsByVenue:                    marketsByVenue,
		AvailableUSDTByVenue:              scannerAvailableUSDT,
		BorrowDailyRateByAsset:            borrowDaily,
		BorrowLimitUSDTByAsset:            borrowLimits,
		BorrowKnownAvailable:              borrowKnown,
		BorrowDefaultDailyRateWhenUnknown: borrowDefaultDaily,
		BorrowDisabledVenues:              borrowDisabled,
		Config:                            DefaultCrossVenuePerpSpotScannerConfig(r.execCfg),
		FundingRateVolPerHourByVenue:      fundingVolByVenue,
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
	recordEarningLegDiag(r.recordDir, "carry_scan", snapshotID, writeCycleID, candidates)

	if r.includePerpPerpCandidates {
		perpPerpCandidates := r.buildPerpPerpCandidatesFromSnapshot(snap, availableUSDTByVenue)
		recordEarningLegDiag(r.recordDir, "carry_scan_perp_perp", snapshotID, writeCycleID, perpPerpCandidates)
		candidates = append(candidates, perpPerpCandidates...)
		if strings.TrimSpace(r.recordDir) != "" {
			AppendCrossVenuePerpPerpCandidates(r.recordDir, writeCycleID, r.execCfg, perpPerpCandidates)
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
		counts := countRotationCandidates(candidates)
		scanDiag := rotationDiagnoseCandidates(candidates, scannerAvailableUSDT, normalizeRotationConfig(r.rotationCfg))
		earningSummary, _ := diagnoseEarningLegConflicts(candidates)
		r.logger.Printf("[统一轮动] 扫描完成 snapshot=%s funding_symbols=%d\n  raw_total=%d raw_by_strategy=[svsp=%d cvsp=%d svps=%d cvps=%d perp_perp=%d]\n  scanner_would_exec=%d rotation_would_exec=%d soft_reject=%d hard_reject=%d earning_leg_groups=%d conflict_keys=%d duplicated_candidates=%d",
			snapshotID, len(symbols), counts.Total, counts.SVSP, counts.CVSP, counts.SVPS, counts.CVPS, perpPerpN,
			scanDiag.ScannerWouldExec, scanDiag.RotationWouldExec, scanDiag.SoftRejectCount, scanDiag.HardRejectCount,
			earningSummary.UniqueKeyCount, earningSummary.ConflictKeyCount, earningSummary.DuplicatedCandidateCount)
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
			if !r.openLive() {
				if r.logger != nil {
					r.logger.Printf("[统一轮动] SWITCH 开新跳过(控制面 halt/禁开) old=%s replace=%s", act.OpportunityID, act.ReplaceOpportunityID)
				}
				continue
			}
			if err := r.openOpportunity(ctx, act.ReplaceStrategyType, opp, true); err != nil {
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
			if !r.openLive() {
				if r.logger != nil {
					r.logger.Printf("[统一轮动] OPEN 跳过(控制面 halt/禁开) %s", act.OpportunityID)
				}
				continue
			}
			if opp, ok := candidatesByID[act.OpportunityID]; ok {
				if err := r.openOpportunity(ctx, act.StrategyType, opp, true); err != nil && r.logger != nil {
					r.logger.Printf("[统一轮动] OPEN 失败(已吞掉) %s: %v", act.OpportunityID, err)
				}
			}
		}
	}
}

func (r *UnifiedRotationRunner) trackConsecutiveZeroCVSPCVPS(counts RotationCandidateCounts) {
	if r == nil {
		return
	}
	r.statusMu.Lock()
	if counts.CVSP == 0 && counts.CVPS == 0 {
		r.consecutiveZeroCVSPCVPS++
	} else {
		r.consecutiveZeroCVSPCVPS = 0
	}
	streak := r.consecutiveZeroCVSPCVPS
	r.statusMu.Unlock()
	if streak >= rotationZeroCVSPCVPSAlertThreshold {
		r.maybeLogZeroCVSPCVPSMajor(streak)
	}
}

func (r *UnifiedRotationRunner) maybeLogZeroCVSPCVPSMajor(streak int) {
	if r == nil || r.logger == nil {
		return
	}
	r.lastZeroCVAlertMu.Lock()
	defer r.lastZeroCVAlertMu.Unlock()
	now := time.Now()
	if r.nowFn != nil {
		now = r.nowFn()
	}
	interval := r.tickerInterval()
	if !r.lastZeroCVAlertAt.IsZero() && now.Sub(r.lastZeroCVAlertAt) < interval {
		return
	}
	r.logger.Printf("[统一轮动] MAJOR: 连续 %d 轮 cvsp=0 且 cvps=0,跨所 carry 候选持续为空", streak)
	r.lastZeroCVAlertAt = now
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
		r.recordOpenFailureCooldown(strategyType, opp)
		return err
	}
	appendAttributionPredictionBestEffort(r.recordDir, predictionRecordFromOpportunity(opp, time.Now().UTC()))
	return nil
}

func (r *UnifiedRotationRunner) recordOpenFailureCooldown(strategyType string, opp UnifiedOpportunity) {
	if r == nil || r.openCooldownStore == nil {
		return
	}
	now := time.Now().UTC()
	if r.nowFn != nil {
		now = r.nowFn().UTC()
	}
	r.openCooldownStore.RecordFailure(strategyType, opp.Symbol, rotationOpportunityVenuePair(opp), now)
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
	// 保证金 = 名义/lev; buffer 为钱包小额垫,不替代可部署杠杆。
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
	// 与执行闸 crossVenuePerVenueMarginUSDT 同口径,供 PlanCapitalRouting 所内 spot→perp 补足。
	margin := crossVenuePerVenueMarginUSDT(execCfg, notional)
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

// rotationInferNotionalFromTotalCapital 由 spot+perp 单所总占用反推名义。
// total = notional + rotationPerpMarginWithBuffer(notional) = notional × (1 + 1/lev + buffer)。
func rotationInferNotionalFromTotalCapital(execCfg CrossVenueExecutorConfig, total float64) float64 {
	if total <= 0 {
		return 0
	}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	lev := execCfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	buffer := scannerCfg.PerpWalletBufferRatio
	if buffer <= 0 {
		buffer = crossVenueSpotPerpDefaultPerpWalletBuffer
	}
	return total / (1 + 1/lev + buffer)
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

func rotationCapitalRouterConfig(execCfg CrossVenueExecutorConfig, base CapitalRouterConfig, venues map[string]venue.Venue) CapitalRouterConfig {
	cfg := CapitalRouterConfigWithVenues(base, venues)
	if execCfg.MaxEffectiveLeverage > 0 {
		cfg.PerpLeverage = execCfg.MaxEffectiveLeverage
	}
	return cfg
}

func rotationWalletBalancesFromAvailable(fetched map[string]float64, held []HeldPosition, execCfg CrossVenueExecutorConfig, venues map[string]venue.Venue, pending []UnifiedOpportunity, routerCfg ...CapitalRouterConfig) VenueWalletBalances {
	unified := UnifiedWalletVenuesFromVenues(venues)
	totals := cloneVenueCapitalMap(fetched)
	for _, pos := range held {
		rotationAddCapitalMap(totals, pos.CapitalRequiredByVenue)
	}

	capRouterCfg := DefaultCapitalRouterConfig()
	if len(routerCfg) > 0 {
		capRouterCfg = routerCfg[0]
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
		reqByVenue := aggregateOpportunityRequirements(opp, capRouterCfg)
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
			out[venue] = rotationNonUnifiedIdleWalletSplit(venues[venue], total, false)
			continue
		}
		if rotationWalletNeedFuturesOnly(needs) {
			if real := rotationFetchRealVenueWalletBalances(context.Background(), venues[venue]); real != nil {
				if venueTotalBalance(real) > capitalRouterEpsilon {
					out[venue] = real
					continue
				}
			}
			// 无法读取分仓余额时退回:idle 放 spot,PlanCapitalRouting 再 spot→futures 补足执行闸。
			out[venue] = rotationNonUnifiedIdleWalletSplit(venues[venue], total, true)
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

func rotationWalletNeedFuturesOnly(needs venueWalletRequirements) bool {
	if needs[WalletKindFutures] <= capitalRouterEpsilon {
		return false
	}
	for _, w := range capitalRouterWalletKinds {
		if w == WalletKindFutures {
			continue
		}
		if needs[w] > capitalRouterEpsilon {
			return false
		}
	}
	return true
}

// rotationNonUnifiedIdleWalletSplit 非统一账户 idle 分配:支持所内划转且纯 perp 备资时假定 USDT 在 spot(路由后搬进 perp)。
func rotationNonUnifiedIdleWalletSplit(v venue.Venue, total float64, futuresOnlyNeed bool) map[WalletKind]float64 {
	if futuresOnlyNeed && v != nil {
		if _, ok := v.(walletTransferer); ok {
			return map[WalletKind]float64{WalletKindSpot: total}
		}
	}
	return map[WalletKind]float64{WalletKindFutures: total}
}

// rotationFetchRealVenueWalletBalances 读取非统一账户 spot/perp 分仓真实可用(用于路由,避免 idle 误假定在 spot)。
func rotationFetchRealVenueWalletBalances(ctx context.Context, v venue.Venue) map[WalletKind]float64 {
	if v == nil || v.Capabilities().UnifiedWallet {
		return nil
	}
	out := map[WalletKind]float64{}
	gotPerp := false
	gotSpot := false
	if perp, err := fetchVenuePerpAvailableUSDT(ctx, v); err == nil {
		out[WalletKindFutures] = perp
		gotPerp = true
	}
	if spotP, ok := v.(venueSpotAvailableUSDTProvider); ok {
		if spot, err := fetchVenueWalletAvailableUSDT(ctx, "spot", spotP.FetchSpotAvailableUSDT); err == nil {
			out[WalletKindSpot] = spot
			gotSpot = true
		}
	}
	if !gotPerp && !gotSpot {
		return nil
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
		if cost := perpSpotBorrowCostUSDT(baseAsset, notional, holdHours, borrowDailyByAsset, 0); cost > 0 {
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
		} else {
			dailyRates[base] = 0
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
