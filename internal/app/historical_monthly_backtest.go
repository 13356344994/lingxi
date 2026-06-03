package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/exchange"
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
// FundingRateHistoryItem / MarketKline 已迁移到 internal/exchange（市场/历史数据类型）。
// 保留同名别名，让回测与抓取代码无需改动即可继续编译。
type (
	FundingRateHistoryItem = exchange.FundingRateHistoryItem
	MarketKline            = exchange.MarketKline
)

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
	ResidualFillHaircut     float64
	EstimatedNetPnL         float64
	RealizedNetPnL          float64
	Reason                  string
	Notes                   []string
}

type monthlyEventGroup struct {
	FundingTime time.Time
	RawCount    int
	Items       []FundingRateHistoryItem
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
	ResidualFillHaircutEnabled             bool
	ExpectedLegFillRatio                   float64
	LegRejectProbability                   float64
	EmergencyHedgeSpreadMultiplier         float64
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
		if pair := client.HistoricalSpotPairForFuture(symbol); strings.TrimSpace(pair) != "" {
			spotPairs = append(spotPairs, pair)
		}
		if meta, ok := client.HistoricalSpotMetaForFuture(symbol); ok && strings.TrimSpace(meta.BaseAsset) != "" {
			borrowAssets = append(borrowAssets, meta.BaseAsset)
		}
	}
	if profile, profileErr := client.ResolveAccountTradingCostProfile(ctx, spotPairs, symbols, borrowAssets); profileErr != nil {
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

	fetchStats.RequestSummary = client.HistoricalRequestSummary()
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
