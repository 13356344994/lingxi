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
	spotFeeBNBSafetyMultiplier     = 1.2
	defaultFundingSettlementDelay  = 30 * time.Second
	sleepWaitChunkNear             = 1 * time.Second
	sleepWaitChunkMid              = 5 * time.Second
	sleepWaitChunkFar              = 30 * time.Second
	sleepResyncIntervalNear        = 10 * time.Second
	sleepResyncIntervalMid         = 1 * time.Minute
	sleepResyncIntervalFar         = 10 * time.Minute
	sleepNearThreshold             = 2 * time.Minute
	sleepMidThreshold              = 10 * time.Minute
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
	client                      *BinanceClient
	cfg                         EngineConfig
	logger                      *log.Logger
	runID                       string
	currentCycleID              string
	goldenSymbols               []string
	goldenSymbolSet             map[string]struct{}
	goldenSpotPairByFuture      map[string]string
	spotMeta                    map[string]SpotSymbol
	spotRules                   map[string]SymbolTradeRules
	futuresMeta                 map[string]FuturesSymbol
	futuresRules                map[string]SymbolTradeRules
	spotTickerCache             *BookTickerCache
	futuresTickerCache          *BookTickerCache
	spotDepthCache              *DepthCache
	futuresDepthCache           *DepthCache
	spotOrderHub                *OrderEventHub
	marginOrderHub              *OrderEventHub
	futuresOrderHub             *OrderEventHub
	reportStore                 *ReportStore
	serverTimeOffset            time.Duration
	executionIncidents          int32
	executionEventHook          func(ExecutionIncidentRecord)
	liveStatusHook              func(LiveRuntimeSnapshot)
	platformRiskMu              sync.Mutex
	platformFuseReason          string
	platformFuseTripped         bool
	platformCooldownUntil       time.Time
	consecutiveRequestErrors    int
	consecutivePlatformRejects  int
	requestErrorsThisCycle      int
	platformRejectsThisCycle    int
	recoveryActionsThisCycle    int
	orderUncertaintyCount       int
	protectedIOCAttempts        int
	protectedIOCCanceled        int
	protectedIOCExpired         int
	protectedIOCPartial         int
	protectedIOCUnfilledSum     float64
	lastOrderAt                 time.Time
	lastOrderAction             string
	lastRecoveryAt              time.Time
	lastRecoveryAction          string
	lastManagedDrawdownAt       time.Time
	lastManagedDrawdownAction   string
	managedDrawdownSummary      string
	lastUncertainOrder          string
	lastExceptionCategory       string
	lastExceptionSeverity       string
	lastExceptionDetail         string
	emergencyStopFile           string
	openHedgeStatePath          string
	openHedgeStateMu            sync.Mutex
	emergencyStopTriggered      bool
	emergencyStopReason         string
	dailyRiskStatePath          string
	dailyRiskPersistCh          chan persistedDailyRiskState
	dailyRiskPersistWorkerOnce  sync.Once
	dailyRiskDate               string
	dailyRealizedPnL            float64
	dailyOrderCount             int
	dailyOpenNotionalUsed       float64
	activeOpenHedges            []OpenHedge
	futuresRealtimeWalletUSDT   float64
	futuresRealtimePositions    map[string]futuresRealtimePosition
	lastFuturesAccountUpdateAt  time.Time
	spotRealtimeBalances        map[string]float64
	lastSpotAccountUpdateAt     time.Time
	realtimeRiskWakeCh          chan struct{}
	realtimeRiskWorkerOnce      sync.Once
	panicFlattenInFlight        bool
	symbolPanicFlattenInFlight  map[string]time.Time
	dailyLimitTriggered         bool
	dailyLimitReason            string
	currentCooldownCategory     string
	currentCooldownSeverity     string
	currentMonitorTargets       []string
	currentPositions            []string
	cycleOpenNotionalUsed       float64
	currentStage                EngineStage
	currentStageReason          string
	currentStageTransitionedAt  string
	symbolCooldowns             map[string]symbolCooldownState
	exposureLocks               map[string]exposureLockState
	exposureManager             *ExposureManager
	fundingSettlementDelay      time.Duration
	runtimeReporter             *RuntimeReporter
	fundingSettlementWorkerOnce sync.Once
	fundingSettlementWakeCh     chan struct{}
	cacheOnlyBookTickerUntil    int64
}

type symbolCooldownState struct {
	Category string
	Severity string
	Reason   string
	Until    time.Time
}

type futuresRealtimePosition struct {
	Symbol           string
	PositionAmt      float64
	EntryPrice       float64
	MarkPrice        float64
	UnrealizedProfit float64
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

type signedRequestError struct {
	Endpoint   string
	StatusCode int
	Status     string
	Body       string
	Cause      error
}

type unknownOrderStateError struct {
	Market        string
	Symbol        string
	ClientOrderID string
	Reason        string
}

type signedRequestMode int

const (
	signedRequestNormal signedRequestMode = iota
	signedRequestRescue
)

func (e signedRequestError) Error() string {
	switch {
	case e.Cause != nil && e.StatusCode > 0:
		return fmt.Sprintf("签名请求失败: endpoint=%s status=%s body=%s cause=%v", e.Endpoint, e.Status, e.Body, e.Cause)
	case e.Cause != nil:
		return fmt.Sprintf("签名请求失败: endpoint=%s cause=%v", e.Endpoint, e.Cause)
	case e.StatusCode > 0:
		return fmt.Sprintf("签名请求失败: endpoint=%s status=%s body=%s", e.Endpoint, e.Status, e.Body)
	default:
		return fmt.Sprintf("签名请求失败: endpoint=%s", e.Endpoint)
	}
}

func (e signedRequestError) Unwrap() error {
	return e.Cause
}

func (e unknownOrderStateError) Error() string {
	return fmt.Sprintf(
		"订单终态未知: market=%s symbol=%s clientOrderId=%s reason=%s",
		nonEmpty(e.Market, "unknown"),
		nonEmpty(e.Symbol, "-"),
		nonEmpty(e.ClientOrderID, "-"),
		nonEmpty(e.Reason, "-"),
	)
}

func isUnknownOrderStateError(err error) bool {
	if err == nil {
		return false
	}
	var target unknownOrderStateError
	if errors.As(err, &target) {
		return true
	}
	var blind blindStateError
	return errors.As(err, &blind)
}

func isUnknownOrderRequestError(err error) bool {
	if err == nil {
		return false
	}

	var requestErr signedRequestError
	if errors.As(err, &requestErr) && isUnknownOrderHTTPStatus(requestErr.StatusCode) {
		return true
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	type timeoutError interface {
		Timeout() bool
	}
	var netErr timeoutError
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	text := strings.ToLower(err.Error())
	return strings.Contains(text, "timeout") ||
		strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "bad gateway") ||
		strings.Contains(text, "gateway timeout") ||
		strings.Contains(text, "unexpected eof") ||
		strings.Contains(text, " eof") ||
		strings.HasSuffix(text, "eof") ||
		strings.Contains(text, "connection reset by peer") ||
		strings.Contains(text, "server closed idle connection")
}

func isUnknownOrderHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// FundingCandidate 表示模块二筛选出来的候选猎物。
type FundingCandidate struct {
	Symbol            string
	Pair              string
	RiskTier          string
	Direction         fundingCarryDirection
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
	MinQty              float64
	StepSize            float64
	PriceTick           float64
	MinNotional         float64
	PercentPriceUp      float64
	PercentPriceDown    float64
	PercentPriceAvgMins int
	BidPercentPriceUp   float64
	BidPercentPriceDown float64
	AskPercentPriceUp   float64
	AskPercentPriceDown float64
}

// MonitoredTarget 表示模块三进入死守监控阶段的目标。
type MonitoredTarget struct {
	TraceRecordMetadata
	RunID                      string
	CycleID                    string
	Candidate                  FundingCandidate
	IsValid                    int32
	LastSpread                 float64
	LastNetProfit              float64
	LastSpotAsk                float64
	LastFuturesBid             float64
	LastSpotVWAP               float64
	LastFuturesVWAP            float64
	LastSpotVisibleNotional    float64
	LastFuturesVisibleNotional float64
	LastDepthStabilityRatio    float64
	LastMarketRiskCategory     string
	LastMarketRiskSeverity     string
	LastMarketRiskAction       string
	LastMarketRiskDetail       string
	InvalidReason              string
	BreachCount                int
	MonitorState               string
	LastObserveTime            time.Time
	LastEstimateAt             time.Time
}

type openSequencePlan struct {
	target       *MonitoredTarget
	futuresOrder FuturesBatchOrderRequest
}

type closeSequencePlan struct {
	hedge             *OpenHedge
	futuresOrder      FuturesBatchOrderRequest
	preferPassiveSpot bool
}

type openSequenceBuildState struct {
	remainingSpotUSDT      float64
	remainingFuturesUSDT   float64
	remainingCycleNotional float64
}

type openExecutionPreparation struct {
	beforeSnapshot CycleAccountSnapshot
	plans          []openSequencePlan
	fireAt         time.Time
	goNoGoAt       time.Time
}

type nonFatalOpenAbortError struct {
	message string
}

func (e nonFatalOpenAbortError) Error() string {
	return e.message
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
	SelfTradeMode    string `json:"selfTradePreventionMode,omitempty"`
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
	Direction               fundingCarryDirection
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
	FundingIncomePending  bool
	FundingSettledAt      time.Time
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

type CrossMarginAssetBalance struct {
	Asset    string
	Free     float64
	Borrowed float64
	Interest float64
	Locked   float64
	NetAsset float64
}

type CrossMarginAccountState struct {
	Created             bool
	BorrowEnabled       bool
	TradeEnabled        bool
	TransferInEnabled   bool
	TransferOutEnabled  bool
	AccountType         string
	TotalCollateralUSDT float64
	TotalNetAssetBTC    float64
	TotalLiabilityBTC   float64
	USDT                CrossMarginAssetBalance
	Assets              map[string]CrossMarginAssetBalance
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
	client, err := NewAuthenticatedBinanceClient(secret, cfg.NetworkConfig)
	if err != nil {
		return nil, err
	}
	client.applyNetworkConfig(cfg.NetworkConfig)

	engine := &FundingRateSniperEngine{
		client:                     client,
		cfg:                        cfg,
		logger:                     log.New(os.Stdout, "[FundingRateSniper] ", log.LstdFlags|log.Lmicroseconds),
		goldenSymbolSet:            make(map[string]struct{}),
		goldenSpotPairByFuture:     make(map[string]string),
		spotMeta:                   make(map[string]SpotSymbol),
		spotRules:                  make(map[string]SymbolTradeRules),
		futuresMeta:                make(map[string]FuturesSymbol),
		futuresRules:               make(map[string]SymbolTradeRules),
		reportStore:                reportStore,
		emergencyStopFile:          effectiveEmergencyStopFile(cfg),
		openHedgeStatePath:         effectiveOpenHedgeStatePath(cfg),
		dailyRiskStatePath:         filepath.Join(cfg.NetworkConfig.RecordDir, liveDailyRiskStateName),
		dailyRiskPersistCh:         make(chan persistedDailyRiskState, 1),
		currentStage:               StageReady,
		symbolCooldowns:            make(map[string]symbolCooldownState),
		exposureLocks:              make(map[string]exposureLockState),
		futuresRealtimePositions:   make(map[string]futuresRealtimePosition),
		spotRealtimeBalances:       make(map[string]float64),
		realtimeRiskWakeCh:         make(chan struct{}, 1),
		symbolPanicFlattenInFlight: make(map[string]time.Time),
		fundingSettlementWakeCh:    make(chan struct{}, 1),
	}
	engine.exposureManager = newExposureManager(engine)
	engine.runtimeReporter = newRuntimeReporter(engine)
	return engine, nil
}

// Start 启动完整的小时级主循环。
func (e *FundingRateSniperEngine) Start(ctx context.Context) error {
	e.startDailyRiskStateWriter(ctx)
	if err := e.initialize(ctx); err != nil {
		return err
	}
	if e.exposureManager != nil {
		e.exposureManager.Start(ctx)
	}
	e.startFundingSettlementWorker(ctx)
	e.startUserDataStreams(ctx)
	e.startRealtimeDrawdownMonitor(ctx)

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

	e.goldenSymbols, e.goldenSpotPairByFuture = intersectGoldenSymbols(spotSymbols, futuresSymbols, e.livePreferredSpotQuoteAssets())
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
	if err := e.verifySpotBNBBurnEnabled(ctx); err != nil {
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

func (e *FundingRateSniperEngine) livePreferredSpotQuoteAssets() []string {
	asset := strings.ToUpper(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset))
	if asset == "" {
		return preferredSpotQuoteAssets(e.cfg.StrategyConfig.PreferredSpotQuoteAssets)
	}
	return []string{asset}
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
	preparedOpen, err := e.prepareOpenSequence(ctx, validTargets)
	if err != nil {
		return err
	}
	openHedges, beforeSnapshot, err := e.executeOpenSequence(ctx, validTargets, preparedOpen)
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

	e.runtimeReporter.EmitWithReason(StageCloseMonitor, len(openHedges), fmt.Sprintf("进入平仓监控阶段，持仓数=%d", len(openHedges)))
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
	e.scheduleAsyncFundingSettlement(settlement, openHedges)
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
		estimatedRate, lastFundingRate, fundingJump, basisRate, _ := estimateScanFundingSignal(item)
		direction := fundingDirectionFromRate(estimatedRate)
		if !liveFundingMagnitudePasses(e.cfg, direction, estimatedRate, profile) {
			continue
		}
		if fundingJump >= e.cfg.StrategyConfig.MaxFundingRateJump {
			continue
		}
		if math.Abs(basisRate) > e.cfg.StrategyConfig.MaxScanBasisRate {
			continue
		}

		meta, ok := e.futuresMeta[item.Symbol]
		if !ok {
			continue
		}
		spotPair, ok := e.goldenSpotPairByFuture[item.Symbol]
		if !ok {
			continue
		}
		spotMeta, ok := e.spotMeta[spotPair]
		if !ok {
			continue
		}

		candidates = append(candidates, FundingCandidate{
			Symbol:            item.Symbol,
			Pair:              spotPair,
			RiskTier:          profile.Tier,
			Direction:         direction,
			EstimatedRate:     estimatedRate,
			EstimatedRateText: formatFloat(estimatedRate),
			LastFundingRate:   lastFundingRate,
			FundingRateJump:   fundingJump,
			BasisRate:         basisRate,
			ScanScoreRate:     liveFundingPriorityScore(e.cfg.StrategyConfig.LiveFundingDirectionMode, estimatedRate, basisRate),
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
	stopAt := e.openPreparationStartAt(targets)
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

func (e *FundingRateSniperEngine) openFireAt(targets []*MonitoredTarget) time.Time {
	if len(targets) == 0 {
		return time.Time{}
	}
	return earliestFundingTime(targets).Add(-e.cfg.StrategyConfig.MonitorStopLead)
}

func (e *FundingRateSniperEngine) openPreparationStartAt(targets []*MonitoredTarget) time.Time {
	fireAt := e.openFireAt(targets)
	if fireAt.IsZero() {
		return time.Time{}
	}
	lead := e.cfg.ExecutionConfig.OpenPreparationLead
	if lead <= 0 {
		return fireAt
	}
	return fireAt.Add(-lead)
}

func (e *FundingRateSniperEngine) openNoGoCheckAt(targets []*MonitoredTarget) time.Time {
	fireAt := e.openFireAt(targets)
	if fireAt.IsZero() {
		return time.Time{}
	}
	lead := e.cfg.ExecutionConfig.PreOpenNoGoLead
	if lead <= 0 {
		return fireAt
	}
	return fireAt.Add(-lead)
}

func maxDurationValue(a time.Duration, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}

func minDurationValue(a time.Duration, b time.Duration) time.Duration {
	if a <= b {
		return a
	}
	return b
}

func (e *FundingRateSniperEngine) setBookTickerCacheOnlyUntil(until time.Time) {
	if until.IsZero() {
		atomic.StoreInt64(&e.cacheOnlyBookTickerUntil, 0)
		return
	}
	atomic.StoreInt64(&e.cacheOnlyBookTickerUntil, until.UnixNano())
}

func (e *FundingRateSniperEngine) bookTickerCacheOnlyModeEnabled() bool {
	until := atomic.LoadInt64(&e.cacheOnlyBookTickerUntil)
	return until > 0 && time.Now().UTC().UnixNano() < until
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
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)

	spotRule := e.spotRules[target.Candidate.Pair]
	spotQty := normalizeQuantity(futuresQty, spotRule, target.Candidate.SpotBasePrecision)
	requiredQty := math.Min(futuresQty, spotQty)
	if requiredQty <= 0 {
		return
	}

	spotLevels, futuresLevels := openDepthLevelsForDirection(direction, spotDepth, futuresDepth)
	spotVWAP, spotBestAsk, spotFilled, spotEnough := computeAverageFillPrice(spotLevels, requiredQty)
	futuresVWAP, futuresBestBid, futuresFilled, futuresEnough := computeAverageFillPrice(futuresLevels, requiredQty)
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
	if risk, detail, unstable, spotVisible, futuresVisible, stabilityRatio := e.evaluateOpenDepthStability(target, direction, spotDepth, futuresDepth, spotBestAsk, futuresBestBid); unstable {
		target.LastSpotVisibleNotional = spotVisible
		target.LastFuturesVisibleNotional = futuresVisible
		target.LastDepthStabilityRatio = stabilityRatio
		e.handleMarketRisk(target, consecutiveBreaches, target.LastNetProfit, risk, detail)
		return
	}

	// 真实开仓方向按 funding carry direction 选择现货/合约对应吃单侧，再按该方向评估入场冲击成本。
	spread := openEntrySpreadRate(direction, spotVWAP, futuresVWAP)
	netProfit := estimateReservedNetProfitRate(target.Candidate.EstimatedRate, spread, e.cfg.StrategyConfig.FeeReserveRate, e.cfg.StrategyConfig.ExitSpreadReserveRate)

	target.LastSpotAsk = spotBestAsk
	target.LastFuturesBid = futuresBestBid
	target.LastSpotVWAP = spotVWAP
	target.LastFuturesVWAP = futuresVWAP
	target.LastSpotVisibleNotional, target.LastFuturesVisibleNotional = visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
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

func estimateReservedNetProfitRate(estimatedFundingRate float64, entrySpreadRate float64, roundTripFeeRate float64, exitSpreadReserveRate float64) float64 {
	return estimatedFundingRate - entrySpreadRate - roundTripFeeRate - maxFloat(exitSpreadReserveRate, 0)
}

// executeOpenSequence 执行模块四的压哨多空开枪，并记录开仓前账户基线。
func (e *FundingRateSniperEngine) prepareOpenSequence(ctx context.Context, targets []*MonitoredTarget) (openExecutionPreparation, error) {
	validTargets := filterValidTargets(targets)
	if len(validTargets) == 0 {
		return openExecutionPreparation{}, nil
	}
	if err := e.ensureFuturesExecutionSettings(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	if err := e.ensureUSDTFundingBalancesForOpen(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	if err := e.ensureSpotBNBFeeCoverageForOpen(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	beforeSnapshot, err := e.captureCycleAccountSnapshot(ctx, validTargets)
	if err != nil {
		return openExecutionPreparation{}, err
	}
	plans, err := e.buildOpenSequencePlans(ctx, validTargets, beforeSnapshot)
	if err != nil {
		return openExecutionPreparation{beforeSnapshot: beforeSnapshot}, err
	}
	prepared := openExecutionPreparation{
		beforeSnapshot: beforeSnapshot,
		plans:          plans,
		fireAt:         e.openFireAt(validTargets),
		goNoGoAt:       e.openNoGoCheckAt(validTargets),
	}
	e.logger.Printf(
		"模块四预备完成，开仓时间=%s，战前检查时间=%s，顺序池候选=%d",
		prepared.fireAt.Format("15:04:05"),
		prepared.goNoGoAt.Format("15:04:05"),
		len(prepared.plans),
	)
	return prepared, nil
}

func (e *FundingRateSniperEngine) executeOpenSequence(ctx context.Context, targets []*MonitoredTarget, prepared openExecutionPreparation) ([]OpenHedge, CycleAccountSnapshot, error) {
	validTargets := filterValidTargets(targets)
	if len(validTargets) == 0 {
		return nil, CycleAccountSnapshot{}, nil
	}
	if len(prepared.plans) == 0 {
		return nil, prepared.beforeSnapshot, nil
	}
	if !prepared.goNoGoAt.IsZero() && e.serverNow().Before(prepared.goNoGoAt) {
		if sleepErr := e.sleepUntilServerTime(ctx, prepared.goNoGoAt); sleepErr != nil {
			return nil, CycleAccountSnapshot{}, sleepErr
		}
	}
	if syncErr := e.syncServerTimeOffset(ctx); syncErr != nil {
		e.logger.Printf("模块四战前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	if err := e.verifyPreOpenGoNoGo(validTargets); err != nil {
		e.logger.Printf("模块四放弃本轮开仓，战前检查失败: %v", err)
		e.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, fmt.Sprintf("战前 Go/No-Go 未通过: %v", err))
		return nil, prepared.beforeSnapshot, nil
	}
	stablePlans, stabilityAlerts := e.filterOpenPlansByFundingStability(ctx, prepared.plans, prepared.fireAt)
	for _, alert := range stabilityAlerts {
		e.logger.Printf("模块四战前稳定性过滤: %s", alert)
	}
	if len(stablePlans) == 0 {
		e.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, "战前 funding 稳定性门未通过")
		return nil, prepared.beforeSnapshot, nil
	}
	cacheOnlyUntil := prepared.fireAt.Add(maxDurationValue(e.cfg.ExecutionConfig.ExecutionRecoveryTimeout, 5*time.Second))
	e.setBookTickerCacheOnlyUntil(cacheOnlyUntil)
	defer e.setBookTickerCacheOnlyUntil(time.Time{})
	if prepared.fireAt.After(e.serverNow()) {
		if sleepErr := e.sleepUntilServerTime(ctx, prepared.fireAt); sleepErr != nil {
			return nil, CycleAccountSnapshot{}, sleepErr
		}
	}

	sequentialPlans, parallelPlans := splitOpenSequencePlans(e.cfg, stablePlans)
	e.logger.Printf("模块四开始，触发时间=%s，顺序池=%d，并发池=%d", prepared.fireAt.Format("15:04:05"), len(sequentialPlans), len(parallelPlans))

	var (
		wg            sync.WaitGroup
		sequentialErr error
		parallelErr   error
		sequentialHgs []OpenHedge
		parallelHgs   []OpenHedge
	)
	if len(sequentialPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sequentialHgs, sequentialErr = e.executeSequentialOpenPlans(ctx, sequentialPlans)
		}()
	}
	if len(parallelPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parallelHgs, parallelErr = e.executeParallelOpenPlans(ctx, parallelPlans)
		}()
	}
	wg.Wait()
	if sequentialErr != nil {
		return nil, prepared.beforeSnapshot, sequentialErr
	}
	if parallelErr != nil {
		return nil, prepared.beforeSnapshot, parallelErr
	}
	hedges := append(sequentialHgs, parallelHgs...)
	e.setCurrentPositionsFromHedges(hedges)
	e.logger.Printf("模块四结束，成功锁仓数量: %d", len(hedges))
	return hedges, prepared.beforeSnapshot, nil
}

const (
	preOpenFundingStabilitySamples       = 3
	preOpenFundingStabilityMinSamples    = 1
	preOpenFundingStabilityMaxInterval   = 250 * time.Millisecond
	preOpenFundingStabilityMinInterval   = 75 * time.Millisecond
	preOpenFundingStabilityRefreshBudget = 400 * time.Millisecond
)

func fundingStabilitySampleCount(window time.Duration) int {
	switch {
	case window < 250*time.Millisecond:
		return preOpenFundingStabilityMinSamples
	case window < 500*time.Millisecond:
		return 2
	default:
		return preOpenFundingStabilitySamples
	}
}

func fundingStabilitySleepInterval(window time.Duration, samples int) time.Duration {
	if samples <= 1 || window <= 0 {
		return 0
	}
	interval := window / time.Duration(samples)
	if interval > preOpenFundingStabilityMaxInterval {
		interval = preOpenFundingStabilityMaxInterval
	}
	if interval < preOpenFundingStabilityMinInterval {
		interval = preOpenFundingStabilityMinInterval
	}
	return interval
}

func (e *FundingRateSniperEngine) filterOpenPlansByFundingStability(ctx context.Context, plans []openSequencePlan, fireAt time.Time) ([]openSequencePlan, []string) {
	if len(plans) == 0 {
		return nil, nil
	}
	filtered := make([]openSequencePlan, 0, len(plans))
	alerts := make([]string, 0, len(plans))
	for _, plan := range plans {
		ok, detail := e.verifyPreparedPlanFundingStability(ctx, &plan, fireAt)
		if ok {
			filtered = append(filtered, plan)
			continue
		}
		if plan.target != nil {
			e.invalidateMonitorTarget(plan.target, plan.target.LastNetProfit, fmt.Sprintf("战前 funding 稳定性门未通过: %s", detail))
		}
		alerts = append(alerts, fmt.Sprintf("%s %s", plan.target.Candidate.Symbol, detail))
	}
	return filtered, alerts
}

func (e *FundingRateSniperEngine) verifyPreparedPlanFundingStability(ctx context.Context, plan *openSequencePlan, fireAt time.Time) (bool, string) {
	if e == nil || plan == nil || plan.target == nil {
		return false, "plan 为空"
	}
	target := plan.target
	profile := e.symbolRiskProfile(target.Candidate.Symbol)
	baselineDirection := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if baselineDirection == "" {
		return false, "目标方向为空"
	}
	window := fireAt.Sub(e.serverNow())
	if window < 0 {
		window = 0
	}
	samples := fundingStabilitySampleCount(window)
	interval := fundingStabilitySleepInterval(window, samples)
	lastRate := target.Candidate.EstimatedRate
	lastJump := target.Candidate.FundingRateJump
	lastBasis := target.Candidate.BasisRate
	for sample := 0; sample < samples; sample++ {
		refreshBudget := preOpenFundingStabilityRefreshBudget
		if remaining := fireAt.Sub(e.serverNow()); !fireAt.IsZero() && remaining > 0 {
			refreshBudget = minDurationValue(refreshBudget, remaining)
		}
		if refreshBudget <= 0 {
			refreshBudget = preOpenFundingStabilityMinInterval
		}
		refreshCtx, cancel := context.WithTimeout(ctx, refreshBudget)
		err := e.refreshTargetFundingEstimate(refreshCtx, target, true)
		cancel()
		if err != nil {
			return false, fmt.Sprintf("sample=%d funding refresh 失败: %v", sample+1, err)
		}
		currentDirection := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
		lastRate = target.Candidate.EstimatedRate
		lastJump = target.Candidate.FundingRateJump
		lastBasis = target.Candidate.BasisRate
		if currentDirection != baselineDirection {
			return false, fmt.Sprintf("sample=%d funding 方向翻转: baseline=%s current=%s rate=%.6f", sample+1, baselineDirection, currentDirection, target.Candidate.EstimatedRate)
		}
		if !liveFundingMagnitudePasses(e.cfg, currentDirection, target.Candidate.EstimatedRate, profile) {
			return false, fmt.Sprintf("sample=%d funding 预估不足: rate=%.6f", sample+1, target.Candidate.EstimatedRate)
		}
		if math.Abs(target.Candidate.EstimatedRate) >= e.cfg.StrategyConfig.MaxEstimatedFundingRate {
			return false, fmt.Sprintf("sample=%d funding 预估过高触发异常熔断: rate=%.6f", sample+1, target.Candidate.EstimatedRate)
		}
		if target.Candidate.FundingRateJump >= e.cfg.StrategyConfig.MaxFundingRateJump {
			return false, fmt.Sprintf("sample=%d funding jump 过大: jump=%.6f", sample+1, target.Candidate.FundingRateJump)
		}
		if math.Abs(target.Candidate.BasisRate) > e.cfg.StrategyConfig.MaxScanBasisRate {
			return false, fmt.Sprintf("sample=%d basis 超阈值: basis=%.6f", sample+1, target.Candidate.BasisRate)
		}
		if sample+1 < samples && interval > 0 {
			if !sleepWithContext(ctx, interval) {
				return false, "funding 稳定性采样被取消"
			}
		}
	}
	return true, fmt.Sprintf("samples=%d direction=%s rate=%.6f jump=%.6f basis=%.6f", samples, baselineDirection, lastRate, lastJump, lastBasis)
}

func newOpenSequenceBuildState(beforeSnapshot CycleAccountSnapshot, cfg EngineConfig) openSequenceBuildState {
	return openSequenceBuildState{
		remainingSpotUSDT:      beforeSnapshot.SpotUSDTFree,
		remainingFuturesUSDT:   beforeSnapshot.FuturesUSDTAvailable,
		remainingCycleNotional: cfg.RiskConfig.MaxCycleOpenNotionalUSDT,
	}
}

func effectiveOpenHedgeMaxConcurrency(cfg EngineConfig) int {
	if cfg.ExecutionConfig.OpenHedgeMaxConcurrency > 0 {
		return cfg.ExecutionConfig.OpenHedgeMaxConcurrency
	}
	return 1
}

func configuredSymbolMatch(rawList string, symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return false
	}
	for _, raw := range strings.Split(rawList, ",") {
		token := strings.ToUpper(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		if token == symbol || token == futuresBaseAsset(FuturesSymbol{Symbol: symbol, Pair: symbol, MarginAsset: "USDT"}) {
			return true
		}
	}
	return false
}

func shouldUseParallelOpenHedge(cfg EngineConfig, symbol string) bool {
	return configuredSymbolMatch(cfg.ExecutionConfig.ParallelOpenSymbols, symbol)
}

func shouldUseParallelCloseHedge(cfg EngineConfig, symbol string) bool {
	rawList := cfg.ExecutionConfig.ParallelCloseSymbols
	if strings.TrimSpace(rawList) == "" {
		rawList = cfg.ExecutionConfig.ParallelOpenSymbols
	}
	return configuredSymbolMatch(rawList, symbol)
}

func splitOpenSequencePlans(cfg EngineConfig, plans []openSequencePlan) ([]openSequencePlan, []openSequencePlan) {
	sequential := make([]openSequencePlan, 0, len(plans))
	parallel := make([]openSequencePlan, 0, len(plans))
	for _, plan := range plans {
		if shouldUseParallelOpenHedge(cfg, plan.target.Candidate.Symbol) {
			parallel = append(parallel, plan)
			continue
		}
		sequential = append(sequential, plan)
	}
	return sequential, parallel
}

func splitCloseSequencePlans(cfg EngineConfig, plans []closeSequencePlan) ([]closeSequencePlan, []closeSequencePlan) {
	sequential := make([]closeSequencePlan, 0, len(plans))
	parallel := make([]closeSequencePlan, 0, len(plans))
	for _, plan := range plans {
		if shouldUseParallelCloseHedge(cfg, plan.hedge.Target.Symbol) {
			parallel = append(parallel, plan)
			continue
		}
		sequential = append(sequential, plan)
	}
	return sequential, parallel
}

func (e *FundingRateSniperEngine) isLiveSpotPairFundable(pair string) bool {
	meta, ok := e.spotMeta[pair]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(meta.QuoteAsset), strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset))
}

func (e *FundingRateSniperEngine) desiredOpenUSDTPerAccount(targets []*MonitoredTarget) float64 {
	total := 0.0
	for _, target := range targets {
		total += e.cfg.StrategyConfig.PerSymbolQuoteUSDT * e.symbolRiskProfile(target.Candidate.Symbol).PerSymbolQuoteMultiplier
	}
	if total <= 0 {
		total = float64(len(targets)) * e.cfg.StrategyConfig.PerSymbolQuoteUSDT
	}
	if e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0 && total > e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT {
		total = e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT
	}
	return maxFloat(total, 0)
}

func (e *FundingRateSniperEngine) ensureUSDTFundingBalancesForOpen(ctx context.Context, targets []*MonitoredTarget) error {
	if !e.cfg.ExecutionConfig.AutoBalanceUSDT || len(targets) == 0 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset), "USDT") {
		return nil
	}

	_, spotUSDT, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货 USDT 余额失败: %w", err)
	}
	_, futuresAvailableUSDT, _, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return fmt.Errorf("读取合约 USDT 余额失败: %w", err)
	}
	totalUSDT := spotUSDT + futuresAvailableUSDT
	if totalUSDT <= executionQtyTolerance {
		return nil
	}

	perAccountTarget := e.desiredOpenUSDTPerAccount(targets)
	if perAccountTarget <= executionQtyTolerance {
		return nil
	}
	if totalUSDT < 2*perAccountTarget {
		perAccountTarget = totalUSDT / 2
	}

	const transferEpsilon = 1e-6
	switch {
	case spotUSDT+transferEpsilon < perAccountTarget && futuresAvailableUSDT > perAccountTarget+transferEpsilon:
		transferAmount := math.Min(perAccountTarget-spotUSDT, futuresAvailableUSDT-perAccountTarget)
		if transferAmount <= transferEpsilon {
			return nil
		}
		e.logger.Printf("模块四开仓前自动平衡 USDT，从 UM futures 转到 spot，amount=%.6f", transferAmount)
		return e.transferAssetBetweenAccounts(ctx, "UMFUTURE_MAIN", "USDT", transferAmount)
	case futuresAvailableUSDT+transferEpsilon < perAccountTarget && spotUSDT > perAccountTarget+transferEpsilon:
		transferAmount := math.Min(perAccountTarget-futuresAvailableUSDT, spotUSDT-perAccountTarget)
		if transferAmount <= transferEpsilon {
			return nil
		}
		e.logger.Printf("模块四开仓前自动平衡 USDT，从 spot 转到 UM futures，amount=%.6f", transferAmount)
		return e.transferAssetBetweenAccounts(ctx, "MAIN_UMFUTURE", "USDT", transferAmount)
	default:
		return nil
	}
}

func (e *FundingRateSniperEngine) ensureSpotBNBFeeCoverageForOpen(ctx context.Context, targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	freeBalances, _, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货账户余额失败: %w", err)
	}
	bnbFree := freeBalances["BNB"]
	if bnbFree <= executionQtyTolerance {
		return fmt.Errorf("现货账户 BNB 可用余额不足，已拒绝开仓以避免手续费从基础资产扣除")
	}
	bnbPriceUSDT, err := e.fetchSpotAssetUSDTPrice(ctx, "BNB")
	if err != nil {
		return fmt.Errorf("获取 BNB/USDT 价格失败，无法校验手续费覆盖: %w", err)
	}
	if bnbPriceUSDT <= 0 {
		return fmt.Errorf("获取 BNB/USDT 价格失败，无法校验手续费覆盖: 无效价格 %.8f", bnbPriceUSDT)
	}
	requiredFeeUSDT := 0.0
	for _, target := range targets {
		if target == nil {
			continue
		}
		referencePrice := maxFloat(target.LastSpotAsk, target.Candidate.MarkPrice)
		if referencePrice <= 0 {
			referencePrice = target.LastSpotVWAP
		}
		if referencePrice <= 0 || target.Candidate.OrderQuantity <= 0 {
			continue
		}
		requiredFeeUSDT += target.Candidate.OrderQuantity * referencePrice * e.cfg.HistoricalConfig.SpotTakerFeeRate
	}
	if requiredFeeUSDT <= executionQtyTolerance {
		return nil
	}
	requiredBNB := (requiredFeeUSDT * spotFeeBNBSafetyMultiplier) / bnbPriceUSDT
	if bnbFree+executionQtyTolerance < requiredBNB {
		return fmt.Errorf(
			"现货账户 BNB 可用余额不足，已拒绝开仓: free=%.8f required=%.8f fee_usdt=%.6f bnb_price=%.6f",
			bnbFree,
			requiredBNB,
			requiredFeeUSDT,
			bnbPriceUSDT,
		)
	}
	return nil
}

func (e *FundingRateSniperEngine) executeSequentialOpenPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(plans))
	for _, plan := range plans {
		futuresOrders = append(futuresOrders, plan.futuresOrder)
	}
	if err := e.noteOrderPlaced("batch_open_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), true); err != nil {
		return nil, err
	}
	futuresResults, err := e.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return nil, err
	}
	return e.executeOpenHedgePlans(ctx, plans, futuresResults)
}

func (e *FundingRateSniperEngine) executeParallelOpenPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	hedges := make([]OpenHedge, 0, len(plans))
	var hedgesMu sync.Mutex
	err := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(e.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		hedge, openErr := e.openHedgeForTargetParallel(workerCtx, plans[idx])
		if openErr != nil {
			var skipped nonFatalOpenAbortError
			if errors.As(openErr, &skipped) {
				e.logger.Printf("模块四跳过 %s: %v", plans[idx].target.Candidate.Symbol, skipped)
				return nil
			}
			return e.recordExecutionIncident("open_recovery", plans[idx].target.Candidate.Symbol, openErr.Error())
		}
		hedgesMu.Lock()
		hedges = append(hedges, hedge)
		persistErr := e.syncPersistedOpenHedges(hedges)
		hedgesMu.Unlock()
		return persistErr
	})
	if err != nil {
		return nil, err
	}
	return hedges, nil
}

func (e *FundingRateSniperEngine) waitForCloseExecutionWindow(ctx context.Context, hedges []OpenHedge, fundingTime time.Time) (time.Time, []string, bool, error) {
	alerts := make([]string, 0, 2)
	if syncErr := e.syncServerTimeOffset(ctx); syncErr != nil {
		e.logger.Printf("模块五平仓前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	if e.serverNow().Before(fundingTime) {
		if err := e.sleepUntilServerTime(ctx, fundingTime); err != nil {
			return time.Time{}, nil, false, err
		}
	}

	deadline := fundingTime.Add(e.cfg.ExecutionConfig.ExitDelayAfterFunding)
	interval := e.cfg.ExecutionConfig.ExitMonitorInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	var lastMonitorErr error
	for {
		if err := e.checkLiveSafetyGuards("close_monitor"); err != nil {
			return time.Time{}, nil, false, err
		}
		now := e.serverNow()
		ready, worstAdverseSpread, monitorErr := e.closeWindowReady(ctx, hedges)
		if monitorErr == nil && ready {
			return now, alerts, false, nil
		}
		if monitorErr != nil {
			lastMonitorErr = monitorErr
			e.logger.Printf("模块五退出监控暂未就绪: %v", monitorErr)
		}
		if !now.Before(deadline) {
			preferPassiveSpot := false
			if lastMonitorErr != nil {
				alerts = append(alerts, fmt.Sprintf("退出监控到达最晚时限，忽略最近盘口异常: %v", lastMonitorErr))
			}
			if worstAdverseSpread > e.cfg.StrategyConfig.ExitSpreadReserveRate {
				alerts = append(alerts, fmt.Sprintf("退出监控到达最晚时限，按强制平仓执行: adverseSpread=%.6f reserve=%.6f", worstAdverseSpread, e.cfg.StrategyConfig.ExitSpreadReserveRate))
				preferPassiveSpot = true
			}
			return now, alerts, preferPassiveSpot, nil
		}
		nextCheckAt := now.Add(interval)
		if nextCheckAt.After(deadline) {
			nextCheckAt = deadline
		}
		timer := time.NewTimer(time.Until(nextCheckAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Time{}, nil, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *FundingRateSniperEngine) closeWindowReady(ctx context.Context, hedges []OpenHedge) (bool, float64, error) {
	worstAdverseSpread := 0.0
	for _, hedge := range hedges {
		spotTicker, futuresTicker, err := e.fetchDualBookTicker(ctx, hedge.Target.Pair, hedge.Target.Symbol)
		if err != nil {
			return false, worstAdverseSpread, err
		}
		adverseSpread, ok := adverseCloseSpreadRate(hedge.carryDirection(), spotTicker, futuresTicker)
		if !ok {
			return false, worstAdverseSpread, fmt.Errorf("退出监控盘口异常: symbol=%s", hedge.Target.Symbol)
		}
		if adverseSpread > worstAdverseSpread {
			worstAdverseSpread = adverseSpread
		}
		if adverseSpread > e.cfg.StrategyConfig.ExitSpreadReserveRate {
			return false, worstAdverseSpread, nil
		}
	}
	return true, worstAdverseSpread, nil
}

func adverseCloseSpreadRate(direction fundingCarryDirection, spotTicker BookTicker, futuresTicker BookTicker) (float64, bool) {
	switch resolvedFundingCarryDirection(direction, 0) {
	case fundingCarryDirectionNegative:
		spotAsk := parseFloat(spotTicker.AskPrice)
		futuresBid := parseFloat(futuresTicker.BidPrice)
		if spotAsk <= 0 || futuresBid <= 0 {
			return 0, false
		}
		if spotAsk <= futuresBid {
			return 0, true
		}
		return (spotAsk - futuresBid) / futuresBid, true
	default:
		spotBid := parseFloat(spotTicker.BidPrice)
		futuresAsk := parseFloat(futuresTicker.AskPrice)
		if spotBid <= 0 || futuresAsk <= 0 {
			return 0, false
		}
		if futuresAsk <= spotBid {
			return 0, true
		}
		return (futuresAsk - spotBid) / spotBid, true
	}
}

func (e *FundingRateSniperEngine) executeSequentialClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(plans))
	for _, plan := range plans {
		futuresOrders = append(futuresOrders, plan.futuresOrder)
	}
	if err := e.noteOrderPlaced("batch_close_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), false); err != nil {
		return nil, err
	}
	futuresCloseResults, err := e.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return nil, err
	}
	closeResultBySymbol := make(map[string]FuturesBatchOrderResult, len(futuresCloseResults))
	for _, result := range futuresCloseResults {
		closeResultBySymbol[result.Symbol] = result
	}

	alerts := make([]string, 0, len(plans))
	var alertsMu sync.Mutex
	runErr := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(e.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		plan := plans[idx]
		futureResult, ok := closeResultBySymbol[plan.hedge.Target.Symbol]
		if !ok {
			alertsMu.Lock()
			alerts = append(alerts, fmt.Sprintf("合约平仓回执缺失 %s", plan.hedge.Target.Symbol))
			alertsMu.Unlock()
			return e.recordExecutionIncident("close_futures_missing", plan.hedge.Target.Symbol, "合约平仓回执缺失")
		}
		closeAlerts, closeErr := e.closeHedgePosition(workerCtx, plan.hedge, futureResult, plan.preferPassiveSpot)
		alertsMu.Lock()
		alerts = append(alerts, closeAlerts...)
		if closeErr != nil {
			alerts = append(alerts, closeErr.Error())
		}
		alertsMu.Unlock()
		if closeErr == nil {
			return nil
		}
		return e.recordExecutionIncident("close_recovery", plan.hedge.Target.Symbol, closeErr.Error())
	})
	if runErr != nil {
		return nil, runErr
	}
	return alerts, nil
}

func (e *FundingRateSniperEngine) executeParallelClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	alerts := make([]string, 0, len(plans))
	var alertsMu sync.Mutex
	runErr := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(e.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		closeAlerts, closeErr := e.closeHedgePositionParallel(workerCtx, plans[idx])
		alertsMu.Lock()
		alerts = append(alerts, closeAlerts...)
		if closeErr != nil {
			alerts = append(alerts, closeErr.Error())
		}
		alertsMu.Unlock()
		if closeErr == nil {
			return nil
		}
		return e.recordExecutionIncident("close_recovery", plans[idx].hedge.Target.Symbol, closeErr.Error())
	})
	if runErr != nil {
		return nil, runErr
	}
	return alerts, nil
}

func runBoundedConcurrentTasks(ctx context.Context, maxConcurrency int, total int, worker func(context.Context, int) error) error {
	if total <= 0 {
		return nil
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}

	var (
		wg       sync.WaitGroup
		firstErr error
		mu       sync.Mutex
	)
	slots := make(chan struct{}, maxConcurrency)
	for idx := 0; idx < total; idx++ {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := worker(ctx, i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(idx)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func (e *FundingRateSniperEngine) buildOpenSequencePlans(ctx context.Context, validTargets []*MonitoredTarget, beforeSnapshot CycleAccountSnapshot) ([]openSequencePlan, error) {
	state := newOpenSequenceBuildState(beforeSnapshot, e.cfg)
	plans := make([]openSequencePlan, 0, len(validTargets))
	for _, target := range validTargets {
		plan, err := e.buildOpenSequencePlan(ctx, target, &state)
		if err != nil {
			return nil, err
		}
		if plan == nil {
			continue
		}
		plans = append(plans, *plan)
	}
	usedNotional := 0.0
	if e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0 {
		usedNotional = e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT - maxFloat(state.remainingCycleNotional, 0)
	}
	if err := e.setCycleOpenNotionalUsed(usedNotional); err != nil {
		return nil, err
	}
	return plans, nil
}

func (e *FundingRateSniperEngine) buildOpenSequencePlan(ctx context.Context, target *MonitoredTarget, state *openSequenceBuildState) (*openSequencePlan, error) {
	profile := e.symbolRiskProfile(target.Candidate.Symbol)
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	refreshErr := e.refreshTargetFundingEstimate(refreshCtx, target, true)
	refreshCancel()
	if refreshErr != nil {
		e.logger.Printf("模块四跳过 %s，开仓前刷新资金费预估失败: %v", target.Candidate.Symbol, refreshErr)
		return nil, nil
	}
	if !liveFundingMagnitudePasses(e.cfg, target.Candidate.Direction, target.Candidate.EstimatedRate, profile) {
		e.logger.Printf("模块四跳过 %s，开仓前最新预估费率不足: %.6f", target.Candidate.Symbol, target.Candidate.EstimatedRate)
		return nil, nil
	}
	if math.Abs(target.Candidate.EstimatedRate) >= e.cfg.StrategyConfig.MaxEstimatedFundingRate {
		e.logger.Printf("模块四跳过 %s，开仓前最新预估费率过高触发异常熔断: %.6f", target.Candidate.Symbol, target.Candidate.EstimatedRate)
		return nil, nil
	}
	if target.Candidate.FundingRateJump >= e.cfg.StrategyConfig.MaxFundingRateJump {
		e.logger.Printf("模块四跳过 %s，开仓前资金费跳变过大触发异常熔断: %.6f", target.Candidate.Symbol, target.Candidate.FundingRateJump)
		return nil, nil
	}
	if math.Abs(target.Candidate.BasisRate) > e.cfg.StrategyConfig.MaxScanBasisRate {
		e.logger.Printf("模块四跳过 %s，开仓前最新基差超阈值: %.6f", target.Candidate.Symbol, target.Candidate.BasisRate)
		return nil, nil
	}
	if !e.isLiveSpotPairFundable(target.Candidate.Pair) {
		e.logger.Printf("模块四跳过 %s，现货交易对 %s 的计价资产不是 live funding asset=%s", target.Candidate.Symbol, target.Candidate.Pair, strings.ToUpper(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset)))
		return nil, nil
	}

	futuresRule := e.futuresRules[target.Candidate.Symbol]
	rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / target.Candidate.MarkPrice
	target.Candidate.OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, target.Candidate.QuantityPrecision)
	if !isQuantityTradable(target.Candidate.OrderQuantity, target.Candidate.MarkPrice, futuresRule) {
		return nil, nil
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
		return nil, nil
	}
	if adjustedQty <= 0 {
		e.setSymbolCooldown(target.Candidate.Symbol, "liquidity", "major", fmt.Sprintf("开仓前市场容量校验后数量归零: %s", capacityReason))
		e.logger.Printf("模块四跳过 %s，市场容量校验后数量归零: %s", target.Candidate.Symbol, capacityReason)
		return nil, nil
	}
	if adjustedQty+1e-12 < target.Candidate.OrderQuantity {
		coverageRatio := adjustedQty / target.Candidate.OrderQuantity
		decision := EvaluateExecutionCoverageRisk("liquidity", coverageRatio)
		switch decision.Action {
		case "symbol_cooldown":
			e.setSymbolCooldown(target.Candidate.Symbol, decision.Category, decision.Severity, fmt.Sprintf("开仓前市场容量不足 coverage=%.4f %s", coverageRatio, capacityReason))
		case "global_pause":
			if fuseErr := e.tripPlatformFuse(decision.Category, decision.Severity, fmt.Sprintf("symbol=%s 开仓前市场容量极差 coverage=%.4f %s", target.Candidate.Symbol, coverageRatio, capacityReason)); fuseErr != nil {
				return nil, fuseErr
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
		return nil, nil
	}

	orderNotional := target.Candidate.OrderQuantity * futuresReferencePrice
	if state.remainingCycleNotional > 0 && orderNotional > state.remainingCycleNotional+1e-9 {
		cycleLimitedQty := normalizeQuantity(state.remainingCycleNotional/futuresReferencePrice, futuresRule, target.Candidate.QuantityPrecision)
		if !isQuantityTradable(cycleLimitedQty, futuresReferencePrice, futuresRule) {
			e.logger.Printf("模块四跳过 %s，单周期总敞口上限不足以继续开仓，remainingNotional=%.6f", target.Candidate.Symbol, state.remainingCycleNotional)
			return nil, nil
		}
		e.logger.Printf("模块四缩减 %s 下单数量以满足单周期总敞口上限，原名义=%.6fUSDT 新名义=%.6fUSDT 上限剩余=%.6fUSDT", target.Candidate.Symbol, orderNotional, cycleLimitedQty*futuresReferencePrice, state.remainingCycleNotional)
		target.Candidate.OrderQuantity = cycleLimitedQty
		orderNotional = target.Candidate.OrderQuantity * futuresReferencePrice
	}

	spotRequiredUSDT, futuresRequiredUSDT := estimateTargetCapitalRequirement(e.cfg, target, spotReferencePrice, futuresReferencePrice)
	if spotRequiredUSDT <= 0 || futuresRequiredUSDT <= 0 {
		e.logger.Printf("模块四跳过 %s，资金需求估算无效，spotRequired=%.6f futuresRequired=%.6f", target.Candidate.Symbol, spotRequiredUSDT, futuresRequiredUSDT)
		return nil, nil
	}
	if spotRequiredUSDT > state.remainingSpotUSDT+1e-9 || futuresRequiredUSDT > state.remainingFuturesUSDT+1e-9 {
		e.logger.Printf(
			"模块四跳过 %s，账户可用资金不足，spotRequired=%.6f spotFree=%.6f futuresRequired=%.6f futuresAvailable=%.6f",
			target.Candidate.Symbol,
			spotRequiredUSDT,
			state.remainingSpotUSDT,
			futuresRequiredUSDT,
			state.remainingFuturesUSDT,
		)
		return nil, nil
	}

	state.remainingSpotUSDT -= spotRequiredUSDT
	state.remainingFuturesUSDT -= futuresRequiredUSDT
	if state.remainingCycleNotional > 0 {
		state.remainingCycleNotional -= orderNotional
	}

	return &openSequencePlan{
		target:       target,
		futuresOrder: e.buildProtectedOpenFuturesOrder(target),
	}, nil
}

func (e *FundingRateSniperEngine) executeOpenHedgePlans(ctx context.Context, plans []openSequencePlan, futuresResults []FuturesBatchOrderResult) ([]OpenHedge, error) {
	if len(plans) != len(futuresResults) {
		return nil, fmt.Errorf("开仓计划与合约回执数量不一致: plans=%d results=%d", len(plans), len(futuresResults))
	}

	hedges := make([]OpenHedge, 0, len(plans))
	var hedgesMu sync.Mutex
	err := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(e.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		plan := plans[idx]
		result := futuresResults[idx]
		if result.Code != 0 || result.OrderID == 0 {
			return e.recordExecutionIncident("open_futures_rejected", plan.futuresOrder.Symbol, fmt.Sprintf("code=%d msg=%s", result.Code, result.Msg))
		}

		hedge, openErr := e.openHedgeForTarget(workerCtx, plan.target, result)
		if openErr != nil {
			var skipped nonFatalOpenAbortError
			if errors.As(openErr, &skipped) {
				e.logger.Printf("模块四跳过 %s: %v", plan.target.Candidate.Symbol, skipped)
				return nil
			}
			return e.recordExecutionIncident("open_recovery", plan.target.Candidate.Symbol, openErr.Error())
		}

		hedgesMu.Lock()
		hedges = append(hedges, hedge)
		persistErr := e.syncPersistedOpenHedges(hedges)
		hedgesMu.Unlock()
		return persistErr
	})
	if err != nil {
		return nil, err
	}
	return hedges, nil
}

// executeCloseSequence 执行模块五的收益落袋与闪电撤退。
func (e *FundingRateSniperEngine) executeCloseSequence(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot) (CycleSettlement, error) {
	if len(hedges) == 0 {
		return CycleSettlement{}, nil
	}

	fundingTime := earliestFundingTimeFromHedges(hedges)
	closeAt, monitorAlerts, preferPassiveSpotClose, err := e.waitForCloseExecutionWindow(ctx, hedges, fundingTime)
	if err != nil {
		return CycleSettlement{}, err
	}

	plans := make([]closeSequencePlan, 0, len(hedges))
	for idx := range hedges {
		plans = append(plans, closeSequencePlan{
			hedge:             &hedges[idx],
			futuresOrder:      e.buildProtectedCloseFuturesOrder(ctx, hedges[idx]),
			preferPassiveSpot: preferPassiveSpotClose,
		})
	}
	sequentialPlans, parallelPlans := splitCloseSequencePlans(e.cfg, plans)
	e.runtimeReporter.EmitWithReason(StageClose, len(hedges), fmt.Sprintf("开始平仓执行，顺序池=%d，并发池=%d", len(sequentialPlans), len(parallelPlans)))
	e.logger.Printf("模块五开始，实际平仓时间=%s，顺序池=%d，并发池=%d", closeAt.Format("15:04:05"), len(sequentialPlans), len(parallelPlans))

	var (
		wg               sync.WaitGroup
		sequentialErr    error
		parallelErr      error
		sequentialAlerts []string
		parallelAlerts   []string
	)
	if len(sequentialPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sequentialAlerts, sequentialErr = e.executeSequentialClosePlans(ctx, sequentialPlans)
		}()
	}
	if len(parallelPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parallelAlerts, parallelErr = e.executeParallelClosePlans(ctx, parallelPlans)
		}()
	}
	wg.Wait()
	if sequentialErr != nil {
		return CycleSettlement{}, sequentialErr
	}
	if parallelErr != nil {
		return CycleSettlement{}, parallelErr
	}
	e.setCurrentPositions(nil)

	afterSnapshot, err := e.captureCycleAccountSnapshotFromHedges(ctx, hedges)
	if err != nil {
		return CycleSettlement{}, err
	}

	spotTradingPnL, futuresTradingPnL, totalFees := 0.0, 0.0, 0.0
	expectedFundingIncome, fundingAlerts := e.expectedFundingIncomeSummary(hedges, beforeSnapshot)
	for _, hedge := range hedges {
		direction := hedge.carryDirection()
		closedSpotQty := math.Min(hedge.HedgeQty, hedge.CloseSpotExecutedQty)
		if closedSpotQty > 0 {
			openSpotCost := hedge.OpenSpotQuoteQty * (closedSpotQty / hedge.HedgeQty)
			spotEntryPrice := openSpotCost / closedSpotQty
			spotExitPrice := hedge.CloseSpotQuoteQty / closedSpotQty
			spotTradingPnL += direction.spotGrossPnL(closedSpotQty, spotEntryPrice, spotExitPrice)
		}
		totalFees += hedge.OpenSpotFeeUSDT + hedge.CloseSpotFeeUSDT + hedge.OpenFuturesFeeUSDT + hedge.CloseFuturesFeeUSDT

		if hedge.CloseFuturesRealizedPnL != 0 {
			futuresTradingPnL += hedge.CloseFuturesRealizedPnL
		} else if hedge.OpenFuturesAvgPrice > 0 && hedge.CloseFuturesAvgPrice > 0 {
			closedFuturesQty := hedge.CloseFuturesExecutedQty
			if closedFuturesQty <= 0 {
				closedFuturesQty = hedge.HedgeQty
			}
			futuresTradingPnL += direction.futuresGrossPnL(closedFuturesQty, hedge.OpenFuturesAvgPrice, hedge.CloseFuturesAvgPrice)
		}
	}

	residualSpotValue := calculateResidualSpotValue(afterSnapshot.SpotAssetBalances, hedges)
	spotUSDTDelta := afterSnapshot.SpotUSDTFree - beforeSnapshot.SpotUSDTFree
	futuresUSDTDelta := afterSnapshot.FuturesUSDTWallet - beforeSnapshot.FuturesUSDTWallet
	totalFundingIncome := 0.0
	netPnL := spotTradingPnL + futuresTradingPnL - totalFees
	fundingIncomeDiff := totalFundingIncome - expectedFundingIncome
	snapshotNetPnL := spotUSDTDelta + futuresUSDTDelta + residualSpotValue
	alerts := e.buildSettlementAlerts(afterSnapshot, hedges, residualSpotValue, netPnL, snapshotNetPnL)
	alerts = append(alerts, fundingAlerts...)
	alerts = append(alerts, fmt.Sprintf("资金费异步结算待处理，预计在 %s 后更新实收流水", e.effectiveFundingSettlementDelay().Round(time.Second)))
	alerts = append(alerts, monitorAlerts...)
	execAlerts := append(sequentialAlerts, parallelAlerts...)
	if len(execAlerts) > 0 {
		e.logger.Printf("模块五对账警告: %s", strings.Join(execAlerts, " | "))
		alerts = append(alerts, execAlerts...)
	}
	if e.canClearPersistedOpenHedges(afterSnapshot, hedges) {
		if clearErr := e.clearPersistedOpenHedges(); clearErr != nil {
			return CycleSettlement{}, clearErr
		}
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
		FundingIncomePending:  true,
		HasAlert:              len(alerts) > 0,
		Alerts:                alerts,
	}, nil
}

func (e *FundingRateSniperEngine) openHedgeForTarget(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult) (OpenHedge, error) {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if err := e.ensureSymbolNotLockedByExposure(target.Candidate.Symbol, "open_hedge"); err != nil {
		return OpenHedge{}, err
	}
	futuresExecution, resolveErr := e.resolveFuturesOrderExecution(ctx, futureResult.Symbol, futureResult.ClientOrderID, futureResult)
	filledFuturesQty := futuresExecution.ExecutedQty
	if resolveErr != nil {
		if isUnknownOrderStateError(resolveErr) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                 exposureRescueModeOpen,
				Phase:                "open_futures_unknown",
				Direction:            direction,
				Symbol:               target.Candidate.Symbol,
				Pair:                 target.Candidate.Pair,
				SpotBaseAsset:        target.Candidate.SpotBaseAsset,
				SpotBasePrecision:    target.Candidate.SpotBasePrecision,
				SpotReferencePrice:   target.LastSpotAsk,
				FuturesClientOrderID: futureResult.ClientOrderID,
			})
			if rescueErr != nil {
				return OpenHedge{}, fmt.Errorf("合约开仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return OpenHedge{}, fmt.Errorf("合约开仓终态未知，已转入 ExposureManager 托管")
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		e.flattenUncertainFuturesOpen(cleanupCtx, target.Candidate.Symbol, futureResult.ClientOrderID, futureResult)
		return OpenHedge{}, fmt.Errorf("合约空单未确认成交: %w", resolveErr)
	}
	if filledFuturesQty <= executionQtyTolerance {
		return OpenHedge{}, fmt.Errorf("合约空单成交数量为 0")
	}

	spotQty, spotPriceForCheck, spotTradable := e.openSpotTarget(target, filledFuturesQty)
	if !spotTradable {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeDirectionalFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty, direction); err != nil {
			return OpenHedge{}, fmt.Errorf("现货对冲数量无效且合约回补失败: %w", err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_invalid_spot_qty", fmt.Sprintf("filledFuturesQty=%.8f spotTargetQty=%.8f", filledFuturesQty, spotQty))
		return OpenHedge{}, fmt.Errorf("现货对冲数量归零，已回补合约空单")
	}

	spotOrder, spotErr := e.placeDirectionalSpotMarketOrderIdempotent(ctx, target.Candidate.Pair, direction.openSpotSide(), spotQty, newClientOrderID("sopen", target.Candidate.Pair), direction.spotVenue(), "spot_market_"+strings.ToLower(direction.openSpotSide()), true)
	if spotErr != nil {
		if isUnknownOrderStateError(spotErr) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                    exposureRescueModeOpen,
				Phase:                   "open_spot_unknown",
				Direction:               direction,
				Symbol:                  target.Candidate.Symbol,
				Pair:                    target.Candidate.Pair,
				SpotBaseAsset:           target.Candidate.SpotBaseAsset,
				SpotBasePrecision:       target.Candidate.SpotBasePrecision,
				SpotReferencePrice:      spotPriceForCheck,
				FuturesClientOrderID:    futureResult.ClientOrderID,
				KnownFuturesExecutedQty: filledFuturesQty,
			})
			if rescueErr != nil {
				return OpenHedge{}, fmt.Errorf("现货开仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return OpenHedge{}, fmt.Errorf("现货开仓终态未知，已转入 ExposureManager 托管")
		}
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeDirectionalFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty, direction); err != nil {
			return OpenHedge{}, fmt.Errorf("现货补仓失败且合约回补失败: spotErr=%v recoverErr=%w", spotErr, err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_spot_open_failure", fmt.Sprintf("filledFuturesQty=%.8f spotErr=%v", filledFuturesQty, spotErr))
		return OpenHedge{}, fmt.Errorf("现货补仓失败，已回补合约空单: %w", spotErr)
	}

	hedge, err := e.finalizeOpenHedge(ctx, target, futureResult, futuresExecution, spotOrder, spotPriceForCheck)
	if err != nil {
		return OpenHedge{}, err
	}
	if err := e.validateFreshOpenHedge(ctx, &hedge); err != nil {
		return OpenHedge{}, err
	}
	return hedge, nil
}

func (e *FundingRateSniperEngine) openHedgeForTargetParallel(ctx context.Context, plan openSequencePlan) (OpenHedge, error) {
	target := plan.target
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if err := e.ensureSymbolNotLockedByExposure(target.Candidate.Symbol, "parallel_open_hedge"); err != nil {
		return OpenHedge{}, err
	}
	spotQty, spotPriceForCheck, spotTradable := e.openSpotTarget(target, target.Candidate.OrderQuantity)
	if !spotTradable {
		return OpenHedge{}, fmt.Errorf("并发对冲的现货目标数量无效")
	}
	spotClientOrderID := newClientOrderID("sopen", target.Candidate.Pair)

	type spotOpenResult struct {
		order SpotOrderResponse
		err   error
	}
	type futuresOpenResult struct {
		snapshot OrderExecutionSnapshot
		err      error
	}

	spotResultCh := make(chan spotOpenResult, 1)
	futuresResultCh := make(chan futuresOpenResult, 1)
	go func() {
		order, err := e.placeDirectionalSpotMarketOrderIdempotent(ctx, target.Candidate.Pair, direction.openSpotSide(), spotQty, spotClientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(direction.openSpotSide()), true)
		spotResultCh <- spotOpenResult{order: order, err: err}
	}()
	go func() {
		snapshot, err := e.placeSingleFuturesOpenOrderIdempotent(ctx, plan.futuresOrder)
		futuresResultCh <- futuresOpenResult{snapshot: snapshot, err: err}
	}()

	spotResult := <-spotResultCh
	futuresResult := <-futuresResultCh
	filledFuturesQty := futuresResult.snapshot.ExecutedQty
	if futuresResult.err != nil {
		if isUnknownOrderStateError(futuresResult.err) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                 exposureRescueModeOpen,
				Phase:                "parallel_open_futures_unknown",
				Direction:            direction,
				Symbol:               target.Candidate.Symbol,
				Pair:                 target.Candidate.Pair,
				SpotBaseAsset:        target.Candidate.SpotBaseAsset,
				SpotBasePrecision:    target.Candidate.SpotBasePrecision,
				SpotReferencePrice:   spotPriceForCheck,
				SpotClientOrderID:    spotClientOrderID,
				FuturesClientOrderID: plan.futuresOrder.NewClientOrderID,
			})
			if rescueErr != nil {
				return OpenHedge{}, fmt.Errorf("并发合约开仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return OpenHedge{}, fmt.Errorf("并发合约开仓终态未知，已转入 ExposureManager 托管")
		}
		if spotResult.err == nil {
			if err := e.revertSpotOpenAfterFuturesFailure(ctx, target, spotResult.order, spotPriceForCheck); err != nil {
				rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
					Mode:                 exposureRescueModeOpen,
					Phase:                "parallel_open_revert_spot_failed",
					Direction:            direction,
					Symbol:               target.Candidate.Symbol,
					Pair:                 target.Candidate.Pair,
					SpotBaseAsset:        target.Candidate.SpotBaseAsset,
					SpotBasePrecision:    target.Candidate.SpotBasePrecision,
					SpotReferencePrice:   spotPriceForCheck,
					SpotClientOrderID:    nonEmpty(spotResult.order.ClientOrderID, spotClientOrderID),
					KnownSpotExecutedQty: resolvedSpotOrderBaseQty(spotResult.order, target.Candidate.SpotBaseAsset),
				})
				if rescueErr != nil {
					return OpenHedge{}, fmt.Errorf("并发合约开仓失败且现货回滚失败，ExposureManager 也失败: rollbackErr=%v rescueErr=%w", err, rescueErr)
				}
				return OpenHedge{}, fmt.Errorf("并发合约开仓失败且现货回滚失败，已转入 ExposureManager: %w", err)
			}
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		e.flattenUncertainFuturesOpen(cleanupCtx, target.Candidate.Symbol, plan.futuresOrder.NewClientOrderID, FuturesBatchOrderResult{
			Symbol:        plan.futuresOrder.Symbol,
			ClientOrderID: plan.futuresOrder.NewClientOrderID,
		})
		return OpenHedge{}, fmt.Errorf("并发合约空单未确认成交: %w", futuresResult.err)
	}
	if filledFuturesQty <= executionQtyTolerance {
		if spotResult.err == nil {
			if err := e.revertSpotOpenAfterFuturesFailure(ctx, target, spotResult.order, spotPriceForCheck); err != nil {
				rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
					Mode:                 exposureRescueModeOpen,
					Phase:                "parallel_open_zero_fill_revert_failed",
					Direction:            direction,
					Symbol:               target.Candidate.Symbol,
					Pair:                 target.Candidate.Pair,
					SpotBaseAsset:        target.Candidate.SpotBaseAsset,
					SpotBasePrecision:    target.Candidate.SpotBasePrecision,
					SpotReferencePrice:   spotPriceForCheck,
					SpotClientOrderID:    nonEmpty(spotResult.order.ClientOrderID, spotClientOrderID),
					KnownSpotExecutedQty: resolvedSpotOrderBaseQty(spotResult.order, target.Candidate.SpotBaseAsset),
				})
				if rescueErr != nil {
					return OpenHedge{}, fmt.Errorf("并发合约零成交且现货回滚失败，ExposureManager 也失败: rollbackErr=%v rescueErr=%w", err, rescueErr)
				}
				return OpenHedge{}, fmt.Errorf("并发合约零成交且现货回滚失败，已转入 ExposureManager: %w", err)
			}
		}
		return OpenHedge{}, fmt.Errorf("并发合约空单成交数量为 0")
	}
	if spotResult.err != nil {
		if isUnknownOrderStateError(spotResult.err) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                    exposureRescueModeOpen,
				Phase:                   "parallel_open_spot_unknown",
				Direction:               direction,
				Symbol:                  target.Candidate.Symbol,
				Pair:                    target.Candidate.Pair,
				SpotBaseAsset:           target.Candidate.SpotBaseAsset,
				SpotBasePrecision:       target.Candidate.SpotBasePrecision,
				SpotReferencePrice:      spotPriceForCheck,
				SpotClientOrderID:       spotClientOrderID,
				FuturesClientOrderID:    plan.futuresOrder.NewClientOrderID,
				KnownFuturesExecutedQty: filledFuturesQty,
			})
			if rescueErr != nil {
				return OpenHedge{}, fmt.Errorf("并发现货开仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return OpenHedge{}, fmt.Errorf("并发现货开仓终态未知，已转入 ExposureManager 托管")
		}
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeDirectionalFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty, direction); err != nil {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                    exposureRescueModeOpen,
				Phase:                   "parallel_open_spot_failure_close_futures_failed",
				Direction:               direction,
				Symbol:                  target.Candidate.Symbol,
				Pair:                    target.Candidate.Pair,
				SpotBaseAsset:           target.Candidate.SpotBaseAsset,
				SpotBasePrecision:       target.Candidate.SpotBasePrecision,
				SpotReferencePrice:      spotPriceForCheck,
				FuturesClientOrderID:    plan.futuresOrder.NewClientOrderID,
				KnownFuturesExecutedQty: filledFuturesQty,
			})
			if rescueErr != nil {
				return OpenHedge{}, fmt.Errorf("并发现货补仓失败且合约回补失败，ExposureManager 也失败: spotErr=%v recoverErr=%v rescueErr=%w", spotResult.err, err, rescueErr)
			}
			return OpenHedge{}, fmt.Errorf("并发现货补仓失败且合约回补失败，已转入 ExposureManager: spotErr=%v recoverErr=%w", spotResult.err, err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_parallel_spot_open_failure", fmt.Sprintf("filledFuturesQty=%.8f spotErr=%v", filledFuturesQty, spotResult.err))
		return OpenHedge{}, fmt.Errorf("并发现货补仓失败，已回补合约空单: %w", spotResult.err)
	}

	hedge, err := e.finalizeOpenHedge(ctx, target, futuresExecutionToResult(futuresResult.snapshot, plan.futuresOrder), futuresResult.snapshot, spotResult.order, spotPriceForCheck)
	if err != nil {
		return OpenHedge{}, err
	}
	if err := e.validateFreshOpenHedge(ctx, &hedge); err != nil {
		return OpenHedge{}, err
	}
	return hedge, nil
}

func (e *FundingRateSniperEngine) openSpotTarget(target *MonitoredTarget, hedgeQty float64) (float64, float64, bool) {
	spotRule := e.spotRules[target.Candidate.Pair]
	spotQty := normalizeQuantity(hedgeQty, spotRule, target.Candidate.SpotBasePrecision)
	spotPriceForCheck := target.LastSpotAsk
	if spotPriceForCheck <= 0 {
		spotPriceForCheck = target.Candidate.MarkPrice
	}
	return spotQty, spotPriceForCheck, isQuantityTradable(spotQty, spotPriceForCheck, spotRule)
}

func (e *FundingRateSniperEngine) revertSpotOpenAfterFuturesFailure(ctx context.Context, target *MonitoredTarget, spotOrder SpotOrderResponse, spotPriceForCheck float64) error {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	spotExecutedQty := resolveSpotNetBaseQuantity(spotOrder, target.Candidate.SpotBaseAsset)
	if spotExecutedQty <= executionQtyTolerance {
		return nil
	}
	spotRule := e.spotRules[target.Candidate.Pair]
	residualQty := normalizeQuantity(spotExecutedQty, spotRule, target.Candidate.SpotBasePrecision)
	if residualQty <= executionQtyTolerance || !isQuantityTradable(residualQty, spotPriceForCheck, spotRule) {
		return fmt.Errorf("现货已成交但无法安全回滚: spotQty=%.8f", spotExecutedQty)
	}
	recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	recoveryOrder, err := e.placeDirectionalSpotMarketOrderIdempotent(recoverCtx, target.Candidate.Pair, direction.closeSpotSide(), residualQty, newClientOrderID("srevert", target.Candidate.Pair), direction.spotVenue(), "spot_"+strings.ToLower(direction.closeSpotSide())+"_revert", true)
	if err != nil {
		return fmt.Errorf("现货回滚失败: %w", err)
	}
	e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "revert_spot_after_futures_open_failure", fmt.Sprintf("direction=%s spotQty=%.8f revertedQty=%.8f", direction, spotExecutedQty, resolveSpotNetBaseQuantity(recoveryOrder, target.Candidate.SpotBaseAsset)))
	return nil
}

func (e *FundingRateSniperEngine) finalizeOpenHedge(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult, futuresExecution OrderExecutionSnapshot, spotOrder SpotOrderResponse, spotPriceForCheck float64) (OpenHedge, error) {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	filledFuturesQty := futuresExecution.ExecutedQty
	spotExecutedQty := resolveSpotNetBaseQuantity(spotOrder, target.Candidate.SpotBaseAsset)
	if spotExecutedQty <= executionQtyTolerance {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		defer cancel()
		if _, err := e.placeDirectionalFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, filledFuturesQty, direction); err != nil {
			return OpenHedge{}, fmt.Errorf("现货买单无有效成交且合约回补失败: %w", err)
		}
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_after_zero_spot_fill", fmt.Sprintf("filledFuturesQty=%.8f", filledFuturesQty))
		return OpenHedge{}, fmt.Errorf("现货买单无有效成交，已回补合约空单")
	}

	spotRule := e.spotRules[target.Candidate.Pair]
	finalSpotQty := spotExecutedQty
	additionalSpotFeeUSDT := 0.0
	if spotResidualQty := finalSpotQty - filledFuturesQty; spotResidualQty > executionQtyTolerance {
		residualQty := normalizeQuantity(spotResidualQty, spotRule, target.Candidate.SpotBasePrecision)
		if residualQty > executionQtyTolerance && isQuantityTradable(residualQty, spotPriceForCheck, spotRule) {
			recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
			recoveryOrder, err := e.placeDirectionalSpotChasingOrderWithOptions(recoverCtx, target.Candidate.Pair, direction.closeSpotSide(), residualQty, "srebalance", spotChaseOptions{}, direction.spotVenue())
			cancel()
			if err != nil {
				return OpenHedge{}, fmt.Errorf("现货超配后回滚失败: %w", err)
			}
			revertedQty := resolveSpotNetBaseQuantity(recoveryOrder, target.Candidate.SpotBaseAsset)
			finalSpotQty = math.Max(0, finalSpotQty-revertedQty)
			additionalSpotFeeUSDT += e.estimateSpotCommissionUSDT(ctx, recoveryOrder)
			e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "rebalance_spot_excess", fmt.Sprintf("direction=%s spotQty=%.8f revertedQty=%.8f", direction, spotExecutedQty, revertedQty))
		} else if residualQty > executionQtyTolerance {
			if err := e.noteDustResidual("open_recovery", target.Candidate.Symbol, target.Candidate.Pair, target.Candidate.SpotBaseAsset, residualQty, residualQty*maxFloat(spotPriceForCheck, target.Candidate.MarkPrice), "spot_excess_below_min_notional"); err != nil {
				e.logger.Printf("记录现货超买灰尘残差失败: %v", err)
			}
		}
	}

	hedgeQty := math.Min(filledFuturesQty, finalSpotQty)
	additionalFuturesFeeUSDT := 0.0
	if futuresResidualQty := filledFuturesQty - hedgeQty; futuresResidualQty > executionQtyTolerance {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeDirectionalFuturesCloseConfirmed(recoverCtx, target.Candidate.Symbol, futuresResidualQty, direction)
		cancel()
		if err != nil {
			return OpenHedge{}, fmt.Errorf("修正合约裸腿失败: %w", err)
		}
		additionalFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("open_recovery", target.Candidate.Symbol, "close_futures_residual", fmt.Sprintf("futuresResidualQty=%.8f hedgeQty=%.8f", futuresResidualQty, hedgeQty))
	}

	if hedgeQty <= executionQtyTolerance {
		return OpenHedge{}, fmt.Errorf("对冲后有效数量为 0")
	}

	openSpotQuoteQty := parseFloat(spotOrder.CummulativeQuoteQty)
	if spotExecutedQty > executionQtyTolerance && hedgeQty+executionQtyTolerance < spotExecutedQty {
		openSpotQuoteQty *= hedgeQty / spotExecutedQty
	}

	return OpenHedge{
		Target:              target.Candidate,
		FuturesOrder:        futureResult,
		SpotOrder:           spotOrder,
		Direction:           direction,
		SpotBaseQty:         formatFloat(hedgeQty),
		HedgeQty:            hedgeQty,
		HedgeQtyText:        formatFloat(hedgeQty),
		OpenSpotQuoteQty:    openSpotQuoteQty,
		OpenSpotFeeUSDT:     e.estimateSpotCommissionUSDT(ctx, spotOrder) + additionalSpotFeeUSDT,
		OpenFuturesAvgPrice: futuresExecution.AvgPrice,
		OpenFuturesFeeUSDT:  e.estimateFuturesExecutionFeeUSDT(ctx, futuresExecution) + additionalFuturesFeeUSDT,
	}, nil
}

func (e *FundingRateSniperEngine) validateFreshOpenHedge(ctx context.Context, hedge *OpenHedge) error {
	spotTicker, futuresTicker, err := e.fetchDualBookTicker(ctx, hedge.Target.Pair, hedge.Target.Symbol)
	if err != nil {
		return e.abortFreshOpenHedge(ctx, hedge, fmt.Sprintf("post_open_ticker_unavailable: %v", err))
	}
	adverseSpread, ok := adverseCloseSpreadRate(hedge.carryDirection(), spotTicker, futuresTicker)
	if !ok {
		return e.abortFreshOpenHedge(ctx, hedge, "post_open_spread_invalid")
	}
	immediateNetProfit := estimateReservedNetProfitRate(
		hedge.Target.EstimatedRate,
		adverseSpread,
		e.cfg.StrategyConfig.FeeReserveRate,
		e.cfg.StrategyConfig.ExitSpreadReserveRate,
	)
	if adverseSpread <= e.cfg.StrategyConfig.ExitSpreadReserveRate && immediateNetProfit > e.cfg.RiskConfig.ImmediateInvalidateRate {
		return nil
	}
	return e.abortFreshOpenHedge(
		ctx,
		hedge,
		fmt.Sprintf(
			"adverse_spread=%.6f reserve=%.6f immediate_net=%.6f invalidate=%.6f",
			adverseSpread,
			e.cfg.StrategyConfig.ExitSpreadReserveRate,
			immediateNetProfit,
			e.cfg.RiskConfig.ImmediateInvalidateRate,
		),
	)
}

func (e *FundingRateSniperEngine) abortFreshOpenHedge(ctx context.Context, hedge *OpenHedge, reason string) error {
	closeCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	alerts, err := e.closeHedgePositionParallel(closeCtx, closeSequencePlan{
		hedge:             hedge,
		futuresOrder:      e.buildProtectedCloseFuturesOrder(closeCtx, *hedge),
		preferPassiveSpot: false,
	})
	if err != nil {
		return fmt.Errorf("开仓后逆向选择复核失败且即时撤退失败: %w", err)
	}
	if len(alerts) > 0 {
		e.logger.Printf("开仓后即时撤退告警 %s: %s", hedge.Target.Symbol, strings.Join(alerts, " | "))
	}
	e.recordExecutionAction("open_recovery", hedge.Target.Symbol, "abort_after_adverse_selection", reason)
	return nonFatalOpenAbortError{message: fmt.Sprintf("开仓后逆向选择复核未通过，已即时撤退: %s", reason)}
}

func (e *FundingRateSniperEngine) closeHedgePosition(ctx context.Context, hedge *OpenHedge, futureResult FuturesBatchOrderResult, preferPassiveSpot bool) ([]string, error) {
	alerts := make([]string, 0, 2)
	direction := hedge.carryDirection()
	if err := e.ensureSymbolNotLockedByExposure(hedge.Target.Symbol, "close_hedge"); err != nil {
		return alerts, err
	}
	futuresExecution, resolveErr := e.resolveFuturesOrderExecution(ctx, hedge.Target.Symbol, futureResult.ClientOrderID, futureResult)
	if resolveErr != nil {
		if isUnknownOrderStateError(resolveErr) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                 exposureRescueModeClose,
				Phase:                "close_futures_unknown",
				Direction:            direction,
				Symbol:               hedge.Target.Symbol,
				Pair:                 hedge.Target.Pair,
				SpotBaseAsset:        hedge.Target.SpotBaseAsset,
				SpotBasePrecision:    hedge.Target.SpotBasePrecision,
				SpotReferencePrice:   hedge.LastKnownSpotPrice(),
				InitialHedgeQty:      hedge.HedgeQty,
				FuturesClientOrderID: futureResult.ClientOrderID,
			})
			if rescueErr != nil {
				return alerts, fmt.Errorf("合约平仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return alerts, fmt.Errorf("合约平仓终态未知，已转入 ExposureManager 托管")
		}
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
		if spotQty > executionQtyTolerance {
			if err := e.noteDustResidual("close_recovery", hedge.Target.Symbol, hedge.Target.Pair, hedge.Target.SpotBaseAsset, spotQty, spotQty*math.Max(spotReferencePrice, hedge.Target.MarkPrice), "close_spot_below_min_notional"); err != nil {
				e.logger.Printf("记录现货平仓灰尘残差失败: %v", err)
			}
		}
		if closeableSpotQty*math.Max(spotReferencePrice, hedge.Target.MarkPrice) >= e.cfg.ResidualSpotAlertUSDT {
			recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
			recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, closeableSpotQty, direction)
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
	var (
		spotOrder SpotOrderResponse
		spotErr   error
	)
	spotCloseSide := direction.closeSpotSide()
	if preferPassiveSpot && direction.spotVenue() == spotExecutionVenueSpot && spotCloseSide == "SELL" {
		spotOrder, spotErr = e.placeSpotPassiveMakerClose(ctx, hedge.Target.Pair, spotQty, "sclosemaker")
		if spotErr == nil && parseFloat(spotOrder.ExecutedQty)+executionQtyTolerance < spotQty {
			chaseOrder, chaseErr := e.placeDirectionalSpotChasingOrderWithOptions(ctx, hedge.Target.Pair, spotCloseSide, maxFloat(spotQty-parseFloat(spotOrder.ExecutedQty), 0), "sclosechase", spotChaseOptions{}, direction.spotVenue())
			if chaseErr != nil {
				spotErr = chaseErr
			} else {
				spotOrder = mergeSpotOrderResponses(spotOrder, chaseOrder)
			}
		}
	} else {
		spotOrder, spotErr = e.placeDirectionalSpotMarketOrderIdempotent(ctx, hedge.Target.Pair, spotCloseSide, spotQty, clientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(spotCloseSide), true)
	}
	if spotErr != nil {
		if isUnknownOrderStateError(spotErr) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                    exposureRescueModeClose,
				Phase:                   "close_spot_unknown",
				Direction:               direction,
				Symbol:                  hedge.Target.Symbol,
				Pair:                    hedge.Target.Pair,
				SpotBaseAsset:           hedge.Target.SpotBaseAsset,
				SpotBasePrecision:       hedge.Target.SpotBasePrecision,
				SpotReferencePrice:      spotReferencePrice,
				InitialHedgeQty:         hedge.HedgeQty,
				SpotClientOrderID:       clientOrderID,
				KnownFuturesExecutedQty: closeableSpotQty,
			})
			if rescueErr != nil {
				return alerts, fmt.Errorf("现货平仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return alerts, fmt.Errorf("现货平仓终态未知，已转入 ExposureManager 托管")
		}
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, closeableSpotQty, direction)
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
		recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, residualQty, direction)
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

func (e *FundingRateSniperEngine) closeHedgePositionParallel(ctx context.Context, plan closeSequencePlan) ([]string, error) {
	hedge := plan.hedge
	direction := hedge.carryDirection()
	spotQty, spotReferencePrice, spotTradable := closeSpotTarget(hedge, hedge.HedgeQty, e.spotRules[hedge.Target.Pair])
	if !spotTradable {
		futuresExecution, err := e.placeSingleFuturesOrderIdempotent(ctx, plan.futuresOrder, "futures_close_single")
		if err != nil {
			return nil, fmt.Errorf("并发平仓回退到单腿模式时，合约平仓失败: %w", err)
		}
		return e.closeHedgePosition(ctx, hedge, futuresExecutionToResult(futuresExecution, plan.futuresOrder), plan.preferPassiveSpot)
	}

	type spotCloseResult struct {
		order SpotOrderResponse
		err   error
	}
	type futuresCloseResult struct {
		snapshot OrderExecutionSnapshot
		err      error
	}
	spotClientOrderID := newClientOrderID("sclose", hedge.Target.Pair)

	spotResultCh := make(chan spotCloseResult, 1)
	futuresResultCh := make(chan futuresCloseResult, 1)
	go func() {
		var (
			order SpotOrderResponse
			err   error
		)
		spotCloseSide := direction.closeSpotSide()
		if plan.preferPassiveSpot && direction.spotVenue() == spotExecutionVenueSpot && spotCloseSide == "SELL" {
			order, err = e.placeSpotPassiveMakerClose(ctx, hedge.Target.Pair, spotQty, "sclosemaker")
			if err == nil && parseFloat(order.ExecutedQty)+executionQtyTolerance < spotQty {
				var chaseOrder SpotOrderResponse
				chaseOrder, err = e.placeDirectionalSpotChasingOrderWithOptions(ctx, hedge.Target.Pair, spotCloseSide, maxFloat(spotQty-parseFloat(order.ExecutedQty), 0), "sclosechase", spotChaseOptions{}, direction.spotVenue())
				if err == nil {
					order = mergeSpotOrderResponses(order, chaseOrder)
				}
			}
		} else {
			order, err = e.placeDirectionalSpotMarketOrderIdempotent(ctx, hedge.Target.Pair, spotCloseSide, spotQty, spotClientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(spotCloseSide), true)
		}
		spotResultCh <- spotCloseResult{order: order, err: err}
	}()
	go func() {
		snapshot, err := e.placeSingleFuturesOrderIdempotent(ctx, plan.futuresOrder, "futures_close_single")
		futuresResultCh <- futuresCloseResult{snapshot: snapshot, err: err}
	}()

	spotResult := <-spotResultCh
	futuresResult := <-futuresResultCh
	filledFuturesQty := math.Min(hedge.HedgeQty, futuresResult.snapshot.ExecutedQty)
	if futuresResult.err != nil {
		if isUnknownOrderStateError(futuresResult.err) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                 exposureRescueModeClose,
				Phase:                "parallel_close_futures_unknown",
				Direction:            direction,
				Symbol:               hedge.Target.Symbol,
				Pair:                 hedge.Target.Pair,
				SpotBaseAsset:        hedge.Target.SpotBaseAsset,
				SpotBasePrecision:    hedge.Target.SpotBasePrecision,
				SpotReferencePrice:   spotReferencePrice,
				InitialHedgeQty:      hedge.HedgeQty,
				SpotClientOrderID:    spotClientOrderID,
				FuturesClientOrderID: plan.futuresOrder.NewClientOrderID,
			})
			if rescueErr != nil {
				return nil, fmt.Errorf("并发合约平仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return nil, fmt.Errorf("并发合约平仓终态未知，已转入 ExposureManager 托管")
		}
		if spotResult.err == nil {
			if err := e.revertSpotCloseAfterFuturesFailure(ctx, hedge, spotResult.order, spotReferencePrice); err != nil {
				return nil, fmt.Errorf("并发合约平仓失败且现货回补失败: %w", err)
			}
		}
		return nil, fmt.Errorf("并发合约平仓未确认成交: %w", futuresResult.err)
	}
	if filledFuturesQty <= executionQtyTolerance {
		if spotResult.err == nil {
			if err := e.revertSpotCloseAfterFuturesFailure(ctx, hedge, spotResult.order, spotReferencePrice); err != nil {
				return nil, fmt.Errorf("并发合约零成交且现货回补失败: %w", err)
			}
		}
		return nil, fmt.Errorf("并发合约平仓成交数量为 0")
	}

	alerts := make([]string, 0, 2)
	if futuresResult.snapshot.ExecutedQty+executionQtyTolerance < hedge.HedgeQty {
		alerts = append(alerts, fmt.Sprintf("合约平仓部分成交 %s: requested=%.8f executed=%.8f", hedge.Target.Symbol, hedge.HedgeQty, futuresResult.snapshot.ExecutedQty))
	}
	hedge.CloseFuturesAvgPrice = futuresResult.snapshot.AvgPrice
	hedge.CloseFuturesFeeUSDT = e.estimateFuturesExecutionFeeUSDT(ctx, futuresResult.snapshot)
	hedge.CloseFuturesRealizedPnL = futuresResult.snapshot.RealizedPnL

	if spotResult.err != nil {
		if isUnknownOrderStateError(spotResult.err) {
			rescueErr := e.scheduleExposureRescue(ctx, exposureRescueTask{
				Mode:                    exposureRescueModeClose,
				Phase:                   "parallel_close_spot_unknown",
				Direction:               direction,
				Symbol:                  hedge.Target.Symbol,
				Pair:                    hedge.Target.Pair,
				SpotBaseAsset:           hedge.Target.SpotBaseAsset,
				SpotBasePrecision:       hedge.Target.SpotBasePrecision,
				SpotReferencePrice:      spotReferencePrice,
				InitialHedgeQty:         hedge.HedgeQty,
				FuturesClientOrderID:    plan.futuresOrder.NewClientOrderID,
				KnownFuturesExecutedQty: filledFuturesQty,
			})
			if rescueErr != nil {
				return alerts, fmt.Errorf("并发现货平仓终态未知且 ExposureManager 托管失败: %w", rescueErr)
			}
			return alerts, fmt.Errorf("并发现货平仓终态未知，已转入 ExposureManager 托管")
		}
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, filledFuturesQty, direction)
		cancel()
		if err != nil {
			return alerts, fmt.Errorf("并发现货平仓失败且恢复空头失败: spotErr=%v recoverErr=%w", spotResult.err, err)
		}
		hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "reopen_short_after_parallel_spot_close_failure", fmt.Sprintf("closeableSpotQty=%.8f spotErr=%v", filledFuturesQty, spotResult.err))
		return append(alerts, fmt.Sprintf("现货平仓失败 %s: %v，已恢复空头对冲", hedge.Target.Pair, spotResult.err)), nil
	}

	soldSpotQty := parseFloat(spotResult.order.ExecutedQty)
	if soldSpotQty <= executionQtyTolerance {
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, filledFuturesQty, direction)
		cancel()
		if err != nil {
			return alerts, fmt.Errorf("并发现货平仓无有效成交且恢复空头失败: %w", err)
		}
		hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		return append(alerts, fmt.Sprintf("现货平仓无有效成交 %s，已恢复空头对冲", hedge.Target.Pair)), nil
	}

	hedge.CloseSpotExecutedQty = soldSpotQty
	hedge.CloseSpotQuoteQty = parseFloat(spotResult.order.CummulativeQuoteQty)
	hedge.CloseSpotFeeUSDT = e.estimateSpotCommissionUSDT(ctx, spotResult.order)
	effectiveCloseQty := math.Min(filledFuturesQty, soldSpotQty)
	if soldSpotQty > executionQtyTolerance && effectiveCloseQty+executionQtyTolerance < soldSpotQty {
		residualQty := normalizeQuantity(soldSpotQty-effectiveCloseQty, e.spotRules[hedge.Target.Pair], hedge.Target.SpotBasePrecision)
		if residualQty > executionQtyTolerance && isQuantityTradable(residualQty, spotReferencePrice, e.spotRules[hedge.Target.Pair]) {
			recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
			recoveryOrder, err := e.placeDirectionalSpotMarketOrderIdempotent(recoverCtx, hedge.Target.Pair, direction.openSpotSide(), residualQty, newClientOrderID("sclose_revert", hedge.Target.Pair), direction.spotVenue(), "spot_"+strings.ToLower(direction.openSpotSide())+"_revert", true)
			cancel()
			if err != nil {
				return alerts, fmt.Errorf("并发现货超卖后回补失败: %w", err)
			}
			hedge.CloseSpotFeeUSDT += e.estimateSpotCommissionUSDT(ctx, recoveryOrder)
			hedge.CloseSpotQuoteQty *= effectiveCloseQty / soldSpotQty
			e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "rebalance_spot_excess_after_parallel_close", fmt.Sprintf("soldSpotQty=%.8f effectiveCloseQty=%.8f", soldSpotQty, effectiveCloseQty))
		}
	}
	if filledFuturesQty > effectiveCloseQty+executionQtyTolerance {
		residualQty := filledFuturesQty - effectiveCloseQty
		recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
		recoveryExecution, err := e.placeDirectionalFuturesReopen(recoverCtx, hedge.Target.Symbol, residualQty, direction)
		cancel()
		if err != nil {
			return alerts, fmt.Errorf("并发平仓后恢复剩余空头失败: %w", err)
		}
		hedge.CloseFuturesFeeUSDT += e.estimateFuturesExecutionFeeUSDT(ctx, recoveryExecution)
		e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "reopen_short_after_parallel_partial_spot_close", fmt.Sprintf("filledFuturesQty=%.8f effectiveCloseQty=%.8f residualQty=%.8f", filledFuturesQty, effectiveCloseQty, residualQty))
		alerts = append(alerts, fmt.Sprintf("现货平仓部分成交 %s: requested=%.8f executed=%.8f，已恢复剩余空头", hedge.Target.Pair, filledFuturesQty, effectiveCloseQty))
	}
	hedge.CloseFuturesExecutedQty = effectiveCloseQty
	hedge.CloseSpotExecutedQty = effectiveCloseQty
	if effectiveCloseQty <= executionQtyTolerance {
		return alerts, fmt.Errorf("并发平仓后有效成交数量为 0")
	}
	return alerts, nil
}

func closeSpotTarget(hedge *OpenHedge, closeableQty float64, spotRule SymbolTradeRules) (float64, float64, bool) {
	spotQty := normalizeQuantity(closeableQty, spotRule, hedge.Target.SpotBasePrecision)
	spotReferencePrice := hedge.LastKnownSpotPrice()
	if spotReferencePrice <= 0 {
		spotReferencePrice = hedge.Target.MarkPrice
	}
	return spotQty, spotReferencePrice, isQuantityTradable(spotQty, spotReferencePrice, spotRule)
}

func (e *FundingRateSniperEngine) revertSpotCloseAfterFuturesFailure(ctx context.Context, hedge *OpenHedge, spotOrder SpotOrderResponse, spotReferencePrice float64) error {
	direction := hedge.carryDirection()
	spotExecutedQty := parseFloat(spotOrder.ExecutedQty)
	if spotExecutedQty <= executionQtyTolerance {
		return nil
	}
	spotRule := e.spotRules[hedge.Target.Pair]
	recoverQty := normalizeQuantity(spotExecutedQty, spotRule, hedge.Target.SpotBasePrecision)
	if recoverQty <= executionQtyTolerance || !isQuantityTradable(recoverQty, spotReferencePrice, spotRule) {
		return fmt.Errorf("现货已平部分但无法安全回滚: spotQty=%.8f", spotExecutedQty)
	}
	recoverCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	recoveryOrder, err := e.placeDirectionalSpotMarketOrderIdempotent(recoverCtx, hedge.Target.Pair, direction.openSpotSide(), recoverQty, newClientOrderID("sclose_revert", hedge.Target.Pair), direction.spotVenue(), "spot_"+strings.ToLower(direction.openSpotSide())+"_revert", true)
	if err != nil {
		return fmt.Errorf("现货回补失败: %w", err)
	}
	e.recordExecutionAction("close_recovery", hedge.Target.Symbol, "revert_spot_after_futures_close_failure", fmt.Sprintf("direction=%s spotQty=%.8f revertedQty=%.8f", direction, spotExecutedQty, resolveSpotNetBaseQuantity(recoveryOrder, hedge.Target.SpotBaseAsset)))
	return nil
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

	estimatedRate, lastFundingRate, fundingJump, basisRate, _ := estimateScanFundingSignal(item)
	target.Candidate.Direction = fundingDirectionFromRate(estimatedRate)
	target.Candidate.EstimatedRate = estimatedRate
	target.Candidate.EstimatedRateText = formatFloat(estimatedRate)
	target.Candidate.LastFundingRate = lastFundingRate
	target.Candidate.FundingRateJump = fundingJump
	target.Candidate.BasisRate = basisRate
	target.Candidate.ScanScoreRate = liveFundingPriorityScore(e.cfg.StrategyConfig.LiveFundingDirectionMode, estimatedRate, basisRate)
	target.Candidate.MarkPrice = parseFloat(item.MarkPrice)
	if nextFundingTime := time.UnixMilli(item.NextFundingTime); !nextFundingTime.IsZero() {
		target.Candidate.NextFundingTime = nextFundingTime
	}
	target.LastEstimateAt = time.Now()
	return nil
}

func (e *FundingRateSniperEngine) fetchDualBookTicker(ctx context.Context, spotSymbol string, futuresSymbol string) (BookTicker, BookTicker, error) {
	spotTicker, spotErr := e.fetchSpotBookTicker(ctx, spotSymbol)
	if spotErr != nil {
		return BookTicker{}, BookTicker{}, fmt.Errorf("现货盘口失败: %w", spotErr)
	}
	futuresTicker, futuresErr := e.fetchFuturesBookTicker(ctx, futuresSymbol)
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

	receivedAt := time.Now().UTC()
	return ParsedOrderBookDepth{
			LastUpdateID: spotDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(spotDepthRaw.Bids),
			Asks:         parseDepthLevels(spotDepthRaw.Asks),
			ReceivedAt:   receivedAt,
		}, ParsedOrderBookDepth{
			LastUpdateID: futuresDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(futuresDepthRaw.Bids),
			Asks:         parseDepthLevels(futuresDepthRaw.Asks),
			ReceivedAt:   receivedAt,
		}, nil
}

const (
	depthStabilityMaxAge         = 750 * time.Millisecond
	depthStabilityLookbackWindow = 1500 * time.Millisecond
	depthStabilityObserveRatio   = 0.80
	depthStabilityCooldownRatio  = 0.65
	depthStabilityPriceDriftRate = 0.0008
)

func openDepthLevelsForDirection(direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth) ([]DepthLevel, []DepthLevel) {
	switch direction {
	case fundingCarryDirectionNegative:
		return spotDepth.Bids, futuresDepth.Asks
	default:
		return spotDepth.Asks, futuresDepth.Bids
	}
}

func visibleDepthNotionalForDirection(direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth) (float64, float64) {
	spotLevels, futuresLevels := openDepthLevelsForDirection(direction, spotDepth, futuresDepth)
	return estimateVisibleDepthNotional(spotLevels), estimateVisibleDepthNotional(futuresLevels)
}

func openEntrySpreadRate(direction fundingCarryDirection, spotVWAP float64, futuresVWAP float64) float64 {
	if spotVWAP <= 0 || futuresVWAP <= 0 {
		return 0
	}
	switch direction {
	case fundingCarryDirectionNegative:
		return (futuresVWAP - spotVWAP) / spotVWAP
	default:
		return (spotVWAP - futuresVWAP) / spotVWAP
	}
}

func priceMovedAdversely(direction fundingCarryDirection, previousSpot float64, currentSpot float64, previousFutures float64, currentFutures float64) bool {
	switch direction {
	case fundingCarryDirectionNegative:
		spotWorse := previousSpot > 0 && currentSpot > 0 && currentSpot < previousSpot*(1-depthStabilityPriceDriftRate)
		futuresWorse := previousFutures > 0 && currentFutures > 0 && currentFutures > previousFutures*(1+depthStabilityPriceDriftRate)
		return spotWorse || futuresWorse
	default:
		spotWorse := previousSpot > 0 && currentSpot > 0 && currentSpot > previousSpot*(1+depthStabilityPriceDriftRate)
		futuresWorse := previousFutures > 0 && currentFutures > 0 && currentFutures < previousFutures*(1-depthStabilityPriceDriftRate)
		return spotWorse || futuresWorse
	}
}

func (e *FundingRateSniperEngine) evaluateOpenDepthStability(target *MonitoredTarget, direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth, currentSpotBest float64, currentFuturesBest float64) (MarketRiskDecision, string, bool, float64, float64, float64) {
	spotVisible, futuresVisible := visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
	now := time.Now().UTC()
	if !spotDepth.ReceivedAt.IsZero() {
		spotAge := now.Sub(spotDepth.ReceivedAt)
		if spotAge > depthStabilityMaxAge {
			return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
				fmt.Sprintf("spot depth 延迟过高: age=%s limit=%s", spotAge.Round(time.Millisecond), depthStabilityMaxAge), true, spotVisible, futuresVisible, 0
		}
	}
	if !futuresDepth.ReceivedAt.IsZero() {
		futuresAge := now.Sub(futuresDepth.ReceivedAt)
		if futuresAge > depthStabilityMaxAge {
			return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
				fmt.Sprintf("futures depth 延迟过高: age=%s limit=%s", futuresAge.Round(time.Millisecond), depthStabilityMaxAge), true, spotVisible, futuresVisible, 0
		}
	}
	if target == nil || target.LastObserveTime.IsZero() || now.Sub(target.LastObserveTime) > depthStabilityLookbackWindow {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, 0
	}
	previousVisible := minPositiveFloat(target.LastSpotVisibleNotional, target.LastFuturesVisibleNotional)
	currentVisible := minPositiveFloat(spotVisible, futuresVisible)
	if previousVisible <= 0 || currentVisible <= 0 {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, 0
	}
	ratio := currentVisible / previousVisible
	if !priceMovedAdversely(direction, target.LastSpotAsk, currentSpotBest, target.LastFuturesBid, currentFuturesBest) {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, ratio
	}
	switch {
	case ratio < depthStabilityCooldownRatio:
		return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
			fmt.Sprintf("可见深度快速塌陷: ratio=%.4f prev=%.6f current=%.6f", ratio, previousVisible, currentVisible), true, spotVisible, futuresVisible, ratio
	case ratio < depthStabilityObserveRatio:
		return MarketRiskDecision{Category: "depth_stability", Severity: "minor", Action: "observe"},
			fmt.Sprintf("可见深度快速恶化: ratio=%.4f prev=%.6f current=%.6f", ratio, previousVisible, currentVisible), true, spotVisible, futuresVisible, ratio
	default:
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, ratio
	}
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

func isProtectedIOCRequestType(orderType string, tif string) bool {
	return strings.EqualFold(strings.TrimSpace(orderType), "LIMIT") &&
		strings.EqualFold(strings.TrimSpace(tif), protectedExecutionIOC)
}

func protectedIOCUnfilledRatio(requestedQty float64, executedQty float64) float64 {
	if requestedQty <= executionQtyTolerance {
		return 0
	}
	executedQty = maxFloat(executedQty, 0)
	if executedQty > requestedQty {
		executedQty = requestedQty
	}
	return maxFloat(requestedQty-executedQty, 0) / requestedQty
}

func (e *FundingRateSniperEngine) noteProtectedIOCResult(symbol string, market string, requestedQty float64, executedQty float64, status string) {
	if e == nil || requestedQty <= executionQtyTolerance {
		return
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	unfilledRatio := protectedIOCUnfilledRatio(requestedQty, executedQty)
	partial := executedQty > executionQtyTolerance && executedQty+executionQtyTolerance < requestedQty

	e.platformRiskMu.Lock()
	e.protectedIOCAttempts++
	e.protectedIOCUnfilledSum += unfilledRatio
	if status == "CANCELED" {
		e.protectedIOCCanceled++
	}
	if status == "EXPIRED" {
		e.protectedIOCExpired++
	}
	if partial {
		e.protectedIOCPartial++
	}
	if status == "CANCELED" || status == "EXPIRED" || partial {
		e.lastExceptionCategory = "protected_ioc"
		e.lastExceptionSeverity = "major"
		e.lastExceptionDetail = fmt.Sprintf(
			"market=%s symbol=%s status=%s requested=%.8f executed=%.8f unfilled_ratio=%.4f",
			nonEmpty(strings.TrimSpace(market), "unknown"),
			nonEmpty(symbol, "-"),
			nonEmpty(status, "UNKNOWN"),
			requestedQty,
			executedQty,
			unfilledRatio,
		)
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) protectedIOCStatsLocked() (attempts int, canceled int, expired int, partial int, avgUnfilledRatio float64, cancelRate float64) {
	attempts = e.protectedIOCAttempts
	canceled = e.protectedIOCCanceled
	expired = e.protectedIOCExpired
	partial = e.protectedIOCPartial
	if attempts <= 0 {
		return attempts, canceled, expired, partial, 0, 0
	}
	avgUnfilledRatio = e.protectedIOCUnfilledSum / float64(attempts)
	cancelRate = float64(canceled+expired) / float64(attempts)
	return attempts, canceled, expired, partial, avgUnfilledRatio, cancelRate
}

func hasConfirmedOrderExecution(snapshot OrderExecutionSnapshot) bool {
	return snapshot.OrderID != 0 && (snapshot.ExecutedQty > executionQtyTolerance || isClosedOrderStatus(snapshot.Status))
}

func hasConfirmedSpotOrderResult(order SpotOrderResponse) bool {
	return order.OrderID != 0 && (parseFloat(order.ExecutedQty) > executionQtyTolerance || isClosedOrderStatus(order.Status))
}

func hasConfirmedFuturesOrderResult(result FuturesBatchOrderResult) bool {
	return result.OrderID != 0 && (parseFloat(result.ExecutedQty) > executionQtyTolerance || isClosedOrderStatus(result.Status))
}

func unknownOrderReconcileTimeout(cfg EngineConfig) time.Duration {
	if cfg.ExecutionRecoveryTimeout > 0 {
		return cfg.ExecutionRecoveryTimeout
	}
	return 8 * time.Second
}

func (e *FundingRateSniperEngine) reconcileSpotOrderTerminalState(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	if strings.TrimSpace(clientOrderID) == "" {
		return SpotOrderResponse{}, unknownOrderStateError{Market: "spot", Symbol: symbol, Reason: "clientOrderId 为空"}
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, unknownOrderReconcileTimeout(e.cfg))
	defer cancel()
	backoff := 250 * time.Millisecond
	var lastErr error
	for {
		queried, err := e.querySpotOrderByClientID(reconcileCtx, symbol, clientOrderID)
		if err == nil && hasConfirmedSpotOrderResult(queried) {
			return queried, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("spot status=%s executedQty=%s", queried.Status, queried.ExecutedQty)
		} else {
			lastErr = err
		}
		if reconcileCtx.Err() != nil {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-reconcileCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
			if backoff > time.Second {
				backoff = time.Second
			}
		}
	}
	return SpotOrderResponse{}, unknownOrderStateError{
		Market:        "spot",
		Symbol:        symbol,
		ClientOrderID: clientOrderID,
		Reason:        nonEmpty(errorString(lastErr), "reconcile timeout"),
	}
}

func (e *FundingRateSniperEngine) reconcileFuturesOrderTerminalState(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	if strings.TrimSpace(clientOrderID) == "" {
		return FuturesBatchOrderResult{}, unknownOrderStateError{Market: "futures", Symbol: symbol, Reason: "clientOrderId 为空"}
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, unknownOrderReconcileTimeout(e.cfg))
	defer cancel()
	backoff := 250 * time.Millisecond
	var lastErr error
	for {
		queried, err := e.queryFuturesOrderByClientID(reconcileCtx, symbol, clientOrderID)
		if err == nil && hasConfirmedFuturesOrderResult(queried) {
			return queried, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("futures status=%s executedQty=%s", queried.Status, queried.ExecutedQty)
		} else {
			lastErr = err
		}
		if reconcileCtx.Err() != nil {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-reconcileCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
			if backoff > time.Second {
				backoff = time.Second
			}
		}
	}
	return FuturesBatchOrderResult{}, unknownOrderStateError{
		Market:        "futures",
		Symbol:        symbol,
		ClientOrderID: clientOrderID,
		Reason:        nonEmpty(errorString(lastErr), "reconcile timeout"),
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (e *FundingRateSniperEngine) handleUnknownOrderState(symbol string, phase string, err error) error {
	detail := nonEmpty(errorString(err), "订单终态未知")
	if strings.TrimSpace(symbol) != "" {
		e.setSymbolCooldown(symbol, "unknown_order", "critical", detail)
	}
	if incidentErr := e.recordExecutionIncident(phase, symbol, detail); incidentErr != nil {
		e.logger.Printf("记录未知订单执行事故失败: %v", incidentErr)
	}
	return e.tripPlatformFuse("unknown_order", "critical", fmt.Sprintf("symbol=%s phase=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(phase, "unknown_order"), detail))
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
		for idx := range result {
			if idx >= len(orders) || !futuresProtectedOpenFallbackNeeded(orders[idx], result[idx]) {
				continue
			}
			snapshot, fallbackErr := e.placeFuturesProtectedOpenDualTrack(ctx, orders[idx], "futures_batch_open_single", e.submitSingleFuturesOrderIdempotent)
			if fallbackErr != nil {
				result[idx] = FuturesBatchOrderResult{
					Symbol:        orders[idx].Symbol,
					ClientOrderID: orders[idx].NewClientOrderID,
					Msg:           fallbackErr.Error(),
				}
				continue
			}
			result[idx] = futuresExecutionToResult(snapshot, orders[idx])
		}
		return result, nil
	}
	if !isUnknownOrderRequestError(err) {
		return nil, err
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

func (e *FundingRateSniperEngine) placeDirectionalFuturesCloseConfirmed(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection) (OrderExecutionSnapshot, error) {
	side := resolvedFundingCarryDirection(direction, 0).closeFuturesSide()
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, side, quantity, true, newClientOrderID("ftrim", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesCloseConfirmed(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeDirectionalFuturesCloseConfirmed(ctx, symbol, quantity, fundingCarryDirectionPositive)
}

func (e *FundingRateSniperEngine) placeDirectionalFuturesReopen(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection) (OrderExecutionSnapshot, error) {
	side := resolvedFundingCarryDirection(direction, 0).openFuturesSide()
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, side, quantity, false, newClientOrderID("frescue", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesReopenShort(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeDirectionalFuturesReopen(ctx, symbol, quantity, fundingCarryDirectionPositive)
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
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := e.noteOrderPlaced("futures_market_"+strings.ToLower(side), fmt.Sprintf("%s qty=%s reduceOnly=%t", symbol, formatFloat(quantity), reduceOnly), 1, !reduceOnly); err != nil {
		return OrderExecutionSnapshot{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.futuresOrderRequestPath(), params)
	if err != nil {
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return OrderExecutionSnapshot{}, err
		}
		e.noteOrderUncertainty(symbol, "futures_market_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
			if protectedIOC {
				e.noteProtectedIOCResult(symbol, "futures", quantity, snapshot.ExecutedQty, snapshot.Status)
			}
			return snapshot, nil
		} else if blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		}
		queried, queryErr := e.queryFuturesOrderByClientID(ctx, symbol, clientOrderID)
		if queryErr != nil {
			queried = FuturesBatchOrderResult{
				Symbol:        symbol,
				ClientOrderID: clientOrderID,
			}
		}
		snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, queried)
		if resolveErr == nil && protectedIOC {
			e.noteProtectedIOCResult(symbol, "futures", quantity, snapshot.ExecutedQty, snapshot.Status)
		}
		return snapshot, resolveErr
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return OrderExecutionSnapshot{}, fmt.Errorf("解析合约单笔市价单回执失败: %w", err)
	}
	if result.OrderID != 0 && isTerminalOrderStatus(result.Status) {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCResult(symbol, "futures", quantity, snapshot.ExecutedQty, snapshot.Status)
		}
		return snapshot, nil
	}
	snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, result)
	if resolveErr == nil && protectedIOC {
		e.noteProtectedIOCResult(symbol, "futures", quantity, snapshot.ExecutedQty, snapshot.Status)
	}
	return snapshot, resolveErr
}

func (e *FundingRateSniperEngine) submitSingleFuturesOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest, action string) (OrderExecutionSnapshot, error) {
	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", order.Side)
	params.Set("type", order.Type)
	params.Set("quantity", order.Quantity)
	if order.Price != "" {
		params.Set("price", order.Price)
	}
	if order.TimeInForce != "" {
		params.Set("timeInForce", order.TimeInForce)
	}
	if order.PriceProtect != "" {
		params.Set("priceProtect", order.PriceProtect)
	}
	if order.NewClientOrderID != "" {
		params.Set("newClientOrderId", order.NewClientOrderID)
	}
	if order.NewOrderRespType != "" {
		params.Set("newOrderRespType", order.NewOrderRespType)
	}
	if order.ReduceOnly != "" {
		params.Set("reduceOnly", order.ReduceOnly)
	}
	if order.SelfTradeMode != "" {
		params.Set("selfTradePreventionMode", order.SelfTradeMode)
	}
	requestedQty := parseFloat(order.Quantity)
	protectedIOC := isProtectedIOCRequestType(order.Type, order.TimeInForce)
	if err := e.noteOrderPlaced(action, fmt.Sprintf("%s qty=%s", order.Symbol, order.Quantity), 1, strings.Contains(strings.ToLower(action), "open")); err != nil {
		return OrderExecutionSnapshot{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.futuresOrderRequestPath(), params)
	if err != nil {
		if order.NewClientOrderID == "" || !isUnknownOrderRequestError(err) {
			return OrderExecutionSnapshot{}, err
		}
		e.noteOrderUncertainty(order.Symbol, action+"_fallback", fmt.Sprintf("clientOrderId=%s err=%v", order.NewClientOrderID, err))
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, order.Symbol, order.NewClientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
			if protectedIOC {
				e.noteProtectedIOCResult(order.Symbol, "futures", requestedQty, snapshot.ExecutedQty, snapshot.Status)
			}
			return snapshot, nil
		} else if blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		}
		queried, queryErr := e.queryFuturesOrderByClientID(ctx, order.Symbol, order.NewClientOrderID)
		if queryErr != nil {
			queried = FuturesBatchOrderResult{
				Symbol:        order.Symbol,
				ClientOrderID: order.NewClientOrderID,
			}
		}
		snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, order.Symbol, order.NewClientOrderID, queried)
		if resolveErr == nil && protectedIOC {
			e.noteProtectedIOCResult(order.Symbol, "futures", requestedQty, snapshot.ExecutedQty, snapshot.Status)
		}
		return snapshot, resolveErr
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return OrderExecutionSnapshot{}, fmt.Errorf("解析合约单笔回执失败: %w", err)
	}
	if parseFloat(result.ExecutedQty) > 0 {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCResult(order.Symbol, "futures", requestedQty, snapshot.ExecutedQty, snapshot.Status)
		}
		return snapshot, nil
	}
	if result.OrderID != 0 && isTerminalOrderStatus(result.Status) {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCResult(order.Symbol, "futures", requestedQty, snapshot.ExecutedQty, snapshot.Status)
		}
		return snapshot, nil
	}
	snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, order.Symbol, order.NewClientOrderID, result)
	if resolveErr == nil && protectedIOC {
		e.noteProtectedIOCResult(order.Symbol, "futures", requestedQty, snapshot.ExecutedQty, snapshot.Status)
	}
	return snapshot, resolveErr
}

func (e *FundingRateSniperEngine) placeSingleFuturesOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest, action string) (OrderExecutionSnapshot, error) {
	if isOpenProtectedFuturesOrder(order) && strings.Contains(strings.ToLower(action), "open") {
		return e.placeFuturesProtectedOpenDualTrack(ctx, order, action, e.submitSingleFuturesOrderIdempotent)
	}
	return e.submitSingleFuturesOrderIdempotent(ctx, order, action)
}

func (e *FundingRateSniperEngine) placeSingleFuturesOpenOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest) (OrderExecutionSnapshot, error) {
	return e.placeSingleFuturesOrderIdempotent(ctx, order, "futures_open_single")
}

func futuresExecutionToResult(snapshot OrderExecutionSnapshot, order FuturesBatchOrderRequest) FuturesBatchOrderResult {
	return FuturesBatchOrderResult{
		OrderID:       snapshot.OrderID,
		Symbol:        nonEmpty(snapshot.Symbol, order.Symbol),
		Status:        snapshot.Status,
		ClientOrderID: nonEmpty(snapshot.ClientOrderID, order.NewClientOrderID),
		ExecutedQty:   formatFloat(snapshot.ExecutedQty),
		OrigQty:       order.Quantity,
		AvgPrice:      formatFloat(snapshot.AvgPrice),
		Side:          order.Side,
		UpdateTime:    snapshot.EventTime.UnixMilli(),
	}
}

func (e *FundingRateSniperEngine) placeSpotMarketBuyByQuantityIdempotent(ctx context.Context, symbol string, quantity float64, clientOrderID string) (SpotOrderResponse, error) {
	params, err := e.spotProtectedOrderParams(ctx, symbol, "BUY", quantity, clientOrderID)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce")) {
		result, dualTrackErr := e.placeSpotProtectedOpenDualTrack(ctx, symbol, "BUY", quantity, clientOrderID, params)
		if dualTrackErr == nil || parseFloat(result.ExecutedQty) > executionQtyTolerance {
			return result, dualTrackErr
		}
		if !isUnknownOrderRequestError(dualTrackErr) && !isPercentPriceFilterError(dualTrackErr) {
			return SpotOrderResponse{}, dualTrackErr
		}
	}
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := e.noteOrderPlaced("spot_market_buy", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, true); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, "spot_buy_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			if snapshot, blindErr := e.waitForSpotOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
				}
				return result, nil
			} else if blindErr != nil {
				return SpotOrderResponse{}, blindErr
			}
			queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
			if queryErr == nil && hasConfirmedSpotOrderResult(queried) {
				e.logger.Printf("现货买单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID)
			if reconcileErr == nil {
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
				}
				return queried, nil
			}
			return SpotOrderResponse{}, reconcileErr
		}
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return SpotOrderResponse{}, err
		}
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货买单回执失败: %w", err)
	}
	if protectedIOC {
		e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
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
	if isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce")) {
		result, dualTrackErr := e.placeSpotProtectedOpenDualTrack(ctx, symbol, "SELL", quantity, clientOrderID, params)
		if dualTrackErr == nil || parseFloat(result.ExecutedQty) > executionQtyTolerance {
			return result, dualTrackErr
		}
		if !isUnknownOrderRequestError(dualTrackErr) && !isPercentPriceFilterError(dualTrackErr) {
			return SpotOrderResponse{}, dualTrackErr
		}
	}
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := e.noteOrderPlaced("spot_market_sell", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, false); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, "spot_sell_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			if snapshot, blindErr := e.waitForSpotOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
				}
				return result, nil
			} else if blindErr != nil {
				return SpotOrderResponse{}, blindErr
			}
			queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
			if queryErr == nil && hasConfirmedSpotOrderResult(queried) {
				e.logger.Printf("现货卖单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID)
			if reconcileErr == nil {
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
				}
				return queried, nil
			}
			return SpotOrderResponse{}, reconcileErr
		}
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return SpotOrderResponse{}, err
		}
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货卖单回执失败: %w", err)
	}
	if protectedIOC {
		e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
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

func (e *FundingRateSniperEngine) expectedFundingIncomeSummary(hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot) (float64, []string) {
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	expectedFundingIncome := 0.0
	alerts := make([]string, 0)
	for symbol := range symbolSet {
		beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
		if math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
			alerts = append(alerts, fmt.Sprintf("资金费归因已跳过 %s: 开仓前已有持仓 positionAmt=%.10f", symbol, beforeQty))
			continue
		}
		expectedFundingIncome += expectedFundingBySymbol[symbol]
	}
	return expectedFundingIncome, alerts
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

func (e *FundingRateSniperEngine) effectiveFundingSettlementDelay() time.Duration {
	if e == nil || e.fundingSettlementDelay <= 0 {
		return defaultFundingSettlementDelay
	}
	return e.fundingSettlementDelay
}

func (e *FundingRateSniperEngine) scheduleAsyncFundingSettlement(base CycleSettlement, hedges []OpenHedge) {
	if e == nil || e.reportStore == nil {
		return
	}
	hedgeCopy := append([]OpenHedge(nil), hedges...)
	task := PendingFundingSettlementRecord{
		TraceRecordMetadata: base.TraceRecordMetadata.normalized(),
		RunID:               strings.TrimSpace(base.RunID),
		CycleID:             strings.TrimSpace(base.CycleID),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		NextAttemptAt:       time.Now().UTC().Add(e.effectiveFundingSettlementDelay()).Format(time.RFC3339),
		Settlement:          base,
		Hedges:              hedgeCopy,
	}
	if err := e.reportStore.UpsertPendingFundingSettlement(task); err != nil {
		if e.logger != nil {
			e.logger.Printf("持久化待结算资金费任务失败: %v", err)
		}
		return
	}
	e.startFundingSettlementWorker(context.Background())
	e.wakeFundingSettlementWorker()
}

func filterOutFundingPendingAlerts(alerts []string) []string {
	filtered := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		if strings.Contains(alert, "资金费异步结算待处理") {
			continue
		}
		filtered = append(filtered, alert)
	}
	return filtered
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

func (e *FundingRateSniperEngine) cancelSpotOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodDelete, e.client.spotBaseURL+"/api/v3/order", params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货撤单回执失败: %w", err)
	}
	return result, nil
}

func (e *FundingRateSniperEngine) resolveFuturesOrderExecution(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) (OrderExecutionSnapshot, error) {
	result := initial
	if clientOrderID != "" {
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
			return snapshot, nil
		} else if blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		}
		e.noteOrderUncertainty(symbol, "futures_order_confirm_timeout", fmt.Sprintf("clientOrderId=%s", clientOrderID))
	}
	for range 3 {
		filledQty := parseFloat(result.ExecutedQty)
		if filledQty > executionQtyTolerance || isClosedOrderStatus(result.Status) {
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
	if strings.TrimSpace(clientOrderID) == "" {
		return OrderExecutionSnapshot{}, fmt.Errorf("未能确认合约订单真实成交数量, symbol=%s clientOrderId=%s", symbol, clientOrderID)
	}
	queried, err := e.reconcileFuturesOrderTerminalState(ctx, symbol, clientOrderID)
	if err != nil {
		return OrderExecutionSnapshot{}, err
	}
	return futuresResultToSnapshot(queried), nil
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
	closeSide := "BUY"
	if strings.EqualFold(result.Side, "BUY") {
		closeSide = "SELL"
	}
	if _, err := e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, closeSide, closeQty, true, newClientOrderID("ftrim", symbol)); err != nil {
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
	if err := e.reconcilePersistedOpenHedges(spotBalances, positions); err != nil {
		return err
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

func normalizedFuturesMarginType(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ISOLATED":
		return "ISOLATED"
	default:
		return "CROSSED"
	}
}

func (e *FundingRateSniperEngine) ensureFuturesExecutionSettings(ctx context.Context, targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	marginType := normalizedFuturesMarginType(e.cfg.ExecutionConfig.FuturesMarginType)
	leverage := e.cfg.ExecutionConfig.FuturesLeverage
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		symbol := strings.ToUpper(strings.TrimSpace(target.Candidate.Symbol))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		if err := e.setFuturesMarginType(ctx, symbol, marginType); err != nil {
			return err
		}
		if err := e.setFuturesLeverage(ctx, symbol, leverage); err != nil {
			return err
		}
	}
	return nil
}

func (e *FundingRateSniperEngine) setFuturesMarginType(ctx context.Context, symbol string, marginType string) error {
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	params.Set("marginType", normalizedFuturesMarginType(marginType))
	body, err := e.signedRequest(ctx, http.MethodPost, e.client.futuresBaseURL+"/fapi/v1/marginType", params)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no need to change margin type") || strings.Contains(err.Error(), "-4046") {
			return nil
		}
		return fmt.Errorf("设置 %s 保证金模式失败: %w", symbol, err)
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil && payload.Code != 0 {
		if strings.Contains(strings.ToLower(payload.Msg), "no need to change margin type") || payload.Code == -4046 {
			return nil
		}
		return fmt.Errorf("设置 %s 保证金模式失败: code=%d msg=%s", symbol, payload.Code, payload.Msg)
	}
	return nil
}

func (e *FundingRateSniperEngine) setFuturesLeverage(ctx context.Context, symbol string, leverage int) error {
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	params.Set("leverage", strconv.Itoa(leverage))
	if _, err := e.signedRequest(ctx, http.MethodPost, e.client.futuresBaseURL+"/fapi/v1/leverage", params); err != nil {
		return fmt.Errorf("设置 %s 杠杆倍数失败: %w", symbol, err)
	}
	return nil
}

func (e *FundingRateSniperEngine) verifyPreOpenGoNoGo(targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	if err := e.checkLiveSafetyGuards("pre_open_go_nogo"); err != nil {
		return err
	}
	maxAge := e.cfg.ExecutionConfig.PreOpenTickerMaxAge
	if maxAge <= 0 {
		maxAge = time.Second
	}
	issues := make([]string, 0, len(targets)*2)
	for _, target := range targets {
		if _, issue := e.preOpenTickerFreshnessIssue(target.Candidate.Pair, e.spotTickerCache, maxAge, "spot"); issue != "" {
			issues = append(issues, issue)
		}
		if _, issue := e.preOpenTickerFreshnessIssue(target.Candidate.Symbol, e.futuresTickerCache, maxAge, "futures"); issue != "" {
			issues = append(issues, issue)
		}
	}
	if len(issues) > 0 {
		sort.Strings(issues)
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}

func (e *FundingRateSniperEngine) preOpenTickerFreshnessIssue(symbol string, cache *BookTickerCache, maxAge time.Duration, label string) (BookTicker, string) {
	if cache == nil {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 缓存未初始化", label, symbol)
	}
	ticker, receivedAt, ok := cache.GetWithReceivedAt(symbol)
	if !ok || (parseFloat(ticker.AskPrice) <= 0 && parseFloat(ticker.BidPrice) <= 0) {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 缺失", label, symbol)
	}
	age := time.Since(receivedAt)
	if receivedAt.IsZero() || age > maxAge {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 延迟过高: age=%s limit=%s", label, symbol, age.Round(time.Millisecond), maxAge)
	}
	return ticker, ""
}

func (e *FundingRateSniperEngine) verifySpotBNBBurnEnabled(ctx context.Context) error {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.spotBaseURL+"/sapi/v1/bnbBurn", nil)
	if err != nil {
		return fmt.Errorf("读取 Binance BNB burn 开关失败: %w", err)
	}
	var payload struct {
		SpotBNBBurn bool `json:"spotBNBBurn"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析 Binance BNB burn 开关失败: %w", err)
	}
	if !payload.SpotBNBBurn {
		return fmt.Errorf("Binance 现货 BNB 抵扣手续费未开启，拒绝启动")
	}
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
	freeBalances, balances, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return nil, 0, err
	}
	return balances, freeBalances["USDT"], nil
}

func (e *FundingRateSniperEngine) fetchSpotAccountFreeBalances(ctx context.Context) (map[string]float64, map[string]float64, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.spotBaseURL+"/api/v3/account", nil)
	if err != nil {
		return nil, nil, err
	}

	var payload struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("解析现货账户快照失败: %w", err)
	}

	freeBalances := make(map[string]float64, len(payload.Balances))
	balances := make(map[string]float64, len(payload.Balances))
	for _, balance := range payload.Balances {
		free := parseFloat(balance.Free)
		freeBalances[balance.Asset] = free
		total := parseFloat(balance.Free) + parseFloat(balance.Locked)
		balances[balance.Asset] = total
	}

	return freeBalances, balances, nil
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

func (e *FundingRateSniperEngine) transferAssetBetweenAccounts(ctx context.Context, transferType string, asset string, amount float64) error {
	if amount <= executionQtyTolerance {
		return nil
	}
	params := url.Values{}
	params.Set("type", transferType)
	params.Set("asset", strings.ToUpper(strings.TrimSpace(asset)))
	params.Set("amount", formatRequestAmount(amount))

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.spotBaseURL+"/sapi/v1/asset/transfer", params)
	if err != nil {
		return err
	}
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析账户划转回执失败: %w", err)
	}
	if payload.TranID == 0 {
		return fmt.Errorf("账户划转回执缺少 tranId")
	}
	return nil
}

func (e *FundingRateSniperEngine) fetchCrossMarginAccountState(ctx context.Context) (CrossMarginAccountState, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.spotBaseURL+"/sapi/v1/margin/account", nil)
	if err != nil {
		return CrossMarginAccountState{}, err
	}

	var payload struct {
		Created                  bool   `json:"created"`
		BorrowEnabled            bool   `json:"borrowEnabled"`
		TradeEnabled             bool   `json:"tradeEnabled"`
		TransferInEnabled        bool   `json:"transferInEnabled"`
		TransferOutEnabled       bool   `json:"transferOutEnabled"`
		AccountType              string `json:"accountType"`
		TotalCollateralValueUSDT string `json:"TotalCollateralValueInUSDT"`
		TotalNetAssetBTC         string `json:"totalNetAssetOfBtc"`
		TotalLiabilityBTC        string `json:"totalLiabilityOfBtc"`
		UserAssets               []struct {
			Asset    string `json:"asset"`
			Borrowed string `json:"borrowed"`
			Free     string `json:"free"`
			Interest string `json:"interest"`
			Locked   string `json:"locked"`
			NetAsset string `json:"netAsset"`
		} `json:"userAssets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return CrossMarginAccountState{}, fmt.Errorf("解析全仓账户快照失败: %w", err)
	}

	state := CrossMarginAccountState{
		Created:             payload.Created,
		BorrowEnabled:       payload.BorrowEnabled,
		TradeEnabled:        payload.TradeEnabled,
		TransferInEnabled:   payload.TransferInEnabled,
		TransferOutEnabled:  payload.TransferOutEnabled,
		AccountType:         payload.AccountType,
		TotalCollateralUSDT: parseFloat(payload.TotalCollateralValueUSDT),
		TotalNetAssetBTC:    parseFloat(payload.TotalNetAssetBTC),
		TotalLiabilityBTC:   parseFloat(payload.TotalLiabilityBTC),
		Assets:              make(map[string]CrossMarginAssetBalance, len(payload.UserAssets)),
	}
	for _, asset := range payload.UserAssets {
		entry := CrossMarginAssetBalance{
			Asset:    strings.ToUpper(strings.TrimSpace(asset.Asset)),
			Free:     parseFloat(asset.Free),
			Borrowed: parseFloat(asset.Borrowed),
			Interest: parseFloat(asset.Interest),
			Locked:   parseFloat(asset.Locked),
			NetAsset: parseFloat(asset.NetAsset),
		}
		if entry.Asset == "" {
			continue
		}
		state.Assets[entry.Asset] = entry
		if entry.Asset == "USDT" {
			state.USDT = entry
		}
	}
	return state, nil
}

func (e *FundingRateSniperEngine) borrowCrossMarginAsset(ctx context.Context, asset string, amount float64) (int64, error) {
	return e.crossMarginBorrowRepay(ctx, asset, amount, "BORROW")
}

func (e *FundingRateSniperEngine) repayCrossMarginAsset(ctx context.Context, asset string, amount float64) (int64, error) {
	return e.crossMarginBorrowRepay(ctx, asset, amount, "REPAY")
}

func (e *FundingRateSniperEngine) crossMarginBorrowRepay(ctx context.Context, asset string, amount float64, action string) (int64, error) {
	if amount <= executionQtyTolerance {
		return 0, fmt.Errorf("margin %s amount 必须大于 0", strings.ToLower(action))
	}
	params := url.Values{}
	params.Set("asset", strings.ToUpper(strings.TrimSpace(asset)))
	params.Set("isIsolated", "FALSE")
	params.Set("amount", formatRequestAmount(amount))
	params.Set("type", action)

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.spotBaseURL+"/sapi/v1/margin/borrow-repay", params)
	if err != nil {
		return 0, err
	}
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("解析全仓%s回执失败: %w", strings.ToLower(action), err)
	}
	if payload.TranID == 0 {
		return 0, fmt.Errorf("全仓%s回执缺少 tranId", strings.ToLower(action))
	}
	return payload.TranID, nil
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
		if spotPair, ok := e.goldenSpotPairByFuture[symbol]; ok {
			if spotMeta, spotOK := e.spotMeta[spotPair]; spotOK && strings.EqualFold(spotMeta.BaseAsset, commissionAsset) && referencePrice > 0 {
				return commission * referencePrice
			}
		} else if meta, ok := e.futuresMeta[symbol]; ok {
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

	return hedge.carryDirection().expectedFundingIncome(referencePrice, hedge.HedgeQty, hedge.Target.EstimatedRate)
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
	releaseUsedWeightBudget := func() {}
	if !isExposureManagerRequest(ctx) {
		if err := e.checkPlatformFuse(); err != nil {
			return nil, err
		}
		release, err := e.blockNonCriticalRequestByUsedWeight(method, endpoint, params)
		if err != nil {
			return nil, err
		}
		releaseUsedWeightBudget = release
	}
	defer releaseUsedWeightBudget()
	if params == nil {
		params = url.Values{}
	}
	params = cloneURLValues(params)
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
		requestErr := signedRequestError{
			Endpoint: endpoint,
			Cause:    err,
		}
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	defer resp.Body.Close()
	e.client.noteUsedWeightFromHeaders(resp.Header)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		requestErr := signedRequestError{
			Endpoint: endpoint,
			Cause:    err,
		}
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}

	if resp.StatusCode != http.StatusOK {
		requestErr := signedRequestError{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		}
		if markErr := e.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	e.notePlatformRequestSuccess()

	return body, nil
}

func cloneURLValues(values url.Values) url.Values {
	cloned := url.Values{}
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func (e *FundingRateSniperEngine) isCriticalExposureRequest(method string, endpoint string, params url.Values) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	clientOrderID := ""
	if params != nil {
		clientOrderID = strings.ToLower(strings.TrimSpace(params.Get("newClientOrderId")))
	}
	if strings.Contains(endpoint, "/api/v3/order") || strings.Contains(endpoint, "/fapi/v1/order") {
		if method == http.MethodDelete {
			return true
		}
		if strings.EqualFold(params.Get("reduceOnly"), "true") {
			return true
		}
		for _, prefix := range []string{"fclose", "ftrim", "frescue", "sclose", "srevert", "sclose_revert", "exp_"} {
			if strings.HasPrefix(clientOrderID, prefix) {
				return true
			}
		}
	}
	return false
}

func (e *FundingRateSniperEngine) blockNonCriticalRequestByUsedWeight(method string, endpoint string, params url.Values) (func(), error) {
	if e == nil || e.client == nil || e.isCriticalExposureRequest(method, endpoint, params) {
		return func() {}, nil
	}
	projected, threshold, ok := e.client.reserveUsedWeightBudget(1)
	if threshold <= 0 || ok {
		return func() {
			e.client.releaseUsedWeightBudget(1)
		}, nil
	}
	return func() {}, e.tripPlatformFuse("used_weight", "critical", fmt.Sprintf("usedWeightProjected=%d 已达到阈值 %d，主动阻断非平仓/非救援请求 endpoint=%s", projected, threshold, endpoint))
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

func sleepUntilServerTimeResyncInterval(remaining time.Duration) time.Duration {
	if remaining <= sleepNearThreshold {
		return sleepResyncIntervalNear
	}
	if remaining <= sleepMidThreshold {
		return sleepResyncIntervalMid
	}
	return sleepResyncIntervalFar
}

func sleepUntilServerTimeWaitChunk(remaining time.Duration) time.Duration {
	if remaining <= sleepNearThreshold {
		return sleepWaitChunkNear
	}
	if remaining <= sleepMidThreshold {
		return sleepWaitChunkMid
	}
	return sleepWaitChunkFar
}

func (e *FundingRateSniperEngine) sleepUntilServerTime(ctx context.Context, target time.Time) error {
	lastResyncAttemptAt := time.Now()
	for {
		if err := e.checkLiveSafetyGuards("sleep_until_server_time"); err != nil {
			return err
		}
		remainingServer := target.Sub(e.serverNow())
		if remainingServer <= 0 {
			return nil
		}

		now := time.Now()
		resyncInterval := sleepUntilServerTimeResyncInterval(remainingServer)
		if now.Sub(lastResyncAttemptAt) >= resyncInterval {
			lastResyncAttemptAt = now
			if err := e.syncServerTimeOffset(ctx); err != nil && e.logger != nil {
				e.logger.Printf("等待唤醒期间刷新服务器时间偏移失败，将沿用旧偏移: %v", err)
			}
			continue
		}

		waitUntil := target.Add(-e.serverTimeOffset)
		remainingLocal := time.Until(waitUntil)
		if remainingLocal <= 0 {
			return nil
		}

		untilNextResync := resyncInterval - now.Sub(lastResyncAttemptAt)
		if untilNextResync > 0 && untilNextResync < remainingLocal {
			remainingLocal = untilNextResync
		}
		waitChunk := sleepUntilServerTimeWaitChunk(remainingServer)
		if waitChunk > 0 && remainingLocal > waitChunk {
			remainingLocal = waitChunk
		}
		timer := time.NewTimer(remainingLocal)
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
		if math.Abs(settleBasisRate) > math.Abs(premiumEstimate) {
			premiumEstimate = settleBasisRate
		}
	}

	estimatedFundingRate := lastFundingRate
	conservativeRate := conservativeLiveFundingRate(lastFundingRate, premiumEstimate, interestRate)
	if conservativeRate != 0 {
		estimatedFundingRate = conservativeRate
	}

	fundingJump := math.Abs(estimatedFundingRate - lastFundingRate)
	scanScoreRate := math.Abs(estimatedFundingRate) - math.Abs(basisRate)
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
	releaseUsedWeightBudget := func() {}
	if !isExposureManagerRequest(ctx) {
		if err := e.checkPlatformFuse(); err != nil {
			return err
		}
		release, err := e.blockNonCriticalRequestByUsedWeight(http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		releaseUsedWeightBudget = release
	}
	defer releaseUsedWeightBudget()
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
		direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
		spotVisible, futuresVisible := visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
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
	e.protectedIOCAttempts = 0
	e.protectedIOCCanceled = 0
	e.protectedIOCExpired = 0
	e.protectedIOCPartial = 0
	e.protectedIOCUnfilledSum = 0
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
			if lockState, locked := e.exposureLocks[target.Candidate.Symbol]; locked {
				e.logger.Printf("symbol 被 ExposureManager 锁定，跳过 %s phase=%s mode=%s locked_at=%s", target.Candidate.Symbol, lockState.Phase, lockState.Mode, lockState.LockedAt.Format(time.RFC3339))
				continue
			}
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
	target.LastMarketRiskCategory = strings.TrimSpace(nonEmpty(decision.Category, "market"))
	target.LastMarketRiskSeverity = strings.TrimSpace(nonEmpty(decision.Severity, "healthy"))
	target.LastMarketRiskAction = strings.TrimSpace(nonEmpty(decision.Action, "healthy"))
	target.LastMarketRiskDetail = strings.TrimSpace(detail)
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
		labels = append(labels, fmt.Sprintf("%s %s %.8f", hedge.Target.Symbol, hedge.carryDirection().positionLabel(hedge.Target.SpotBaseAsset, hedge.Target.Symbol), hedge.HedgeQty))
	}
	e.platformRiskMu.Lock()
	e.activeOpenHedges = append([]OpenHedge(nil), hedges...)
	e.currentPositions = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
}

func (e *FundingRateSniperEngine) setCurrentPositions(labels []string) {
	e.platformRiskMu.Lock()
	if len(labels) == 0 {
		e.activeOpenHedges = nil
	}
	e.currentPositions = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
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
	protectedIOCAttempts, _, _, _, protectedIOCAvgUnfilledRatio, protectedIOCCancelRate := e.protectedIOCStatsLocked()
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
	if protectedIOCAttempts >= e.cfg.RiskConfig.ProtectedIOCMinAttempts {
		if protectedIOCAvgUnfilledRatio >= e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio {
			return "paused", fmt.Sprintf("保护 IOC 平均未成交率 %.2f%% 超过阈值 %.2f%%，当前轮应暂停扩张", protectedIOCAvgUnfilledRatio*100, e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*100), false
		}
		if protectedIOCCancelRate >= e.cfg.RiskConfig.ProtectedIOCMaxCancelRate {
			return "paused", fmt.Sprintf("保护 IOC 撤单/过期率 %.2f%% 超过阈值 %.2f%%，当前轮应暂停扩张", protectedIOCCancelRate*100, e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*100), false
		}
		if protectedIOCAvgUnfilledRatio >= e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*0.8 {
			return "degraded", fmt.Sprintf("保护 IOC 平均未成交率 %.2f%% 已接近阈值 %.2f%%", protectedIOCAvgUnfilledRatio*100, e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*100), true
		}
		if protectedIOCCancelRate >= e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*0.8 {
			return "degraded", fmt.Sprintf("保护 IOC 撤单/过期率 %.2f%% 已接近阈值 %.2f%%", protectedIOCCancelRate*100, e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*100), true
		}
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

func intersectGoldenSymbols(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol, preferredQuotes []string) ([]string, map[string]string) {
	pairByFuture := buildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, preferredQuotes, time.Now().UTC())
	result := make([]string, 0, len(pairByFuture))
	for symbol := range pairByFuture {
		result = append(result, symbol)
	}
	sort.Strings(result)
	return result, pairByFuture
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
		case "PERCENT_PRICE":
			if rules.PercentPriceUp == 0 {
				rules.PercentPriceUp = parseFloat(filter.MultiplierUp)
			}
			if rules.PercentPriceDown == 0 {
				rules.PercentPriceDown = parseFloat(filter.MultiplierDown)
			}
			if rules.PercentPriceAvgMins == 0 {
				rules.PercentPriceAvgMins = filter.AvgPriceMins
			}
		case "PERCENT_PRICE_BY_SIDE":
			if rules.BidPercentPriceUp == 0 {
				rules.BidPercentPriceUp = parseFloat(filter.BidMultiplierUp)
			}
			if rules.BidPercentPriceDown == 0 {
				rules.BidPercentPriceDown = parseFloat(filter.BidMultiplierDown)
			}
			if rules.AskPercentPriceUp == 0 {
				rules.AskPercentPriceUp = parseFloat(filter.AskMultiplierUp)
			}
			if rules.AskPercentPriceDown == 0 {
				rules.AskPercentPriceDown = parseFloat(filter.AskMultiplierDown)
			}
			if rules.PercentPriceAvgMins == 0 {
				rules.PercentPriceAvgMins = filter.AvgPriceMins
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

func precisionFromIncrement(increment float64, fallbackPrecision int) int {
	if increment <= 0 {
		if fallbackPrecision < 0 {
			return 0
		}
		return fallbackPrecision
	}
	text := strconv.FormatFloat(increment, 'f', -1, 64)
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		return len(strings.TrimRight(text[dot+1:], "0"))
	}
	return 0
}

func formatFloatWithPrecision(value float64, precision int) string {
	if precision < 0 {
		precision = 0
	}
	return strconv.FormatFloat(roundDownQuantity(value, precision), 'f', precision, 64)
}

func formatQuantityForRules(quantity float64, rules SymbolTradeRules, fallbackPrecision int) string {
	normalized := normalizeQuantity(quantity, rules, fallbackPrecision)
	precision := precisionFromIncrement(rules.StepSize, fallbackPrecision)
	return formatFloatWithPrecision(normalized, precision)
}

func formatPriceForTick(price float64, tickSize float64) string {
	precision := precisionFromIncrement(tickSize, 0)
	return formatFloatWithPrecision(price, precision)
}

func formatRequestAmount(value float64) string {
	text := formatFloatWithPrecision(value, 12)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		return "0"
	}
	return text
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
