package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	monthlySimulationLogCSVName       = "monthly_simulation_logs.json"
	monthlySimulationArchiveDirName   = "monthly_runs"
	monthlyFundingFetchProgressStep   = 20
	monthlyEvaluationProgressStep     = 24
	monthlyFundingDataCompleteFloor   = 0.85
	monthlyFundingTopSymbolsPerTime   = 48
	monthlyFundingSparseGroupMinItems = 4
	monthlyFundingFetchBatchSize      = 20
	monthlyFundingFetchBatchPause     = 10 * time.Second
	monthlyFundingGroupMergeWindow    = 3 * time.Hour
	monthlyFundingPause202            = 30 * time.Second
	monthlyFundingPause429            = 60 * time.Second
	monthlyFundingPause403            = 60 * time.Second
	monthlyFundingPause418            = 120 * time.Second
	monthlyFundingPauseOther4xx       = 30 * time.Second
	monthlyFundingAbort202Batches     = 4
	monthlyFundingAbort429Batches     = 2
	monthlyFundingAbort403Batches     = 2
	monthlyFundingAbort418Batches     = 1
)

var monthlySimulationLogHeader = []string{
	"recorded_at",
	"funding_time",
	"action",
	"symbol",
	"estimated_funding_rate",
	"funding_rate",
	"estimated_funding_income",
	"funding_income",
	"hedge_qty",
	"spot_entry_price",
	"futures_entry_price",
	"spot_exit_price",
	"futures_exit_price",
	"open_basis_rate",
	"close_basis_rate",
	"spot_entry_quote_volume",
	"futures_entry_quote_volume",
	"spot_exit_quote_volume",
	"futures_exit_quote_volume",
	"spot_entry_slip_rate",
	"futures_entry_slip_rate",
	"spot_exit_slip_rate",
	"futures_exit_slip_rate",
	"spot_entry_impact_rate",
	"futures_entry_impact_rate",
	"spot_exit_impact_rate",
	"futures_exit_impact_rate",
	"spot_entry_fee",
	"futures_entry_fee",
	"spot_exit_fee",
	"futures_exit_fee",
	"open_basis_penalty",
	"projected_exit_cost",
	"min_liquidity_required",
	"spot_gross_pnl",
	"futures_gross_pnl",
	"total_fees",
	"total_friction",
	"estimated_net_pnl",
	"realized_net_pnl",
	"equity_before",
	"equity_after",
	"drawdown_rate",
	"reason",
	"notes",
}

// FundingRateHistoryItem 对应币安 fundingRate 历史接口响应。
type FundingRateHistoryItem struct {
	Symbol               string `json:"symbol"`
	FundingRate          string `json:"fundingRate"`
	FundingTime          int64  `json:"fundingTime"`
	MarkPrice            string `json:"markPrice"`
	EstimatedFundingRate string `json:"-"`
	ScreeningScore       string `json:"-"`
}

// MarketKline 表示币安 K 线的一根蜡烛。
type MarketKline struct {
	OpenTime         int64
	Open             float64
	High             float64
	Low              float64
	Close            float64
	QuoteAssetVolume float64
}

// MonthlySimulationLogRecord 表示月度真实行情模拟的逐时点日志。
type MonthlySimulationLogRecord struct {
	TraceRecordMetadata
	RecordedAt              string
	FundingTime             string
	Action                  string
	Symbol                  string
	EstimatedFundingRate    string
	FundingRate             string
	EstimatedFundingIncome  string
	FundingIncome           string
	HedgeQty                string
	SpotEntryPrice          string
	FuturesEntryPrice       string
	SpotExitPrice           string
	FuturesExitPrice        string
	OpenBasisRate           string
	CloseBasisRate          string
	SpotEntryQuoteVolume    string
	FuturesEntryQuoteVolume string
	SpotExitQuoteVolume     string
	FuturesExitQuoteVolume  string
	SpotEntrySlipRate       string
	FuturesEntrySlipRate    string
	SpotExitSlipRate        string
	FuturesExitSlipRate     string
	SpotEntryImpactRate     string
	FuturesEntryImpactRate  string
	SpotExitImpactRate      string
	FuturesExitImpactRate   string
	SpotEntryFee            string
	FuturesEntryFee         string
	SpotExitFee             string
	FuturesExitFee          string
	OpenBasisPenalty        string
	ProjectedExitCost       string
	MinLiquidityRequired    string
	SpotGrossPnL            string
	FuturesGrossPnL         string
	TotalFees               string
	TotalFriction           string
	EstimatedNetPnL         string
	RealizedNetPnL          string
	EquityBefore            string
	EquityAfter             string
	DrawdownRate            string
	Reason                  string
	Notes                   string
}

type monthlyCandidate struct {
	Symbol                  string
	Direction               fundingCarryDirection
	FundingTime             time.Time
	ExitTime                time.Time
	HoldingCycles           int
	EstimatedFundingRate    float64
	ScreeningScore          float64
	FundingRate             float64
	EstimatedFundingIncome  float64
	FundingIncome           float64
	HedgeQty                float64
	SpotEntryPrice          float64
	FuturesEntryPrice       float64
	SpotExitPrice           float64
	FuturesExitPrice        float64
	SpotEntryQuoteVolume    float64
	FuturesEntryQuoteVolume float64
	SpotExitQuoteVolume     float64
	FuturesExitQuoteVolume  float64
	SpotEntrySlipRate       float64
	FuturesEntrySlipRate    float64
	SpotExitSlipRate        float64
	FuturesExitSlipRate     float64
	SpotEntryImpactRate     float64
	FuturesEntryImpactRate  float64
	SpotExitImpactRate      float64
	FuturesExitImpactRate   float64
	SpotEntryFee            float64
	FuturesEntryFee         float64
	SpotExitFee             float64
	FuturesExitFee          float64
	OpenBasisRate           float64
	CloseBasisRate          float64
	OpenBasisPenalty        float64
	ProjectedExitCost       float64
	MinLiquidityRequired    float64
	SpotGrossPnL            float64
	FuturesGrossPnL         float64
	TotalFees               float64
	TotalFriction           float64
	EstimatedNetPnL         float64
	RealizedNetPnL          float64
	Reason                  string
	Notes                   []string
}

type fundingCarryDirection string

const (
	fundingCarryDirectionPositive fundingCarryDirection = "positive_carry"
	fundingCarryDirectionNegative fundingCarryDirection = "negative_carry"
	historicalFundingModePositive                       = "positive_only"
	historicalFundingModeNegative                       = "negative_only"
	historicalFundingModeBoth                           = "both"
)

func normalizeHistoricalFundingDirectionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case historicalFundingModePositive:
		return historicalFundingModePositive
	case historicalFundingModeNegative:
		return historicalFundingModeNegative
	case historicalFundingModeBoth:
		return historicalFundingModeBoth
	default:
		return historicalFundingModeBoth
	}
}

func isValidHistoricalFundingDirectionMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case historicalFundingModePositive, historicalFundingModeNegative, historicalFundingModeBoth:
		return true
	default:
		return false
	}
}

func fundingDirectionFromRate(rate float64) fundingCarryDirection {
	if rate < 0 {
		return fundingCarryDirectionNegative
	}
	if rate > 0 {
		return fundingCarryDirectionPositive
	}
	return ""
}

func historicalFundingDirectionEnabled(mode string, direction fundingCarryDirection) bool {
	switch normalizeHistoricalFundingDirectionMode(mode) {
	case historicalFundingModeNegative:
		return direction == fundingCarryDirectionNegative
	case historicalFundingModeBoth:
		return direction == fundingCarryDirectionPositive || direction == fundingCarryDirectionNegative
	default:
		return direction == fundingCarryDirectionPositive
	}
}

func historicalFundingIncome(direction fundingCarryDirection, hedgeQty float64, markPrice float64, fundingRate float64, cycles int) float64 {
	if hedgeQty <= 0 || markPrice <= 0 || cycles <= 0 {
		return 0
	}
	multiplier := 1.0
	if direction == fundingCarryDirectionNegative {
		multiplier = -1
	}
	return hedgeQty * markPrice * fundingRate * float64(cycles) * multiplier
}

func (d fundingCarryDirection) spotGrossPnL(hedgeQty float64, entryPrice float64, exitPrice float64) float64 {
	switch d {
	case fundingCarryDirectionNegative:
		return hedgeQty * (entryPrice - exitPrice)
	default:
		return hedgeQty * (exitPrice - entryPrice)
	}
}

func (d fundingCarryDirection) futuresGrossPnL(hedgeQty float64, entryPrice float64, exitPrice float64) float64 {
	switch d {
	case fundingCarryDirectionNegative:
		return hedgeQty * (exitPrice - entryPrice)
	default:
		return hedgeQty * (entryPrice - exitPrice)
	}
}

func historicalFundingPriorityScore(item FundingRateHistoryItem, mode string) float64 {
	rate := parseFloat(item.EstimatedFundingRate)
	if rate == 0 {
		rate = parseFloat(item.FundingRate)
	}
	switch normalizeHistoricalFundingDirectionMode(mode) {
	case historicalFundingModeNegative:
		return math.Max(-rate, 0)
	case historicalFundingModeBoth:
		return math.Abs(rate)
	default:
		return math.Max(rate, 0)
	}
}

func monthlyPreselectPriorityScore(item FundingRateHistoryItem, mode string) float64 {
	if score := parseFloat(item.ScreeningScore); score != 0 {
		return score
	}
	return historicalFundingPriorityScore(item, mode)
}

func monthlyCandidateScreeningPriorityScore(candidate monthlyCandidate) float64 {
	if candidate.ScreeningScore != 0 {
		return candidate.ScreeningScore
	}
	return math.Abs(candidate.EstimatedFundingRate)
}

func alignHistoricalEstimatedFundingRate(estimatedRate float64, previousFundingRate float64) float64 {
	estimatedDirection := fundingDirectionFromRate(estimatedRate)
	previousDirection := fundingDirectionFromRate(previousFundingRate)
	if estimatedDirection == "" || previousDirection == "" {
		return estimatedRate
	}
	if estimatedDirection != previousDirection {
		return 0
	}
	return estimatedRate
}

func historicalBorrowInterestHours(direction fundingCarryDirection, entryTime time.Time, exitTime time.Time, minBillingHours int) int {
	if direction != fundingCarryDirectionNegative {
		return 0
	}
	hours := 1
	if exitTime.After(entryTime) {
		hours = int(math.Ceil(exitTime.Sub(entryTime).Hours()))
		if hours < 1 {
			hours = 1
		}
	}
	if minBillingHours > hours {
		hours = minBillingHours
	}
	return hours
}

func historicalBorrowInterestCost(direction fundingCarryDirection, notionalUSDT float64, hourlyRate float64, billedHours int) float64 {
	if direction != fundingCarryDirectionNegative || notionalUSDT <= 0 || hourlyRate <= 0 || billedHours <= 0 {
		return 0
	}
	return notionalUSDT * hourlyRate * float64(billedHours)
}

func historicalSpotImpactMultiplier(hcfg historicalConfig, direction fundingCarryDirection, isEntry bool) float64 {
	if direction != fundingCarryDirectionNegative {
		return 1
	}
	if isEntry {
		return math.Max(hcfg.NegativeCarrySpotEntryImpactMultiplier, 1)
	}
	return math.Max(hcfg.NegativeCarrySpotExitImpactMultiplier, 1)
}

type monthlyEventGroup struct {
	FundingTime time.Time
	RawCount    int
	Items       []FundingRateHistoryItem
}

type monthlyKlineCache struct {
	mu      sync.Mutex
	spot    map[string]MarketKline
	futures map[string]MarketKline
	index   map[string]MarketKline
	premium map[string]MarketKline
	funding map[string]FundingRateHistoryItem
	series  map[string][]FundingRateHistoryItem
	hits    map[string]int
	loads   map[string]int
}

func newMonthlyKlineCache() *monthlyKlineCache {
	return &monthlyKlineCache{
		spot:    make(map[string]MarketKline),
		futures: make(map[string]MarketKline),
		index:   make(map[string]MarketKline),
		premium: make(map[string]MarketKline),
		funding: make(map[string]FundingRateHistoryItem),
		series:  make(map[string][]FundingRateHistoryItem),
		hits:    make(map[string]int),
		loads:   make(map[string]int),
	}
}

func normalizeFundingHistoryItems(items []FundingRateHistoryItem) []FundingRateHistoryItem {
	if len(items) == 0 {
		return nil
	}
	cloned := append([]FundingRateHistoryItem(nil), items...)
	sort.Slice(cloned, func(i, j int) bool {
		if cloned[i].FundingTime == cloned[j].FundingTime {
			return cloned[i].Symbol < cloned[j].Symbol
		}
		return cloned[i].FundingTime < cloned[j].FundingTime
	})
	normalized := cloned[:0]
	for _, item := range cloned {
		item.Symbol = strings.ToUpper(strings.TrimSpace(item.Symbol))
		if len(normalized) > 0 && normalized[len(normalized)-1].FundingTime == item.FundingTime {
			normalized[len(normalized)-1] = item
			continue
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func mergeFundingHistoryItems(existing []FundingRateHistoryItem, incoming []FundingRateHistoryItem) []FundingRateHistoryItem {
	if len(existing) == 0 {
		return normalizeFundingHistoryItems(incoming)
	}
	if len(incoming) == 0 {
		return normalizeFundingHistoryItems(existing)
	}
	merged := append(append([]FundingRateHistoryItem(nil), existing...), incoming...)
	return normalizeFundingHistoryItems(merged)
}

func (c *monthlyKlineCache) storeFundingHistory(symbol string, items []FundingRateHistoryItem) {
	if c == nil || len(items) == 0 {
		return
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	normalized := normalizeFundingHistoryItems(items)
	c.mu.Lock()
	c.series[normalizedSymbol] = mergeFundingHistoryItems(c.series[normalizedSymbol], normalized)
	for _, item := range normalized {
		key := normalizedSymbol + ":" + strconv.FormatInt(item.FundingTime, 10)
		c.funding[key] = item
	}
	c.mu.Unlock()
}

func (c *monthlyKlineCache) fundingHistory(symbol string) []FundingRateHistoryItem {
	if c == nil {
		return nil
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.series[normalizedSymbol]
	if len(items) == 0 {
		return nil
	}
	return append([]FundingRateHistoryItem(nil), items...)
}

func (c *monthlyKlineCache) getFundingSchedule(ctx context.Context, client *BinanceClient, symbol string, fundingTime time.Time, cycles int) ([]FundingRateHistoryItem, error) {
	if cycles <= 0 {
		return nil, nil
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	series := c.fundingHistory(normalizedSymbol)
	if len(series) < cycles {
		lookahead := time.Duration(maxInt(24, cycles*24)) * time.Hour
		items, err := client.fetchFundingRateHistory(ctx, normalizedSymbol, fundingTime.Add(-24*time.Hour).UnixMilli(), fundingTime.Add(lookahead).UnixMilli())
		if err != nil && len(series) == 0 {
			return nil, err
		}
		if len(items) > 0 {
			c.storeFundingHistory(normalizedSymbol, items)
			series = c.fundingHistory(normalizedSymbol)
		}
	}
	if len(series) == 0 {
		return nil, fmt.Errorf("未找到 funding 结算序列")
	}
	targetMillis := fundingTime.UnixMilli()
	startIndex := sort.Search(len(series), func(i int) bool {
		return series[i].FundingTime >= targetMillis
	})
	if startIndex >= len(series) || series[startIndex].FundingTime != targetMillis {
		return nil, fmt.Errorf("未找到 fundingTime=%s 对应的结算记录", fundingTime.UTC().Format(time.RFC3339))
	}
	if len(series)-startIndex < cycles {
		available := len(series) - startIndex
		if available <= 0 {
			return nil, fmt.Errorf("funding 结算序列不足: symbol=%s requested=%d available=%d", normalizedSymbol, cycles, available)
		}
		return append([]FundingRateHistoryItem(nil), series[startIndex:]...), nil
	}
	return append([]FundingRateHistoryItem(nil), series[startIndex:startIndex+cycles]...), nil
}

func (c *monthlyKlineCache) fundingRateBefore(symbol string, fundingTime time.Time) (FundingRateHistoryItem, bool) {
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	series := c.fundingHistory(normalizedSymbol)
	if len(series) == 0 {
		return FundingRateHistoryItem{}, false
	}
	targetMillis := fundingTime.UnixMilli()
	index := sort.Search(len(series), func(i int) bool {
		return series[i].FundingTime >= targetMillis
	})
	if index <= 0 {
		return FundingRateHistoryItem{}, false
	}
	return series[index-1], true
}

type monthlyExperimentPlan struct {
	Index int
	Total int
	Cfg   EngineConfig
}

type monthlyFundingFetchStats struct {
	SuccessSymbols int
	FailureCount   int
	FailureReasons map[string]int
	FailureSamples map[string][]string
	RequestSummary string
}

func (s monthlyFundingFetchStats) successRate(total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(s.SuccessSymbols) / float64(total)
}

type monthlySimulationStats struct {
	GroupCount          int
	EvaluatedCandidates int
	SkippedGroups       int
	TradeCount          int
	SkipReasons         map[string]int
	CandidateReasons    map[string]int
	CacheSummary        string
}

type monthlyLogAggregation struct {
	counts  map[string]int
	samples map[string][]string
}

type historicalConfig struct {
	DefaultLookbackDays                    int
	InitialEquityUSDT                      float64
	StopEquityUSDT                         float64
	PerLegNotionalUSDT                     float64
	FundingDirectionMode                   string
	MaxFundingCyclesHeld                   int
	MaxPerLegEquityShare                   float64
	SpotTakerFeeRate                       float64
	FuturesTakerFeeRate                    float64
	AssumedHourlyBorrowInterestRate        float64
	BorrowInterestMinBillingHours          int
	NegativeCarrySpotEntryImpactMultiplier float64
	NegativeCarrySpotExitImpactMultiplier  float64
	CrossStableQuoteMaxDeviationRate       float64
	FundingPhaseLead                       time.Duration
	FundingFetchConcurrency                int
	FundingRetryMax                        int
	CacheDir                               string
	ReferenceKlineLookback                 int
	EstimateKlineLookback                  int
	SafetyBufferFloorUSDT                  float64
	SafetyBufferCostMultiplier             float64
	MaxDrawdownRate                        float64
	MaxConsecutiveLosses                   int
	MaxFundingRate                         float64
	MaxFundingRateJump                     float64
	MaxSingleTradeLossUSDT                 float64
	FundingFetchSlot                       time.Duration
	FundingFetchTimeout                    time.Duration
	GroupEvaluationTimeout                 time.Duration
	CandidateBuildTimeout                  time.Duration
	CandidateFinalizeTimeout               time.Duration
	ScreeningConcurrency                   int
	DeepCandidateLimit                     int
	CoarseSymbolLimit                      int
	FundingGroupTopSymbolsPerTime          int
	LiquidityShareMin                      float64
	LiquidityShareFloor                    float64
	LiquidityShareMax                      float64
	SevereLiquidityShareCap                float64
	SevereLiquidityShareMultiplier         float64
	SevereLiquidityShareMax                float64
}

// RunMonthlyMarketSimulation 拉取过去一段时间的真实历史数据，并按资金费套利规则做回放。
func (r *BacktestRunner) RunMonthlyMarketSimulation(ctx context.Context, secretPath string, lookbackDays int) ([]BacktestCSVRecord, error) {
	hcfg := r.historicalConfig()
	if lookbackDays <= 0 {
		lookbackDays = hcfg.DefaultLookbackDays
	}

	client, err := newHistoricalBacktestClient(secretPath, r.cfg.HTTPTimeout, r.cfg.NetworkConfig, hcfg)
	if err != nil {
		return nil, err
	}

	end := time.Now().UTC().Truncate(time.Minute)
	start := end.AddDate(0, 0, -lookbackDays)
	periodLabel := fmt.Sprintf("%s~%s", start.Format("2006-01-02"), end.Format("2006-01-02"))

	symbols, err := client.ListGoldenSymbols(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取黄金币种失败: %w", err)
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("没有可用于月度模拟的黄金币种")
	}
	spotPairs := make([]string, 0, len(symbols))
	borrowAssets := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if pair := client.historicalSpotPairForFuture(symbol); strings.TrimSpace(pair) != "" {
			spotPairs = append(spotPairs, pair)
		}
		if meta, ok := client.historicalSpotMetaForFuture(symbol); ok && strings.TrimSpace(meta.BaseAsset) != "" {
			borrowAssets = append(borrowAssets, meta.BaseAsset)
		}
	}
	if profile, profileErr := client.resolveAccountTradingCostProfile(ctx, spotPairs, symbols, borrowAssets); profileErr != nil {
		if profile.SpotTakerFeeRate > 0 {
			r.cfg.HistoricalConfig.SpotTakerFeeRate = profile.SpotTakerFeeRate
		}
		if profile.FuturesTakerFeeRate > 0 {
			r.cfg.HistoricalConfig.FuturesTakerFeeRate = profile.FuturesTakerFeeRate
		}
		if len(profile.BorrowHourlyRateByAsset) > 0 {
			r.borrowHourlyInterestByAsset = profile.BorrowHourlyRateByAsset
		}
		hcfg = r.historicalConfig()
		if r.logger != nil {
			r.logger.Printf("月度模拟加载账户真实成本存在部分失败，已尽量使用成功结果并对其余项回退: %v", profileErr)
			r.logger.Printf("月度模拟当前成本参数: spot_taker=%.6f(symbol=%s) futures_taker=%.6f(symbol=%s) borrow_assets=%d",
				hcfg.SpotTakerFeeRate, profile.SpotFeeSymbol, hcfg.FuturesTakerFeeRate, profile.FuturesFeeSymbol, len(r.borrowHourlyInterestByAsset))
		}
	} else {
		if profile.SpotTakerFeeRate > 0 {
			r.cfg.HistoricalConfig.SpotTakerFeeRate = profile.SpotTakerFeeRate
		}
		if profile.FuturesTakerFeeRate > 0 {
			r.cfg.HistoricalConfig.FuturesTakerFeeRate = profile.FuturesTakerFeeRate
		}
		r.borrowHourlyInterestByAsset = profile.BorrowHourlyRateByAsset
		hcfg = r.historicalConfig()
		if r.logger != nil {
			r.logger.Printf("月度模拟已加载账户真实成本: spot_taker=%.6f(symbol=%s) futures_taker=%.6f(symbol=%s) borrow_assets=%d",
				hcfg.SpotTakerFeeRate, profile.SpotFeeSymbol, hcfg.FuturesTakerFeeRate, profile.FuturesFeeSymbol, profile.BorrowInterestResolvedCount)
		}
	}

	experiments := r.monthlyExperimentPlans()
	coarseLimit := r.monthlyCoarseSymbolLimit(len(symbols))
	if r.logger != nil {
		r.logger.Printf("月度真实行情模拟启动，区间=%s，黄金币种数=%d，粗筛上限=%d，实验组数=%d，初始单组参数: 单腿名义=%.2f，止损权益=%.2f，现货费率=%.4f，合约费率=%.4f，持有 funding 次数=%d",
			periodLabel, len(symbols), coarseLimit, len(experiments), r.monthlyPerLegNotional(), hcfg.StopEquityUSDT, hcfg.SpotTakerFeeRate, hcfg.FuturesTakerFeeRate, r.monthlyHoldingCycles())
	}
	r.reportProgress(EngineStage("coarse_screen"), fmt.Sprintf("正在按 premiumIndex 粗筛历史币池: 0/%d", len(symbols)), 0, len(symbols))
	screenedSymbols, err := r.coarseScreenMonthlySymbols(ctx, client, symbols, end)
	if err != nil {
		return nil, err
	}
	if len(screenedSymbols) == 0 {
		return nil, fmt.Errorf("粗筛后没有可用于月度模拟的币种")
	}
	cache := newMonthlyKlineCache()
	r.reportProgress(EngineStage("fetch_funding"), fmt.Sprintf("正在按时间轴构建历史候选: 0/%d", len(screenedSymbols)), 0, len(screenedSymbols))

	groups, fetchStats, err := r.buildMonthlyTimelineGroups(ctx, client, cache, screenedSymbols, start, end)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fetchStats.RequestSummary = client.historicalRequestSummary()
	allRecords, allLogs, totalTrades, err := r.runMonthlyExperimentPlans(ctx, client, cache, groups, periodLabel, fetchStats, experiments)
	if err != nil {
		return nil, err
	}

	if err := writeMonthlySimulationLogs(r.recordDir(), r.cfg, allLogs); err != nil {
		return nil, err
	}

	if r.store != nil {
		if err := r.store.ReplaceBacktestRecords(allRecords); err != nil {
			return nil, err
		}
		if err := r.store.AppendBacktestRunSummaries("monthly_market", allRecords); err != nil {
			return nil, err
		}
		if err := r.store.ReplaceResidualRiskRecords(nil); err != nil {
			return nil, err
		}
	}

	if r.logger != nil {
		r.logger.Printf("月度真实行情模拟完成，区间=%s，实验组数=%d，总执行交易=%d，输出汇总=%d", periodLabel, len(experiments), totalTrades, len(allRecords))
	}
	r.reportProgress(StageCompleted, fmt.Sprintf("月度历史模拟完成，实验组=%d，总执行交易=%d，输出汇总=%d", len(experiments), totalTrades, len(allRecords)), len(experiments), len(experiments))

	return allRecords, nil
}

func (r *BacktestRunner) buildMonthlyTimelineGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbols []string, start time.Time, end time.Time) ([]monthlyEventGroup, monthlyFundingFetchStats, error) {
	hcfg := r.historicalConfig()
	groups, stats, err := r.fetchMonthlyFundingGroups(ctx, client, cache, symbols, start, end)
	if err != nil {
		return nil, stats, err
	}
	groups = mergeSparseMonthlyEventGroups(groups, monthlyFundingSparseGroupMinItems, monthlyFundingGroupMergeWindow, hcfg.FundingDirectionMode)
	totalObservations := 0
	for _, group := range groups {
		totalObservations += len(group.Items)
	}
	if totalObservations == 0 {
		return nil, stats, fmt.Errorf("指定区间内没有可评估的 funding 结算记录")
	}
	processedObservations := 0
	preselectConcurrency := maxInt(1, minInt(hcfg.FundingFetchConcurrency, maxInt(1, hcfg.ScreeningConcurrency)))
	enrichedGroups := make([]monthlyEventGroup, 0, len(groups))

	for index, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		fundingTime := group.FundingTime
		entryTime := fundingTime.Add(-hcfg.FundingPhaseLead)
		type result struct {
			symbol string
			item   FundingRateHistoryItem
			err    error
		}
		results := make(chan result, len(group.Items))
		sem := make(chan struct{}, preselectConcurrency)
		var wg sync.WaitGroup

		for _, actualItem := range group.Items {
			actualItem := actualItem
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					results <- result{symbol: actualItem.Symbol, err: ctx.Err()}
					return
				}
				defer func() { <-sem }()

				symbolCtx, cancel := context.WithTimeout(ctx, hcfg.CandidateBuildTimeout)
				defer cancel()

				item, buildErr := r.buildMonthlyTimelinePreselectItem(symbolCtx, client, cache, actualItem.Symbol, fundingTime, entryTime)
				if buildErr == nil {
					item.FundingRate = actualItem.FundingRate
					item.FundingTime = actualItem.FundingTime
				}
				results <- result{symbol: actualItem.Symbol, item: item, err: buildErr}
			}()
		}
		go func() {
			wg.Wait()
			close(results)
		}()

		items := make([]FundingRateHistoryItem, 0, len(group.Items))
		for result := range results {
			processedObservations++
			if result.err != nil {
				stats.FailureCount++
				reason := classifyMonthlyFetchError(result.err)
				stats.FailureReasons[reason]++
				if len(stats.FailureSamples[reason]) < 5 {
					stats.FailureSamples[reason] = append(stats.FailureSamples[reason], result.symbol)
				}
			} else {
				items = append(items, result.item)
			}
			if r.logger != nil && (processedObservations == totalObservations || processedObservations%monthlyFundingFetchProgressStep == 0) {
				r.logger.Printf("月度模拟 funding 结算预选进度：%d/%d，symbolFetchSuccess=%d，失败=%d", processedObservations, totalObservations, stats.SuccessSymbols, stats.FailureCount)
			}
			r.reportProgress(EngineStage("fetch_funding"),
				fmt.Sprintf("正在按真实 funding 结算构建历史候选: %d/%d，symbolFetchSuccess=%d，失败=%d", processedObservations, totalObservations, stats.SuccessSymbols, stats.FailureCount),
				processedObservations, totalObservations)
		}

		sort.SliceStable(items, func(i, j int) bool {
			left := monthlyPreselectPriorityScore(items[i], hcfg.FundingDirectionMode)
			right := monthlyPreselectPriorityScore(items[j], hcfg.FundingDirectionMode)
			if left == right {
				return items[i].Symbol < items[j].Symbol
			}
			return left > right
		})
		rawCount := len(items)
		items = trimMonthlyFundingGroupItems(items, hcfg.FundingGroupTopSymbolsPerTime)
		if len(items) > 0 {
			enrichedGroups = append(enrichedGroups, monthlyEventGroup{
				FundingTime: fundingTime,
				RawCount:    rawCount,
				Items:       items,
			})
		}
		if r.logger != nil {
			r.logger.Printf("月度模拟真实 funding 结算预选完成：index=%d/%d fundingTime=%s raw=%d retained=%d",
				index+1, len(groups), fundingTime.Format(time.RFC3339), rawCount, len(items))
		}
	}

	if len(enrichedGroups) == 0 {
		return nil, stats, fmt.Errorf("所有 funding 结算时点都未构建出候选")
	}
	if r.logger != nil {
		r.logger.Printf("月度模拟真实 funding 结算预选汇总：时点=%d，symbolFetchSuccess=%d，失败=%d，失败分类=%s", len(enrichedGroups), stats.SuccessSymbols, stats.FailureCount, summarizeMonthlyReasonCounts(stats.FailureReasons, stats.FailureSamples))
	}
	return enrichedGroups, stats, nil
}

func mergeSparseMonthlyEventGroups(groups []monthlyEventGroup, minItems int, maxWindow time.Duration, fundingDirectionMode string) []monthlyEventGroup {
	if len(groups) <= 1 || minItems <= 1 {
		return groups
	}
	merged := make([]monthlyEventGroup, 0, len(groups))
	pendingItems := make([]FundingRateHistoryItem, 0, minItems)
	pendingRawCount := 0
	var pendingStart time.Time
	var pendingEnd time.Time
	resetPending := func() {
		pendingItems = pendingItems[:0]
		pendingRawCount = 0
		pendingStart = time.Time{}
		pendingEnd = time.Time{}
	}
	flushPending := func(anchor time.Time) {
		if pendingRawCount == 0 {
			return
		}
		items := append([]FundingRateHistoryItem(nil), pendingItems...)
		sort.SliceStable(items, func(i, j int) bool {
			left := historicalFundingPriorityScore(items[i], fundingDirectionMode)
			right := historicalFundingPriorityScore(items[j], fundingDirectionMode)
			if left == right {
				return items[i].Symbol < items[j].Symbol
			}
			return left > right
		})
		merged = append(merged, monthlyEventGroup{
			FundingTime: anchor,
			RawCount:    pendingRawCount,
			Items:       trimMonthlyFundingGroupItems(items, monthlyFundingTopSymbolsPerTime),
		})
		resetPending()
	}
	appendPending := func(group monthlyEventGroup) {
		if pendingRawCount == 0 {
			pendingStart = group.FundingTime
		}
		pendingEnd = group.FundingTime
		pendingRawCount += group.RawCount
		pendingItems = append(pendingItems, group.Items...)
	}

	for _, group := range groups {
		if group.RawCount >= minItems {
			if pendingRawCount > 0 {
				if maxWindow <= 0 || group.FundingTime.Sub(pendingStart) <= maxWindow {
					items := append([]FundingRateHistoryItem(nil), pendingItems...)
					items = append(items, group.Items...)
					sort.SliceStable(items, func(i, j int) bool {
						left := historicalFundingPriorityScore(items[i], fundingDirectionMode)
						right := historicalFundingPriorityScore(items[j], fundingDirectionMode)
						if left == right {
							return items[i].Symbol < items[j].Symbol
						}
						return left > right
					})
					merged = append(merged, monthlyEventGroup{
						FundingTime: group.FundingTime,
						RawCount:    pendingRawCount + group.RawCount,
						Items:       trimMonthlyFundingGroupItems(items, monthlyFundingTopSymbolsPerTime),
					})
					resetPending()
					continue
				}
				flushPending(pendingEnd)
			}
			merged = append(merged, group)
			continue
		}
		if pendingRawCount > 0 && maxWindow > 0 && group.FundingTime.Sub(pendingStart) > maxWindow {
			flushPending(pendingEnd)
		}
		appendPending(group)
	}
	if pendingRawCount > 0 {
		flushPending(pendingEnd)
	}
	return merged
}

type monthlyCoarseSymbol struct {
	Symbol               string
	EstimatedFundingRate float64
	FundingRateJump      float64
	BasisRate            float64
	ScanScoreRate        float64
	PriorityScore        float64
}

func (r *BacktestRunner) coarseScreenMonthlySymbols(ctx context.Context, client *BinanceClient, symbols []string, snapshotTime time.Time) ([]string, error) {
	limit := r.monthlyCoarseSymbolLimit(len(symbols))
	if limit <= 0 || limit >= len(symbols) {
		return append([]string(nil), symbols...), nil
	}
	items, err := client.fetchPremiumIndex(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 拉取失败，回退到全量 symbol，err=%v", err)
		}
		return append([]string(nil), symbols...), nil
	}
	if len(items) == 0 {
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 返回为空，回退到全量 symbol")
		}
		return append([]string(nil), symbols...), nil
	}
	hcfg := r.historicalConfig()
	symbolSet := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if normalized == "" {
			continue
		}
		symbolSet[normalized] = struct{}{}
	}
	candidates := make([]monthlyCoarseSymbol, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	staleSkipped := 0
	directionSkipped := 0
	jumpSkipped := 0
	for _, item := range items {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if _, ok := symbolSet[symbol]; !ok {
			continue
		}
		seen[symbol] = struct{}{}
		if r.cfg.NetworkConfig.MaxPremiumIndexStaleness > 0 {
			staleness := premiumIndexStaleness(snapshotTime, item)
			if staleness > r.cfg.NetworkConfig.MaxPremiumIndexStaleness {
				staleSkipped++
				continue
			}
		}
		estimatedFundingRate, _, fundingJump, basisRate, scanScoreRate := estimateScanFundingSignal(item)
		priorityScore := historicalFundingPriorityScore(FundingRateHistoryItem{
			Symbol:               symbol,
			EstimatedFundingRate: formatFloat(estimatedFundingRate),
		}, hcfg.FundingDirectionMode)
		if priorityScore <= 0 {
			directionSkipped++
			continue
		}
		if hcfg.MaxFundingRateJump > 0 && fundingJump >= hcfg.MaxFundingRateJump {
			jumpSkipped++
			continue
		}
		candidates = append(candidates, monthlyCoarseSymbol{
			Symbol:               symbol,
			EstimatedFundingRate: estimatedFundingRate,
			FundingRateJump:      fundingJump,
			BasisRate:            basisRate,
			ScanScoreRate:        scanScoreRate,
			PriorityScore:        priorityScore,
		})
	}
	if len(candidates) == 0 {
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 没筛出候选，回退到全量 symbol，matched=%d stale=%d direction=%d jump=%d",
				len(seen), staleSkipped, directionSkipped, jumpSkipped)
		}
		return append([]string(nil), symbols...), nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].PriorityScore == candidates[j].PriorityScore {
			if candidates[i].ScanScoreRate == candidates[j].ScanScoreRate {
				if candidates[i].FundingRateJump == candidates[j].FundingRateJump {
					if math.Abs(candidates[i].BasisRate) == math.Abs(candidates[j].BasisRate) {
						return candidates[i].Symbol < candidates[j].Symbol
					}
					return math.Abs(candidates[i].BasisRate) < math.Abs(candidates[j].BasisRate)
				}
				return candidates[i].FundingRateJump < candidates[j].FundingRateJump
			}
			return candidates[i].ScanScoreRate > candidates[j].ScanScoreRate
		}
		return candidates[i].PriorityScore > candidates[j].PriorityScore
	})
	retained := minInt(limit, len(candidates))
	selected := make([]string, 0, limit)
	selectedSet := make(map[string]struct{}, limit)
	for index := 0; index < retained; index++ {
		selected = append(selected, candidates[index].Symbol)
		selectedSet[candidates[index].Symbol] = struct{}{}
	}
	for _, symbol := range symbols {
		if len(selected) >= limit {
			break
		}
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if _, ok := selectedSet[normalized]; ok {
			continue
		}
		selected = append(selected, normalized)
		selectedSet[normalized] = struct{}{}
	}
	if r.logger != nil {
		topSymbol := ""
		topScore := 0.0
		if len(candidates) > 0 {
			topSymbol = candidates[0].Symbol
			topScore = candidates[0].PriorityScore
		}
		r.logger.Printf("月度模拟 coarse 粗筛完成：golden=%d matched=%d retained=%d/%d fallbackFill=%d stale=%d direction=%d jump=%d top=%s score=%.6f",
			len(symbols), len(seen), len(selected), limit, maxInt(len(selected)-retained, 0), staleSkipped, directionSkipped, jumpSkipped, topSymbol, topScore)
	}
	r.reportProgress(EngineStage("coarse_screen"),
		fmt.Sprintf("正在按 premiumIndex 粗筛历史币池: %d/%d，保留=%d", len(symbols), len(symbols), len(selected)),
		len(symbols), len(symbols))
	return selected, nil
}

func monthlyFundingTimeline(start time.Time, end time.Time) []time.Time {
	if !end.After(start) {
		return nil
	}
	cursor := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	for cursor.Before(start.UTC()) {
		cursor = cursor.Add(8 * time.Hour)
	}
	timeline := make([]time.Time, 0, int(end.Sub(start)/(8*time.Hour))+2)
	for !cursor.After(end.UTC()) {
		timeline = append(timeline, cursor)
		cursor = cursor.Add(8 * time.Hour)
	}
	return timeline
}

func (r *BacktestRunner) buildMonthlyTimelinePreselectItem(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, fundingTime time.Time, entryTime time.Time) (FundingRateHistoryItem, error) {
	premiumEntryRef, err := cache.getFuturesPremiumKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}
	indexEntryRef, err := cache.getFuturesIndexKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}

	preFundingBasisRate := 0.0
	estimatedMarkPrice := indexEntryRef.Close
	if premiumEntryRef.Close > -1 && indexEntryRef.Close > 0 {
		estimatedMarkPrice = indexEntryRef.Close * (1 + premiumEntryRef.Close)
	}
	if indexEntryRef.Close > 0 && estimatedMarkPrice > 0 {
		preFundingBasisRate = (estimatedMarkPrice - indexEntryRef.Close) / indexEntryRef.Close
	}
	estimatedFundingRate := conservativeHistoricalFundingRate(premiumEntryRef.Close, preFundingBasisRate)
	if previousFunding, ok := cache.fundingRateBefore(symbol, fundingTime); ok {
		estimatedFundingRate = alignHistoricalEstimatedFundingRate(estimatedFundingRate, parseFloat(previousFunding.FundingRate))
	}
	screeningScore := r.estimateMonthlyPreselectScore(ctx, client, cache, symbol, fundingTime, entryTime, estimatedFundingRate, estimatedMarkPrice)

	return FundingRateHistoryItem{
		Symbol:               symbol,
		FundingTime:          fundingTime.UnixMilli(),
		MarkPrice:            formatFloat(estimatedMarkPrice),
		EstimatedFundingRate: formatFloat(estimatedFundingRate),
		ScreeningScore:       formatFloat(screeningScore),
	}, nil
}

func alignHistoricalExperimentCapital(cfg EngineConfig) EngineConfig {
	perLeg := cfg.HistoricalConfig.PerLegNotionalUSDT
	if perLeg <= 0 || cfg.HistoricalConfig.InitialEquityUSDT <= 0 {
		return cfg
	}
	requiredShare := perLeg / cfg.HistoricalConfig.InitialEquityUSDT
	if requiredShare > 1 {
		requiredShare = 1
	}
	if requiredShare > cfg.HistoricalConfig.MaxPerLegEquityShare {
		cfg.HistoricalConfig.MaxPerLegEquityShare = requiredShare
	}
	return cfg
}

func (r *BacktestRunner) fetchMonthlyFundingGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbols []string, start time.Time, end time.Time) ([]monthlyEventGroup, monthlyFundingFetchStats, error) {
	hcfg := r.historicalConfig()
	startMillis := start.UnixMilli()
	endMillis := end.UnixMilli()

	type result struct {
		symbol string
		items  []FundingRateHistoryItem
		err    error
	}

	grouped := make(map[int64][]FundingRateHistoryItem)
	stats := monthlyFundingFetchStats{
		FailureReasons: make(map[string]int),
		FailureSamples: make(map[string][]string),
	}
	successCount := 0
	processedCount := 0
	consecutive202Batches := 0
	consecutive429Batches := 0
	consecutive403Batches := 0
	consecutive418Batches := 0

	batchSize := monthlyFundingFetchBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	batchConcurrency := maxInt(1, hcfg.FundingFetchConcurrency)
	missingSymbols := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		items, ok, err := client.loadCachedFundingRateHistory(symbol, startMillis, endMillis)
		processedCount++
		switch {
		case err != nil:
			stats.FailureCount++
			reason := classifyMonthlyFetchError(err)
			stats.FailureReasons[reason]++
			if len(stats.FailureSamples[reason]) < 5 {
				stats.FailureSamples[reason] = append(stats.FailureSamples[reason], symbol)
			}
			missingSymbols = append(missingSymbols, symbol)
		case ok:
			successCount++
			stats.SuccessSymbols++
			if cache != nil {
				cache.storeFundingHistory(symbol, items)
			}
			for _, item := range items {
				grouped[item.FundingTime] = append(grouped[item.FundingTime], item)
			}
		default:
			missingSymbols = append(missingSymbols, symbol)
		}
		if r.logger != nil && (processedCount == len(symbols) || processedCount%monthlyFundingFetchProgressStep == 0) {
			r.logger.Printf("月度模拟 funding 缓存预热进度：%d/%d，成功=%d，待补抓=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, len(missingSymbols), stats.FailureCount)
		}
		r.reportProgress(EngineStage("fetch_funding"),
			fmt.Sprintf("正在加载 funding 历史缓存: %d/%d，成功=%d，待补抓=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, len(missingSymbols), stats.FailureCount),
			processedCount, len(symbols))
	}

	processedCount = successCount + stats.FailureCount
	for batchStart := 0; batchStart < len(missingSymbols); batchStart += batchSize {
		batchEnd := minInt(len(missingSymbols), batchStart+batchSize)
		batchSymbols := missingSymbols[batchStart:batchEnd]
		results := make(chan result, len(batchSymbols))
		sem := make(chan struct{}, batchConcurrency)
		batchFailures := make(map[string]int)
		var wg sync.WaitGroup

		for _, symbol := range batchSymbols {
			wg.Add(1)
			go func(sym string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				symbolCtx, cancel := context.WithTimeout(ctx, hcfg.FundingFetchTimeout)
				defer cancel()

				items, err := client.fetchFundingRateHistory(symbolCtx, sym, startMillis, endMillis)
				results <- result{symbol: sym, items: items, err: err}
			}(symbol)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		for result := range results {
			processedCount++
			if result.err != nil {
				stats.FailureCount++
				reason := classifyMonthlyFetchError(result.err)
				stats.FailureReasons[reason]++
				batchFailures[reason]++
				if len(stats.FailureSamples[reason]) < 5 {
					stats.FailureSamples[reason] = append(stats.FailureSamples[reason], result.symbol)
				}
			} else {
				successCount++
				stats.SuccessSymbols++
				if cache != nil {
					cache.storeFundingHistory(result.symbol, result.items)
				}
				for _, item := range result.items {
					grouped[item.FundingTime] = append(grouped[item.FundingTime], item)
				}
			}
			if r.logger != nil && (processedCount == len(symbols) || processedCount%monthlyFundingFetchProgressStep == 0) {
				r.logger.Printf("月度模拟 funding 拉取进度：%d/%d，成功=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, stats.FailureCount)
			}
			r.reportProgress(EngineStage("fetch_funding"),
				fmt.Sprintf("正在拉取 funding 历史: %d/%d，成功=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, stats.FailureCount),
				processedCount, len(symbols))
		}
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		if shouldAbortMonthlyFundingFetch(batchFailures, len(batchSymbols), &consecutive202Batches, &consecutive429Batches, &consecutive403Batches, &consecutive418Batches) {
			reasons := summarizeMonthlyReasonCounts(batchFailures, nil)
			if r.logger != nil {
				r.logger.Printf("月度模拟 funding 提前中止：batch=%d-%d reasons=%s", batchStart+1, batchEnd, reasons)
			}
			return nil, stats, fmt.Errorf("连续批次遭遇访问拒绝，已提前中止本轮 funding 拉取: %s", reasons)
		}
		nextBatchConcurrency := adaptiveMonthlyFundingFetchConcurrency(hcfg.FundingFetchConcurrency, batchConcurrency, len(batchSymbols), batchFailures)
		if r.logger != nil && nextBatchConcurrency != batchConcurrency {
			r.logger.Printf("月度模拟 funding 并发调整：batch=%d-%d current=%d next=%d reasons=%s",
				batchStart+1, batchEnd, batchConcurrency, nextBatchConcurrency, summarizeMonthlyReasonCounts(batchFailures, nil))
		}
		batchConcurrency = nextBatchConcurrency
		if batchEnd < len(symbols) && ctx.Err() == nil {
			pause := adaptiveMonthlyFundingBatchPause(batchFailures, len(batchSymbols))
			if pause > 0 && r.logger != nil {
				r.logger.Printf("月度模拟 funding 批次暂停：batch=%d-%d pause=%s next_concurrency=%d reasons=%s",
					batchStart+1, batchEnd, pause, batchConcurrency, summarizeMonthlyReasonCounts(batchFailures, nil))
			}
			if pause > 0 && !sleepWithContext(ctx, pause) {
				return nil, stats, ctx.Err()
			}
		}
	}

	if successCount == 0 {
		return nil, stats, fmt.Errorf("所有 symbol 的 fundingRate 拉取都失败了")
	}
	stats.RequestSummary = client.historicalRequestSummary()
	if r.logger != nil {
		r.logger.Printf("月度模拟 fundingRate 拉取汇总：成功=%d 失败=%d 失败分类=%s", stats.SuccessSymbols, stats.FailureCount, summarizeMonthlyReasonCounts(stats.FailureReasons, stats.FailureSamples))
		if strings.TrimSpace(stats.RequestSummary) != "" {
			r.logger.Printf("月度模拟历史请求摘要：%s", stats.RequestSummary)
		}
		successRate := stats.successRate(len(symbols))
		if successRate < monthlyFundingDataCompleteFloor {
			r.logger.Printf("月度模拟 funding 数据完整性不足：successRate=%.4f threshold=%.2f", successRate, monthlyFundingDataCompleteFloor)
		}
	}

	times := make([]int64, 0, len(grouped))
	for fundingTime := range grouped {
		times = append(times, fundingTime)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	groups := make([]monthlyEventGroup, 0, len(times))
	for _, fundingTime := range times {
		items := grouped[fundingTime]
		sort.Slice(items, func(i, j int) bool {
			left := historicalFundingPriorityScore(items[i], r.historicalConfig().FundingDirectionMode)
			right := historicalFundingPriorityScore(items[j], r.historicalConfig().FundingDirectionMode)
			if left == right {
				return items[i].Symbol < items[j].Symbol
			}
			return left > right
		})
		rawCount := len(items)
		items = trimMonthlyFundingGroupItems(items, r.historicalConfig().FundingGroupTopSymbolsPerTime)
		groups = append(groups, monthlyEventGroup{
			FundingTime: time.UnixMilli(fundingTime).UTC(),
			RawCount:    rawCount,
			Items:       items,
		})
	}
	return groups, stats, nil
}

func (r *BacktestRunner) simulateMonthlyFundingGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, groups []monthlyEventGroup, periodLabel string) ([]BacktestTradeSample, []MonthlySimulationLogRecord, monthlySimulationStats, string, error) {
	hcfg := r.historicalConfig()
	equity := hcfg.InitialEquityUSDT
	peakEquity := hcfg.InitialEquityUSDT
	stopReason := "lookback_completed"
	consecutiveLosses := 0
	stats := monthlySimulationStats{
		GroupCount:       len(groups),
		SkipReasons:      make(map[string]int),
		CandidateReasons: make(map[string]int),
	}

	trades := make([]BacktestTradeSample, 0, len(groups))
	logs := make([]MonthlySimulationLogRecord, 0, len(groups)+1)
	processedGroups := 0

	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, nil, stats, "context_canceled", err
		}
		processedGroups++
		drawdownBefore := computeDrawdownRate(peakEquity, equity)
		if r.logger != nil && (processedGroups == len(groups) || processedGroups%monthlyEvaluationProgressStep == 0) {
			r.logger.Printf("月度模拟评估进度：%d/%d，已评估候选=%d，已成交=%d，已跳过轮次=%d", processedGroups, len(groups), stats.EvaluatedCandidates, stats.TradeCount, stats.SkippedGroups)
		}
		r.reportProgress(EngineStage("evaluate_groups"),
			fmt.Sprintf("正在评估 funding 时点: %d/%d，已成交=%d，已跳过=%d", processedGroups, len(groups), stats.TradeCount, stats.SkippedGroups),
			processedGroups, len(groups))
		if drawdownBefore >= hcfg.MaxDrawdownRate || equity <= hcfg.StopEquityUSDT {
			stopReason = "drawdown_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				EquityBefore: formatFloat(equity),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownBefore),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("权益回撤已达到 %.0f%% 或权益已跌到 %.2f USDT 以下，停止后续模拟", hcfg.MaxDrawdownRate*100, hcfg.StopEquityUSDT),
			})
			break
		}

		best, evaluated, groupReason, err := r.selectBestMonthlyCandidate(ctx, client, cache, group)
		if err != nil {
			return nil, nil, stats, nonEmpty(groupReason, "context_canceled"), err
		}
		stats.EvaluatedCandidates += len(evaluated)
		bestFound := false
		if best != nil {
			bestFound = true
		}
		for _, candidate := range evaluated {
			action := "evaluate_skip"
			reason := candidate.Reason
			extraNotes := []string{}
			if strings.TrimSpace(candidate.Reason) != "" {
				stats.CandidateReasons[candidate.Reason]++
			}
			switch {
			case candidate.Reason != "":
			case bestFound && candidate.Symbol == best.Symbol:
				action = "evaluate_selected"
				reason = "selected_for_trade"
			default:
				action = "evaluate_ranked_out"
				reason = "viable_but_not_top1"
				extraNotes = append(extraNotes, fmt.Sprintf("bestEstimatedNetPnL=%.6f", best.EstimatedNetPnL))
			}
			logs = append(logs, buildMonthlyCandidateLogRecord(candidate, action, equity, equity, drawdownBefore, reason, extraNotes...))
		}
		logs = append(logs, buildMonthlyGroupSummaryLogRecord(group, evaluated, best, equity, drawdownBefore, groupReason))
		if best == nil {
			stats.SkippedGroups++
			skipped := summarizeMonthlyGroupSkip(group, evaluated, groupReason, hcfg.GroupEvaluationTimeout)
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "skip",
				EquityBefore: formatFloat(equity),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownBefore),
				Reason:       nonEmpty(skipped.Reason, "no_viable_candidate"),
				Notes:        strings.Join(skipped.Notes, " | "),
			})
			stats.SkipReasons[nonEmpty(skipped.Reason, "no_viable_candidate")]++
			continue
		}

		equityBefore := equity
		equity += best.RealizedNetPnL
		stats.TradeCount++
		if equity > peakEquity {
			peakEquity = equity
		}
		drawdownRate := computeDrawdownRate(peakEquity, equity)
		drawdownAmount := math.Max(0, peakEquity-equity)
		if best.RealizedNetPnL < 0 {
			consecutiveLosses++
		} else {
			consecutiveLosses = 0
		}

		logs = append(logs, buildMonthlyTradeLogRecord(best, "trade", equityBefore, equity, drawdownRate))
		trades = append(trades, BacktestTradeSample{
			Scenario:      r.monthlyScenarioName("MonthlyMarketReplay"),
			Symbol:        best.Symbol,
			Period:        periodLabel,
			TradeID:       best.FundingTime.Format(time.RFC3339),
			FundingIncome: best.FundingIncome,
			TradingPnL:    best.SpotGrossPnL + best.FuturesGrossPnL - best.TotalFriction,
			TotalFees:     best.TotalFees,
			Win:           best.RealizedNetPnL > 0,
			Drawdown:      -drawdownAmount,
			Notes:         strings.Join(best.Notes, " | "),
		})

		if r.logger != nil {
			r.logger.Printf("月度模拟成交 fundingTime=%s symbol=%s fundingRate=%.6f fundingIncome=%.6f openBasis=%.6f closeBasis=%.6f fees=%.6f friction=%.6f realizedNet=%.6f equity=%.6f drawdown=%.6f",
				best.FundingTime.Format(time.RFC3339), best.Symbol, best.FundingRate, best.FundingIncome, best.OpenBasisRate, best.CloseBasisRate, best.TotalFees, best.TotalFriction, best.RealizedNetPnL, equity, drawdownRate)
		}
		if hcfg.MaxSingleTradeLossUSDT > 0 && best.RealizedNetPnL <= -hcfg.MaxSingleTradeLossUSDT {
			stopReason = "single_trade_loss_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("单笔亏损 %.6f USDT 已达到阈值 %.6f USDT，停止后续模拟", -best.RealizedNetPnL, hcfg.MaxSingleTradeLossUSDT),
			})
			break
		}

		if consecutiveLosses >= hcfg.MaxConsecutiveLosses {
			stopReason = "consecutive_losses_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("连续亏损次数已达到 %d 次，停止后续模拟", hcfg.MaxConsecutiveLosses),
			})
			break
		}

		if drawdownRate >= hcfg.MaxDrawdownRate || equity <= hcfg.StopEquityUSDT {
			stopReason = "drawdown_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("本轮交易后触发 %.0f%% 回撤或止损权益规则", hcfg.MaxDrawdownRate*100),
			})
			break
		}
	}

	if r.logger != nil && len(stats.SkipReasons) > 0 {
		r.logger.Printf("月度模拟轮次跳过汇总：%s", summarizeMonthlyReasonCounts(stats.SkipReasons, nil))
	}
	if r.logger != nil && len(stats.CandidateReasons) > 0 {
		r.logger.Printf("月度模拟候选过滤汇总：%s", summarizeMonthlyReasonCounts(stats.CandidateReasons, nil))
	}
	stats.CacheSummary = cache.summary()
	if r.logger != nil && stats.CacheSummary != "" {
		r.logger.Printf("月度模拟 K 线缓存摘要：%s", stats.CacheSummary)
	}
	return trades, logs, stats, stopReason, nil
}

func (r *BacktestRunner) selectBestMonthlyCandidate(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, group monthlyEventGroup) (*monthlyCandidate, []monthlyCandidate, string, error) {
	hcfg := r.historicalConfig()
	groupTimeout := adaptiveMonthlyGroupTimeout(hcfg.GroupEvaluationTimeout, len(group.Items))
	groupCtx, cancel := context.WithTimeout(ctx, groupTimeout)
	defer cancel()

	screeningItems, prefiltered := r.preselectMonthlyGroupItems(group)
	evaluated := make([]monthlyCandidate, 0, len(group.Items))
	evaluated = append(evaluated, prefiltered...)
	screened := make([]monthlyCandidate, 0, len(screeningItems))
	groupReason := ""
	logAgg := newMonthlyLogAggregation()
	type screeningResult struct {
		item      FundingRateHistoryItem
		candidate monthlyCandidate
		err       error
	}
	results := make(chan screeningResult, len(screeningItems))
	sem := make(chan struct{}, hcfg.ScreeningConcurrency)
	var wg sync.WaitGroup
	for _, item := range screeningItems {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-groupCtx.Done():
				results <- screeningResult{item: item, err: groupCtx.Err()}
				return
			}
			defer func() { <-sem }()

			candidateBuildTimeout := adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))
			candidateCtx, candidateCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
			candidateFundingTime := group.FundingTime
			if item.FundingTime > 0 {
				candidateFundingTime = time.UnixMilli(item.FundingTime).UTC()
			}
			candidate, err := r.buildMonthlyCandidate(candidateCtx, client, cache, candidateFundingTime, item)
			candidateCancel()
			results <- screeningResult{item: item, candidate: candidate, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			if errors.Is(result.err, context.DeadlineExceeded) || errors.Is(result.err, context.Canceled) {
				candidateFundingTime := group.FundingTime
				if result.item.FundingTime > 0 {
					candidateFundingTime = time.UnixMilli(result.item.FundingTime).UTC()
				}
				evaluated = append(evaluated, monthlyCandidate{
					Symbol:      result.item.Symbol,
					FundingTime: candidateFundingTime,
					FundingRate: parseFloat(result.item.FundingRate),
					Reason:      "candidate_build_timeout",
					Notes: []string{
						"conservativeSkip=true",
						fmt.Sprintf("timeout=%s", adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))),
						result.err.Error(),
					},
				})
				logAgg.add("candidate_build_timeout", result.item.Symbol)
				continue
			}
			if r.logger != nil {
				r.logger.Printf("历史模拟评估失败 fundingTime=%s symbol=%s err=%v", group.FundingTime.Format(time.RFC3339), result.item.Symbol, result.err)
			}
			continue
		}
		if result.candidate.Reason == "candidate_build_timeout" {
			logAgg.add("candidate_build_timeout", result.item.Symbol)
		}
		if strings.TrimSpace(result.candidate.Reason) != "" {
			evaluated = append(evaluated, result.candidate)
			continue
		}
		screened = append(screened, result.candidate)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, "context_canceled", err
	}

	if groupCtx.Err() != nil {
		if errors.Is(groupCtx.Err(), context.Canceled) {
			return nil, nil, "context_canceled", groupCtx.Err()
		}
		groupReason = "group_evaluation_timeout"
	}

	sort.SliceStable(screened, func(i, j int) bool {
		left := monthlyCandidateScreeningPriorityScore(screened[i])
		right := monthlyCandidateScreeningPriorityScore(screened[j])
		if left == right {
			return screened[i].Symbol < screened[j].Symbol
		}
		return left > right
	})

	if len(screened) > hcfg.DeepCandidateLimit {
		for index := hcfg.DeepCandidateLimit; index < len(screened); index++ {
			candidate := screened[index]
			candidate.Reason = "screening_topn_excluded"
			candidate.Notes = append(candidate.Notes,
				fmt.Sprintf("screeningLimit=%d", hcfg.DeepCandidateLimit),
				fmt.Sprintf("screeningRank=%d", index+1),
			)
			evaluated = append(evaluated, candidate)
		}
		screened = screened[:hcfg.DeepCandidateLimit]
	}

	for _, screenedCandidate := range screened {
		if groupCtx.Err() != nil {
			if errors.Is(groupCtx.Err(), context.Canceled) {
				return nil, nil, "context_canceled", groupCtx.Err()
			}
			groupReason = "group_evaluation_timeout"
			if r.logger != nil {
				r.logger.Printf("历史模拟评估超时 fundingTime=%s candidates=%d timeout=%s err=%v",
					group.FundingTime.Format(time.RFC3339), len(group.Items), groupTimeout, groupCtx.Err())
			}
			break
		}
		candidate := screenedCandidate
		candidateBuildTimeout := adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))
		hydrateCtx, hydrateCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
		err := r.hydrateMonthlyCandidateFunding(hydrateCtx, client, cache, &candidate)
		hydrateCancel()
		if err != nil {
			candidate.Reason = monthlyBuildSkipReason("funding_rate_unavailable", err)
			candidate.Notes = append(candidate.Notes, err.Error())
			evaluated = append(evaluated, candidate)
			if candidate.Reason == "candidate_build_timeout" {
				logAgg.add("candidate_funding_timeout", candidate.Symbol)
			}
			continue
		}
		if reason, notes := r.validateMonthlyActualFunding(candidate); reason != "" {
			candidate.Reason = reason
			candidate.Notes = append(candidate.Notes, notes...)
			evaluated = append(evaluated, candidate)
			continue
		}
		pricingCtx, pricingCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
		err = r.completeMonthlyCandidateProxy(pricingCtx, client, cache, &candidate)
		pricingCancel()
		if err != nil {
			candidate.Reason = monthlyBuildSkipReason("reference_kline_unavailable", err)
			candidate.Notes = append(candidate.Notes, err.Error())
			evaluated = append(evaluated, candidate)
			if candidate.Reason == "candidate_build_timeout" {
				logAgg.add("candidate_pricing_timeout", candidate.Symbol)
			}
			continue
		}
		evaluated = append(evaluated, candidate)
	}

	sort.SliceStable(evaluated, func(i, j int) bool {
		if evaluated[i].EstimatedNetPnL == evaluated[j].EstimatedNetPnL {
			return evaluated[i].Symbol < evaluated[j].Symbol
		}
		return evaluated[i].EstimatedNetPnL > evaluated[j].EstimatedNetPnL
	})

	for index := range evaluated {
		if strings.TrimSpace(evaluated[index].Reason) != "" {
			continue
		}
		candidate := evaluated[index]
		finalizeTimeout := adaptiveMonthlyCandidateFinalizeTimeout(hcfg.CandidateFinalizeTimeout, len(group.Items))
		finalizeCtx, finalizeCancel := context.WithTimeout(groupCtx, finalizeTimeout)
		err := r.finalizeMonthlyCandidateExecution(finalizeCtx, client, cache, &candidate)
		finalizeCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				evaluated[index].Reason = "candidate_finalize_timeout"
				evaluated[index].Notes = append(evaluated[index].Notes,
					fmt.Sprintf("timeout=%s", finalizeTimeout),
					err.Error(),
				)
				logAgg.add("candidate_finalize_timeout", candidate.Symbol)
				continue
			}
			evaluated[index].Reason = "execution_kline_unavailable"
			evaluated[index].Notes = append(evaluated[index].Notes, err.Error())
			continue
		}
		evaluated[index] = candidate
		if strings.TrimSpace(evaluated[index].Reason) != "" {
			continue
		}
		if r.logger != nil {
			r.logMonthlyGroupAggregation(group, len(screeningItems), logAgg, groupReason)
		}
		return &evaluated[index], evaluated, groupReason, nil
	}
	if r.logger != nil {
		r.logMonthlyGroupAggregation(group, len(screeningItems), logAgg, groupReason)
	}
	return nil, evaluated, groupReason, nil
}

func (r *BacktestRunner) preselectMonthlyGroupItems(group monthlyEventGroup) ([]FundingRateHistoryItem, []monthlyCandidate) {
	limit := r.monthlyScreeningCandidateLimit(len(group.Items))
	if limit >= len(group.Items) {
		return group.Items, nil
	}
	selected := make([]FundingRateHistoryItem, 0, limit)
	excluded := make([]monthlyCandidate, 0, len(group.Items)-limit)
	for index, item := range group.Items {
		if index < limit {
			selected = append(selected, item)
			continue
		}
		candidateFundingTime := group.FundingTime
		if item.FundingTime > 0 {
			candidateFundingTime = time.UnixMilli(item.FundingTime).UTC()
		}
		excluded = append(excluded, monthlyCandidate{
			Symbol:         item.Symbol,
			FundingTime:    candidateFundingTime,
			FundingRate:    parseFloat(item.FundingRate),
			ScreeningScore: parseFloat(item.ScreeningScore),
			Reason:         "screening_prefilter_excluded",
			Notes: []string{
				fmt.Sprintf("prefilterLimit=%d", limit),
				fmt.Sprintf("prefilterRank=%d", index+1),
				fmt.Sprintf("screeningScore=%s", item.ScreeningScore),
				"screeningStage=entry_friction_liquidity_proxy",
			},
		})
	}
	return selected, excluded
}

func (r *BacktestRunner) buildMonthlyCandidate(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, fundingTime time.Time, item FundingRateHistoryItem) (monthlyCandidate, error) {
	hcfg := r.historicalConfig()
	entryTime := fundingTime.Add(-hcfg.FundingPhaseLead)
	requestedHoldingCycles := r.monthlyHoldingCycles()
	fundingSchedule, err := cache.getFundingSchedule(ctx, client, item.Symbol, fundingTime, requestedHoldingCycles)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, "funding_schedule_incomplete", err, hcfg.CandidateBuildTimeout), nil
	}
	holdingCycles := len(fundingSchedule)
	exitTime := time.UnixMilli(fundingSchedule[len(fundingSchedule)-1].FundingTime).UTC()
	premiumEntryRef, err := cache.getFuturesPremiumKlineBefore(ctx, client, item.Symbol, entryTime)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, monthlyBuildSkipReason("premium_index_kline_unavailable", err), err, hcfg.CandidateBuildTimeout), nil
	}
	indexEntryRef, err := cache.getFuturesIndexKlineBefore(ctx, client, item.Symbol, entryTime)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, monthlyBuildSkipReason("index_price_kline_unavailable", err), err, hcfg.CandidateBuildTimeout), nil
	}

	actualFundingLoaded := strings.TrimSpace(item.FundingRate) != ""
	fundingRate := parseFloat(item.FundingRate)
	markPrice := parseFloat(item.MarkPrice)
	preFundingBasisRate := 0.0
	if indexEntryRef.Close > 0 && markPrice > 0 {
		preFundingBasisRate = (markPrice - indexEntryRef.Close) / indexEntryRef.Close
	}
	if markPrice <= 0 && indexEntryRef.Close > 0 {
		markPrice = indexEntryRef.Close * (1 + premiumEntryRef.Close)
	}
	effectiveMarkPrice := markPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = indexEntryRef.Close
	}
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = 1
	}
	roughHedgeQty := r.monthlyPerLegNotional() / effectiveMarkPrice
	estimatedFundingRate := parseFloat(item.EstimatedFundingRate)
	if estimatedFundingRate == 0 {
		estimatedFundingRate = conservativeHistoricalFundingRate(premiumEntryRef.Close, preFundingBasisRate)
	}
	direction := fundingDirectionFromRate(estimatedFundingRate)
	estimatedFundingIncome := historicalFundingIncome(direction, roughHedgeQty, effectiveMarkPrice, estimatedFundingRate, holdingCycles)
	fundingIncome := 0.0
	fundingRateJump := 0.0
	if actualFundingLoaded {
		fundingIncome = historicalFundingIncome(direction, roughHedgeQty, effectiveMarkPrice, fundingRate, holdingCycles)
		fundingRateJump = math.Abs(fundingRate - estimatedFundingRate)
	}

	baseNotes := []string{
		"fundingDirectionMode=" + normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode),
		"direction=" + string(direction),
		fmt.Sprintf("requestedHoldingCycles=%d", requestedHoldingCycles),
		fmt.Sprintf("fundingScheduleLen=%d", holdingCycles),
		fmt.Sprintf("exitTime=%s", exitTime.UTC().Format(time.RFC3339)),
		fmt.Sprintf("premiumIndexClose=%.6f", premiumEntryRef.Close),
		fmt.Sprintf("markIndexBasis=%.6f", preFundingBasisRate),
		fmt.Sprintf("screeningScore=%s", item.ScreeningScore),
		fmt.Sprintf("actualFundingLoaded=%t", actualFundingLoaded),
		"screeningStage=entry_friction_liquidity_proxy",
		"estimatedFundingModel=conservative_premium_basis_haircut",
	}
	if holdingCycles < requestedHoldingCycles {
		baseNotes = append(baseNotes,
			"holdingCyclesShortened=true",
			fmt.Sprintf("fundingScheduleShortfall=%d", requestedHoldingCycles-holdingCycles),
		)
	}
	if holdingCycles > 1 {
		baseNotes = append(baseNotes, fmt.Sprintf("fundingInterval=%s", time.UnixMilli(fundingSchedule[1].FundingTime).UTC().Sub(time.UnixMilli(fundingSchedule[0].FundingTime).UTC())))
	}
	if actualFundingLoaded {
		baseNotes = append(baseNotes, fmt.Sprintf("fundingRateJump=%.6f", fundingRateJump))
	}

	if direction == "" {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			ScreeningScore:         parseFloat(item.ScreeningScore),
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "estimated_funding_non_positive",
			Notes:                  append([]string(nil), baseNotes...),
		}, nil
	}
	if !historicalFundingDirectionEnabled(hcfg.FundingDirectionMode, direction) {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "estimated_funding_direction_disabled",
			Notes:                  append([]string(nil), baseNotes...),
		}, nil
	}
	if math.Abs(estimatedFundingRate) >= hcfg.MaxFundingRate || (actualFundingLoaded && math.Abs(fundingRate) >= hcfg.MaxFundingRate) {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "funding_rate_extreme",
			Notes: append(baseNotes,
				fmt.Sprintf("maxFundingRate=%.6f", hcfg.MaxFundingRate),
			),
		}, nil
	}
	if actualFundingLoaded && fundingRateJump >= hcfg.MaxFundingRateJump {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "funding_jump_too_large",
			Notes: append(baseNotes,
				fmt.Sprintf("maxFundingRateJump=%.6f", hcfg.MaxFundingRateJump),
			),
		}, nil
	}
	return monthlyCandidate{
		Symbol:                 item.Symbol,
		Direction:              direction,
		FundingTime:            fundingTime,
		ExitTime:               exitTime,
		HoldingCycles:          holdingCycles,
		EstimatedFundingRate:   estimatedFundingRate,
		ScreeningScore:         parseFloat(item.ScreeningScore),
		FundingRate:            fundingRate,
		EstimatedFundingIncome: estimatedFundingIncome,
		FundingIncome:          fundingIncome,
		Notes:                  append([]string(nil), baseNotes...),
	}, nil
}

func (r *BacktestRunner) hydrateMonthlyCandidateFunding(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	item, err := cache.getFundingRateAt(ctx, client, candidate.Symbol, candidate.FundingTime)
	if err != nil {
		return err
	}
	candidate.FundingRate = parseFloat(item.FundingRate)
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("actualFundingRate=%.6f", candidate.FundingRate),
		fmt.Sprintf("actualFundingTime=%s", time.UnixMilli(item.FundingTime).UTC().Format(time.RFC3339)),
	)
	return nil
}

func (r *BacktestRunner) validateMonthlyActualFunding(candidate monthlyCandidate) (string, []string) {
	hcfg := r.historicalConfig()
	actualDirection := fundingDirectionFromRate(candidate.FundingRate)
	if candidate.Direction != "" && actualDirection != "" && actualDirection != candidate.Direction {
		return "funding_direction_reversed", []string{
			"estimatedDirection=" + string(candidate.Direction),
			"actualDirection=" + string(actualDirection),
		}
	}
	if math.Abs(candidate.FundingRate) >= hcfg.MaxFundingRate {
		return "funding_rate_extreme", []string{fmt.Sprintf("maxFundingRate=%.6f", hcfg.MaxFundingRate)}
	}
	fundingRateJump := math.Abs(candidate.FundingRate - candidate.EstimatedFundingRate)
	if fundingRateJump >= hcfg.MaxFundingRateJump {
		return "funding_jump_too_large", []string{
			fmt.Sprintf("fundingRateJump=%.6f", fundingRateJump),
			fmt.Sprintf("maxFundingRateJump=%.6f", hcfg.MaxFundingRateJump),
		}
	}
	return "", nil
}

type historicalSpotQuoteContext struct {
	SpotPair   string
	QuoteAsset string
}

type historicalSpotQuoteConversion struct {
	SpotPair      string
	QuoteAsset    string
	FXSymbol      string
	RateToUSDT    float64
	DeviationRate float64
}

func resolveHistoricalSpotQuoteContext(client *BinanceClient, futureSymbol string) historicalSpotQuoteContext {
	normalizedFuture := strings.ToUpper(strings.TrimSpace(futureSymbol))
	ctx := historicalSpotQuoteContext{
		SpotPair:   normalizedFuture,
		QuoteAsset: "USDT",
	}
	if client == nil {
		return ctx
	}
	if pair := strings.TrimSpace(client.historicalSpotPairForFuture(normalizedFuture)); pair != "" {
		ctx.SpotPair = strings.ToUpper(pair)
	}
	if meta, ok := client.historicalSpotMetaForFuture(normalizedFuture); ok {
		if strings.TrimSpace(meta.Symbol) != "" {
			ctx.SpotPair = strings.ToUpper(strings.TrimSpace(meta.Symbol))
		}
		if strings.TrimSpace(meta.QuoteAsset) != "" {
			ctx.QuoteAsset = strings.ToUpper(strings.TrimSpace(meta.QuoteAsset))
		}
	}
	return ctx
}

func historicalSpotKlineAt(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, at time.Time, before bool) (MarketKline, error) {
	if before {
		return cache.getSpotKlineBefore(ctx, client, symbol, at)
	}
	return cache.getSpotKline(ctx, client, symbol, at)
}

func historicalKlineReferencePrice(kline MarketKline, before bool) float64 {
	if before {
		return kline.Close
	}
	return kline.Open
}

func historicalSpotQuoteConversionAt(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, futureSymbol string, at time.Time, before bool) (historicalSpotQuoteConversion, error) {
	quoteCtx := resolveHistoricalSpotQuoteContext(client, futureSymbol)
	conversion := historicalSpotQuoteConversion{
		SpotPair:   quoteCtx.SpotPair,
		QuoteAsset: quoteCtx.QuoteAsset,
		FXSymbol:   "USDT",
		RateToUSDT: 1,
	}
	if quoteCtx.QuoteAsset == "" || quoteCtx.QuoteAsset == "USDT" {
		return conversion, nil
	}
	directSymbol := quoteCtx.QuoteAsset + "USDT"
	if directKline, err := historicalSpotKlineAt(ctx, client, cache, directSymbol, at, before); err == nil {
		rate := historicalKlineReferencePrice(directKline, before)
		if rate <= 0 {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 无效: pair=%s rate=%.8f", directSymbol, rate)
		}
		conversion.FXSymbol = directSymbol
		conversion.RateToUSDT = rate
		conversion.DeviationRate = math.Abs(rate - 1)
		return conversion, nil
	} else {
		inverseSymbol := "USDT" + quoteCtx.QuoteAsset
		inverseKline, inverseErr := historicalSpotKlineAt(ctx, client, cache, inverseSymbol, at, before)
		if inverseErr != nil {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 不可用: future=%s spotPair=%s quote=%s directErr=%v inverseErr=%v", futureSymbol, quoteCtx.SpotPair, quoteCtx.QuoteAsset, err, inverseErr)
		}
		rawRate := historicalKlineReferencePrice(inverseKline, before)
		if rawRate <= 0 {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 无效: pair=%s rate=%.8f", inverseSymbol, rawRate)
		}
		conversion.FXSymbol = inverseSymbol
		conversion.RateToUSDT = 1 / rawRate
		conversion.DeviationRate = math.Abs(conversion.RateToUSDT - 1)
		return conversion, nil
	}
}

func convertHistoricalSpotMetricsToUSDT(price float64, quoteVolume float64, conversion historicalSpotQuoteConversion) (float64, float64) {
	rate := conversion.RateToUSDT
	if rate <= 0 {
		rate = 1
	}
	return price * rate, quoteVolume * rate
}

func (r *BacktestRunner) estimateMonthlyPreselectScore(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, fundingTime time.Time, entryTime time.Time, estimatedFundingRate float64, estimatedMarkPrice float64) float64 {
	direction := fundingDirectionFromRate(estimatedFundingRate)
	if direction == "" {
		return 0
	}
	hcfg := r.historicalConfig()
	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, symbol)
	spotEntryRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	futuresEntryRef, err := cache.getFuturesKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	spotConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, symbol, entryTime, true)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	spotEntryPrice, spotEntryQuoteVolume := convertHistoricalSpotMetricsToUSDT(
		historicalKlineReferencePrice(spotEntryRef, true),
		spotEntryRef.QuoteAssetVolume,
		spotConversion,
	)
	futuresEntryPrice := historicalKlineReferencePrice(futuresEntryRef, true)
	if spotEntryPrice <= 0 || futuresEntryPrice <= 0 {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	hedgeQty := math.Min(r.monthlyPerLegNotional()/spotEntryPrice, r.monthlyPerLegNotional()/futuresEntryPrice)
	if hedgeQty <= 0 {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	effectiveMarkPrice := estimatedMarkPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = futuresEntryPrice
	}
	spotEntryNotional := hedgeQty * spotEntryPrice
	futuresEntryNotional := hedgeQty * futuresEntryPrice
	fundingIncome := historicalFundingIncome(direction, hedgeQty, effectiveMarkPrice, estimatedFundingRate, maxInt(1, r.monthlyHoldingCycles()))
	borrowInterestHours := historicalBorrowInterestHours(direction, entryTime, fundingTime, hcfg.BorrowInterestMinBillingHours)
	borrowInterestCost := historicalBorrowInterestCost(direction, spotEntryNotional, r.hourlyBorrowInterestRateForSymbol(client, symbol), borrowInterestHours)
	spotSlip := estimateHistoricalSlipRate(spotEntryRef)
	futuresSlip := estimateHistoricalSlipRate(futuresEntryRef)
	spotImpactMultiplier := historicalSpotImpactMultiplier(hcfg, direction, true)
	spotImpact := estimateMarketImpactRate(spotEntryNotional*spotImpactMultiplier, spotEntryQuoteVolume)
	futuresImpact := estimateMarketImpactRate(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	entryCost := spotEntryNotional*(spotSlip+spotImpact+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(futuresSlip+futuresImpact+hcfg.FuturesTakerFeeRate)
	openBasisPenalty := computeAdverseBasisPenalty(computeBasisRate(spotEntryPrice, futuresEntryPrice), spotEntryNotional, futuresEntryNotional)
	spotLiquidityShare := ratioOrZero(spotEntryNotional, spotEntryQuoteVolume) * spotImpactMultiplier
	futuresLiquidityShare := ratioOrZero(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	liquidityPenalty := 0.0
	liquidityShareLimit := r.monthlyLiquidityShareLimit()
	severeLiquidityShareLimit := r.monthlySevereLiquidityShareLimit(liquidityShareLimit)
	if spotLiquidityShare > severeLiquidityShareLimit || futuresLiquidityShare > severeLiquidityShareLimit {
		liquidityPenalty += r.monthlyPerLegNotional()
	} else if spotLiquidityShare > liquidityShareLimit || futuresLiquidityShare > liquidityShareLimit {
		liquidityPenalty += r.monthlyPerLegNotional() * 0.25
	}
	return fundingIncome - entryCost - borrowInterestCost - openBasisPenalty - liquidityPenalty
}

func (r *BacktestRunner) completeMonthlyCandidateProxy(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	hcfg := r.historicalConfig()
	entryTime := candidate.FundingTime.Add(-hcfg.FundingPhaseLead)
	exitTime := candidate.ExitTime
	if exitTime.IsZero() {
		exitTime = r.monthlyExitTime(candidate.FundingTime)
		candidate.ExitTime = exitTime
	}
	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, candidate.Symbol)
	spotEntryRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return err
	}
	futuresEntryRef, err := cache.getFuturesKlineBefore(ctx, client, candidate.Symbol, entryTime)
	if err != nil {
		return err
	}
	spotExitRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, exitTime)
	if err != nil {
		return err
	}
	futuresExitRef, err := cache.getFuturesKlineBefore(ctx, client, candidate.Symbol, exitTime)
	if err != nil {
		return err
	}
	entryConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, entryTime, true)
	if err != nil {
		candidate.Reason = "cross_quote_fx_unavailable"
		candidate.Notes = append(candidate.Notes, err.Error(), "pricingModel=entry_proxy_screening")
		return nil
	}
	exitConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, exitTime, true)
	if err != nil {
		candidate.Reason = "cross_quote_fx_unavailable"
		candidate.Notes = append(candidate.Notes, err.Error(), "pricingModel=entry_proxy_screening")
		return nil
	}
	if entryConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate || exitConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate {
		candidate.Reason = "cross_quote_deviation_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
			fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
			fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
			fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
			fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
			fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
			fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
			fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
			fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.6f", hcfg.CrossStableQuoteMaxDeviationRate),
			"pricingModel=entry_proxy_screening",
		)
		return nil
	}

	spotEntryPrice, spotEntryQuoteVolume := convertHistoricalSpotMetricsToUSDT(spotEntryRef.Close, spotEntryRef.QuoteAssetVolume, entryConversion)
	futuresEntryPrice := futuresEntryRef.Close
	if spotEntryPrice <= 0 || futuresEntryPrice <= 0 {
		candidate.Reason = "invalid_market_data"
		candidate.Notes = append(candidate.Notes, "存在非正价格", "pricingModel=entry_proxy_screening")
		return nil
	}
	spotExitPrice, spotExitQuoteVolume := convertHistoricalSpotMetricsToUSDT(spotExitRef.Close, spotExitRef.QuoteAssetVolume, exitConversion)

	perLegNotional := r.monthlyPerLegNotional()
	hedgeQty := math.Min(perLegNotional/spotEntryPrice, perLegNotional/futuresEntryPrice)
	if hedgeQty <= 0 {
		candidate.Reason = "invalid_quantity"
		candidate.Notes = append(candidate.Notes, "对冲数量无法计算")
		return nil
	}

	spotEntryNotional := hedgeQty * spotEntryPrice
	futuresEntryNotional := hedgeQty * futuresEntryPrice
	spotExitNotional := hedgeQty * spotExitRef.Close
	futuresExitNotional := hedgeQty * futuresExitRef.Close
	effectiveMarkPrice := futuresEntryPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = 1
	}
	if candidate.Direction == "" {
		candidate.Direction = fundingDirectionFromRate(candidate.EstimatedFundingRate)
	}
	spotEntryImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, true)
	spotExitImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, false)
	borrowInterestHours := historicalBorrowInterestHours(candidate.Direction, entryTime, exitTime, hcfg.BorrowInterestMinBillingHours)
	candidate.HedgeQty = hedgeQty
	candidate.SpotEntryPrice = spotEntryPrice
	candidate.FuturesEntryPrice = futuresEntryPrice
	candidate.SpotExitPrice = spotExitPrice
	candidate.FuturesExitPrice = futuresExitRef.Close
	candidate.SpotEntryQuoteVolume = spotEntryQuoteVolume
	candidate.FuturesEntryQuoteVolume = futuresEntryRef.QuoteAssetVolume
	candidate.SpotExitQuoteVolume = spotExitQuoteVolume
	candidate.FuturesExitQuoteVolume = futuresExitRef.QuoteAssetVolume
	candidate.EstimatedFundingIncome = historicalFundingIncome(candidate.Direction, hedgeQty, effectiveMarkPrice, candidate.EstimatedFundingRate, maxInt(candidate.HoldingCycles, 1))
	candidate.FundingIncome = historicalFundingIncome(candidate.Direction, hedgeQty, effectiveMarkPrice, candidate.FundingRate, maxInt(candidate.HoldingCycles, 1))
	candidate.OpenBasisRate = computeBasisRate(spotEntryPrice, futuresEntryPrice)
	candidate.CloseBasisRate = computeBasisRate(candidate.SpotExitPrice, candidate.FuturesExitPrice)
	candidate.SpotEntrySlipRate = estimateHistoricalSlipRate(spotEntryRef)
	candidate.FuturesEntrySlipRate = estimateHistoricalSlipRate(futuresEntryRef)
	candidate.SpotExitSlipRate = estimateHistoricalSlipRate(spotExitRef)
	candidate.FuturesExitSlipRate = estimateHistoricalSlipRate(futuresExitRef)
	candidate.SpotEntryImpactRate = estimateMarketImpactRate(spotEntryNotional*spotEntryImpactMultiplier, spotEntryQuoteVolume)
	candidate.FuturesEntryImpactRate = estimateMarketImpactRate(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	candidate.SpotExitImpactRate = estimateMarketImpactRate(spotExitNotional*spotExitImpactMultiplier, spotExitQuoteVolume)
	candidate.FuturesExitImpactRate = estimateMarketImpactRate(futuresExitNotional, futuresExitRef.QuoteAssetVolume)
	borrowInterestBaseNotional := hedgeQty * effectiveMarkPrice
	if borrowInterestBaseNotional <= 0 {
		borrowInterestBaseNotional = spotEntryNotional
	}
	borrowInterestCost := historicalBorrowInterestCost(candidate.Direction, borrowInterestBaseNotional, r.hourlyBorrowInterestRateForSymbol(client, candidate.Symbol), borrowInterestHours)
	candidate.SpotEntryFee = spotEntryNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesEntryFee = futuresEntryNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotExitFee = spotExitNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesExitFee = futuresExitNotional * hcfg.FuturesTakerFeeRate
	candidate.TotalFees = candidate.SpotEntryFee + candidate.FuturesEntryFee + candidate.SpotExitFee + candidate.FuturesExitFee + borrowInterestCost
	candidate.TotalFriction = spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate) +
		spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate)
	candidate.ProjectedExitCost = spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate+hcfg.SpotTakerFeeRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.OpenBasisPenalty = computeAdverseBasisPenalty(candidate.OpenBasisRate, spotEntryNotional, futuresEntryNotional)
	entryExecutionCost := spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.EstimatedNetPnL = candidate.EstimatedFundingIncome - candidate.OpenBasisPenalty - candidate.ProjectedExitCost - entryExecutionCost - borrowInterestCost
	candidate.SpotGrossPnL = 0
	candidate.FuturesGrossPnL = 0
	candidate.RealizedNetPnL = candidate.FundingIncome - candidate.TotalFriction - candidate.TotalFees
	liquidityShareLimit := r.monthlyLiquidityShareLimit()
	severeLiquidityShareLimit := r.monthlySevereLiquidityShareLimit(liquidityShareLimit)
	candidate.MinLiquidityRequired = math.Max(spotEntryNotional*spotEntryImpactMultiplier, futuresEntryNotional) / liquidityShareLimit
	rawSpotLiquidityShare := ratioOrZero(spotEntryNotional, spotEntryQuoteVolume)
	spotLiquidityShare := rawSpotLiquidityShare * spotEntryImpactMultiplier
	futuresLiquidityShare := ratioOrZero(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	liquidityRisk := EvaluateLiquidityShareRisk(math.Max(spotLiquidityShare, futuresLiquidityShare), liquidityShareLimit, severeLiquidityShareLimit)
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
		fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
		fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
		fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
		fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
		fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
		fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
		fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
		"direction="+string(candidate.Direction),
		fmt.Sprintf("estimatedFundingRate=%.6f", candidate.EstimatedFundingRate),
		fmt.Sprintf("actualFundingRate=%.6f", candidate.FundingRate),
		fmt.Sprintf("estimatedFundingIncome=%.6f", candidate.EstimatedFundingIncome),
		fmt.Sprintf("actualFundingIncome=%.6f", candidate.FundingIncome),
		fmt.Sprintf("holdingCycles=%d", maxInt(candidate.HoldingCycles, 1)),
		fmt.Sprintf("exitTime=%s", candidate.ExitTime.UTC().Format(time.RFC3339)),
		fmt.Sprintf("spotEntrySlip=%.6f", candidate.SpotEntrySlipRate),
		fmt.Sprintf("futuresEntrySlip=%.6f", candidate.FuturesEntrySlipRate),
		fmt.Sprintf("spotExitSlip=%.6f", candidate.SpotExitSlipRate),
		fmt.Sprintf("futuresExitSlip=%.6f", candidate.FuturesExitSlipRate),
		fmt.Sprintf("spotEntryImpact=%.6f", candidate.SpotEntryImpactRate),
		fmt.Sprintf("futuresEntryImpact=%.6f", candidate.FuturesEntryImpactRate),
		fmt.Sprintf("spotExitImpact=%.6f", candidate.SpotExitImpactRate),
		fmt.Sprintf("futuresExitImpact=%.6f", candidate.FuturesExitImpactRate),
		fmt.Sprintf("spotEntryImpactMultiplier=%.2f", spotEntryImpactMultiplier),
		fmt.Sprintf("spotExitImpactMultiplier=%.2f", spotExitImpactMultiplier),
		fmt.Sprintf("borrowInterestHours=%d", borrowInterestHours),
		fmt.Sprintf("borrowInterestCost=%.6f", borrowInterestCost),
		fmt.Sprintf("openBasisPenalty=%.6f", candidate.OpenBasisPenalty),
		fmt.Sprintf("projectedExitCost=%.6f", candidate.ProjectedExitCost),
		fmt.Sprintf("rawSpotLiquidityShare=%.6f", rawSpotLiquidityShare),
		fmt.Sprintf("spotLiquidityShare=%.6f", spotLiquidityShare),
		fmt.Sprintf("futuresLiquidityShare=%.6f", futuresLiquidityShare),
		fmt.Sprintf("liquidityShareLimit=%.6f", liquidityShareLimit),
		fmt.Sprintf("severeLiquidityShareLimit=%.6f", severeLiquidityShareLimit),
		fmt.Sprintf("liquidityRiskCategory=%s", liquidityRisk.Category),
		fmt.Sprintf("liquidityRiskSeverity=%s", liquidityRisk.Severity),
		fmt.Sprintf("liquidityRiskAction=%s", liquidityRisk.Action),
		"pricingModel=entry_proxy_screening",
		"exitCostModel=historical_exit_proxy",
	)

	if spotLiquidityShare > severeLiquidityShareLimit || futuresLiquidityShare > severeLiquidityShareLimit {
		candidate.Reason = "entry_liquidity_extremely_poor"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotEntryQuoteVol=%.6f", spotEntryQuoteVolume),
			fmt.Sprintf("futuresEntryQuoteVol=%.6f", futuresEntryRef.QuoteAssetVolume),
		)
		return nil
	}
	if spotLiquidityShare > liquidityShareLimit || futuresLiquidityShare > liquidityShareLimit {
		candidate.Reason = "entry_liquidity_share_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotEntryQuoteVol=%.6f", spotEntryQuoteVolume),
			fmt.Sprintf("futuresEntryQuoteVol=%.6f", futuresEntryRef.QuoteAssetVolume),
		)
		return nil
	}
	safetyBufferUSDT := r.monthlySafetyBuffer(entryExecutionCost, candidate.ProjectedExitCost, candidate.OpenBasisPenalty)
	if candidate.EstimatedNetPnL <= safetyBufferUSDT {
		candidate.Reason = "estimated_net_below_safety_buffer"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("safetyBufferUSDT=%.6f", safetyBufferUSDT),
			fmt.Sprintf("entryExecutionCost=%.6f", entryExecutionCost),
			fmt.Sprintf("safetyBufferGap=%.6f", safetyBufferUSDT-candidate.EstimatedNetPnL),
			fmt.Sprintf("estimatedNetPnL=%.6f", candidate.EstimatedNetPnL),
		)
		return nil
	}
	return nil
}

func (r *BacktestRunner) finalizeMonthlyCandidateExecution(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	hcfg := r.historicalConfig()
	entryTime := candidate.FundingTime.Add(-hcfg.FundingPhaseLead)
	exitTime := candidate.ExitTime
	if exitTime.IsZero() {
		exitTime = r.monthlyExitTime(candidate.FundingTime)
		candidate.ExitTime = exitTime
	}

	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, candidate.Symbol)
	spotEntryExec, err := cache.getSpotKline(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return fmt.Errorf("entry_spot_execution: %w", err)
	}
	futuresEntryExec, err := cache.getFuturesKline(ctx, client, candidate.Symbol, entryTime)
	if err != nil {
		return fmt.Errorf("entry_futures_execution: %w", err)
	}
	spotExitExec, err := cache.getSpotKline(ctx, client, spotQuoteCtx.SpotPair, exitTime)
	if err != nil {
		return fmt.Errorf("exit_spot_execution: %w", err)
	}
	futuresExitExec, err := cache.getFuturesKline(ctx, client, candidate.Symbol, exitTime)
	if err != nil {
		return fmt.Errorf("exit_futures_execution: %w", err)
	}
	entryConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, entryTime, false)
	if err != nil {
		return fmt.Errorf("entry_spot_fx_execution: %w", err)
	}
	exitConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, exitTime, false)
	if err != nil {
		return fmt.Errorf("exit_spot_fx_execution: %w", err)
	}
	if entryConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate || exitConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate {
		candidate.Reason = "cross_quote_deviation_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
			fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
			fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
			fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
			fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
			fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
			fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
			fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
			fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.6f", hcfg.CrossStableQuoteMaxDeviationRate),
			"executionModel=exact_entry_exit_open",
		)
		return nil
	}

	candidate.SpotEntryPrice, candidate.SpotEntryQuoteVolume = convertHistoricalSpotMetricsToUSDT(spotEntryExec.Open, spotEntryExec.QuoteAssetVolume, entryConversion)
	candidate.FuturesEntryPrice = futuresEntryExec.Open
	candidate.SpotExitPrice, candidate.SpotExitQuoteVolume = convertHistoricalSpotMetricsToUSDT(spotExitExec.Open, spotExitExec.QuoteAssetVolume, exitConversion)
	candidate.FuturesExitPrice = futuresExitExec.Open
	candidate.FuturesEntryQuoteVolume = futuresEntryExec.QuoteAssetVolume
	candidate.FuturesExitQuoteVolume = futuresExitExec.QuoteAssetVolume
	if candidate.Direction == "" {
		candidate.Direction = fundingDirectionFromRate(candidate.EstimatedFundingRate)
	}
	spotEntryImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, true)
	spotExitImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, false)
	borrowInterestHours := historicalBorrowInterestHours(candidate.Direction, entryTime, exitTime, hcfg.BorrowInterestMinBillingHours)

	spotEntryNotional := candidate.HedgeQty * candidate.SpotEntryPrice
	futuresEntryNotional := candidate.HedgeQty * candidate.FuturesEntryPrice
	spotExitNotional := candidate.HedgeQty * candidate.SpotExitPrice
	futuresExitNotional := candidate.HedgeQty * candidate.FuturesExitPrice

	candidate.OpenBasisRate = computeBasisRate(candidate.SpotEntryPrice, candidate.FuturesEntryPrice)
	candidate.CloseBasisRate = computeBasisRate(candidate.SpotExitPrice, candidate.FuturesExitPrice)
	candidate.SpotGrossPnL = candidate.Direction.spotGrossPnL(candidate.HedgeQty, candidate.SpotEntryPrice, candidate.SpotExitPrice)
	candidate.FuturesGrossPnL = candidate.Direction.futuresGrossPnL(candidate.HedgeQty, candidate.FuturesEntryPrice, candidate.FuturesExitPrice)
	borrowInterestCost := historicalBorrowInterestCost(candidate.Direction, spotEntryNotional, r.hourlyBorrowInterestRateForSymbol(client, candidate.Symbol), borrowInterestHours)
	candidate.TotalFees = spotEntryNotional*hcfg.SpotTakerFeeRate +
		futuresEntryNotional*hcfg.FuturesTakerFeeRate +
		spotExitNotional*hcfg.SpotTakerFeeRate +
		futuresExitNotional*hcfg.FuturesTakerFeeRate +
		borrowInterestCost
	candidate.SpotEntryFee = spotEntryNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesEntryFee = futuresEntryNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotExitFee = spotExitNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesExitFee = futuresExitNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotEntryImpactRate = estimateMarketImpactRate(spotEntryNotional*spotEntryImpactMultiplier, candidate.SpotEntryQuoteVolume)
	candidate.SpotExitImpactRate = estimateMarketImpactRate(spotExitNotional*spotExitImpactMultiplier, candidate.SpotExitQuoteVolume)
	candidate.TotalFriction = spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate) +
		spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate)
	candidate.ProjectedExitCost = spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate+hcfg.SpotTakerFeeRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.OpenBasisPenalty = computeAdverseBasisPenalty(candidate.OpenBasisRate, spotEntryNotional, futuresEntryNotional)
	entryExecutionCost := spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.EstimatedNetPnL = candidate.EstimatedFundingIncome - candidate.OpenBasisPenalty - candidate.ProjectedExitCost - entryExecutionCost - borrowInterestCost
	actualFundingIncome, err := r.sumMonthlyFundingIncome(ctx, client, cache, candidate.Symbol, candidate.Direction, candidate.HedgeQty, candidate.FuturesEntryPrice, candidate.FundingTime)
	if err != nil {
		return fmt.Errorf("funding_income_accumulate: %w", err)
	}
	candidate.FundingIncome = actualFundingIncome
	candidate.RealizedNetPnL = candidate.FundingIncome + candidate.SpotGrossPnL + candidate.FuturesGrossPnL - candidate.TotalFriction - candidate.TotalFees
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
		fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
		fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
		fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
		fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
		fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
		fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
		fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
		"direction="+string(candidate.Direction),
		"executionModel=exact_entry_exit_open",
		fmt.Sprintf("spotEntryImpactMultiplier=%.2f", spotEntryImpactMultiplier),
		fmt.Sprintf("spotExitImpactMultiplier=%.2f", spotExitImpactMultiplier),
		fmt.Sprintf("borrowInterestHours=%d", borrowInterestHours),
		fmt.Sprintf("borrowInterestCost=%.6f", borrowInterestCost),
		fmt.Sprintf("holdingCycles=%d", maxInt(candidate.HoldingCycles, 1)),
		fmt.Sprintf("exitTime=%s", candidate.ExitTime.UTC().Format(time.RFC3339)),
	)
	return nil
}

func monthlyBuildSkipReason(baseReason string, err error) string {
	if err == nil {
		return baseReason
	}
	if errors.Is(err, context.Canceled) {
		return "candidate_build_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "candidate_build_timeout"
	}
	return baseReason
}

func monthlySkippedCandidate(item FundingRateHistoryItem, fundingTime time.Time, reason string, err error, candidateBuildTimeout time.Duration) monthlyCandidate {
	notes := []string{"conservativeSkip=true"}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			notes = append(notes, "canceled=true")
		} else if errors.Is(err, context.DeadlineExceeded) {
			notes = append(notes, fmt.Sprintf("timeout=%s", candidateBuildTimeout))
		}
		notes = append(notes, err.Error())
	}
	return monthlyCandidate{
		Symbol:          item.Symbol,
		FundingTime:     fundingTime,
		ExitTime:        fundingTime,
		HoldingCycles:   1,
		ScreeningScore:  parseFloat(item.ScreeningScore),
		FundingRate:     parseFloat(item.FundingRate),
		FundingIncome:   0,
		EstimatedNetPnL: 0,
		RealizedNetPnL:  0,
		Reason:          reason,
		Notes:           notes,
	}
}

func (r *BacktestRunner) buildMonthlySummaryRecord(periodLabel string, trades []BacktestTradeSample, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, stopReason string) BacktestCSVRecord {
	totalFunding := 0.0
	totalTrading := 0.0
	totalFees := 0.0
	wins := 0
	maxDrawdown := 0.0
	for _, trade := range trades {
		totalFunding += trade.FundingIncome
		totalTrading += trade.TradingPnL
		totalFees += trade.TotalFees
		if trade.Win {
			wins++
		}
		if trade.Drawdown < maxDrawdown {
			maxDrawdown = trade.Drawdown
		}
	}

	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}
	notes := []string{
		"stop=" + stopReason,
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("skipped=%d", simStats.SkippedGroups),
		fmt.Sprintf("fetchSuccessSymbols=%d", fetchStats.SuccessSymbols),
		fmt.Sprintf("fetchFailures=%d", fetchStats.FailureCount),
		fmt.Sprintf("fetchSuccessRate=%.4f", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount)),
		fmt.Sprintf("dataComplete=%t", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount) >= monthlyFundingDataCompleteFloor),
	}
	notes = append(notes, r.monthlyExperimentNotes()...)
	if skipSummary := summarizeMonthlyReasonCounts(simStats.SkipReasons, nil); skipSummary != "" {
		notes = append(notes, "groupSkips="+skipSummary)
	}
	if candidateSummary := summarizeMonthlyReasonCounts(simStats.CandidateReasons, nil); candidateSummary != "" {
		notes = append(notes, "candidateReasons="+candidateSummary)
	}
	if timeoutSummary := summarizeMonthlyTimeoutReasons(simStats.CandidateReasons); timeoutSummary != "" {
		notes = append(notes, "timeouts="+timeoutSummary)
	}
	if fetchSummary := summarizeMonthlyReasonCounts(fetchStats.FailureReasons, fetchStats.FailureSamples); fetchSummary != "" {
		notes = append(notes, "fetchReasons="+fetchSummary)
	}
	if strings.TrimSpace(fetchStats.RequestSummary) != "" {
		notes = append(notes, "requestSummary="+fetchStats.RequestSummary)
	}
	if strings.TrimSpace(simStats.CacheSummary) != "" {
		notes = append(notes, "klineCache="+simStats.CacheSummary)
	}

	return BacktestCSVRecord{
		TraceRecordMetadata: buildTraceRecordMetadata(r.cfg),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Scenario:            r.monthlyScenarioName("MonthlyMarketReplaySummary"),
		Symbol:              "ALL",
		Period:              periodLabel,
		Trades:              len(trades),
		WinRate:             formatFloat(winRate),
		FundingIncome:       formatFloat(totalFunding),
		TradingPnL:          formatFloat(totalTrading),
		TotalFees:           formatFloat(totalFees),
		NetPnL:              formatFloat(totalFunding + totalTrading - totalFees),
		MaxDrawdown:         formatFloat(maxDrawdown),
		Notes:               strings.Join(notes, " | "),
	}
}

func (r *BacktestRunner) buildMonthlyStressRecords(periodLabel string, trades []BacktestTradeSample, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats) []BacktestCSVRecord {
	if len(trades) == 0 {
		return nil
	}
	hcfg := r.historicalConfig()
	records := make([]BacktestCSVRecord, 0, 3)
	metadata := buildTraceRecordMetadata(r.cfg)

	needleSamples := applyMonthlyStressNeedle(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressNeedleShock"),
		periodLabel,
		needleSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=needle_shock", "assumption=entry_exit_price_spike"},
	))

	fundingSamples := applyMonthlyStressFundingSpike(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressFundingSpike"),
		periodLabel,
		fundingSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=funding_spike", "assumption=funding_income_reversal"},
	))

	downtimeSamples := applyMonthlyStressPlatformDowntime(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressPlatformDowntime"),
		periodLabel,
		downtimeSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=platform_downtime", "assumption=exit_delay_and_emergency_hedge"},
	))

	return records
}

func applyMonthlyStressNeedle(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for _, sample := range samples {
		extraTradingLoss := math.Max(perLegNotional*0.02, math.Abs(sample.TradingPnL)*1.25+sample.TotalFees)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressNeedleShock", 0, -extraTradingLoss, 0, fmt.Sprintf("needleLoss=%.6f", extraTradingLoss)))
	}
	return stressed
}

func applyMonthlyStressFundingSpike(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for _, sample := range samples {
		fundingShock := math.Max(perLegNotional*0.008, math.Abs(sample.FundingIncome)*1.6)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressFundingSpike", -fundingShock, 0, 0, fmt.Sprintf("fundingShock=%.6f", fundingShock)))
	}
	return stressed
}

func applyMonthlyStressPlatformDowntime(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for idx, sample := range samples {
		if idx%3 != 0 {
			stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressPlatformDowntime", 0, 0, 0, "downtimeImpact=none"))
			continue
		}
		emergencyLoss := math.Max(perLegNotional*0.03, math.Abs(sample.TradingPnL)+sample.TotalFees*2+0.3)
		extraFees := math.Max(0.05, sample.TotalFees*0.5)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressPlatformDowntime", 0, -emergencyLoss, extraFees, fmt.Sprintf("downtimeLoss=%.6f extraFees=%.6f", emergencyLoss, extraFees)))
	}
	return stressed
}

func withStressAdjustment(sample BacktestTradeSample, scenario string, fundingDelta float64, tradingDelta float64, feeDelta float64, note string) BacktestTradeSample {
	adjusted := sample
	adjusted.Scenario = scenario
	adjusted.FundingIncome += fundingDelta
	adjusted.TradingPnL += tradingDelta
	adjusted.TotalFees += feeDelta
	adjusted.Drawdown = 0
	adjusted.Win = adjusted.FundingIncome+adjusted.TradingPnL-adjusted.TotalFees > 0
	if strings.TrimSpace(note) != "" {
		if strings.TrimSpace(adjusted.Notes) == "" {
			adjusted.Notes = note
		} else {
			adjusted.Notes += " | " + note
		}
	}
	return adjusted
}

func buildStressSummaryRecord(scenario string, periodLabel string, trades []BacktestTradeSample, initialEquity float64, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, metadata TraceRecordMetadata, extraNotes []string) BacktestCSVRecord {
	totalFunding := 0.0
	totalTrading := 0.0
	totalFees := 0.0
	wins := 0
	equity := initialEquity
	peakEquity := initialEquity
	maxDrawdown := 0.0
	for _, trade := range trades {
		totalFunding += trade.FundingIncome
		totalTrading += trade.TradingPnL
		totalFees += trade.TotalFees
		netPnL := trade.FundingIncome + trade.TradingPnL - trade.TotalFees
		if netPnL > 0 {
			wins++
		}
		equity += netPnL
		if equity > peakEquity {
			peakEquity = equity
		}
		drawdownAmount := math.Max(0, peakEquity-equity)
		if -drawdownAmount < maxDrawdown {
			maxDrawdown = -drawdownAmount
		}
	}
	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}
	notes := []string{
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("fetchSuccessRate=%.4f", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount)),
	}
	notes = append(notes, extraNotes...)
	return BacktestCSVRecord{
		TraceRecordMetadata: metadata.normalized(),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Scenario:            scenario,
		Symbol:              "ALL",
		Period:              periodLabel,
		Trades:              len(trades),
		WinRate:             formatFloat(winRate),
		FundingIncome:       formatFloat(totalFunding),
		TradingPnL:          formatFloat(totalTrading),
		TotalFees:           formatFloat(totalFees),
		NetPnL:              formatFloat(totalFunding + totalTrading - totalFees),
		MaxDrawdown:         formatFloat(maxDrawdown),
		Notes:               strings.Join(notes, " | "),
	}
}

func (r *BacktestRunner) buildMonthlyRunSummaryLogRecord(periodLabel string, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, stopReason string, experiment monthlyExperimentPlan) MonthlySimulationLogRecord {
	notes := []string{
		"period=" + periodLabel,
		"stop=" + stopReason,
		fmt.Sprintf("experiment=%d/%d", experiment.Index, experiment.Total),
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("skipped=%d", simStats.SkippedGroups),
		fmt.Sprintf("fetchSuccessSymbols=%d", fetchStats.SuccessSymbols),
		fmt.Sprintf("fetchFailures=%d", fetchStats.FailureCount),
	}
	notes = append(notes, r.monthlyExperimentNotes()...)
	if fetchSummary := summarizeMonthlyReasonCounts(fetchStats.FailureReasons, fetchStats.FailureSamples); fetchSummary != "" {
		notes = append(notes, "fetchReasons="+fetchSummary)
	}
	if strings.TrimSpace(fetchStats.RequestSummary) != "" {
		notes = append(notes, "requestSummary="+fetchStats.RequestSummary)
	}
	if strings.TrimSpace(simStats.CacheSummary) != "" {
		notes = append(notes, "klineCache="+simStats.CacheSummary)
	}
	return MonthlySimulationLogRecord{
		RecordedAt:  time.Now().UTC().Format(time.RFC3339),
		Action:      "run_summary",
		Symbol:      "ALL",
		Reason:      stopReason,
		Notes:       strings.Join(notes, " | "),
		FundingTime: periodLabel,
	}
}

func summarizeMonthlyTimeoutReasons(reasonCounts map[string]int) string {
	if len(reasonCounts) == 0 {
		return ""
	}
	timeoutKeys := []string{
		"candidate_build_timeout",
		"candidate_finalize_timeout",
		"group_evaluation_timeout",
	}
	parts := make([]string, 0, len(timeoutKeys))
	for _, key := range timeoutKeys {
		count := reasonCounts[key]
		if count <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", key, count))
	}
	return strings.Join(parts, ",")
}

func (r *BacktestRunner) recordDir() string {
	if r.store != nil {
		return r.store.RecordDir()
	}
	return r.cfg.NetworkConfig.RecordDir
}

func (r *BacktestRunner) historicalConfig() historicalConfig {
	hcfg := r.cfg.HistoricalConfig
	zeroHistorical := HistoricalConfig{}
	cfg := historicalConfig{
		DefaultLookbackDays:                    hcfg.DefaultLookbackDays,
		InitialEquityUSDT:                      hcfg.InitialEquityUSDT,
		StopEquityUSDT:                         hcfg.StopEquityUSDT,
		PerLegNotionalUSDT:                     hcfg.PerLegNotionalUSDT,
		FundingDirectionMode:                   hcfg.FundingDirectionMode,
		MaxFundingCyclesHeld:                   hcfg.MaxFundingCyclesHeld,
		MaxPerLegEquityShare:                   hcfg.MaxPerLegEquityShare,
		SpotTakerFeeRate:                       hcfg.SpotTakerFeeRate,
		FuturesTakerFeeRate:                    hcfg.FuturesTakerFeeRate,
		AssumedHourlyBorrowInterestRate:        hcfg.AssumedHourlyBorrowInterestRate,
		BorrowInterestMinBillingHours:          hcfg.BorrowInterestMinBillingHours,
		NegativeCarrySpotEntryImpactMultiplier: hcfg.NegativeCarrySpotEntryImpactMultiplier,
		NegativeCarrySpotExitImpactMultiplier:  hcfg.NegativeCarrySpotExitImpactMultiplier,
		CrossStableQuoteMaxDeviationRate:       hcfg.CrossStableQuoteMaxDeviationRate,
		FundingPhaseLead:                       hcfg.FundingPhaseLead,
		FundingFetchConcurrency:                hcfg.FundingFetchConcurrency,
		FundingRetryMax:                        hcfg.FundingRetryMax,
		CacheDir:                               hcfg.CacheDir,
		ReferenceKlineLookback:                 hcfg.ReferenceKlineLookback,
		EstimateKlineLookback:                  hcfg.EstimateKlineLookback,
		SafetyBufferFloorUSDT:                  hcfg.SafetyBufferFloorUSDT,
		SafetyBufferCostMultiplier:             hcfg.SafetyBufferCostMultiplier,
		MaxDrawdownRate:                        hcfg.MaxDrawdownRate,
		MaxConsecutiveLosses:                   hcfg.MaxConsecutiveLosses,
		MaxFundingRate:                         hcfg.MaxFundingRate,
		MaxFundingRateJump:                     hcfg.MaxFundingRateJump,
		MaxSingleTradeLossUSDT:                 hcfg.MaxSingleTradeLossUSDT,
		FundingFetchSlot:                       hcfg.FundingFetchSlot,
		FundingFetchTimeout:                    hcfg.FundingFetchTimeout,
		GroupEvaluationTimeout:                 hcfg.GroupEvaluationTimeout,
		CandidateBuildTimeout:                  hcfg.CandidateBuildTimeout,
		CandidateFinalizeTimeout:               hcfg.CandidateFinalizeTimeout,
		ScreeningConcurrency:                   hcfg.ScreeningConcurrency,
		DeepCandidateLimit:                     hcfg.DeepCandidateLimit,
		CoarseSymbolLimit:                      hcfg.CoarseSymbolLimit,
		FundingGroupTopSymbolsPerTime:          hcfg.FundingGroupTopSymbolsPerTime,
		LiquidityShareMin:                      hcfg.LiquidityShareMin,
		LiquidityShareFloor:                    hcfg.LiquidityShareFloor,
		LiquidityShareMax:                      hcfg.LiquidityShareMax,
		SevereLiquidityShareCap:                hcfg.SevereLiquidityShareCap,
		SevereLiquidityShareMultiplier:         hcfg.SevereLiquidityShareMultiplier,
		SevereLiquidityShareMax:                hcfg.SevereLiquidityShareMax,
	}
	if cfg.DefaultLookbackDays <= 0 {
		cfg.DefaultLookbackDays = 30
	}
	if cfg.InitialEquityUSDT <= 0 {
		cfg.InitialEquityUSDT = 20
	}
	if cfg.StopEquityUSDT <= 0 || cfg.StopEquityUSDT >= cfg.InitialEquityUSDT {
		cfg.StopEquityUSDT = cfg.InitialEquityUSDT * 0.5
	}
	if cfg.PerLegNotionalUSDT <= 0 {
		cfg.PerLegNotionalUSDT = 10
	}
	if cfg.MaxFundingCyclesHeld <= 0 {
		cfg.MaxFundingCyclesHeld = 1
	}
	if strings.TrimSpace(cfg.FundingDirectionMode) == "" {
		cfg.FundingDirectionMode = historicalFundingModeBoth
	}
	cfg.FundingDirectionMode = normalizeHistoricalFundingDirectionMode(cfg.FundingDirectionMode)
	if cfg.MaxPerLegEquityShare <= 0 || cfg.MaxPerLegEquityShare > 1 {
		cfg.MaxPerLegEquityShare = 0.5
	}
	if cfg.AssumedHourlyBorrowInterestRate < 0 || (cfg.AssumedHourlyBorrowInterestRate == 0 && hcfg == zeroHistorical) {
		cfg.AssumedHourlyBorrowInterestRate = 0.00005
	}
	if cfg.BorrowInterestMinBillingHours <= 0 {
		cfg.BorrowInterestMinBillingHours = 1
	}
	if cfg.NegativeCarrySpotEntryImpactMultiplier < 1 {
		cfg.NegativeCarrySpotEntryImpactMultiplier = 1.5
	}
	if cfg.NegativeCarrySpotExitImpactMultiplier < 1 {
		cfg.NegativeCarrySpotExitImpactMultiplier = 1.5
	}
	if cfg.CrossStableQuoteMaxDeviationRate < 0 || (cfg.CrossStableQuoteMaxDeviationRate == 0 && hcfg == zeroHistorical) {
		cfg.CrossStableQuoteMaxDeviationRate = 0.0050
	}
	if cfg.MaxFundingRate <= 0 {
		cfg.MaxFundingRate = 0.03
	}
	if cfg.MaxFundingRateJump <= 0 {
		cfg.MaxFundingRateJump = 0.015
	}
	if cfg.MaxSingleTradeLossUSDT <= 0 {
		cfg.MaxSingleTradeLossUSDT = math.Max(1, cfg.InitialEquityUSDT*0.075)
	}
	if cfg.SpotTakerFeeRate < 0 || (cfg.SpotTakerFeeRate == 0 && hcfg == zeroHistorical) {
		cfg.SpotTakerFeeRate = 0.0010
	}
	if cfg.FuturesTakerFeeRate < 0 || (cfg.FuturesTakerFeeRate == 0 && hcfg == zeroHistorical) {
		cfg.FuturesTakerFeeRate = 0.0005
	}
	if cfg.FundingPhaseLead <= 0 {
		cfg.FundingPhaseLead = r.cfg.StrategyConfig.MonitorStopLead
		if cfg.FundingPhaseLead <= 0 {
			cfg.FundingPhaseLead = 30 * time.Second
		}
	}
	if cfg.FundingFetchConcurrency <= 0 {
		cfg.FundingFetchConcurrency = 2
	}
	if cfg.FundingRetryMax <= 0 {
		cfg.FundingRetryMax = 6
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		recordDir := r.recordDir()
		if strings.TrimSpace(recordDir) == "" {
			recordDir = "records"
		}
		cfg.CacheDir = filepath.Join(recordDir, "historical_cache")
	}
	if cfg.ReferenceKlineLookback <= 0 {
		cfg.ReferenceKlineLookback = 60
	}
	if cfg.EstimateKlineLookback <= 0 {
		cfg.EstimateKlineLookback = 180
	}
	if cfg.SafetyBufferFloorUSDT <= 0 {
		cfg.SafetyBufferFloorUSDT = 0.001
	}
	if cfg.SafetyBufferCostMultiplier <= 0 {
		cfg.SafetyBufferCostMultiplier = 0.35
	}
	if cfg.MaxDrawdownRate <= 0 || cfg.MaxDrawdownRate >= 1 {
		cfg.MaxDrawdownRate = 0.25
	}
	if cfg.MaxConsecutiveLosses <= 0 {
		cfg.MaxConsecutiveLosses = 2
	}
	if cfg.FundingFetchSlot <= 0 {
		cfg.FundingFetchSlot = 0
	}
	if cfg.FundingFetchTimeout <= 0 {
		cfg.FundingFetchTimeout = 45 * time.Second
	}
	if cfg.GroupEvaluationTimeout <= 0 {
		cfg.GroupEvaluationTimeout = 90 * time.Second
	}
	if cfg.CandidateBuildTimeout <= 0 {
		cfg.CandidateBuildTimeout = 20 * time.Second
	}
	if cfg.CandidateFinalizeTimeout <= 0 {
		cfg.CandidateFinalizeTimeout = 20 * time.Second
	}
	if cfg.ScreeningConcurrency <= 0 {
		cfg.ScreeningConcurrency = 10
	}
	if cfg.DeepCandidateLimit <= 0 {
		cfg.DeepCandidateLimit = 24
	}
	if cfg.CoarseSymbolLimit <= 0 {
		cfg.CoarseSymbolLimit = maxInt(cfg.DeepCandidateLimit*4, monthlyFundingTopSymbolsPerTime*2)
	}
	if cfg.FundingGroupTopSymbolsPerTime <= 0 {
		cfg.FundingGroupTopSymbolsPerTime = monthlyFundingTopSymbolsPerTime
	}
	if cfg.CoarseSymbolLimit < cfg.FundingGroupTopSymbolsPerTime {
		cfg.CoarseSymbolLimit = cfg.FundingGroupTopSymbolsPerTime
	}
	if cfg.LiquidityShareMin <= 0 || cfg.LiquidityShareMin > 1 {
		cfg.LiquidityShareMin = 0.01
	}
	if cfg.LiquidityShareFloor <= 0 || cfg.LiquidityShareFloor > 1 {
		cfg.LiquidityShareFloor = 0.08
	}
	if cfg.LiquidityShareMax <= 0 || cfg.LiquidityShareMax > 1 {
		cfg.LiquidityShareMax = 0.25
	}
	if cfg.LiquidityShareMax < cfg.LiquidityShareMin {
		cfg.LiquidityShareMax = cfg.LiquidityShareMin
	}
	if cfg.SevereLiquidityShareCap <= 0 || cfg.SevereLiquidityShareCap > 1 {
		cfg.SevereLiquidityShareCap = 0.20
	}
	if cfg.SevereLiquidityShareMultiplier <= 1 {
		cfg.SevereLiquidityShareMultiplier = 2.5
	}
	if cfg.SevereLiquidityShareMax <= 0 || cfg.SevereLiquidityShareMax > 1 {
		cfg.SevereLiquidityShareMax = 0.50
	}
	if cfg.SevereLiquidityShareMax < cfg.SevereLiquidityShareCap {
		cfg.SevereLiquidityShareMax = cfg.SevereLiquidityShareCap
	}
	return cfg
}

func (r *BacktestRunner) monthlyExperimentPlans() []monthlyExperimentPlan {
	if !r.cfg.HistoricalConfig.BatchExperimentEnabled {
		return []monthlyExperimentPlan{{Index: 1, Total: 1, Cfg: r.cfg}}
	}
	spotFees := parseMonthlyFloatList(r.cfg.HistoricalConfig.BatchSpotTakerFeeRates, []float64{r.cfg.HistoricalConfig.SpotTakerFeeRate})
	perLegs := parseMonthlyFloatList(r.cfg.HistoricalConfig.BatchPerLegNotionalUSDT, []float64{r.cfg.HistoricalConfig.PerLegNotionalUSDT})
	holdingCycles := parseMonthlyIntList(r.cfg.HistoricalConfig.BatchMaxFundingCyclesHeld, []int{r.cfg.HistoricalConfig.MaxFundingCyclesHeld})
	plans := make([]monthlyExperimentPlan, 0, len(spotFees)*len(perLegs)*len(holdingCycles))
	index := 1
	for _, spotFee := range spotFees {
		for _, perLeg := range perLegs {
			for _, cycles := range holdingCycles {
				cfg := r.cfg
				cfg.HistoricalConfig.BatchExperimentEnabled = false
				cfg.HistoricalConfig.SpotTakerFeeRate = spotFee
				cfg.HistoricalConfig.PerLegNotionalUSDT = perLeg
				cfg.HistoricalConfig.MaxFundingCyclesHeld = maxInt(cycles, 1)
				cfg = alignHistoricalExperimentCapital(cfg)
				plans = append(plans, monthlyExperimentPlan{
					Index: index,
					Cfg:   cfg,
				})
				index++
			}
		}
	}
	for idx := range plans {
		plans[idx].Total = len(plans)
	}
	if len(plans) == 0 {
		return []monthlyExperimentPlan{{Index: 1, Total: 1, Cfg: r.cfg}}
	}
	return plans
}

func parseMonthlyFloatList(raw string, fallback []float64) []float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	values := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value := parseFloat(part)
		key := fmt.Sprintf("%.8f", value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func parseMonthlyIntList(raw string, fallback []int) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	seen := make(map[int]struct{}, len(parts))
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value := int(parseFloat(part))
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func (r *BacktestRunner) runMonthlyExperimentPlans(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, groups []monthlyEventGroup, periodLabel string, fetchStats monthlyFundingFetchStats, experiments []monthlyExperimentPlan) ([]BacktestCSVRecord, []MonthlySimulationLogRecord, int, error) {
	allRecords := make([]BacktestCSVRecord, 0, len(experiments)*8)
	allLogs := make([]MonthlySimulationLogRecord, 0, len(experiments)*32)
	totalTrades := 0
	for _, experiment := range experiments {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		r.reportProgress(EngineStage("evaluate_groups"), fmt.Sprintf("实验 %d/%d: 现货费率=%.4f 单腿=%.2f funding=%d", experiment.Index, experiment.Total, experiment.Cfg.HistoricalConfig.SpotTakerFeeRate, experiment.Cfg.HistoricalConfig.PerLegNotionalUSDT, experiment.Cfg.HistoricalConfig.MaxFundingCyclesHeld), experiment.Index-1, experiment.Total)
		experimentRunner := &BacktestRunner{
			store:        r.store,
			logger:       r.logger,
			cfg:          experiment.Cfg,
			progressHook: nil,
		}
		tradeSamples, logs, simStats, stopReason, err := experimentRunner.simulateMonthlyFundingGroups(ctx, client, cache, groups, periodLabel)
		if err != nil {
			return nil, nil, 0, err
		}
		localFetchStats := fetchStats
		localFetchStats.RequestSummary = client.historicalRequestSummary()
		logs = append(logs, experimentRunner.buildMonthlyRunSummaryLogRecord(periodLabel, localFetchStats, simStats, stopReason, experiment))
		records := buildBacktestRecords(experimentRunner.cfg, tradeSamples)
		records = append(records, experimentRunner.buildMonthlySummaryRecord(periodLabel, tradeSamples, localFetchStats, simStats, stopReason))
		records = append(records, experimentRunner.buildMonthlyStressRecords(periodLabel, tradeSamples, localFetchStats, simStats)...)
		allLogs = append(allLogs, logs...)
		allRecords = append(allRecords, records...)
		totalTrades += len(tradeSamples)
	}
	return allRecords, allLogs, totalTrades, nil
}

func (r *BacktestRunner) monthlyHoldingCycles() int {
	return maxInt(1, r.historicalConfig().MaxFundingCyclesHeld)
}

func (r *BacktestRunner) monthlyExitTime(fundingTime time.Time) time.Time {
	cycles := r.monthlyHoldingCycles()
	return fundingTime.Add(time.Duration(cycles-1) * 8 * time.Hour)
}

func (r *BacktestRunner) monthlyScenarioName(base string) string {
	hcfg := r.historicalConfig()
	return fmt.Sprintf("%s[mode=%s,spot=%.4f,futures=%.4f,per_leg=%.2f,hold=%d]", base, normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode), hcfg.SpotTakerFeeRate, hcfg.FuturesTakerFeeRate, r.monthlyPerLegNotional(), r.monthlyHoldingCycles())
}

func (r *BacktestRunner) monthlyExperimentNotes() []string {
	hcfg := r.historicalConfig()
	notes := []string{
		fmt.Sprintf("batchExperimentEnabled=%t", r.cfg.HistoricalConfig.BatchExperimentEnabled),
		"fundingTimeline=actual_history",
		"fundingDirectionMode=" + normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode),
		fmt.Sprintf("configuredPerLegNotionalUSDT=%.2f", r.cfg.HistoricalConfig.PerLegNotionalUSDT),
		fmt.Sprintf("spotFeeRate=%.4f", hcfg.SpotTakerFeeRate),
		fmt.Sprintf("futuresFeeRate=%.4f", hcfg.FuturesTakerFeeRate),
		fmt.Sprintf("assumedHourlyBorrowInterestRate=%.4f", hcfg.AssumedHourlyBorrowInterestRate),
		fmt.Sprintf("borrowInterestMinBillingHours=%d", hcfg.BorrowInterestMinBillingHours),
		fmt.Sprintf("negativeCarrySpotEntryImpactMultiplier=%.2f", hcfg.NegativeCarrySpotEntryImpactMultiplier),
		fmt.Sprintf("negativeCarrySpotExitImpactMultiplier=%.2f", hcfg.NegativeCarrySpotExitImpactMultiplier),
		fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.4f", hcfg.CrossStableQuoteMaxDeviationRate),
		fmt.Sprintf("perLegNotionalUSDT=%.2f", r.monthlyPerLegNotional()),
		fmt.Sprintf("maxFundingCyclesHeld=%d", r.monthlyHoldingCycles()),
	}
	if len(r.borrowHourlyInterestByAsset) > 0 {
		notes = append(notes, fmt.Sprintf("borrowHourlyRateAssets=%d", len(r.borrowHourlyInterestByAsset)))
	}
	return notes
}

func (r *BacktestRunner) hourlyBorrowInterestRateForSymbol(client *BinanceClient, symbol string) float64 {
	rate := r.historicalConfig().AssumedHourlyBorrowInterestRate
	if r == nil || client == nil || len(r.borrowHourlyInterestByAsset) == 0 {
		return rate
	}
	if meta, ok := client.historicalSpotMetaForFuture(symbol); ok {
		asset := strings.ToUpper(strings.TrimSpace(meta.BaseAsset))
		if asset != "" {
			if actualRate, ok := r.borrowHourlyInterestByAsset[asset]; ok && actualRate > 0 {
				return actualRate
			}
		}
	}
	return rate
}

func (r *BacktestRunner) sumMonthlyFundingIncome(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, direction fundingCarryDirection, hedgeQty float64, markPrice float64, fundingTime time.Time) (float64, error) {
	if hedgeQty <= 0 || markPrice <= 0 {
		return 0, nil
	}
	schedule, err := cache.getFundingSchedule(ctx, client, symbol, fundingTime, r.monthlyHoldingCycles())
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, item := range schedule {
		total += historicalFundingIncome(direction, hedgeQty, markPrice, parseFloat(item.FundingRate), 1)
	}
	return total, nil
}

func (r *BacktestRunner) monthlySafetyBuffer(entryExecutionCost float64, projectedExitCost float64, openBasisPenalty float64) float64 {
	hcfg := r.historicalConfig()
	explicitCost := math.Max(0, entryExecutionCost)*0.50 +
		math.Max(0, projectedExitCost)*0.25 +
		math.Max(0, openBasisPenalty)*0.50
	return math.Max(hcfg.SafetyBufferFloorUSDT, explicitCost*hcfg.SafetyBufferCostMultiplier)
}

func (r *BacktestRunner) monthlyPerLegNotional() float64 {
	hcfg := r.historicalConfig()
	notional := hcfg.PerLegNotionalUSDT
	if r.cfg.StrategyConfig.PerSymbolQuoteUSDT > 0 {
		notional = math.Min(notional, r.cfg.StrategyConfig.PerSymbolQuoteUSDT)
	}
	maxPerLegByEquity := hcfg.InitialEquityUSDT * hcfg.MaxPerLegEquityShare
	if maxPerLegByEquity > 0 {
		notional = math.Min(notional, maxPerLegByEquity)
	}
	return math.Max(1, notional)
}

func (r *BacktestRunner) monthlyLiquidityShareLimit() float64 {
	hcfg := r.historicalConfig()
	limit := r.cfg.MaxVisibleDepthShare
	if limit <= 0 {
		limit = hcfg.LiquidityShareFloor
	}
	return math.Max(hcfg.LiquidityShareMin, math.Min(hcfg.LiquidityShareMax, limit))
}

func (r *BacktestRunner) monthlySevereLiquidityShareLimit(baseLimit float64) float64 {
	hcfg := r.historicalConfig()
	if baseLimit <= 0 {
		baseLimit = r.monthlyLiquidityShareLimit()
	}
	return math.Max(hcfg.SevereLiquidityShareCap, math.Min(hcfg.SevereLiquidityShareMax, baseLimit*hcfg.SevereLiquidityShareMultiplier))
}

func (r *BacktestRunner) monthlyScreeningCandidateLimit(groupSize int) int {
	if groupSize <= 0 {
		return 0
	}
	hcfg := r.historicalConfig()
	screeningRounds := int(hcfg.GroupEvaluationTimeout / maxDuration(hcfg.CandidateBuildTimeout, time.Second))
	if screeningRounds <= 1 {
		screeningRounds = 2
	}
	limitByBudget := hcfg.ScreeningConcurrency * maxInt(2, screeningRounds-1)
	limit := maxInt(hcfg.DeepCandidateLimit*2, limitByBudget)
	if limit < hcfg.DeepCandidateLimit {
		limit = hcfg.DeepCandidateLimit
	}
	limit = minInt(limit, maxInt(hcfg.DeepCandidateLimit*3, 72))
	return minInt(groupSize, limit)
}

func (r *BacktestRunner) monthlyCoarseSymbolLimit(totalSymbols int) int {
	if totalSymbols <= 0 {
		return 0
	}
	limit := r.historicalConfig().CoarseSymbolLimit
	if limit <= 0 {
		limit = maxInt(r.historicalConfig().DeepCandidateLimit*4, monthlyFundingTopSymbolsPerTime*2)
	}
	return minInt(totalSymbols, limit)
}

func trimMonthlyFundingGroupItems(items []FundingRateHistoryItem, limit int) []FundingRateHistoryItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	trimmed := make([]FundingRateHistoryItem, limit)
	copy(trimmed, items[:limit])
	return trimmed
}

func summarizeMonthlyGroupSkip(group monthlyEventGroup, evaluated []monthlyCandidate, groupReason string, groupTimeout time.Duration) monthlyCandidate {
	summary := monthlyCandidate{
		Reason: "no_viable_candidate",
		Notes:  []string{"本轮所有候选经过收益与流动性校验后都未通过"},
	}
	if strings.TrimSpace(groupReason) != "" {
		summary.Reason = groupReason
		summary.Notes = []string{
			fmt.Sprintf("groupTimeout=%s", groupTimeout),
			fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
			fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		}
	}
	if len(evaluated) == 0 {
		return summary
	}
	reasonCounts := make(map[string]int)
	for _, candidate := range evaluated {
		reason := strings.TrimSpace(candidate.Reason)
		if reason == "" {
			continue
		}
		reasonCounts[reason]++
	}
	if len(reasonCounts) == 0 {
		return summary
	}
	primaryReason := dominantMonthlyGroupSkipReason(reasonCounts)
	summary.Reason = nonEmpty(primaryReason, summary.Reason)
	notes := []string{
		fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		"reasonBreakdown=" + summarizeMonthlyReasonCounts(reasonCounts, nil),
	}
	if focus := firstMonthlyCandidateByReason(evaluated, primaryReason); focus != nil {
		if focus.Symbol != "" {
			notes = append(notes, "sampleSymbol="+focus.Symbol)
		}
		notes = append(notes, firstMatchingMonthlyNotes(focus.Notes,
			"safetyBufferUSDT=",
			"safetyBufferGap=",
			"estimatedNetPnL=",
			"entryExecutionCost=",
			"spotLiquidityShare=",
			"futuresLiquidityShare=",
			"premiumIndexClose=",
			"markIndexBasis=",
		)...)
	}
	if strings.TrimSpace(groupReason) != "" && groupReason != summary.Reason {
		notes = append(notes, "groupReason="+groupReason)
	}
	summary.Notes = notes
	return summary
}

func dominantMonthlyGroupSkipReason(reasonCounts map[string]int) string {
	if len(reasonCounts) == 0 {
		return ""
	}
	bestReason := ""
	bestCount := -1
	for reason, count := range reasonCounts {
		if reason == "screening_prefilter_excluded" && len(reasonCounts) > 1 {
			continue
		}
		if count > bestCount || (count == bestCount && reason < bestReason) {
			bestReason = reason
			bestCount = count
		}
	}
	if strings.TrimSpace(bestReason) != "" {
		return bestReason
	}
	for reason, count := range reasonCounts {
		if count > bestCount || (count == bestCount && reason < bestReason) {
			bestReason = reason
			bestCount = count
		}
	}
	return bestReason
}

func firstMonthlyCandidateByReason(candidates []monthlyCandidate, reason string) *monthlyCandidate {
	for index := range candidates {
		if candidates[index].Reason == reason {
			return &candidates[index]
		}
	}
	return nil
}

func firstMatchingMonthlyNotes(notes []string, prefixes ...string) []string {
	if len(notes) == 0 || len(prefixes) == 0 {
		return nil
	}
	matches := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		for _, note := range notes {
			if strings.HasPrefix(note, prefix) {
				matches = append(matches, note)
				break
			}
		}
	}
	return matches
}

func newMonthlyLogAggregation() *monthlyLogAggregation {
	return &monthlyLogAggregation{
		counts:  make(map[string]int),
		samples: make(map[string][]string),
	}
}

func (a *monthlyLogAggregation) add(reason string, symbol string) {
	if a == nil || strings.TrimSpace(reason) == "" {
		return
	}
	a.counts[reason]++
	if strings.TrimSpace(symbol) == "" {
		return
	}
	if len(a.samples[reason]) >= 5 {
		return
	}
	a.samples[reason] = append(a.samples[reason], symbol)
}

func (r *BacktestRunner) logMonthlyGroupAggregation(group monthlyEventGroup, screeningCount int, agg *monthlyLogAggregation, groupReason string) {
	if r.logger == nil {
		return
	}
	parts := make([]string, 0, 4)
	parts = append(parts,
		fmt.Sprintf("原始候选=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("保留候选=%d", len(group.Items)),
		fmt.Sprintf("预筛后=%d", screeningCount),
	)
	for _, reason := range []string{"candidate_build_timeout", "candidate_pricing_timeout", "candidate_finalize_timeout"} {
		count := agg.counts[reason]
		if count <= 0 {
			continue
		}
		part := fmt.Sprintf("%s=%d", reason, count)
		if len(agg.samples[reason]) > 0 {
			part += "[" + strings.Join(agg.samples[reason], ",") + "]"
		}
		parts = append(parts, part)
	}
	if strings.TrimSpace(groupReason) != "" {
		parts = append(parts, "groupReason="+groupReason)
	}
	if len(parts) <= 2 && strings.TrimSpace(groupReason) == "" {
		return
	}
	r.logger.Printf("历史模拟评估摘要 fundingTime=%s %s", group.FundingTime.Format(time.RFC3339), strings.Join(parts, " | "))
}

func buildMonthlyTradeLogRecord(candidate *monthlyCandidate, action string, equityBefore float64, equityAfter float64, drawdownRate float64) MonthlySimulationLogRecord {
	return MonthlySimulationLogRecord{
		RecordedAt:              time.Now().UTC().Format(time.RFC3339),
		FundingTime:             candidate.FundingTime.Format(time.RFC3339),
		Action:                  action,
		Symbol:                  candidate.Symbol,
		EstimatedFundingRate:    formatFloat(candidate.EstimatedFundingRate),
		FundingRate:             formatFloat(candidate.FundingRate),
		EstimatedFundingIncome:  formatFloat(candidate.EstimatedFundingIncome),
		FundingIncome:           formatFloat(candidate.FundingIncome),
		HedgeQty:                formatFloat(candidate.HedgeQty),
		SpotEntryPrice:          formatFloat(candidate.SpotEntryPrice),
		FuturesEntryPrice:       formatFloat(candidate.FuturesEntryPrice),
		SpotExitPrice:           formatFloat(candidate.SpotExitPrice),
		FuturesExitPrice:        formatFloat(candidate.FuturesExitPrice),
		OpenBasisRate:           formatFloat(candidate.OpenBasisRate),
		CloseBasisRate:          formatFloat(candidate.CloseBasisRate),
		SpotEntryQuoteVolume:    formatFloat(candidate.SpotEntryQuoteVolume),
		FuturesEntryQuoteVolume: formatFloat(candidate.FuturesEntryQuoteVolume),
		SpotExitQuoteVolume:     formatFloat(candidate.SpotExitQuoteVolume),
		FuturesExitQuoteVolume:  formatFloat(candidate.FuturesExitQuoteVolume),
		SpotEntrySlipRate:       formatFloat(candidate.SpotEntrySlipRate),
		FuturesEntrySlipRate:    formatFloat(candidate.FuturesEntrySlipRate),
		SpotExitSlipRate:        formatFloat(candidate.SpotExitSlipRate),
		FuturesExitSlipRate:     formatFloat(candidate.FuturesExitSlipRate),
		SpotEntryImpactRate:     formatFloat(candidate.SpotEntryImpactRate),
		FuturesEntryImpactRate:  formatFloat(candidate.FuturesEntryImpactRate),
		SpotExitImpactRate:      formatFloat(candidate.SpotExitImpactRate),
		FuturesExitImpactRate:   formatFloat(candidate.FuturesExitImpactRate),
		SpotEntryFee:            formatFloat(candidate.SpotEntryFee),
		FuturesEntryFee:         formatFloat(candidate.FuturesEntryFee),
		SpotExitFee:             formatFloat(candidate.SpotExitFee),
		FuturesExitFee:          formatFloat(candidate.FuturesExitFee),
		OpenBasisPenalty:        formatFloat(candidate.OpenBasisPenalty),
		ProjectedExitCost:       formatFloat(candidate.ProjectedExitCost),
		MinLiquidityRequired:    formatFloat(candidate.MinLiquidityRequired),
		SpotGrossPnL:            formatFloat(candidate.SpotGrossPnL),
		FuturesGrossPnL:         formatFloat(candidate.FuturesGrossPnL),
		TotalFees:               formatFloat(candidate.TotalFees),
		TotalFriction:           formatFloat(candidate.TotalFriction),
		EstimatedNetPnL:         formatFloat(candidate.EstimatedNetPnL),
		RealizedNetPnL:          formatFloat(candidate.RealizedNetPnL),
		EquityBefore:            formatFloat(equityBefore),
		EquityAfter:             formatFloat(equityAfter),
		DrawdownRate:            formatFloat(drawdownRate),
		Reason:                  candidate.Reason,
		Notes:                   strings.Join(candidate.Notes, " | "),
	}
}

func buildMonthlyGroupSummaryLogRecord(group monthlyEventGroup, evaluated []monthlyCandidate, selected *monthlyCandidate, equity float64, drawdownRate float64, groupReason string) MonthlySimulationLogRecord {
	focus := selected
	if focus == nil {
		focus = bestMonthlyCandidateForSummary(evaluated)
	}
	reasonCounts := make(map[string]int)
	viableCount := 0
	for index := range evaluated {
		reason := strings.TrimSpace(evaluated[index].Reason)
		if reason == "" {
			viableCount++
			continue
		}
		reasonCounts[reason]++
	}
	summaryReason := "group_without_trade"
	if selected != nil {
		summaryReason = "selected_for_trade"
	} else if strings.TrimSpace(groupReason) != "" {
		summaryReason = groupReason
	} else if dominant := dominantMonthlyGroupSkipReason(reasonCounts); strings.TrimSpace(dominant) != "" {
		summaryReason = dominant
	}
	notes := []string{
		fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		fmt.Sprintf("evaluated=%d", len(evaluated)),
		fmt.Sprintf("viableCandidates=%d", viableCount),
		"reasonBreakdown=" + summarizeMonthlyReasonCounts(reasonCounts, nil),
	}
	if focus != nil {
		if focus.Symbol != "" {
			notes = append(notes, "focusSymbol="+focus.Symbol)
		}
		focusReason := focus.Reason
		if strings.TrimSpace(focusReason) == "" && selected != nil {
			focusReason = "selected_for_trade"
		}
		if strings.TrimSpace(focusReason) != "" {
			notes = append(notes, "focusReason="+focusReason)
		}
		notes = append(notes,
			fmt.Sprintf("focusEstimatedNetPnL=%.6f", focus.EstimatedNetPnL),
			fmt.Sprintf("focusEstimatedFundingIncome=%.6f", focus.EstimatedFundingIncome),
			fmt.Sprintf("focusActualFundingIncome=%.6f", focus.FundingIncome),
		)
		notes = append(notes, firstMatchingMonthlyNotes(focus.Notes,
			"safetyBufferUSDT=",
			"safetyBufferGap=",
			"estimatedNetPnL=",
			"entryExecutionCost=",
			"spotLiquidityShare=",
			"futuresLiquidityShare=",
			"premiumIndexClose=",
			"markIndexBasis=",
			"prefilterRank=",
			"screeningRank=",
		)...)
	}
	if strings.TrimSpace(groupReason) != "" && groupReason != summaryReason {
		notes = append(notes, "groupReason="+groupReason)
	}
	record := MonthlySimulationLogRecord{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		FundingTime:  group.FundingTime.Format(time.RFC3339),
		Action:       "group_summary",
		EquityBefore: formatFloat(equity),
		EquityAfter:  formatFloat(equity),
		DrawdownRate: formatFloat(drawdownRate),
		Reason:       summaryReason,
		Notes:        strings.Join(notes, " | "),
	}
	if focus != nil {
		record.Symbol = focus.Symbol
		record.EstimatedFundingRate = formatFloat(focus.EstimatedFundingRate)
		record.FundingRate = formatFloat(focus.FundingRate)
		record.EstimatedFundingIncome = formatFloat(focus.EstimatedFundingIncome)
		record.FundingIncome = formatFloat(focus.FundingIncome)
		record.HedgeQty = formatFloat(focus.HedgeQty)
		record.SpotEntryPrice = formatFloat(focus.SpotEntryPrice)
		record.FuturesEntryPrice = formatFloat(focus.FuturesEntryPrice)
		record.OpenBasisRate = formatFloat(focus.OpenBasisRate)
		record.SpotEntryQuoteVolume = formatFloat(focus.SpotEntryQuoteVolume)
		record.FuturesEntryQuoteVolume = formatFloat(focus.FuturesEntryQuoteVolume)
		record.TotalFees = formatFloat(focus.TotalFees)
		record.TotalFriction = formatFloat(focus.TotalFriction)
		record.EstimatedNetPnL = formatFloat(focus.EstimatedNetPnL)
		record.RealizedNetPnL = formatFloat(focus.RealizedNetPnL)
	}
	return record
}

func buildMonthlyCandidateLogRecord(candidate monthlyCandidate, action string, equityBefore float64, equityAfter float64, drawdownRate float64, reason string, extraNotes ...string) MonthlySimulationLogRecord {
	notes := append([]string{}, candidate.Notes...)
	notes = append(notes, extraNotes...)
	candidate.Reason = reason
	candidate.Notes = notes
	return buildMonthlyTradeLogRecord(&candidate, action, equityBefore, equityAfter, drawdownRate)
}

func bestMonthlyCandidateForSummary(candidates []monthlyCandidate) *monthlyCandidate {
	if len(candidates) == 0 {
		return nil
	}
	bestIndex := -1
	for index := range candidates {
		if bestIndex == -1 || betterMonthlySummaryCandidate(candidates[index], candidates[bestIndex]) {
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &candidates[bestIndex]
}

func betterMonthlySummaryCandidate(candidate monthlyCandidate, current monthlyCandidate) bool {
	if candidate.EstimatedNetPnL != current.EstimatedNetPnL {
		return candidate.EstimatedNetPnL > current.EstimatedNetPnL
	}
	if math.Abs(candidate.EstimatedFundingRate) != math.Abs(current.EstimatedFundingRate) {
		return math.Abs(candidate.EstimatedFundingRate) > math.Abs(current.EstimatedFundingRate)
	}
	if monthlyReasonPriority(candidate.Reason) != monthlyReasonPriority(current.Reason) {
		return monthlyReasonPriority(candidate.Reason) < monthlyReasonPriority(current.Reason)
	}
	return candidate.Symbol < current.Symbol
}

func monthlyReasonPriority(reason string) int {
	switch strings.TrimSpace(reason) {
	case "":
		return 0
	case "funding_rate_extreme":
		return 1
	case "funding_jump_too_large":
		return 2
	case "funding_direction_reversed":
		return 3
	case "estimated_net_below_safety_buffer":
		return 4
	case "entry_liquidity_share_too_high":
		return 5
	case "entry_liquidity_extremely_poor":
		return 6
	case "estimated_funding_direction_disabled":
		return 7
	case "estimated_funding_non_positive":
		return 8
	case "screening_topn_excluded":
		return 9
	case "screening_prefilter_excluded":
		return 10
	default:
		return 11
	}
}

func writeMonthlySimulationLogs(recordDir string, cfg EngineConfig, logs []MonthlySimulationLogRecord) error {
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return fmt.Errorf("创建记录目录失败: %w", err)
	}
	metadata := buildTraceRecordMetadata(cfg)
	for idx := range logs {
		if strings.TrimSpace(logs[idx].ConfigHash) == "" {
			logs[idx].TraceRecordMetadata = metadata
		}
	}

	path := filepath.Join(recordDir, monthlySimulationLogCSVName)
	if err := writeMonthlySimulationLogFile(path, logs); err != nil {
		return err
	}

	if len(logs) == 0 {
		return nil
	}
	archiveDir := filepath.Join(recordDir, monthlySimulationArchiveDirName)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("创建月度模拟归档目录失败: %w", err)
	}
	archiveName := time.Now().UTC().Format("20060102_150405") + "_" + monthlySimulationLogCSVName
	return writeMonthlySimulationLogFile(filepath.Join(archiveDir, archiveName), logs)
}

func writeMonthlySimulationLogFile(path string, logs []MonthlySimulationLogRecord) error {
	if err := writeJSONFile(path, logs); err != nil {
		return fmt.Errorf("写入月度模拟日志失败: %w", err)
	}
	_, err := compactJSONByRecordedAt(path, time.Now().UTC(), reportCSVRetention, reportCSVMaxFileSizeBytes)
	return err
}

func newHistoricalBacktestClient(secretPath string, timeout time.Duration, ncfg NetworkConfig, hcfg historicalConfig) (*BinanceClient, error) {
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	cache, cacheErr := newHistoricalPayloadCache(hcfg.CacheDir)
	if cacheErr != nil {
		return nil, cacheErr
	}
	secret, err := LoadSecretConfig(secretPath)
	if err == nil {
		httpClient, clientErr := NewHTTPClient(ncfg, secret.ProxyURL)
		if clientErr == nil {
			client := &BinanceClient{
				spotBaseURL:                      binanceSpotBaseURL,
				futuresBaseURL:                   binanceFuturesBaseURL,
				apiKey:                           secret.APIKey,
				secretKey:                        secret.Secret,
				proxyURL:                         secret.ProxyURL,
				httpClient:                       httpClient,
				historicalFundingRetryMax:        hcfg.FundingRetryMax,
				historicalPayloadCache:           cache,
				historicalReferenceKlineLookback: hcfg.ReferenceKlineLookback,
				historicalEstimateKlineLookback:  hcfg.EstimateKlineLookback,
				historicalFundingFetchSlot:       hcfg.FundingFetchSlot,
			}
			client.applyNetworkConfig(ncfg)
			return client, nil
		}
	}
	client, clientErr := NewBinanceClientWithNetworkConfig(ncfg)
	if clientErr != nil {
		client = NewBinanceClient(timeout)
		client.applyNetworkConfig(ncfg)
	}
	client.historicalFundingRetryMax = hcfg.FundingRetryMax
	client.historicalPayloadCache = cache
	client.historicalReferenceKlineLookback = hcfg.ReferenceKlineLookback
	client.historicalEstimateKlineLookback = hcfg.EstimateKlineLookback
	client.historicalFundingFetchSlot = hcfg.FundingFetchSlot
	return client, nil
}

func classifyMonthlyFetchError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if statusCode := extractHTTPStatusCode(text); statusCode > 0 {
		switch statusCode {
		case http.StatusAccepted:
			return "http_202"
		case http.StatusForbidden:
			return "http_403"
		case http.StatusTeapot:
			return "http_418"
		case http.StatusTooManyRequests:
			return "http_429"
		default:
			if statusCode >= 400 && statusCode < 600 {
				return fmt.Sprintf("http_%d", statusCode)
			}
		}
	}
	switch {
	case strings.Contains(text, "429"):
		return "http_429"
	case strings.Contains(text, "202"):
		return "http_202"
	case strings.Contains(text, "403"):
		return "http_403"
	case strings.Contains(text, "418"):
		return "http_418"
	case strings.Contains(text, "context canceled"):
		return "context_canceled"
	case strings.Contains(text, "deadline exceeded"):
		return "deadline_exceeded"
	case strings.Contains(text, "service unavailable"):
		return "service_unavailable"
	case strings.Contains(text, "bad gateway"):
		return "bad_gateway"
	case strings.Contains(text, "gateway timeout"):
		return "gateway_timeout"
	case strings.Contains(text, "解析"):
		return "decode_error"
	default:
		text = strings.ReplaceAll(text, "|", "/")
		if len(text) > 48 {
			text = text[:48]
		}
		return text
	}
}

func extractHTTPStatusCode(text string) int {
	prefixes := []string{"http 状态异常: ", "状态异常: "}
	for _, prefix := range prefixes {
		idx := strings.Index(text, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		if start >= len(text) {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(text[start:], "%d", &code); err == nil && code >= 100 && code <= 599 {
			return code
		}
	}
	return 0
}

func monthlyFundingEscalatedPause(reasonCounts map[string]int) time.Duration {
	switch {
	case reasonCounts["http_418"] > 0:
		return monthlyFundingPause418
	case reasonCounts["http_403"] > 0:
		return monthlyFundingPause403
	case reasonCounts["http_429"] > 0:
		return monthlyFundingPause429
	case reasonCounts["http_202"] > 0:
		return monthlyFundingPause202
	}
	other4xxCount := 0
	for reason, count := range reasonCounts {
		if strings.HasPrefix(reason, "http_4") {
			other4xxCount += count
		}
	}
	if other4xxCount > 0 {
		return monthlyFundingPauseOther4xx
	}
	return 0
}

func adaptiveMonthlyFundingBatchPause(reasonCounts map[string]int, batchSize int) time.Duration {
	if len(reasonCounts) == 0 {
		return 0
	}
	pause := monthlyFundingEscalatedPause(reasonCounts)
	if pause <= 0 {
		return 0
	}
	if batchSize <= 0 || len(reasonCounts) == 0 {
		return pause
	}
	pressure := float64(reasonCounts["http_202"]) +
		float64(reasonCounts["http_429"])*2 +
		float64(reasonCounts["http_403"])*3 +
		float64(reasonCounts["http_418"])*4
	if pressure <= 0 {
		return pause
	}
	multiplier := 1.0 + math.Min(3.0, pressure/float64(batchSize))
	adjusted := time.Duration(float64(pause) * multiplier)
	maxPause := 4 * time.Minute
	if adjusted > maxPause {
		return maxPause
	}
	return adjusted
}

func adaptiveMonthlyFundingFetchConcurrency(base int, current int, batchSize int, reasonCounts map[string]int) int {
	base = maxInt(1, base)
	current = maxInt(1, current)
	if batchSize <= 0 {
		batchSize = monthlyFundingFetchBatchSize
	}
	threshold := maxInt(1, batchSize/2)
	severeCount := reasonCounts["http_418"] + reasonCounts["http_403"] + reasonCounts["http_429"]
	softCount := reasonCounts["http_202"]
	stepDown := maxInt(1, base/4)

	switch {
	case severeCount >= threshold:
		return maxInt(1, current/2)
	case softCount >= threshold:
		return maxInt(1, current-maxInt(1, stepDown/2))
	case severeCount > 0 || softCount > 0:
		return maxInt(1, current-1)
	case current < base:
		return current + 1
	default:
		return current
	}
}

func shouldAbortMonthlyFundingFetch(reasonCounts map[string]int, batchSize int, consecutive202Batches *int, consecutive429Batches *int, consecutive403Batches *int, consecutive418Batches *int) bool {
	if batchSize <= 0 {
		batchSize = monthlyFundingFetchBatchSize
	}
	threshold := maxInt(1, batchSize/2)
	if reasonCounts["http_202"] >= batchSize {
		*consecutive202Batches = *consecutive202Batches + 1
	} else {
		*consecutive202Batches = 0
	}
	if reasonCounts["http_429"] >= threshold {
		*consecutive429Batches = *consecutive429Batches + 1
	} else {
		*consecutive429Batches = 0
	}
	if reasonCounts["http_418"] >= threshold {
		*consecutive418Batches = *consecutive418Batches + 1
	} else {
		*consecutive418Batches = 0
	}
	if reasonCounts["http_403"] >= threshold {
		*consecutive403Batches = *consecutive403Batches + 1
	} else {
		*consecutive403Batches = 0
	}
	return *consecutive202Batches >= monthlyFundingAbort202Batches ||
		*consecutive429Batches >= monthlyFundingAbort429Batches ||
		*consecutive418Batches >= monthlyFundingAbort418Batches ||
		*consecutive403Batches >= monthlyFundingAbort403Batches
}

func isRetryableMonthlyFundingStatus(statusCode int) bool {
	return statusCode == http.StatusAccepted ||
		statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusGatewayTimeout
}

func isRetryableMonthlyFundingError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "tempor") ||
		strings.Contains(text, "reset by peer") ||
		strings.Contains(text, "eof")
}

func monthlyFundingRetryDelay(attempt int, statusCode int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 2 * time.Second
	switch statusCode {
	case http.StatusAccepted:
		base = 4 * time.Second
	case http.StatusTooManyRequests:
		base = 6 * time.Second
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		base = 3 * time.Second
	}
	delay := time.Duration(attempt) * base
	maxDelay := 20 * time.Second
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func adaptiveMonthlyGroupTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 90 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*20*time.Second
	maxTimeout := 3 * time.Minute
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func adaptiveMonthlyCandidateBuildTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 20 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*4*time.Second
	maxTimeout := 40 * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func adaptiveMonthlyCandidateFinalizeTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 20 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*3*time.Second
	maxTimeout := 35 * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func summarizeMonthlyReasonCounts(counts map[string]int, samples map[string][]string) string {
	if len(counts) == 0 {
		return ""
	}
	type entry struct {
		reason string
		count  int
	}
	items := make([]entry, 0, len(counts))
	for reason, count := range counts {
		items = append(items, entry{reason: reason, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].reason < items[j].reason
		}
		return items[i].count > items[j].count
	})
	parts := make([]string, 0, len(items))
	for index, item := range items {
		if index >= 5 {
			break
		}
		part := fmt.Sprintf("%s=%d", item.reason, item.count)
		if len(samples[item.reason]) > 0 {
			part += "[" + strings.Join(samples[item.reason], ",") + "]"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ; ")
}

func (c *BinanceClient) fundingRateHistoryEndpoint(symbol string, startTimeMillis int64, endTimeMillis int64) string {
	return fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&startTime=%d&endTime=%d&limit=1000",
		c.futuresBaseURL,
		url.QueryEscape(symbol),
		startTimeMillis,
		endTimeMillis,
	)
}

func (c *BinanceClient) loadCachedFundingRateHistory(symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, bool, error) {
	endpoint := c.fundingRateHistoryEndpoint(symbol, startTimeMillis, endTimeMillis)
	payload, ok, err := c.loadHistoricalPayload(endpoint)
	if err != nil || !ok {
		return nil, ok, err
	}
	var result []FundingRateHistoryItem
	if decodeErr := json.Unmarshal(payload, &result); decodeErr != nil {
		_ = c.invalidateHistoricalPayload(endpoint)
		return nil, false, nil
	}
	return result, true, nil
}

func (c *BinanceClient) fetchFundingRateHistory(ctx context.Context, symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, error) {
	endpoint := c.fundingRateHistoryEndpoint(symbol, startTimeMillis, endTimeMillis)
	return c.fetchFundingRateHistoryWithRetry(ctx, endpoint)
}

func (c *BinanceClient) fetchFundingRateAt(ctx context.Context, symbol string, fundingTime time.Time) (FundingRateHistoryItem, error) {
	windowStart := fundingTime.Add(-24 * time.Hour).UnixMilli()
	windowEnd := fundingTime.Add(time.Minute).UnixMilli()
	items, err := c.fetchFundingRateHistory(ctx, symbol, windowStart, windowEnd)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}
	for _, item := range items {
		if item.FundingTime == fundingTime.UnixMilli() {
			return item, nil
		}
	}
	for idx := len(items) - 1; idx >= 0; idx-- {
		if items[idx].FundingTime <= fundingTime.UnixMilli() {
			return items[idx], nil
		}
	}
	return FundingRateHistoryItem{}, fmt.Errorf("未找到 funding 结算记录")
}

func (c *BinanceClient) fetchSpotKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKline(ctx, c.spotBaseURL+"/api/v3/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKline(ctx, c.futuresBaseURL+"/fapi/v1/klines", symbol, start)
}

func (c *BinanceClient) fetchSpotKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBefore(ctx, c.spotBaseURL+"/api/v3/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBefore(ctx, c.futuresBaseURL+"/fapi/v1/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesPremiumKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, c.futuresBaseURL+"/fapi/v1/premiumIndexKlines", "symbol", symbol, start, c.historicalEstimateLookback())
}

func (c *BinanceClient) fetchFuturesIndexKlineBefore(ctx context.Context, pair string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, c.futuresBaseURL+"/fapi/v1/indexPriceKlines", "pair", pair, start, c.historicalEstimateLookback())
}

func (c *BinanceClient) fetchKline(ctx context.Context, endpoint string, symbol string, start time.Time) (MarketKline, error) {
	queryURL := fmt.Sprintf("%s?symbol=%s&interval=1m&startTime=%d&limit=1", endpoint, url.QueryEscape(symbol), start.UnixMilli())
	var payload [][]any
	if err := c.getJSONWithMonthlyRetry(ctx, queryURL, &payload); err != nil {
		return MarketKline{}, err
	}
	if len(payload) == 0 {
		return MarketKline{}, fmt.Errorf("未返回 K 线")
	}
	return parseMarketKline(payload[0])
}

func (c *BinanceClient) fetchKlineBefore(ctx context.Context, endpoint string, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, endpoint, "symbol", symbol, start, c.historicalReferenceLookback())
}

func (c *BinanceClient) fetchKlineBeforeWithLimit(ctx context.Context, endpoint string, queryKey string, queryValue string, start time.Time, limit int) (MarketKline, error) {
	if limit <= 0 {
		limit = c.historicalReferenceLookback()
	}
	queryURL := fmt.Sprintf("%s?%s=%s&interval=1m&endTime=%d&limit=%d", endpoint, queryKey, url.QueryEscape(queryValue), start.UnixMilli()-1, limit)
	var payload [][]any
	if err := c.getJSONWithMonthlyRetry(ctx, queryURL, &payload); err != nil {
		return MarketKline{}, err
	}
	if len(payload) == 0 {
		return MarketKline{}, fmt.Errorf("未返回参考 K 线")
	}
	return parseMarketKline(payload[len(payload)-1])
}

func (c *BinanceClient) fetchFundingRateHistoryWithRetry(ctx context.Context, endpoint string) ([]FundingRateHistoryItem, error) {
	if payload, ok, err := c.loadHistoricalPayload(endpoint); err == nil && ok {
		var result []FundingRateHistoryItem
		if decodeErr := json.Unmarshal(payload, &result); decodeErr == nil {
			return result, nil
		}
		_ = c.invalidateHistoricalPayload(endpoint)
	}
	retryLimit := maxInt(0, c.historicalFundingRetryLimit())
	var lastPayload []byte
	lastStatusCode := 0
	var lastErr error
	for attempt := 0; attempt <= retryLimit; attempt++ {
		payload, statusCode, err := c.fetchHTTPPayload(ctx, endpoint)
		if err == nil && statusCode == http.StatusOK {
			var result []FundingRateHistoryItem
			if decodeErr := json.Unmarshal(payload, &result); decodeErr != nil {
				return nil, fmt.Errorf("解析 fundingRate 历史失败: %w", decodeErr)
			}
			_ = c.storeHistoricalPayload(endpoint, payload)
			return result, nil
		}
		lastPayload = payload
		lastStatusCode = statusCode
		if err != nil {
			lastErr = fmt.Errorf("获取 fundingRate 历史失败: 请求失败: %w", err)
		} else {
			lastErr = formatHistoricalHTTPStatusError("获取 fundingRate 历史失败", statusCode, payload)
		}
		if attempt >= retryLimit {
			break
		}
		if statusCode > 0 {
			if !isRetryableMonthlyFundingStatus(statusCode) {
				break
			}
		} else if !isRetryableMonthlyFundingError(err) {
			break
		}
		c.recordHistoricalRetry()
		if !sleepWithContext(ctx, monthlyFundingRetryDelay(attempt+1, statusCode)) {
			return nil, ctx.Err()
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if lastStatusCode != http.StatusOK {
		return nil, formatHistoricalHTTPStatusError("获取 fundingRate 历史失败", lastStatusCode, lastPayload)
	}
	return nil, fmt.Errorf("获取 fundingRate 历史失败: 未知错误")
}

func (c *BinanceClient) fetchHTTPPayload(ctx context.Context, rawURL string) ([]byte, int, error) {
	var lastErr error
	lastStatusCode := 0
	for _, candidateURL := range c.candidateRequestURLs(rawURL) {
		if err := c.waitPublicGETRequestSlot(ctx, candidateURL, c.historicalFundingFetchSlot); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidateURL, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("创建请求失败: %w", err)
		}
		c.applyPublicGETHeaders(req, candidateURL)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("读取响应失败: %w", readErr)
			lastStatusCode = resp.StatusCode
			continue
		}
		lastStatusCode = resp.StatusCode
		c.notePublicGETResponseStatus(resp.StatusCode)
		if resp.StatusCode == http.StatusOK {
			return body, resp.StatusCode, nil
		}
		lastErr = newPublicGETHTTPError(candidateURL, resp.StatusCode, resp.Status, body)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知请求失败")
	}
	return nil, lastStatusCode, lastErr
}

func (c *BinanceClient) getJSONWithMonthlyRetry(ctx context.Context, rawURL string, target any) error {
	if payload, ok, err := c.loadHistoricalPayload(rawURL); err == nil && ok {
		if decodeErr := json.Unmarshal(payload, target); decodeErr == nil {
			return nil
		}
		_ = c.invalidateHistoricalPayload(rawURL)
	}
	payload, statusCode, err := c.fetchHTTPPayload(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	if statusCode != http.StatusOK {
		return formatHistoricalHTTPStatusError("HTTP 状态异常", statusCode, payload)
	}
	if decodeErr := json.Unmarshal(payload, target); decodeErr != nil {
		return fmt.Errorf("解析 JSON 失败: %w", decodeErr)
	}
	_ = c.storeHistoricalPayload(rawURL, payload)
	return nil
}

func formatHistoricalHTTPStatusError(prefix string, statusCode int, payload []byte) error {
	message := fmt.Sprintf("%s: %d %s", prefix, statusCode, http.StatusText(statusCode))
	snippet := strings.TrimSpace(string(payload))
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	if snippet != "" {
		message += " body=" + snippet
	}
	return fmt.Errorf("%s", message)
}

func (c *BinanceClient) historicalReferenceLookback() int {
	if c.historicalReferenceKlineLookback > 0 {
		return c.historicalReferenceKlineLookback
	}
	return DefaultEngineConfig().HistoricalConfig.ReferenceKlineLookback
}

func (c *BinanceClient) historicalEstimateLookback() int {
	if c.historicalEstimateKlineLookback > 0 {
		return c.historicalEstimateKlineLookback
	}
	return DefaultEngineConfig().HistoricalConfig.EstimateKlineLookback
}

func (c *BinanceClient) historicalFundingRetryLimit() int {
	if c.historicalFundingRetryMax > 0 {
		return c.historicalFundingRetryMax
	}
	return DefaultEngineConfig().HistoricalConfig.FundingRetryMax
}

func (c *BinanceClient) loadHistoricalPayload(rawURL string) ([]byte, bool, error) {
	if c == nil || c.historicalPayloadCache == nil {
		return nil, false, nil
	}
	payload, ok, err := c.historicalPayloadCache.Load(rawURL)
	if err == nil {
		c.recordHistoricalCacheLookup(ok)
	}
	return payload, ok, err
}

func (c *BinanceClient) storeHistoricalPayload(rawURL string, payload []byte) error {
	if c == nil || c.historicalPayloadCache == nil {
		return nil
	}
	return c.historicalPayloadCache.Save(rawURL, payload)
}

func (c *BinanceClient) invalidateHistoricalPayload(rawURL string) error {
	if c == nil || c.historicalPayloadCache == nil {
		return nil
	}
	return c.historicalPayloadCache.Remove(rawURL)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func parseMarketKline(row []any) (MarketKline, error) {
	if len(row) < 8 {
		return MarketKline{}, fmt.Errorf("K 线字段不足")
	}
	return MarketKline{
		OpenTime:         parseAnyInt64(row[0]),
		Open:             parseAnyFloat(row[1]),
		High:             parseAnyFloat(row[2]),
		Low:              parseAnyFloat(row[3]),
		Close:            parseAnyFloat(row[4]),
		QuoteAssetVolume: parseAnyFloat(row[7]),
	}, nil
}

func (c *monthlyKlineCache) getSpotKline(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.spot[key]; ok {
		c.hits["spot_exec"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["spot_exec"]++
	c.mu.Unlock()

	kline, err := client.fetchSpotKline(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.spot[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesKline(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.futures[key]; ok {
		c.hits["futures_exec"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["futures_exec"]++
	c.mu.Unlock()

	kline, err := client.fetchFuturesKline(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.futures[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getSpotKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.spot[key]; ok {
		c.hits["spot_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["spot_before"]++
	c.mu.Unlock()

	kline, err := client.fetchSpotKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.spot[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.futures[key]; ok {
		c.hits["futures_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["futures_before"]++
	c.mu.Unlock()

	kline, err := client.fetchFuturesKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.futures[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesPremiumKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:premium:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.premium[key]; ok {
		c.hits["premium_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["premium_before"]++
	c.mu.Unlock()

	kline, err := client.fetchFuturesPremiumKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.premium[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesIndexKlineBefore(ctx context.Context, client *BinanceClient, pair string, start time.Time) (MarketKline, error) {
	key := "before:index:" + pair + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.index[key]; ok {
		c.hits["index_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["index_before"]++
	c.mu.Unlock()

	kline, err := client.fetchFuturesIndexKlineBefore(ctx, pair, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.index[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFundingRateAt(ctx context.Context, client *BinanceClient, symbol string, fundingTime time.Time) (FundingRateHistoryItem, error) {
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	key := normalizedSymbol + ":" + strconv.FormatInt(fundingTime.UnixMilli(), 10)
	c.mu.Lock()
	if item, ok := c.funding[key]; ok {
		c.hits["funding_actual"]++
		c.mu.Unlock()
		return item, nil
	}
	c.loads["funding_actual"]++
	c.mu.Unlock()
	if series := c.fundingHistory(normalizedSymbol); len(series) > 0 {
		targetMillis := fundingTime.UnixMilli()
		index := sort.Search(len(series), func(i int) bool {
			return series[i].FundingTime >= targetMillis
		})
		if index < len(series) && series[index].FundingTime == targetMillis {
			c.mu.Lock()
			c.funding[key] = series[index]
			c.hits["funding_actual"]++
			c.mu.Unlock()
			return series[index], nil
		}
		if index > 0 {
			c.mu.Lock()
			c.funding[key] = series[index-1]
			c.hits["funding_actual"]++
			c.mu.Unlock()
			return series[index-1], nil
		}
	}

	item, err := client.fetchFundingRateAt(ctx, normalizedSymbol, fundingTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}

	c.mu.Lock()
	c.funding[key] = item
	c.mu.Unlock()
	return item, nil
}

func (c *monthlyKlineCache) summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := []string{"premium_before", "index_before", "spot_before", "futures_before", "spot_exec", "futures_exec", "funding_actual"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		loads := c.loads[key]
		hits := c.hits[key]
		if loads == 0 && hits == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(loads=%d,hits=%d)", key, loads, hits))
	}
	return strings.Join(parts, ";")
}

func computeBasisRate(spotPrice float64, futuresPrice float64) float64 {
	if spotPrice <= 0 {
		return 0
	}
	return (spotPrice - futuresPrice) / spotPrice
}

func computeAdverseBasisPenalty(openBasisRate float64, spotEntryNotional float64, futuresEntryNotional float64) float64 {
	if openBasisRate >= 0 {
		return openBasisRate * math.Max(spotEntryNotional, futuresEntryNotional)
	}
	return 0
}

func ratioOrZero(numerator float64, denominator float64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func computeDrawdownRate(peakEquity float64, equity float64) float64 {
	if peakEquity <= 0 || equity >= peakEquity {
		return 0
	}
	return (peakEquity - equity) / peakEquity
}

func estimateHistoricalSlipRate(kline MarketKline) float64 {
	if kline.Open <= 0 {
		return 0.0003
	}
	rangeRate := math.Abs(kline.High-kline.Low) / kline.Open
	return math.Max(0.0003, rangeRate*0.10)
}

func estimateMarketImpactRate(notionalUSDT float64, quoteVolume float64) float64 {
	if notionalUSDT <= 0 || quoteVolume <= 0 {
		return 0.0025
	}
	impact := (notionalUSDT / quoteVolume) * 0.50
	return math.Max(0.0001, math.Min(0.0025, impact))
}

func conservativeHistoricalFundingRate(premiumIndexRate float64, markIndexBasisRate float64) float64 {
	const (
		singleSourceHaircut     = 0.10
		confirmedBaseHaircut    = 0.10
		confirmedAgreementBonus = 0.10
		minAgreementFloor       = 0.35
	)
	premiumSign := 0.0
	if premiumIndexRate > 0 {
		premiumSign = 1
	} else if premiumIndexRate < 0 {
		premiumSign = -1
	}
	basisSign := 0.0
	if markIndexBasisRate > 0 {
		basisSign = 1
	} else if markIndexBasisRate < 0 {
		basisSign = -1
	}
	absPremium := math.Abs(premiumIndexRate)
	absBasis := math.Abs(markIndexBasisRate)
	if absPremium <= 0 && absBasis <= 0 {
		return 0
	}
	switch {
	case absPremium > 0 && absBasis > 0 && premiumSign != 0 && basisSign != 0 && premiumSign != basisSign:
		return 0
	case absPremium > 0 && absBasis <= 0:
		return premiumSign * absPremium * singleSourceHaircut
	case absBasis > 0 && absPremium <= 0:
		return basisSign * absBasis * singleSourceHaircut
	}
	sign := premiumSign
	if sign == 0 {
		sign = basisSign
	}
	lower := math.Min(absPremium, absBasis)
	upper := math.Max(absPremium, absBasis)
	if lower <= 0 || upper <= 0 {
		return 0
	}
	agreement := lower / upper
	if agreement < minAgreementFloor {
		return sign * lower * singleSourceHaircut
	}
	haircut := confirmedBaseHaircut + confirmedAgreementBonus*agreement
	return sign * lower * haircut
}

func parseAnyFloat(value any) float64 {
	switch typed := value.(type) {
	case string:
		return parseFloat(typed)
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	default:
		return parseFloat(fmt.Sprint(value))
	}
}

func parseAnyInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return parsed
	}
}
