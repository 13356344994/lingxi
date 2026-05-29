package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	monitorEstimateRefreshInterval = 2 * time.Second
	executionQtyTolerance          = 1e-8
)

var (
	majorRiskTierSymbols = map[string]struct{}{
		"BTCUSDT": {}, "ETHUSDT": {}, "BNBUSDT": {}, "SOLUSDT": {}, "XRPUSDT": {},
	}
	mainstreamRiskTierSymbols = map[string]struct{}{
		"DOGEUSDT": {}, "ADAUSDT": {}, "LINKUSDT": {}, "LTCUSDT": {}, "AVAXUSDT": {},
		"DOTUSDT": {}, "TRXUSDT": {}, "BCHUSDT": {}, "APTUSDT": {}, "SUIUSDT": {},
	}
)

type symbolRiskProfile struct {
	Tier                       string
	PerSymbolQuoteMultiplier   float64
	MinEstimatedRateMultiplier float64
	MinNetProfitMultiplier     float64
	ImmediateInvalidateBump    float64
	MaxRecentVolumeShareMul    float64
	MaxOpenInterestShareMul    float64
	MaxVisibleDepthShareMul    float64
}

// FundingRateSniperEngine 是资费压哨狙击引擎的单进程 MVP 主控。
// 它负责模块二到模块五的完整调度链路。
type FundingRateSniperEngine struct {
	client                     *BinanceClient
	cfg                        EngineConfig
	logger                     *log.Logger
	runID                      string
	currentCycleID             string
	goldenSymbols              []string
	goldenSymbolSet            map[string]struct{}
	spotMeta                   map[string]SpotSymbol
	spotRules                  map[string]SymbolTradeRules
	futuresMeta                map[string]FuturesSymbol
	futuresRules               map[string]SymbolTradeRules
	spotTickerCache            *BookTickerCache
	futuresTickerCache         *BookTickerCache
	spotDepthCache             *DepthCache
	futuresDepthCache          *DepthCache
	spotOrderHub               *OrderEventHub
	futuresOrderHub            *OrderEventHub
	reportStore                *ReportStore
	serverTimeOffset           time.Duration
	executionIncidents         int32
	executionEventHook         func(ExecutionIncidentRecord)
	liveStatusHook             func(LiveRuntimeSnapshot)
	platformRiskMu             sync.Mutex
	platformFuseReason         string
	platformFuseTripped        bool
	platformCooldownUntil      time.Time
	consecutiveRequestErrors   int
	consecutivePlatformRejects int
	requestErrorsThisCycle     int
	platformRejectsThisCycle   int
	recoveryActionsThisCycle   int
	orderUncertaintyCount      int
	lastOrderAt                time.Time
	lastOrderAction            string
	lastRecoveryAt             time.Time
	lastRecoveryAction         string
	lastUncertainOrder         string
	lastExceptionCategory      string
	lastExceptionSeverity      string
	lastExceptionDetail        string
	emergencyStopFile          string
	emergencyStopTriggered     bool
	emergencyStopReason        string
	dailyRiskStatePath         string
	dailyRiskDate              string
	dailyRealizedPnL           float64
	dailyOrderCount            int
	dailyOpenNotionalUsed      float64
	dailyLimitTriggered        bool
	dailyLimitReason           string
	currentCooldownCategory    string
	currentCooldownSeverity    string
	currentMonitorTargets      []string
	currentPositions           []string
	cycleOpenNotionalUsed      float64
	currentStage               EngineStage
	currentStageReason         string
	currentStageTransitionedAt string
	symbolCooldowns            map[string]symbolCooldownState
	runtimeReporter            *RuntimeReporter
}

type symbolCooldownState struct {
	Category string
	Severity string
	Reason   string
	Until    time.Time
}

type fatalEngineError struct {
	message string
}

type platformFuseError struct {
	message string
}

func (e fatalEngineError) Error() string {
	return e.message
}

func (e platformFuseError) Error() string {
	return e.message
}

// FundingCandidate 表示模块二筛选出来的候选猎物。
type FundingCandidate struct {
	Symbol            string
	Pair              string
	RiskTier          string
	EstimatedRate     float64
	EstimatedRateText string
	LastFundingRate   float64
	FundingRateJump   float64
	BasisRate         float64
	ScanScoreRate     float64
	NextFundingTime   time.Time
	MarkPrice         float64
	SpotBaseAsset     string
	SpotBasePrecision int
	QuantityPrecision int
	OrderQuantity     float64
}

// SymbolTradeRules 表示一个交易对在下单前需要满足的最小交易规则。
type SymbolTradeRules struct {
	MinQty      float64
	StepSize    float64
	PriceTick   float64
	MinNotional float64
}

// MonitoredTarget 表示模块三进入死守监控阶段的目标。
type MonitoredTarget struct {
	TraceRecordMetadata
	RunID           string
	CycleID         string
	Candidate       FundingCandidate
	IsValid         int32
	LastSpread      float64
	LastNetProfit   float64
	LastSpotAsk     float64
	LastFuturesBid  float64
	LastSpotVWAP    float64
	LastFuturesVWAP float64
	InvalidReason   string
	BreachCount     int
	MonitorState    string
	LastObserveTime time.Time
	LastEstimateAt  time.Time
}

// PremiumIndexItem 对应币安 /fapi/v1/premiumIndex 响应。
// 模块二会结合 lastFundingRate、interestRate 和 mark/index 基差做更保守的费率预估。
type PremiumIndexItem struct {
	Symbol               string `json:"symbol"`
	MarkPrice            string `json:"markPrice"`
	IndexPrice           string `json:"indexPrice"`
	EstimatedSettlePrice string `json:"estimatedSettlePrice"`
	LastFundingRate      string `json:"lastFundingRate"`
	InterestRate         string `json:"interestRate"`
	NextFundingTime      int64  `json:"nextFundingTime"`
	Time                 int64  `json:"time"`
}

// BookTicker 表示最优买卖一盘口。
type BookTicker struct {
	Symbol      string `json:"symbol"`
	BidPrice    string `json:"bidPrice"`
	BidQuantity string `json:"bidQty"`
	AskPrice    string `json:"askPrice"`
	AskQuantity string `json:"askQty"`
}

// OrderBookDepth 表示币安 depth 接口返回的多档盘口。
type OrderBookDepth struct {
	LastUpdateID int64      `json:"lastUpdateId"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

type marketCapacitySnapshot struct {
	FuturesOpenInterestNotional float64
	RecentSpotQuoteVolume       float64
	RecentFuturesQuoteVolume    float64
	VisibleSpotAskNotional      float64
	VisibleFuturesBidNotional   float64
	AvailableMetrics            int
}

// FuturesBatchOrderRequest 是合约批量下单的请求结构。
type FuturesBatchOrderRequest struct {
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Type             string `json:"type"`
	Quantity         string `json:"quantity"`
	Price            string `json:"price,omitempty"`
	TimeInForce      string `json:"timeInForce,omitempty"`
	PriceProtect     string `json:"priceProtect,omitempty"`
	ReduceOnly       string `json:"reduceOnly,omitempty"`
	NewClientOrderID string `json:"newClientOrderId,omitempty"`
	NewOrderRespType string `json:"newOrderRespType,omitempty"`
}

// FuturesBatchOrderResult 表示合约批量下单回执。
type FuturesBatchOrderResult struct {
	Code            int64  `json:"code,omitempty"`
	Msg             string `json:"msg,omitempty"`
	OrderID         int64  `json:"orderId,omitempty"`
	Symbol          string `json:"symbol,omitempty"`
	Status          string `json:"status,omitempty"`
	ClientOrderID   string `json:"clientOrderId,omitempty"`
	ExecutedQty     string `json:"executedQty,omitempty"`
	OrigQty         string `json:"origQty,omitempty"`
	AvgPrice        string `json:"avgPrice,omitempty"`
	Side            string `json:"side,omitempty"`
	PositionSide    string `json:"positionSide,omitempty"`
	ReduceOnly      bool   `json:"reduceOnly,omitempty"`
	ClosePosition   bool   `json:"closePosition,omitempty"`
	UpdateTime      int64  `json:"updateTime,omitempty"`
	SelfTradeMode   string `json:"selfTradePreventionMode,omitempty"`
	GoodTillDate    int64  `json:"goodTillDate,omitempty"`
	WorkingType     string `json:"workingType,omitempty"`
	PriceProtect    bool   `json:"priceProtect,omitempty"`
	OriginalOrderTy string `json:"origType,omitempty"`
}

// SpotOrderResponse 表示现货市价单回执。
type SpotOrderResponse struct {
	Symbol                  string     `json:"symbol"`
	OrderID                 int64      `json:"orderId"`
	Status                  string     `json:"status"`
	ClientOrderID           string     `json:"clientOrderId"`
	ExecutedQty             string     `json:"executedQty"`
	CummulativeQuoteQty     string     `json:"cummulativeQuoteQty"`
	OrigQty                 string     `json:"origQty"`
	OrigQuoteOrderQty       string     `json:"origQuoteOrderQty"`
	TransactTime            int64      `json:"transactTime"`
	SelfTradePreventionMode string     `json:"selfTradePreventionMode"`
	Fills                   []SpotFill `json:"fills,omitempty"`
}

// SpotFill 表示现货订单的单笔成交明细。
type SpotFill struct {
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
}

// OpenHedge 表示已经完成现货-合约锁仓的一组持仓。
type OpenHedge struct {
	Target                  FundingCandidate
	FuturesOrder            FuturesBatchOrderResult
	SpotOrder               SpotOrderResponse
	SpotBaseQty             string
	HedgeQty                float64
	HedgeQtyText            string
	OpenSpotQuoteQty        float64
	OpenSpotFeeUSDT         float64
	OpenFuturesAvgPrice     float64
	OpenFuturesFeeUSDT      float64
	CloseFuturesExecutedQty float64
	CloseSpotExecutedQty    float64
	CloseSpotQuoteQty       float64
	CloseSpotFeeUSDT        float64
	CloseFuturesAvgPrice    float64
	CloseFuturesFeeUSDT     float64
	CloseFuturesRealizedPnL float64
}

// LastKnownSpotPrice 返回最近一次可用于名义价值校验的现货价格。
func (h OpenHedge) LastKnownSpotPrice() float64 {
	executedQty := parseFloat(h.SpotOrder.ExecutedQty)
	quoteQty := parseFloat(h.SpotOrder.CummulativeQuoteQty)
	if executedQty > 0 && quoteQty > 0 {
		return quoteQty / executedQty
	}
	if h.Target.MarkPrice > 0 {
		return h.Target.MarkPrice
	}
	return 0
}

// FundingIncomeRecord 表示资金费收入流水。
type FundingIncomeRecord struct {
	Symbol     string `json:"symbol"`
	IncomeType string `json:"incomeType"`
	Income     string `json:"income"`
	Time       int64  `json:"time"`
}

// CycleSettlement 表示一次完整狙击周期的落袋结果。
type CycleSettlement struct {
	TraceRecordMetadata
	RunID                 string
	CycleID               string
	Targets               int
	FundingIncome         float64
	ExpectedFundingIncome float64
	FundingIncomeDiff     float64
	TradingPnL            float64
	SpotTradingPnL        float64
	FuturesTradingPnL     float64
	TotalFees             float64
	NetPnL                float64
	SnapshotNetPnL        float64
	ResidualSpotValue     float64
	SpotUSDTDelta         float64
	FuturesUSDTDelta      float64
	CloseExecutedAt       time.Time
	FundingTimePoint      time.Time
	BeforeSnapshot        CycleAccountSnapshot
	AfterSnapshot         CycleAccountSnapshot
	HasAlert              bool
	Alerts                []string
}

// CycleAccountSnapshot 表示一个狙击周期前后的账户快照。
type CycleAccountSnapshot struct {
	CapturedAt             time.Time
	SpotUSDTFree           float64
	SpotAssetBalances      map[string]float64
	FuturesUSDTWallet      float64
	FuturesUSDTAvailable   float64
	FuturesPositionAmounts map[string]float64
}

// NewFundingRateSniperEngine 创建完整的资费压哨狙击引擎。
func NewFundingRateSniperEngine(secretPath string) (*FundingRateSniperEngine, error) {
	return NewFundingRateSniperEngineWithConfig(secretPath, DefaultEngineConfig())
}

// NewFundingRateSniperEngineWithConfig 使用指定配置创建完整的资费压哨狙击引擎。
func NewFundingRateSniperEngineWithConfig(secretPath string, cfg EngineConfig) (*FundingRateSniperEngine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret, err := LoadSecretConfig(secretPath)
	if err != nil {
		return nil, err
	}

	reportStore, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, err
	}
	client, err := NewAuthenticatedBinanceClient(secret, cfg.NetworkConfig.HTTPTimeout)
	if err != nil {
		return nil, err
	}

	engine := &FundingRateSniperEngine{
		client:             client,
		cfg:                cfg,
		logger:             log.New(os.Stdout, "[FundingRateSniper] ", log.LstdFlags|log.Lmicroseconds),
		goldenSymbolSet:    make(map[string]struct{}),
		spotMeta:           make(map[string]SpotSymbol),
		spotRules:          make(map[string]SymbolTradeRules),
		futuresMeta:        make(map[string]FuturesSymbol),
		futuresRules:       make(map[string]SymbolTradeRules),
		reportStore:        reportStore,
		emergencyStopFile:  effectiveEmergencyStopFile(cfg),
		dailyRiskStatePath: filepath.Join(cfg.NetworkConfig.RecordDir, liveDailyRiskStateName),
		currentStage:       StageReady,
		symbolCooldowns:    make(map[string]symbolCooldownState),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)
	return engine, nil
}

// Start 启动完整的小时级主循环。
func (e *FundingRateSniperEngine) Start(ctx context.Context) error {
	if err := e.initialize(ctx); err != nil {
		return err
	}
	e.startUserDataStreams(ctx)

	for {
		if err := e.checkLiveSafetyGuards("idle"); err != nil {
			return err
		}
		if err := e.syncServerTimeOffset(ctx); err != nil {
			e.logger.Printf("刷新服务器时间偏移失败，将沿用旧偏移继续调度: %v", err)
		}

		wakeAt := nextWakeTime(e.serverNow(), e.cfg.StrategyConfig.WakeMinute, e.cfg.StrategyConfig.WakeSecond)
		e.logger.Printf("系统静默中，等待下一次觉醒时间: %s", wakeAt.Format("2006-01-02 15:04:05"))
		if err := e.sleepUntilServerTime(ctx, wakeAt); err != nil {
			return err
		}

		if err := e.waitPlatformCooldown(ctx); err != nil {
			return err
		}
		if err := e.checkLiveSafetyGuards("before_cycle"); err != nil {
			return err
		}
		if err := e.runOneCycle(ctx, wakeAt); err != nil {
			var fatalErr fatalEngineError
			if errors.As(err, &fatalErr) {
				return err
			}
			var platformErr platformFuseError
			if errors.As(err, &platformErr) {
				e.logger.Printf("本轮触发平台异常熔断: %v", err)
				continue
			}
			e.logger.Printf("本轮狙击失败: %v", err)
		}
	}
}

// initialize 完成黄金币种列表和精度元数据的初始化。
func (e *FundingRateSniperEngine) initialize(ctx context.Context) error {
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return err
	}

	spotSymbols, futuresSymbols, err := e.client.fetchExchangeInfos(ctx)
	if err != nil {
		return err
	}

	e.goldenSymbols = intersectGoldenSymbols(spotSymbols, futuresSymbols)
	e.goldenSymbolSet = make(map[string]struct{}, len(e.goldenSymbols))
	e.spotMeta = make(map[string]SpotSymbol, len(spotSymbols))
	e.spotRules = make(map[string]SymbolTradeRules, len(spotSymbols))
	for _, symbol := range spotSymbols {
		e.spotMeta[symbol.Symbol] = symbol
		e.spotRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}
	for _, symbol := range e.goldenSymbols {
		e.goldenSymbolSet[symbol] = struct{}{}
	}

	e.futuresMeta = make(map[string]FuturesSymbol, len(futuresSymbols))
	e.futuresRules = make(map[string]SymbolTradeRules, len(futuresSymbols))
	for _, symbol := range futuresSymbols {
		e.futuresMeta[symbol.Symbol] = symbol
		e.futuresRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}

	if err := e.verifyTradingEnvironment(ctx); err != nil {
		return err
	}
	if err := e.loadDailyRiskState(); err != nil {
		return err
	}
	if err := e.checkLiveSafetyGuards("initialize"); err != nil {
		return err
	}

	e.logger.Printf("模块一已加载完成，黄金币种数量: %d", len(e.goldenSymbols))
	return nil
}

// runOneCycle 串起模块二到模块五。
func (e *FundingRateSniperEngine) runOneCycle(ctx context.Context, wakeAt time.Time) error {
	e.platformRiskMu.Lock()
	e.currentCycleID = newCycleID(e.runID)
	e.platformRiskMu.Unlock()
	e.resetCyclePlatformRiskCounters()
	e.runtimeReporter.EmitWithReason(StageScan, 0, "开始扫描候选标的")
	e.logger.Printf("模块二启动，觉醒时间: %s", wakeAt.Format("15:04:05"))
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return err
	}

	targets, err := e.scanTopFundingTargets(ctx)
	if err != nil {
		return err
	}
	targets = e.filterTargetsBySymbolCooldown(targets)
	if len(targets) == 0 {
		e.setCurrentMonitorTargets(nil)
		e.runtimeReporter.EmitWithReason(StageScanEmpty, 0, "扫描结束，没有满足条件的目标")
		e.logger.Println("模块二扫描结束，本轮没有满足条件的目标")
		return nil
	}

	e.setCurrentMonitorTargets(targets)
	e.runtimeReporter.EmitWithReason(StageMonitor, len(targets), fmt.Sprintf("进入监控阶段，目标数=%d", len(targets)))
	monitored := e.monitorTargets(ctx, targets)
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	validTargets := filterValidTargets(monitored)
	if len(validTargets) == 0 {
		e.setCurrentMonitorTargets(nil)
		e.runtimeReporter.EmitWithReason(StageMonitorInvalidated, 0, "监控结束，所有目标都已被风控作废")
		e.logger.Println("模块三结束，所有目标都已被风控作废")
		return nil
	}

	e.setCurrentMonitorTargets(validTargets)
	e.runtimeReporter.EmitWithReason(StageOpen, len(validTargets), fmt.Sprintf("进入开仓阶段，目标数=%d", len(validTargets)))
	openHedges, beforeSnapshot, err := e.executeOpenSequence(ctx, validTargets)
	if err != nil {
		return err
	}
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	if len(openHedges) == 0 {
		e.setCurrentPositions(nil)
		e.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, "开仓结束，没有成功建仓的目标")
		e.logger.Println("模块四结束，没有成功建仓的目标")
		return nil
	}

	e.runtimeReporter.EmitWithReason(StageClose, len(openHedges), fmt.Sprintf("进入平仓阶段，持仓数=%d", len(openHedges)))
	settlement, err := e.executeCloseSequence(ctx, openHedges, beforeSnapshot)
	if err != nil {
		return err
	}

	e.logger.Printf(
		"模块五完成，目标数=%d 资金费收入=%.6f 理论资金费=%.6f 资金费偏差=%.6f 现货交易PnL=%.6f 合约交易PnL=%.6f 总手续费=%.6f 精确净利润=%.6f 账户净变动=%.6f 结算时间=%s",
		settlement.Targets,
		settlement.FundingIncome,
		settlement.ExpectedFundingIncome,
		settlement.FundingIncomeDiff,
		settlement.SpotTradingPnL,
		settlement.FuturesTradingPnL,
		settlement.TotalFees,
		settlement.NetPnL,
		settlement.SnapshotNetPnL,
		settlement.CloseExecutedAt.Format("15:04:05"),
	)
	if settlement.HasAlert {
		e.logger.Printf("模块五告警: %s", strings.Join(settlement.Alerts, " | "))
	}
	if e.reportStore != nil {
		if err := e.reportStore.AppendSettlement(settlement); err != nil {
			e.logger.Printf("模块五 JSON 落盘失败: %v", err)
		}
	}
	if err := e.noteDailySettlement(settlement.NetPnL); err != nil {
		return err
	}
	if e.cfg.RiskConfig.MaxCycleLossUSDT > 0 && settlement.NetPnL <= -e.cfg.RiskConfig.MaxCycleLossUSDT {
		if incidentErr := e.recordExecutionIncident("cycle_loss_limit", "-", fmt.Sprintf("单周期净亏损 %.6f USDT 已超过阈值 %.6f USDT", -settlement.NetPnL, e.cfg.RiskConfig.MaxCycleLossUSDT)); incidentErr != nil {
			return incidentErr
		}
		return fatalEngineError{message: fmt.Sprintf("单周期净亏损达到熔断阈值: loss=%.6f limit=%.6f", -settlement.NetPnL, e.cfg.RiskConfig.MaxCycleLossUSDT)}
	}
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	e.setCurrentMonitorTargets(nil)
	e.setCurrentPositions(nil)
	e.runtimeReporter.EmitWithReason(StageCompleted, 0, "本轮狙击周期已完成")

	return nil
}

// scanTopFundingTargets 在 XX:57:00 后扫描全网费率，并取 Top 5。
func (e *FundingRateSniperEngine) scanTopFundingTargets(ctx context.Context) ([]*MonitoredTarget, error) {
	items, err := e.fetchPremiumIndex(ctx)
	if err != nil {
		return nil, err
	}

	now := e.serverNow()
	candidates := make([]FundingCandidate, 0, len(items))
	for _, item := range items {
		if _, ok := e.goldenSymbolSet[item.Symbol]; !ok {
			continue
		}

		nextFundingTime := time.UnixMilli(item.NextFundingTime)
		remaining := nextFundingTime.Sub(now)
		if remaining <= 0 || remaining > e.cfg.StrategyConfig.ScanWindow {
			continue
		}

		staleness := premiumIndexStaleness(now, item)
		if e.cfg.NetworkConfig.MaxPremiumIndexStaleness > 0 && staleness > e.cfg.NetworkConfig.MaxPremiumIndexStaleness {
			continue
		}
		profile := e.symbolRiskProfile(item.Symbol)
		estimatedRate, lastFundingRate, fundingJump, basisRate, scanScoreRate := estimateScanFundingSignal(item)
		if estimatedRate <= e.cfg.StrategyConfig.MinEstimatedFundingRate*profile.MinEstimatedRateMultiplier {
			continue
		}
		if estimatedRate >= e.cfg.StrategyConfig.MaxEstimatedFundingRate {
			continue
		}
		if fundingJump >= e.cfg.StrategyConfig.MaxFundingRateJump {
			continue
		}
		if basisRate > e.cfg.StrategyConfig.MaxScanBasisRate {
			continue
		}

		meta, ok := e.futuresMeta[item.Symbol]
		if !ok {
			continue
		}
		spotMeta, ok := e.spotMeta[meta.Pair]
		if !ok {
			continue
		}

		candidates = append(candidates, FundingCandidate{
			Symbol:            item.Symbol,
			Pair:              meta.Pair,
			RiskTier:          profile.Tier,
			EstimatedRate:     estimatedRate,
			EstimatedRateText: formatFloat(estimatedRate),
			LastFundingRate:   lastFundingRate,
			FundingRateJump:   fundingJump,
			BasisRate:         basisRate,
			ScanScoreRate:     scanScoreRate,
			NextFundingTime:   nextFundingTime,
			MarkPrice:         parseFloat(item.MarkPrice),
			SpotBaseAsset:     spotMeta.BaseAsset,
			SpotBasePrecision: spotMeta.BaseAssetPrecision,
			QuantityPrecision: meta.QuantityPrecision,
		})
	}

	for idx := range candidates {
		futuresRule := e.futuresRules[candidates[idx].Symbol]
		profile := e.symbolRiskProfile(candidates[idx].Symbol)
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / candidates[idx].MarkPrice
		candidates[idx].OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, candidates[idx].QuantityPrecision)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ScanScoreRate > candidates[j].ScanScoreRate
	})
	if len(candidates) > e.cfg.StrategyConfig.MaxTargets {
		candidates = candidates[:e.cfg.StrategyConfig.MaxTargets]
	}

	targets := make([]*MonitoredTarget, 0, len(candidates))
	for _, candidate := range candidates {
		targets = append(targets, &MonitoredTarget{
			TraceRecordMetadata: e.recordMetadata(),
			RunID:               e.runID,
			CycleID:             e.currentCycleID,
			Candidate:           candidate,
			IsValid:             1,
			MonitorState:        "watching",
		})
	}

	e.logger.Printf("模块二完成，候选目标数量: %d", len(targets))
	for _, target := range targets {
		e.logger.Printf(
			"Top 目标: %s tier=%s 预估费率=%.6f 上期费率=%.6f 跳变=%.6f 基差=%.6f 扫描得分=%.6f 下次结算=%s",
			target.Candidate.Symbol,
			target.Candidate.RiskTier,
			target.Candidate.EstimatedRate,
			target.Candidate.LastFundingRate,
			target.Candidate.FundingRateJump,
			target.Candidate.BasisRate,
			target.Candidate.ScanScoreRate,
			target.Candidate.NextFundingTime.Format("15:04:05"),
		)
	}

	return targets, nil
}

// monitorTargets 启动模块三的 Top 5 高频死守监控。
func (e *FundingRateSniperEngine) monitorTargets(ctx context.Context, targets []*MonitoredTarget) []*MonitoredTarget {
	stopAt := earliestFundingTime(targets).Add(-e.cfg.StrategyConfig.MonitorStopLead)
	monitorCtx, cancel := context.WithDeadline(ctx, stopAt)
	defer cancel()

	e.StartBookTickerStreams(monitorCtx, targets)
	e.warmupMarketCaches(monitorCtx, targets)

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(t *MonitoredTarget) {
			defer wg.Done()
			e.monitorWorker(monitorCtx, t)
		}(target)
	}

	wg.Wait()
	e.logMonitorSummary(targets)
	if e.reportStore != nil {
		if err := e.reportStore.AppendMonitorSnapshot(stopAt, targets); err != nil {
			e.logger.Printf("模块三 JSON 落盘失败: %v", err)
		}
	}
	e.logger.Printf("模块三完成，监控截止时间: %s", stopAt.Format("15:04:05"))
	return targets
}

func (e *FundingRateSniperEngine) warmupMarketCaches(ctx context.Context, targets []*MonitoredTarget) {
	if len(targets) == 0 {
		return
	}

	spotSymbols := make([]string, 0, len(targets))
	futuresSymbols := make([]string, 0, len(targets))
	for _, target := range targets {
		spotSymbols = append(spotSymbols, target.Candidate.Pair)
		futuresSymbols = append(futuresSymbols, target.Candidate.Symbol)
	}

	waitCtx := ctx
	var cancel context.CancelFunc
	if e.cfg.StreamWarmupTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, e.cfg.StreamWarmupTimeout)
		defer cancel()
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		spotTickerMissing, futuresTickerMissing, spotDepthMissing, futuresDepthMissing := e.marketCacheMissingSymbols(spotSymbols, futuresSymbols)
		if len(spotTickerMissing) == 0 && len(futuresTickerMissing) == 0 && len(spotDepthMissing) == 0 && len(futuresDepthMissing) == 0 {
			e.logger.Printf(
				"模块三预热完成，现货bookTicker=%d 合约bookTicker=%d 现货深度=%d 合约深度=%d",
				len(spotSymbols),
				len(futuresSymbols),
				len(spotSymbols),
				len(futuresSymbols),
			)
			return
		}

		select {
		case <-waitCtx.Done():
			e.logger.Printf(
				"模块三预热超时，开始补齐缺失行情，现货bookTicker缺失=%d 合约bookTicker缺失=%d 现货深度缺失=%d 合约深度缺失=%d",
				len(spotTickerMissing),
				len(futuresTickerMissing),
				len(spotDepthMissing),
				len(futuresDepthMissing),
			)
			e.backfillMissingMarketData(ctx, targets)
			return
		case <-ticker.C:
		}
	}
}

func (e *FundingRateSniperEngine) marketCacheMissingSymbols(spotSymbols []string, futuresSymbols []string) ([]string, []string, []string, []string) {
	var spotTickerMissing []string
	if e.spotTickerCache != nil {
		spotTickerMissing = e.spotTickerCache.MissingSymbols(spotSymbols)
	}

	var futuresTickerMissing []string
	if e.futuresTickerCache != nil {
		futuresTickerMissing = e.futuresTickerCache.MissingSymbols(futuresSymbols)
	}

	var spotDepthMissing []string
	if e.spotDepthCache != nil {
		spotDepthMissing = e.spotDepthCache.MissingSymbols(spotSymbols)
	}

	var futuresDepthMissing []string
	if e.futuresDepthCache != nil {
		futuresDepthMissing = e.futuresDepthCache.MissingSymbols(futuresSymbols)
	}

	return spotTickerMissing, futuresTickerMissing, spotDepthMissing, futuresDepthMissing
}

func (e *FundingRateSniperEngine) backfillMissingMarketData(ctx context.Context, targets []*MonitoredTarget) {
	var wg sync.WaitGroup
	for _, target := range targets {
		needSpotTicker := e.spotTickerCache != nil
		if needSpotTicker {
			_, needSpotTicker = e.spotTickerCache.Get(target.Candidate.Pair)
			needSpotTicker = !needSpotTicker
		}

		needFuturesTicker := e.futuresTickerCache != nil
		if needFuturesTicker {
			_, needFuturesTicker = e.futuresTickerCache.Get(target.Candidate.Symbol)
			needFuturesTicker = !needFuturesTicker
		}

		needSpotDepth := e.spotDepthCache != nil
		if needSpotDepth {
			_, needSpotDepth = e.spotDepthCache.Get(target.Candidate.Pair)
			needSpotDepth = !needSpotDepth
		}

		needFuturesDepth := e.futuresDepthCache != nil
		if needFuturesDepth {
			_, needFuturesDepth = e.futuresDepthCache.Get(target.Candidate.Symbol)
			needFuturesDepth = !needFuturesDepth
		}

		if !needSpotTicker && !needFuturesTicker && !needSpotDepth && !needFuturesDepth {
			continue
		}

		wg.Add(1)
		go func(t *MonitoredTarget, needSpotTicker bool, needFuturesTicker bool, needSpotDepth bool, needFuturesDepth bool) {
			defer wg.Done()

			if needSpotTicker || needFuturesTicker {
				spotTicker, futuresTicker, tickerErr := e.fetchDualBookTicker(ctx, t.Candidate.Pair, t.Candidate.Symbol)
				if tickerErr != nil {
					e.logger.Printf("补齐 bookTicker 失败，symbol=%s err=%v", t.Candidate.Symbol, tickerErr)
				} else {
					if needSpotTicker && e.spotTickerCache != nil {
						e.spotTickerCache.Update(spotTicker)
					}
					if needFuturesTicker && e.futuresTickerCache != nil {
						e.futuresTickerCache.Update(futuresTicker)
					}
				}
			}

			if needSpotDepth || needFuturesDepth {
				spotDepth, futuresDepth, depthErr := e.fetchDualOrderBookDepth(ctx, t.Candidate.Pair, t.Candidate.Symbol)
				if depthErr != nil {
					e.logger.Printf("补齐深度失败，symbol=%s err=%v", t.Candidate.Symbol, depthErr)
					return
				}

				if needSpotDepth && e.spotDepthCache != nil {
					e.spotDepthCache.Update(t.Candidate.Pair, spotDepth)
				}
				if needFuturesDepth && e.futuresDepthCache != nil {
					e.futuresDepthCache.Update(t.Candidate.Symbol, futuresDepth)
				}
			}
		}(target, needSpotTicker, needFuturesTicker, needSpotDepth, needFuturesDepth)
	}
	wg.Wait()
}

// monitorWorker 负责单个目标的盘口监控。
func (e *FundingRateSniperEngine) monitorWorker(ctx context.Context, target *MonitoredTarget) {
	ticker := time.NewTicker(e.cfg.MonitorInterval)
	defer ticker.Stop()

	var spotSignal <-chan struct{}
	var futuresSignal <-chan struct{}
	spotUnsubscribe := func() {}
	futuresUnsubscribe := func() {}
	if e.spotDepthCache != nil {
		spotSignal, spotUnsubscribe = e.spotDepthCache.Subscribe(target.Candidate.Pair)
	}
	if e.futuresDepthCache != nil {
		futuresSignal, futuresUnsubscribe = e.futuresDepthCache.Subscribe(target.Candidate.Symbol)
	}
	defer spotUnsubscribe()
	defer futuresUnsubscribe()

	consecutiveBreaches := 0
	e.checkTargetCost(ctx, target, &consecutiveBreaches)

	for {
		if err := e.checkPlatformFuse(); err != nil {
			e.logger.Printf("监控 %s 提前结束: %v", target.Candidate.Symbol, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-spotSignal:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		case <-futuresSignal:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		case <-ticker.C:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		}
	}
}

// checkTargetCost 执行一次盘口摩擦成本检查。
func (e *FundingRateSniperEngine) checkTargetCost(ctx context.Context, target *MonitoredTarget, consecutiveBreaches *int) {
	if err := e.refreshTargetFundingEstimate(ctx, target, false); err != nil {
		e.logger.Printf("监控 %s 时刷新资金费预估失败: %v", target.Candidate.Symbol, err)
		return
	}
	spotDepth, futuresDepth, err := e.fetchDualOrderBookDepth(ctx, target.Candidate.Pair, target.Candidate.Symbol)
	if err != nil {
		e.logger.Printf("监控 %s 时获取深度盘口失败: %v", target.Candidate.Symbol, err)
		return
	}

	futuresQty := target.Candidate.OrderQuantity
	profile := e.symbolRiskProfile(target.Candidate.Symbol)
	if futuresQty <= 0 {
		futuresRule := e.futuresRules[target.Candidate.Symbol]
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / target.Candidate.MarkPrice
		futuresQty = normalizeQuantity(rawOrderQty, futuresRule, target.Candidate.QuantityPrecision)
		target.Candidate.OrderQuantity = futuresQty
	}
	if futuresQty <= 0 {
		return
	}

	spotRule := e.spotRules[target.Candidate.Pair]
	spotQty := normalizeQuantity(futuresQty, spotRule, target.Candidate.SpotBasePrecision)
	requiredQty := math.Min(futuresQty, spotQty)
	if requiredQty <= 0 {
		return
	}

	spotVWAP, spotBestAsk, spotFilled, spotEnough := computeAverageFillPrice(spotDepth.Asks, requiredQty)
	futuresVWAP, futuresBestBid, futuresFilled, futuresEnough := computeAverageFillPrice(futuresDepth.Bids, requiredQty)
	if !spotEnough || !futuresEnough || spotVWAP <= 0 || futuresVWAP <= 0 {
		shortQty := requiredQty - math.Min(spotFilled, futuresFilled)
		coverageRatio := 0.0
		if requiredQty > 0 {
			coverageRatio = math.Min(spotFilled/requiredQty, futuresFilled/requiredQty)
		}
		risk := EvaluateExecutionCoverageRisk("depth", coverageRatio)
		e.handleMarketRisk(target, consecutiveBreaches, -1, risk, fmt.Sprintf("连续观察阶段发现盘口深度不足，coverage=%.4f 所需数量=%.8f 缺口=%.8f", coverageRatio, requiredQty, shortQty))
		return
	}

	// 真实开仓方向是“现货吃卖盘买入 + 合约吃买盘卖出”，因此使用按下单量计算的 VWAP 评估冲击成本。
	spread := (spotVWAP - futuresVWAP) / spotVWAP
	netProfit := target.Candidate.EstimatedRate - spread - e.cfg.StrategyConfig.FeeReserveRate

	target.LastSpotAsk = spotBestAsk
	target.LastFuturesBid = futuresBestBid
	target.LastSpotVWAP = spotVWAP
	target.LastFuturesVWAP = futuresVWAP
	target.LastSpread = spread
	target.LastNetProfit = netProfit
	target.LastObserveTime = time.Now()

	immediateInvalidateRate := e.cfg.RiskConfig.ImmediateInvalidateRate + profile.ImmediateInvalidateBump
	decision := EvaluateMonitorNetProfit(netProfit, immediateInvalidateRate, e.cfg.RiskConfig.MinNetProfitRate*profile.MinNetProfitMultiplier, e.cfg.RiskConfig.MonitorMaxConsecutiveBreach, *consecutiveBreaches)
	if decision.Invalidate && decision.State == "invalidated" {
		e.invalidateMonitorTarget(target, netProfit, fmt.Sprintf("净收益触发立即作废阈值，净收益 %.6f", netProfit))
		return
	}

	if decision.State == "observing" {
		e.applyMonitorObserveBreach(target, consecutiveBreaches, netProfit, fmt.Sprintf("连续观察阶段净收益不足，净收益 %.6f", netProfit))
		return
	}

	*consecutiveBreaches = 0
	target.BreachCount = 0
	e.transitionMonitorState(target, "healthy", "净收益回到健康区间")
}

// executeOpenSequence 执行模块四的压哨多空开枪，并记录开仓前账户基线。
func (e *FundingRateSniperEngine) executeOpenSequence(ctx context.Context, targets []*MonitoredTarget) ([]OpenHedge, CycleAccountSnapshot, error) {
	validTargets := filterValidTargets(targets)
	if len(validTargets) == 0 {
		return nil, CycleAccountSnapshot{}, nil
	}

	beforeSnapshot, err := e.captureCycleAccountSnapshot(ctx, validTargets)
	if err != nil {
		return nil, CycleAccountSnapshot{}, err
	}

	fireAt := earliestFundingTime(validTargets).Add(-e.cfg.StrategyConfig.MonitorStopLead)
	if syncErr := e.syncServerTimeOffset(ctx); syncErr != nil {
		e.logger.Printf("模块四开仓前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(validTargets))
	orderTargets := make([]*MonitoredTarget, 0, len(validTargets))
	remainingSpotUSDT := beforeSnapshot.SpotUSDTFree
	remainingFuturesUSDT := beforeSnapshot.FuturesUSDTAvailable
	remainingCycleNotional := e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT
	for _, target := range validTargets {
		profile := e.symbolRiskProfile(target.Candidate.Symbol)
		refreshCtx, refreshCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		refreshErr := e.refreshTargetFundingEstimate(refreshCtx, target, true)
		refreshCancel()
		if refreshErr != nil {
			e.logger.Printf("模块四跳过 %s，开仓前刷新资金费预估失败: %v", target.Candidate.Symbol, refreshErr)
			continue
		}
		if target.Candidate.EstimatedRate <= e.cfg.StrategyConfig.MinEstimatedFundingRate*profile.MinEstimatedRateMultiplier {
			e.logger.Printf("模块四跳过 %s，开仓前最新预估费率不足: %.6f", target.Candidate.Symbol, target.Candidate.EstimatedRate)
			continue
		}
		if target.Candidate.EstimatedRate >= e.cfg.StrategyConfig.MaxEstimatedFundingRate {
			e.logger.Printf("模块四跳过 %s，开仓前最新预估费率过高触发异常熔断: %.6f", target.Candidate.Symbol, target.Candidate.EstimatedRate)
			continue
		}
		if target.Candidate.FundingRateJump >= e.cfg.StrategyConfig.MaxFundingRateJump {
			e.logger.Printf("模块四跳过 %s，开仓前资金费跳变过大触发异常熔断: %.6f", target.Candidate.Symbol, target.Candidate.FundingRateJump)
			continue
		}
		if target.Candidate.BasisRate > e.cfg.StrategyConfig.MaxScanBasisRate {
			e.logger.Printf("模块四跳过 %s，开仓前最新基差超阈值: %.6f", target.Candidate.Symbol, target.Candidate.BasisRate)
			continue
		}

		futuresRule := e.futuresRules[target.Candidate.Symbol]
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / target.Candidate.MarkPrice
		target.Candidate.OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, target.Candidate.QuantityPrecision)
		if !isQuantityTradable(target.Candidate.OrderQuantity, target.Candidate.MarkPrice, futuresRule) {
			continue
		}
		spotReferencePrice := target.LastSpotAsk
		if spotReferencePrice <= 0 {
			spotReferencePrice = target.Candidate.MarkPrice
		}
		futuresReferencePrice := target.LastFuturesBid
		if futuresReferencePrice <= 0 {
			futuresReferencePrice = target.Candidate.MarkPrice
		}
		capacityCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		adjustedQty, capacityReason, capacityErr := e.constrainOrderQuantityByMarketCapacity(capacityCtx, target, spotReferencePrice, futuresReferencePrice)
		cancel()
		if capacityErr != nil {
			e.setSymbolCooldown(target.Candidate.Symbol, "liquidity", "major", fmt.Sprintf("开仓前市场容量校验失败: %v", capacityErr))
			e.logger.Printf("模块四跳过 %s，市场容量校验失败: %v", target.Candidate.Symbol, capacityErr)
			continue
		}
		if adjustedQty <= 0 {
			e.setSymbolCooldown(target.Candidate.Symbol, "liquidity", "major", fmt.Sprintf("开仓前市场容量校验后数量归零: %s", capacityReason))
			e.logger.Printf("模块四跳过 %s，市场容量校验后数量归零: %s", target.Candidate.Symbol, capacityReason)
			continue
		}
		if adjustedQty+1e-12 < target.Candidate.OrderQuantity {
			coverageRatio := adjustedQty / target.Candidate.OrderQuantity
			decision := EvaluateExecutionCoverageRisk("liquidity", coverageRatio)
			if decision.Action == "symbol_cooldown" {
				e.setSymbolCooldown(target.Candidate.Symbol, decision.Category, decision.Severity, fmt.Sprintf("开仓前市场容量不足 coverage=%.4f %s", coverageRatio, capacityReason))
			} else if decision.Action == "global_pause" {
				if fuseErr := e.tripPlatformFuse(decision.Category, decision.Severity, fmt.Sprintf("symbol=%s 开仓前市场容量极差 coverage=%.4f %s", target.Candidate.Symbol, coverageRatio, capacityReason)); fuseErr != nil {
					return nil, beforeSnapshot, fuseErr
				}
			}
			e.logger.Printf(
				"模块四缩减 %s 下单数量，原数量=%.8f 新数量=%.8f 原名义=%.6fUSDT 新名义=%.6fUSDT %s",
				target.Candidate.Symbol,
				target.Candidate.OrderQuantity,
				adjustedQty,
				target.Candidate.OrderQuantity*futuresReferencePrice,
				adjustedQty*futuresReferencePrice,
				capacityReason,
			)
		}
		target.Candidate.OrderQuantity = adjustedQty
		if !isQuantityTradable(target.Candidate.OrderQuantity, futuresReferencePrice, futuresRule) {
			e.logger.Printf("模块四跳过 %s，市场容量限制后数量低于最小下单门槛: %s", target.Candidate.Symbol, capacityReason)
			continue
		}
		orderNotional := target.Candidate.OrderQuantity * futuresReferencePrice
		if remainingCycleNotional > 0 && orderNotional > remainingCycleNotional+1e-9 {
			cycleLimitedQty := normalizeQuantity(remainingCycleNotional/futuresReferencePrice, futuresRule, target.Candidate.QuantityPrecision)
			if !isQuantityTradable(cycleLimitedQty, futuresReferencePrice, futuresRule) {
				e.logger.Printf("模块四跳过 %s，单周期总敞口上限不足以继续开仓，remainingNotional=%.6f", target.Candidate.Symbol, remainingCycleNotional)
				continue
			}
			e.logger.Printf("模块四缩减 %s 下单数量以满足单周期总敞口上限，原名义=%.6fUSDT 新名义=%.6fUSDT 上限剩余=%.6fUSDT", target.Candidate.Symbol, orderNotional, cycleLimitedQty*futuresReferencePrice, remainingCycleNotional)
			target.Candidate.OrderQuantity = cycleLimitedQty
			orderNotional = target.Candidate.OrderQuantity * futuresReferencePrice
		}
		spotRequiredUSDT, futuresRequiredUSDT := estimateTargetCapitalRequirement(e.cfg, target, spotReferencePrice, futuresReferencePrice)
		if spotRequiredUSDT <= 0 || futuresRequiredUSDT <= 0 {
			e.logger.Printf("模块四跳过 %s，资金需求估算无效，spotRequired=%.6f futuresRequired=%.6f", target.Candidate.Symbol, spotRequiredUSDT, futuresRequiredUSDT)
			continue
		}
		if spotRequiredUSDT > remainingSpotUSDT+1e-9 || futuresRequiredUSDT > remainingFuturesUSDT+1e-9 {
			e.logger.Printf(
				"模块四跳过 %s，账户可用资金不足，spotRequired=%.6f spotFree=%.6f futuresRequired=%.6f futuresAvailable=%.6f",
				target.Candidate.Symbol,
				spotRequiredUSDT,
				remainingSpotUSDT,
				futuresRequiredUSDT,
				remainingFuturesUSDT,
			)
			continue
		}
		remainingSpotUSDT -= spotRequiredUSDT
		remainingFuturesUSDT -= futuresRequiredUSDT
		if remainingCycleNotional > 0 {
			remainingCycleNotional -= orderNotional
		}
		if err := e.setCycleOpenNotionalUsed(e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT - maxFloat(remainingCycleNotional, 0)); err != nil {
			return nil, CycleAccountSnapshot{}, err
		}

		futuresOrders = append(futuresOrders, e.buildProtectedOpenFuturesOrder(target))
		orderTargets = append(orderTargets, target)
	}
	if len(futuresOrders) == 0 {
		return nil, beforeSnapshot, nil
	}
	if sleepErr := e.sleepUntilServerTime(ctx, fireAt); sleepErr != nil {
		return nil, CycleAccountSnapshot{}, sleepErr
	}

	e.logger.Printf("模块四开始，触发时间=%s，先发送合约批量空单，下单目标=%d", fireAt.Format("15:04:05"), len(futuresOrders))
	if err := e.noteOrderPlaced("batch_open_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), true); err != nil {
		return nil, CycleAccountSnapshot{}, err
	}
	futuresResults, err := e.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return nil, beforeSnapshot, err
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		hedges   []OpenHedge
		fatalErr error
	)

	for idx, result := range futuresResults {
		if result.Code != 0 || result.OrderID == 0 {
			incidentErr := e.recordExecutionIncident("open_futures_rejected", futuresOrders[idx].Symbol, fmt.Sprintf("code=%d msg=%s", result.Code, result.Msg))
			if incidentErr != nil {
				mu.Lock()
				if fatalErr == nil {
					fatalErr = incidentErr
				}
				mu.Unlock()
			}
			continue
		}

		target := orderTargets[idx]
		wg.Add(1)
		go func(t *MonitoredTarget, futureResult FuturesBatchOrderResult) {
			defer wg.Done()
			hedge, err := e.openHedgeForTarget(ctx, t, futureResult)
			if err != nil {
				incidentErr := e.recordExecutionIncident("open_recovery", t.Candidate.Symbol, err.Error())
				mu.Lock()
				if incidentErr != nil && fatalErr == nil {
					fatalErr = incidentErr
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			hedges = append(hedges, hedge)
			mu.Unlock()
		}(target, result)
	}

	wg.Wait()
	if fatalErr != nil {
		return nil, beforeSnapshot, fatalErr
	}
	e.setCurrentPositionsFromHedges(hedges)
	e.logger.Printf("模块四结束，成功锁仓数量: %d", len(hedges))
	return hedges, beforeSnapshot, nil
}

// executeCloseSequence 执行模块五的收益落袋与闪电撤退。
func (e *FundingRateSniperEngine) executeCloseSequence(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot) (CycleSettlement, error) {
	if len(hedges) == 0 {
		return CycleSettlement{}, nil
	}

	fundingTime := earliestFundingTimeFromHedges(hedges)
	closeAt := fundingTime.Add(e.cfg.ExecutionConfig.ExitDelayAfterFunding)
	if syncErr := e.syncServerTimeOffset(ctx); syncErr != nil {
		e.logger.Printf("模块五平仓前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	if err := e.sleepUntilServerTime(ctx, closeAt); err != nil {
		return CycleSettlement{}, err
	}

	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(hedges))
	for _, hedge := range hedges {
		futuresOrders = append(futuresOrders, e.buildProtectedCloseFuturesOrder(ctx, hedge))
	}

	e.logger.Printf("模块五开始，闪电撤退时间=%s", closeAt.Format("15:04:05"))
	if err := e.noteOrderPlaced("batch_close_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), false); err != nil {
		return CycleSettlement{}, err
	}
	futuresCloseResults, err := e.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return CycleSettlement{}, err
	}

	closeResultBySymbol := make(map[string]FuturesBatchOrderResult, len(futuresCloseResults))
	for _, result := range futuresCloseResults {
		closeResultBySymbol[result.Symbol] = result
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		errs     []string
		fatalErr error
	)
	for idx := range hedges {
		wg.Add(1)
		go func(hedge *OpenHedge) {
			defer wg.Done()
			futureResult, ok := closeResultBySymbol[hedge.Target.Symbol]
			if !ok {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("合约平仓回执缺失 %s", hedge.Target.Symbol))
				mu.Unlock()
				if incidentErr := e.recordExecutionIncident("close_futures_missing", hedge.Target.Symbol, "合约平仓回执缺失"); incidentErr != nil {
					mu.Lock()
					if fatalErr == nil {
						fatalErr = incidentErr
					}
					mu.Unlock()
				}
				return
			}

			closeAlerts, closeErr := e.closeHedgePosition(ctx, hedge, futureResult)
			mu.Lock()
			errs = append(errs, closeAlerts...)
			mu.Unlock()
			if closeErr == nil {
				return
			}
			incidentErr := e.recordExecutionIncident("close_recovery", hedge.Target.Symbol, closeErr.Error())
			mu.Lock()
			errs = append(errs, closeErr.Error())
			if incidentErr != nil && fatalErr == nil {
				fatalErr = incidentErr
			}
			mu.Unlock()
		}(&hedges[idx])
	}
	wg.Wait()
	if fatalErr != nil {
		return CycleSettlement{}, fatalErr
	}
	e.setCurrentPositions(nil)

	afterSnapshot, err := e.captureCycleAccountSnapshotFromHedges(ctx, hedges)
	if err != nil {
		return CycleSettlement{}, err
	}

	spotTradingPnL, futuresTradingPnL, totalFees := 0.0, 0.0, 0.0
	totalFundingIncome, expectedFundingIncome, fundingAlerts := e.settleFundingIncome(ctx, hedges, beforeSnapshot, fundingTime)
	for _, hedge := range hedges {
		closedSpotOpenCost := 0.0
		if hedge.HedgeQty > 0 && hedge.CloseSpotExecutedQty > 0 {
			closedSpotOpenCost = hedge.OpenSpotQuoteQty * (math.Min(hedge.HedgeQty, hedge.CloseSpotExecutedQty) / hedge.HedgeQty)
		}
		spotTradingPnL += hedge.CloseSpotQuoteQty - closedSpotOpenCost
		totalFees += hedge.OpenSpotFeeUSDT + hedge.CloseSpotFeeUSDT + hedge.OpenFuturesFeeUSDT + hedge.CloseFuturesFeeUSDT

		if hedge.CloseFuturesRealizedPnL != 0 {
			futuresTradingPnL += hedge.CloseFuturesRealizedPnL
		} else if hedge.OpenFuturesAvgPrice > 0 && hedge.CloseFuturesAvgPrice > 0 {
			closedFuturesQty := hedge.CloseFuturesExecutedQty
			if closedFuturesQty <= 0 {
				closedFuturesQty = hedge.HedgeQty
			}
			futuresTradingPnL += (hedge.OpenFuturesAvgPrice - hedge.CloseFuturesAvgPrice) * closedFuturesQty
		}
	}

	residualSpotValue := calculateResidualSpotValue(afterSnapshot.SpotAssetBalances, hedges)
	spotUSDTDelta := afterSnapshot.SpotUSDTFree - beforeSnapshot.SpotUSDTFree
	futuresUSDTDelta := afterSnapshot.FuturesUSDTWallet - beforeSnapshot.FuturesUSDTWallet
	netPnL := totalFundingIncome + spotTradingPnL + futuresTradingPnL - totalFees
	fundingIncomeDiff := totalFundingIncome - expectedFundingIncome
	snapshotNetPnL := spotUSDTDelta + futuresUSDTDelta + residualSpotValue
	alerts := e.buildSettlementAlerts(afterSnapshot, hedges, residualSpotValue, netPnL, snapshotNetPnL)
	alerts = append(alerts, fundingAlerts...)

	if len(errs) > 0 {
		e.logger.Printf("模块五对账警告: %s", strings.Join(errs, " | "))
		alerts = append(alerts, errs...)
	}

	return CycleSettlement{
		TraceRecordMetadata:   e.recordMetadata(),
		RunID:                 e.runID,
		CycleID:               e.currentCycleID,
		Targets:               len(hedges),
		FundingIncome:         totalFundingIncome,
		ExpectedFundingIncome: expectedFundingIncome,
		FundingIncomeDiff:     fundingIncomeDiff,
		TradingPnL:            spotTradingPnL + futuresTradingPnL,
		SpotTradingPnL:        spotTradingPnL,
		FuturesTradingPnL:     futuresTradingPnL,
		TotalFees:             totalFees,
		NetPnL:                netPnL,
		SnapshotNetPnL:        snapshotNetPnL,
		ResidualSpotValue:     residualSpotValue,
		SpotUSDTDelta:         spotUSDTDelta,
		FuturesUSDTDelta:      futuresUSDTDelta,
		CloseExecutedAt:       closeAt,
		FundingTimePoint:      fundingTime,
		BeforeSnapshot:        beforeSnapshot,
		AfterSnapshot:         afterSnapshot,
		HasAlert:              len(alerts) > 0,
		Alerts:                alerts,
	}, nil
}

func (e *FundingRateSniperEngine) openHedgeForTarget(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult) (OpenHedge, error) {
	futuresExecution, resolveErr := e.resolveFuturesOrderExecution(ctx, futureResult.Symbol, futureResult.ClientOrderID, futureResult)
	filledFuturesQty := futuresExecution.ExecutedQty
	if resolveErr != nil || filledFuturesQty <= executionQtyTolerance {
		cleanupCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		e.flattenUncertainFuturesOpen(cleanupCtx, target.Candidate.Symbol, futureResult.ClientOrderID, futureResult)
		if resolveErr != nil {
			return OpenHedge{}, fmt.Errorf("合约空单未确认成交: %w", resolveErr)
		}
		return OpenHedge{}, fmt.Errorf("合约空单成交数量为 0")
	}

	spotRule := e.spotRules[target.Candidate.Pair]
	spotTargetQty := normalizeQuantity(filledFuturesQty, spotRule, target.Candidate.SpotBasePrecision)
	spotPriceForCheck := target.LastSpotAsk
	if spotPriceForCheck <= 0 {
		spotPriceForCheck = target.Candidate.MarkPrice
	}
	if !isQuantityTradable(spotTargetQty, spotPriceForCheck, spotRule) {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeSingleFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty); err != nil {
			return OpenHedge{}, fmt.Errorf("现货对冲数量无效且合约回补失败: %w", err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_invalid_spot_qty", fmt.Sprintf("filledFuturesQty=%.8f spotTargetQty=%.8f", filledFuturesQty, spotTargetQty))
		return OpenHedge{}, fmt.Errorf("现货对冲数量归零，已回补合约空单")
	}

	spotClientOrderID := newClientOrderID("sopen", target.Candidate.Pair)
	spotOrder, spotErr := e.placeSpotMarketBuyByQuantityIdempotent(ctx, target.Candidate.Pair, spotTargetQty, spotClientOrderID)
	if spotErr != nil {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeSingleFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty); err != nil {
			return OpenHedge{}, fmt.Errorf("现货补仓失败且合约回补失败: spotErr=%v recoverErr=%w", spotErr, err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_spot_open_failure", fmt.Sprintf("filledFuturesQty=%.8f spotErr=%v", filledFuturesQty, spotErr))
		return OpenHedge{}, fmt.Errorf("现货补仓失败，已回补合约空单: %w", spotErr)
	}

	boughtSpotQty := resolveSpotNetBaseQuantity(spotOrder, target.Candidate.SpotBaseAsset)
	if boughtSpotQty <= executionQtyTolerance {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeSingleFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty); err != nil {
			return OpenHedge{}, fmt.Errorf("现货买单无有效成交且合约回补失败: %w", err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_zero_spot_fill", fmt.Sprintf("filledFuturesQty=%.8f", filledFuturesQty))
		return OpenHedge{}, fmt.Errorf("现货买单无有效成交，已回补合约空单")
	}

	finalSpotQty := boughtSpotQty
	additionalSpotFeeUSDT := 0.0
	if spotResidualQty := finalSpotQty - filledFuturesQty; spotResidualQty > executionQtyTolerance {
		residualQty := normalizeQuantity(spotResidualQty, spotRule, target.Candidate.SpotBasePrecision)
		if residualQty > executionQtyTolerance && isQuantityTradable(residualQty, spotPriceForCheck, spotRule) {
			recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
			recoveryOrder, err := e.placeSpotMarketSellIdempotent(recoverCtx, target.Candidate.Pair, residualQty, newClientOrderID("srebalance", target.Candidate.Pair))
			cancel()
			if err != nil {
				return OpenHedge{}, fmt.Errorf("现货超买后回卖失败: %w", err)
			}
			soldQty := parseFloat(recoveryOrder.ExecutedQty)
			finalSpotQty = math.Max(0, finalSpotQty-soldQty)
			additionalSpotFeeUSDT += e.estimateSpotCommissionUSDT(ctx, recoveryOrder)
			e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "rebalance_spot_excess", fmt.Sprintf("boughtSpotQty=%.8f soldQty=%.8f", boughtSpotQty, soldQty))
		}
	}

	hedgeQty := math.Min(filledFuturesQty, finalSpotQty)
	additionalFuturesFeeUSDT := 0.0
	if futuresResidualQty := filledFuturesQty - hedgeQty; futuresResidualQty > executionQtyTolerance {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeSingleFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, futuresResidualQty)
		cancel()
		if err != nil {
			return OpenHedge{}, fmt.Errorf("修正合约裸空失败: %w", err)
		}
		additionalFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_residual", fmt.Sprintf("futuresResidualQty=%.8f hedgeQty=%.8f", futuresResidualQty, hedgeQty))
	}

	if hedgeQty <= executionQtyTolerance {
		return OpenHedge{}, fmt.Errorf("对冲后有效数量为 0")
	}

	openSpotQuoteQty := parseFloat(spotOrder.CummulativeQuoteQty)
	if boughtSpotQty > executionQtyTolerance && hedgeQty+executionQtyTolerance < boughtSpotQty {
		openSpotQuoteQty *= hedgeQty / boughtSpotQty
	}

	return OpenHedge{
		Target:              target.Candidate,
		FuturesOrder:        futureResult,
		SpotOrder:           spotOrder,
		SpotBaseQty:         formatFloat(hedgeQty),
		HedgeQty:            hedgeQty,
		HedgeQtyText:        formatFloat(hedgeQty),
		OpenSpotQuoteQty:    openSpotQuoteQty,
		OpenSpotFeeUSDT:     e.estimateSpotCommissionUSDT(ctx, spotOrder) + additionalSpotFeeUSDT,
		OpenFuturesAvgPrice: futuresExecution.AvgPrice,
		OpenFuturesFeeUSDT:  e.estimateFuturesExecutionFeeUSDT(ctx, futuresExecution) + additionalFuturesFeeUSDT,
	}, nil
}

func (e *FundingRateSniperEngine) closeHedgePosition(ctx context.Context, hedge *OpenHedge, futureResult FuturesBatchOrderResult) ([]string, error) {
	alerts := make([]string, 0, 2)
	futuresExecution, resolveErr := e.resolveFuturesOrderExecution(ctx, hedge.Target.Symbol, futureResult.ClientOrderID, futureResult)
	if resolveErr != nil {
		return alerts, fmt.Errorf("合约平仓未确认成交: %w", resolveErr)
	}
	if futuresExecution.ExecutedQty <= executionQtyTolerance {
		return alerts, fmt.Errorf("合约平仓成交数量为 0")
	}

	hedge.CloseFuturesExecutedQty = math.Min(hedge.HedgeQty, futuresExecution.ExecutedQty)
	closeableSpotQty := hedge.CloseFuturesExecutedQty
	if closeableSpotQty <= executionQtyTolerance {
		return alerts, nil
	}
	if futuresExecution.ExecutedQty+executionQtyTolerance < hedge.HedgeQty {
		alerts = append(alerts, fmt.Sprintf("合约平仓部分成交 %s: requested=%.8f executed=%.8f", hedge.Target.Symbol, hedge.HedgeQty, futuresExecution.ExecutedQty))
	}
	hedge.CloseFuturesAvgPrice = futuresExecution.AvgPrice
	hedge.CloseFuturesFeeUSDT = e.estimateFuturesExecutionFeeUSDT(ctx, futuresExecution)
	hedge.CloseFuturesRealizedPnL = futuresExecution.RealizedPnL

	spotRule := e.spotRules[hedge.Target.Pair]
	spotQty := normalizeQuantity(closeableSpotQty, spotRule, hedge.Target.SpotBasePrecision)
	spotReferencePrice := hedge.LastKnownSpotPrice()
	if !isQuantityTradable(spotQty, spotReferencePrice, spotRule) {
		if closeableSpotQty*math.Max(spotReferencePrice, hedge.Target.MarkPrice) >= e.cfg.ResidualSpotAlertUSDT {
			recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
			recoveryExecution, err := e.placeSingleFuturesReopenShort(recoverCtx, hedge.Target.Symbol, closeableSpotQty)
			cancel()
			if err != nil {
				return alerts, fmt.Errorf("现货平仓数量不足且恢复空头失败: %w", err)
			}
			hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
			e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "reopen_short_after_untradable_spot_close", fmt.Sprintf("closeableSpotQty=%.8f spotQty=%.8f", closeableSpotQty, spotQty))
		}
		return append(alerts, fmt.Sprintf("现货平仓数量不足 %s: qty=%.8f", hedge.Target.Pair, spotQty)), nil
	}

	clientOrderID := newClientOrderID("sclose", hedge.Target.Pair)
	spotOrder, spotErr := e.placeSpotMarketSellIdempotent(ctx, hedge.Target.Pair, spotQty, clientOrderID)
	if spotErr != nil {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeSingleFuturesReopenShort(recoverCtx, hedge.Target.Symbol, closeableSpotQty)
		cancel()
		if err != nil {
			return alerts, fmt.Errorf("现货平仓失败且恢复空头失败: spotErr=%v recoverErr=%w", spotErr, err)
		}
		hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "reopen_short_after_spot_close_failure", fmt.Sprintf("closeableSpotQty=%.8f spotErr=%v", closeableSpotQty, spotErr))
		return append(alerts, fmt.Sprintf("现货平仓失败 %s: %v，已恢复空头对冲", hedge.Target.Pair, spotErr)), nil
	}

	hedge.CloseSpotExecutedQty = parseFloat(spotOrder.ExecutedQty)
	hedge.CloseSpotQuoteQty = parseFloat(spotOrder.CummulativeQuoteQty)
	hedge.CloseSpotFeeUSDT = e.estimateSpotCommissionUSDT(ctx, spotOrder)
	if hedge.CloseSpotExecutedQty+executionQtyTolerance < closeableSpotQty {
		residualQty := closeableSpotQty - hedge.CloseSpotExecutedQty
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeSingleFuturesReopenShort(recoverCtx, hedge.Target.Symbol, residualQty)
		cancel()
		if err != nil {
			return alerts, fmt.Errorf("现货平仓部分成交且恢复空头失败: %w", err)
		}
		hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "reopen_short_after_partial_spot_close", fmt.Sprintf("closeableSpotQty=%.8f spotExecutedQty=%.8f residualQty=%.8f", closeableSpotQty, hedge.CloseSpotExecutedQty, residualQty))
		alerts = append(alerts, fmt.Sprintf("现货平仓部分成交 %s: requested=%.8f executed=%.8f，已恢复剩余空头", hedge.Target.Pair, closeableSpotQty, hedge.CloseSpotExecutedQty))
	}
	return alerts, nil
}

func (e *FundingRateSniperEngine) fetchPremiumIndex(ctx context.Context) ([]PremiumIndexItem, error) {
	var payload []PremiumIndexItem
	if err := e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/premiumIndex", &payload); err != nil {
		return nil, fmt.Errorf("获取 premiumIndex 失败: %w", err)
	}
	return payload, nil
}

func (e *FundingRateSniperEngine) fetchPremiumIndexBySymbol(ctx context.Context, symbol string) (PremiumIndexItem, error) {
	var payload PremiumIndexItem
	if err := e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/premiumIndex?symbol="+url.QueryEscape(symbol), &payload); err != nil {
		return PremiumIndexItem{}, fmt.Errorf("获取 premiumIndex 失败: %w", err)
	}
	return payload, nil
}

func (e *FundingRateSniperEngine) refreshTargetFundingEstimate(ctx context.Context, target *MonitoredTarget, force bool) error {
	if target == nil {
		return fmt.Errorf("target 为空")
	}
	if !force && !target.LastEstimateAt.IsZero() && time.Since(target.LastEstimateAt) < monitorEstimateRefreshInterval {
		return nil
	}

	item, err := e.fetchPremiumIndexBySymbol(ctx, target.Candidate.Symbol)
	if err != nil {
		return err
	}
	if e.cfg.MaxPremiumIndexStaleness > 0 {
		staleness := premiumIndexStaleness(e.serverNow(), item)
		if staleness > e.cfg.MaxPremiumIndexStaleness {
			return fmt.Errorf("premiumIndex 数据过期: age=%s limit=%s", staleness.Round(time.Millisecond), e.cfg.MaxPremiumIndexStaleness)
		}
	}

	estimatedRate, lastFundingRate, fundingJump, basisRate, scanScoreRate := estimateScanFundingSignal(item)
	target.Candidate.EstimatedRate = estimatedRate
	target.Candidate.EstimatedRateText = formatFloat(estimatedRate)
	target.Candidate.LastFundingRate = lastFundingRate
	target.Candidate.FundingRateJump = fundingJump
	target.Candidate.BasisRate = basisRate
	target.Candidate.ScanScoreRate = scanScoreRate
	target.Candidate.MarkPrice = parseFloat(item.MarkPrice)
	if nextFundingTime := time.UnixMilli(item.NextFundingTime); !nextFundingTime.IsZero() {
		target.Candidate.NextFundingTime = nextFundingTime
	}
	target.LastEstimateAt = time.Now()
	return nil
}

func (e *FundingRateSniperEngine) fetchDualBookTicker(ctx context.Context, spotSymbol string, futuresSymbol string) (BookTicker, BookTicker, error) {
	if e.spotTickerCache != nil && e.futuresTickerCache != nil {
		if spotTicker, ok := e.spotTickerCache.Get(spotSymbol); ok {
			if futuresTicker, ok := e.futuresTickerCache.Get(futuresSymbol); ok {
				return spotTicker, futuresTicker, nil
			}
		}
	}

	var (
		wg            sync.WaitGroup
		spotTicker    BookTicker
		futuresTicker BookTicker
		spotErr       error
		futuresErr    error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		spotErr = e.getJSON(ctx, e.client.spotBaseURL+"/api/v3/ticker/bookTicker?symbol="+spotSymbol, &spotTicker)
	}()
	go func() {
		defer wg.Done()
		futuresErr = e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/ticker/bookTicker?symbol="+futuresSymbol, &futuresTicker)
	}()
	wg.Wait()

	if spotErr != nil {
		return BookTicker{}, BookTicker{}, fmt.Errorf("现货盘口失败: %w", spotErr)
	}
	if futuresErr != nil {
		return BookTicker{}, BookTicker{}, fmt.Errorf("合约盘口失败: %w", futuresErr)
	}

	return spotTicker, futuresTicker, nil
}

func (e *FundingRateSniperEngine) fetchDualOrderBookDepth(ctx context.Context, spotSymbol string, futuresSymbol string) (ParsedOrderBookDepth, ParsedOrderBookDepth, error) {
	if e.spotDepthCache != nil && e.futuresDepthCache != nil {
		if spotDepth, ok := e.spotDepthCache.Get(spotSymbol); ok {
			if futuresDepth, ok := e.futuresDepthCache.Get(futuresSymbol); ok {
				return spotDepth, futuresDepth, nil
			}
		}
	}

	var (
		wg              sync.WaitGroup
		spotDepthRaw    OrderBookDepth
		futuresDepthRaw OrderBookDepth
		spotErr         error
		futuresErr      error
	)

	limit := strconv.Itoa(e.cfg.StrategyConfig.DepthLevels)
	wg.Add(2)
	go func() {
		defer wg.Done()
		spotErr = e.getJSON(ctx, e.client.spotBaseURL+"/api/v3/depth?symbol="+spotSymbol+"&limit="+limit, &spotDepthRaw)
	}()
	go func() {
		defer wg.Done()
		futuresErr = e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/depth?symbol="+futuresSymbol+"&limit="+limit, &futuresDepthRaw)
	}()
	wg.Wait()

	if spotErr != nil {
		return ParsedOrderBookDepth{}, ParsedOrderBookDepth{}, fmt.Errorf("现货深度失败: %w", spotErr)
	}
	if futuresErr != nil {
		return ParsedOrderBookDepth{}, ParsedOrderBookDepth{}, fmt.Errorf("合约深度失败: %w", futuresErr)
	}

	return ParsedOrderBookDepth{
			LastUpdateID: spotDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(spotDepthRaw.Bids),
			Asks:         parseDepthLevels(spotDepthRaw.Asks),
		}, ParsedOrderBookDepth{
			LastUpdateID: futuresDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(futuresDepthRaw.Bids),
			Asks:         parseDepthLevels(futuresDepthRaw.Asks),
		}, nil
}

func computeAverageFillPrice(levels []DepthLevel, requiredQty float64) (float64, float64, float64, bool) {
	if requiredQty <= 0 {
		return 0, 0, 0, false
	}
	if len(levels) == 0 {
		return 0, 0, 0, false
	}

	bestPrice := levels[0].Price
	remaining := requiredQty
	filledQty := 0.0
	totalCost := 0.0

	for _, level := range levels {
		if level.Price <= 0 || level.Quantity <= 0 {
			continue
		}

		takeQty := math.Min(remaining, level.Quantity)
		totalCost += takeQty * level.Price
		filledQty += takeQty
		remaining -= takeQty
		if remaining <= 1e-12 {
			break
		}
	}

	if filledQty <= 0 {
		return 0, bestPrice, 0, false
	}

	return totalCost / filledQty, bestPrice, filledQty, remaining <= 1e-12
}

func (e *FundingRateSniperEngine) applyMonitorObserveBreach(target *MonitoredTarget, consecutiveBreaches *int, netProfit float64, reason string) {
	*consecutiveBreaches = *consecutiveBreaches + 1
	target.BreachCount = *consecutiveBreaches
	target.LastNetProfit = netProfit
	e.transitionMonitorState(target, "observing", reason)
	if *consecutiveBreaches < e.cfg.RiskConfig.MonitorMaxConsecutiveBreach {
		return
	}
	e.invalidateMonitorTarget(target, netProfit, fmt.Sprintf("连续 %d 次触发观察阈值后作废: %s", e.cfg.RiskConfig.MonitorMaxConsecutiveBreach, reason))
}

func (e *FundingRateSniperEngine) invalidateMonitorTarget(target *MonitoredTarget, netProfit float64, reason string) {
	target.LastNetProfit = netProfit
	e.transitionMonitorState(target, "invalidated", reason)
	if atomic.CompareAndSwapInt32(&target.IsValid, 1, 0) {
		target.InvalidReason = reason
		e.logger.Printf("目标 %s 已作废: %s", target.Candidate.Symbol, target.InvalidReason)
	}
}

func (e *FundingRateSniperEngine) transitionMonitorState(target *MonitoredTarget, nextState string, detail string) {
	prevState := target.MonitorState
	if prevState == nextState {
		return
	}

	target.MonitorState = nextState
	e.logger.Printf(
		"模块三状态变更，symbol=%s %s->%s breach=%d net=%.6f spread=%.6f detail=%s",
		target.Candidate.Symbol,
		nonEmpty(prevState, "unknown"),
		nextState,
		target.BreachCount,
		target.LastNetProfit,
		target.LastSpread,
		nonEmpty(detail, "-"),
	)
}

func (e *FundingRateSniperEngine) recordExecutionIncident(phase string, symbol string, detail string) error {
	count := atomic.AddInt32(&e.executionIncidents, 1)
	message := fmt.Sprintf("执行事故，phase=%s symbol=%s count=%d detail=%s", nonEmpty(phase, "unknown"), nonEmpty(symbol, "-"), count, nonEmpty(detail, "-"))
	e.logger.Print(message)
	fatal := e.cfg.RiskConfig.MaxExecutionIncidents > 0 && int(count) >= e.cfg.RiskConfig.MaxExecutionIncidents
	e.emitRiskEvent(newRiskEvent("incident", RiskCategoryExecution, RiskSeverityWarning, phase, symbol, "incident", int(count), e.cfg.RiskConfig.MaxExecutionIncidents, fatal, detail))
	if fatal {
		return fatalEngineError{message: fmt.Sprintf("执行事故次数达到熔断阈值(%d)，已停止引擎: %s", e.cfg.RiskConfig.MaxExecutionIncidents, message)}
	}
	return nil
}

func (e *FundingRateSniperEngine) recordExecutionAction(phase string, symbol string, action string, detail string) {
	category := RiskCategoryExecution
	if strings.Contains(phase, "recovery") {
		category = RiskCategoryRecovery
	}
	e.emitRiskEvent(newRiskEvent("action", category, RiskSeverityInfo, phase, symbol, action, int(atomic.LoadInt32(&e.executionIncidents)), e.cfg.RiskConfig.MaxExecutionIncidents, false, detail))
	if strings.Contains(phase, "recovery") {
		e.noteRecoveryStatus(symbol, action, detail)
		if err := e.noteRecoveryAction(phase, symbol, action, detail); err != nil {
			e.logger.Printf("平台级风控已触发: %v", err)
		}
	}
}

func (e *FundingRateSniperEngine) emitRiskEvent(event RiskEvent) {
	runID, cycleID := e.traceIDs()
	e.emitExecutionEvent(event.WithTrace(runID, cycleID).WithMetadata(e.recordMetadata()).ExecutionRecord())
}

func (e *FundingRateSniperEngine) emitExecutionEvent(record ExecutionIncidentRecord) {
	if e.reportStore != nil {
		if err := e.reportStore.AppendExecutionIncident(record); err != nil {
			e.logger.Printf("写入执行事故记录失败: %v", err)
		}
	}
	if e.executionEventHook != nil {
		e.executionEventHook(record)
	}
}

func (e *FundingRateSniperEngine) logMonitorSummary(targets []*MonitoredTarget) {
	stateCounts := make(map[string]int)
	for _, target := range targets {
		state := nonEmpty(target.MonitorState, "unknown")
		stateCounts[state]++
		e.logger.Printf(
			"模块三摘要，symbol=%s state=%s valid=%t breach=%d net=%.6f spread=%.6f reason=%s",
			target.Candidate.Symbol,
			state,
			atomic.LoadInt32(&target.IsValid) == 1,
			target.BreachCount,
			target.LastNetProfit,
			target.LastSpread,
			nonEmpty(target.InvalidReason, "-"),
		)
	}

	e.logger.Printf(
		"模块三状态汇总，healthy=%d observing=%d invalidated=%d watching=%d unknown=%d",
		stateCounts["healthy"],
		stateCounts["observing"],
		stateCounts["invalidated"],
		stateCounts["watching"],
		stateCounts["unknown"],
	)
}

func (e *FundingRateSniperEngine) placeFuturesBatchOrders(ctx context.Context, orders []FuturesBatchOrderRequest) ([]FuturesBatchOrderResult, error) {
	raw, err := json.Marshal(orders)
	if err != nil {
		return nil, fmt.Errorf("序列化合约批量订单失败: %w", err)
	}

	params := url.Values{}
	params.Set("batchOrders", string(raw))

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.futuresBaseURL+"/fapi/v1/batchOrders", params)
	if err != nil {
		return nil, err
	}

	var result []FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析合约批量下单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeFuturesBatchOrdersIdempotent(ctx context.Context, orders []FuturesBatchOrderRequest) ([]FuturesBatchOrderResult, error) {
	result, err := e.placeFuturesBatchOrders(ctx, orders)
	if err == nil {
		return result, nil
	}
	e.noteOrderUncertainty("-", "futures_batch_fallback", fmt.Sprintf("orders=%d err=%v", len(orders), err))

	recovered := make([]FuturesBatchOrderResult, 0, len(orders))
	recoveredCount := 0
	for _, order := range orders {
		if order.NewClientOrderID == "" {
			recovered = append(recovered, FuturesBatchOrderResult{
				Symbol: order.Symbol,
				Msg:    "缺少 clientOrderId，无法幂等回查",
			})
			continue
		}

		queried, queryErr := e.queryFuturesOrderByClientID(ctx, order.Symbol, order.NewClientOrderID)
		if queryErr != nil {
			recovered = append(recovered, FuturesBatchOrderResult{
				Symbol:        order.Symbol,
				ClientOrderID: order.NewClientOrderID,
				Msg:           queryErr.Error(),
			})
			continue
		}

		recoveredCount++
		recovered = append(recovered, queried)
	}

	if recoveredCount > 0 {
		e.logger.Printf("合约批量下单出现不确定状态，已通过 clientOrderId 回查恢复 %d/%d 条订单", recoveredCount, len(orders))
		return recovered, nil
	}

	return nil, err
}

func (e *FundingRateSniperEngine) placeSingleFuturesClose(ctx context.Context, symbol string, quantity float64) error {
	_, err := e.placeSingleFuturesCloseConfirmed(ctx, symbol, quantity)
	return err
}

func (e *FundingRateSniperEngine) placeSingleFuturesCloseConfirmed(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, "BUY", quantity, true, newClientOrderID("ftrim", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesReopenShort(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, "SELL", quantity, false, newClientOrderID("frescue", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesMarketOrderIdempotent(ctx context.Context, symbol string, side string, quantity float64, reduceOnly bool, clientOrderID string) (OrderExecutionSnapshot, error) {
	if quantity <= executionQtyTolerance {
		return OrderExecutionSnapshot{}, fmt.Errorf("quantity 必须大于 0")
	}
	params := url.Values{}
	params, err := e.futuresProtectedOrderParams(ctx, symbol, side, quantity, reduceOnly, clientOrderID)
	if err != nil {
		return OrderExecutionSnapshot{}, err
	}
	if err := e.noteOrderPlaced("futures_market_"+strings.ToLower(side), fmt.Sprintf("%s qty=%s reduceOnly=%t", symbol, formatFloat(quantity), reduceOnly), 1, !reduceOnly); err != nil {
		return OrderExecutionSnapshot{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.futuresOrderRequestPath(), params)
	if err != nil {
		if clientOrderID == "" {
			return OrderExecutionSnapshot{}, err
		}
		e.noteOrderUncertainty(symbol, "futures_market_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
		waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
		defer cancel()
		if snapshot, ok := e.waitForFuturesOrder(waitCtx, symbol, clientOrderID); ok && snapshot.ExecutedQty > 0 {
			return snapshot, nil
		}
		queried, queryErr := e.queryFuturesOrderByClientID(ctx, symbol, clientOrderID)
		if queryErr != nil {
			return OrderExecutionSnapshot{}, err
		}
		return e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, queried)
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return OrderExecutionSnapshot{}, fmt.Errorf("解析合约单笔市价单回执失败: %w", err)
	}
	return e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, result)
}

func (e *FundingRateSniperEngine) placeSpotMarketBuyByQuantityIdempotent(ctx context.Context, symbol string, quantity float64, clientOrderID string) (SpotOrderResponse, error) {
	params, err := e.spotProtectedOrderParams(ctx, symbol, "BUY", quantity, clientOrderID)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if err := e.noteOrderPlaced("spot_market_buy", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, true); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" {
			e.noteOrderUncertainty(symbol, "spot_buy_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
			defer cancel()
			if snapshot, ok := e.waitForSpotOrder(waitCtx, symbol, clientOrderID); ok && snapshot.OrderID != 0 {
				return e.spotSnapshotToOrderResponse(symbol, snapshot), nil
			}
		}
		if clientOrderID == "" {
			return SpotOrderResponse{}, err
		}

		queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
		if queryErr == nil && queried.OrderID != 0 {
			e.logger.Printf("现货买单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
			return queried, nil
		}

		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货买单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeSpotMarketSell(ctx context.Context, symbol string, quantity string) (SpotOrderResponse, error) {
	qtyValue := parseFloat(quantity)
	params, err := e.spotProtectedOrderParams(ctx, symbol, "SELL", qtyValue, "")
	if err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货卖单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeSpotMarketSellIdempotent(ctx context.Context, symbol string, quantity float64, clientOrderID string) (SpotOrderResponse, error) {
	params, err := e.spotProtectedOrderParams(ctx, symbol, "SELL", quantity, clientOrderID)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if err := e.noteOrderPlaced("spot_market_sell", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, false); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" {
			e.noteOrderUncertainty(symbol, "spot_sell_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
			defer cancel()
			if snapshot, ok := e.waitForSpotOrder(waitCtx, symbol, clientOrderID); ok && snapshot.OrderID != 0 {
				return e.spotSnapshotToOrderResponse(symbol, snapshot), nil
			}
		}
		if clientOrderID == "" {
			return SpotOrderResponse{}, err
		}

		queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
		if queryErr == nil && queried.OrderID != 0 {
			e.logger.Printf("现货卖单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
			return queried, nil
		}

		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货卖单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) spotSnapshotToOrderResponse(symbol string, snapshot OrderExecutionSnapshot) SpotOrderResponse {
	response := SpotOrderResponse{
		Symbol:              symbol,
		OrderID:             snapshot.OrderID,
		Status:              snapshot.Status,
		ClientOrderID:       snapshot.ClientOrderID,
		ExecutedQty:         formatFloat(snapshot.ExecutedQty),
		CummulativeQuoteQty: formatFloat(snapshot.CumQuoteQty),
		TransactTime:        snapshot.EventTime.UnixMilli(),
	}
	if snapshot.ExecutedQty > 0 {
		response.OrigQty = formatFloat(snapshot.ExecutedQty)
	}
	if snapshot.Commission > 0 || strings.TrimSpace(snapshot.CommissionAsset) != "" {
		response.Fills = []SpotFill{{
			Price:           formatFloat(snapshot.AvgPrice),
			Qty:             formatFloat(snapshot.ExecutedQty),
			Commission:      formatFloat(snapshot.Commission),
			CommissionAsset: snapshot.CommissionAsset,
		}}
	}
	return response
}

func (e *FundingRateSniperEngine) settleFundingIncome(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingTime time.Time) (float64, float64, []string) {
	startTime := fundingTime.Add(-1 * time.Minute)
	endTime := fundingTime.Add(2 * time.Minute)
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	totalFundingIncome := 0.0
	expectedFundingIncome := 0.0
	alerts := make([]string, 0)
	for symbol := range symbolSet {
		beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
		if math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
			alerts = append(alerts, fmt.Sprintf("资金费归因已跳过 %s: 开仓前已有持仓 positionAmt=%.10f", symbol, beforeQty))
			continue
		}

		income, err := e.fetchFundingIncome(ctx, startTime, endTime, symbol)
		if err != nil {
			alerts = append(alerts, fmt.Sprintf("资金费查询失败 %s: %v", symbol, err))
			continue
		}

		actualFundingIncome := 0.0
		for _, item := range income {
			if item.Symbol != symbol {
				continue
			}
			actualFundingIncome += parseFloat(item.Income)
		}

		totalFundingIncome += actualFundingIncome
		expectedFundingIncome += expectedFundingBySymbol[symbol]
		diff := actualFundingIncome - expectedFundingBySymbol[symbol]
		if math.Abs(diff) >= e.cfg.RiskConfig.FundingIncomeDiffAlertUSDT {
			alerts = append(alerts, fmt.Sprintf("资金费到账偏差过大 %s: 实收=%.6f 理论=%.6f 偏差=%.6f", symbol, actualFundingIncome, expectedFundingBySymbol[symbol], diff))
		}
	}

	return totalFundingIncome, expectedFundingIncome, alerts
}

func (e *FundingRateSniperEngine) fetchFundingIncome(ctx context.Context, startTime time.Time, endTime time.Time, symbol string) ([]FundingIncomeRecord, error) {
	params := url.Values{}
	params.Set("incomeType", "FUNDING_FEE")
	params.Set("startTime", strconv.FormatInt(startTime.UnixMilli(), 10))
	params.Set("endTime", strconv.FormatInt(endTime.UnixMilli(), 10))
	params.Set("limit", "100")
	if strings.TrimSpace(symbol) != "" {
		params.Set("symbol", symbol)
	}

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.futuresBaseURL+"/fapi/v1/income", params)
	if err != nil {
		return nil, err
	}

	var result []FundingIncomeRecord
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析资金费流水失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) queryFuturesOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.futuresBaseURL+"/fapi/v1/order", params)
	if err != nil {
		return FuturesBatchOrderResult{}, err
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return FuturesBatchOrderResult{}, fmt.Errorf("解析合约订单查询回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) cancelFuturesOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodDelete, e.client.futuresBaseURL+"/fapi/v1/order", params)
	if err != nil {
		return FuturesBatchOrderResult{}, err
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return FuturesBatchOrderResult{}, fmt.Errorf("解析合约撤单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) querySpotOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.spotBaseURL+"/api/v3/order", params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货订单查询回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) resolveFuturesOrderExecution(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) (OrderExecutionSnapshot, error) {
	result := initial
	if clientOrderID != "" {
		waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
		defer cancel()
		if snapshot, ok := e.waitForFuturesOrder(waitCtx, symbol, clientOrderID); ok && snapshot.ExecutedQty > 0 {
			return snapshot, nil
		}
		e.noteOrderUncertainty(symbol, "futures_order_confirm_timeout", fmt.Sprintf("clientOrderId=%s", clientOrderID))
	}
	for range 3 {
		filledQty := parseFloat(result.ExecutedQty)
		if filledQty > 0 {
			return futuresResultToSnapshot(result), nil
		}

		if clientOrderID == "" {
			break
		}

		time.Sleep(100 * time.Millisecond)
		queried, err := e.queryFuturesOrderByClientID(ctx, symbol, clientOrderID)
		if err != nil {
			continue
		}
		result = queried
	}

	return OrderExecutionSnapshot{}, fmt.Errorf("未能确认合约订单真实成交数量, symbol=%s clientOrderId=%s", symbol, clientOrderID)
}

func (e *FundingRateSniperEngine) flattenUncertainFuturesOpen(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) {
	result := initial
	if clientOrderID != "" {
		if queried, err := e.queryFuturesOrderByClientID(ctx, symbol, clientOrderID); err == nil {
			result = queried
		} else {
			e.logger.Printf("合约空单兜底回查失败，symbol=%s clientOrderId=%s err=%v", symbol, clientOrderID, err)
		}
	}

	if clientOrderID != "" && !isClosedOrderStatus(result.Status) {
		canceled, err := e.cancelFuturesOrderByClientID(ctx, symbol, clientOrderID)
		if err != nil {
			e.logger.Printf("合约空单兜底撤单失败，symbol=%s clientOrderId=%s err=%v", symbol, clientOrderID, err)
		} else {
			result = canceled
		}
	}

	closeQty := parseFloat(result.ExecutedQty)
	if closeQty <= 0 {
		e.logger.Printf("合约空单兜底完成，未发现已成交数量，symbol=%s clientOrderId=%s status=%s", symbol, clientOrderID, result.Status)
		return
	}
	if _, err := e.placeSingleFuturesCloseConfirmed(ctx, symbol, closeQty); err != nil {
		e.logger.Printf("合约空单兜底平仓失败，symbol=%s qty=%.8f err=%v", symbol, closeQty, err)
		return
	}
	e.logger.Printf("合约空单兜底平仓成功，symbol=%s qty=%.8f", symbol, closeQty)
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshot(ctx context.Context, targets []*MonitoredTarget) (CycleAccountSnapshot, error) {
	pairs := make([]string, 0, len(targets))
	symbols := make([]string, 0, len(targets))
	for _, target := range targets {
		pairs = append(pairs, target.Candidate.Pair)
		symbols = append(symbols, target.Candidate.Symbol)
	}
	return e.captureCycleAccountSnapshotForSymbols(ctx, pairs, symbols, []string{"BNB"})
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshotFromHedges(ctx context.Context, hedges []OpenHedge) (CycleAccountSnapshot, error) {
	pairs := make([]string, 0, len(hedges))
	symbols := make([]string, 0, len(hedges))
	extraAssets := []string{"BNB"}
	seenAssets := map[string]struct{}{"BNB": {}}
	for _, hedge := range hedges {
		pairs = append(pairs, hedge.Target.Pair)
		symbols = append(symbols, hedge.Target.Symbol)
		for _, fill := range append([]SpotFill{}, hedge.SpotOrder.Fills...) {
			asset := strings.ToUpper(strings.TrimSpace(fill.CommissionAsset))
			if asset == "" {
				continue
			}
			if _, ok := seenAssets[asset]; ok {
				continue
			}
			seenAssets[asset] = struct{}{}
			extraAssets = append(extraAssets, asset)
		}
	}
	return e.captureCycleAccountSnapshotForSymbols(ctx, pairs, symbols, extraAssets)
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshotForSymbols(ctx context.Context, pairs []string, symbols []string, extraAssets []string) (CycleAccountSnapshot, error) {
	spotBalances, spotUSDT, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return CycleAccountSnapshot{}, err
	}

	futuresWallet, futuresAvailable, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return CycleAccountSnapshot{}, err
	}

	snapshot := CycleAccountSnapshot{
		CapturedAt:             time.Now(),
		SpotUSDTFree:           spotUSDT,
		SpotAssetBalances:      make(map[string]float64),
		FuturesUSDTWallet:      futuresWallet,
		FuturesUSDTAvailable:   futuresAvailable,
		FuturesPositionAmounts: make(map[string]float64),
	}

	for _, pair := range pairs {
		meta, ok := e.spotMeta[pair]
		if !ok {
			continue
		}
		snapshot.SpotAssetBalances[meta.BaseAsset] = spotBalances[meta.BaseAsset]
	}
	for _, symbol := range symbols {
		snapshot.FuturesPositionAmounts[symbol] = positions[symbol]
	}
	for _, asset := range extraAssets {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" {
			continue
		}
		if _, ok := snapshot.SpotAssetBalances[asset]; ok {
			continue
		}
		snapshot.SpotAssetBalances[asset] = spotBalances[asset]
	}

	return snapshot, nil
}

func (e *FundingRateSniperEngine) verifyTradingEnvironment(ctx context.Context) error {
	dualSide, err := e.fetchFuturesPositionMode(ctx)
	if err != nil {
		return fmt.Errorf("校验合约持仓模式失败: %w", err)
	}
	if dualSide {
		return fmt.Errorf("当前合约账户仍处于双向持仓模式，程序要求单向持仓模式")
	}

	spotBalances, _, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货账户状态失败: %w", err)
	}
	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return fmt.Errorf("读取合约账户状态失败: %w", err)
	}

	futuresIssues := make([]string, 0)
	for symbol, qty := range positions {
		if math.Abs(qty) <= e.cfg.ResidualPositionAlertQty {
			continue
		}
		futuresIssues = append(futuresIssues, fmt.Sprintf("%s=%.8f", symbol, qty))
	}
	if len(futuresIssues) > 0 {
		sort.Strings(futuresIssues)
		return fmt.Errorf("检测到存量合约仓位，拒绝启动: %s", strings.Join(futuresIssues, ", "))
	}

	spotIssues := make([]string, 0)
	for asset, balance := range spotBalances {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" || asset == "USDT" || asset == "BNB" {
			continue
		}
		if balance <= e.cfg.ResidualPositionAlertQty {
			continue
		}
		assetPrice, priceErr := e.fetchSpotAssetUSDTPrice(ctx, asset)
		if priceErr != nil || assetPrice <= 0 {
			continue
		}
		notionalUSDT := balance * assetPrice
		if notionalUSDT < e.cfg.ResidualSpotAlertUSDT {
			continue
		}
		spotIssues = append(spotIssues, fmt.Sprintf("%s=%.8f(%.6fUSDT)", asset, balance, notionalUSDT))
	}
	if len(spotIssues) > 0 {
		sort.Strings(spotIssues)
		return fmt.Errorf("检测到存量现货仓位，拒绝启动: %s", strings.Join(spotIssues, ", "))
	}

	e.logger.Printf("模块一环境校验通过：单向持仓、无显著残余现货/合约仓位")
	return nil
}

func (e *FundingRateSniperEngine) fetchFuturesPositionMode(ctx context.Context) (bool, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.futuresBaseURL+"/fapi/v1/positionSide/dual", nil)
	if err != nil {
		return false, err
	}

	var payload struct {
		DualSidePosition any `json:"dualSidePosition"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("解析合约持仓模式失败: %w", err)
	}
	switch typed := payload.DualSidePosition.(type) {
	case bool:
		return typed, nil
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true"), nil
	default:
		return false, fmt.Errorf("未知持仓模式字段: %v", payload.DualSidePosition)
	}
}

func (e *FundingRateSniperEngine) fetchSpotAccountBalances(ctx context.Context) (map[string]float64, float64, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.spotBaseURL+"/api/v3/account", nil)
	if err != nil {
		return nil, 0, err
	}

	var payload struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, fmt.Errorf("解析现货账户快照失败: %w", err)
	}

	balances := make(map[string]float64, len(payload.Balances))
	spotUSDT := 0.0
	for _, balance := range payload.Balances {
		total := parseFloat(balance.Free) + parseFloat(balance.Locked)
		balances[balance.Asset] = total
		if balance.Asset == "USDT" {
			spotUSDT = parseFloat(balance.Free)
		}
	}

	return balances, spotUSDT, nil
}

func (e *FundingRateSniperEngine) fetchFuturesAccountState(ctx context.Context) (float64, float64, map[string]float64, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.futuresBaseURL+"/fapi/v3/account", nil)
	if err != nil {
		return 0, 0, nil, err
	}

	var payload struct {
		Assets []struct {
			Asset            string `json:"asset"`
			WalletBalance    string `json:"walletBalance"`
			AvailableBalance string `json:"availableBalance"`
		} `json:"assets"`
		Positions []struct {
			Symbol      string `json:"symbol"`
			PositionAmt string `json:"positionAmt"`
		} `json:"positions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, 0, nil, fmt.Errorf("解析合约账户快照失败: %w", err)
	}

	walletUSDT, availableUSDT := 0.0, 0.0
	for _, asset := range payload.Assets {
		if asset.Asset != "USDT" {
			continue
		}
		walletUSDT = parseFloat(asset.WalletBalance)
		availableUSDT = parseFloat(asset.AvailableBalance)
		break
	}

	positions := make(map[string]float64, len(payload.Positions))
	for _, position := range payload.Positions {
		positions[position.Symbol] = parseFloat(position.PositionAmt)
	}

	return walletUSDT, availableUSDT, positions, nil
}

func futuresResultToSnapshot(result FuturesBatchOrderResult) OrderExecutionSnapshot {
	snapshot := OrderExecutionSnapshot{
		Market:        "futures",
		Symbol:        result.Symbol,
		ClientOrderID: result.ClientOrderID,
		OrderID:       result.OrderID,
		Status:        result.Status,
		ExecutedQty:   parseFloat(result.ExecutedQty),
		AvgPrice:      parseFloat(result.AvgPrice),
		EventTime:     time.UnixMilli(result.UpdateTime),
	}
	if snapshot.ExecutedQty > 0 && snapshot.AvgPrice > 0 {
		snapshot.CumQuoteQty = snapshot.ExecutedQty * snapshot.AvgPrice
	}
	return snapshot
}

func (e *FundingRateSniperEngine) estimateSpotCommissionUSDT(ctx context.Context, order SpotOrderResponse) float64 {
	avgPrice := 0.0
	executedQty := parseFloat(order.ExecutedQty)
	quoteQty := parseFloat(order.CummulativeQuoteQty)
	if executedQty > 0 && quoteQty > 0 {
		avgPrice = quoteQty / executedQty
	}

	total := 0.0
	for _, fill := range order.Fills {
		commission := parseFloat(fill.Commission)
		commissionAsset := strings.ToUpper(fill.CommissionAsset)
		switch commissionAsset {
		case "", "USDT":
			total += commission
		default:
			if meta, ok := e.spotMeta[order.Symbol]; ok && commissionAsset == strings.ToUpper(meta.BaseAsset) && avgPrice > 0 {
				total += commission * avgPrice
				continue
			}
			assetPrice, err := e.fetchSpotAssetUSDTPrice(ctx, commissionAsset)
			if err == nil && assetPrice > 0 {
				total += commission * assetPrice
			}
		}
	}
	return total
}

func (e *FundingRateSniperEngine) estimateFuturesExecutionFeeUSDT(ctx context.Context, snapshot OrderExecutionSnapshot) float64 {
	if snapshot.OrderID > 0 {
		total, err := e.fetchFuturesTradeCommissionUSDT(ctx, snapshot.Symbol, snapshot.OrderID, snapshot.AvgPrice)
		if err == nil && total > 0 {
			return total
		}
		if err != nil {
			e.logger.Printf("查询合约成交手续费失败，symbol=%s orderId=%d err=%v", snapshot.Symbol, snapshot.OrderID, err)
		}
	}
	return e.convertCommissionToUSDT(ctx, snapshot.Symbol, snapshot.CommissionAsset, snapshot.Commission, snapshot.AvgPrice)
}

func (e *FundingRateSniperEngine) fetchFuturesTradeCommissionUSDT(ctx context.Context, symbol string, orderID int64, referencePrice float64) (float64, error) {
	if strings.TrimSpace(symbol) == "" || orderID == 0 {
		return 0, nil
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	params.Set("limit", "1000")
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.futuresBaseURL+"/fapi/v1/userTrades", params)
	if err != nil {
		return 0, err
	}

	var payload []struct {
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("解析合约成交手续费失败: %w", err)
	}

	total := 0.0
	for _, trade := range payload {
		total += e.convertCommissionToUSDT(ctx, symbol, trade.CommissionAsset, parseFloat(trade.Commission), referencePrice)
	}
	return total, nil
}

func (e *FundingRateSniperEngine) convertCommissionToUSDT(ctx context.Context, symbol string, commissionAsset string, commission float64, referencePrice float64) float64 {
	if commission <= 0 {
		return 0
	}
	switch strings.ToUpper(strings.TrimSpace(commissionAsset)) {
	case "", "USDT":
		return commission
	default:
		if meta, ok := e.spotMeta[symbol]; ok && strings.EqualFold(meta.BaseAsset, commissionAsset) && referencePrice > 0 {
			return commission * referencePrice
		}
		if meta, ok := e.futuresMeta[symbol]; ok {
			if spotMeta, spotOK := e.spotMeta[meta.Pair]; spotOK && strings.EqualFold(spotMeta.BaseAsset, commissionAsset) && referencePrice > 0 {
				return commission * referencePrice
			}
		}
		assetPrice, err := e.fetchSpotAssetUSDTPrice(ctx, commissionAsset)
		if err == nil && assetPrice > 0 {
			return commission * assetPrice
		}
		return 0
	}
}

func estimateExpectedFundingIncome(hedge OpenHedge) float64 {
	referencePrice := hedge.OpenFuturesAvgPrice
	if referencePrice <= 0 {
		referencePrice = hedge.Target.MarkPrice
	}
	if referencePrice <= 0 || hedge.HedgeQty <= 0 || hedge.Target.EstimatedRate == 0 {
		return 0
	}

	return referencePrice * hedge.HedgeQty * hedge.Target.EstimatedRate
}

func calculateResidualSpotValue(balances map[string]float64, hedges []OpenHedge) float64 {
	priceByAsset := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		priceByAsset[hedge.Target.SpotBaseAsset] = hedge.LastKnownSpotPrice()
	}

	total := 0.0
	for asset, balance := range balances {
		if balance <= 0 {
			continue
		}
		price := priceByAsset[asset]
		if price <= 0 {
			continue
		}
		total += balance * price
	}
	return total
}

func (e *FundingRateSniperEngine) fetchSpotAssetUSDTPrice(ctx context.Context, asset string) (float64, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" {
		return 0, fmt.Errorf("手续费资产为空")
	}
	if asset == "USDT" {
		return 1, nil
	}

	symbol := asset + "USDT"
	ticker := BookTicker{}
	if err := e.getJSON(ctx, e.client.spotBaseURL+"/api/v3/ticker/bookTicker?symbol="+symbol, &ticker); err != nil {
		return 0, err
	}

	price := parseFloat(nonEmpty(ticker.AskPrice, ticker.BidPrice))
	if price <= 0 {
		return 0, fmt.Errorf("无法获取 %s 的 USDT 价格", asset)
	}
	return price, nil
}

func (e *FundingRateSniperEngine) buildSettlementAlerts(afterSnapshot CycleAccountSnapshot, hedges []OpenHedge, residualSpotValue float64, netPnL float64, snapshotNetPnL float64) []string {
	alerts := make([]string, 0)

	if math.Abs(residualSpotValue) >= e.cfg.ResidualSpotAlertUSDT {
		alerts = append(alerts, fmt.Sprintf("现货残余价值过高: %.6f USDT", residualSpotValue))
	}

	for symbol, qty := range afterSnapshot.FuturesPositionAmounts {
		if math.Abs(qty) < e.cfg.ResidualPositionAlertQty {
			continue
		}
		alerts = append(alerts, fmt.Sprintf("合约残仓未清零: %s positionAmt=%.10f", symbol, qty))
	}

	for asset, balance := range afterSnapshot.SpotAssetBalances {
		if strings.EqualFold(asset, "USDT") || strings.EqualFold(asset, "BNB") {
			continue
		}
		if balance <= 0 {
			continue
		}
		assetPrice := latestSpotAssetPriceFromHedges(asset, hedges)
		if assetPrice <= 0 {
			continue
		}
		if balance*assetPrice < e.cfg.ResidualSpotAlertUSDT {
			continue
		}
		alerts = append(alerts, fmt.Sprintf("现货残仓未清零: %s balance=%.10f value=%.6f USDT", asset, balance, balance*assetPrice))
	}

	diff := math.Abs(netPnL - snapshotNetPnL)
	if diff >= e.cfg.SettlementDiffAlertUSDT {
		alerts = append(alerts, fmt.Sprintf("交易口径与账户口径偏差过大: diff=%.6f USDT", diff))
	}

	return alerts
}

func latestSpotAssetPriceFromHedges(asset string, hedges []OpenHedge) float64 {
	for _, hedge := range hedges {
		if !strings.EqualFold(asset, hedge.Target.SpotBaseAsset) {
			continue
		}
		price := hedge.LastKnownSpotPrice()
		if price > 0 {
			return price
		}
	}
	return 0
}

func resolveSpotNetBaseQuantity(order SpotOrderResponse, baseAsset string) float64 {
	executedQty := parseFloat(order.ExecutedQty)
	if executedQty <= 0 {
		return 0
	}

	netQty := executedQty
	for _, fill := range order.Fills {
		if !strings.EqualFold(fill.CommissionAsset, baseAsset) {
			continue
		}
		netQty -= parseFloat(fill.Commission)
	}

	if netQty < 0 {
		return 0
	}

	return netQty
}

func (e *FundingRateSniperEngine) signedRequest(ctx context.Context, method string, endpoint string, params url.Values) ([]byte, error) {
	if err := e.checkPlatformFuse(); err != nil {
		return nil, err
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("recvWindow", strconv.FormatInt(e.cfg.ExecutionConfig.RecvWindow, 10))
	params.Set("timestamp", strconv.FormatInt(e.currentServerTimeMillis(), 10))

	query := params.Encode()
	mac := hmac.New(sha256.New, []byte(e.client.secretKey))
	_, _ = mac.Write([]byte(query))
	signature := hex.EncodeToString(mac.Sum(nil))
	query = query + "&signature=" + signature

	req, err := http.NewRequestWithContext(ctx, method, endpoint+"?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("创建签名请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", e.client.apiKey)

	resp, err := e.client.httpClient.Do(req)
	if err != nil {
		requestErr := fmt.Errorf("发送签名请求失败: %w", err)
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		requestErr := fmt.Errorf("读取签名响应失败: %w", err)
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}

	if resp.StatusCode != http.StatusOK {
		requestErr := fmt.Errorf("签名请求失败: status=%s body=%s", resp.Status, string(body))
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	e.notePlatformRequestSuccess()

	return body, nil
}

func (e *FundingRateSniperEngine) syncServerTimeOffset(ctx context.Context) error {
	var payload struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/time", &payload); err != nil {
		return fmt.Errorf("同步服务器时间失败: %w", err)
	}

	e.serverTimeOffset = time.Until(time.UnixMilli(payload.ServerTime))
	return nil
}

func (e *FundingRateSniperEngine) currentServerTimeMillis() int64 {
	return e.serverNow().UnixMilli()
}

func (e *FundingRateSniperEngine) serverNow() time.Time {
	return time.Now().Add(e.serverTimeOffset)
}

func (e *FundingRateSniperEngine) sleepUntilServerTime(ctx context.Context, target time.Time) error {
	for {
		if err := e.checkLiveSafetyGuards("sleep_until_server_time"); err != nil {
			return err
		}
		if !target.After(e.serverNow()) {
			return nil
		}
		waitUntil := target.Add(-e.serverTimeOffset)
		remaining := time.Until(waitUntil)
		if remaining <= 0 {
			return nil
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func estimateScanFundingSignal(item PremiumIndexItem) (float64, float64, float64, float64, float64) {
	lastFundingRate := parseFloat(item.LastFundingRate)
	interestRate := parseFloat(item.InterestRate)
	markPrice := parseFloat(item.MarkPrice)
	indexPrice := parseFloat(item.IndexPrice)
	estimatedSettlePrice := parseFloat(item.EstimatedSettlePrice)

	basisRate := 0.0
	if markPrice > 0 && indexPrice > 0 {
		basisRate = (markPrice - indexPrice) / indexPrice
	}

	premiumEstimate := basisRate
	if estimatedSettlePrice > 0 && indexPrice > 0 {
		settleBasisRate := (estimatedSettlePrice - indexPrice) / indexPrice
		if settleBasisRate > 0 {
			premiumEstimate = settleBasisRate
		}
	}

	estimatedFundingRate := lastFundingRate
	conservativeRate := math.Max(0, premiumEstimate+interestRate)
	if conservativeRate > 0 {
		estimatedFundingRate = math.Min(lastFundingRate, conservativeRate)
	}
	if estimatedFundingRate < 0 {
		estimatedFundingRate = 0
	}

	fundingJump := math.Abs(estimatedFundingRate - lastFundingRate)
	scanScoreRate := estimatedFundingRate - math.Max(basisRate, 0)
	return estimatedFundingRate, lastFundingRate, fundingJump, basisRate, scanScoreRate
}

func premiumIndexStaleness(now time.Time, item PremiumIndexItem) time.Duration {
	if item.Time <= 0 {
		return 0
	}
	age := now.Sub(time.UnixMilli(item.Time))
	if age < 0 {
		return 0
	}
	return age
}

func estimateTargetCapitalRequirement(cfg EngineConfig, target *MonitoredTarget, spotReferencePrice float64, futuresReferencePrice float64) (float64, float64) {
	if target == nil || target.Candidate.OrderQuantity <= 0 {
		return 0, 0
	}
	if spotReferencePrice <= 0 {
		spotReferencePrice = target.Candidate.MarkPrice
	}
	if futuresReferencePrice <= 0 {
		futuresReferencePrice = target.Candidate.MarkPrice
	}
	if spotReferencePrice <= 0 || futuresReferencePrice <= 0 {
		return 0, 0
	}

	spotFeeRate := cfg.HistoricalConfig.SpotTakerFeeRate
	if spotFeeRate <= 0 {
		spotFeeRate = 0.0010
	}
	futuresFeeRate := cfg.HistoricalConfig.FuturesTakerFeeRate
	if futuresFeeRate <= 0 {
		futuresFeeRate = 0.0005
	}
	spotRequiredUSDT := target.Candidate.OrderQuantity * spotReferencePrice * (1 + spotFeeRate)
	futuresRequiredUSDT := target.Candidate.OrderQuantity * futuresReferencePrice * (1 + futuresFeeRate)
	return spotRequiredUSDT, futuresRequiredUSDT
}

func (e *FundingRateSniperEngine) getJSON(ctx context.Context, endpoint string, target any) error {
	if err := e.checkPlatformFuse(); err != nil {
		return err
	}
	if err := e.client.getJSON(ctx, endpoint, target); err != nil {
		if markErr := e.notePlatformRequestResult(endpoint, err); markErr != nil {
			return markErr
		}
		return err
	}
	e.notePlatformRequestSuccess()
	return nil
}

func (e *FundingRateSniperEngine) constrainOrderQuantityByMarketCapacity(ctx context.Context, target *MonitoredTarget, spotReferencePrice float64, futuresReferencePrice float64) (float64, string, error) {
	if target == nil || target.Candidate.OrderQuantity <= 0 {
		return 0, "target_or_quantity_invalid", nil
	}

	referencePrice := math.Max(spotReferencePrice, futuresReferencePrice)
	if referencePrice <= 0 {
		referencePrice = target.Candidate.MarkPrice
	}
	if referencePrice <= 0 {
		return 0, "reference_price_invalid", fmt.Errorf("参考价格无效")
	}

	desiredQty := target.Candidate.OrderQuantity
	desiredNotional := desiredQty * referencePrice
	snapshot, err := e.collectMarketCapacitySnapshot(ctx, target, futuresReferencePrice)
	if err != nil && snapshot.AvailableMetrics == 0 {
		return 0, "", err
	}

	limitNotional := math.Inf(1)
	profile := e.symbolRiskProfile(target.Candidate.Symbol)
	reasons := []string{fmt.Sprintf("desiredNotional=%.6f", desiredNotional)}
	if snapshot.FuturesOpenInterestNotional > 0 && e.cfg.MaxOpenInterestShare > 0 {
		capNotional := snapshot.FuturesOpenInterestNotional * e.cfg.MaxOpenInterestShare * profile.MaxOpenInterestShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("openInterestNotional=%.6f cap=%.6f", snapshot.FuturesOpenInterestNotional, capNotional))
	}
	if recentMin := minPositiveFloat(snapshot.RecentSpotQuoteVolume, snapshot.RecentFuturesQuoteVolume); recentMin > 0 && e.cfg.MaxRecentVolumeShare > 0 {
		capNotional := recentMin * e.cfg.MaxRecentVolumeShare * profile.MaxRecentVolumeShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("recentQuoteVolume=%.6f cap=%.6f", recentMin, capNotional))
	}
	if visibleMin := minPositiveFloat(snapshot.VisibleSpotAskNotional, snapshot.VisibleFuturesBidNotional); visibleMin > 0 && e.cfg.MaxVisibleDepthShare > 0 {
		capNotional := visibleMin * e.cfg.MaxVisibleDepthShare * profile.MaxVisibleDepthShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("visibleDepthNotional=%.6f cap=%.6f", visibleMin, capNotional))
	}

	if math.IsInf(limitNotional, 1) {
		if snapshot.AvailableMetrics == 0 {
			return 0, strings.Join(reasons, " "), fmt.Errorf("市场容量指标不可用")
		}
		return desiredQty, strings.Join(reasons, " "), nil
	}
	if limitNotional <= 0 {
		return 0, strings.Join(reasons, " "), nil
	}
	if desiredNotional <= limitNotional+1e-9 {
		return desiredQty, strings.Join(reasons, " "), nil
	}

	futuresRule := e.futuresRules[target.Candidate.Symbol]
	limitedQty := normalizeQuantity(limitNotional/referencePrice, futuresRule, target.Candidate.QuantityPrecision)
	return limitedQty, strings.Join(reasons, " "), nil
}

func (e *FundingRateSniperEngine) collectMarketCapacitySnapshot(ctx context.Context, target *MonitoredTarget, futuresReferencePrice float64) (marketCapacitySnapshot, error) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		errs     []string
		snapshot marketCapacitySnapshot
	)

	wg.Add(4)

	go func() {
		defer wg.Done()
		openInterestNotional, err := e.fetchFuturesOpenInterestNotional(ctx, target.Candidate.Symbol, futuresReferencePrice)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("open_interest=%v", err))
			mu.Unlock()
			return
		}
		if openInterestNotional <= 0 {
			return
		}
		mu.Lock()
		snapshot.FuturesOpenInterestNotional = openInterestNotional
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		quoteVolume, err := e.fetchRecentSpotQuoteVolume(ctx, target.Candidate.Pair)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("spot_recent_volume=%v", err))
			mu.Unlock()
			return
		}
		if quoteVolume <= 0 {
			return
		}
		mu.Lock()
		snapshot.RecentSpotQuoteVolume = quoteVolume
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		quoteVolume, err := e.fetchRecentFuturesQuoteVolume(ctx, target.Candidate.Symbol)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("futures_recent_volume=%v", err))
			mu.Unlock()
			return
		}
		if quoteVolume <= 0 {
			return
		}
		mu.Lock()
		snapshot.RecentFuturesQuoteVolume = quoteVolume
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		spotDepth, futuresDepth, err := e.fetchDualOrderBookDepth(ctx, target.Candidate.Pair, target.Candidate.Symbol)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("visible_depth=%v", err))
			mu.Unlock()
			return
		}
		spotVisible := estimateVisibleDepthNotional(spotDepth.Asks)
		futuresVisible := estimateVisibleDepthNotional(futuresDepth.Bids)
		mu.Lock()
		if spotVisible > 0 {
			snapshot.VisibleSpotAskNotional = spotVisible
			snapshot.AvailableMetrics++
		}
		if futuresVisible > 0 {
			snapshot.VisibleFuturesBidNotional = futuresVisible
			snapshot.AvailableMetrics++
		}
		mu.Unlock()
	}()

	wg.Wait()
	if snapshot.AvailableMetrics == 0 && len(errs) > 0 {
		return snapshot, fmt.Errorf("市场容量数据抓取失败: %s", strings.Join(errs, " | "))
	}
	return snapshot, nil
}

func (e *FundingRateSniperEngine) fetchFuturesOpenInterestNotional(ctx context.Context, symbol string, referencePrice float64) (float64, error) {
	if referencePrice <= 0 {
		return 0, fmt.Errorf("参考价格无效")
	}

	var payload struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/openInterest?symbol="+symbol, &payload); err != nil {
		return 0, err
	}
	openInterestQty := parseFloat(payload.OpenInterest)
	if openInterestQty <= 0 {
		return 0, fmt.Errorf("openInterest 为空")
	}
	return openInterestQty * referencePrice, nil
}

func (e *FundingRateSniperEngine) fetchRecentSpotQuoteVolume(ctx context.Context, symbol string) (float64, error) {
	return e.fetchRecentQuoteVolume(ctx, e.client.spotBaseURL+"/api/v3/klines", symbol)
}

func (e *FundingRateSniperEngine) fetchRecentFuturesQuoteVolume(ctx context.Context, symbol string) (float64, error) {
	return e.fetchRecentQuoteVolume(ctx, e.client.futuresBaseURL+"/fapi/v1/klines", symbol)
}

func (e *FundingRateSniperEngine) fetchRecentQuoteVolume(ctx context.Context, endpoint string, symbol string) (float64, error) {
	interval := klineIntervalForDuration(e.cfg.RecentTradeVolumeWindow)
	var payload [][]any
	url := fmt.Sprintf("%s?symbol=%s&interval=%s&limit=2", endpoint, symbol, interval)
	if err := e.getJSON(ctx, url, &payload); err != nil {
		return 0, err
	}
	if len(payload) == 0 {
		return 0, fmt.Errorf("未返回 K 线")
	}

	row := payload[len(payload)-1]
	if len(payload) > 1 {
		row = payload[len(payload)-2]
	}
	if len(row) < 8 {
		return 0, fmt.Errorf("K 线字段不足")
	}
	quoteVolume := parseAnyFloat(row[7])
	if quoteVolume <= 0 {
		return 0, fmt.Errorf("quoteVolume 无效")
	}
	return quoteVolume, nil
}

func klineIntervalForDuration(window time.Duration) string {
	switch {
	case window >= 15*time.Minute:
		return "15m"
	case window >= 5*time.Minute:
		return "5m"
	case window >= 3*time.Minute:
		return "3m"
	default:
		return "1m"
	}
}

func estimateVisibleDepthNotional(levels []DepthLevel) float64 {
	total := 0.0
	for _, level := range levels {
		if level.Price <= 0 || level.Quantity <= 0 {
			continue
		}
		total += level.Price * level.Quantity
	}
	return total
}

func minPositiveFloat(values ...float64) float64 {
	minimum := 0.0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if minimum <= 0 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func (e *FundingRateSniperEngine) symbolRiskProfile(symbol string) symbolRiskProfile {
	if _, ok := majorRiskTierSymbols[symbol]; ok {
		return symbolRiskProfile{
			Tier:                       "major",
			PerSymbolQuoteMultiplier:   1.0,
			MinEstimatedRateMultiplier: 1.0,
			MinNetProfitMultiplier:     1.0,
			MaxRecentVolumeShareMul:    1.0,
			MaxOpenInterestShareMul:    1.0,
			MaxVisibleDepthShareMul:    1.0,
		}
	}
	if _, ok := mainstreamRiskTierSymbols[symbol]; ok {
		return symbolRiskProfile{
			Tier:                       "mainstream",
			PerSymbolQuoteMultiplier:   0.75,
			MinEstimatedRateMultiplier: 1.15,
			MinNetProfitMultiplier:     1.20,
			ImmediateInvalidateBump:    0.0002,
			MaxRecentVolumeShareMul:    0.75,
			MaxOpenInterestShareMul:    0.75,
			MaxVisibleDepthShareMul:    0.80,
		}
	}
	return symbolRiskProfile{
		Tier:                       "long_tail",
		PerSymbolQuoteMultiplier:   0.40,
		MinEstimatedRateMultiplier: 1.40,
		MinNetProfitMultiplier:     1.50,
		ImmediateInvalidateBump:    0.0005,
		MaxRecentVolumeShareMul:    0.50,
		MaxOpenInterestShareMul:    0.50,
		MaxVisibleDepthShareMul:    0.60,
	}
}

func (e *FundingRateSniperEngine) resetCyclePlatformRiskCounters() {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	e.consecutiveRequestErrors = 0
	e.consecutivePlatformRejects = 0
	e.requestErrorsThisCycle = 0
	e.platformRejectsThisCycle = 0
	e.recoveryActionsThisCycle = 0
	e.platformFuseReason = ""
	e.platformFuseTripped = false
	e.platformCooldownUntil = time.Time{}
	e.currentCooldownCategory = ""
	e.currentCooldownSeverity = ""
	e.currentMonitorTargets = nil
	e.currentPositions = nil
	e.cycleOpenNotionalUsed = 0
}

func (e *FundingRateSniperEngine) checkPlatformFuse() error {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if !e.platformFuseTripped {
		return nil
	}
	return platformFuseError{message: e.platformFuseReason}
}

func (e *FundingRateSniperEngine) notePlatformRequestSuccess() {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	e.consecutiveRequestErrors = 0
	e.consecutivePlatformRejects = 0
	e.runtimeReporter.EmitLocked(EngineStage(""))
}

func (e *FundingRateSniperEngine) notePlatformRequestResult(endpoint string, err error) error {
	if err == nil {
		e.notePlatformRequestSuccess()
		return nil
	}
	category, severity := classifyPlatformException(err)
	text := strings.ToLower(err.Error())
	reject := category == "platform_reject"
	requestAbnormal := reject || strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded") || strings.Contains(text, "context canceled") || strings.Contains(text, "request failed") || strings.Contains(text, "签名请求失败")

	e.platformRiskMu.Lock()
	e.lastExceptionCategory = category
	e.lastExceptionSeverity = severity
	e.lastExceptionDetail = fmt.Sprintf("endpoint=%s err=%v", endpoint, err)
	if requestAbnormal {
		e.consecutiveRequestErrors++
		e.requestErrorsThisCycle++
	} else {
		e.consecutiveRequestErrors = 0
	}
	if reject {
		e.consecutivePlatformRejects++
		e.platformRejectsThisCycle++
	} else {
		e.consecutivePlatformRejects = 0
	}
	requestErrors := e.consecutiveRequestErrors
	rejects := e.consecutivePlatformRejects
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()

	if e.cfg.RiskConfig.MaxConsecutivePlatformRejects > 0 && rejects >= e.cfg.RiskConfig.MaxConsecutivePlatformRejects {
		return e.tripPlatformFuse(category, severity, fmt.Sprintf("平台连续拒绝请求，endpoint=%s consecutive_rejects=%d err=%v", endpoint, rejects, err))
	}
	if e.cfg.RiskConfig.MaxConsecutiveRequestErrors > 0 && requestErrors >= e.cfg.RiskConfig.MaxConsecutiveRequestErrors {
		return e.tripPlatformFuse(category, severity, fmt.Sprintf("平台连续异常请求过多，endpoint=%s consecutive_errors=%d err=%v", endpoint, requestErrors, err))
	}
	return nil
}

func (e *FundingRateSniperEngine) noteRecoveryAction(phase string, symbol string, action string, detail string) error {
	e.platformRiskMu.Lock()
	e.recoveryActionsThisCycle++
	recoveryActions := e.recoveryActionsThisCycle
	e.lastExceptionCategory = "recovery_action"
	e.lastExceptionSeverity = "major"
	e.lastExceptionDetail = fmt.Sprintf("phase=%s symbol=%s action=%s detail=%s", phase, nonEmpty(symbol, "-"), action, nonEmpty(detail, "-"))
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()

	if e.cfg.RiskConfig.MaxRecoveryActionsPerCycle > 0 && recoveryActions > e.cfg.RiskConfig.MaxRecoveryActionsPerCycle {
		return e.tripPlatformFuse("recovery_action", "critical", fmt.Sprintf("单轮恢复动作过多，phase=%s symbol=%s action=%s count=%d detail=%s", phase, nonEmpty(symbol, "-"), action, recoveryActions, nonEmpty(detail, "-")))
	}
	return nil
}

func (e *FundingRateSniperEngine) tripPlatformFuse(category string, severity string, reason string) error {
	e.platformRiskMu.Lock()
	if e.platformFuseTripped {
		reason = e.platformFuseReason
		e.platformRiskMu.Unlock()
		return platformFuseError{message: reason}
	}
	e.platformFuseTripped = true
	cooldown := e.cooldownForSeverity(severity)
	if cooldown > 0 {
		e.platformCooldownUntil = time.Now().UTC().Add(cooldown)
	}
	e.currentCooldownCategory = strings.TrimSpace(category)
	e.currentCooldownSeverity = strings.TrimSpace(severity)
	e.lastExceptionCategory = strings.TrimSpace(category)
	e.lastExceptionSeverity = strings.TrimSpace(severity)
	e.lastExceptionDetail = reason
	e.platformFuseReason = fmt.Sprintf("平台级风控熔断触发: %s", reason)
	e.runtimeReporter.EmitLockedWithReason(StageCooldown, fmt.Sprintf("平台级风控熔断触发: %s", reason))
	message := e.platformFuseReason
	e.platformRiskMu.Unlock()

	e.logger.Printf("%s", message)
	e.emitRiskEvent(newRiskEvent("incident", RiskCategoryPlatform, RiskSeverityCritical, "platform_fuse", "-", "trip", int(atomic.LoadInt32(&e.executionIncidents)), e.cfg.RiskConfig.MaxExecutionIncidents, true, reason))
	return platformFuseError{message: message}
}

func (e *FundingRateSniperEngine) waitPlatformCooldown(ctx context.Context) error {
	e.platformRiskMu.Lock()
	cooldownUntil := e.platformCooldownUntil
	tripped := e.platformFuseTripped
	e.platformRiskMu.Unlock()
	if !tripped || cooldownUntil.IsZero() {
		return nil
	}
	if !cooldownUntil.After(time.Now().UTC()) {
		e.clearPlatformFuse()
		return nil
	}
	e.runtimeReporter.EmitWithReason(StageCooldown, 0, fmt.Sprintf("等待平台冷却结束，截止到 %s", cooldownUntil.UTC().Format(time.RFC3339)))
	for cooldownUntil.After(time.Now().UTC()) {
		if err := e.checkLiveSafetyGuards("platform_cooldown"); err != nil {
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
	e.clearPlatformFuse()
	return nil
}

func (e *FundingRateSniperEngine) clearPlatformFuse() {
	e.platformRiskMu.Lock()
	e.platformFuseTripped = false
	e.platformFuseReason = ""
	e.platformCooldownUntil = time.Time{}
	e.currentCooldownCategory = ""
	e.currentCooldownSeverity = ""
	e.runtimeReporter.EmitLockedWithReason(StageReady, "平台冷却结束，恢复为 ready")
	e.platformRiskMu.Unlock()
}

func classifyPlatformException(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "403"), strings.Contains(text, "418"):
		return "platform_reject", "critical"
	case strings.Contains(text, "429"), strings.Contains(text, "too many requests"):
		return "platform_reject", "major"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"), strings.Contains(text, "context canceled"):
		return "request_timeout", "minor"
	case strings.Contains(text, "签名请求失败"), strings.Contains(text, "request failed"):
		return "request_error", "major"
	default:
		return "request_error", "minor"
	}
}

func (e *FundingRateSniperEngine) cooldownForSeverity(severity string) time.Duration {
	switch strings.TrimSpace(severity) {
	case "critical":
		if e.cfg.RiskConfig.PlatformCriticalCooldown > 0 {
			return e.cfg.RiskConfig.PlatformCriticalCooldown
		}
		return e.cfg.RiskConfig.PlatformAbnormalCooldown
	case "minor":
		if e.cfg.RiskConfig.PlatformMinorCooldown > 0 {
			return e.cfg.RiskConfig.PlatformMinorCooldown
		}
		return e.cfg.RiskConfig.PlatformAbnormalCooldown
	default:
		return e.cfg.RiskConfig.PlatformAbnormalCooldown
	}
}

func (e *FundingRateSniperEngine) listActiveSymbolCooldownsLocked() []string {
	now := time.Now().UTC()
	items := make([]string, 0, len(e.symbolCooldowns))
	for symbol, state := range e.symbolCooldowns {
		if !state.Until.IsZero() && !state.Until.After(now) {
			delete(e.symbolCooldowns, symbol)
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

func (e *FundingRateSniperEngine) setSymbolCooldown(symbol string, category string, severity string, reason string) {
	if strings.TrimSpace(symbol) == "" || e.cfg.RiskConfig.SymbolAbnormalCooldown <= 0 {
		return
	}
	e.platformRiskMu.Lock()
	e.symbolCooldowns[symbol] = symbolCooldownState{
		Category: strings.TrimSpace(category),
		Severity: strings.TrimSpace(severity),
		Reason:   strings.TrimSpace(reason),
		Until:    time.Now().UTC().Add(e.cfg.RiskConfig.SymbolAbnormalCooldown),
	}
	e.lastExceptionCategory = strings.TrimSpace(category)
	e.lastExceptionSeverity = strings.TrimSpace(severity)
	e.lastExceptionDetail = reason
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) filterTargetsBySymbolCooldown(targets []*MonitoredTarget) []*MonitoredTarget {
	now := time.Now().UTC()
	filtered := make([]*MonitoredTarget, 0, len(targets))
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	for _, target := range targets {
		if target == nil {
			continue
		}
		state, ok := e.symbolCooldowns[target.Candidate.Symbol]
		if !ok {
			filtered = append(filtered, target)
			continue
		}
		if !state.Until.After(now) {
			delete(e.symbolCooldowns, target.Candidate.Symbol)
			filtered = append(filtered, target)
			continue
		}
		e.logger.Printf("symbol 冷却中，跳过 %s category=%s severity=%s until=%s reason=%s", target.Candidate.Symbol, state.Category, state.Severity, state.Until.Format(time.RFC3339), state.Reason)
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	return filtered
}

func (e *FundingRateSniperEngine) handleMarketRisk(target *MonitoredTarget, consecutiveBreaches *int, netProfit float64, decision MarketRiskDecision, detail string) {
	if target == nil {
		return
	}
	message := fmt.Sprintf("%s severity=%s action=%s %s", nonEmpty(decision.Category, "market"), nonEmpty(decision.Severity, "unknown"), nonEmpty(decision.Action, "unknown"), strings.TrimSpace(detail))
	switch decision.Action {
	case "observe":
		e.applyMonitorObserveBreach(target, consecutiveBreaches, netProfit, message)
	case "symbol_cooldown":
		e.setSymbolCooldown(target.Candidate.Symbol, decision.Category, decision.Severity, detail)
		e.invalidateMonitorTarget(target, netProfit, fmt.Sprintf("symbol 冷却: %s", message))
	case "global_pause":
		e.invalidateMonitorTarget(target, netProfit, fmt.Sprintf("全局暂停: %s", message))
		if err := e.tripPlatformFuse(decision.Category, decision.Severity, fmt.Sprintf("symbol=%s %s", target.Candidate.Symbol, message)); err != nil {
			e.logger.Printf("平台级风控已触发: %v", err)
		}
	default:
		e.transitionMonitorState(target, "healthy", detail)
	}
}

func (e *FundingRateSniperEngine) noteRecoveryStatus(symbol string, action string, detail string) {
	e.platformRiskMu.Lock()
	e.lastRecoveryAt = time.Now().UTC()
	e.lastRecoveryAction = strings.TrimSpace(fmt.Sprintf("%s | %s | %s", nonEmpty(symbol, "-"), nonEmpty(action, "action"), nonEmpty(detail, "-")))
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) noteOrderUncertainty(symbol string, action string, detail string) {
	e.platformRiskMu.Lock()
	e.orderUncertaintyCount++
	e.lastExceptionCategory = "order_uncertain"
	e.lastExceptionSeverity = "critical"
	e.lastExceptionDetail = fmt.Sprintf("symbol=%s action=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-"))
	currentCount := e.orderUncertaintyCount
	recoveryActions := e.recoveryActionsThisCycle
	e.lastUncertainOrder = strings.TrimSpace(fmt.Sprintf("%s | %s | %s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-")))
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.emitRiskEvent(newRiskEvent("action", RiskCategoryOrderUncertain, RiskSeverityCritical, "order_uncertain", symbol, action, int(atomic.LoadInt32(&e.executionIncidents)), e.cfg.RiskConfig.MaxExecutionIncidents, false, detail))
	if recoveryActions > 0 {
		if err := e.tripPlatformFuse("order_uncertain", "critical", fmt.Sprintf("回补后再次出现不确定成交，symbol=%s action=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), nonEmpty(detail, "-"))); err != nil {
			e.logger.Printf("平台级风控已触发: %v", err)
		}
		return
	}
	if e.cfg.RiskConfig.MaxOrderUncertaintyPerCycle > 0 && currentCount >= e.cfg.RiskConfig.MaxOrderUncertaintyPerCycle {
		if err := e.tripPlatformFuse("order_uncertain", "critical", fmt.Sprintf("单轮不确定成交次数过多，symbol=%s action=%s count=%d detail=%s", nonEmpty(symbol, "-"), nonEmpty(action, "uncertain"), currentCount, nonEmpty(detail, "-"))); err != nil {
			e.logger.Printf("平台级风控已触发: %v", err)
		}
	}
}

func (e *FundingRateSniperEngine) setCurrentMonitorTargets(targets []*MonitoredTarget) {
	labels := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s %.4f%%", target.Candidate.Symbol, target.Candidate.EstimatedRate*100))
	}
	e.setCurrentMonitorTargetLabels(labels)
}

func (e *FundingRateSniperEngine) setCurrentMonitorTargetLabels(labels []string) {
	e.platformRiskMu.Lock()
	e.currentMonitorTargets = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) setCurrentPositionsFromHedges(hedges []OpenHedge) {
	labels := make([]string, 0, len(hedges))
	for _, hedge := range hedges {
		if hedge.HedgeQty <= 0 {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s spot+short %.8f", hedge.Target.Symbol, hedge.HedgeQty))
	}
	e.setCurrentPositions(labels)
}

func (e *FundingRateSniperEngine) setCurrentPositions(labels []string) {
	e.platformRiskMu.Lock()
	e.currentPositions = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func remainingRiskBudget(limit int, used int) int {
	if limit <= 0 {
		return -1
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func maxFloat(a float64, b float64) float64 {
	if a >= b {
		return a
	}
	return b
}

func (e *FundingRateSniperEngine) healthStatusLocked() (string, string, bool) {
	activeCooldowns := e.listActiveSymbolCooldownsLocked()
	if e.emergencyStopTriggered {
		return "paused", nonEmpty(e.emergencyStopReason, "检测到 emergency stop，当前任务应立即停止"), false
	}
	if e.dailyLimitTriggered {
		return "paused", nonEmpty(e.dailyLimitReason, "触发日级风控限制，当前任务应立即停止"), false
	}
	if e.platformFuseTripped {
		return "paused", nonEmpty(e.platformFuseReason, "平台异常，当前轮应暂停"), false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxExecutionIncidents, int(atomic.LoadInt32(&e.executionIncidents))) == 0 {
		return "paused", "执行事故预算已耗尽，当前轮不应继续扩张", false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxRecoveryActionsPerCycle, e.recoveryActionsThisCycle) == 0 {
		return "paused", "回补预算已耗尽，当前轮不应继续扩张", false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxOrderUncertaintyPerCycle, e.orderUncertaintyCount) == 0 {
		return "paused", "不确定成交预算已耗尽，当前轮应立即暂停", false
	}
	if e.consecutivePlatformRejects >= maxIntValue(1, e.cfg.RiskConfig.MaxConsecutivePlatformRejects-1) && e.cfg.RiskConfig.MaxConsecutivePlatformRejects > 1 {
		return "degraded", "平台已接近连续拒单阈值，应谨慎继续", true
	}
	if e.consecutiveRequestErrors >= maxIntValue(1, e.cfg.RiskConfig.MaxConsecutiveRequestErrors-1) && e.cfg.RiskConfig.MaxConsecutiveRequestErrors > 1 {
		return "degraded", "平台异常请求接近阈值，应谨慎继续", true
	}
	if e.orderUncertaintyCount > 0 || e.recoveryActionsThisCycle > 0 {
		return "degraded", "本轮已出现不确定成交或回补动作，应停止扩张并优先收敛", true
	}
	if strings.TrimSpace(e.lastExceptionSeverity) == "major" || strings.TrimSpace(e.lastExceptionSeverity) == "critical" {
		return "degraded", fmt.Sprintf("最近异常等级=%s category=%s，应谨慎继续", nonEmpty(e.lastExceptionSeverity, "unknown"), nonEmpty(e.lastExceptionCategory, "unknown")), true
	}
	if len(activeCooldowns) > 0 {
		return "degraded", fmt.Sprintf("当前有 %d 个 symbol 处于冷却中", len(activeCooldowns)), true
	}
	return "healthy", "当前轮运行健康，可继续观察但仍按阈值保守执行", true
}

func maxIntValue(a int, b int) int {
	if a >= b {
		return a
	}
	return b
}

func (e *FundingRateSniperEngine) traceIDs() (string, string) {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return strings.TrimSpace(e.runID), strings.TrimSpace(e.currentCycleID)
}

func (e *FundingRateSniperEngine) recordMetadata() TraceRecordMetadata {
	if e == nil {
		return TraceRecordMetadata{}
	}
	return buildTraceRecordMetadata(e.cfg)
}

func filterValidTargets(targets []*MonitoredTarget) []*MonitoredTarget {
	result := make([]*MonitoredTarget, 0, len(targets))
	for _, target := range targets {
		if atomic.LoadInt32(&target.IsValid) != 1 {
			continue
		}
		result = append(result, target)
	}
	return result
}

func earliestFundingTime(targets []*MonitoredTarget) time.Time {
	earliest := targets[0].Candidate.NextFundingTime
	for _, target := range targets[1:] {
		if target.Candidate.NextFundingTime.Before(earliest) {
			earliest = target.Candidate.NextFundingTime
		}
	}
	return earliest
}

func earliestFundingTimeFromHedges(hedges []OpenHedge) time.Time {
	earliest := hedges[0].Target.NextFundingTime
	for _, hedge := range hedges[1:] {
		if hedge.Target.NextFundingTime.Before(earliest) {
			earliest = hedge.Target.NextFundingTime
		}
	}
	return earliest
}

func nextWakeTime(now time.Time, minute int, second int) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), minute, second, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(time.Hour)
	}
	return candidate
}

func sleepUntil(ctx context.Context, target time.Time) error {
	if !target.After(time.Now()) {
		return nil
	}

	timer := time.NewTimer(time.Until(target))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func intersectGoldenSymbols(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol) []string {
	spotSet := buildSpotUSDTTradingSet(spotSymbols)
	perpetuals := filterUSDTPERPTradingFutures(futuresSymbols, time.Now().UTC())

	result := make([]string, 0, len(perpetuals))
	seen := make(map[string]struct{}, len(perpetuals))
	for _, symbol := range perpetuals {
		pair := symbol.Pair
		if pair == "" {
			pair = symbol.Symbol
		}
		if _, ok := spotSet[pair]; !ok {
			continue
		}
		if _, duplicated := seen[pair]; duplicated {
			continue
		}
		seen[pair] = struct{}{}
		result = append(result, pair)
	}

	sort.Strings(result)
	return result
}

func extractTradeRules(filters []ExchangeFilter) SymbolTradeRules {
	rules := SymbolTradeRules{}
	for _, filter := range filters {
		switch filter.FilterType {
		case "PRICE_FILTER":
			if rules.PriceTick == 0 {
				rules.PriceTick = parseFloat(filter.TickSize)
			}
		case "LOT_SIZE", "MARKET_LOT_SIZE":
			if rules.MinQty == 0 {
				rules.MinQty = parseFloat(filter.MinQty)
			}
			if rules.StepSize == 0 {
				rules.StepSize = parseFloat(filter.StepSize)
			}
		case "MIN_NOTIONAL":
			if rules.MinNotional == 0 {
				rules.MinNotional = parseFloat(filter.MinNotional)
			}
		case "NOTIONAL":
			if rules.MinNotional == 0 {
				rules.MinNotional = parseFloat(nonEmpty(filter.MinNotional, filter.Notional))
			}
		}
	}

	return rules
}

func normalizeQuantity(raw float64, rules SymbolTradeRules, fallbackPrecision int) float64 {
	if raw <= 0 {
		return 0
	}
	if rules.StepSize > 0 {
		return floorToStep(raw, rules.StepSize)
	}

	return roundDownQuantity(raw, fallbackPrecision)
}

func isQuantityTradable(quantity float64, referencePrice float64, rules SymbolTradeRules) bool {
	if quantity <= 0 {
		return false
	}
	if rules.MinQty > 0 && quantity+1e-12 < rules.MinQty {
		return false
	}
	if referencePrice > 0 && rules.MinNotional > 0 && quantity*referencePrice+1e-12 < rules.MinNotional {
		return false
	}

	return true
}

func floorToStep(value float64, step float64) float64 {
	if step <= 0 {
		return value
	}

	steps := math.Floor((value + 1e-12) / step)
	return steps * step
}

func ceilToStep(value float64, step float64) float64 {
	if step <= 0 {
		return value
	}

	steps := math.Ceil((value - 1e-12) / step)
	return steps * step
}

func roundDownQuantity(value float64, precision int) float64 {
	if precision < 0 {
		precision = 0
	}
	factor := math.Pow10(precision)
	return math.Floor(value*factor) / factor
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func parseFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newClientOrderID(prefix string, symbol string) string {
	symbol = strings.ToLower(symbol)
	symbol = strings.ReplaceAll(symbol, "_", "")
	symbol = strings.ReplaceAll(symbol, "-", "")
	if len(symbol) > 10 {
		symbol = symbol[:10]
	}

	return fmt.Sprintf("%s-%s-%d", prefix, symbol, time.Now().UnixNano()%1_000_000_000_000)
}
