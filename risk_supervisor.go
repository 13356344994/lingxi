package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// RiskSupervisor 收口平台熔断、symbol 冷却、恢复预算与不确定订单等风控职责。
// 第一阶段先保持对 Engine 内状态的托管，不改变现有业务语义。
type RiskSupervisor struct {
	engine *FundingRateSniperEngine
}

type ProtectedIOCStatsSnapshot struct {
	Attempts         int
	Canceled         int
	Expired          int
	Partial          int
	AvgUnfilledRatio float64
	CancelRate       float64
}

type RiskSnapshot struct {
	PlatformRejectCount        int
	PlatformRequestErrors      int
	RecoveryActionCount        int
	OrderUncertaintyCount      int
	FuseTripped                bool
	FuseReason                 string
	CooldownUntil              time.Time
	LastRecoveryAt             time.Time
	LastRecoveryAction         string
	LastUncertainOrder         string
	RemainingIncidentBudget    int
	RemainingRecoveryBudget    int
	RemainingUncertaintyBudget int
	RemainingRejectBudget      int
	RemainingErrorBudget       int
	ProtectedIOC               ProtectedIOCStatsSnapshot
	HealthStatus               string
	HealthSummary              string
	CanContinue                bool
	ConsecutiveRejects         int
	ConsecutiveErrors          int
	LastExceptionCategory      string
	LastExceptionSeverity      string
	LastExceptionDetail        string
	CurrentCooldownCategory    string
	CurrentCooldownSeverity    string
	CooledSymbols              []string
}

func (e *FundingRateSniperEngine) ensureRiskSupervisor() *RiskSupervisor {
	if e == nil {
		return nil
	}
	e.riskSupervisorOnce.Do(func() {
		e.riskSupervisor = newRiskSupervisor(e)
	})
	return e.riskSupervisor
}

func newRiskSupervisor(engine *FundingRateSniperEngine) *RiskSupervisor {
	if engine == nil {
		return nil
	}
	return &RiskSupervisor{engine: engine}
}

func (s *RiskSupervisor) CheckPlatformFuse() error {
	engine := s.engine
	engine.platformRiskMu.Lock()
	defer engine.platformRiskMu.Unlock()
	if !engine.platformState.FuseTripped {
		return nil
	}
	return platformFuseError{message: engine.platformState.FuseReason}
}

func (s *RiskSupervisor) Snapshot() RiskSnapshot {
	engine := s.engine
	engine.platformRiskMu.Lock()
	defer engine.platformRiskMu.Unlock()
	return s.SnapshotLocked()
}

func (s *RiskSupervisor) SnapshotLocked() RiskSnapshot {
	engine := s.engine
	healthStatus, healthSummary, canContinue := engine.healthStatusLocked()
	attempts, canceled, expired, partial, avgUnfilledRatio, cancelRate := engine.protectedIOCStatsLocked()
	return RiskSnapshot{
		PlatformRejectCount:        engine.platformRejectsThisCycle,
		PlatformRequestErrors:      engine.requestErrorsThisCycle,
		RecoveryActionCount:        engine.recoveryActionsThisCycle,
		OrderUncertaintyCount:      engine.orderUncertaintyCount,
		FuseTripped:                engine.platformState.FuseTripped,
		FuseReason:                 engine.platformState.FuseReason,
		CooldownUntil:              engine.platformState.CooldownUntil,
		LastRecoveryAt:             engine.lastRecoveryAt,
		LastRecoveryAction:         engine.lastRecoveryAction,
		LastUncertainOrder:         engine.lastUncertainOrder,
		RemainingIncidentBudget:    remainingRiskBudget(engine.cfg.RiskConfig.MaxExecutionIncidents, int(atomic.LoadInt32(&engine.executionIncidents))),
		RemainingRecoveryBudget:    remainingRiskBudget(engine.cfg.RiskConfig.MaxRecoveryActionsPerCycle, engine.recoveryActionsThisCycle),
		RemainingUncertaintyBudget: remainingRiskBudget(engine.cfg.RiskConfig.MaxOrderUncertaintyPerCycle, engine.orderUncertaintyCount),
		RemainingRejectBudget:      remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutivePlatformRejects, engine.platformRejectsThisCycle),
		RemainingErrorBudget:       remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutiveRequestErrors, engine.requestErrorsThisCycle),
		ProtectedIOC: ProtectedIOCStatsSnapshot{
			Attempts:         attempts,
			Canceled:         canceled,
			Expired:          expired,
			Partial:          partial,
			AvgUnfilledRatio: avgUnfilledRatio,
			CancelRate:       cancelRate,
		},
		HealthStatus:            healthStatus,
		HealthSummary:           healthSummary,
		CanContinue:             canContinue,
		ConsecutiveRejects:      engine.consecutivePlatformRejects,
		ConsecutiveErrors:       engine.consecutiveRequestErrors,
		LastExceptionCategory:   engine.platformState.LastExceptionCategory,
		LastExceptionSeverity:   engine.platformState.LastExceptionSeverity,
		LastExceptionDetail:     engine.platformState.LastExceptionDetail,
		CurrentCooldownCategory: engine.platformState.CurrentCooldownCategory,
		CurrentCooldownSeverity: engine.platformState.CurrentCooldownSeverity,
		CooledSymbols:           append([]string(nil), engine.listActiveSymbolCooldownsLocked()...),
	}
}

func (s *RiskSupervisor) NotePlatformRequestSuccess() {
	engine := s.engine
	engine.platformRiskMu.Lock()
	defer engine.platformRiskMu.Unlock()
	engine.consecutiveRequestErrors = 0
	engine.consecutivePlatformRejects = 0
	engine.runtimeReporter.EmitLocked(EngineStage(""))
}

func (s *RiskSupervisor) NotePlatformRequestResult(endpoint string, err error) error {
	engine := s.engine
	if err == nil {
		s.NotePlatformRequestSuccess()
		return nil
	}
	category, severity := classifyPlatformException(err)
	reject := category == "platform_reject" || category == "order_rate_limit"

	// 统一错误归类判断，不让业务逻辑感知 Binance 原始报错文本
	requestAbnormal := reject ||
		isUnknownOrderRequestError(err) ||
		isUnknownOrderStateError(err) ||
		category == "network_error" ||
		category == "unknown_order" ||
		category == "api_key_error"

	if category == "order_rate_limit" {
		engine.orderRateMu.Lock()
		cooldownUntil := time.Now().UTC().Add(orderRateLimitExchangeCooldown)
		if cooldownUntil.After(engine.platformState.OrderRateLimitUntil) {
			engine.platformState.OrderRateLimitUntil = cooldownUntil
		}
		engine.orderRateMu.Unlock()
	}

	engine.platformRiskMu.Lock()
	engine.platformState.RecordException(category, severity, fmt.Sprintf("endpoint=%s err=%v", endpoint, err))
	if requestAbnormal {
		engine.consecutiveRequestErrors++
		engine.riskBudgetState.AddRequestError()
	} else {
		engine.consecutiveRequestErrors = 0
	}
	if reject {
		engine.consecutivePlatformRejects++
		engine.riskBudgetState.AddPlatformReject()
	} else {
		engine.consecutivePlatformRejects = 0
	}

	requestErrors := engine.consecutiveRequestErrors
	rejects := engine.consecutivePlatformRejects
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	engine.platformRiskMu.Unlock()

	if engine.cfg.RiskConfig.MaxConsecutivePlatformRejects > 0 && rejects >= engine.cfg.RiskConfig.MaxConsecutivePlatformRejects {
		return s.TripPlatformFuse(category, severity, fmt.Sprintf("平台连续拒绝请求，endpoint=%s consecutive_rejects=%d err=%v", endpoint, rejects, err))
	}
	if engine.cfg.RiskConfig.MaxConsecutiveRequestErrors > 0 && requestErrors >= engine.cfg.RiskConfig.MaxConsecutiveRequestErrors {
		return s.TripPlatformFuse(category, severity, fmt.Sprintf("平台连续异常请求过多，endpoint=%s consecutive_errors=%d err=%v", endpoint, requestErrors, err))
	}
	return nil
}

func (s *RiskSupervisor) NoteRecoveryAction(phase string, symbol string, action string, detail string) error {
	engine := s.engine
	engine.platformRiskMu.Lock()
	engine.riskBudgetState.AddRecoveryAction()
	recoveryActions := engine.riskBudgetState.RecoveryActionsThisCycle
	engine.platformState.RecordException("recovery_action", "major", fmt.Sprintf("phase=%s symbol=%s action=%s detail=%s", phase, nonEmpty(symbol, "-"), action, nonEmpty(detail, "-")))
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	engine.platformRiskMu.Unlock()
	engine.emitRiskEvent(newRiskEvent("action", RiskCategoryRecovery, RiskSeverityWarning, nonEmpty(phase, "recovery"), symbol, nonEmpty(action, "recover"), int(atomic.LoadInt32(&engine.executionIncidents)), engine.cfg.RiskConfig.MaxExecutionIncidents, false, detail).WithEventType(EventTypeRecoveryEscalated))

	if engine.cfg.RiskConfig.MaxRecoveryActionsPerCycle > 0 && recoveryActions > engine.cfg.RiskConfig.MaxRecoveryActionsPerCycle {
		return s.TripPlatformFuse("recovery_action", "critical", fmt.Sprintf("单轮恢复动作过多，phase=%s symbol=%s action=%s count=%d detail=%s", phase, nonEmpty(symbol, "-"), action, recoveryActions, nonEmpty(detail, "-")))
	}
	return nil
}

func (s *RiskSupervisor) TripPlatformFuse(category string, severity string, reason string) error {
	engine := s.engine
	engine.platformRiskMu.Lock()
	if engine.platformState.FuseTripped {
		reason = engine.platformState.FuseReason
		engine.platformRiskMu.Unlock()
		return platformFuseError{message: reason}
	}
	engine.platformState.TripFuse(reason)
	cooldown := s.CooldownForSeverity(severity)
	engine.platformState.ApplyCooldown(category, severity, cooldown)
	engine.platformState.RecordException(category, severity, reason)

	engine.runtimeReporter.EmitLockedWithReason(StageCooldown, engine.platformState.FuseReason)
	message := engine.platformState.FuseReason
	engine.platformRiskMu.Unlock()

	engine.logger.Printf("%s", message)
	engine.emitRiskEvent(newRiskEvent("incident", RiskCategoryPlatform, RiskSeverityCritical, "platform_fuse", "-", "trip", int(atomic.LoadInt32(&engine.executionIncidents)), engine.cfg.RiskConfig.MaxExecutionIncidents, true, reason).WithEventType(EventTypeFuseTripped))
	return platformFuseError{message: message}
}

func (s *RiskSupervisor) WaitPlatformCooldown(ctx context.Context) error {
	engine := s.engine
	engine.platformRiskMu.Lock()
	cooldownUntil := engine.platformState.CooldownUntil
	tripped := engine.platformState.FuseTripped
	engine.platformRiskMu.Unlock()
	if !tripped || cooldownUntil.IsZero() {
		return nil
	}
	if !cooldownUntil.After(time.Now().UTC()) {
		s.ClearPlatformFuse()
		return nil
	}
	engine.runtimeReporter.EmitWithReason(StageCooldown, 0, fmt.Sprintf("等待平台冷却结束，截止到 %s", cooldownUntil.UTC().Format(time.RFC3339)))
	for cooldownUntil.After(time.Now().UTC()) {
		if err := engine.checkLiveSafetyGuards("platform_cooldown"); err != nil {
			return err
		}
		wait := time.Until(cooldownUntil)
		if wait > time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	s.ClearPlatformFuse()
	return nil
}

// CurrentGear 返回系统当前的建议运行档位
func (s *RiskSupervisor) CurrentGear() EngineGear {
	s.engine.platformRiskMu.Lock()
	defer s.engine.platformRiskMu.Unlock()
	return s.currentGearLocked()
}

// currentGearLocked 返回建议运行档位，调用方必须已持有 platformRiskMu。
func (s *RiskSupervisor) currentGearLocked() EngineGear {
	if s.engine.platformState.FuseTripped {
		return GearObserve
	}
	score := s.currentHealthScoreLocked()
	if score <= 0.4 {
		return GearDefensive
	}
	// 将配置的 profile 映射回默认 Gear
	switch s.engine.cfg.ActiveProfile {
	case ProfileSmallCapital:
		return GearConservative
	default:
		return GearNormal
	}
}

func (s *RiskSupervisor) ClearPlatformFuse() {
	engine := s.engine
	engine.platformRiskMu.Lock()
	engine.platformState.ClearFuse()
	engine.runtimeReporter.EmitLockedWithReason(StageReady, "平台熔断已解除")
	engine.platformRiskMu.Unlock()
}

func (s *RiskSupervisor) CooldownForSeverity(severity string) time.Duration {
	engine := s.engine
	switch strings.TrimSpace(severity) {
	case "critical":
		if engine.cfg.RiskConfig.PlatformCriticalCooldown > 0 {
			return engine.cfg.RiskConfig.PlatformCriticalCooldown
		}
		return engine.cfg.RiskConfig.PlatformAbnormalCooldown
	case "minor":
		if engine.cfg.RiskConfig.PlatformMinorCooldown > 0 {
			return engine.cfg.RiskConfig.PlatformMinorCooldown
		}
		return engine.cfg.RiskConfig.PlatformAbnormalCooldown
	default:
		return engine.cfg.RiskConfig.PlatformAbnormalCooldown
	}
}

func (s *RiskSupervisor) ListActiveSymbolCooldownsLocked() []string {
	engine := s.engine
	now := time.Now().UTC()
	items := make([]string, 0, len(engine.symbolCooldowns))
	for symbol, state := range engine.symbolCooldowns {
		if !state.Until.IsZero() && !state.Until.After(now) {
			delete(engine.symbolCooldowns, symbol)
			continue
		}
		if state.Until.IsZero() {
			continue
		}
		items = append(items, fmt.Sprintf("%s | %s | %s | until=%s | %s", symbol, nonEmpty(state.Category, "-"), nonEmpty(state.Severity, "-"), state.Until.Format(time.RFC3339), nonEmpty(state.Reason, "-")))
	}
	sort.Strings(items)
	return items
}

func (s *RiskSupervisor) SetSymbolCooldown(symbol string, category string, severity string, reason string) {
	engine := s.engine
	if strings.TrimSpace(symbol) == "" || engine.cfg.RiskConfig.SymbolAbnormalCooldown <= 0 {
		return
	}
	until := time.Now().UTC().Add(engine.cfg.RiskConfig.SymbolAbnormalCooldown)
	engine.platformRiskMu.Lock()
	engine.symbolCooldowns[symbol] = symbolCooldownState{
		Category: strings.TrimSpace(category),
		Severity: strings.TrimSpace(severity),
		Reason:   strings.TrimSpace(reason),
		Until:    until,
	}
	engine.platformState.RecordException(category, severity, reason)
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	engine.platformRiskMu.Unlock()
	engine.emitRiskEvent(newRiskEvent("action", RiskCategoryPlatform, nonEmpty(strings.TrimSpace(severity), RiskSeverityInfo), "symbol_cooldown", symbol, "set", int(atomic.LoadInt32(&engine.executionIncidents)), engine.cfg.RiskConfig.MaxExecutionIncidents, false, fmt.Sprintf("until=%s reason=%s", until.Format(time.RFC3339), nonEmpty(reason, "-"))).WithEventType(EventTypeSymbolCooldownSet))
}

func (s *RiskSupervisor) FilterTargetsBySymbolCooldown(targets []*MonitoredTarget) []*MonitoredTarget {
	engine := s.engine
	now := time.Now().UTC()
	filtered := make([]*MonitoredTarget, 0, len(targets))
	engine.platformRiskMu.Lock()
	defer engine.platformRiskMu.Unlock()
	for _, target := range targets {
		if target == nil {
			continue
		}
		state, ok := engine.symbolCooldowns[target.Candidate.Symbol]
		if !ok {
			if lockState, locked := engine.exposureLocks[target.Candidate.Symbol]; locked {
				engine.logger.Printf("symbol 被 ExposureManager 锁定，跳过 %s phase=%s mode=%s locked_at=%s", target.Candidate.Symbol, lockState.Phase, lockState.Mode, lockState.LockedAt.Format(time.RFC3339))
				continue
			}
			filtered = append(filtered, target)
			continue
		}
		if !state.Until.After(now) {
			delete(engine.symbolCooldowns, target.Candidate.Symbol)
			filtered = append(filtered, target)
			continue
		}
		engine.logger.Printf("symbol 冷却中，跳过 %s category=%s severity=%s until=%s reason=%s", target.Candidate.Symbol, state.Category, state.Severity, state.Until.Format(time.RFC3339), state.Reason)
	}
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	return filtered
}

func (s *RiskSupervisor) NoteRecoveryStatus(symbol string, action string, detail string) {
	engine := s.engine
	engine.platformRiskMu.Lock()
	engine.lastRecoveryAt = time.Now().UTC()
	engine.lastRecoveryAction = strings.TrimSpace(fmt.Sprintf("%s | %s | %s", nonEmpty(symbol, "-"), nonEmpty(action, "action"), nonEmpty(detail, "-")))
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	engine.platformRiskMu.Unlock()
}

func (s *RiskSupervisor) NoteOrderUncertainty(symbol string, action string, detail string) {
	engine := s.engine
	engine.platformRiskMu.Lock()
	engine.riskBudgetState.AddOrderUncertainty()
	engine.platformState.RecordException("order_uncertain", "critical", fmt.Sprintf("symbol=%s action=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-")))
	currentCount := engine.riskBudgetState.OrderUncertaintyCount
	recoveryActions := engine.riskBudgetState.RecoveryActionsThisCycle
	engine.lastUncertainOrder = strings.TrimSpace(fmt.Sprintf("%s | %s | %s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-")))
	engine.runtimeReporter.EmitLocked(EngineStage(""))
	engine.platformRiskMu.Unlock()
	engine.emitRiskEvent(newRiskEvent("action", RiskCategoryOrderUncertain, RiskSeverityCritical, "order_uncertain", symbol, action, int(atomic.LoadInt32(&engine.executionIncidents)), engine.cfg.RiskConfig.MaxExecutionIncidents, false, detail).WithEventType(EventTypeOrderUncertain))
	if recoveryActions > 0 {
		if err := s.TripPlatformFuse("order_uncertain", "critical", fmt.Sprintf("回补后再次出现不确定成交，symbol=%s action=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-"))); err != nil {
			engine.logger.Printf("平台级风控已触发: %v", err)
		}
		return
	}
	if engine.cfg.RiskConfig.MaxOrderUncertaintyPerCycle > 0 && currentCount >= engine.cfg.RiskConfig.MaxOrderUncertaintyPerCycle {
		if err := s.TripPlatformFuse("order_uncertain", "critical", fmt.Sprintf("单轮不确定成交次数过多，symbol=%s action=%s count=%d detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), currentCount, nonEmpty(detail, "-"))); err != nil {
			engine.logger.Printf("平台级风控已触发: %v", err)
		}
	}
}
