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
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// cross_venue_executor.go P1 跨所资金费对冲执行引擎:消费扫描器结果,空高费所/多低费所 perp-perp 对冲。
// 默认 OFF 模式(enabled=false / live_trading_enabled=false),绝不在未显式开启 live 时下单。

const crossVenueTradeableVenueList = "binance,okx,gate,bybit"

var crossVenueExecutorVenues = []string{binance.VenueName, okx.VenueName, gate.VenueName, bybit.VenueName}

var crossVenueExecutorFactory = buildCrossVenueExecutorAdapters

// CrossVenueExecutor 跨所 perp-perp 对冲执行引擎。
type CrossVenueExecutor struct {
	cfg       EngineConfig
	scanner   *CrossVenueScanner
	venues    map[string]venue.Venue
	orch      *Orchestrator
	store     *crossVenuePositionStore
	alerts    *AlertDispatcher
	logger    *log.Logger
	haltPaths []string

	mu                       sync.RWMutex
	earningsMu               sync.Mutex
	settlementsMu            sync.Mutex
	equitySnapMu             sync.Mutex
	positions                []CrossVenuePosition
	symbolCooldown           map[string]time.Time
	openQuarantine           *openFailureQuarantine
	deadManUnsupportedVenues map[string]struct{} // venue 适配器声明有 dead-man 但实际 ErrUnsupported 时缓存,避免每分钟重试刷日志
	// deadManLastArm 记录每所上次成功续期 dead-man 的时刻。dead-man(如 OKX cancel-all-after)是账户级操作
	// 且限频严格(OKX 1次/2秒),而管理是逐笔持仓循环——多笔含同一所的腿会在同一周期内重复续期触发 429。
	// 按所节流(每 cd/2 最多一次)即可:一次成功续期已重置整账户倒计时,同周期其余同所腿跳过。
	deadManLastArm map[string]time.Time
	// realFundingLogLast 节流管理循环「真实资费拉取失败」日志(每 symbol+venue 一次/30min)。
	realFundingLogLast map[string]time.Time
	nowFn              func() time.Time
	// openingsEnabled 是运行时开仓开关(第三道闸);OFF=跳过 evaluateNewOpens,在场持仓管理/平仓/风控照常。
	openingsEnabled atomic.Bool
	// rotationManaged 为 true 时统一轮动 runner 接管 open/manage/reconcile,本引擎 tick 不再独立调度(防双循环)。
	rotationManaged atomic.Bool
	// running 标记 Run 主循环是否在跑(healthz 就绪判定用)。
	running atomic.Bool
	// recordDir 用于 control.json 持久化(重启维持上次选择)。
	recordDir string
	// healthHaltVenue 健康闸当前暂停开仓的 venue(空=正常);用于告警去抖与 API 暴露。
	healthHaltVenue string
	lastCycle       CrossVenueCycleExplain
	// controlPlane 统一控制面附加闸(nil=仅沿用 .halt-live / openings 等既有语义)。
	controlPlane *ControlPlane
	// symbolVolMultiplier 可选 vol hook;nil 时不做 vol 缩放(见 risk_liq_distance.go)。
	symbolVolMultiplier SymbolVolatilityMultiplier
}

type crossVenueUntrackedLeg struct {
	Venue      string
	Symbol     string
	Qty        float64
	MarkPrice  float64
	EntryPrice float64
}

type crossVenuePreparedOpenCandidate struct {
	Opp          CrossVenueOpportunity
	Gate         crossVenueGateResult
	GateReject   string
	SwitchIntent *crossVenueSwitchIntent
}

// CrossVenuePositionsAPIResponse 供面板 API 返回。
type CrossVenuePositionsAPIResponse struct {
	Enabled                bool                       `json:"enabled"`
	Live                   bool                       `json:"live"`
	Mode                   string                     `json:"mode"`
	OpeningsEnabled        bool                       `json:"openings_enabled"`
	HealthHaltVenue        string                     `json:"health_halt_venue"`
	ActivePositions        int                        `json:"active_positions"`
	PendingSwitches        int                        `json:"pending_switches"`
	PartialSwitches        int                        `json:"partial_switches"`
	OrphanPositions        int                        `json:"orphan_positions"`
	TransitioningPositions int                        `json:"transitioning_positions"`
	TransitionHeadroomUSDT float64                    `json:"transition_headroom_usdt"`
	LastCycle              CrossVenueCycleExplain     `json:"last_cycle"`
	ActivePositionDetails  []CrossVenuePositionDetail `json:"active_position_details"`
	Positions              []CrossVenuePosition       `json:"positions"`
	UpdatedAt              time.Time                  `json:"updated_at"`
}

type CrossVenueReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type CrossVenueCycleExplain struct {
	DecisionCycleID   string                  `json:"decision_cycle_id,omitempty"`
	SnapshotID        string                  `json:"snapshot_id,omitempty"`
	DecisionTime      time.Time               `json:"decision_time,omitempty"`
	CycleReason       string                  `json:"cycle_reason,omitempty"`
	OpenSummary       string                  `json:"open_summary,omitempty"`
	SwitchSummary     string                  `json:"switch_summary,omitempty"`
	FallbackSummary   string                  `json:"fallback_summary,omitempty"`
	HaltReason        string                  `json:"halt_reason,omitempty"`
	NoTradeReasonsTop []CrossVenueReasonCount `json:"no_trade_reasons_top,omitempty"`
}

// CrossVenuePositionDetail 面板持仓明细行(由在场持仓派生,含持有时长等展示字段)。
type CrossVenuePositionDetail struct {
	Symbol             string    `json:"symbol"`
	HighVenue          string    `json:"high_venue"`
	LowVenue           string    `json:"low_venue"`
	HighLegSide        string    `json:"high_leg_side"`
	HighLegQty         float64   `json:"high_leg_qty"`
	HighLegEntryPrice  float64   `json:"high_leg_entry_price"`
	LowLegSide         string    `json:"low_leg_side"`
	LowLegQty          float64   `json:"low_leg_qty"`
	LowLegEntryPrice   float64   `json:"low_leg_entry_price"`
	NotionalUSDT       float64   `json:"notional_usdt"`
	OpenSpreadAPR      float64   `json:"open_spread_apr"`
	EstFundingUSDT     float64   `json:"est_funding_usdt"`
	OpenFeesUSDT       float64   `json:"open_fees_usdt"`
	EstCloseFeesUSDT   float64   `json:"est_close_fees_usdt"`
	NetUSDT            float64   `json:"net_usdt"`
	CurrentFutureEV    float64   `json:"current_future_ev,omitempty"`
	FreshnessAgeSec    float64   `json:"freshness_age_seconds,omitempty"`
	DecisionCycleID    string    `json:"decision_cycle_id,omitempty"`
	CurrentSnapshotID  string    `json:"current_snapshot_id,omitempty"`
	TransitionState    string    `json:"transition_state,omitempty"`
	RecoveryAction     string    `json:"recovery_action,omitempty"`
	PendingSwitchTo    string    `json:"pending_switch_to,omitempty"`
	PendingSwitchGain  float64   `json:"pending_switch_expected_gain_usdt,omitempty"`
	PendingSwitchSeq   string    `json:"pending_switch_sequence,omitempty"`
	PartialSwitchPhase string    `json:"partial_switch_phase,omitempty"`
	HoldDurationSec    float64   `json:"hold_duration_seconds"`
	Status             string    `json:"status"`
	OpenedAt           time.Time `json:"opened_at"`
}

func buildCrossVenuePositionDetails(cfg CrossVenueExecutorConfig, ps []CrossVenuePosition, now time.Time) []CrossVenuePositionDetail {
	active := activeCrossVenuePositions(ps)
	out := make([]CrossVenuePositionDetail, 0, len(active))
	for _, p := range active {
		holdSec := 0.0
		if !p.OpenedAt.IsZero() && !now.Before(p.OpenedAt) {
			holdSec = now.Sub(p.OpenedAt).Seconds()
		}
		openFees := p.OpenFeesUSDT
		estCloseFees := crossVenueEstCloseFeeUSDT(cfg, &p)
		out = append(out, CrossVenuePositionDetail{
			Symbol:             p.Symbol,
			HighVenue:          p.HighVenue,
			LowVenue:           p.LowVenue,
			HighLegSide:        p.HighLeg.Side,
			HighLegQty:         p.HighLeg.Qty,
			HighLegEntryPrice:  p.HighLeg.EntryPrice,
			LowLegSide:         p.LowLeg.Side,
			LowLegQty:          p.LowLeg.Qty,
			LowLegEntryPrice:   p.LowLeg.EntryPrice,
			NotionalUSDT:       p.NotionalUSDT,
			OpenSpreadAPR:      p.OpenSpreadAPR,
			EstFundingUSDT:     p.EstFundingUSDT,
			OpenFeesUSDT:       openFees,
			EstCloseFeesUSDT:   estCloseFees,
			NetUSDT:            p.EstFundingUSDT - openFees - estCloseFees,
			CurrentFutureEV:    p.CurrentFutureEV,
			FreshnessAgeSec:    p.FreshnessAgeSeconds,
			DecisionCycleID:    p.DecisionCycleID,
			CurrentSnapshotID:  p.CurrentSnapshotID,
			TransitionState:    p.TransitionState,
			RecoveryAction:     p.RecoveryAction,
			PendingSwitchTo:    crossVenuePendingSwitchTarget(p),
			PendingSwitchGain:  p.PendingSwitchExpectedGainUSDT,
			PendingSwitchSeq:   p.PendingSwitchSequence,
			PartialSwitchPhase: p.PartialSwitchPhase,
			HoldDurationSec:    holdSec,
			Status:             string(p.Status),
			OpenedAt:           p.OpenedAt,
		})
	}
	return out
}

func buildCrossVenueReasonCounts(reasons []string, limit int) []CrossVenueReasonCount {
	if limit <= 0 {
		limit = 5
	}
	counts := make(map[string]int)
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		counts[reason]++
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]CrossVenueReasonCount, 0, len(counts))
	for reason, count := range counts {
		out = append(out, CrossVenueReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func crossVenueTransitionMetrics(ps []CrossVenuePosition) (pending, partial, orphan, transitioning int, headroom float64) {
	for _, p := range activeCrossVenuePositions(ps) {
		if crossVenueHasPendingSwitch(p) || p.TransitionState == "switch_pending" {
			pending++
		}
		if p.TransitionState == "partial_switch" {
			partial++
		}
		if p.Status == CrossVenueStatusOrphanSingleLeg {
			orphan++
		}
		if strings.TrimSpace(p.TransitionState) != "" && p.TransitionState != "hold" {
			transitioning++
			headroom += math.Max(0, p.EstimatedSwitchCost)
		}
	}
	return pending, partial, orphan, transitioning, headroom
}

func crossVenueExplainSwitchSummary(ps []CrossVenuePosition) string {
	pending, partial, orphan, transitioning, _ := crossVenueTransitionMetrics(ps)
	if pending == 0 && partial == 0 && orphan == 0 && transitioning == 0 {
		return "无待执行换仓/恢复"
	}
	return fmt.Sprintf("pending=%d partial=%d orphan=%d transitioning=%d", pending, partial, orphan, transitioning)
}

// NewCrossVenueExecutor 构造执行引擎。
func NewCrossVenueExecutor(cfg EngineConfig, orch *Orchestrator, scanner *CrossVenueScanner, secretPath string, logger *log.Logger) (*CrossVenueExecutor, error) {
	if logger == nil {
		logger = log.Default()
	}
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	store, err := newCrossVenuePositionStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, err
	}
	venues := crossVenueExecutorFactory(orch, cfg, secretPath, logger)
	e := &CrossVenueExecutor{
		cfg:                      cfg,
		scanner:                  scanner,
		venues:                   venues,
		orch:                     orch,
		store:                    store,
		alerts:                   newCrossVenueAlertDispatcher(cfg.CrossVenueScanner, secretPath, logger),
		logger:                   logger,
		haltPaths:                emergencyStopPaths(cfg),
		symbolCooldown:           make(map[string]time.Time),
		openQuarantine:           newOpenFailureQuarantine(time.Now),
		deadManUnsupportedVenues: make(map[string]struct{}),
		deadManLastArm:           make(map[string]time.Time),
		realFundingLogLast:       make(map[string]time.Time),
		nowFn:                    time.Now,
	}
	if ps, lerr := store.load(); lerr == nil {
		e.positions = ps
	}
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if recordDir == "" {
		recordDir = "records"
	}
	e.recordDir = recordDir
	if enabled, lerr := loadCrossVenueControlState(recordDir); lerr == nil {
		e.openingsEnabled.Store(enabled)
	} else if logger != nil {
		logger.Printf("[跨所执行] 读取 control.json 失败,开仓开关默认关: %v", lerr)
	}
	return e, nil
}

// SetControlPlane 注入统一控制面(启动装配时调用)。
func (e *CrossVenueExecutor) SetControlPlane(cp *ControlPlane) {
	if e == nil {
		return
	}
	e.controlPlane = cp
}

func buildCrossVenueExecutorAdapters(orch *Orchestrator, cfg EngineConfig, secretPath string, logger *log.Logger) map[string]venue.Venue {
	out := make(map[string]venue.Venue, len(crossVenueExecutorVenues))
	for _, name := range crossVenueExecutorVenues {
		if adp := crossVenueAdapterFromOrchestrator(orch, name); adp != nil {
			out[name] = adp
			continue
		}
		adp, err := buildCrossVenueReadOnlyAdapter(name, cfg, secretPath)
		if err != nil {
			if logger != nil {
				logger.Printf("[跨所执行] venue=%s 适配器构造失败,跳过: %v", name, err)
			}
			continue
		}
		out[name] = adp
	}
	return out
}

func crossVenueExecutorModeLabel(cfg EngineConfig) string {
	if cfg.CrossVenueExecutor.LiveTradingEnabled {
		return "实盘下单"
	}
	return "OFF(不下单)"
}

// IsRunning 报告跨所执行器主循环是否在跑(Enabled=false 或未启动时为 false)。
func (e *CrossVenueExecutor) IsRunning() bool {
	if e == nil {
		return false
	}
	return e.running.Load()
}

func (e *CrossVenueExecutor) tick(ctx context.Context, live bool) {
	now := e.nowFn()
	cycleID := crossVenueDecisionCycleID(now)
	snap := CrossVenueScanSnapshot{}
	if e.scanner != nil {
		snap = e.scanner.Snapshot()
	}
	baseExplain := CrossVenueCycleExplain{
		DecisionCycleID: cycleID,
		SnapshotID:      snap.SnapshotID,
		DecisionTime:    now,
		CycleReason:     "scheduled_tick",
		SwitchSummary:   crossVenueExplainSwitchSummary(e.snapshotPositions()),
	}
	// 统一轮动 runner 已绑定:本 tick 仅同步健康闸,open/manage 由 runner 派发(杜绝双重开仓/双重 manage)。
	if e.RotationManaged() {
		unhealthy := e.firstUnhealthyTradeableVenue()
		e.syncHealthHaltAlerts(unhealthy)
		baseExplain.HaltReason = "rotation_managed"
		baseExplain.OpenSummary = "统一轮动 runner 接管 open/manage"
		e.setLastCycle(baseExplain)
		return
	}
	haltActive := e.isHaltActive()
	if haltActive {
		e.logger.Printf("[跨所执行] 急停生效,跳过开新仓(在场持仓仍管理)")
	}
	if e.controlPlane == nil || e.controlPlane.AllowManageOrders() {
		e.manageOpenPositions(ctx, live)
	}

	unhealthy := e.firstUnhealthyTradeableVenue()
	e.syncHealthHaltAlerts(unhealthy)

	if haltActive {
		baseExplain.HaltReason = "halt_active"
		baseExplain.OpenSummary = "急停生效,仅管理在场仓"
		e.setLastCycle(baseExplain)
		return
	}
	if e.controlPlane != nil && !e.controlPlane.AllowNewOpens() {
		e.logger.Printf("[跨所执行] 控制面 allow_new_opens=false,跳过 evaluateNewOpens")
		baseExplain.HaltReason = "control_plane_new_opens_disabled"
		baseExplain.OpenSummary = "控制面禁止新开仓"
		e.setLastCycle(baseExplain)
		return
	}
	if e.controlPlane != nil && !e.controlPlane.PerpPerpLiveEnabled() {
		e.logger.Printf("[跨所执行] 控制面 perp_perp_live_enabled=false,跳过 evaluateNewOpens")
		baseExplain.HaltReason = "control_plane_perp_perp_disabled"
		baseExplain.OpenSummary = "控制面 perp-perp 策略闸关闭"
		e.setLastCycle(baseExplain)
		return
	}
	if !e.OpeningsEnabled() {
		e.logger.Printf("[跨所执行] 运行时开仓开关已关闭,跳过 evaluateNewOpens(在场持仓管理照常)")
		baseExplain.HaltReason = "openings_disabled"
		baseExplain.OpenSummary = "运行时开仓开关关闭"
		e.setLastCycle(baseExplain)
		return
	}
	if unhealthy != "" {
		e.logger.Printf("[跨所执行] 健康闸: venue=%s 不健康,跳过 evaluateNewOpens(在场持仓管理照常)", unhealthy)
		baseExplain.HaltReason = "health_halt:" + unhealthy
		baseExplain.OpenSummary = fmt.Sprintf("venue=%s 不健康,跳过新开仓", unhealthy)
		e.setLastCycle(baseExplain)
		return
	}
	e.evaluateNewOpens(ctx, live)
}

// firstUnhealthyTradeableVenue 返回第一个不健康的跨所可交易 venue;idle/未运行不算不健康。
func (e *CrossVenueExecutor) firstUnhealthyTradeableVenue() string {
	if e == nil || e.orch == nil {
		return ""
	}
	for _, name := range crossVenueExecutorVenues {
		if !crossVenueTradeableVenues[name] {
			continue
		}
		rt, ok := e.orch.Get(name)
		if !ok || rt == nil || rt.controller == nil {
			continue
		}
		if isCrossVenueVenueUnhealthy(rt.controller.Status()) {
			return name
		}
	}
	return ""
}

func isCrossVenueVenueUnhealthy(st RuntimeTaskStatus) bool {
	if st.CircuitBreakerTripped {
		return true
	}
	return st.Running && strings.EqualFold(strings.TrimSpace(st.HealthStatus), "failed")
}

// syncHealthHaltAlerts 仅在健康闸状态翻转时发告警(去抖,不每轮刷)。
func (e *CrossVenueExecutor) syncHealthHaltAlerts(unhealthy string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	prev := e.healthHaltVenue
	if prev == unhealthy {
		e.mu.Unlock()
		return
	}
	e.healthHaltVenue = unhealthy
	e.mu.Unlock()

	if unhealthy != "" && prev == "" {
		e.notifyCritical("跨所健康闸", fmt.Sprintf("venue=%s health=failed,已暂停所有跨所新开仓;在场仓继续管理", unhealthy), "")
	} else if unhealthy == "" && prev != "" {
		e.notifyWarn("跨所健康闸", fmt.Sprintf("%s 恢复,跨所开仓恢复", prev), "")
	}
}

func (e *CrossVenueExecutor) healthHaltVenueSnapshot() string {
	if e == nil {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.healthHaltVenue
}

func (e *CrossVenueExecutor) isHaltActive() bool {
	if e.controlPlane != nil {
		return e.controlPlane.IsGlobalHaltActive()
	}
	return ensureEmergencyStopsClear(e.haltPaths) != nil
}

func (e *CrossVenueExecutor) manageOpenPositions(ctx context.Context, live bool) {
	active := activeCrossVenuePositions(e.snapshotPositions())
	for i := range active {
		_, _ = e.manageCrossVenuePosition(ctx, active[i], live)
	}
}

func (e *CrossVenueExecutor) evaluateNewOpens(ctx context.Context, live bool) {
	cfg := e.cfg.CrossVenueExecutor
	active := activeCrossVenuePositions(e.snapshotPositions())
	now := e.nowFn()
	cycleID := crossVenueDecisionCycleID(now)
	explain := CrossVenueCycleExplain{
		DecisionCycleID: cycleID,
		DecisionTime:    now,
		CycleReason:     "scheduled_tick",
		SwitchSummary:   crossVenueExplainSwitchSummary(e.snapshotPositions()),
	}
	activeSwitchIntents := e.activeSwitchIntents(now)
	concurrentLimit := cfg.MaxConcurrent + crossVenueTransitionHeadroomSlots(activeSwitchIntents)
	if len(active) >= concurrentLimit {
		explain.OpenSummary = fmt.Sprintf("active=%d 已达并发上限=%d", len(active), cfg.MaxConcurrent)
		explain.HaltReason = "max_concurrent_reached"
		e.setLastCycle(explain)
		return
	}
	if e.scanner == nil {
		explain.OpenSummary = "scanner 未注入"
		explain.HaltReason = "scanner_missing"
		e.setLastCycle(explain)
		return
	}
	snap := e.scanner.Snapshot()
	explain.SnapshotID = snap.SnapshotID
	if !e.snapshotFreshEnough(snap.ScannedAt) {
		e.logger.Printf("[跨所执行] scanner 快照过旧 age=%.0fs,跳过开新仓", crossVenueSnapshotAgeSeconds(snap.ScannedAt, e.nowFn()))
		explain.OpenSummary = fmt.Sprintf("snapshot 过旧 age=%.0fs", crossVenueSnapshotAgeSeconds(snap.ScannedAt, now))
		explain.HaltReason = "snapshot_too_old"
		e.setLastCycle(explain)
		return
	}
	deps := e.gateDeps(snap)
	// 拒绝原因汇总(每轮一行,最多列前 5 条):OFF 观察期必须能看到「为什么不开」,否则空跑无法验证决策质量。
	var rejected []string
	var rawRejectReasons []string
	var opened []string
	var fallbackUsed []string
	unifiedTraceByKey := make(map[string]*crossVenueUnifiedCandidateTrace, len(snap.Opportunities))
	resolvedOpps := make([]CrossVenueOpportunity, 0, len(snap.Opportunities))
	for _, raw := range snap.Opportunities {
		opp := e.enrichOpportunityFromSnapshot(raw, snap, now)
		resolved, downgradeReject := e.resolveTradeableOpportunity(opp, snap.FundingByVenue)
		if downgradeReject != "" {
			rawRejectReasons = append(rawRejectReasons, downgradeReject)
			key := crossVenueUnifiedCandidateKey(opp)
			unifiedTraceByKey[key] = &crossVenueUnifiedCandidateTrace{
				Opp:           opp,
				RejectReasons: []string{downgradeReject},
			}
			if len(rejected) < 5 {
				rejected = append(rejected, fmt.Sprintf("%s(%s)", opp.Symbol, downgradeReject))
			}
			continue
		}
		resolved = e.enrichOpportunityFromSnapshot(resolved, snap, now)
		resolvedOpps = append(resolvedOpps, resolved)
	}
	resolvedOpps = prioritizeCrossVenueOpportunitiesForSwitches(resolvedOpps, activeSwitchIntents)
	resolvedOpps = filterCrossVenueOpportunitiesForSwitchContinuation(resolvedOpps, activeSwitchIntents)
	prepared := make([]crossVenuePreparedOpenCandidate, 0, len(resolvedOpps))
	hasStrictCandidate := false
	for _, opp := range resolvedOpps {
		var switchIntent *crossVenueSwitchIntent
		allowExistingSymbol := false
		if intent, ok := e.switchIntentForOpportunity(opp, now); ok {
			intentCopy := intent
			switchIntent = &intentCopy
			allowExistingSymbol = crossVenueSwitchSequenceNeedsTargetFirst(intent.SwitchSequence)
		}
		requiredMargin := conservativeCrossVenueRequiredMargin(cfg)
		ensureCrossVenuePerpMargin(ctx, e.logger, e.venues, opp.HighVenue, opp.LowVenue, requiredMargin)
		if crossVenueUsesBybit(opp.HighVenue, opp.LowVenue) {
			ensureBybitTradingAgreement(ctx, e.logger, e.venues[bybit.VenueName])
		}
		gateRes := evaluateCrossVenueOpenGate(ctx, cfg, deps, e.venues, crossVenueGateInput{
			Opp:                 opp,
			Positions:           e.snapshotPositions(),
			AllowExistingSymbol: allowExistingSymbol,
			BybitHeld:           func(sym venue.Symbol) bool { return isBybitSymbolHeld(e.cfg.NetworkConfig.RecordDir, sym) },
			BinanceHeld:         func(sym venue.Symbol) bool { return isBinanceSymbolHeld(e.cfg.NetworkConfig.RecordDir, sym) },
			FundingHeld:         func(sym venue.Symbol) bool { return isFundingSymbolHeldAnyVenue(e.cfg.NetworkConfig.RecordDir, sym) },
			HaltActive:          false,
			SymbolCooling:       e.isSymbolCooling,
			Now:                 now,
		})
		candidate := crossVenuePreparedOpenCandidate{Opp: opp, Gate: gateRes, SwitchIntent: switchIntent}
		if !gateRes.OK {
			candidate.GateReject = gateRes.Reason
			rawRejectReasons = append(rawRejectReasons, gateRes.Reason)
		} else if crossVenueOpportunityIsStrict(opp) {
			hasStrictCandidate = true
		}
		trace := &crossVenueUnifiedCandidateTrace{
			Opp:             opp,
			Gate:            gateRes,
			HasGateNotional: gateRes.OK,
		}
		if candidate.GateReject != "" {
			trace.RejectReasons = []string{candidate.GateReject}
		}
		unifiedTraceByKey[crossVenueUnifiedCandidateKey(opp)] = trace
		prepared = append(prepared, candidate)
	}
	for _, candidate := range prepared {
		trace := unifiedTraceByKey[crossVenueUnifiedCandidateKey(candidate.Opp)]
		if len(activeCrossVenuePositions(e.snapshotPositions())) >= concurrentLimit {
			if trace != nil {
				trace.RejectReasons = append(trace.RejectReasons, "max_concurrent_reached")
			}
			break
		}
		opp := candidate.Opp
		if candidate.GateReject != "" {
			if len(rejected) < 5 {
				rejected = append(rejected, fmt.Sprintf("%s(%s)", opp.Symbol, candidate.GateReject))
			}
			continue
		}
		mode, selectReject := crossVenueSelectOpenMode(opp, hasStrictCandidate)
		if crossVenueOpportunityMatchesAnySwitchIntent(opp, activeSwitchIntents) {
			if crossVenueOpportunityIsRecoverableSwitchTarget(opp) {
				mode, selectReject = crossVenueOpenStrict, ""
			} else {
				mode, selectReject = crossVenueOpenReject, "pending switch 目标已失效"
			}
		}
		if mode == crossVenueOpenReject {
			rawRejectReasons = append(rawRejectReasons, selectReject)
			if trace != nil && selectReject != "" {
				trace.RejectReasons = append(trace.RejectReasons, selectReject)
			}
			if len(rejected) < 5 {
				rejected = append(rejected, fmt.Sprintf("%s(%s)", opp.Symbol, selectReject))
			}
			continue
		}
		if mode == crossVenueOpenFallback {
			opp = crossVenueApplyAcceptableFallback(opp)
			fallbackUsed = append(fallbackUsed, opp.Symbol)
			e.logger.Printf("[跨所执行] %s 使用 acceptable fallback: rank=%.2f size=%.2f notional=%.2f",
				opp.Symbol, opp.RankScore, opp.SizeMultiplier, opp.TradableNotional)
		}
		gateRes := candidate.Gate
		gateRes = crossVenueCapGateNotional(gateRes, opp)
		highCaps := e.venues[opp.HighVenue].Capabilities()
		lowCaps := e.venues[opp.LowVenue].Capabilities()
		refPrice := (gateRes.HighMid + gateRes.LowMid) / 2
		qty := alignCrossVenueQty(gateRes.NotionalUSDT, refPrice, gateRes.HighInstr, gateRes.LowInstr, highCaps, lowCaps)
		if !crossVenueQtyTradable(qty, refPrice, gateRes.HighInstr) {
			if trace != nil {
				trace.RejectReasons = append(trace.RejectReasons, "qty_not_tradable")
			}
			continue
		}
		switchIntent := candidate.SwitchIntent
		plan := crossVenueOpenPlan{
			Opp:             opp,
			Gate:            gateRes,
			Qty:             qty,
			RefPrice:        refPrice,
			IdemPrefix:      crossVenueIdemPrefix(venue.Symbol(opp.Symbol), opp.HighVenue),
			DecisionCycleID: cycleID,
			SnapshotID:      snap.SnapshotID,
			SnapshotTime:    snap.ScannedAt,
			SwitchIntent:    switchIntent,
			SelectionMode:   mode,
		}
		if switchIntent != nil {
			_ = e.updateSwitchIntentPhase(switchIntent.Symbol, switchIntent.TargetHighVenue, switchIntent.TargetLowVenue, "open_new")
		}
		pos, err := e.executeCrossVenueOpen(ctx, plan, live)
		if err != nil {
			e.logger.Printf("[跨所执行] 开仓失败 %s: %v", opp.Symbol, err)
			if trace != nil {
				trace.RejectReasons = append(trace.RejectReasons, "open_failed")
			}
			if isBybitAgreementRequiredError(err) {
				e.handleBybitAgreementOpenFailure(ctx, opp, err)
			}
			continue
		}
		if trace != nil {
			trace.WouldExecute = true
			trace.NotionalUSDT = gateRes.NotionalUSDT
			trace.Opp = opp
			trace.Gate = gateRes
			trace.HasGateNotional = true
		}
		// OFF 模式返回空持仓(只记日志不建仓),不能把零值记录灌进内存台账/面板。
		if pos.Symbol == "" {
			opened = append(opened, opp.Symbol+"(OFF)")
			if switchIntent == nil || crossVenueSwitchTargetOpenCompleted(pos) {
				_ = e.removeSwitchIntent(opp.Symbol, opp.HighVenue, opp.LowVenue)
			}
			continue
		}
		opened = append(opened, pos.Symbol)
		if switchIntent == nil || crossVenueSwitchTargetOpenCompleted(pos) {
			_ = e.removeSwitchIntent(opp.Symbol, opp.HighVenue, opp.LowVenue)
		}
		e.mu.Lock()
		e.positions = upsertCrossVenuePosition(e.positions, pos)
		e.mu.Unlock()
		unified := UnifiedOpportunityFromCrossVenue(cfg, opp, nil, gateRes.NotionalUSDT, refPrice)
		appendAttributionPredictionBestEffort(e.recordDir, predictionRecordFromOpportunity(unified, pos.OpenedAt))
		active = append(active, pos)
	}
	if len(rejected) > 0 {
		e.logger.Printf("[跨所执行] 本轮闸门拒绝 %d 例(前5): %s", len(rejected), strings.Join(rejected, "; "))
	}
	switch {
	case len(opened) > 0:
		explain.OpenSummary = "opened: " + strings.Join(opened, ",")
	default:
		explain.OpenSummary = "未开新仓"
	}
	if len(fallbackUsed) > 0 {
		explain.FallbackSummary = "acceptable_fallback: " + strings.Join(fallbackUsed, ",")
	} else if len(rawRejectReasons) > 0 {
		explain.FallbackSummary = "无 acceptable fallback"
	} else {
		explain.FallbackSummary = "none"
	}
	explain.NoTradeReasonsTop = buildCrossVenueReasonCounts(rawRejectReasons, 5)
	if len(unifiedTraceByKey) > 0 {
		unifiedTraces := make([]crossVenueUnifiedCandidateTrace, 0, len(unifiedTraceByKey))
		for _, trace := range unifiedTraceByKey {
			if trace == nil {
				continue
			}
			unifiedTraces = append(unifiedTraces, *trace)
		}
		e.appendUnifiedCandidateLogs(cycleID, cfg, unifiedTraces)
	}
	e.setLastCycle(explain)
}

// resolveTradeableOpportunity 处理「全局最优腿对含不可交易所」的机会(扫描器对每个 symbol 只保留
// 跨全部四所的全局最优对,binance 覆盖 798 币,全局最优经常换成含币安的对):
//   - 两腿都可交易 → 原样放行(零行为变更);
//   - 含不可交易所 → 用扫描器费率索引在可交易所子集(okx/gate/bybit)内重算最优对,
//     价差达标则返回替代机会继续走完整闸门;子集凑不出达标对才拒。
//
// 返回 (机会, 拒绝原因):原因为空 = 放行(机会可能已被替换);非空 = 本条机会跳过。
func (e *CrossVenueExecutor) resolveTradeableOpportunity(opp CrossVenueOpportunity, byVenue map[string]map[string]CrossVenueFundingEntry) (CrossVenueOpportunity, string) {
	if crossVenueTradeableVenues[opp.HighVenue] && crossVenueTradeableVenues[opp.LowVenue] {
		return opp, ""
	}
	alt, ok := bestTradeableSubsetPair(opp.Symbol, byVenue)
	if !ok {
		return opp, fmt.Sprintf("最优对(%s/%s)含不可交易所,费率索引中可交易所子集不足两所", opp.HighVenue, opp.LowVenue)
	}
	if alt.SpreadAPR < e.cfg.CrossVenueExecutor.OpenSpreadAPR && e.cfg.CrossVenueExecutor.OpenSpreadAPR > 0 {
		return opp, fmt.Sprintf("最优对(%s/%s)含不可交易所,子集内无达标替代(子集最优=%.1f%%)", opp.HighVenue, opp.LowVenue, alt.SpreadAPR*100)
	}
	e.logger.Printf("[跨所执行] %s 全局最优对(%s/%s)含不可交易所,降级用子集对(%s/%s, %.1f%%)",
		opp.Symbol, opp.HighVenue, opp.LowVenue, alt.HighVenue, alt.LowVenue, alt.SpreadAPR*100)
	return alt, ""
}

// enrichOpportunityFromSnapshot 为执行层兼容旧版/手工构造的机会快照:
// 只要 FundingByVenue 里有真值,就补齐 funding metadata、lineage 与派生 EV 指标,
// 避免旧测试或外部注入机会因缺 NextFundingTime/Interval 被误判为幽灵腿。
func (e *CrossVenueExecutor) enrichOpportunityFromSnapshot(opp CrossVenueOpportunity, snap CrossVenueScanSnapshot, now time.Time) CrossVenueOpportunity {
	if opp.Symbol == "" {
		return opp
	}
	if opp.SnapshotID == "" {
		opp.SnapshotID = snap.SnapshotID
	}
	if opp.SnapshotTime.IsZero() {
		opp.SnapshotTime = snap.ScannedAt
	}
	if opp.FreshnessAgeSeconds <= 0 && !snap.ScannedAt.IsZero() {
		opp.FreshnessAgeSeconds = crossVenueSnapshotAgeSeconds(snap.ScannedAt, now)
	}
	if entry, ok := snap.FundingByVenue[opp.HighVenue][opp.Symbol]; ok {
		opp.HighFundingRate = entry.Rate
		if entry.IntervalHours > 0 {
			opp.HighIntervalHours = entry.IntervalHours
		}
		if !entry.NextFundingTime.IsZero() {
			opp.HighNextFundingTime = entry.NextFundingTime
		}
		if opp.HighAnnualizedAPR == 0 && opp.HighIntervalHours > 0 {
			opp.HighAnnualizedAPR = fundingAnnualizedAPR(opp.HighFundingRate, opp.HighIntervalHours)
		}
	}
	if entry, ok := snap.FundingByVenue[opp.LowVenue][opp.Symbol]; ok {
		opp.LowFundingRate = entry.Rate
		if entry.IntervalHours > 0 {
			opp.LowIntervalHours = entry.IntervalHours
		}
		if !entry.NextFundingTime.IsZero() {
			opp.LowNextFundingTime = entry.NextFundingTime
		}
		if opp.LowAnnualizedAPR == 0 && opp.LowIntervalHours > 0 {
			opp.LowAnnualizedAPR = fundingAnnualizedAPR(opp.LowFundingRate, opp.LowIntervalHours)
		}
	}
	if opp.ComparableWindowHours <= 0 || opp.ExpectedHoldHours <= 0 || opp.NetEV == 0 || opp.RankScore == 0 {
		opp = crossVenueEstimateOpportunityMetrics(e.cfg.CrossVenueExecutor, opp, now)
		ranked := []CrossVenueOpportunity{opp}
		crossVenueFinalizeOpportunityRanking(e.cfg.CrossVenueExecutor, ranked)
		opp = ranked[0]
	}
	return opp
}

// bestTradeableSubsetPair 用扫描器费率索引对某 symbol 在可交易所子集内重算最优腿对。
// 年化口径与扫描器完全一致:fundingAnnualizedAPR 折算 + bestPairSpread 两两取最大,不另抄算法。
// 索引里该 symbol 覆盖不足两个可交易所时返回 not-ok(沿用现有降级语义)。
func bestTradeableSubsetPair(sym string, byVenue map[string]map[string]CrossVenueFundingEntry) (CrossVenueOpportunity, bool) {
	entries := make([]symbolVenueFunding, 0, len(crossVenueExecutorVenues))
	for _, name := range crossVenueExecutorVenues {
		entry, ok := byVenue[name][sym]
		if !ok {
			continue
		}
		interval := entry.IntervalHours
		if interval <= 0 {
			interval = 8
		}
		entries = append(entries, symbolVenueFunding{
			Venue:            name,
			FundingRate:      entry.Rate,
			RawFundingRate:   entry.Rate,
			AnnualizedAPR:    fundingAnnualizedAPR(entry.Rate, interval),
			RawAnnualizedAPR: fundingAnnualizedAPR(entry.Rate, interval),
			IntervalHours:    interval,
			NextFundingTime:  entry.NextFundingTime,
		})
	}
	if len(entries) < 2 {
		return CrossVenueOpportunity{}, false
	}
	return bestPairSpread(sym, entries)
}

func upsertCrossVenuePosition(ps []CrossVenuePosition, p CrossVenuePosition) []CrossVenuePosition {
	crossVenueEnsurePositionKey(&p)
	for i := range ps {
		if crossVenueSamePosition(ps[i], p) {
			ps[i] = p
			return ps
		}
	}
	return append(ps, p)
}

func (e *CrossVenueExecutor) gateDeps(snap CrossVenueScanSnapshot) crossVenueGateDeps {
	return crossVenueGateDeps{
		lookupInstrument: lookupVenueInstrument,
		fetchBookTicker: func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error) {
			return v.FetchBookTicker(ctx, venue.MarketPerp, sym)
		},
		fetchDepth: func(ctx context.Context, v venue.Venue, sym venue.Symbol, limit int) (venue.Depth, error) {
			return v.FetchDepth(ctx, venue.MarketPerp, sym, limit)
		},
		fetchFunding: func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.FundingSnapshot, bool) {
			_ = ctx
			return e.fetchFundingFromSnapshot(v, sym, snap)
		},
		fetchPerpAvailableUSDT: fetchVenuePerpAvailableUSDT,
		venueCaps:              func(v venue.Venue) venue.Capabilities { return v.Capabilities() },
		allocatedUSDT:          e.allocatedUSDTForVenue,
	}
}

func (e *CrossVenueExecutor) snapshotFreshEnough(scannedAt time.Time) bool {
	maxAge := e.cfg.CrossVenueScanner.MaxSnapshotAgeSeconds
	if maxAge <= 0 {
		maxAge = 720
	}
	return crossVenueSnapshotAgeSeconds(scannedAt, e.nowFn()) <= float64(maxAge)
}

func (e *CrossVenueExecutor) fetchFundingFromSnapshot(v venue.Venue, sym venue.Symbol, snap CrossVenueScanSnapshot) (venue.FundingSnapshot, bool) {
	if v == nil {
		return venue.FundingSnapshot{}, false
	}
	name := strings.ToLower(strings.TrimSpace(v.Name()))
	if name == "" {
		switch vv := v.(type) {
		case interface{ Name() string }:
			name = strings.ToLower(strings.TrimSpace(vv.Name()))
		}
	}
	if name == "" {
		for venueName, adp := range e.venues {
			if adp == v {
				name = venueName
				break
			}
		}
	}
	if name == "" {
		return venue.FundingSnapshot{}, false
	}
	entry, ok := snap.FundingByVenue[name][string(sym)]
	if !ok {
		return venue.FundingSnapshot{}, false
	}
	return venue.FundingSnapshot{
		Symbol:          sym,
		LastFundingRate: entry.Rate,
		IntervalHours:   entry.IntervalHours,
		NextFundingTime: entry.NextFundingTime,
	}, true
}

func (e *CrossVenueExecutor) allocatedUSDTForVenue(venueName string) float64 {
	if e.orch != nil {
		if rt, ok := e.orch.Get(venueName); ok && rt != nil && rt.allocatedCapitalUSDT > 0 {
			return rt.allocatedCapitalUSDT
		}
	}
	dir := recordDirForVenue(e.cfg, venueName)
	if ov, ok := loadVenueCapitalOverride(dir); ok {
		return ov
	}
	return e.cfg.AllocatedCapitalUSDT
}

func (e *CrossVenueExecutor) reconcileOnStart(ctx context.Context) {
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[跨所执行] reconcile 读台账失败: %v", err)
		return
	}
	active := activeCrossVenuePositions(ps)
	if len(active) == 0 {
		ps = nil
	}
	if len(active) > 0 {
		e.logger.Printf("[跨所执行] reconcile: %d 笔落盘持仓", len(active))
		for _, p := range active {
			highV := e.venues[p.HighVenue]
			lowV := e.venues[p.LowVenue]
			if highV == nil || lowV == nil {
				e.logger.Printf("[跨所执行] reconcile:%s venue 未就绪,保留台账", p.Symbol)
				continue
			}
			e.reconcileTrackedPositionOnStart(ctx, p, highV, lowV)
		}
	}
	// 所内有但台账没有 → 进入显式 import/flatten/halt 分支。
	untrackedBySymbol := make(map[string][]crossVenueUntrackedLeg)
	for _, name := range crossVenueExecutorVenues {
		v := e.venues[name]
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err != nil {
			continue
		}
		for _, ep := range positions {
			if absQty(ep.PositionAmt) <= executionQtyTolerance {
				continue
			}
			if crossVenueHasTrackedLeg(ps, name, ep.Symbol) {
				continue
			}
			if carryStrategyOwnsPerpLeg(e.recordDir, name, ep.Symbol) {
				e.logger.Printf("[跨所执行] reconcile: venue=%s %s≈%.6f 归属 carry 策略台账,跳过未登记判定", name, ep.Symbol, ep.PositionAmt)
				continue
			}
			e.logger.Printf("[跨所执行] reconcile: venue=%s 未登记持仓 %s≈%.6f,请人工核对", name, ep.Symbol, ep.PositionAmt)
			untrackedBySymbol[string(ep.Symbol)] = append(untrackedBySymbol[string(ep.Symbol)], crossVenueUntrackedLeg{
				Venue:      name,
				Symbol:     string(ep.Symbol),
				Qty:        ep.PositionAmt,
				MarkPrice:  ep.MarkPrice,
				EntryPrice: ep.EntryPrice,
			})
		}
	}
	if len(untrackedBySymbol) > 0 {
		action := crossVenueUntrackedExposureAction(e.cfg.CrossVenueExecutor)
		if !e.handleUntrackedExposureOnStart(ctx, action, untrackedBySymbol) {
			_ = e.SetOpeningsEnabled(false)
			e.notifyCritical("跨所冷启动接管", "检测到未登记真实持仓,已关闭跨所开仓直到人工确认", "")
		}
	}
	if latest, lerr := e.store.load(); lerr == nil {
		ps = latest
	}
	e.mu.Lock()
	e.positions = ps
	e.mu.Unlock()
}

func crossVenueUntrackedExposureAction(cfg CrossVenueExecutorConfig) string {
	action := strings.ToLower(strings.TrimSpace(cfg.UntrackedExposureAction))
	switch action {
	case crossVenueRecoveryActionImport, crossVenueRecoveryActionFlatten, crossVenueRecoveryActionHalt:
		return action
	default:
		return crossVenueRecoveryActionHalt
	}
}

func (e *CrossVenueExecutor) handleUntrackedExposureOnStart(ctx context.Context, action string, bySymbol map[string][]crossVenueUntrackedLeg) bool {
	if e == nil || len(bySymbol) == 0 {
		return true
	}
	cycleID := crossVenueDecisionCycleID(e.nowFn())
	allHandled := true
	for symbol, legs := range bySymbol {
		pos, ok := e.buildUntrackedImportedPosition(symbol, legs, cycleID)
		if !ok {
			allHandled = false
			continue
		}
		switch action {
		case crossVenueRecoveryActionImport:
			pos.TransitionState = "import"
			pos.RecoveryAction = crossVenueRecoveryActionImport
			if err := e.store.upsert(pos); err != nil {
				allHandled = false
				continue
			}
			e.recordCrossVenueTransition(pos, "", "import", "冷启动导入未登记真实持仓")
			e.notifyWarn("跨所冷启动导入", symbol+" 已导入 SSOT，后续按在场仓管理", symbol)
		case crossVenueRecoveryActionFlatten:
			if !e.cfg.CrossVenueExecutor.LiveTradingEnabled {
				allHandled = false
				continue
			}
			pos.TransitionState = "flatten"
			pos.RecoveryAction = crossVenueRecoveryActionFlatten
			if err := e.store.upsert(pos); err != nil {
				allHandled = false
				continue
			}
			e.recordCrossVenueTransition(pos, "", "flatten", "冷启动对未登记真实持仓执行 flatten")
			if err := e.closeCrossVenuePosition(ctx, pos, true, "冷启动 flatten 未登记真实持仓"); err != nil {
				allHandled = false
				continue
			}
			e.notifyWarn("跨所冷启动 flatten", symbol+" 已执行平仓收敛", symbol)
		default:
			allHandled = false
			e.recordCrossVenueTransition(pos, "", "halt", "冷启动发现未登记真实持仓")
		}
	}
	return allHandled && action != crossVenueRecoveryActionHalt
}

func (e *CrossVenueExecutor) buildUntrackedImportedPosition(symbol string, legs []crossVenueUntrackedLeg, cycleID string) (CrossVenuePosition, bool) {
	if len(legs) != 2 {
		return CrossVenuePosition{}, false
	}
	var high, low *crossVenueUntrackedLeg
	for i := range legs {
		if legs[i].Qty < -executionQtyTolerance {
			if high != nil {
				return CrossVenuePosition{}, false
			}
			high = &legs[i]
		}
		if legs[i].Qty > executionQtyTolerance {
			if low != nil {
				return CrossVenuePosition{}, false
			}
			low = &legs[i]
		}
	}
	if high == nil || low == nil {
		return CrossVenuePosition{}, false
	}
	now := e.nowFn()
	highPrice := high.EntryPrice
	if highPrice <= 0 {
		highPrice = high.MarkPrice
	}
	lowPrice := low.EntryPrice
	if lowPrice <= 0 {
		lowPrice = low.MarkPrice
	}
	pos := CrossVenuePosition{
		Symbol:               symbol,
		HighVenue:            high.Venue,
		LowVenue:             low.Venue,
		DecisionCycleID:      cycleID,
		EntryDecisionCycleID: cycleID,
		Status:               CrossVenueStatusOpen,
		OpenedAt:             now,
		UpdatedAt:            now,
		IdemPrefix:           crossVenueIdemPrefix(venue.Symbol(symbol), high.Venue),
		HighLeg: CrossVenueLegState{
			Venue:      high.Venue,
			Side:       "sell",
			Qty:        absQty(high.Qty),
			EntryPrice: highPrice,
		},
		LowLeg: CrossVenueLegState{
			Venue:      low.Venue,
			Side:       "buy",
			Qty:        absQty(low.Qty),
			EntryPrice: lowPrice,
		},
	}
	pos.NotionalUSDT = math.Min(absQty(high.Qty)*highPrice, absQty(low.Qty)*lowPrice)
	if pos.NotionalUSDT <= 0 {
		pos.NotionalUSDT = math.Min(absQty(high.Qty), absQty(low.Qty)) * maxFloat64(highPrice, lowPrice)
	}
	e.refreshCurrentPositionMetrics(&pos)
	return pos, true
}

func (e *CrossVenueExecutor) persistPosition(p CrossVenuePosition) error {
	crossVenueEnsurePositionKey(&p)
	if err := e.store.upsert(p); err != nil {
		e.logger.Printf("[跨所执行] 落盘失败 %s: %v", p.Symbol, err)
		return err
	}
	e.mu.Lock()
	e.positions = upsertCrossVenuePosition(e.positions, p)
	e.mu.Unlock()
	return nil
}

// removeCrossVenuePosition 从落盘台账与内存台账同时移除(平仓收尾/reconcile orphan 必须双删,
// 否则 manageOpenPositions 下轮仍从 snapshotPositions 读到 stale closing 持仓)。
func (e *CrossVenueExecutor) removeCrossVenuePosition(pos CrossVenuePosition) error {
	key := crossVenuePositionKey(pos)
	if err := e.store.remove(key); err != nil {
		e.logger.Printf("[跨所执行] 删台账失败 %s key=%s: %v", pos.Symbol, key, err)
		return err
	}
	e.mu.Lock()
	out := make([]CrossVenuePosition, 0, len(e.positions))
	for _, p := range e.positions {
		if crossVenuePositionKey(p) != key {
			out = append(out, p)
		}
	}
	e.positions = out
	e.mu.Unlock()
	return nil
}

func (e *CrossVenueExecutor) snapshotPositions() []CrossVenuePosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]CrossVenuePosition, len(e.positions))
	copy(out, e.positions)
	return out
}

// PositionsSnapshot 返回台账+模式(供 HTTP API)。
func (e *CrossVenueExecutor) PositionsSnapshot() CrossVenuePositionsAPIResponse {
	ps, _ := e.store.load()
	if ps == nil {
		ps = e.snapshotPositions()
	}
	pending, partial, orphan, transitioning, headroom := crossVenueTransitionMetrics(ps)
	return CrossVenuePositionsAPIResponse{
		Enabled:                e.cfg.CrossVenueExecutor.Enabled,
		Live:                   e.cfg.CrossVenueExecutor.LiveTradingEnabled,
		Mode:                   crossVenueExecutorModeLabel(e.cfg),
		OpeningsEnabled:        e.OpeningsEnabled(),
		HealthHaltVenue:        e.healthHaltVenueSnapshot(),
		ActivePositions:        len(activeCrossVenuePositions(ps)),
		PendingSwitches:        pending,
		PartialSwitches:        partial,
		OrphanPositions:        orphan,
		TransitioningPositions: transitioning,
		TransitionHeadroomUSDT: headroom,
		LastCycle:              e.lastCycleSnapshot(),
		ActivePositionDetails:  buildCrossVenuePositionDetails(e.cfg.CrossVenueExecutor, ps, e.nowFn()),
		Positions:              ps,
		UpdatedAt:              e.nowFn(),
	}
}

func (e *CrossVenueExecutor) setLastCycle(explain CrossVenueCycleExplain) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.lastCycle = explain
	e.mu.Unlock()
	e.appendCycleExplain(explain)
}

func (e *CrossVenueExecutor) lastCycleSnapshot() CrossVenueCycleExplain {
	if e == nil {
		return CrossVenueCycleExplain{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastCycle
}

func (e *CrossVenueExecutor) setSymbolCooldown(sym string) {
	e.setSymbolCooldownWithSeverity(sym, "execution", "major")
}

func (e *CrossVenueExecutor) setSymbolCooldownWithSeverity(sym, category, severity string) {
	if strings.TrimSpace(sym) == "" {
		return
	}
	d := CrossVenueSymbolCooldownForCategory(e.cfg.RiskConfig, e.cfg.CrossVenueExecutor, category, severity)
	e.mu.Lock()
	e.symbolCooldown[sym] = e.nowFn().Add(d)
	e.mu.Unlock()
}

// setSymbolAgreementCooldown 对 Bybit 110126 协议未签 symbol 施加更长冷却(非永久黑名单,签完可交易)。
func (e *CrossVenueExecutor) setSymbolAgreementCooldown(sym string) {
	d := CrossVenueSymbolCooldownForCategory(e.cfg.RiskConfig, e.cfg.CrossVenueExecutor, "bybit_agreement", "major")
	e.mu.Lock()
	e.symbolCooldown[sym] = e.nowFn().Add(d)
	e.mu.Unlock()
}

func (e *CrossVenueExecutor) handleBybitAgreementOpenFailure(ctx context.Context, opp CrossVenueOpportunity, err error) {
	if e.venues[bybit.VenueName] != nil {
		ensureBybitTradingAgreement(ctx, e.logger, e.venues[bybit.VenueName])
	}
	sym := extractBybitAgreementSymbol(err, opp)
	e.setSymbolAgreementCooldown(sym)
	hours := e.cfg.CrossVenueExecutor.BybitAgreementCooldownHours
	if hours <= 0 {
		hours = 6
	}
	e.notifyWarn("Bybit 合约协议",
		fmt.Sprintf("请在 Bybit App 手动签署 %s 合约协议(retCode=110126);已冷却 %d 小时跳过", sym, hours),
		sym)
}

// fundingFromScanner 从扫描器缓存查某所某 symbol 的费率明细(快照最旧 10 分钟,对平仓判断/记账精度足够)。
// 缓存缺失(扫描器未注入/该所本轮失败/symbol 不在快照)返回 not-ok,**绝不现场拉全量快照**——
// OKX 全量逐币串行拉一次 20–46s,管理循环现场拉会拖死 60s 主循环并打爆限频。
func (e *CrossVenueExecutor) fundingFromScanner(venueName string, sym venue.Symbol) (CrossVenueFundingEntry, bool) {
	if e.scanner == nil {
		return CrossVenueFundingEntry{}, false
	}
	byVenue := e.scanner.Snapshot().FundingByVenue
	if byVenue == nil {
		return CrossVenueFundingEntry{}, false
	}
	entry, ok := byVenue[venueName][string(sym)]
	return entry, ok
}

func (e *CrossVenueExecutor) isSymbolCooling(sym string) bool {
	e.mu.RLock()
	until, ok := e.symbolCooldown[sym]
	e.mu.RUnlock()
	return ok && e.nowFn().Before(until)
}

// OpenBlockedForSymbol 决策层查询:冷却/回滚隔离/连续失败隔离。
func (e *CrossVenueExecutor) OpenBlockedForSymbol(symbol string) bool {
	if e == nil {
		return false
	}
	return e.isSymbolCooling(symbol) || e.openQuarantine.isQuarantined(symbol)
}

func (e *CrossVenueExecutor) recordOpenOutcome(symbol string, success bool) {
	if e == nil {
		return
	}
	if success {
		e.openQuarantine.recordOpenSuccess(symbol)
		return
	}
	e.openQuarantine.recordOpenFailure(symbol)
}

func (e *CrossVenueExecutor) notifyTrade(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.NotifyTrade(Alert{Level: EventLevelInfo, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

func (e *CrossVenueExecutor) notifyWarn(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.Notify(Alert{Level: EventLevelWarning, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

func (e *CrossVenueExecutor) notifyCritical(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.Notify(Alert{Level: EventLevelCritical, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

// CloseAllPositions 一键全平:载入在场台账,FetchPositions 对账后只平真实存在的腿;纯合成记录仅清台账不下单。
func (e *CrossVenueExecutor) CloseAllPositions(ctx context.Context, live bool) (closed int, errs []error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	ps, err := e.store.load()
	if err != nil {
		return 0, []error{err}
	}
	for _, pos := range activeCrossVenuePositions(ps) {
		highV := e.venues[pos.HighVenue]
		lowV := e.venues[pos.LowVenue]
		if highV == nil || lowV == nil {
			errs = append(errs, fmt.Errorf("%s: venue 未就绪", pos.Symbol))
			continue
		}
		sym := venue.Symbol(pos.Symbol)
		hPos, errH := highV.FetchPositions(ctx)
		lPos, errL := lowV.FetchPositions(ctx)
		if errH != nil || errL != nil {
			errs = append(errs, fmt.Errorf("%s: 拉持仓失败", pos.Symbol))
			continue
		}
		hOpen := absQty(perpPositionQty(hPos, sym)) > executionQtyTolerance
		lOpen := absQty(perpPositionQty(lPos, sym)) > executionQtyTolerance
		if !hOpen && !lOpen {
			pos.Status = CrossVenueStatusClosed
			pos.CloseReason = "flatten_all_orphan"
			pos.RecoveryAction = crossVenueRecoveryActionObserve
			e.finalizeClosedCrossVenuePosition(pos, live)
			closed++
			continue
		}
		if cerr := e.closeCrossVenuePosition(ctx, pos, live, "flatten_all"); cerr != nil {
			errs = append(errs, cerr)
		} else {
			closed++
		}
	}
	return closed, errs
}

// crossVenueExecutorRecordDir 返回跨所台账目录(测试/文档用)。
func crossVenueExecutorRecordDir(base string) string {
	return filepath.Join(strings.TrimSpace(base), "crossvenue")
}
