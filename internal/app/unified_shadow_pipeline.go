package app

import (
	"context"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// unified_shadow_pipeline.go 统一影子管线: 单所/跨所 spot-perp 扫描 + shadow 分配器, 仅写 JSONL 诊断。

type unifiedShadowFetchMarketsFn func(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap

type unifiedShadowFetchAvailableUSDTFn func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64

type unifiedShadowPipelineFetchDeps struct {
	fetchMarkets        unifiedShadowFetchMarketsFn
	fetchAvailableUSDT  unifiedShadowFetchAvailableUSDTFn
}

// unifiedShadowPipelinePassInput 是纯函数 orchestration 输入(单轮扫描+写盘+分配)。
type unifiedShadowPipelinePassInput struct {
	CycleID               string
	SnapshotID            string
	Now                   time.Time
	ExecCfg               CrossVenueExecutorConfig
	RecordDir             string
	FundingByVenue        map[string]map[string]CrossVenueSpotPerpFundingSnap
	MarketsByVenue        map[string]map[string]CrossVenueSpotPerpMarketSnap
	AvailableUSDTByVenue  map[string]float64
	PerpPerpOpportunities []CrossVenueOpportunity
}

func (e *CrossVenueExecutor) runUnifiedShadowPipeline(ctx context.Context) {
	if e == nil || !e.cfg.CrossVenueExecutor.Enabled {
		return
	}
	// 影子观测管线由 capital_allocator 开关控制:默认关闭(符合 NO-GO 后"默认 off/shadow"纪律)。
	// 纪律路线第 1 步:置 capital_allocator.enabled=true + mode=shadow 即开启 shadow 观测(不下单)。
	if !e.cfg.CapitalAllocator.Enabled {
		if e.logger != nil {
			e.logger.Printf("[统一影子] capital_allocator.enabled=false,影子管线未启动(置 true + mode=shadow 开启观测)")
		}
		return
	}
	if mode := strings.TrimSpace(e.cfg.CapitalAllocator.Mode); mode != "" && mode != "shadow" && e.logger != nil {
		e.logger.Printf("[统一影子] 警告: capital_allocator.mode=%q 请求非影子模式,但实盘资本部署尚未实现,仍按 shadow 运行", mode)
	}

	interval := e.unifiedShadowPipelineTickerInterval()
	if e.logger != nil {
		e.logger.Printf("[统一影子] 已启动: interval=%s", interval)
	}

	lastSnapshotID := ""
	tryOnce := func() {
		defer func() {
			if r := recover(); r != nil && e.logger != nil {
				e.logger.Printf("[统一影子] panic recovered: %v", r)
			}
		}()
		snap := CrossVenueScanSnapshot{}
		if e.scanner != nil {
			snap = e.scanner.Snapshot()
		}
		snapshotID := strings.TrimSpace(snap.SnapshotID)
		if snapshotID == "" || snapshotID == lastSnapshotID {
			return
		}
		lastSnapshotID = snapshotID
		e.runUnifiedShadowPipelineOnce(ctx, snap, unifiedShadowPipelineFetchDeps{})
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	tryOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryOnce()
		}
	}
}

func (e *CrossVenueExecutor) unifiedShadowPipelineTickerInterval() time.Duration {
	interval := 60 * time.Second
	if e != nil && e.scanner != nil {
		scannerInterval := time.Duration(e.scanner.cfg.ScanIntervalMinutes) * time.Minute
		if scannerInterval > interval {
			interval = scannerInterval
		}
	}
	return interval
}

func (e *CrossVenueExecutor) runUnifiedShadowPipelineOnce(ctx context.Context, snap CrossVenueScanSnapshot, deps unifiedShadowPipelineFetchDeps) {
	defer func() {
		if r := recover(); r != nil && e != nil && e.logger != nil {
			e.logger.Printf("[统一影子] pass panic recovered: %v", r)
		}
	}()
	if e == nil {
		return
	}

	if deps.fetchMarkets == nil {
		deps.fetchMarkets = defaultUnifiedShadowFetchMarketsByVenue
	}
	if deps.fetchAvailableUSDT == nil {
		deps.fetchAvailableUSDT = defaultUnifiedShadowFetchAvailableUSDTByVenue
	}

	now := time.Now().UTC()
	if e.nowFn != nil {
		now = e.nowFn().UTC()
	}
	if !snap.ScannedAt.IsZero() {
		now = snap.ScannedAt.UTC()
	}

	fundingByVenue := unifiedShadowFundingByVenueFromSnapshot(snap.FundingByVenue)
	symbols := crossVenueSpotPerpCollectSymbols(fundingByVenue, nil)
	marketsByVenue := deps.fetchMarkets(ctx, e.venues, symbols)
	availableUSDTByVenue := deps.fetchAvailableUSDT(ctx, e.venues, crossVenueExecutorVenues)

	runUnifiedShadowPipelinePass(unifiedShadowPipelinePassInput{
		CycleID:               crossVenueDecisionCycleID(now),
		SnapshotID:            snap.SnapshotID,
		Now:                   now,
		ExecCfg:               e.cfg.CrossVenueExecutor,
		RecordDir:             e.recordDir,
		FundingByVenue:        fundingByVenue,
		MarketsByVenue:        marketsByVenue,
		AvailableUSDTByVenue:  availableUSDTByVenue,
		PerpPerpOpportunities: snap.Opportunities,
	})
}

func runUnifiedShadowPipelinePass(in unifiedShadowPipelinePassInput) UnifiedAllocatorPlan {
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	symbols := crossVenueSpotPerpCollectSymbols(in.FundingByVenue, in.MarketsByVenue)

	singleVenueOpps := ScanSingleVenueSpotPerpOpportunities(SingleVenueSpotPerpScanInput{
		SnapshotID:           in.SnapshotID,
		Now:                  now,
		Symbols:              symbols,
		Venues:               crossVenueExecutorVenues,
		FundingByVenue:       in.FundingByVenue,
		MarketsByVenue:       in.MarketsByVenue,
		AvailableUSDTByVenue: in.AvailableUSDTByVenue,
		Config:               DefaultSingleVenueSpotPerpScannerConfig(in.ExecCfg),
	})
	crossVenueOpps := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID:           in.SnapshotID,
		Now:                  now,
		Symbols:              symbols,
		FundingByVenue:       in.FundingByVenue,
		MarketsByVenue:       in.MarketsByVenue,
		AvailableUSDTByVenue: in.AvailableUSDTByVenue,
		Config:               DefaultCrossVenueSpotPerpScannerConfig(in.ExecCfg),
	})

	AppendSingleVenueSpotPerpShadowCandidates(in.RecordDir, in.CycleID, in.ExecCfg, singleVenueOpps)
	AppendCrossVenueSpotPerpShadowCandidates(in.RecordDir, in.CycleID, in.ExecCfg, crossVenueOpps)

	allocatorOpps := make([]UnifiedOpportunity, 0, len(singleVenueOpps)+len(crossVenueOpps)+len(in.PerpPerpOpportunities))
	allocatorOpps = append(allocatorOpps, singleVenueOpps...)
	allocatorOpps = append(allocatorOpps, crossVenueOpps...)
	for _, opp := range in.PerpPerpOpportunities {
		allocatorOpps = append(allocatorOpps, UnifiedOpportunityFromCrossVenue(in.ExecCfg, opp, nil, 0, 0))
	}

	totalCapitalUSDT := 0.0
	for _, avail := range in.AvailableUSDTByVenue {
		totalCapitalUSDT += avail
	}

	return RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              in.CycleID,
		Opportunities:        allocatorOpps,
		AvailableUSDTByVenue: in.AvailableUSDTByVenue,
		TotalCapitalUSDT:     totalCapitalUSDT,
		Config:               DefaultUnifiedAllocatorConfig(),
		RecordDir:            in.RecordDir,
		Now:                  now,
	})
}

func unifiedShadowFundingByVenueFromSnapshot(src map[string]map[string]CrossVenueFundingEntry) map[string]map[string]CrossVenueSpotPerpFundingSnap {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]map[string]CrossVenueSpotPerpFundingSnap, len(src))
	for venueName, bySym := range src {
		if len(bySym) == 0 {
			continue
		}
		dstBySym := make(map[string]CrossVenueSpotPerpFundingSnap, len(bySym))
		for sym, entry := range bySym {
			dstBySym[sym] = CrossVenueSpotPerpFundingSnap{
				Rate:            entry.Rate,
				IntervalHours:   entry.IntervalHours,
				NextFundingTime: entry.NextFundingTime,
			}
		}
		out[venueName] = dstBySym
	}
	return out
}

func defaultUnifiedShadowFetchMarketsByVenue(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
	out := make(map[string]map[string]CrossVenueSpotPerpMarketSnap)
	for venueName, v := range venues {
		if v == nil {
			continue
		}
		bySym := make(map[string]CrossVenueSpotPerpMarketSnap)
		for _, sym := range symbols {
			sym = strings.TrimSpace(sym)
			if sym == "" {
				continue
			}
			mkt := CrossVenueSpotPerpMarketSnap{}
			if spotBT, err := v.FetchBookTicker(ctx, venue.MarketSpot, venue.Symbol(sym)); err == nil {
				mid := (spotBT.BidPrice + spotBT.AskPrice) / 2
				if mid > 0 {
					mkt.SpotMid = mid
					mkt.HasSpot = true
				}
			}
			if perpBT, err := v.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(sym)); err == nil {
				mid := (perpBT.BidPrice + perpBT.AskPrice) / 2
				if mid > 0 {
					mkt.PerpMid = mid
					mkt.HasPerp = true
				}
			}
			if mkt.HasSpot || mkt.HasPerp {
				bySym[sym] = mkt
			}
		}
		if len(bySym) > 0 {
			out[venueName] = bySym
		}
	}
	return out
}

func defaultUnifiedShadowFetchAvailableUSDTByVenue(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
	out := make(map[string]float64, len(venueNames))
	for _, venueName := range venueNames {
		v := venues[venueName]
		if v == nil {
			continue
		}
		avail, err := fetchVenuePerpAvailableUSDT(ctx, v)
		if err != nil {
			continue
		}
		out[venueName] = avail
	}
	return out
}
