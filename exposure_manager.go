package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type exposureRescueMode string

const (
	exposureRescueModeOpen      exposureRescueMode      = "open"
	exposureRescueModeClose     exposureRescueMode      = "close"
	liveExposureRescueStateName exposureRescueStateName = "live_exposure_rescue_tasks.json"
)

type exposureRescueStateName string

type exposureRescueTask struct {
	Mode                    exposureRescueMode
	Phase                   string
	Direction               fundingCarryDirection
	Symbol                  string
	Pair                    string
	SpotBaseAsset           string
	SpotBasePrecision       int
	SpotReferencePrice      float64
	InitialHedgeQty         float64
	SpotClientOrderID       string
	FuturesClientOrderID    string
	KnownSpotExecutedQty    float64
	KnownFuturesExecutedQty float64
}

type exposureManagerContextKey string

const exposureManagerRequestContextKey exposureManagerContextKey = "exposure-manager-request"

type ExposureManager struct {
	engine    *FundingRateSniperEngine
	logger    *log.Logger
	taskQueue chan exposureRescueTask
	startOnce sync.Once
	mu        sync.Mutex
	statePath string
	pending   map[string]exposureRescueTask
}

func newExposureManager(engine *FundingRateSniperEngine) *ExposureManager {
	statePath := ""
	if engine != nil {
		statePath = effectiveExposureRescueStatePath(engine.cfg)
	}
	return &ExposureManager{
		engine:    engine,
		logger:    engine.logger,
		taskQueue: make(chan exposureRescueTask, 32),
		statePath: statePath,
		pending:   make(map[string]exposureRescueTask),
	}
}

type persistedExposureRescueState struct {
	UpdatedAt string               `json:"updated_at"`
	Tasks     []exposureRescueTask `json:"tasks"`
}

func effectiveExposureRescueStatePath(cfg EngineConfig) string {
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if recordDir == "" {
		return string(liveExposureRescueStateName)
	}
	return filepath.Join(recordDir, string(liveExposureRescueStateName))
}

func loadPersistedExposureRescueState(path string) (persistedExposureRescueState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return persistedExposureRescueState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedExposureRescueState{}, nil
		}
		return persistedExposureRescueState{}, fmt.Errorf("读取 exposure rescue 台账失败: %w", err)
	}
	if len(data) == 0 {
		return persistedExposureRescueState{}, nil
	}
	state := persistedExposureRescueState{}
	if err := jsonUnmarshalNoNumberLoss(data, &state); err != nil {
		return persistedExposureRescueState{}, fmt.Errorf("解析 exposure rescue 台账失败: %w", err)
	}
	return state, nil
}

func writePersistedExposureRescueState(path string, state persistedExposureRescueState) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if len(state.Tasks) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 exposure rescue 台账失败: %w", err)
		}
		return nil
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return writeJSONFile(path, state)
}

func normalizeExposureRescueTask(task exposureRescueTask) exposureRescueTask {
	task.Mode = exposureRescueMode(strings.ToLower(strings.TrimSpace(string(task.Mode))))
	task.Phase = strings.TrimSpace(task.Phase)
	task.Direction = fundingCarryDirection(strings.ToLower(strings.TrimSpace(string(task.Direction))))
	task.Symbol = strings.ToUpper(strings.TrimSpace(task.Symbol))
	task.Pair = strings.ToUpper(strings.TrimSpace(task.Pair))
	task.SpotBaseAsset = strings.ToUpper(strings.TrimSpace(task.SpotBaseAsset))
	task.SpotClientOrderID = strings.TrimSpace(task.SpotClientOrderID)
	task.FuturesClientOrderID = strings.TrimSpace(task.FuturesClientOrderID)
	return task
}

func exposureRescueTaskKey(task exposureRescueTask) string {
	task = normalizeExposureRescueTask(task)
	return strings.Join([]string{task.Symbol, string(task.Mode), task.Phase, task.Pair}, "|")
}

func (m *ExposureManager) syncPersistedTasksLocked() error {
	if m == nil {
		return nil
	}
	tasks := make([]exposureRescueTask, 0, len(m.pending))
	for _, task := range m.pending {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return exposureRescueTaskKey(tasks[i]) < exposureRescueTaskKey(tasks[j])
	})
	return writePersistedExposureRescueState(m.statePath, persistedExposureRescueState{Tasks: tasks})
}

func (m *ExposureManager) persistPendingTask(task exposureRescueTask) error {
	if m == nil {
		return nil
	}
	task = normalizeExposureRescueTask(task)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		m.pending = make(map[string]exposureRescueTask)
	}
	m.pending[exposureRescueTaskKey(task)] = task
	return m.syncPersistedTasksLocked()
}

func (m *ExposureManager) removePendingTask(task exposureRescueTask) error {
	if m == nil {
		return nil
	}
	task = normalizeExposureRescueTask(task)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, exposureRescueTaskKey(task))
	return m.syncPersistedTasksLocked()
}

func (m *ExposureManager) restorePendingTasks() error {
	if m == nil {
		return nil
	}
	state, err := loadPersistedExposureRescueState(m.statePath)
	if err != nil {
		return err
	}
	if len(state.Tasks) == 0 {
		return nil
	}
	m.mu.Lock()
	if m.pending == nil {
		m.pending = make(map[string]exposureRescueTask, len(state.Tasks))
	}
	for _, task := range state.Tasks {
		normalized := normalizeExposureRescueTask(task)
		m.pending[exposureRescueTaskKey(normalized)] = normalized
	}
	tasks := make([]exposureRescueTask, 0, len(m.pending))
	for _, task := range m.pending {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return exposureRescueTaskKey(tasks[i]) < exposureRescueTaskKey(tasks[j])
	})
	m.mu.Unlock()

	for _, task := range tasks {
		if m.engine != nil && strings.TrimSpace(task.Symbol) != "" {
			m.engine.lockSymbolForExposure(task)
			m.engine.setSymbolCooldown(task.Symbol, "exposure_rescue_recovered", "critical", fmt.Sprintf("phase=%s mode=%s", task.Phase, task.Mode))
			m.engine.recordExecutionAction(task.Phase, task.Symbol, "exposure_rescue_recovered", fmt.Sprintf("mode=%s pair=%s", task.Mode, task.Pair))
		}
		m.taskQueue <- task
		if m.logger != nil {
			m.logger.Printf("ExposureManager 恢复待处理任务，phase=%s mode=%s symbol=%s pair=%s", task.Phase, task.Mode, task.Symbol, task.Pair)
		}
	}
	return nil
}

func withExposureManagerRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, exposureManagerRequestContextKey, true)
}

func isExposureManagerRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(exposureManagerRequestContextKey).(bool)
	return value
}

// guardRecoveryFuturesReduceOnly 是 C3 策略的硬保证：恢复/平残腿通道(exposure manager / panic flatten)
// 拥有绕过熔断与急停的特权，但物理上只允许减仓。任何处于恢复上下文的非 reduceOnly 合约下单都被拒绝，
// 确保该特权通道永远无法被用来开新仓或加仓。
func guardRecoveryFuturesReduceOnly(ctx context.Context, symbol string, descriptor string, reduceOnly bool) error {
	if isExposureManagerRequest(ctx) && !reduceOnly {
		return fmt.Errorf("恢复/平残腿通道禁止开新仓: symbol=%s %s 必须 reduceOnly", symbol, descriptor)
	}
	return nil
}

func exposureRescueTimeout(cfg EngineConfig) time.Duration {
	timeout := unknownOrderReconcileTimeout(cfg) * 4
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	return timeout
}

func (m *ExposureManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		if err := m.restorePendingTasks(); err != nil && m.logger != nil {
			m.logger.Printf("ExposureManager 恢复待处理任务失败: %v", err)
		}
		go m.run(ctx)
	})
}

func (m *ExposureManager) Submit(ctx context.Context, task exposureRescueTask) error {
	if m == nil {
		return fmt.Errorf("exposure manager 未初始化")
	}
	task = normalizeExposureRescueTask(task)
	if err := m.persistPendingTask(task); err != nil {
		return err
	}
	select {
	case m.taskQueue <- task:
		if m.logger != nil {
			m.logger.Printf("ExposureManager 任务已入队，phase=%s mode=%s symbol=%s pair=%s", task.Phase, task.Mode, task.Symbol, task.Pair)
		}
		return nil
	default:
		_ = m.removePendingTask(task)
		return fmt.Errorf("exposure manager 队列已满: symbol=%s phase=%s", task.Symbol, task.Phase)
	case <-ctx.Done():
		_ = m.removePendingTask(task)
		return ctx.Err()
	}
}

func (m *ExposureManager) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-m.taskQueue:
			taskCtx, cancel := context.WithTimeout(withExposureManagerRequest(ctx), exposureRescueTimeout(m.engine.cfg))
			err := m.handleTask(taskCtx, task)
			cancel()
			m.reportTaskResult(task, err)
		}
	}
}

func (m *ExposureManager) reportTaskResult(task exposureRescueTask, err error) {
	if m == nil || m.engine == nil {
		return
	}
	m.engine.releaseExposureLock(task.Symbol)
	if err == nil {
		if removeErr := m.removePendingTask(task); removeErr != nil && m.logger != nil {
			m.logger.Printf("ExposureManager 清理已完成任务失败，symbol=%s phase=%s err=%v", task.Symbol, task.Phase, removeErr)
		}
		m.engine.recordExecutionAction(task.Phase, task.Symbol, "exposure_rescue_completed", fmt.Sprintf("mode=%s pair=%s", task.Mode, task.Pair))
		return
	}
	if m.logger != nil {
		m.logger.Printf("ExposureManager 处理失败，phase=%s mode=%s symbol=%s pair=%s err=%v", task.Phase, task.Mode, task.Symbol, task.Pair, err)
	}
	if incidentErr := m.engine.recordExecutionIncident(task.Phase, task.Symbol, fmt.Sprintf("ExposureManager 处理失败: %v", err)); incidentErr != nil && m.logger != nil {
		m.logger.Printf("ExposureManager 记录执行事故失败: %v", incidentErr)
	}
	m.engine.emitRiskEvent(newRiskEvent("incident", RiskCategoryPlatform, RiskSeverityCritical, task.Phase, task.Symbol, "exposure_rescue_pending", int(m.engine.executionIncidents), m.engine.cfg.RiskConfig.MaxExecutionIncidents, false, fmt.Sprintf("任务已持久化等待恢复: %v", err)))
}

func (m *ExposureManager) handleTask(ctx context.Context, task exposureRescueTask) error {
	if m == nil || m.engine == nil {
		return fmt.Errorf("exposure manager 不可用")
	}
	engine := m.engine
	if err := engine.noteRecoveryAction(task.Phase, task.Symbol, "exposure_rescue_start", fmt.Sprintf("mode=%s pair=%s", task.Mode, task.Pair)); err != nil {
		return err
	}
	if m.logger != nil {
		m.logger.Printf("ExposureManager 接管敞口，phase=%s mode=%s symbol=%s pair=%s", task.Phase, task.Mode, task.Symbol, task.Pair)
	}

	spotExecutedQty := maxFloat(task.KnownSpotExecutedQty, 0)
	if strings.TrimSpace(task.SpotClientOrderID) != "" {
		spotOrder, err := engine.reconcileSpotOrderTerminalState(ctx, task.Pair, task.SpotClientOrderID)
		if err != nil && !isUnknownOrderStateError(err) {
			return fmt.Errorf("ExposureManager 现货对账失败: %w", err)
		}
		if err == nil {
			spotExecutedQty = maxFloat(spotOrder.ExecutedBaseQty(task.SpotBaseAsset), 0)
		}
	}

	futuresExecutedQty := maxFloat(task.KnownFuturesExecutedQty, 0)
	if strings.TrimSpace(task.FuturesClientOrderID) != "" {
		futuresOrder, err := engine.reconcileFuturesOrderTerminalState(ctx, task.Symbol, task.FuturesClientOrderID)
		if err != nil && !isUnknownOrderStateError(err) {
			return fmt.Errorf("ExposureManager 合约对账失败: %w", err)
		}
		if err == nil {
			futuresExecutedQty = maxFloat(futuresOrder.ExecutedQtyValue(), 0)
		}
	}

	switch task.Mode {
	case exposureRescueModeOpen:
		return m.flattenOpenExposure(ctx, task, spotExecutedQty, futuresExecutedQty)
	case exposureRescueModeClose:
		return m.flattenCloseExposure(ctx, task, spotExecutedQty, futuresExecutedQty)
	default:
		return fmt.Errorf("未知 exposure rescue mode: %s", task.Mode)
	}
}

func (m *ExposureManager) flattenOpenExposure(ctx context.Context, task exposureRescueTask, spotExecutedQty float64, futuresExecutedQty float64) error {
	spotFallback := maxFloat(spotExecutedQty, 0)
	if resolvedFundingCarryDirection(task.Direction, 0) == fundingCarryDirectionNegative {
		spotFallback = -spotFallback
	}
	return m.dynamicallyFlattenExposure(ctx, task, spotFallback, task.Direction.futuresPositionSign()*maxFloat(futuresExecutedQty, 0))
}

func (m *ExposureManager) flattenCloseExposure(ctx context.Context, task exposureRescueTask, soldSpotQty float64, futuresCloseQty float64) error {
	direction := resolvedFundingCarryDirection(task.Direction, 0)
	remainingFuturesQty := maxFloat(task.InitialHedgeQty-futuresCloseQty, 0)
	remainingSpotQty := maxFloat(task.InitialHedgeQty-soldSpotQty, 0)
	spotFallback := remainingSpotQty
	if direction == fundingCarryDirectionNegative {
		spotFallback = -remainingSpotQty
	}
	return m.dynamicallyFlattenExposure(ctx, task, spotFallback, direction.futuresPositionSign()*remainingFuturesQty)
}

func (m *ExposureManager) dynamicallyFlattenExposure(ctx context.Context, task exposureRescueTask, fallbackSpotPosition float64, fallbackFuturesPosition float64) error {
	engine := m.engine
	attempt := 0
	for {
		attempt++
		spotPositionAmt, futuresPositionAmt, err := m.currentExposureState(ctx, task, fallbackSpotPosition, fallbackFuturesPosition)
		if err != nil {
			return err
		}
		fallbackSpotPosition = spotPositionAmt
		fallbackFuturesPosition = futuresPositionAmt
		if math.Abs(futuresPositionAmt) <= executionQtyTolerance && math.Abs(spotPositionAmt) <= executionQtyTolerance {
			return nil
		}
		if math.Abs(futuresPositionAmt) > executionQtyTolerance {
			futuresSide := "BUY"
			action := "exposure_rescue_close_futures"
			closeQty := math.Abs(futuresPositionAmt)
			if futuresPositionAmt > 0 {
				futuresSide = "SELL"
				action = "exposure_rescue_close_long_futures"
			}
			if futuresSide == "BUY" {
				if _, err := engine.placeFuturesChasingClose(ctx, task.Symbol, closeQty, "expfclose"); err != nil {
					return fmt.Errorf("ExposureManager 平掉残余合约仓位失败: %w", err)
				}
			} else {
				if _, err := engine.placeSingleFuturesMarketOrderIdempotent(ctx, task.Symbol, futuresSide, closeQty, true, newClientOrderID("expfclose", task.Symbol)); err != nil {
					return fmt.Errorf("ExposureManager 平掉残余合约多头失败: %w", err)
				}
			}
			if err := engine.noteRecoveryAction(task.Phase, task.Symbol, action, fmt.Sprintf("attempt=%d qty=%.8f", attempt, closeQty)); err != nil {
				return err
			}
		}
		if math.Abs(spotPositionAmt) > executionQtyTolerance {
			spotSide := "SELL"
			action := "exposure_rescue_sell_spot"
			spotQty := math.Abs(spotPositionAmt)
			if spotPositionAmt < 0 {
				spotSide = "BUY"
				action = "exposure_rescue_buy_spot"
			}
			if err := m.flattenSpotResidual(ctx, task, spotQty, spotSide, action); err != nil {
				return err
			}
		}
		if !sleepWithContext(ctx, executionChaseWaitDuration(150*time.Millisecond)) {
			break
		}
	}
	spotPositionAmt, futuresPositionAmt, _ := m.currentExposureState(context.Background(), task, fallbackSpotPosition, fallbackFuturesPosition)
	if math.Abs(futuresPositionAmt) <= executionQtyTolerance && math.Abs(spotPositionAmt) <= executionQtyTolerance {
		return nil
	}
	return fmt.Errorf("ExposureManager 在超时前未能完全收敛: spotPositionAmt=%.8f futuresPositionAmt=%.8f", spotPositionAmt, futuresPositionAmt)
}

func (m *ExposureManager) currentExposureState(ctx context.Context, task exposureRescueTask, fallbackSpotPosition float64, fallbackFuturesPosition float64) (float64, float64, error) {
	engine := m.engine
	direction := resolvedFundingCarryDirection(task.Direction, 0)
	spotPositionAmt := fallbackSpotPosition
	if strings.TrimSpace(task.SpotBaseAsset) != "" {
		if direction == fundingCarryDirectionNegative {
			if marginState, err := engine.fetchCrossMarginAccountState(ctx); err == nil {
				asset := marginState.Assets[task.SpotBaseAsset]
				if math.Abs(asset.NetAsset) > executionQtyTolerance {
					spotPositionAmt = asset.NetAsset
				}
			} else if m.logger != nil {
				m.logger.Printf("ExposureManager 读取全仓账户失败，沿用回退现货方向仓位: %v", err)
			}
		} else if balances, _, err := engine.fetchSpotAccountBalances(ctx); err == nil {
			spotPositionAmt = maxFloat(balances[task.SpotBaseAsset], 0)
		} else if m.logger != nil {
			m.logger.Printf("ExposureManager 读取现货余额失败，沿用回退数量: %v", err)
		}
	}
	futuresPositionAmt := fallbackFuturesPosition
	if positionsWallet, _, positions, err := engine.fetchFuturesAccountState(ctx); err == nil {
		_ = positionsWallet
		futuresPositionAmt = positions[task.Symbol]
	} else if m.logger != nil {
		m.logger.Printf("ExposureManager 读取合约仓位失败，沿用回退仓位: %v", err)
	}
	return spotPositionAmt, futuresPositionAmt, nil
}

func (m *ExposureManager) flattenSpotResidual(ctx context.Context, task exposureRescueTask, quantity float64, side string, action string) error {
	engine := m.engine
	if quantity <= executionQtyTolerance {
		return nil
	}
	spotRule := engine.spotRules[task.Pair]
	referencePrice := maxFloat(task.SpotReferencePrice, 0)
	if referencePrice <= 0 {
		referencePrice = maxFloat(engine.futuresReferencePrice(task.Symbol), 0)
	}
	normalizedQty := normalizeQuantity(quantity, spotRule, task.SpotBasePrecision)
	if normalizedQty <= executionQtyTolerance {
		return nil
	}
	if !isQuantityTradable(normalizedQty, referencePrice, spotRule) {
		notional := normalizedQty * maxFloat(referencePrice, 0)
		if err := engine.noteDustResidual(task.Phase, task.Symbol, task.Pair, task.SpotBaseAsset, normalizedQty, notional, "exposure_residual_untradable"); err != nil && m.logger != nil {
			m.logger.Printf("ExposureManager 记录灰尘残差失败: %v", err)
		}
		if notional >= engine.cfg.RiskConfig.ResidualSpotAlertUSDT {
			return fmt.Errorf("ExposureManager 现货残腿不可交易: pair=%s qty=%.8f notional=%.6f", task.Pair, normalizedQty, notional)
		}
		if m.logger != nil {
			m.logger.Printf("ExposureManager 遇到微小现货残余，pair=%s qty=%.8f", task.Pair, normalizedQty)
		}
		return nil
	}
	chaseOptions := buildAnchoredSpotChaseOptions(side, referencePrice, spotRule.PriceTick)
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "SELL":
		if _, err := engine.placeDirectionalSpotChasingOrderWithOptions(ctx, task.Pair, "SELL", normalizedQty, "exp_sell", chaseOptions, resolvedFundingCarryDirection(task.Direction, 0).spotVenue()); err != nil {
			return fmt.Errorf("ExposureManager 卖出现货残腿失败: %w", err)
		}
	case "BUY":
		if _, err := engine.placeDirectionalSpotChasingOrderWithOptions(ctx, task.Pair, "BUY", normalizedQty, "exp_buy", chaseOptions, resolvedFundingCarryDirection(task.Direction, 0).spotVenue()); err != nil {
			return fmt.Errorf("ExposureManager 买回现货残腿失败: %w", err)
		}
	default:
		return fmt.Errorf("ExposureManager 不支持的现货残腿方向: %s", side)
	}
	return engine.noteRecoveryAction(task.Phase, task.Symbol, action, fmt.Sprintf("qty=%.8f side=%s", normalizedQty, side))
}

func (e *FundingRateSniperEngine) scheduleExposureRescue(ctx context.Context, task exposureRescueTask) error {
	if e == nil {
		return fmt.Errorf("exposure manager 未初始化")
	}
	if e.exposureManager == nil {
		e.exposureManager = newExposureManager(e)
		e.exposureManager.Start(context.Background())
	}
	if strings.TrimSpace(task.Symbol) != "" {
		e.setSymbolCooldown(task.Symbol, "exposure_rescue", "critical", fmt.Sprintf("phase=%s mode=%s", task.Phase, task.Mode))
	}
	e.lockSymbolForExposure(task)
	if err := e.exposureManager.Submit(ctx, task); err != nil {
		e.releaseExposureLock(task.Symbol)
		return err
	}
	return nil
}

func (e *FundingRateSniperEngine) futuresReferencePrice(symbol string) float64 {
	if e == nil || strings.TrimSpace(symbol) == "" {
		return 0
	}
	if e.futuresTickerCache != nil {
		if ticker, _, ok := e.futuresTickerCache.GetWithReceivedAt(symbol); ok {
			return math.Max(parseFloat(ticker.AskPrice), parseFloat(ticker.BidPrice))
		}
	}
	return 0
}
