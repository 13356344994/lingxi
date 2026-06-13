package app

import (
	"context"
	"fmt"
	"log"
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
)

// crossVenueNames 扫描目标所(固定四所)。
var crossVenueNames = []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName}

// crossVenueScannerFactory 可注入(单测替换 fake,避免触网)。
var crossVenueScannerFactory = buildCrossVenueScannerAdapters

// symbolVenueFunding 是某 symbol 在某所的一条资金费记录(含年化)。
type symbolVenueFunding struct {
	Venue           string
	FundingRate     float64
	AnnualizedAPR   float64
	IntervalHours   int
	NextFundingTime time.Time
}

// CrossVenueOpportunity 是一条跨所资金费价差机会(策略:空高费所、多低费所)。
type CrossVenueOpportunity struct {
	Symbol              string    `json:"symbol"`
	HighVenue           string    `json:"high_venue"`
	LowVenue            string    `json:"low_venue"`
	HighAnnualizedAPR   float64   `json:"high_annualized_apr"`
	LowAnnualizedAPR    float64   `json:"low_annualized_apr"`
	SpreadAPR           float64   `json:"spread_apr"`
	HighIntervalHours   int       `json:"high_interval_hours"`
	LowIntervalHours    int       `json:"low_interval_hours"`
	HighNextFundingTime time.Time `json:"high_next_funding_time"`
	LowNextFundingTime  time.Time `json:"low_next_funding_time"`
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
	Rate            float64   `json:"rate"`
	IntervalHours   int       `json:"interval_hours"`
	NextFundingTime time.Time `json:"next_funding_time"`
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
	recordDir string
	venues    map[string]venue.Venue
	logger    *log.Logger
	alerts    *AlertDispatcher

	mu           sync.RWMutex
	lastSnapshot CrossVenueScanSnapshot

	alertMu    sync.Mutex
	lastAlert  map[string]time.Time
	nowFn      func() time.Time
	sleepFn    func(time.Duration)
	fetchVenue func(ctx context.Context, name string, adp venue.Venue) ([]venue.FundingSnapshot, error)
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
	return &CrossVenueScanner{
		cfg:        cfg.CrossVenueScanner,
		recordDir:  recordDir,
		venues:     venues,
		logger:     logger,
		alerts:     newCrossVenueAlertDispatcher(cfg.CrossVenueScanner, secretPath, logger),
		lastAlert:  make(map[string]time.Time),
		nowFn:      time.Now,
		sleepFn:    time.Sleep,
		fetchVenue: defaultFetchVenueSnapshot,
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
	opps := computeCrossVenueOpportunities(snapshots, s.cfg.MinSpreadAPR, s.cfg.TopN)
	snapshot := CrossVenueScanSnapshot{
		ScannedAt:        start,
		VenueStatuses:    statuses,
		Opportunities:    opps,
		MinSpreadAPR:     s.cfg.MinSpreadAPR,
		OpportunityCount: len(opps),
		FundingByVenue:   buildCrossVenueFundingIndex(snapshots),
	}
	s.mu.Lock()
	s.lastSnapshot = snapshot
	s.mu.Unlock()

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
		probes := s.probeBorrowForOpportunities(ctx, start, opps)
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

func (s *CrossVenueScanner) probeBorrowForOpportunities(ctx context.Context, scannedAt time.Time, opps []CrossVenueOpportunity) []CrossVenueBorrowProbe {
	topN := s.cfg.BorrowProbeTopN
	if topN <= 0 {
		topN = 10
	}
	if len(opps) > topN {
		opps = opps[:topN]
	}
	probes := make([]CrossVenueBorrowProbe, 0, len(opps)*len(crossVenueNames))
	jsonlPath := filepath.Join(s.recordDir, crossVenueBorrowProbesLog)
	bySymbolSummary := make(map[string][]string, len(opps))

	for _, opp := range opps {
		base := baseAssetFromCanonicalSymbol(opp.Symbol)
		if base == "" {
			continue
		}
		var parts []string
		for _, venueName := range crossVenueNames {
			adp, ok := s.venues[venueName]
			if !ok || adp == nil {
				continue
			}
			if !adp.Capabilities().SupportsBorrowQuote {
				continue
			}
			quote, err := adp.FetchBorrowQuote(ctx, base)
			probe := CrossVenueBorrowProbe{Symbol: opp.Symbol, Venue: venueName}
			if err != nil {
				if err == venue.ErrUnsupported {
					parts = append(parts, venueName+"不支持")
					continue
				}
				probe.Err = err.Error()
				parts = append(parts, fmt.Sprintf("%s失败", venueName))
			} else {
				probe.Available = quote.Available
				probe.MaxBorrowable = quote.MaxBorrowable
				probe.HourlyRateAPR = borrowHourlyRateAPR(quote.HourlyRate)
				if quote.Available {
					parts = append(parts, fmt.Sprintf("%s可借(年化%.0f%%)", venueName, probe.HourlyRateAPR*100))
				} else {
					parts = append(parts, venueName+"不可借")
				}
			}
			probes = append(probes, probe)
			rec := borrowProbeJSONLRecord{
				ScannedAt:     scannedAt,
				Symbol:        opp.Symbol,
				BaseAsset:     base,
				Venue:         venueName,
				Available:     probe.Available,
				MaxBorrowable: probe.MaxBorrowable,
				HourlyRateAPR: probe.HourlyRateAPR,
				Err:           probe.Err,
			}
			if err == nil {
				rec.HourlyRate = quote.HourlyRate
			}
			if werr := appendJSONLRecord(jsonlPath, rec); werr != nil {
				s.logger.Printf("[借币探测] 落盘失败 path=%s: %v", jsonlPath, werr)
			}
			if s.sleepFn != nil {
				s.sleepFn(crossVenueBorrowProbeGap)
			}
		}
		if len(parts) > 0 {
			bySymbolSummary[base] = parts
		}
	}
	for base, parts := range bySymbolSummary {
		s.logger.Printf("[借币探测] %s: %s", base, strings.Join(parts, " "))
	}
	return probes
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

// indexSnapshotsBySymbol 把各所快照按 canonical symbol 索引。
func indexSnapshotsBySymbol(byVenue map[string][]venue.FundingSnapshot) map[string][]symbolVenueFunding {
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
			out[sym] = append(out[sym], symbolVenueFunding{
				Venue:           venueName,
				FundingRate:     snap.LastFundingRate,
				AnnualizedAPR:   fundingAnnualizedAPR(snap.LastFundingRate, interval),
				IntervalHours:   interval,
				NextFundingTime: snap.NextFundingTime,
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
			if b.AnnualizedAPR > a.AnnualizedAPR {
				high, low = b, a
			} else if b.AnnualizedAPR == a.AnnualizedAPR {
				continue
			}
			spread := high.AnnualizedAPR - low.AnnualizedAPR
			if !found || spread > best.SpreadAPR {
				found = true
				best = CrossVenueOpportunity{
					Symbol:              symbol,
					HighVenue:           high.Venue,
					LowVenue:            low.Venue,
					HighAnnualizedAPR:   high.AnnualizedAPR,
					LowAnnualizedAPR:    low.AnnualizedAPR,
					SpreadAPR:           spread,
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

// computeCrossVenueOpportunities 聚合各所快照,计算跨所年化价差并过滤/排序/截断。
func computeCrossVenueOpportunities(byVenue map[string][]venue.FundingSnapshot, minSpread float64, topN int) []CrossVenueOpportunity {
	if topN <= 0 {
		topN = 30
	}
	bySymbol := indexSnapshotsBySymbol(byVenue)
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
		opps = append(opps, opp)
	}
	sort.Slice(opps, func(i, j int) bool {
		if opps[i].SpreadAPR == opps[j].SpreadAPR {
			return opps[i].Symbol < opps[j].Symbol
		}
		return opps[i].SpreadAPR > opps[j].SpreadAPR
	})
	if len(opps) > topN {
		opps = opps[:topN]
	}
	return opps
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
