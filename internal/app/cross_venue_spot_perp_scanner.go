package app

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	crossVenueSpotPerpDefaultSpotFeeBps          = 10.0
	crossVenueSpotPerpDefaultPeakFundingHaircut  = 0.50
	crossVenueSpotPerpDefaultBasisConvergence    = 0.50
	crossVenueSpotPerpDefaultBasisWidenStressBps = 5.0
	crossVenueSpotPerpDefaultRebalanceCostBps    = 3.0
	crossVenueSpotPerpDefaultRebalanceLatencyH   = 2.0
	crossVenueSpotPerpDefaultPerpMaintMarginRate = 0.005
	crossVenueSpotPerpDefaultPerpWalletBuffer    = 0.10
)

// CrossVenueSpotPerpFundingSnap 是某所某 symbol 的永续资金费快照(扫描输入)。
type CrossVenueSpotPerpFundingSnap struct {
	Rate            float64   `json:"rate"`
	IntervalHours   int       `json:"interval_hours"`
	NextFundingTime time.Time `json:"next_funding_time"`
	// SpikeThreshold 尖峰衰减阈值(通常为历史 |rate| p90); <=0 时不做尖峰裁剪但会降低 data_quality。
	SpikeThreshold float64 `json:"spike_threshold,omitempty"`
}

// CrossVenueSpotPerpMarketSnap 是某所某 symbol 的现货/永续中间价与深度容量(扫描输入)。
type CrossVenueSpotPerpMarketSnap struct {
	SpotMid       float64 `json:"spot_mid"`
	PerpMid       float64 `json:"perp_mid"`
	SpotDepthUSDT float64 `json:"spot_depth_usdt"`
	PerpDepthUSDT float64 `json:"perp_depth_usdt"`
	HasSpot       bool    `json:"has_spot"`
	HasPerp       bool    `json:"has_perp"`
}

// CrossVenueSpotPerpScannerConfig 跨所 spot-perp 扫描器成本与风控参数(v1 保守默认)。
type CrossVenueSpotPerpScannerConfig struct {
	Exec CrossVenueExecutorConfig `json:"exec"`

	SpotTakerFeeBps          float64 `json:"spot_taker_fee_bps"`
	PeakFundingHaircut       float64 `json:"peak_funding_haircut"`
	BasisConvergenceFraction float64 `json:"basis_convergence_fraction"`
	BasisWideningStressBps   float64 `json:"basis_widening_stress_bps"`
	RebalanceTransferCostBps float64 `json:"rebalance_transfer_cost_bps"`
	RebalanceLatencyHours    float64 `json:"rebalance_latency_hours"`
	PerpMaintenanceMarginRate float64 `json:"perp_maintenance_margin_rate"`
	PerpWalletBufferRatio    float64 `json:"perp_wallet_buffer_ratio"`
	ReserveUSDTByVenue       map[string]float64 `json:"reserve_usdt_by_venue,omitempty"`
	DefaultReserveUSDT       float64 `json:"default_reserve_usdt"`
	ExpectedHoldHours        float64 `json:"expected_hold_hours"`
	PriceShockPcts           []float64 `json:"price_shock_pcts"`
}

// DefaultCrossVenueSpotPerpScannerConfig 返回 v1 保守默认(对齐 go_no_go 口径)。
func DefaultCrossVenueSpotPerpScannerConfig(exec CrossVenueExecutorConfig) CrossVenueSpotPerpScannerConfig {
	return CrossVenueSpotPerpScannerConfig{
		Exec:                     exec,
		SpotTakerFeeBps:          crossVenueSpotPerpDefaultSpotFeeBps,
		PeakFundingHaircut:       crossVenueSpotPerpDefaultPeakFundingHaircut,
		BasisConvergenceFraction: crossVenueSpotPerpDefaultBasisConvergence,
		BasisWideningStressBps:   crossVenueSpotPerpDefaultBasisWidenStressBps,
		RebalanceTransferCostBps: crossVenueSpotPerpDefaultRebalanceCostBps,
		RebalanceLatencyHours:    crossVenueSpotPerpDefaultRebalanceLatencyH,
		PerpMaintenanceMarginRate: crossVenueSpotPerpDefaultPerpMaintMarginRate,
		PerpWalletBufferRatio:    crossVenueSpotPerpDefaultPerpWalletBuffer,
		PriceShockPcts:           []float64{0.20, 0.30},
	}
}

// CrossVenueSpotPerpScanInput 是纯函数扫描器输入。
type CrossVenueSpotPerpScanInput struct {
	SnapshotID           string
	Now                  time.Time
	Symbols              []string
	FundingByVenue       map[string]map[string]CrossVenueSpotPerpFundingSnap
	MarketsByVenue       map[string]map[string]CrossVenueSpotPerpMarketSnap
	AvailableUSDTByVenue map[string]float64
	Config               CrossVenueSpotPerpScannerConfig
}

// ScanCrossVenueSpotPerpOpportunities 扫描跨所 spot-perp 机会(纯函数, shadow-only)。
// v1: 仅正费率 carry(A 所买现货 + B 所空永续), 不借币、不做空现货。
func ScanCrossVenueSpotPerpOpportunities(in CrossVenueSpotPerpScanInput) []UnifiedOpportunity {
	cfg := normalizeCrossVenueSpotPerpScannerConfig(in.Config)
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	symbols := in.Symbols
	if len(symbols) == 0 {
		symbols = crossVenueSpotPerpCollectSymbols(in.FundingByVenue, in.MarketsByVenue)
	}

	out := make([]UnifiedOpportunity, 0)
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		spotVenues := crossVenueSpotPerpSpotBuyVenues(in.MarketsByVenue, sym)
		perpVenues := crossVenueSpotPerpPositivePerpVenues(in.FundingByVenue, in.MarketsByVenue, sym)
		if len(spotVenues) == 0 || len(perpVenues) == 0 {
			continue
		}
		bestPerpVenue, bestRate := crossVenueSpotPerpBestPositivePerpVenue(perpVenues)
		if bestPerpVenue == "" || bestRate <= 0 {
			continue
		}
		for _, spotVenue := range spotVenues {
			if spotVenue == bestPerpVenue {
				continue
			}
			opp, ok := buildCrossVenueSpotPerpOpportunity(
				in.SnapshotID, now, sym, spotVenue, bestPerpVenue,
				in.FundingByVenue, in.MarketsByVenue, in.AvailableUSDTByVenue, cfg,
			)
			if ok {
				out = append(out, opp)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpectedNetEVUSDT == out[j].ExpectedNetEVUSDT {
			return out[i].ID < out[j].ID
		}
		return out[i].ExpectedNetEVUSDT > out[j].ExpectedNetEVUSDT
	})
	return out
}

// AppendCrossVenueSpotPerpShadowCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(shadow-only)。
func AppendCrossVenueSpotPerpShadowCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		notional := crossVenueSpotPerpLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, false, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

func normalizeCrossVenueSpotPerpScannerConfig(cfg CrossVenueSpotPerpScannerConfig) CrossVenueSpotPerpScannerConfig {
	if cfg.Exec.MaxPositionUSDT <= 0 {
		cfg.Exec.MaxPositionUSDT = 50
	}
	if cfg.Exec.MaxEffectiveLeverage <= 0 {
		cfg.Exec.MaxEffectiveLeverage = 2
	}
	if cfg.Exec.MarginSafetyFactor <= 0 {
		cfg.Exec.MarginSafetyFactor = 1.5
	}
	if cfg.Exec.MidPriceDeviationMax <= 0 {
		cfg.Exec.MidPriceDeviationMax = 0.03
	}
	if cfg.Exec.DepthUsageRatio <= 0 {
		cfg.Exec.DepthUsageRatio = 0.20
	}
	if cfg.Exec.EVExpectedSlippageBps <= 0 {
		cfg.Exec.EVExpectedSlippageBps = 3
	}
	if cfg.SpotTakerFeeBps <= 0 {
		cfg.SpotTakerFeeBps = crossVenueSpotPerpDefaultSpotFeeBps
	}
	if cfg.PeakFundingHaircut <= 0 {
		cfg.PeakFundingHaircut = crossVenueSpotPerpDefaultPeakFundingHaircut
	}
	if cfg.BasisConvergenceFraction <= 0 {
		cfg.BasisConvergenceFraction = crossVenueSpotPerpDefaultBasisConvergence
	}
	if cfg.BasisWideningStressBps <= 0 {
		cfg.BasisWideningStressBps = crossVenueSpotPerpDefaultBasisWidenStressBps
	}
	if cfg.RebalanceTransferCostBps <= 0 {
		cfg.RebalanceTransferCostBps = crossVenueSpotPerpDefaultRebalanceCostBps
	}
	if cfg.RebalanceLatencyHours <= 0 {
		cfg.RebalanceLatencyHours = crossVenueSpotPerpDefaultRebalanceLatencyH
	}
	if cfg.PerpMaintenanceMarginRate <= 0 {
		cfg.PerpMaintenanceMarginRate = crossVenueSpotPerpDefaultPerpMaintMarginRate
	}
	if cfg.PerpWalletBufferRatio <= 0 {
		cfg.PerpWalletBufferRatio = crossVenueSpotPerpDefaultPerpWalletBuffer
	}
	if len(cfg.PriceShockPcts) == 0 {
		cfg.PriceShockPcts = []float64{0.20, 0.30}
	}
	return cfg
}

func crossVenueSpotPerpCollectSymbols(funding map[string]map[string]CrossVenueSpotPerpFundingSnap, markets map[string]map[string]CrossVenueSpotPerpMarketSnap) []string {
	seen := make(map[string]struct{})
	for _, bySym := range funding {
		for sym := range bySym {
			seen[sym] = struct{}{}
		}
	}
	for _, bySym := range markets {
		for sym := range bySym {
			seen[sym] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

func crossVenueSpotPerpSpotBuyVenues(markets map[string]map[string]CrossVenueSpotPerpMarketSnap, sym string) []string {
	out := make([]string, 0)
	for venueName, bySym := range markets {
		m, ok := bySym[sym]
		if !ok || !m.HasSpot || m.SpotMid <= 0 {
			continue
		}
		out = append(out, venueName)
	}
	sort.Strings(out)
	return out
}

type crossVenueSpotPerpPerpCandidate struct {
	venue string
	rate  float64
}

func crossVenueSpotPerpPositivePerpVenues(
	funding map[string]map[string]CrossVenueSpotPerpFundingSnap,
	markets map[string]map[string]CrossVenueSpotPerpMarketSnap,
	sym string,
) []crossVenueSpotPerpPerpCandidate {
	out := make([]crossVenueSpotPerpPerpCandidate, 0)
	for venueName, bySym := range funding {
		fund, ok := bySym[sym]
		if !ok || fund.Rate <= 0 {
			continue
		}
		mkt, ok := markets[venueName][sym]
		if !ok || !mkt.HasPerp || mkt.PerpMid <= 0 {
			continue
		}
		out = append(out, crossVenueSpotPerpPerpCandidate{venue: venueName, rate: fund.Rate})
	}
	return out
}

func crossVenueSpotPerpBestPositivePerpVenue(candidates []crossVenueSpotPerpPerpCandidate) (string, float64) {
	bestVenue := ""
	bestRate := 0.0
	for _, c := range candidates {
		if c.rate > bestRate || (c.rate == bestRate && (bestVenue == "" || c.venue < bestVenue)) {
			bestVenue = c.venue
			bestRate = c.rate
		}
	}
	return bestVenue, bestRate
}

func buildCrossVenueSpotPerpOpportunity(
	snapshotID string,
	now time.Time,
	symbol, spotVenue, perpVenue string,
	funding map[string]map[string]CrossVenueSpotPerpFundingSnap,
	markets map[string]map[string]CrossVenueSpotPerpMarketSnap,
	available map[string]float64,
	cfg CrossVenueSpotPerpScannerConfig,
) (UnifiedOpportunity, bool) {
	spotMkt := markets[spotVenue][symbol]
	perpMkt := markets[perpVenue][symbol]
	fund := funding[perpVenue][symbol]

	if !spotMkt.HasSpot || spotMkt.SpotMid <= 0 || !perpMkt.HasPerp || perpMkt.PerpMid <= 0 || fund.Rate <= 0 {
		return UnifiedOpportunity{}, false
	}

	midDev := math.Abs(perpMkt.PerpMid/spotMkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		return UnifiedOpportunity{}, false
	}

	notional := crossVenueSpotPerpTradableNotional(cfg, spotMkt, perpMkt)
	if notional <= 0 {
		return UnifiedOpportunity{}, false
	}

	if crossVenueSpotPerpPerpLiquidatedAtShock(cfg, notional) {
		return UnifiedOpportunity{}, false
	}

	holdHours := cfg.ExpectedHoldHours
	if holdHours <= 0 {
		holdHours = crossVenueSpotPerpExpectedHoldHours(now, fund)
	}
	if holdHours <= 0 {
		return UnifiedOpportunity{}, false
	}

	settlements := crossVenueSpotPerpFundingSettlementCount(now, holdHours, fund.IntervalHours, fund.NextFundingTime)
	if settlements <= 0 {
		return UnifiedOpportunity{}, false
	}

	effectiveRate := crossVenueSpotPerpEffectiveFundingRate(fund.Rate, fund.SpikeThreshold, cfg.PeakFundingHaircut)
	expectedFunding := notional * effectiveRate * float64(settlements)

	entryBasisUSDT := crossVenueSpotPerpEntryBasisUSDT(spotMkt.SpotMid, perpMkt.PerpMid, notional)
	expectedBasisPnL := crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional, cfg)

	openCost := crossVenueSpotPerpOpenCostUSDT(cfg, spotVenue, perpVenue, notional)
	closeCost := openCost
	slippage := crossVenueSpotPerpSlippageUSDT(cfg, notional)

	spotCashRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cfg, notional)

	rebalancePenalty := 0.0
	requiresRebalance := crossVenueSpotPerpRequiresRebalance(
		spotVenue, perpVenue, spotCashRequired, perpMarginRequired, available, cfg,
	)
	if requiresRebalance {
		rebalancePenalty = notional * cfg.RebalanceTransferCostBps / 10000
	}

	netEV := expectedFunding + expectedBasisPnL - openCost - closeCost - slippage - rebalancePenalty

	refPrice := (spotMkt.SpotMid + perpMkt.PerpMid) / 2
	qty := notional / refPrice

	dataQuality := crossVenueSpotPerpDataQualityScore(fund)
	executionScore := crossVenueExecutionScore(slippage, notional)
	liquidityScore := crossVenueSpotPerpLiquidityScore(spotMkt, perpMkt, notional, holdHours)
	riskScore := crossVenueSpotPerpRiskScore(cfg, requiresRebalance, midDev, dataQuality)

	totalCapital := spotCashRequired + perpMarginRequired
	capitalEfficiency := 0.0
	if totalCapital > 0 {
		capitalEfficiency = netEV / totalCapital
	}

	rejectReasons := make([]string, 0)
	if fund.SpikeThreshold <= 0 {
		rejectReasons = append(rejectReasons, "insufficient_funding_history")
	}
	if requiresRebalance {
		rejectReasons = append(rejectReasons, "requires_rebalance")
	}
	if netEV <= 0 {
		rejectReasons = append(rejectReasons, "net_ev<=0")
	}

	return UnifiedOpportunity{
		ID: unifiedOpportunityID(UnifiedStrategyCrossVenueSpotPerp, symbol, snapshotID, spotVenue, perpVenue),
		StrategyType: UnifiedStrategyCrossVenueSpotPerp,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: spotVenue, Market: "spot", Side: "buy", Qty: qty, Notional: notional},
			{Venue: perpVenue, Market: "perp", Side: "short", Qty: qty, Notional: notional},
		},
		ExpectedFundingUSDT:      expectedFunding,
		ExpectedOpenCostUSDT:     openCost,
		ExpectedCloseCostUSDT:    closeCost,
		ExpectedSlippageUSDT:     slippage,
		ExpectedBorrowCostUSDT:   0,
		ExpectedRollbackCostUSDT: rebalancePenalty,
		ExpectedNetEVUSDT:        netEV,
		ExpectedHoldHours:        holdHours,
		CapitalRequiredByVenue: map[string]float64{
			spotVenue: spotCashRequired,
			perpVenue: perpMarginRequired,
		},
		RiskScore:              riskScore,
		ExecutionScore:         executionScore,
		LiquidityScore:         liquidityScore,
		DataQualityScore:       dataQuality,
		CapitalEfficiencyScore: capitalEfficiency,
		RejectReasons:          rejectReasons,
		RequiresRebalance:      requiresRebalance,
	}, true
}

func crossVenueSpotPerpTradableNotional(cfg CrossVenueSpotPerpScannerConfig, spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) float64 {
	maxNotional := cfg.Exec.MaxPositionUSDT
	if maxNotional <= 0 {
		maxNotional = 50
	}
	if spotMkt.SpotDepthUSDT > 0 {
		cap := spotMkt.SpotDepthUSDT * cfg.Exec.DepthUsageRatio
		if cap+1e-9 < maxNotional {
			return 0
		}
		maxNotional = math.Min(maxNotional, cap)
	}
	if perpMkt.PerpDepthUSDT > 0 {
		cap := perpMkt.PerpDepthUSDT * cfg.Exec.DepthUsageRatio
		if cap+1e-9 < cfg.Exec.MaxPositionUSDT {
			return 0
		}
		maxNotional = math.Min(maxNotional, cap)
	}
	if maxNotional <= 0 {
		return 0
	}
	return maxNotional
}

func crossVenueSpotPerpExpectedHoldHours(now time.Time, fund CrossVenueSpotPerpFundingSnap) float64 {
	window := crossVenueComparableWindowHours(now, fund.NextFundingTime, fund.NextFundingTime)
	if window > 0 {
		return window
	}
	interval := fund.IntervalHours
	if interval <= 0 {
		interval = 8
	}
	return float64(interval)
}

func crossVenueSpotPerpFundingSettlementCount(now time.Time, holdHours float64, intervalHours int, nextFunding time.Time) int {
	if holdHours <= 0 {
		return 0
	}
	if intervalHours <= 0 {
		intervalHours = 8
	}
	holdEnd := now.Add(time.Duration(holdHours * float64(time.Hour)))
	count := 0
	t := nextFunding
	if t.IsZero() || !t.After(now) {
		t = now.Add(time.Duration(intervalHours) * time.Hour)
	}
	for !t.After(holdEnd) {
		if !t.Before(now) {
			count++
		}
		t = t.Add(time.Duration(intervalHours) * time.Hour)
	}
	return count
}

func crossVenueSpotPerpEffectiveFundingRate(rate, spikeThreshold, peakHaircut float64) float64 {
	if rate <= 0 {
		return 0
	}
	if spikeThreshold <= 0 {
		return rate
	}
	if rate <= spikeThreshold {
		return rate
	}
	return spikeThreshold + peakHaircut*(rate-spikeThreshold)
}

func crossVenueSpotPerpEntryBasisUSDT(spotMid, perpMid, notional float64) float64 {
	if spotMid <= 0 || notional <= 0 {
		return 0
	}
	return (perpMid/spotMid - 1) * notional
}

func crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional float64, cfg CrossVenueSpotPerpScannerConfig) float64 {
	stress := notional * cfg.BasisWideningStressBps / 10000
	if entryBasisUSDT >= 0 {
		return entryBasisUSDT*cfg.BasisConvergenceFraction - stress
	}
	return entryBasisUSDT - stress
}

func crossVenueSpotPerpOpenCostUSDT(cfg CrossVenueSpotPerpScannerConfig, spotVenue, perpVenue string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	spotFee := cfg.SpotTakerFeeBps
	perpFee := crossVenueTakerFeeBps(cfg.Exec, perpVenue)
	return notional * (spotFee + perpFee) / 10000
}

func crossVenueSpotPerpSlippageUSDT(cfg CrossVenueSpotPerpScannerConfig, notional float64) float64 {
	return crossVenueExpectedSlippageUSDT(cfg.Exec, notional, 4)
}

func crossVenueSpotPerpPerpMarginRequired(cfg CrossVenueSpotPerpScannerConfig, notional float64) float64 {
	margin := crossVenuePerVenueMarginUSDT(cfg.Exec, notional)
	buffer := notional * cfg.PerpWalletBufferRatio
	return margin + buffer
}

func crossVenueSpotPerpPerpLiquidatedAtShock(cfg CrossVenueSpotPerpScannerConfig, notional float64) bool {
	for _, shock := range cfg.PriceShockPcts {
		if shock <= 0 {
			continue
		}
		if crossVenueSpotPerpPerpWalletLiquidated(cfg, notional, shock) {
			return true
		}
	}
	return false
}

func crossVenueSpotPerpPerpWalletLiquidated(cfg CrossVenueSpotPerpScannerConfig, notional, shockPct float64) bool {
	if notional <= 0 {
		return false
	}
	lev := cfg.Exec.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	initialMargin := notional / lev
	buffer := notional * cfg.PerpWalletBufferRatio
	maintenance := notional * cfg.PerpMaintenanceMarginRate
	equity := initialMargin + buffer - shockPct*notional
	return equity < maintenance
}

func crossVenueSpotPerpVenueReserve(venueName string, cfg CrossVenueSpotPerpScannerConfig) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venueName]; ok {
			return math.Max(v, 0)
		}
	}
	return math.Max(cfg.DefaultReserveUSDT, 0)
}

func crossVenueSpotPerpRequiresRebalance(
	spotVenue, perpVenue string,
	spotCashRequired, perpMarginRequired float64,
	available map[string]float64,
	cfg CrossVenueSpotPerpScannerConfig,
) bool {
	spotAvail := available[spotVenue] - crossVenueSpotPerpVenueReserve(spotVenue, cfg)
	perpAvail := available[perpVenue] - crossVenueSpotPerpVenueReserve(perpVenue, cfg)
	return spotAvail+1e-9 < spotCashRequired || perpAvail+1e-9 < perpMarginRequired
}

func crossVenueSpotPerpDataQualityScore(fund CrossVenueSpotPerpFundingSnap) float64 {
	if fund.SpikeThreshold <= 0 {
		return 0.35
	}
	return 0.75
}

func crossVenueSpotPerpLiquidityScore(spotMkt, perpMkt CrossVenueSpotPerpMarketSnap, notional, holdHours float64) float64 {
	score := crossVenueLiquidityScore(notional, holdHours)
	if spotMkt.SpotDepthUSDT > 0 && spotMkt.SpotDepthUSDT*cfgDepthUsageDefault() < notional {
		score *= 0.8
	}
	if perpMkt.PerpDepthUSDT > 0 && perpMkt.PerpDepthUSDT*cfgDepthUsageDefault() < notional {
		score *= 0.8
	}
	return clamp01(score)
}

func cfgDepthUsageDefault() float64 { return 0.20 }

func crossVenueSpotPerpRiskScore(cfg CrossVenueSpotPerpScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.35
	if requiresRebalance {
		risk += 0.20
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func crossVenueSpotPerpLegNotional(opp UnifiedOpportunity) float64 {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return leg.Notional
		}
	}
	return 0
}
