package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEmergencyStopFile = ".halt-live"
	liveDailyRiskStateName   = "live_daily_risk_state.json"
)

type persistedDailyRiskState struct {
	SchemaVersion     int     `json:"schema_version,omitempty"`
	Checksum          string  `json:"checksum,omitempty"`
	TradingDay        string  `json:"trading_day"`
	RealizedPnL       float64 `json:"realized_pnl"`
	OrderCount        int     `json:"order_count"`
	OpenNotionalUSDT  float64 `json:"open_notional_usdt"`
	ConsecutiveLosses int     `json:"consecutive_losses"`
	UpdatedAt         string  `json:"updated_at"`
}

func (s *persistedDailyRiskState) getSchemaVersion() int  { return s.SchemaVersion }
func (s *persistedDailyRiskState) setSchemaVersion(v int) { s.SchemaVersion = v }
func (s *persistedDailyRiskState) getChecksum() string    { return s.Checksum }
func (s *persistedDailyRiskState) setChecksum(c string)   { s.Checksum = c }

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
					if guardErr := guardStateEnvelope("日级风控状态", &loaded); guardErr != nil {
						return guardErr
					}
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
	cumulativePnL, peakPnL := e.computeRealizedEquityWaterMark()
	e.platformRiskMu.Lock()
	e.dailyRiskDate = day
	e.riskBudgetState.DailyRealizedPnL = state.RealizedPnL
	e.riskBudgetState.DailyOrderCount = maxIntValue(state.OrderCount, 0)
	e.riskBudgetState.DailyOpenNotionalUsed = maxFloat(state.OpenNotionalUSDT, 0)
	e.riskBudgetState.ConsecutiveLossCycles = maxIntValue(state.ConsecutiveLosses, 0)
	e.riskBudgetState.RealizedPnLCumulativeUSDT = cumulativePnL
	e.riskBudgetState.RealizedPnLPeakUSDT = peakPnL
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
		TradingDay:        e.dailyRiskDate,
		RealizedPnL:       e.riskBudgetState.DailyRealizedPnL,
		OrderCount:        e.riskBudgetState.DailyOrderCount,
		OpenNotionalUSDT:  e.riskBudgetState.DailyOpenNotionalUsed,
		ConsecutiveLosses: e.riskBudgetState.ConsecutiveLossCycles,
		UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
}

func writeDailyRiskState(path string, payload persistedDailyRiskState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建日级风控状态目录失败: %w", err)
	}
	if err := stampStateEnvelope(&payload); err != nil {
		return fmt.Errorf("标记日级风控状态信封失败: %w", err)
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
		// 退化路径：没有配置异步 writer(单元测试 / 装配早期)。此时不存在抢 platformRiskMu 的并发协程，
		// 同步写盘没有锁竞争风险，反而保证调用返回时状态已落盘(测试依赖这一点)。
		return writeDailyRiskState(e.dailyRiskStatePath, payload)
	}
	// 关键不变量：本函数在持有 platformRiskMu(全局风控锁)的前提下被调用，因此【绝不能】在锁内做任何
	// 阻塞式磁盘 I/O——否则云盘几十毫秒抖动就会让所有抢这把锁的协程排队卡死。这里只做非阻塞入队，真正写盘
	// 交给 startDailyRiskStateWriter 的后台协程。daily 风控状态是「全量快照」而非增量，丢旧留新或偶尔跳过
	// 一次中间快照都不损坏一致性：后台 writer 始终持久化它收到的最新快照，下一次风控事件会重新入队最新全量状态。
	select {
	case e.dailyRiskPersistCh <- payload:
		return nil
	default:
		// channel 满：丢弃最旧的待写快照，给最新快照腾位。
		select {
		case <-e.dailyRiskPersistCh:
		default:
		}
		select {
		case e.dailyRiskPersistCh <- payload:
		default:
			// 极端背压下仍塞不进：直接跳过本次入队，绝不在锁内同步写盘。下一次风控事件会带着最新全量状态重新入队。
		}
		return nil
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

// computeRealizedEquityWaterMark 按时间顺序回放全部「已定稿(funding 实收已结算)」的周期净利，得出累计净利
// 曲线的当前值与历史高水位。它是纯读取（只读结算记录、不改引擎状态），因此可在持锁前调用，避免磁盘 IO 占锁。
// 仍在等待 funding 实收的周期净利尚未定稿，跳过以免把不完整净利计入高水位。
func (e *FundingRateSniperEngine) computeRealizedEquityWaterMark() (cumulative float64, peak float64) {
	if e == nil || e.reportStore == nil {
		return 0, 0
	}
	records, err := e.reportStore.ListSettlementRecords(0)
	if err != nil {
		return 0, 0
	}
	type stampedPnL struct {
		at  time.Time
		pnl float64
	}
	ordered := make([]stampedPnL, 0, len(records))
	for _, record := range records {
		if record.FundingIncomePending {
			continue
		}
		stamp := strings.TrimSpace(record.CloseExecutedAt)
		if stamp == "" {
			stamp = strings.TrimSpace(record.RecordedAt)
		}
		parsed, perr := time.Parse(time.RFC3339, stamp)
		if perr != nil {
			parsed = time.Time{}
		}
		ordered = append(ordered, stampedPnL{at: parsed, pnl: parseFloat(record.NetPnL)})
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].at.Before(ordered[j].at) })
	for _, item := range ordered {
		cumulative += item.pnl
		if cumulative > peak {
			peak = cumulative
		}
	}
	return cumulative, peak
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
	// 连续亏损计数与其余日级预算同口径，按 UTC 交易日清零：新的一天给策略一个重新开始的机会，
	// 与 MaxDailyLoss/MaxDailyOrderCount 的「日级」语义保持一致。
	e.riskBudgetState.ConsecutiveLossCycles = 0
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
	lossCycleLimit := e.cfg.RiskConfig.MaxConsecutiveLossCycles
	if lossCycleLimit > 0 && e.riskBudgetState.ConsecutiveLossCycles >= lossCycleLimit {
		return fmt.Sprintf("连续亏损周期数 %d 已达到阈值 %d（系统性失效保护，停止继续开仓待人工复核）", e.riskBudgetState.ConsecutiveLossCycles, lossCycleLimit)
	}
	drawdownLimit := e.cfg.RiskConfig.MaxAccountDrawdownUSDT
	if drawdownLimit > 0 {
		drawdown := e.riskBudgetState.RealizedPnLPeakUSDT - e.riskBudgetState.RealizedPnLCumulativeUSDT
		if drawdown >= drawdownLimit {
			return fmt.Sprintf("账户级回撤 %.6f USDT 已达到阈值 %.6f USDT（自高水位 %.6f 累计回撤，停止继续开仓待人工复核）", drawdown, drawdownLimit, e.riskBudgetState.RealizedPnLPeakUSDT)
		}
	}
	return ""
}

// noteCycleFinalSettlement 在「funding 实收结算完成、周期真实净利已确定」后推进两条账户级风控信号：
// (1) 连续亏损计数——真实净利为负则自增、为正则清零（~0 视作打平不变）；
// (2) 累计净利曲线与高水位——用于账户级回撤熔断。
// 随后立即用日级风控复核：连亏触线或自高水位回撤超阈值都会触发停机。它只会停机、绝不下单（fail-safe），
// 因此即便在异步结算 worker 中调用也安全：worker 只记录返回的 fatalEngineError，真正的拦截发生在主循环
// 下一次 checkLiveSafetyGuards。
func (e *FundingRateSniperEngine) noteCycleFinalSettlement(finalNetPnL float64) error {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	e.ensureDailyRiskDateLocked()
	switch {
	case finalNetPnL < -executionQtyTolerance:
		e.riskBudgetState.ConsecutiveLossCycles++
	case finalNetPnL > executionQtyTolerance:
		e.riskBudgetState.ConsecutiveLossCycles = 0
	}
	// 累计净利曲线增量推进 + 高水位刷新：与启动时由结算记录回放出的初值同口径，跨重启自洽。
	e.riskBudgetState.RealizedPnLCumulativeUSDT += finalNetPnL
	if e.riskBudgetState.RealizedPnLCumulativeUSDT > e.riskBudgetState.RealizedPnLPeakUSDT {
		e.riskBudgetState.RealizedPnLPeakUSDT = e.riskBudgetState.RealizedPnLCumulativeUSDT
	}
	if err := e.queueDailyRiskStatePersistLocked(); err != nil {
		e.platformRiskMu.Unlock()
		return err
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	return e.checkDailyRiskLimits("consecutive_loss")
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
