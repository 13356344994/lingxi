package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// cross_venue_scanner.go 实现 P1 跨所资金费价差扫描器(纯只读,绝不下单):
// 周期性拉取四所全市场永续资金费快照,按 canonical symbol 对齐,计算跨所年化价差,
// 暴露到面板 API + 日志 + Telegram 告警。

const (
	crossVenueAlertCooldown   = 4 * time.Hour // 同一币-所对告警冷却,防刷屏
	crossVenueDefaultFetchTO  = 45 * time.Second
	crossVenueOKXFetchTO      = 120 * time.Second // okx 逐币拉+全局 pacing,全量约 20–30s,留余量
	crossVenueBorrowProbeGap  = 200 * time.Millisecond
	crossVenueBorrowProbesLog = "borrow_probes.jsonl"
	// crossVenueSpreadSanityCap 跨所年化价差绝对荒谬上限(小数,50.0=5000%);仅挡明显快照错误/伪值。
	crossVenueSpreadSanityCap = 50.0
	// crossVenueExtremeSpreadMarkAPR 超过此阈值标记 ExtremeSpread(审计),不再整条丢弃。
	crossVenueExtremeSpreadMarkAPR = 2.0
	// crossVenueTradableInstrumentsCacheTTL instrument 缓存 TTL;与默认扫描周期对齐,避免一轮内重复拉。
	crossVenueTradableInstrumentsCacheTTL = 10 * time.Minute
)

// crossVenueNames 扫描目标所(固定四所)。
var crossVenueNames = []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName}

// crossVenueScannerFactory 可注入(单测替换 fake,避免触网)。
var crossVenueScannerFactory = buildCrossVenueScannerAdapters

// symbolVenueFunding 是某 symbol 在某所的一条资金费记录(含年化)。
type symbolVenueFunding struct {
	Venue                   string
	CanonicalSymbol         string  // venue-canonical 原始 symbol(下单/查 funding 用)
	EconomicPairKey         string  // 经济配对键,如 PEPE-USDT(跨 1000PEPE↔PEPE 对齐)
	EconomicBase            string  // 经济 base,如 PEPE
	Multiplier              float64 // 计价倍数(1/1000/10000/1000000)
	MultKnown               bool    // false 时仅允许同 canonical 配对,禁止跨计价
	FundingRate             float64
	RawFundingRate          float64 // 快照原始费率(EV 重算用,未经平滑/封顶)
	AnnualizedAPR           float64 // 单腿封顶后的年化(仅作 sanity 展示;配对/价差不再用它)
	RawAnnualizedAPR        float64 // 原始(未封顶)年化;bestPairSpread 用它算真实价差,避免多腿同被封顶后价差被抹平
	IntervalHours           int
	NextFundingTime         time.Time
	NextFundingTimeInferred bool // true 表示 NextFundingTime 由 now+interval 兜底推算
}

// crossVenuePerpPerpDiagWatchSymbols 跨所 perp-perp 观察名单(base 或 canonical symbol);每轮最多一条 [perp-perp-diag]。
var crossVenuePerpPerpDiagWatchSymbols = []string{"H-USDT", "BTT", "PEPE"}

func crossVenuePerpPerpDiagWatched(symbol string) bool {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return false
	}
	for _, w := range crossVenuePerpPerpDiagWatchSymbols {
		if symbol == w || strings.HasPrefix(symbol, w+"-") {
			return true
		}
	}
	return false
}

// crossVenueResolveNextFundingTime 解析下次结算时刻;快照缺 NextFundingTime 时用 now+interval 兜底(仅索引/EV 估算,开仓闸仍拉 live 快照校验)。
func crossVenueResolveNextFundingTime(now time.Time, next time.Time, intervalHours int) (resolved time.Time, inferred bool) {
	if intervalHours <= 0 {
		intervalHours = 8
	}
	if !next.IsZero() {
		return next, false
	}
	return now.Add(time.Duration(intervalHours) * time.Hour), true
}

// CrossVenueOpportunity 是一条跨所资金费价差机会(策略:空高费所、多低费所)。
type CrossVenueOpportunity struct {
	Symbol                string    `json:"symbol"`
	HighSymbol            string    `json:"high_symbol,omitempty"` // 高费腿 venue-canonical symbol;空则同 Symbol
	LowSymbol             string    `json:"low_symbol,omitempty"`  // 低费腿 venue-canonical symbol;空则同 Symbol
	HighMult              float64   `json:"high_mult,omitempty"`   // 高费腿计价倍数(护栏/归一 mid 用)
	LowMult               float64   `json:"low_mult,omitempty"`    // 低费腿计价倍数
	HighVenue             string    `json:"high_venue"`
	LowVenue              string    `json:"low_venue"`
	SnapshotID            string    `json:"snapshot_id,omitempty"`
	SnapshotTime          time.Time `json:"snapshot_time,omitempty"`
	FreshnessAgeSeconds   float64   `json:"freshness_age_seconds,omitempty"`
	HighAnnualizedAPR     float64   `json:"high_annualized_apr"`
	LowAnnualizedAPR      float64   `json:"low_annualized_apr"`
	SpreadAPR             float64   `json:"spread_apr"`
	ComparableWindowHours float64   `json:"comparable_window_hours,omitempty"`
	ExpectedHoldHours     float64   `json:"expected_hold_hours,omitempty"`
	SpreadEV              float64   `json:"spread_ev,omitempty"`
	OpenCost              float64   `json:"open_cost,omitempty"`
	CloseCost             float64   `json:"close_cost,omitempty"`
	ExpectedSlippage      float64   `json:"expected_slippage,omitempty"`
	ObservableRiskCost    float64   `json:"observable_risk_cost,omitempty"`
	PolicyBuffer          float64   `json:"policy_buffer,omitempty"`
	NetEV                 float64   `json:"net_ev,omitempty"`
	CapitalEfficiencyEV   float64   `json:"capital_efficiency_ev,omitempty"`
	RiskAdjustedEVPerHour float64   `json:"risk_adjusted_ev_per_hour,omitempty"`
	HighFundingRate       float64   `json:"high_funding_rate"` // 平滑+封顶后的单次结算费率(EV/排序口径)
	LowFundingRate        float64   `json:"low_funding_rate"`
	HighIntervalHours     int       `json:"high_interval_hours"`
	LowIntervalHours      int       `json:"low_interval_hours"`
	HighNextFundingTime   time.Time `json:"high_next_funding_time"`
	LowNextFundingTime    time.Time `json:"low_next_funding_time"`
	HistoryQualityScore   float64   `json:"history_quality_score,omitempty"`
	LiquidityScore        float64   `json:"liquidity_score,omitempty"`
	StabilityScore        float64   `json:"stability_score,omitempty"`
	ExecutionScore        float64   `json:"execution_score,omitempty"`
	RecoveryQualityScore  float64   `json:"recovery_quality_score,omitempty"`
	RankScore             float64   `json:"rank_score,omitempty"`
	SizeMultiplier        float64   `json:"size_multiplier,omitempty"`
	TradableNotional      float64   `json:"tradable_notional,omitempty"`
	TradableQty           float64   `json:"tradable_qty,omitempty"`
	NoTradeReason         string    `json:"no_trade_reason,omitempty"`
	// ExtremeSpread 为 true 表示 spread 超过常规 sanity 阈值但保留(真实极端 carry,如 H-USDT)。
	ExtremeSpread bool `json:"extreme_spread,omitempty"`
}

// CrossVenueVenueStatus 记录单所本轮拉取结果。
type CrossVenueVenueStatus struct {
	Venue         string `json:"venue"`
	OK            bool   `json:"ok"`
	SymbolCount   int    `json:"symbol_count"`
	ElapsedMillis int64  `json:"elapsed_ms"`
	Error         string `json:"error,omitempty"`
}

// CrossVenueFundingEntry 是某所某 symbol 的一条费率明细(供执行引擎离线查询,免现场拉全量快照)。
type CrossVenueFundingEntry struct {
	Rate                  float64   `json:"rate"`
	IntervalHours         int       `json:"interval_hours"`
	NextFundingTime       time.Time `json:"next_funding_time"`
	SpikeThreshold        float64   `json:"spike_threshold,omitempty"`
	FundingHistorySamples int       `json:"funding_history_samples,omitempty"`
}

// CrossVenueBorrowProbe 是某 symbol 在某所的一条借币可得性探测结果(只读遥测)。
type CrossVenueBorrowProbe struct {
	Symbol        string  `json:"symbol"`
	Venue         string  `json:"venue"`
	Available     bool    `json:"available"`
	MaxBorrowable float64 `json:"max_borrowable"`
	HourlyRateAPR float64 `json:"hourly_rate_apr"` // 由小时利率 ×8760 换算的年化
	Err           string  `json:"error,omitempty"`
}

// CrossVenueScanSnapshot 是一轮扫描的内存快照(供 HTTP 端点读取)。
type CrossVenueScanSnapshot struct {
	SnapshotID       string                  `json:"snapshot_id,omitempty"`
	ScannedAt        time.Time               `json:"scanned_at"`
	VenueStatuses    []CrossVenueVenueStatus `json:"venue_statuses"`
	Opportunities    []CrossVenueOpportunity `json:"opportunities"`
	MinSpreadAPR     float64                 `json:"min_spread_apr"`
	OpportunityCount int                     `json:"opportunity_count"`
	BorrowProbes     []CrossVenueBorrowProbe `json:"borrow_probes,omitempty"`
	// FundingByVenue 是每所每 symbol 的原始费率明细缓存(venue → symbol → entry)。
	// 执行引擎的持仓管理(平仓判断/资金费记账)从这里查,绝不现场拉全量快照(OKX 一次 20–46s 会拖死主循环)。
	// 每轮扫描整体重建后原子替换,发布后只读 → 并发安全;不序列化到 API(体积大且面板用不到)。
	FundingByVenue map[string]map[string]CrossVenueFundingEntry `json:"-"`
}

// CrossVenueScanner 周期性扫描跨所资金费价差(纯只读)。
type CrossVenueScanner struct {
	cfg       CrossVenueScannerConfig
	execCfg   CrossVenueExecutorConfig
	recordDir string
	venues    map[string]venue.Venue
	logger    *log.Logger
	alerts    *AlertDispatcher

	mu           sync.RWMutex
	lastSnapshot CrossVenueScanSnapshot

	borrowProbeCache crossVenueBorrowProbeCache
	borrowKnown      *BorrowKnownAvailableRegistry

	alertMu    sync.Mutex
	lastAlert  map[string]time.Time
	nowFn      func() time.Time
	sleepFn    func(time.Duration)
	fetchVenue func(ctx context.Context, name string, adp venue.Venue) ([]venue.FundingSnapshot, error)

	// fundingHistory 滚动费率历史 venue → symbol → 最近若干次原始 LastFundingRate(并发:scanOnce 写,compute 读)。
	fundingHistoryMu sync.Mutex
	fundingHistory   map[string]map[string][]float64

	// tradablePerp* 各所可交易永续 symbol 缓存(幽灵资费腿过滤用);FetchInstruments 失败时 fail-open 跳过该所。
	tradablePerpMu        sync.Mutex
	tradablePerpFetchedAt time.Time
	tradablePerpSymbols   map[string]map[string]struct{} // venue → canonical symbol set
	tradablePerpSkip      map[string]bool                // venue → true 表示 instrument 不可用,不过滤
}

// crossVenueBorrowKnownFreshness 返回借币 known-available 记忆窗口(来自 scanner 配置)。
func crossVenueBorrowKnownFreshness(cfg CrossVenueScannerConfig) time.Duration {
	h := cfg.BorrowKnownFreshnessHours
	if h <= 0 {
		h = 24
	}
	return time.Duration(h * float64(time.Hour))
}

// crossVenueBorrowDefaultDailyRateWhenUnknown 返回缺利率样本时的保守借币日利率(EV 口径)。
func crossVenueBorrowDefaultDailyRateWhenUnknown(cfg CrossVenueScannerConfig) float64 {
	if cfg.BorrowDefaultDailyRateWhenUnknown > 0 {
		return cfg.BorrowDefaultDailyRateWhenUnknown
	}
	return 0.001
}

// BorrowKnownRegistry 返回 accumulated known-borrowable 集(线程安全只读 consult)。
func (s *CrossVenueScanner) BorrowKnownRegistry() *BorrowKnownAvailableRegistry {
	if s == nil {
		return nil
	}
	return s.borrowKnown
}

// NewCrossVenueScanner 构造扫描器;venues 为空 map 时按 orch/secret 自建四所只读适配器。
func NewCrossVenueScanner(cfg EngineConfig, orch *Orchestrator, secretPath string, logger *log.Logger) *CrossVenueScanner {
	if logger == nil {
		logger = log.Default()
	}
	venues := crossVenueScannerFactory(orch, cfg, secretPath, logger)
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if recordDir == "" {
		recordDir = "records"
	}
	borrowKnown := NewBorrowKnownAvailableRegistry(crossVenueBorrowKnownFreshness(cfg.CrossVenueScanner))
	if err := borrowKnown.SeedFromBorrowProbesJSONL(recordDir, time.Now().UTC()); err != nil && logger != nil {
		logger.Printf("[借币记忆] 暖启动 borrow_probes.jsonl 失败(将仅从 live 探测累积): %v", err)
	} else if logger != nil && borrowKnown.CountFresh(time.Now().UTC()) > 0 {
		logger.Printf("[借币记忆] 暖启动 %d 条 venue+asset confirmed-available (freshness=%v)",
			borrowKnown.CountFresh(time.Now().UTC()), borrowKnown.Freshness())
	}
	return &CrossVenueScanner{
		cfg:            cfg.CrossVenueScanner,
		execCfg:        cfg.CrossVenueExecutor,
		recordDir:      recordDir,
		venues:         venues,
		logger:         logger,
		alerts:         newCrossVenueAlertDispatcher(cfg.CrossVenueScanner, secretPath, logger),
		lastAlert:      make(map[string]time.Time),
		nowFn:          time.Now,
		sleepFn:        time.Sleep,
		fetchVenue:     defaultFetchVenueSnapshot,
		fundingHistory: make(map[string]map[string][]float64),
		borrowKnown:    borrowKnown,
	}
}

// newCrossVenueAlertDispatcher 复用 secret.yaml Telegram 凭证构造告警分发器(与 bybit/面板同源)。
func newCrossVenueAlertDispatcher(cfg CrossVenueScannerConfig, secretPath string, logger *log.Logger) *AlertDispatcher {
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	token, chatID := loadAlertingSecretsOrEmpty(secretPath)
	if token == "" || chatID == "" {
		return nil
	}
	ac := AlertingConfig{
		Enabled:          true,
		TelegramBotToken: token,
		TelegramChatID:   chatID,
		MinSeverity:      "warning",
		DedupWindow:      0, // 扫描器自管 4h 冷却,不用 dispatcher 去重
		DispatchTimeout:  5 * time.Second,
	}
	_ = cfg // 预留:未来可按 cross_venue_scanner 独立告警通道扩展
	return newAlertDispatcher(ac, logger)
}

// buildCrossVenueScannerAdapters 为四所构造只读 venue.Venue;okx/gate 优先复用 orch 已注册适配器。
func buildCrossVenueScannerAdapters(orch *Orchestrator, cfg EngineConfig, secretPath string, logger *log.Logger) map[string]venue.Venue {
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	out := make(map[string]venue.Venue, len(crossVenueNames))
	for _, name := range crossVenueNames {
		if adp := crossVenueAdapterFromOrchestrator(orch, name); adp != nil {
			out[name] = adp
			continue
		}
		adp, err := buildCrossVenueReadOnlyAdapter(name, cfg, secretPath)
		if err != nil {
			if logger != nil {
				logger.Printf("[跨所扫描] venue=%s 适配器构造失败,该所本轮将跳过: %v", name, err)
			}
			continue
		}
		out[name] = adp
	}
	return out
}

func crossVenueAdapterFromOrchestrator(orch *Orchestrator, venueName string) venue.Venue {
	if orch == nil {
		return nil
	}
	rt, ok := orch.Get(venueName)
	if !ok || rt == nil || rt.venueAdapter == nil {
		return nil
	}
	return rt.venueAdapter
}

// buildCrossVenueReadOnlyAdapter 按 venue 名加载凭证并构造只读适配器;凭证缺失则返回 error(调用方降级跳过)。
func buildCrossVenueReadOnlyAdapter(venueName string, cfg EngineConfig, secretPath string) (venue.Venue, error) {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	switch venueName {
	case binance.VenueName:
		client, err := NewBinanceClientWithNetworkConfig(cfg.NetworkConfig)
		if err != nil {
			return nil, err
		}
		if sec, secErr := LoadVenueSecretConfig(secretPath, binance.VenueName); secErr == nil {
			if authClient, authErr := NewAuthenticatedBinanceClient(sec, cfg.NetworkConfig); authErr == nil {
				client = authClient
			}
		}
		return binance.New(client), nil
	case bybit.VenueName:
		sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
		if err != nil {
			return nil, err
		}
		return bybit.New(venue.Config{
			Name:      bybit.VenueName,
			APIKey:    sec.APIKey,
			APISecret: sec.Secret,
			ProxyURL:  sec.ProxyURL,
		})
	case okx.VenueName, gate.VenueName:
		return buildRegisteredVenueAdapter(venueName, secretPath)
	default:
		return nil, fmt.Errorf("venue %q 未登记跨所扫描适配器", venueName)
	}
}

// Run 启动周期性扫描;ctx 取消即退出。首轮立即执行一次。
func (s *CrossVenueScanner) Run(ctx context.Context) {
	if s == nil || !s.cfg.Enabled {
		return
	}
	interval := time.Duration(s.cfg.ScanIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	s.logger.Printf("[跨所扫描] 已启动: interval=%s min_spread=%.0f%% alert_spread=%.0f%% top_n=%d venues=%d",
		interval, s.cfg.MinSpreadAPR*100, s.cfg.AlertSpreadAPR*100, s.cfg.TopN, len(s.venues))

	// 热加载:首轮全量扫描(~30min)完成前,先载入磁盘上最近一轮完整快照,让轮动立即有真实数据可决策。
	s.warmStartFromDisk()
	s.scanOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Printf("[跨所扫描] 已停止")
			return
		case <-ticker.C:
			s.scanOnce(ctx)
		}
	}
}

// Snapshot 返回最新一轮扫描结果(线程安全)。
func (s *CrossVenueScanner) Snapshot() CrossVenueScanSnapshot {
	if s == nil {
		return CrossVenueScanSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSnapshot
}

func (s *CrossVenueScanner) scanOnce(ctx context.Context) {
	start := s.nowFn()
	snapshots, statuses := s.fetchAllVenues(ctx)
	snapshots = s.filterGhostFundingSnapshots(ctx, snapshots)
	s.recordFundingHistory(snapshots)
	opps := s.computeCrossVenueOpportunities(ctx, snapshots)
	snapshotID := crossVenueSnapshotID(start)
	for i := range opps {
		opps[i].SnapshotID = snapshotID
		opps[i].SnapshotTime = start
		opps[i].FreshnessAgeSeconds = crossVenueSnapshotAgeSeconds(start, start)
	}
	fundingByVenue := buildCrossVenueFundingIndex(snapshots)
	for venueName, bySym := range fundingByVenue {
		for sym, entry := range bySym {
			entry.SpikeThreshold = s.fundingSpikeThreshold(venueName, sym)
			entry.FundingHistorySamples = s.fundingHistorySampleCount(venueName, sym)
			bySym[sym] = entry
		}
	}
	snapshot := CrossVenueScanSnapshot{
		SnapshotID:       snapshotID,
		ScannedAt:        start,
		VenueStatuses:    statuses,
		Opportunities:    opps,
		MinSpreadAPR:     s.cfg.MinSpreadAPR,
		OpportunityCount: len(opps),
		FundingByVenue:   fundingByVenue,
	}
	s.mu.Lock()
	s.lastSnapshot = snapshot
	s.mu.Unlock()
	s.appendScanSnapshot(snapshot)
	// 落一份含 FundingByVenue 的"最新完整快照"单文件(覆盖写),供下次进程启动热加载,免等首轮全量扫描。
	if err := crossVenuePersistLatestSnapshot(s.recordDir, snapshot); err != nil && s.logger != nil {
		s.logger.Printf("[跨所扫描] 最新快照落盘失败(热加载用): %v", err)
	}

	okCount := 0
	for _, st := range statuses {
		if st.OK {
			okCount++
		}
	}
	s.logger.Printf("[跨所扫描] 完成: venues_ok=%d/%d opportunities=%d elapsed=%s",
		okCount, len(statuses), len(opps), s.nowFn().Sub(start).Round(time.Millisecond))
	for _, opp := range opps {
		if opp.SpreadAPR >= s.cfg.MinSpreadAPR {
			s.logger.Printf("[跨所扫描] 机会 %s spread=%.1f%% high=%s(%.1f%%) low=%s(%.1f%%)",
				opp.Symbol, opp.SpreadAPR*100, opp.HighVenue, opp.HighAnnualizedAPR*100,
				opp.LowVenue, opp.LowAnnualizedAPR*100)
		}
	}
	for _, opp := range opps {
		s.maybeAlert(opp)
	}
	if s.cfg.BorrowProbeEnabled {
		probes := s.probeBorrowForOpportunities(ctx, start, opps, fundingByVenue)
		s.mu.Lock()
		s.lastSnapshot.BorrowProbes = probes
		s.mu.Unlock()
	}
}

// borrowProbeJSONLRecord 是 borrow_probes.jsonl 的单行落盘格式(每轮每币每所一行)。
type borrowProbeJSONLRecord struct {
	ScannedAt     time.Time `json:"scanned_at"`
	Symbol        string    `json:"symbol"`
	BaseAsset     string    `json:"base_asset"`
	Venue         string    `json:"venue"`
	Available     bool      `json:"available"`
	MaxBorrowable float64   `json:"max_borrowable"`
	HourlyRate    float64   `json:"hourly_rate"`
	HourlyRateAPR float64   `json:"hourly_rate_apr"`
	Err           string    `json:"error,omitempty"`
}

func (s *CrossVenueScanner) probeBorrowForOpportunities(
	ctx context.Context,
	scannedAt time.Time,
	opps []CrossVenueOpportunity,
	fundingByVenue map[string]map[string]CrossVenueFundingEntry,
) []CrossVenueBorrowProbe {
	topN := s.cfg.BorrowProbeTopN
	if topN <= 0 {
		topN = 10
	}
	if len(opps) > topN {
		opps = opps[:topN]
	}
	fundingTopK := s.cfg.BorrowProbeFundingTopK
	if fundingTopK <= 0 {
		fundingTopK = 60
	}
	maxLive := s.cfg.BorrowProbeMaxLivePerScan
	if maxLive <= 0 {
		maxLive = 30
	}

	symbols := borrowProbeTargetSymbols(opps, selectNegativeFundingProbeSymbols(fundingByVenue, fundingTopK))
	jsonlPath := filepath.Join(s.recordDir, crossVenueBorrowProbesLog)
	bySymbolSummary := make(map[string][]string, len(symbols))
	seen := make(map[string]CrossVenueBorrowProbe, len(symbols)*len(crossVenueNames))
	liveUsed := 0

	for _, sym := range symbols {
		if ctx.Err() != nil {
			break
		}
		base := baseAssetFromCanonicalSymbol(sym)
		if base == "" {
			continue
		}
		var parts []string
		for _, venueName := range crossVenueNames {
			if ctx.Err() != nil {
				break
			}
			if !s.cfg.VenueBorrowCapable(venueName) {
				continue
			}
			adp, ok := s.venues[venueName]
			if !ok || adp == nil {
				continue
			}
			if !adp.Capabilities().SupportsBorrowQuote {
				continue
			}
			probeKey := sym + "|" + venueName
			if _, done := seen[probeKey]; done {
				continue
			}

			probe := CrossVenueBorrowProbe{Symbol: sym, Venue: venueName}
			fromCache := false
			if cached, ok := s.borrowProbeCache.get(venueName, base, scannedAt); ok {
				probe = cached
				probe.Symbol = sym
				fromCache = true
			} else if liveUsed >= maxLive {
				continue
			} else {
				quote, err := adp.FetchBorrowQuote(ctx, base)
				if err != nil {
					if err == venue.ErrUnsupported {
						parts = append(parts, venueName+"不支持")
						continue
					}
					probe.Err = err.Error()
					parts = append(parts, fmt.Sprintf("%s失败", venueName))
					rec := borrowProbeJSONLRecord{
						ScannedAt: scannedAt,
						Symbol:    sym,
						BaseAsset: base,
						Venue:     venueName,
						Err:       probe.Err,
					}
					if werr := appendJSONLRecord(jsonlPath, rec); werr != nil {
						s.logger.Printf("[借币探测] 落盘失败 path=%s: %v", jsonlPath, werr)
					}
				} else {
					probe.Available = quote.Available
					probe.MaxBorrowable = quote.MaxBorrowable
					probe.HourlyRateAPR = borrowHourlyRateAPR(quote.HourlyRate)
					if quote.Available {
						parts = append(parts, fmt.Sprintf("%s可借(年化%.0f%%)", venueName, probe.HourlyRateAPR*100))
					} else {
						parts = append(parts, venueName+"不可借")
					}
					rec := borrowProbeJSONLRecord{
						ScannedAt:     scannedAt,
						Symbol:        sym,
						BaseAsset:     base,
						Venue:         venueName,
						Available:     probe.Available,
						MaxBorrowable: probe.MaxBorrowable,
						HourlyRateAPR: probe.HourlyRateAPR,
						HourlyRate:    quote.HourlyRate,
					}
					if werr := appendJSONLRecord(jsonlPath, rec); werr != nil {
						s.logger.Printf("[借币探测] 落盘失败 path=%s: %v", jsonlPath, werr)
					}
				}
				liveUsed++
				if probe.Err == "" {
					s.borrowProbeCache.put(venueName, base, probe, scannedAt)
				}
				if s.sleepFn != nil {
					s.sleepFn(crossVenueBorrowProbeGap)
				}
			}
			seen[probeKey] = probe
			if fromCache {
				if probe.Available {
					parts = append(parts, fmt.Sprintf("%s可借(缓存,年化%.0f%%)", venueName, probe.HourlyRateAPR*100))
				} else if probe.Err != "" {
					parts = append(parts, fmt.Sprintf("%s失败(缓存)", venueName))
				} else {
					parts = append(parts, venueName+"不可借(缓存)")
				}
			}
		}
		if len(parts) > 0 {
			bySymbolSummary[base] = parts
		}
	}
	for _, cached := range s.borrowProbeCache.allValid(scannedAt) {
		if !s.cfg.VenueBorrowCapable(cached.Venue) {
			continue
		}
		sym := canonicalSymbolFromBaseAsset(cached.Symbol, fundingByVenue)
		if sym == "" {
			sym = cached.Symbol
		}
		probeKey := sym + "|" + cached.Venue
		if _, ok := seen[probeKey]; ok {
			continue
		}
		probe := cached
		probe.Symbol = sym
		seen[probeKey] = probe
	}
	probes := make([]CrossVenueBorrowProbe, 0, len(seen))
	for _, probe := range seen {
		probes = append(probes, probe)
	}
	sort.Slice(probes, func(i, j int) bool {
		if probes[i].Symbol != probes[j].Symbol {
			return probes[i].Symbol < probes[j].Symbol
		}
		return probes[i].Venue < probes[j].Venue
	})
	for base, parts := range bySymbolSummary {
		s.logger.Printf("[借币探测] %s: %s", base, strings.Join(parts, " "))
	}
	if s.borrowKnown != nil {
		s.borrowKnown.ObserveProbes(probes, scannedAt)
	}
	return probes
}

func borrowProbeTargetSymbols(opps []CrossVenueOpportunity, negativeFunding []string) []string {
	seen := make(map[string]struct{}, len(opps)+len(negativeFunding))
	out := make([]string, 0, len(opps)+len(negativeFunding))
	add := func(sym string) {
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if sym == "" {
			return
		}
		if _, ok := seen[sym]; ok {
			return
		}
		seen[sym] = struct{}{}
		out = append(out, sym)
	}
	for _, opp := range opps {
		add(opp.Symbol)
	}
	for _, sym := range negativeFunding {
		add(sym)
	}
	return out
}

func selectNegativeFundingProbeSymbols(fundingByVenue map[string]map[string]CrossVenueFundingEntry, topK int) []string {
	if topK <= 0 || len(fundingByVenue) == 0 {
		return nil
	}
	minRateBySym := make(map[string]float64)
	for _, bySym := range fundingByVenue {
		for sym, entry := range bySym {
			if entry.Rate >= 0 {
				continue
			}
			if cur, ok := minRateBySym[sym]; !ok || entry.Rate < cur {
				minRateBySym[sym] = entry.Rate
			}
		}
	}
	if len(minRateBySym) == 0 {
		return nil
	}
	syms := make([]string, 0, len(minRateBySym))
	for sym := range minRateBySym {
		syms = append(syms, sym)
	}
	sort.Slice(syms, func(i, j int) bool {
		ri, rj := minRateBySym[syms[i]], minRateBySym[syms[j]]
		if ri != rj {
			return ri < rj
		}
		return syms[i] < syms[j]
	})
	if len(syms) > topK {
		syms = syms[:topK]
	}
	return syms
}

func canonicalSymbolFromBaseAsset(symbolOrBase string, fundingByVenue map[string]map[string]CrossVenueFundingEntry) string {
	sym := strings.ToUpper(strings.TrimSpace(symbolOrBase))
	if sym == "" {
		return ""
	}
	if strings.Contains(sym, "-") {
		return sym
	}
	for _, bySym := range fundingByVenue {
		for candidate := range bySym {
			if baseAssetFromCanonicalSymbol(candidate) == sym {
				return candidate
			}
		}
	}
	return sym + "-USDT"
}

func baseAssetFromCanonicalSymbol(sym string) string {
	sym = strings.ToUpper(strings.TrimSpace(sym))
	if i := strings.Index(sym, "-"); i > 0 {
		return sym[:i]
	}
	return sym
}

func borrowHourlyRateAPR(hourlyRate float64) float64 {
	return hourlyRate * 8760.0
}

func (s *CrossVenueScanner) fetchAllVenues(ctx context.Context) (map[string][]venue.FundingSnapshot, []CrossVenueVenueStatus) {
	type result struct {
		name   string
		snaps  []venue.FundingSnapshot
		status CrossVenueVenueStatus
	}
	ch := make(chan result, len(crossVenueNames))
	var wg sync.WaitGroup
	for _, name := range crossVenueNames {
		adp, ok := s.venues[name]
		if !ok || adp == nil {
			ch <- result{
				name: name,
				status: CrossVenueVenueStatus{
					Venue: name,
					OK:    false,
					Error: "适配器未就绪(凭证缺失或未注册)",
				},
			}
			continue
		}
		wg.Add(1)
		go func(venueName string, adapter venue.Venue) {
			defer wg.Done()
			to := crossVenueDefaultFetchTO
			if venueName == okx.VenueName {
				to = crossVenueOKXFetchTO
			}
			fetchCtx, cancel := context.WithTimeout(ctx, to)
			defer cancel()
			t0 := s.nowFn()
			snaps, err := s.fetchVenue(fetchCtx, venueName, adapter)
			elapsed := s.nowFn().Sub(t0)
			st := CrossVenueVenueStatus{
				Venue:         venueName,
				ElapsedMillis: elapsed.Milliseconds(),
			}
			if err != nil {
				st.Error = err.Error()
				s.logger.Printf("[跨所扫描] venue=%s 拉取失败(跳过该所): %v", venueName, err)
			} else {
				st.OK = true
				st.SymbolCount = len(snaps)
			}
			ch <- result{name: venueName, snaps: snaps, status: st}
		}(name, adp)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	snapshots := make(map[string][]venue.FundingSnapshot, len(crossVenueNames))
	statuses := make([]CrossVenueVenueStatus, 0, len(crossVenueNames))
	for res := range ch {
		if res.status.OK {
			snapshots[res.name] = res.snaps
		}
		statuses = append(statuses, res.status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Venue < statuses[j].Venue })
	return snapshots, statuses
}

func defaultFetchVenueSnapshot(ctx context.Context, _ string, adp venue.Venue) ([]venue.FundingSnapshot, error) {
	if adp == nil {
		return nil, fmt.Errorf("适配器为空")
	}
	return adp.FetchFundingSnapshot(ctx)
}

func crossVenueTradableInstrumentsCacheDuration(cfg CrossVenueScannerConfig) time.Duration {
	if cfg.ScanIntervalMinutes > 0 {
		return time.Duration(cfg.ScanIntervalMinutes) * time.Minute
	}
	return crossVenueTradableInstrumentsCacheTTL
}

// refreshTradablePerpCacheIfNeeded 拉取/刷新各所可交易永续 symbol 集合(TTL 内复用缓存)。
func (s *CrossVenueScanner) refreshTradablePerpCacheIfNeeded(ctx context.Context) {
	if s == nil {
		return
	}
	now := s.nowFn()
	ttl := crossVenueTradableInstrumentsCacheDuration(s.cfg)
	s.tradablePerpMu.Lock()
	defer s.tradablePerpMu.Unlock()
	if !s.tradablePerpFetchedAt.IsZero() && now.Sub(s.tradablePerpFetchedAt) < ttl {
		return
	}
	s.tradablePerpSymbols = make(map[string]map[string]struct{}, len(s.venues))
	s.tradablePerpSkip = make(map[string]bool, len(s.venues))
	for venueName, adp := range s.venues {
		if adp == nil {
			s.tradablePerpSkip[venueName] = true
			continue
		}
		instruments, err := adp.FetchInstruments(ctx)
		if err != nil || len(instruments) == 0 {
			s.tradablePerpSkip[venueName] = true
			if s.logger != nil {
				if err != nil {
					s.logger.Printf("[跨所扫描] venue=%s instrument 不可用,跳过幽灵过滤(fail-open): %v", venueName, err)
				} else {
					s.logger.Printf("[跨所扫描] venue=%s instrument 为空,跳过幽灵过滤(fail-open)", venueName)
				}
			}
			continue
		}
		set := make(map[string]struct{})
		for _, instr := range instruments {
			if instr.Market != venue.MarketPerp || instr.NonTrading {
				continue
			}
			sym := string(instr.Symbol)
			if sym == "" {
				continue
			}
			set[sym] = struct{}{}
		}
		if len(set) == 0 {
			s.tradablePerpSkip[venueName] = true
			if s.logger != nil {
				s.logger.Printf("[跨所扫描] venue=%s 无可交易永续 instrument,跳过幽灵过滤(fail-open)", venueName)
			}
			continue
		}
		s.tradablePerpSymbols[venueName] = set
	}
	s.tradablePerpFetchedAt = now
}

// filterGhostFundingSnapshots 用各所可交易永续集合过滤 premiumIndex 等接口漏入的下架/幽灵 symbol。
// FetchInstruments 失败/空时对该所 fail-open(保留全部 funding),开仓闸 instrument 查询仍为最终兜底。
func (s *CrossVenueScanner) filterGhostFundingSnapshots(ctx context.Context, byVenue map[string][]venue.FundingSnapshot) map[string][]venue.FundingSnapshot {
	if s == nil || len(byVenue) == 0 {
		return byVenue
	}
	s.refreshTradablePerpCacheIfNeeded(ctx)
	out := make(map[string][]venue.FundingSnapshot, len(byVenue))
	for venueName, snaps := range byVenue {
		s.tradablePerpMu.Lock()
		skip := s.tradablePerpSkip[venueName]
		allowed := s.tradablePerpSymbols[venueName]
		s.tradablePerpMu.Unlock()
		if skip || allowed == nil {
			out[venueName] = snaps
			continue
		}
		filtered := make([]venue.FundingSnapshot, 0, len(snaps))
		dropped := 0
		for _, snap := range snaps {
			sym := string(snap.Symbol)
			if sym == "" {
				continue
			}
			if _, ok := allowed[sym]; !ok {
				dropped++
				continue
			}
			filtered = append(filtered, snap)
		}
		if dropped > 0 && s.logger != nil {
			s.logger.Printf("[跨所扫描] 资费幽灵腿过滤: venue=%s 丢弃 %d 个非交易 symbol", venueName, dropped)
		}
		out[venueName] = filtered
	}
	return out
}

// fundingAnnualizedAPR 把单次结算费率按真实 interval 折算为年化(8760h/年)。
func fundingAnnualizedAPR(rate float64, intervalHours int) float64 {
	if intervalHours <= 0 {
		intervalHours = 8
	}
	return rate * (8760.0 / float64(intervalHours))
}

// capFundingRateByAPR 把单次结算费率按年化绝对值封顶(保留符号),防瞬时尖峰污染价差/EV。
func capFundingRateByAPR(rate float64, intervalHours int, capAPR float64) float64 {
	if capAPR <= 0 {
		return rate
	}
	if intervalHours <= 0 {
		intervalHours = 8
	}
	apr := fundingAnnualizedAPR(rate, intervalHours)
	if math.Abs(apr) <= capAPR {
		return rate
	}
	return math.Copysign(capAPR*float64(intervalHours)/8760.0, rate)
}

func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

const crossVenueFundingHistoryMax = 10

func (s *CrossVenueScanner) recordFundingHistory(byVenue map[string][]venue.FundingSnapshot) {
	if s == nil {
		return
	}
	s.fundingHistoryMu.Lock()
	defer s.fundingHistoryMu.Unlock()
	if s.fundingHistory == nil {
		s.fundingHistory = make(map[string]map[string][]float64)
	}
	for venueName, snaps := range byVenue {
		if s.fundingHistory[venueName] == nil {
			s.fundingHistory[venueName] = make(map[string][]float64)
		}
		for _, snap := range snaps {
			sym := string(snap.Symbol)
			if sym == "" {
				continue
			}
			hist := append(s.fundingHistory[venueName][sym], snap.LastFundingRate)
			if len(hist) > crossVenueFundingHistoryMax {
				hist = hist[len(hist)-crossVenueFundingHistoryMax:]
			}
			s.fundingHistory[venueName][sym] = hist
		}
	}
}

func (s *CrossVenueScanner) smoothedFundingRate(venueName, sym string, current float64, window int) (float64, int) {
	if window <= 0 {
		window = 2
	}
	s.fundingHistoryMu.Lock()
	defer s.fundingHistoryMu.Unlock()
	hist := append([]float64(nil), s.fundingHistory[venueName][sym]...)
	if len(hist) < window {
		return 0, len(hist)
	}
	if len(hist) > window {
		hist = hist[len(hist)-window:]
	}
	return medianFloat64(hist), len(hist)
}

func (s *CrossVenueScanner) fundingHistorySampleCount(venueName, sym string) int {
	if s == nil {
		return 0
	}
	s.fundingHistoryMu.Lock()
	defer s.fundingHistoryMu.Unlock()
	return len(s.fundingHistory[venueName][sym])
}

// fundingSpikeThreshold 返回 venue/symbol 滚动 |rate| 历史的 p90; 样本不足 2 时返回 0(不做尖峰裁剪)。
func (s *CrossVenueScanner) fundingSpikeThreshold(venue, sym string) float64 {
	if s == nil {
		return 0
	}
	s.fundingHistoryMu.Lock()
	hist := append([]float64(nil), s.fundingHistory[venue][sym]...)
	s.fundingHistoryMu.Unlock()
	return absRatePercentile(hist, 90)
}

// fundingRateVolatilityPerHour 返回 venue/symbol 最近扫描样本的每小时归一化费率标准差。
// 样本来自 recordFundingHistory 滚动缓存(扫描 cadence);不足 2 个样本时 ok=false。
func (s *CrossVenueScanner) fundingRateVolatilityPerHour(venue, sym string, intervalHours, lookback int) (float64, bool) {
	if s == nil || intervalHours <= 0 {
		return 0, false
	}
	if lookback <= 0 {
		lookback = crossVenueFundingHistoryMax
	}
	s.fundingHistoryMu.Lock()
	hist := append([]float64(nil), s.fundingHistory[venue][sym]...)
	s.fundingHistoryMu.Unlock()
	if len(hist) > lookback {
		hist = hist[len(hist)-lookback:]
	}
	return fundingRateStdDevPerHour(hist, intervalHours)
}

func fundingRateStdDevPerHour(rates []float64, intervalHours int) (float64, bool) {
	if len(rates) < 2 || intervalHours <= 0 {
		return 0, false
	}
	ih := float64(intervalHours)
	mean := 0.0
	for _, r := range rates {
		mean += r / ih
	}
	mean /= float64(len(rates))
	var sumSq float64
	for _, r := range rates {
		d := r/ih - mean
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(rates))), true
}

func absRatePercentile(rates []float64, pct float64) float64 {
	if len(rates) < 2 {
		return 0
	}
	abs := make([]float64, len(rates))
	for i, r := range rates {
		abs[i] = math.Abs(r)
	}
	sort.Float64s(abs)
	if pct <= 0 {
		return abs[0]
	}
	if pct >= 100 {
		return abs[len(abs)-1]
	}
	rank := (pct / 100.0) * float64(len(abs)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(abs) {
		return abs[lo]
	}
	weight := rank - float64(lo)
	return abs[lo]*(1-weight) + abs[hi]*weight
}

// buildCrossVenueFundingIndex 把各所快照整理成 venue → symbol → 费率明细 的只读索引。
// 每轮扫描整体新建(绝不原地改旧 map),发布到 lastSnapshot 后只读,保证并发安全。
func buildCrossVenueFundingIndex(byVenue map[string][]venue.FundingSnapshot) map[string]map[string]CrossVenueFundingEntry {
	out := make(map[string]map[string]CrossVenueFundingEntry, len(byVenue))
	for venueName, snaps := range byVenue {
		m := make(map[string]CrossVenueFundingEntry, len(snaps))
		for _, snap := range snaps {
			sym := string(snap.Symbol)
			if sym == "" {
				continue
			}
			interval := snap.IntervalHours
			if interval <= 0 {
				interval = 8
			}
			m[sym] = CrossVenueFundingEntry{
				Rate:            snap.LastFundingRate,
				IntervalHours:   interval,
				NextFundingTime: snap.NextFundingTime,
			}
		}
		out[venueName] = m
	}
	return out
}

// indexSnapshotsBySymbol 把各所快照按 canonical symbol 索引(用 live forward 费率+APR 封顶算年化)。
func (s *CrossVenueScanner) indexSnapshotsBySymbol(byVenue map[string][]venue.FundingSnapshot) map[string][]symbolVenueFunding {
	now := time.Now().UTC()
	if s != nil && s.nowFn != nil {
		now = s.nowFn().UTC()
	}
	return indexSnapshotsBySymbolAt(byVenue, now, s.cfg.FundingAPRSanityCap)
}

func indexSnapshotsBySymbolAt(byVenue map[string][]venue.FundingSnapshot, now time.Time, fundingAPRSanityCap float64) map[string][]symbolVenueFunding {
	capAPR := fundingAPRSanityCap
	if capAPR <= 0 {
		capAPR = 5.0
	}
	out := make(map[string][]symbolVenueFunding)
	for venueName, snaps := range byVenue {
		for _, snap := range snaps {
			sym := string(snap.Symbol)
			if sym == "" {
				continue
			}
			pairKey, econBase, mult, multKnown := crossVenueEconomicPairKey(sym)
			indexKey := pairKey
			if !multKnown {
				// 倍数无法解析 → 仅按 canonical 精确配对,等同改造前行为
				indexKey = sym
			}
			interval := snap.IntervalHours
			if interval <= 0 {
				interval = 8
			}
			nextFunding, inferred := crossVenueResolveNextFundingTime(now, snap.NextFundingTime, interval)
			rawRate := snap.LastFundingRate
			rate := capFundingRateByAPR(rawRate, interval, capAPR)
			out[indexKey] = append(out[indexKey], symbolVenueFunding{
				Venue:                   venueName,
				CanonicalSymbol:         sym,
				EconomicPairKey:         pairKey,
				EconomicBase:            econBase,
				Multiplier:              mult,
				MultKnown:               multKnown,
				FundingRate:             rate,
				RawFundingRate:          rawRate,
				AnnualizedAPR:           fundingAnnualizedAPR(rate, interval),
				RawAnnualizedAPR:        fundingAnnualizedAPR(rawRate, interval),
				IntervalHours:           interval,
				NextFundingTime:         nextFunding,
				NextFundingTimeInferred: inferred,
			})
		}
	}
	return out
}

// bestPairSpread 对同一经济配对键下的多所记录取最优两两价差(年化高所 − 年化低所)。
// 跨计价配对须两腿 MultKnown 且经济 base 一致;价格比二次确认在开仓闸 evaluateCrossVenueOpenGate 执行。
func bestPairSpread(pairKey string, entries []symbolVenueFunding) (CrossVenueOpportunity, bool) {
	if len(entries) < 2 {
		return CrossVenueOpportunity{}, false
	}
	best := CrossVenueOpportunity{Symbol: pairKey}
	found := false
	bestRawSpread := 0.0
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			a, b := entries[i], entries[j]
			if !crossVenueFundingPairAllowed(a, b) {
				continue
			}
			// 按【原始费率】(每次结算实际收取的费率)选对,而非年化 APR。用户策略口径:只看单次结算实际
			// 领到多少;年化把结算频率(1h/2h/4h)混入,会把"单次大额但低频"的腿(如 binance -0.94%/4h)
			// 误判为不如"单次小额但高频"的腿(bybit -0.33%/1h),选出不符合实际收取的配对。
			// high=原始费率较大(较不负,做空),low=原始费率较小(较负,做多)。
			high, low := a, b
			if b.RawFundingRate > a.RawFundingRate {
				high, low = b, a
			} else if b.RawFundingRate == a.RawFundingRate {
				continue
			}
			rawSpread := high.RawFundingRate - low.RawFundingRate
			// SpreadAPR 仍存年化价差,仅供展示与极端标记;选对/排序的实际收益由离散 raw×结算次数 EV 决定。
			spread := high.RawAnnualizedAPR - low.RawAnnualizedAPR
			if !found || rawSpread > bestRawSpread {
				found = true
				bestRawSpread = rawSpread
				displaySym, highSym, lowSym, highMult, lowMult := crossVenueOpportunitySymbolsFromLegs(pairKey, high, low)
				best = CrossVenueOpportunity{
					Symbol:              displaySym,
					HighSymbol:          highSym,
					LowSymbol:           lowSym,
					HighMult:            highMult,
					LowMult:             lowMult,
					HighVenue:           high.Venue,
					LowVenue:            low.Venue,
					HighAnnualizedAPR:   high.RawAnnualizedAPR,
					LowAnnualizedAPR:    low.RawAnnualizedAPR,
					SpreadAPR:           spread,
					HighFundingRate:     high.RawFundingRate,
					LowFundingRate:      low.RawFundingRate,
					HighIntervalHours:   high.IntervalHours,
					LowIntervalHours:    low.IntervalHours,
					HighNextFundingTime: high.NextFundingTime,
					LowNextFundingTime:  low.NextFundingTime,
				}
			}
		}
	}
	return best, found
}

// crossVenueFundingPairAllowed 索引期初筛:同 canonical 或跨计价且倍数/经济 base 均可解析且一致。
func crossVenueFundingPairAllowed(a, b symbolVenueFunding) bool {
	if a.Venue == b.Venue {
		return false
	}
	if a.CanonicalSymbol == b.CanonicalSymbol {
		return true
	}
	if !a.MultKnown || !b.MultKnown {
		return false
	}
	if a.EconomicBase == "" || b.EconomicBase == "" {
		return false
	}
	return strings.EqualFold(a.EconomicBase, b.EconomicBase)
}

func crossVenueOpportunitySymbolsFromLegs(pairKey string, high, low symbolVenueFunding) (displaySym, highSym, lowSym string, highMult, lowMult float64) {
	highSym = high.CanonicalSymbol
	lowSym = low.CanonicalSymbol
	if highSym == "" {
		highSym = pairKey
	}
	if lowSym == "" {
		lowSym = pairKey
	}
	highMult = high.Multiplier
	lowMult = low.Multiplier
	if highMult <= 0 {
		highMult = 1
	}
	if lowMult <= 0 {
		lowMult = 1
	}
	if highSym == lowSym {
		return highSym, highSym, lowSym, highMult, lowMult
	}
	return pairKey, highSym, lowSym, highMult, lowMult
}

// crossVenueAbsurdSpreadCap 返回跨所 spread 绝对荒谬上限;用户 FundingAPRSanityCap 更严时尊重。
func crossVenueAbsurdSpreadCap(cfg CrossVenueScannerConfig) float64 {
	cap := crossVenueSpreadSanityCap
	if userCap := cfg.FundingAPRSanityCap; userCap > 0 && userCap < cap {
		cap = userCap
	}
	return cap
}

// crossVenueEVFundingRateCapAPR 极端 spread EV 重算用的单腿费率封顶(远高于常规 200% spread 闸)。
func crossVenueEVFundingRateCapAPR(cfg CrossVenueScannerConfig) float64 {
	return crossVenueAbsurdSpreadCap(cfg)
}

func rawFundingRateFromEntries(entries []symbolVenueFunding, venueName string) float64 {
	for _, e := range entries {
		if e.Venue == venueName {
			if e.RawFundingRate != 0 {
				return e.RawFundingRate
			}
			return e.FundingRate
		}
	}
	return 0
}

func crossVenueExtremeSpreadEVRates(cfg CrossVenueScannerConfig, opp CrossVenueOpportunity, entries []symbolVenueFunding) (highRate, lowRate float64) {
	highRate = opp.HighFundingRate
	lowRate = opp.LowFundingRate
	if !opp.ExtremeSpread {
		return highRate, lowRate
	}
	evCap := crossVenueEVFundingRateCapAPR(cfg)
	if raw := rawFundingRateFromEntries(entries, opp.HighVenue); raw != 0 {
		highRate = capFundingRateByAPR(raw, opp.HighIntervalHours, evCap)
	}
	if raw := rawFundingRateFromEntries(entries, opp.LowVenue); raw != 0 {
		lowRate = capFundingRateByAPR(raw, opp.LowIntervalHours, evCap)
	}
	return highRate, lowRate
}

// computeCrossVenueOpportunities 聚合各所快照,用近 N 次中位数+APR 封顶算价差,过滤/排序(全量,不截断)。
func (s *CrossVenueScanner) computeCrossVenueOpportunities(ctx context.Context, byVenue map[string][]venue.FundingSnapshot) []CrossVenueOpportunity {
	minSpread := s.cfg.MinSpreadAPR
	topN := s.cfg.TopN
	if topN <= 0 {
		topN = 30
	}
	absurdCap := crossVenueAbsurdSpreadCap(s.cfg)
	markThreshold := crossVenueExtremeSpreadMarkAPR
	if userCap := s.cfg.FundingAPRSanityCap; userCap > 0 && userCap < markThreshold {
		markThreshold = userCap
	}
	bySymbol := s.indexSnapshotsBySymbol(byVenue)
	diagBySym := s.initPerpPerpDiagRound(bySymbol)
	opps := make([]CrossVenueOpportunity, 0, len(bySymbol))
	for sym, entries := range bySymbol {
		if len(entries) < 2 {
			s.notePerpPerpDiagReject(diagBySym, sym, fmt.Sprintf("venues=%d need>=2", len(entries)))
			continue
		}
		opp, ok := bestPairSpread(sym, entries)
		if !ok {
			s.notePerpPerpDiagReject(diagBySym, sym, "bestPairSpread no spread")
			continue
		}
		s.notePerpPerpDiagPaired(diagBySym, sym, opp)
		if opp.SpreadAPR < minSpread {
			s.notePerpPerpDiagReject(diagBySym, sym, fmt.Sprintf("spreadAPR=%.4f < min=%.4f", opp.SpreadAPR, minSpread))
			continue
		}
		if absurdCap > 0 && opp.SpreadAPR > absurdCap {
			s.notePerpPerpDiagReject(diagBySym, sym, fmt.Sprintf("spreadAPR=%.4f > absurdCap=%.4f", opp.SpreadAPR, absurdCap))
			continue
		}
		if markThreshold > 0 && opp.SpreadAPR > markThreshold {
			opp.ExtremeSpread = true
		}
		highSamples := s.fundingHistorySampleCount(opp.HighVenue, crossVenueOppHighSymbol(opp))
		lowSamples := s.fundingHistorySampleCount(opp.LowVenue, crossVenueOppLowSymbol(opp))
		minSamples := minInt(highSamples, lowSamples)
		windowHours := crossVenueComparableWindowHours(s.nowFn(), opp.HighNextFundingTime, opp.LowNextFundingTime)
		opp.HistoryQualityScore = crossVenueHistoryQualityScore(minSamples, s.cfg.SpreadPersistenceScans)
		opp.StabilityScore = crossVenueStabilityScore(windowHours, minSamples)
		opp.Symbol = sym
		tradableCap, slipLegs := s.perpPerpScanTradableCapAndSlippageLegs(ctx, opp)
		if opp.ExtremeSpread && tradableCap <= 0 {
			s.notePerpPerpDiagReject(diagBySym, sym, "extreme spread but tradableNotional<=0")
			continue
		}
		evHigh, evLow := crossVenueExtremeSpreadEVRates(s.cfg, opp, entries)
		clamp := crossVenuePerpPerpNotionalClampCtxForOpp(ctx, s.venues, opp, slipLegs)
		opp, chosenNotional := crossVenuePerpPerpSearchBestNotionalPerLeg(s.execCfg, opp, s.nowFn(), evHigh, evLow, tradableCap, slipLegs, clamp)
		s.notePerpPerpDiagEntered(diagBySym, sym, opp, chosenNotional)
		opps = append(opps, opp)
	}
	preTopN := len(opps)
	crossVenueFinalizeOpportunityRanking(s.execCfg, opps)
	sort.Slice(opps, func(i, j int) bool {
		if opps[i].RankScore == opps[j].RankScore {
			return opps[i].Symbol < opps[j].Symbol
		}
		return opps[i].RankScore > opps[j].RankScore
	})
	truncated := len(opps) > topN
	s.finalizePerpPerpDiagRound(diagBySym, opps, preTopN, topN, truncated)
	return opps
}

// perpPerpScanTradableCapAndSlippageLegs 扫描期流动性 cap 与四腿深度滑点输入(与执行闸同 fetchDepth 路径,per-leg symbol)。
func (s *CrossVenueScanner) perpPerpScanTradableCapAndSlippageLegs(ctx context.Context, opp CrossVenueOpportunity) (float64, []crossVenueEVSlippageLeg) {
	tradableCap := crossVenuePerpPerpScanTradableNotionalForOpp(ctx, s.execCfg, s.venues, opp)
	highV := s.venues[opp.HighVenue]
	lowV := s.venues[opp.LowVenue]
	if ctx == nil || highV == nil || lowV == nil {
		return tradableCap, nil
	}
	highSym := venue.Symbol(crossVenueOppHighSymbol(opp))
	lowSym := venue.Symbol(crossVenueOppLowSymbol(opp))
	fetchDepth := func(c context.Context, v venue.Venue, symbol venue.Symbol, limit int) (venue.Depth, error) {
		return v.FetchDepth(c, venue.MarketPerp, symbol, limit)
	}
	highMid := crossVenuePerpPerpMidFromDepthOrBook(ctx, highV, highSym)
	lowMid := crossVenuePerpPerpMidFromDepthOrBook(ctx, lowV, lowSym)
	slipLegs := crossVenuePerpPerpEVSlippageLegsPerSymbol(fetchDepth, ctx, highV, lowV, highSym, lowSym, highMid, lowMid)
	return tradableCap, slipLegs
}

// crossVenuePerpPerpScanTradableNotionalForOpp 扫描期 perp-perp 可交易名义(每腿独立 symbol 拉 depth)。
func crossVenuePerpPerpScanTradableNotionalForOpp(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	opp CrossVenueOpportunity,
) float64 {
	maxPos := cfg.MaxPositionUSDT
	if maxPos <= 0 {
		maxPos = 50
	}
	noDepthFactor := resolvedSizingConservativeNoDepthFactor(cfg)
	if cfg.SizingMaxSlippageBps <= 0 && cfg.SizingMaxParticipationPct24h <= 0 {
		return applyLiquiditySizingCap(maxPos, math.MaxFloat64, noDepthFactor)
	}
	highV := venues[opp.HighVenue]
	lowV := venues[opp.LowVenue]
	if ctx == nil || highV == nil || lowV == nil {
		return applyLiquiditySizingCap(maxPos, math.MaxFloat64, noDepthFactor)
	}
	deps := crossVenueGateDeps{
		fetchDepth: func(c context.Context, v venue.Venue, s venue.Symbol, limit int) (venue.Depth, error) {
			return v.FetchDepth(c, venue.MarketPerp, s, limit)
		},
	}
	liqCap := crossVenueGateLiquidityNotionalCapPerLeg(
		ctx, cfg, deps, highV, lowV,
		venue.Symbol(crossVenueOppHighSymbol(opp)),
		venue.Symbol(crossVenueOppLowSymbol(opp)),
	)
	return applyLiquiditySizingCap(maxPos, liqCap, noDepthFactor)
}

// crossVenuePerpPerpNotionalClampCtxForOpp 扫描期名义网格对齐(每腿独立 symbol/instrument/mid)。
func crossVenuePerpPerpNotionalClampCtxForOpp(ctx context.Context, venues map[string]venue.Venue, opp CrossVenueOpportunity, slipLegs []crossVenueEVSlippageLeg) perpPerpNotionalClampCtx {
	out := perpPerpNotionalClampCtx{}
	if ctx == nil || venues == nil {
		return out
	}
	highV := venues[opp.HighVenue]
	lowV := venues[opp.LowVenue]
	if highV == nil || lowV == nil {
		return out
	}
	highSym := venue.Symbol(crossVenueOppHighSymbol(opp))
	lowSym := venue.Symbol(crossVenueOppLowSymbol(opp))
	highInstr, okH := lookupVenueInstrument(ctx, highV, highSym)
	lowInstr, okL := lookupVenueInstrument(ctx, lowV, lowSym)
	if !okH || !okL {
		return out
	}
	out.highInstr = highInstr
	out.lowInstr = lowInstr
	out.highCaps = highV.Capabilities()
	out.lowCaps = lowV.Capabilities()
	highMid := 0.0
	lowMid := 0.0
	if len(slipLegs) >= 2 {
		highMid = slipLegs[0].RefMid
		lowMid = slipLegs[1].RefMid
	}
	if highMid <= 0 {
		highMid = crossVenuePerpPerpMidFromDepthOrBook(ctx, highV, highSym)
	}
	if lowMid <= 0 {
		lowMid = crossVenuePerpPerpMidFromDepthOrBook(ctx, lowV, lowSym)
	}
	out.refMid = crossVenuePerpPerpRefMidFromLegMids(highMid, lowMid)
	out.ok = highMid > 0 && lowMid > 0
	if !out.ok {
		return out
	}
	out.refMid = (highMid + lowMid) / 2
	return out
}

func crossVenuePerpPerpRefMidFromLegMids(highMid, lowMid float64) float64 {
	if highMid > 0 && lowMid > 0 {
		return (highMid + lowMid) / 2
	}
	if highMid > 0 {
		return highMid
	}
	return lowMid
}

func crossVenuePerpPerpEVSlippageLegsPerSymbol(
	fetch func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error),
	ctx context.Context,
	highV, lowV venue.Venue,
	highSym, lowSym venue.Symbol,
	highMid, lowMid float64,
) []crossVenueEVSlippageLeg {
	if fetch == nil {
		return nil
	}
	legs := []crossVenueEVSlippageLeg{
		{Side: venue.SideSell, RefMid: highMid},
		{Side: venue.SideBuy, RefMid: lowMid},
		{Side: venue.SideBuy, RefMid: highMid},
		{Side: venue.SideSell, RefMid: lowMid},
	}
	venues := []venue.Venue{highV, lowV, highV, lowV}
	syms := []venue.Symbol{highSym, lowSym, highSym, lowSym}
	for i := range legs {
		if venues[i] == nil {
			continue
		}
		if depth, err := fetch(ctx, venues[i], syms[i], crossVenueOpenSlippageDepthLimit); err == nil {
			legs[i].Depth = &depth
		}
	}
	return legs
}

// crossVenuePerpPerpSearchBestNotionalPerLeg 扫描期名义网格搜索(每腿 mid/instrument 对齐,跨计价安全)。
func crossVenuePerpPerpSearchBestNotionalPerLeg(
	cfg CrossVenueExecutorConfig,
	opp CrossVenueOpportunity,
	now time.Time,
	evHigh, evLow, tradableCap float64,
	slipLegs []crossVenueEVSlippageLeg,
	clamp perpPerpNotionalClampCtx,
) (CrossVenueOpportunity, float64) {
	highMid, lowMid := crossVenueSlipLegMids(slipLegs)
	if highMid <= 0 || lowMid <= 0 {
		return crossVenuePerpPerpSearchBestNotional(cfg, opp, now, evHigh, evLow, tradableCap, slipLegs, clamp)
	}
	grid := []float64{25, 50, 75, 100, 150}
	grid = appendPerpPerpNotionalGridPoint(grid, tradableCap)
	best := math.Inf(-1)
	var bestOpp CrossVenueOpportunity
	var bestNotional float64
	for _, n := range grid {
		n = crossVenueClampNotionalPerLeg(n, opp, tradableCap, highMid, lowMid, clamp.highInstr, clamp.lowInstr, clamp.highCaps, clamp.lowCaps)
		if n <= 0 {
			continue
		}
		cand := opp
		cand.TradableNotional = n
		cand = crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, cand, now, evHigh, evLow, slipLegs)
		if cand.NetEV > best {
			best = cand.NetEV
			bestOpp = cand
			bestNotional = n
		}
	}
	if math.IsInf(best, -1) {
		fallback := crossVenueClampNotionalPerLeg(tradableCap, opp, tradableCap, highMid, lowMid, clamp.highInstr, clamp.lowInstr, clamp.highCaps, clamp.lowCaps)
		if fallback <= 0 && tradableCap > 0 {
			fallback = tradableCap
		}
		opp.TradableNotional = fallback
		return crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, opp, now, evHigh, evLow, slipLegs), fallback
	}
	return bestOpp, bestNotional
}

func crossVenueSlipLegMids(slipLegs []crossVenueEVSlippageLeg) (highMid, lowMid float64) {
	if len(slipLegs) >= 1 {
		highMid = slipLegs[0].RefMid
	}
	if len(slipLegs) >= 2 {
		lowMid = slipLegs[1].RefMid
	}
	return highMid, lowMid
}

// crossVenueOpportunitiesTopN 取已排序机会的前 N 条(仅面板展示;快照保留全量)。
func crossVenueOpportunitiesTopN(opps []CrossVenueOpportunity, topN int) []CrossVenueOpportunity {
	if topN <= 0 || len(opps) <= topN {
		return opps
	}
	out := make([]CrossVenueOpportunity, topN)
	copy(out, opps[:topN])
	return out
}

// crossVenueScannerPanelDisplayTopN 面板展示条数;未配则退回 TopN,再默认 30。
func crossVenueScannerPanelDisplayTopN(cfg CrossVenueScannerConfig) int {
	if cfg.PanelDisplayTopN > 0 {
		return cfg.PanelDisplayTopN
	}
	if cfg.TopN > 0 {
		return cfg.TopN
	}
	return 30
}

type perpPerpDiagRound struct {
	symbol        string
	venueCount    int
	legs          []string
	paired        bool
	spreadAPR     float64
	entered       bool
	netEV         float64
	rankScore     float64
	inTopN        bool
	topNTruncated bool
	rejectReason  string
	// 成本拆解(entered 后填充),用于定位 netEV 为负时是哪一项成本主导。
	notional       float64
	chosenNotional float64
	spreadEV       float64
	openCost       float64
	closeCost      float64
	slippage       float64
	observableRisk float64
	policyBuffer   float64
}

func (s *CrossVenueScanner) initPerpPerpDiagRound(bySymbol map[string][]symbolVenueFunding) map[string]*perpPerpDiagRound {
	out := make(map[string]*perpPerpDiagRound)
	for sym, entries := range bySymbol {
		if !crossVenuePerpPerpDiagWatched(sym) {
			continue
		}
		d := &perpPerpDiagRound{symbol: sym, venueCount: len(entries)}
		for _, e := range entries {
			d.legs = append(d.legs, formatPerpPerpDiagLeg(e))
		}
		out[sym] = d
	}
	return out
}

func formatPerpPerpDiagLeg(e symbolVenueFunding) string {
	inferred := "real"
	if e.NextFundingTimeInferred {
		inferred = "inferred"
	}
	return fmt.Sprintf("%s raw=%.8f apr=%.2f%% interval=%dh next=%s(%s)",
		e.Venue, e.RawFundingRate, e.AnnualizedAPR*100, e.IntervalHours,
		e.NextFundingTime.UTC().Format(time.RFC3339), inferred)
}

func (s *CrossVenueScanner) notePerpPerpDiagReject(diag map[string]*perpPerpDiagRound, sym, reason string) {
	if d := diag[sym]; d != nil && d.rejectReason == "" {
		d.rejectReason = reason
	}
}

func (s *CrossVenueScanner) notePerpPerpDiagPaired(diag map[string]*perpPerpDiagRound, sym string, opp CrossVenueOpportunity) {
	if d := diag[sym]; d != nil {
		d.paired = true
		d.spreadAPR = opp.SpreadAPR
	}
}

func (s *CrossVenueScanner) notePerpPerpDiagEntered(diag map[string]*perpPerpDiagRound, sym string, opp CrossVenueOpportunity, chosenNotional float64) {
	if d := diag[sym]; d != nil {
		d.entered = true
		d.netEV = opp.NetEV
		d.rankScore = opp.RankScore
		d.notional = opp.TradableNotional
		d.chosenNotional = chosenNotional
		d.spreadEV = opp.SpreadEV
		d.openCost = opp.OpenCost
		d.closeCost = opp.CloseCost
		d.slippage = opp.ExpectedSlippage
		d.observableRisk = opp.ObservableRiskCost
		d.policyBuffer = opp.PolicyBuffer
	}
}

func (s *CrossVenueScanner) finalizePerpPerpDiagRound(diag map[string]*perpPerpDiagRound, opps []CrossVenueOpportunity, preTopN, topN int, truncated bool) {
	if s == nil || s.logger == nil || len(diag) == 0 {
		return
	}
	topSyms := make(map[string]CrossVenueOpportunity, topN)
	for i, opp := range opps {
		if i >= topN {
			break
		}
		topSyms[opp.Symbol] = opp
	}
	for sym, d := range diag {
		if opp, ok := topSyms[sym]; ok {
			d.inTopN = true
			d.netEV = opp.NetEV
			d.rankScore = opp.RankScore
		} else if d.entered && truncated && preTopN > topN {
			d.topNTruncated = true
		}
		s.logger.Printf("[perp-perp-diag] %s venues=%d legs=[%s] paired=%t spreadAPR=%.2f%% entered=%t netEV=%.4f rankScore=%.4f inTop%d=%t topNTruncated=%t reject=%q | cost: chosen_notional=%.2f notional=%.2f spreadEV=%.4f open=%.4f close=%.4f slip=%.4f obsRisk=%.4f policy=%.4f",
			d.symbol, d.venueCount, strings.Join(d.legs, "; "), d.paired, d.spreadAPR*100,
			d.entered, d.netEV, d.rankScore, topN, d.inTopN, d.topNTruncated, d.rejectReason,
			d.chosenNotional, d.notional, d.spreadEV, d.openCost, d.closeCost, d.slippage, d.observableRisk, d.policyBuffer)
	}
}

// computeCrossVenueOpportunities 是无扫描器状态的包级入口(单测);不做持续性/平滑,行为与改造前一致。
func computeCrossVenueOpportunities(byVenue map[string][]venue.FundingSnapshot, minSpread float64, topN int) []CrossVenueOpportunity {
	if topN <= 0 {
		topN = 30
	}
	bySymbol := indexSnapshotsBySymbolRaw(byVenue)
	opps := make([]CrossVenueOpportunity, 0, len(bySymbol))
	for sym, entries := range bySymbol {
		if len(entries) < 2 {
			continue
		}
		opp, ok := bestPairSpread(sym, entries)
		if !ok || opp.SpreadAPR < minSpread {
			continue
		}
		opp.Symbol = sym
		opp.TradableNotional = 50
		opp = crossVenueEstimateOpportunityMetrics(CrossVenueExecutorConfig{MaxPositionUSDT: 50, MaxEffectiveLeverage: 2, MarginSafetyFactor: 1.5, EVExpectedSlippageBps: 3}, opp, time.Now().UTC())
		opps = append(opps, opp)
	}
	crossVenueFinalizeOpportunityRanking(CrossVenueExecutorConfig{MaxPositionUSDT: 50, MaxEffectiveLeverage: 2, MarginSafetyFactor: 1.5, EVExpectedSlippageBps: 3}, opps)
	sort.Slice(opps, func(i, j int) bool {
		if opps[i].RankScore == opps[j].RankScore {
			return opps[i].Symbol < opps[j].Symbol
		}
		return opps[i].RankScore > opps[j].RankScore
	})
	return opps
}

func indexSnapshotsBySymbolRaw(byVenue map[string][]venue.FundingSnapshot) map[string][]symbolVenueFunding {
	out := make(map[string][]symbolVenueFunding)
	for venueName, snaps := range byVenue {
		for _, snap := range snaps {
			sym := string(snap.Symbol)
			if sym == "" {
				continue
			}
			pairKey, econBase, mult, multKnown := crossVenueEconomicPairKey(sym)
			indexKey := pairKey
			if !multKnown {
				indexKey = sym
			}
			interval := snap.IntervalHours
			if interval <= 0 {
				interval = 8
			}
			rate := snap.LastFundingRate
			out[indexKey] = append(out[indexKey], symbolVenueFunding{
				Venue:            venueName,
				CanonicalSymbol:  sym,
				EconomicPairKey:  pairKey,
				EconomicBase:     econBase,
				Multiplier:       mult,
				MultKnown:        multKnown,
				FundingRate:      rate,
				RawFundingRate:   rate,
				AnnualizedAPR:    fundingAnnualizedAPR(rate, interval),
				RawAnnualizedAPR: fundingAnnualizedAPR(rate, interval),
				IntervalHours:    interval,
				NextFundingTime:  snap.NextFundingTime,
			})
		}
	}
	return out
}

func (s *CrossVenueScanner) maybeAlert(opp CrossVenueOpportunity) {
	if s == nil || s.logger == nil || opp.SpreadAPR < s.cfg.AlertSpreadAPR {
		return
	}
	key := opp.Symbol + "|" + opp.HighVenue + "|" + opp.LowVenue
	now := s.nowFn()
	s.alertMu.Lock()
	if s.lastAlert == nil {
		s.lastAlert = make(map[string]time.Time)
	}
	if last, ok := s.lastAlert[key]; ok && now.Sub(last) < crossVenueAlertCooldown {
		s.alertMu.Unlock()
		return
	}
	s.lastAlert[key] = now
	s.alertMu.Unlock()

	s.logger.Printf("[跨所扫描] 价差告警(纯观测) %s 年化价差 %.1f%% 空 %s(%.1f%%/年,间隔 %dh) 多 %s(%.1f%%/年,间隔 %dh) 策略:空高费所、多低费所对冲(未下单)",
		opp.Symbol,
		opp.SpreadAPR*100,
		opp.HighVenue, opp.HighAnnualizedAPR*100, opp.HighIntervalHours,
		opp.LowVenue, opp.LowAnnualizedAPR*100, opp.LowIntervalHours)
}
