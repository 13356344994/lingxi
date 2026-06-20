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
)

// crossVenueNames 扫描目标所(固定四所)。
var crossVenueNames = []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName}

// crossVenueScannerFactory 可注入(单测替换 fake,避免触网)。
var crossVenueScannerFactory = buildCrossVenueScannerAdapters

// symbolVenueFunding 是某 symbol 在某所的一条资金费记录(含年化)。
type symbolVenueFunding struct {
	Venue                   string
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
			interval := snap.IntervalHours
			if interval <= 0 {
				interval = 8
			}
			nextFunding, inferred := crossVenueResolveNextFundingTime(now, snap.NextFundingTime, interval)
			rawRate := snap.LastFundingRate
			rate := capFundingRateByAPR(rawRate, interval, capAPR)
			out[sym] = append(out[sym], symbolVenueFunding{
				Venue:                   venueName,
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

// bestPairSpread 对同一 symbol 的多所记录取最优两两价差(年化高所 − 年化低所)。
func bestPairSpread(symbol string, entries []symbolVenueFunding) (CrossVenueOpportunity, bool) {
	if len(entries) < 2 {
		return CrossVenueOpportunity{}, false
	}
	best := CrossVenueOpportunity{Symbol: symbol}
	found := false
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			a, b := entries[i], entries[j]
			high, low := a, b
			// 用原始(未封顶)年化排序/算价差:若用单腿封顶后的 APR,多条腿同时超过封顶值会被压成相同上限,
			// 真实价差被抹平为 0(H-USDT 这类四腿全爆表的极端机会就因此漏配)。荒谬上限改由 spread 级 absurdCap 兜底。
			if b.RawAnnualizedAPR > a.RawAnnualizedAPR {
				high, low = b, a
			} else if b.RawAnnualizedAPR == a.RawAnnualizedAPR {
				continue
			}
			spread := high.RawAnnualizedAPR - low.RawAnnualizedAPR
			if !found || spread > best.SpreadAPR {
				found = true
				best = CrossVenueOpportunity{
					Symbol:              symbol,
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

// computeCrossVenueOpportunities 聚合各所快照,用近 N 次中位数+APR 封顶算价差,过滤/排序/截断。
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
		highSamples := s.fundingHistorySampleCount(opp.HighVenue, sym)
		lowSamples := s.fundingHistorySampleCount(opp.LowVenue, sym)
		minSamples := minInt(highSamples, lowSamples)
		windowHours := crossVenueComparableWindowHours(s.nowFn(), opp.HighNextFundingTime, opp.LowNextFundingTime)
		opp.HistoryQualityScore = crossVenueHistoryQualityScore(minSamples, s.cfg.SpreadPersistenceScans)
		opp.StabilityScore = crossVenueStabilityScore(windowHours, minSamples)
		opp.Symbol = sym
		opp.TradableNotional = crossVenuePerpPerpScanTradableNotional(ctx, s.execCfg, s.venues, opp.HighVenue, opp.LowVenue, sym)
		if opp.ExtremeSpread && opp.TradableNotional <= 0 {
			s.notePerpPerpDiagReject(diagBySym, sym, "extreme spread but tradableNotional<=0")
			continue
		}
		evHigh, evLow := crossVenueExtremeSpreadEVRates(s.cfg, opp, entries)
		opp = crossVenueEstimateOpportunityMetricsEVRates(s.execCfg, opp, s.nowFn(), evHigh, evLow)
		s.notePerpPerpDiagEntered(diagBySym, sym, opp)
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
	if truncated {
		opps = opps[:topN]
	}
	s.finalizePerpPerpDiagRound(diagBySym, opps, preTopN, topN, truncated)
	return opps
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

func (s *CrossVenueScanner) notePerpPerpDiagEntered(diag map[string]*perpPerpDiagRound, sym string, opp CrossVenueOpportunity) {
	if d := diag[sym]; d != nil {
		d.entered = true
		d.netEV = opp.NetEV
		d.rankScore = opp.RankScore
		d.notional = opp.TradableNotional
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
	topSyms := make(map[string]CrossVenueOpportunity, len(opps))
	for _, opp := range opps {
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
		s.logger.Printf("[perp-perp-diag] %s venues=%d legs=[%s] paired=%t spreadAPR=%.2f%% entered=%t netEV=%.4f rankScore=%.4f inTop%d=%t topNTruncated=%t reject=%q | cost: notional=%.2f spreadEV=%.4f open=%.4f close=%.4f slip=%.4f obsRisk=%.4f policy=%.4f",
			d.symbol, d.venueCount, strings.Join(d.legs, "; "), d.paired, d.spreadAPR*100,
			d.entered, d.netEV, d.rankScore, topN, d.inTopN, d.topNTruncated, d.rejectReason,
			d.notional, d.spreadEV, d.openCost, d.closeCost, d.slippage, d.observableRisk, d.policyBuffer)
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
	if len(opps) > topN {
		opps = opps[:topN]
	}
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
			interval := snap.IntervalHours
			if interval <= 0 {
				interval = 8
			}
			rate := snap.LastFundingRate
			out[sym] = append(out[sym], symbolVenueFunding{
				Venue:            venueName,
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
