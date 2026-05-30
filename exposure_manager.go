package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

type exposureRescueMode string

const (
	exposureRescueModeOpen  exposureRescueMode = "open"
	exposureRescueModeClose exposureRescueMode = "close"
)

type exposureRescueTask struct {
	Mode                    exposureRescueMode
	Phase                   string
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
}

func newExposureManager(engine *FundingRateSniperEngine) *ExposureManager {
	return &ExposureManager{
		engine:    engine,
		logger:    engine.logger,
		taskQueue: make(chan exposureRescueTask, 32),
	}
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
		go m.run(ctx)
	})
}

func (m *ExposureManager) Submit(ctx context.Context, task exposureRescueTask) error {
	if m == nil {
		return fmt.Errorf("exposure manager 未初始化")
	}
	select {
	case m.taskQueue <- task:
		if m.logger != nil {
			m.logger.Printf("ExposureManager 任务已入队，phase=%s mode=%s symbol=%s pair=%s", task.Phase, task.Mode, task.Symbol, task.Pair)
		}
		return nil
	default:
		return fmt.Errorf("exposure manager 队列已满: symbol=%s phase=%s", task.Symbol, task.Phase)
	case <-ctx.Done():
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
		m.engine.recordExecutionAction(task.Phase, task.Symbol, "exposure_rescue_completed", fmt.Sprintf("mode=%s pair=%s", task.Mode, task.Pair))
		return
	}
	if m.logger != nil {
		m.logger.Printf("ExposureManager 处理失败，phase=%s mode=%s symbol=%s pair=%s err=%v", task.Phase, task.Mode, task.Symbol, task.Pair, err)
	}
	if incidentErr := m.engine.recordExecutionIncident(task.Phase, task.Symbol, fmt.Sprintf("ExposureManager 处理失败: %v", err)); incidentErr != nil && m.logger != nil {
		m.logger.Printf("ExposureManager 记录执行事故失败: %v", incidentErr)
	}
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
			spotExecutedQty = maxFloat(resolvedSpotOrderBaseQty(spotOrder, task.SpotBaseAsset), 0)
		}
	}

	futuresExecutedQty := maxFloat(task.KnownFuturesExecutedQty, 0)
	if strings.TrimSpace(task.FuturesClientOrderID) != "" {
		futuresOrder, err := engine.reconcileFuturesOrderTerminalState(ctx, task.Symbol, task.FuturesClientOrderID)
		if err != nil && !isUnknownOrderStateError(err) {
			return fmt.Errorf("ExposureManager 合约对账失败: %w", err)
		}
		if err == nil {
			futuresExecutedQty = maxFloat(parseFloat(futuresOrder.ExecutedQty), 0)
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
	engine := m.engine
	if futuresExecutedQty > executionQtyTolerance {
		if _, err := engine.placeFuturesChasingClose(ctx, task.Symbol, futuresExecutedQty, "expfclose"); err != nil {
			return fmt.Errorf("ExposureManager 平掉残余空头失败: %w", err)
		}
		if err := engine.noteRecoveryAction(task.Phase, task.Symbol, "exposure_rescue_close_futures", fmt.Sprintf("qty=%.8f", futuresExecutedQty)); err != nil {
			return err
		}
	}
	if err := m.flattenSpotResidual(ctx, task, spotExecutedQty, "SELL", "exposure_rescue_sell_spot"); err != nil {
		return err
	}
	return nil
}

func (m *ExposureManager) flattenCloseExposure(ctx context.Context, task exposureRescueTask, soldSpotQty float64, futuresCloseQty float64) error {
	engine := m.engine
	remainingFuturesShort := maxFloat(task.InitialHedgeQty-futuresCloseQty, 0)
	remainingSpotQty := maxFloat(task.InitialHedgeQty-soldSpotQty, 0)
	if remainingFuturesShort > executionQtyTolerance {
		if _, err := engine.placeFuturesChasingClose(ctx, task.Symbol, remainingFuturesShort, "expfclose"); err != nil {
			return fmt.Errorf("ExposureManager 收敛剩余合约空头失败: %w", err)
		}
		if err := engine.noteRecoveryAction(task.Phase, task.Symbol, "exposure_rescue_close_remaining_futures", fmt.Sprintf("qty=%.8f", remainingFuturesShort)); err != nil {
			return err
		}
	}
	if err := m.flattenSpotResidual(ctx, task, remainingSpotQty, "SELL", "exposure_rescue_close_remaining_spot"); err != nil {
		return err
	}
	return nil
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
		if _, err := engine.placeSpotChasingOrderWithOptions(ctx, task.Pair, "SELL", normalizedQty, "exp_sell", chaseOptions); err != nil {
			return fmt.Errorf("ExposureManager 卖出现货残腿失败: %w", err)
		}
	case "BUY":
		if _, err := engine.placeSpotChasingOrderWithOptions(ctx, task.Pair, "BUY", normalizedQty, "exp_buy", chaseOptions); err != nil {
			return fmt.Errorf("ExposureManager 买回现货残腿失败: %w", err)
		}
	default:
		return fmt.Errorf("ExposureManager 不支持的现货残腿方向: %s", side)
	}
	return engine.noteRecoveryAction(task.Phase, task.Symbol, action, fmt.Sprintf("qty=%.8f side=%s", normalizedQty, side))
}

func resolvedSpotOrderBaseQty(order SpotOrderResponse, baseAsset string) float64 {
	if len(order.Fills) > 0 {
		return resolveSpotNetBaseQuantity(order, baseAsset)
	}
	return maxFloat(parseFloat(order.ExecutedQty), 0)
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
