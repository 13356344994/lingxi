package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/fillmodel"
	"lingxi/internal/lifecycle"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载引擎的常量、领域类型与轻量类型方法
// （引擎结构体、错误类型、OpenHedge 生命周期状态机、账户/订单数据结构等，同包搬移、
// 行为不变），让主文件收敛到「生命周期编排」职责并向单文件 ≤ 800 行门禁靠拢。

const (
	monitorEstimateRefreshInterval = 2 * time.Second
	executionQtyTolerance          = 1e-8
	spotFeeBNBSafetyMultiplier     = 1.2
	defaultFundingSettlementDelay  = 30 * time.Second
	futuresAccountStateCacheTTL    = 5 * time.Second
	futuresAccountStateStaleTTL    = 30 * time.Second
	sleepWaitChunkNear             = 1 * time.Second
	sleepWaitChunkMid              = 5 * time.Second
	sleepWaitChunkFar              = 30 * time.Second
	sleepResyncIntervalNear        = 10 * time.Second
	sleepResyncIntervalMid         = 1 * time.Minute
	sleepResyncIntervalFar         = 10 * time.Minute
	sleepNearThreshold             = 2 * time.Minute
	sleepMidThreshold              = 10 * time.Minute
	orderRateLimitWindow10s        = 10 * time.Second
	orderRateLimitWindow1m         = 1 * time.Minute
	orderRateLimitExchangeCooldown = 10 * time.Second
	orderRateLimitCapacity10s      = 300
	orderRateLimitCapacity1m       = 1200
	orderRateLimitReserve10s       = 30
	orderRateLimitReserve1m        = 120
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
type FundingRateSniperEngine struct {
	client         *BinanceClient
	cfg            EngineConfig
	logger         *log.Logger
	runID          string
	currentCycleID string
	// traceLogMu 仅保护下面两个供日志旁路读取的 trace id 副本。它与 platformRiskMu 完全解耦，
	// 杜绝「持有 platformRiskMu 时写日志 -> 日志写入器回调 traceIDs() 再次 Lock platformRiskMu」
	// 造成的同协程自死锁（Go 互斥锁不可重入）。
	traceLogMu                  sync.Mutex
	traceLogRunID               string
	traceLogCycleID             string
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
	domainEventSink             DomainEventSink
	costModel                   fillmodel.CostModel
	borrowHourlyInterestByAsset map[string]float64
	// serverTimeOffset 是「服务器时间 − 本地时间」的偏移(纳秒)。它被主循环 / 唤醒等待 / 开仓预备等
	// 路径写入，同时被监控 worker、并发开平仓、签名时间戳等多个 goroutine 经 serverNow() 并发读取，
	// 故用 atomic.Int64 做无锁安全读写，杜绝撕裂读导致的 fire-at 时点漂移（S0 / R4）。
	serverTimeOffset   atomic.Int64
	executionIncidents int32
	executionEventHook func(ExecutionIncidentRecord)
	domainEventHook    func(DomainEvent)
	liveStatusHook     func(LiveRuntimeSnapshot)

	// 新增拆分的状态容器，后续逐步替换零散字段
	platformState   EnginePlatformState
	riskBudgetState EngineRiskBudgetState

	platformRiskMu sync.Mutex
	orderRateMu    sync.Mutex

	orderRateRequests10s       []time.Time
	orderRateRequests1m        []time.Time
	consecutiveRequestErrors   int
	consecutivePlatformRejects int
	requestErrorsThisCycle     int
	platformRejectsThisCycle   int
	recoveryActionsThisCycle   int
	orderUncertaintyCount      int
	protectedIOCAttempts       int
	protectedIOCCanceled       int
	protectedIOCExpired        int
	protectedIOCPartial        int
	protectedIOCUnfilledSum    float64
	lastOrderAt                time.Time
	lastOrderAction            string
	lastRecoveryAt             time.Time
	lastRecoveryAction         string
	lastManagedDrawdownAt      time.Time
	lastManagedDrawdownAction  string
	managedDrawdownSummary     string
	lastUncertainOrder         string

	emergencyStopFile          string
	openHedgeStatePath         string
	openHedgeStateMu           sync.Mutex
	emergencyStopTriggered     bool
	emergencyStopReason        string
	dailyRiskStatePath         string
	dailyRiskPersistCh         chan persistedDailyRiskState
	dailyRiskPersistWorkerOnce sync.Once
	dailyRiskDate              string
	activeOpenHedges           []OpenHedge
	futuresRealtimeWalletUSDT  float64
	futuresRealtimePositions   map[string]futuresRealtimePosition
	lastFuturesAccountUpdateAt time.Time
	spotRealtimeBalances       map[string]float64
	lastSpotAccountUpdateAt    time.Time
	realtimeRiskWakeCh         chan struct{}
	realtimeRiskWorkerOnce     sync.Once
	panicFlattenInFlight       bool
	symbolPanicFlattenInFlight map[string]time.Time
	currentMonitorTargets      []string
	currentPositions           []string

	currentStage                   EngineStage
	currentStageReason             string
	currentStageTransitionedAt     string
	currentLifecycleStage          LifecycleStage
	currentLifecycleReason         string
	currentLifecycleTransitionedAt string
	lastLifecycleTransitionFrom    LifecycleStage
	lastLifecycleTransitionTo      LifecycleStage
	lifecycleStateMachine          *LifecycleStateMachine
	symbolCooldowns                map[string]symbolCooldownState
	exposureLocks                  map[string]exposureLockState
	exposureManager                *ExposureManager
	runCtx                         context.Context
	riskSupervisorOnce             sync.Once
	riskSupervisor                 *RiskSupervisor
	binanceGatewayOnce             sync.Once
	binanceGateway                 *BinanceGateway
	openProtocolOnce               sync.Once
	openProtocol                   *OpenProtocol
	closeProtocolOnce              sync.Once
	closeProtocol                  *CloseProtocol
	recoveryProtocolOnce           sync.Once
	recoveryProtocol               *RecoveryProtocol
	fundingSettlementDelay         time.Duration
	runtimeReporter                *RuntimeReporter
	costReporter                   *CostReporter
	fundingSettlementWorkerOnce    sync.Once
	fundingSettlementWakeCh        chan struct{}
	cacheOnlyBookTickerUntil       int64
	futuresLeverageBracketMu       sync.Mutex
	futuresLeverageBrackets        map[string][]futuresLeverageBracket
	futuresAccountStateMu          sync.Mutex
	futuresAccountStateCachedAt    time.Time
	futuresAccountWalletUSDT       float64
	futuresAccountAvailableUSDT    float64
	futuresAccountPositions        map[string]float64
}

type symbolCooldownState struct {
	Category string
	Severity string
	Reason   string
	Until    time.Time
}

type marketRiskActionOptions struct {
	detail                  string
	netProfit               float64
	consecutiveBreaches     *int
	allowObserve            bool
	invalidateOnCooldown    bool
	invalidateOnGlobalPause bool
	resetHealthyState       bool
	symbolCooldownReason    string
	globalPauseReason       string
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

func cloneFloat64Map(values map[string]float64) map[string]float64 {
	if len(values) == 0 {
		return map[string]float64{}
	}
	cloned := make(map[string]float64, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
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
	LastBorrowReserveRate      float64
	LastBorrowReserveUSDT      float64
	LastResidualReserveRate    float64
	LastResidualReserveUSDT    float64
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
	plannedNotional        float64
	projectedFuturesAbsQty map[string]float64
}

// plannedCycleNotional 返回本周期实际计划开仓的名义总额（USDT）。
// 即便未配置单周期上限(MaxCycleOpenNotionalUSDT<=0)也能据此统计当日累计开仓名义。
func (s *openSequenceBuildState) plannedCycleNotional() float64 {
	return maxFloat(s.plannedNotional, 0)
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

type futuresLeverageBracket struct {
	Bracket          int     `json:"bracket"`
	InitialLeverage  int     `json:"initialLeverage"`
	NotionalCap      float64 `json:"notionalCap"`
	NotionalFloor    float64 `json:"notionalFloor"`
	MaintMarginRatio float64 `json:"maintMarginRatio"`
	Cum              float64 `json:"cum"`
}

type futuresLeverageBracketEnvelope struct {
	Symbol   string                   `json:"symbol"`
	Brackets []futuresLeverageBracket `json:"brackets"`
}

type futuresLeverageRiskAssessment struct {
	Bracket               futuresLeverageBracket
	ProjectedNotionalUSDT float64
	EffectiveLeverage     int
	InitialMarginUSDT     float64
	MaintenanceMarginUSDT float64
}

// liquidationDistanceRate 估算合约腿开仓后「入场价到强平价」的安全距离（占入场价比例）。
// 隔离保证金近似：可吸收的逆向浮亏 = 初始保证金 − 维持保证金，占名义的比例即价格可逆向移动的幅度。
// MaintenanceMarginUSDT 已含 bracket 的 Cum 项，因此该比例同时反映了维持保证金率与维持保证金常数。
// 对全仓(CROSSED)模式而言这是保守下界（全仓有整账户权益兜底，真实距离更大），用作开仓前安全闸恰当。
// 名义无效或结果为负时返回 0（视为「无安全距离」，必被任何正阈值拦下）。
func (a futuresLeverageRiskAssessment) liquidationDistanceRate() float64 {
	if a.ProjectedNotionalUSDT <= 0 {
		return 0
	}
	rate := (a.InitialMarginUSDT - a.MaintenanceMarginUSDT) / a.ProjectedNotionalUSDT
	if rate < 0 {
		return 0
	}
	return rate
}

func (e nonFatalOpenAbortError) Error() string {
	return e.message
}

// PremiumIndexItem 对应币安 /fapi/v1/premiumIndex 响应。
// 模块二会结合 lastFundingRate、interestRate 和 mark/index 基差做更保守的费率预估。
// PremiumIndexItem 已迁移到 internal/exchange（市场数据类型）。保留别名以零改动兼容。
type PremiumIndexItem = exchange.PremiumIndexItem

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
	Target                     FundingCandidate
	FuturesOrder               FuturesBatchOrderResult
	SpotOrder                  SpotOrderResponse
	LifecycleState             OpenHedgeLifecycleState
	LifecycleReason            string
	LifecycleUpdatedAt         time.Time
	LifecycleIllegalTransition bool
	Direction                  fundingCarryDirection
	SpotBaseQty                string
	HedgeQty                   float64
	HedgeQtyText               string
	OpenSpotQuoteQty           float64
	OpenSpotFeeUSDT            float64
	OpenFuturesAvgPrice        float64
	OpenFuturesFeeUSDT         float64
	OpenSlippageUSDT           float64
	CloseFuturesExecutedQty    float64
	CloseSpotExecutedQty       float64
	CloseSpotQuoteQty          float64
	CloseSpotFeeUSDT           float64
	CloseFuturesAvgPrice       float64
	CloseFuturesFeeUSDT        float64
	CloseSlippageUSDT          float64
	RecoveryFrictionUSDT       float64
	CloseFuturesRealizedPnL    float64
}

// OpenHedgeLifecycleState 的状态词表与合法迁移收敛到 internal/lifecycle（SSOT）。这里以类型别名 +
// 常量转发复用，既保持既有引用零改动，又让对冲生命周期只有一套权威规则（Gate 3：消灭双轨状态）。
type OpenHedgeLifecycleState = lifecycle.State

const (
	OpenHedgeLifecyclePendingValidation   = lifecycle.StatePendingValidation
	OpenHedgeLifecycleActive              = lifecycle.StateActive
	OpenHedgeLifecycleClosing             = lifecycle.StateClosing
	OpenHedgeLifecycleClosed              = lifecycle.StateClosed
	OpenHedgeLifecycleClosedWithResidual  = lifecycle.StateClosedWithResidual
	OpenHedgeLifecycleCloseRecovered      = lifecycle.StateCloseRecovered
	OpenHedgeLifecycleCloseRecoveredPart  = lifecycle.StateCloseRecoveredPart
	OpenHedgeLifecycleEscalated           = lifecycle.StateEscalated
	OpenHedgeLifecycleAborted             = lifecycle.StateAborted
	OpenHedgeLifecycleAbortedWithResidual = lifecycle.StateAbortedWithResidual
)

func (h *OpenHedge) setLifecycleState(state OpenHedgeLifecycleState, reason string) {
	if h == nil || state == "" {
		return
	}
	// 迁移合法性由 SSOT 状态机判定；非法迁移记录到对冲上供观测（不静默吞掉），但仍然应用以保持
	// 既有恢复路径的行为不变——下单前置门 guardHedgeOrderIntent 才是真正阻断非法下单的闸门。
	if !lifecycle.CanTransition(h.LifecycleState, state) {
		h.LifecycleIllegalTransition = true
	}
	h.LifecycleState = state
	h.LifecycleReason = strings.TrimSpace(reason)
	h.LifecycleUpdatedAt = time.Now().UTC()
}

// lifecycleOrderGate 返回当前对冲状态作为下单前置门的判定输入；nil 对冲视为「尚无对冲」的空闲态。
func (h *OpenHedge) lifecycleOrderGate() lifecycle.State {
	if h == nil {
		return lifecycle.StateNone
	}
	return h.LifecycleState
}

func (h *OpenHedge) markPendingValidation(reason string) {
	h.setLifecycleState(OpenHedgeLifecyclePendingValidation, reason)
}

func (h *OpenHedge) markActive(reason string) {
	h.setLifecycleState(OpenHedgeLifecycleActive, reason)
}

func (h *OpenHedge) markClosing(reason string) {
	h.setLifecycleState(OpenHedgeLifecycleClosing, reason)
}

func (h *OpenHedge) markEscalated(reason string) {
	h.setLifecycleState(OpenHedgeLifecycleEscalated, reason)
}

func (h *OpenHedge) markCloseRecovered(partial bool, reason string) {
	if h == nil {
		return
	}
	if partial {
		h.setLifecycleState(OpenHedgeLifecycleCloseRecoveredPart, reason)
		return
	}
	h.setLifecycleState(OpenHedgeLifecycleCloseRecovered, reason)
}

func (h *OpenHedge) completeCloseLifecycle(aborted bool, reason string) {
	if h == nil {
		return
	}
	if aborted {
		if h.hasResidualExposure() {
			h.setLifecycleState(OpenHedgeLifecycleAbortedWithResidual, reason)
			return
		}
		h.setLifecycleState(OpenHedgeLifecycleAborted, reason)
		return
	}
	if h.hasResidualExposure() {
		h.setLifecycleState(OpenHedgeLifecycleClosedWithResidual, reason)
		return
	}
	h.setLifecycleState(OpenHedgeLifecycleClosed, reason)
}

func (h OpenHedge) effectiveClosedQty() float64 {
	return minPositiveFloat(h.CloseFuturesExecutedQty, h.CloseSpotExecutedQty)
}

func (h OpenHedge) hasResidualExposure() bool {
	if h.HedgeQty <= executionQtyTolerance {
		return false
	}
	return h.effectiveClosedQty()+executionQtyTolerance < h.HedgeQty
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
