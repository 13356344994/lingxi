package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEmergencyStopFile = ".halt-live"
	liveDailyRiskStateName   = "live_daily_risk_state.json"
)

type persistedDailyRiskState struct {
	TradingDay       string  `json:"trading_day"`
	RealizedPnL      float64 `json:"realized_pnl"`
	OrderCount       int     `json:"order_count"`
	OpenNotionalUSDT float64 `json:"open_notional_usdt"`
	UpdatedAt        string  `json:"updated_at"`
}

func effectiveEmergencyStopFile(cfg EngineConfig) string {
	path := strings.TrimSpace(cfg.NetworkConfig.EmergencyStopFile)
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if path == "" {
		if recordDir == "" {
			return defaultEmergencyStopFile
		}
		return filepath.Join(recordDir, defaultEmergencyStopFile)
	}
	if filepath.IsAbs(path) || recordDir == "" {
		return path
	}
	return filepath.Join(recordDir, path)
}

func ensureEmergencyStopClear(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return fmt.Errorf("检测到 emergency stop 文件，实盘启动已阻断: %s", path)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("检查 emergency stop 文件失败: %w", err)
}

func (e *FundingRateSniperEngine) loadDailyRiskState() error {
	if e == nil {
		return nil
	}
	if e.logger == nil {
		e.logger = log.Default()
	}
	day := tradingDayUTC(time.Now().UTC())
	realizedPnL := e.seedDailyRealizedPnL(day)
	state := persistedDailyRiskState{}
	if strings.TrimSpace(e.dailyRiskStatePath) != "" {
		if data, readErr := os.ReadFile(e.dailyRiskStatePath); readErr == nil && len(data) > 0 {
			var loaded persistedDailyRiskState
			if err := json.Unmarshal(data, &loaded); err == nil {
				if strings.TrimSpace(loaded.TradingDay) == day {
					state = loaded
					if state.RealizedPnL == 0 && realizedPnL != 0 {
						state.RealizedPnL = realizedPnL
					}
				}
			}
		} else if readErr != nil && !os.IsNotExist(readErr) {
			return fmt.Errorf("读取日级风控状态失败: %w", readErr)
		}
	}
	if strings.TrimSpace(state.TradingDay) == "" {
		state = persistedDailyRiskState{
			TradingDay:       day,
			RealizedPnL:      realizedPnL,
			OrderCount:       0,
			OpenNotionalUSDT: 0,
			UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		}
	}
	e.platformRiskMu.Lock()
	e.dailyRiskDate = day
	e.riskBudgetState.DailyRealizedPnL = state.RealizedPnL
	e.riskBudgetState.DailyOrderCount = maxIntValue(state.OrderCount, 0)
	e.riskBudgetState.DailyOpenNotionalUsed = maxFloat(state.OpenNotionalUSDT, 0)
	e.platformRiskMu.Unlock()
	return e.persistDailyRiskState()
}

func (e *FundingRateSniperEngine) persistDailyRiskState() error {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.queueDailyRiskStatePersistLocked()
}

func (e *FundingRateSniperEngine) dailyRiskStatePayloadLocked() persistedDailyRiskState {
	return persistedDailyRiskState{
		TradingDay:       e.dailyRiskDate,
		RealizedPnL:      e.riskBudgetState.DailyRealizedPnL,
		OrderCount:       e.riskBudgetState.DailyOrderCount,
		OpenNotionalUSDT: e.riskBudgetState.DailyOpenNotionalUsed,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
}

func writeDailyRiskState(path string, payload persistedDailyRiskState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建日级风控状态目录失败: %w", err)
	}
	return writeJSONFile(path, payload)
}

func (e *FundingRateSniperEngine) persistDailyRiskStateLocked() error {
	if strings.TrimSpace(e.dailyRiskStatePath) == "" {
		return nil
	}
	return writeDailyRiskState(e.dailyRiskStatePath, e.dailyRiskStatePayloadLocked())
}

func (e *FundingRateSniperEngine) queueDailyRiskStatePersistLocked() error {
	if strings.TrimSpace(e.dailyRiskStatePath) == "" {
		return nil
	}
	payload := e.dailyRiskStatePayloadLocked()
	if e.dailyRiskPersistCh == nil {
		return writeDailyRiskState(e.dailyRiskStatePath, payload)
	}
	select {
	case e.dailyRiskPersistCh <- payload:
		return nil
	default:
		select {
		case <-e.dailyRiskPersistCh:
		default:
		}
		select {
		case e.dailyRiskPersistCh <- payload:
			return nil
		default:
			return writeDailyRiskState(e.dailyRiskStatePath, payload)
		}
	}
}

func (e *FundingRateSniperEngine) startDailyRiskStateWriter(ctx context.Context) {
	if e == nil || e.dailyRiskPersistCh == nil {
		return
	}
	e.dailyRiskPersistWorkerOnce.Do(func() {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case payload := <-e.dailyRiskPersistCh:
					if err := writeDailyRiskState(e.dailyRiskStatePath, payload); err != nil && e.logger != nil {
						e.logger.Printf("异步持久化日级风控状态失败: %v", err)
					}
				}
			}
		}()
	})
}

func (e *FundingRateSniperEngine) seedDailyRealizedPnL(day string) float64 {
	if e == nil || e.reportStore == nil {
		return 0
	}
	records, err := e.reportStore.ListSettlementRecords(0)
	if err != nil {
		return 0
	}
	total := 0.0
	for _, record := range records {
		closeTime := strings.TrimSpace(record.CloseExecutedAt)
		if closeTime == "" {
			closeTime = strings.TrimSpace(record.RecordedAt)
		}
		if closeTime == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, closeTime)
		if err != nil || tradingDayUTC(parsed.UTC()) != day {
			continue
		}
		total += parseFloat(record.NetPnL)
	}
	return total
}

func tradingDayUTC(at time.Time) string {
	return at.UTC().Format("2006-01-02")
}

func (e *FundingRateSniperEngine) ensureDailyRiskDateLocked() {
	day := tradingDayUTC(time.Now().UTC())
	if e.dailyRiskDate == day {
		return
	}
	e.dailyRiskDate = day
	e.riskBudgetState.DailyRealizedPnL = e.seedDailyRealizedPnL(day)
	e.riskBudgetState.DailyOrderCount = 0
	e.riskBudgetState.DailyOpenNotionalUsed = 0
	e.riskBudgetState.DailyLimitTriggered = false
	e.riskBudgetState.DailyLimitReason = ""
}

func (e *FundingRateSniperEngine) checkLiveSafetyGuards(phase string) error {
	if err := e.checkEmergencyStop(phase); err != nil {
		return err
	}
	return e.checkDailyRiskLimits(phase)
}

func (e *FundingRateSniperEngine) checkEmergencyStop(phase string) error {
	path := strings.TrimSpace(e.emergencyStopFile)
	if path == "" {
		return nil
	}
	if err := ensureEmergencyStopClear(path); err == nil {
		e.platformRiskMu.Lock()
		e.emergencyStopTriggered = false
		e.emergencyStopReason = ""
		e.platformRiskMu.Unlock()
		return nil
	} else if !strings.Contains(err.Error(), "实盘启动已阻断") {
		return err
	}

	reason := fmt.Sprintf("phase=%s emergency stop 文件存在: %s", nonEmpty(strings.TrimSpace(phase), "unknown"), path)
	e.platformRiskMu.Lock()
	e.emergencyStopTriggered = true
	e.emergencyStopReason = reason
	e.platformState.RecordException("kill_switch", RiskSeverityCritical, e.emergencyStopReason)
	e.runtimeReporter.EmitLockedWithReason(StageStopped, "检测到 emergency stop 文件，立即停止实盘")
	e.platformRiskMu.Unlock()
	e.emitRiskEvent(newRiskEvent("incident", RiskCategorySafety, RiskSeverityCritical, nonEmpty(strings.TrimSpace(phase), "kill_switch"), "-", "emergency_stop", int(e.executionIncidents), e.cfg.RiskConfig.MaxExecutionIncidents, true, reason))
	return fatalEngineError{message: reason}
}

func (e *FundingRateSniperEngine) checkDailyRiskLimits(phase string) error {
	e.platformRiskMu.Lock()
	e.ensureDailyRiskDateLocked()
	reason := e.currentDailyLimitReasonLocked()
	if reason == "" {
		e.riskBudgetState.DailyLimitTriggered = false
		e.riskBudgetState.DailyLimitReason = ""
		e.platformRiskMu.Unlock()
		return nil
	}
	reason = fmt.Sprintf("phase=%s %s", nonEmpty(strings.TrimSpace(phase), "unknown"), reason)
	e.riskBudgetState.TripDailyLimit(reason)
	e.platformState.RecordException("daily_limit", RiskSeverityCritical, reason)
	e.runtimeReporter.EmitLockedWithReason(StageStopped, "触发日级风控限制，立即停止实盘")
	e.platformRiskMu.Unlock()
	e.emitRiskEvent(newRiskEvent("incident", RiskCategorySafety, RiskSeverityCritical, nonEmpty(strings.TrimSpace(phase), "daily_limit"), "-", "daily_limit", int(e.executionIncidents), e.cfg.RiskConfig.MaxExecutionIncidents, true, reason))
	return fatalEngineError{message: reason}
}

func (e *FundingRateSniperEngine) currentDailyLimitReasonLocked() string {
	lossLimit := e.cfg.RiskConfig.MaxDailyLossUSDT
	if lossLimit > 0 && e.riskBudgetState.DailyRealizedPnL <= -lossLimit {
		return fmt.Sprintf("当日累计亏损 %.6f USDT 已超过阈值 %.6f USDT", -e.riskBudgetState.DailyRealizedPnL, lossLimit)
	}
	orderLimit := e.cfg.RiskConfig.MaxDailyOrderCount
	if orderLimit > 0 && e.riskBudgetState.DailyOrderCount >= orderLimit {
		return fmt.Sprintf("当日累计下单次数 %d 已达到阈值 %d", e.riskBudgetState.DailyOrderCount, orderLimit)
	}
	notionalLimit := e.cfg.RiskConfig.MaxDailyOpenNotionalUSDT
	if notionalLimit > 0 && e.riskBudgetState.DailyOpenNotionalUsed >= notionalLimit {
		return fmt.Sprintf("当日累计开仓名义 %.6f USDT 已达到阈值 %.6f USDT", e.riskBudgetState.DailyOpenNotionalUsed, notionalLimit)
	}
	return ""
}

func (e *FundingRateSniperEngine) remainingDailyLossBudgetLocked() float64 {
	limit := e.cfg.RiskConfig.MaxDailyLossUSDT
	if limit <= 0 {
		return -1
	}
	usedLoss := maxFloat(-e.riskBudgetState.DailyRealizedPnL, 0)
	return maxFloat(limit-usedLoss, 0)
}

func (e *FundingRateSniperEngine) remainingDailyOrderBudgetLocked() int {
	return remainingRiskBudget(e.cfg.RiskConfig.MaxDailyOrderCount, e.riskBudgetState.DailyOrderCount)
}

func (e *FundingRateSniperEngine) remainingDailyNotionalBudgetLocked() float64 {
	limit := e.cfg.RiskConfig.MaxDailyOpenNotionalUSDT
	if limit <= 0 {
		return -1
	}
	return maxFloat(limit-e.riskBudgetState.DailyOpenNotionalUsed, 0)
}

func (e *FundingRateSniperEngine) noteDailySettlement(netPnL float64) error {
	e.platformRiskMu.Lock()
	e.ensureDailyRiskDateLocked()
	e.riskBudgetState.DailyRealizedPnL += netPnL
	if err := e.queueDailyRiskStatePersistLocked(); err != nil {
		e.platformRiskMu.Unlock()
		return err
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	return e.checkDailyRiskLimits("settlement")
}

func (e *FundingRateSniperEngine) noteOrderPlaced(ctx context.Context, action string, detail string, count int, enforceLimit bool) error {
	// C3 策略：减仓型恢复/平残腿(exposure manager / panic flatten)允许在熔断与急停期间继续，
	// 以尽快收敛敞口；它们绝不会开新仓——开仓的硬保证由下单层的 reduceOnly 守卫负责。
	// 因此恢复请求跳过急停检查与日级预算阻断（仍计入下单计数用于审计），其余路径维持原有硬约束。
	recovery := isExposureManagerRequest(ctx)
	if recovery {
		enforceLimit = false
	} else if err := e.checkEmergencyStop("order_precheck"); err != nil {
		return err
	}
	e.platformRiskMu.Lock()
	e.ensureDailyRiskDateLocked()
	if enforceLimit && e.cfg.RiskConfig.MaxDailyOrderCount > 0 && e.riskBudgetState.DailyOrderCount+count > e.cfg.RiskConfig.MaxDailyOrderCount {
		e.riskBudgetState.TripDailyLimit(fmt.Sprintf("当日累计下单次数即将超过阈值: current=%d add=%d limit=%d", e.riskBudgetState.DailyOrderCount, count, e.cfg.RiskConfig.MaxDailyOrderCount))
		e.platformState.RecordException("daily_limit", RiskSeverityCritical, e.riskBudgetState.DailyLimitReason)
		e.runtimeReporter.EmitLockedWithReason(StageStopped, "触发日级下单次数限制，阻止继续扩仓")
		e.platformRiskMu.Unlock()
		e.emitRiskEvent(newRiskEvent("incident", RiskCategorySafety, RiskSeverityCritical, "order_precheck", "-", "daily_order_limit", int(e.executionIncidents), e.cfg.RiskConfig.MaxExecutionIncidents, true, e.riskBudgetState.DailyLimitReason))
		return fatalEngineError{message: e.riskBudgetState.DailyLimitReason}
	}
	e.riskBudgetState.DailyOrderCount += maxIntValue(count, 0)
	e.lastOrderAt = time.Now().UTC()
	e.lastOrderAction = strings.TrimSpace(nonEmpty(action, detail))
	if err := e.queueDailyRiskStatePersistLocked(); err != nil {
		e.platformRiskMu.Unlock()
		return err
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	return nil
}

func (e *FundingRateSniperEngine) setCycleOpenNotionalUsed(value float64) error {
	if err := e.checkEmergencyStop("capacity_precheck"); err != nil {
		return err
	}
	e.platformRiskMu.Lock()
	e.ensureDailyRiskDateLocked()
	value = maxFloat(value, 0)
	delta := value - e.riskBudgetState.CycleOpenNotionalUsed
	if delta > 0 && e.cfg.RiskConfig.MaxDailyOpenNotionalUSDT > 0 && e.riskBudgetState.DailyOpenNotionalUsed+delta > e.cfg.RiskConfig.MaxDailyOpenNotionalUSDT {
		e.riskBudgetState.TripDailyLimit(fmt.Sprintf("当日累计开仓名义即将超过阈值: current=%s add=%s limit=%s", formatFloat(e.riskBudgetState.DailyOpenNotionalUsed), formatFloat(delta), formatFloat(e.cfg.RiskConfig.MaxDailyOpenNotionalUSDT)))
		e.platformState.RecordException("daily_limit", RiskSeverityCritical, e.riskBudgetState.DailyLimitReason)
		e.runtimeReporter.EmitLockedWithReason(StageStopped, "触发日级名义限制，阻止继续扩仓")
		e.platformRiskMu.Unlock()
		e.emitRiskEvent(newRiskEvent("incident", RiskCategorySafety, RiskSeverityCritical, "capacity_precheck", "-", "daily_notional_limit", int(e.executionIncidents), e.cfg.RiskConfig.MaxExecutionIncidents, true, e.riskBudgetState.DailyLimitReason))
		return fatalEngineError{message: e.riskBudgetState.DailyLimitReason}
	}
	e.riskBudgetState.CycleOpenNotionalUsed = value
	if delta > 0 {
		e.riskBudgetState.DailyOpenNotionalUsed += delta
	}
	if err := e.queueDailyRiskStatePersistLocked(); err != nil {
		e.platformRiskMu.Unlock()
		return err
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	return nil
}

func parseDailyRiskInt(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}
