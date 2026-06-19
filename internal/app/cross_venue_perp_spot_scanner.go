package app

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	crossVenuePerpSpotRejectPositiveFunding   = "positive_funding_v1_not_supported"
	crossVenuePerpSpotRejectNoSpotMarket      = "no_spot_market"
	crossVenuePerpSpotRejectInsufficientDepth = "insufficient_depth"
	crossVenuePerpSpotRejectMarginStress      = "margin_stress_liquidation"
	crossVenuePerpSpotRejectNotBorrowable     = "not_borrowable"
	crossVenuePerpSpotRejectBorrowOverLimit   = "borrow_over_limit"
)

// CrossVenuePerpSpotFundingSnap 是某所某 symbol 的永续资金费快照(扫描输入)。
type CrossVenuePerpSpotFundingSnap = CrossVenueSpotPerpFundingSnap

// CrossVenuePerpSpotMarketSnap 是某所某 symbol 的现货/永续中间价与深度容量(扫描输入)。
type CrossVenuePerpSpotMarketSnap = CrossVenueSpotPerpMarketSnap

// CrossVenuePerpSpotScannerConfig 跨所 perp-spot 扫描器成本与风控参数(v1 保守默认)。
type CrossVenuePerpSpotScannerConfig struct {
	Exec CrossVenueExecutorConfig `json:"exec"`

	SpotTakerFeeBps           float64            `json:"spot_taker_fee_bps"`
	PeakFundingHaircut        float64            `json:"peak_funding_haircut"`
	BasisConvergenceFraction  float64            `json:"basis_convergence_fraction"`
	BasisWideningStressBps    float64            `json:"basis_widening_stress_bps"`
	RebalanceTransferCostBps  float64            `json:"rebalance_transfer_cost_bps"`
	RebalanceLatencyHours     float64            `json:"rebalance_latency_hours"`
	PerpMaintenanceMarginRate float64            `json:"perp_maintenance_margin_rate"`
	PerpWalletBufferRatio     float64            `json:"perp_wallet_buffer_ratio"`
	ReserveUSDTByVenue        map[string]float64 `json:"reserve_usdt_by_venue,omitempty"`
	DefaultReserveUSDT        float64            `json:"default_reserve_usdt"`
	ExpectedHoldHours         float64            `json:"expected_hold_hours"`
	PriceShockPcts            []float64          `json:"price_shock_pcts"`
}

// DefaultCrossVenuePerpSpotScannerConfig 返回 v1 保守默认(对齐 go_no_go 口径)。
func DefaultCrossVenuePerpSpotScannerConfig(exec CrossVenueExecutorConfig) CrossVenuePerpSpotScannerConfig {
	return CrossVenuePerpSpotScannerConfig{
		Exec:                      exec,
		SpotTakerFeeBps:           crossVenueSpotPerpDefaultSpotFeeBps,
		PeakFundingHaircut:        crossVenueSpotPerpDefaultPeakFundingHaircut,
		BasisConvergenceFraction:  crossVenueSpotPerpDefaultBasisConvergence,
		BasisWideningStressBps:    crossVenueSpotPerpDefaultBasisWidenStressBps,
		RebalanceTransferCostBps:  crossVenueSpotPerpDefaultRebalanceCostBps,
		RebalanceLatencyHours:     crossVenueSpotPerpDefaultRebalanceLatencyH,
		PerpMaintenanceMarginRate: crossVenueSpotPerpDefaultPerpMaintMarginRate,
		PerpWalletBufferRatio:     crossVenueSpotPerpDefaultPerpWalletBuffer,
		PriceShockPcts:            []float64{0.20, 0.30},
	}
}

// CrossVenuePerpSpotScanInput 是纯函数扫描器输入。
type CrossVenuePerpSpotScanInput struct {
	SnapshotID             string
	Now                    time.Time
	Symbols                []string
	FundingByVenue         map[string]map[string]CrossVenuePerpSpotFundingSnap
	MarketsByVenue         map[string]map[string]CrossVenuePerpSpotMarketSnap
	AvailableUSDTByVenue   map[string]float64
	BorrowDailyRateByAsset map[string]float64
	BorrowLimitUSDTByAsset map[string]float64
	Config                 CrossVenuePerpSpotScannerConfig
}

// ScanCrossVenuePerpSpotOpportunities 扫描跨所 perp-spot 负 carry 机会(纯函数, plan-only)。
// v1: 仅负费率 carry(A 所多永续 + B 所借币空现货), 需借币表提供日利率与限额。
func ScanCrossVenuePerpSpotOpportunities(in CrossVenuePerpSpotScanInput) []UnifiedOpportunity {
	cfg := normalizeCrossVenuePerpSpotScannerConfig(in.Config)
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	symbols := in.Symbols
	if len(symbols) == 0 {
		symbols = crossVenuePerpSpotCollectSymbols(in.FundingByVenue, in.MarketsByVenue)
	}

	out := make([]UnifiedOpportunity, 0)
	drops := newScannerInstrumentDropLog()
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		spotVenues := crossVenuePerpSpotSpotShortVenues(in.MarketsByVenue, sym, drops)
		perpVenues := crossVenuePerpSpotNegativePerpVenues(in.FundingByVenue, in.MarketsByVenue, sym, drops)
		if len(spotVenues) == 0 || len(perpVenues) == 0 {
			continue
		}
		bestPerpVenue, bestRate := crossVenuePerpSpotBestNegativePerpVenue(perpVenues)
		if bestPerpVenue == "" || bestRate >= 0 {
			continue
		}
		for _, spotVenue := range spotVenues {
			if spotVenue == bestPerpVenue {
				continue
			}
			opp, ok := buildCrossVenuePerpSpotOpportunity(
				in.SnapshotID, now, sym, bestPerpVenue, spotVenue,
				in.FundingByVenue, in.MarketsByVenue, in.AvailableUSDTByVenue,
				in.BorrowDailyRateByAsset, in.BorrowLimitUSDTByAsset, cfg,
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

// AppendCrossVenuePerpSpotCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendCrossVenuePerpSpotCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		notional := crossVenuePerpSpotLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

func normalizeCrossVenuePerpSpotScannerConfig(cfg CrossVenuePerpSpotScannerConfig) CrossVenuePerpSpotScannerConfig {
	normalized := CrossVenueSpotPerpScannerConfig{
		Exec:                      cfg.Exec,
		SpotTakerFeeBps:           cfg.SpotTakerFeeBps,
		PeakFundingHaircut:        cfg.PeakFundingHaircut,
		BasisConvergenceFraction:  cfg.BasisConvergenceFraction,
		BasisWideningStressBps:    cfg.BasisWideningStressBps,
		RebalanceTransferCostBps:  cfg.RebalanceTransferCostBps,
		RebalanceLatencyHours:     cfg.RebalanceLatencyHours,
		PerpMaintenanceMarginRate: cfg.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     cfg.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        cfg.ReserveUSDTByVenue,
		DefaultReserveUSDT:        cfg.DefaultReserveUSDT,
		ExpectedHoldHours:         cfg.ExpectedHoldHours,
		PriceShockPcts:            cfg.PriceShockPcts,
	}
	normalized = normalizeCrossVenueSpotPerpScannerConfig(normalized)
	return CrossVenuePerpSpotScannerConfig{
		Exec:                      normalized.Exec,
		SpotTakerFeeBps:           normalized.SpotTakerFeeBps,
		PeakFundingHaircut:        normalized.PeakFundingHaircut,
		BasisConvergenceFraction:  normalized.BasisConvergenceFraction,
		BasisWideningStressBps:    normalized.BasisWideningStressBps,
		RebalanceTransferCostBps:  normalized.RebalanceTransferCostBps,
		RebalanceLatencyHours:     normalized.RebalanceLatencyHours,
		PerpMaintenanceMarginRate: normalized.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     normalized.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        normalized.ReserveUSDTByVenue,
		DefaultReserveUSDT:        normalized.DefaultReserveUSDT,
		ExpectedHoldHours:         normalized.ExpectedHoldHours,
		PriceShockPcts:            normalized.PriceShockPcts,
	}
}

func crossVenuePerpSpotCollectSymbols(
	funding map[string]map[string]CrossVenuePerpSpotFundingSnap,
	markets map[string]map[string]CrossVenuePerpSpotMarketSnap,
) []string {
	return crossVenueSpotPerpCollectSymbols(funding, markets)
}

func crossVenuePerpSpotSpotShortVenues(markets map[string]map[string]CrossVenuePerpSpotMarketSnap, sym string, drops *scannerInstrumentDropLog) []string {
	out := make([]string, 0)
	for venueName, bySym := range markets {
		m, ok := bySym[sym]
		if !ok {
			continue
		}
		if !scannerSpotLegEligible(m, "cvps", sym, venueName, drops) {
			continue
		}
		out = append(out, venueName)
	}
	sort.Strings(out)
	return out
}

type crossVenuePerpSpotPerpCandidate struct {
	venue string
	rate  float64
}

func crossVenuePerpSpotNegativePerpVenues(
	funding map[string]map[string]CrossVenuePerpSpotFundingSnap,
	markets map[string]map[string]CrossVenuePerpSpotMarketSnap,
	sym string,
	drops *scannerInstrumentDropLog,
) []crossVenuePerpSpotPerpCandidate {
	out := make([]crossVenuePerpSpotPerpCandidate, 0)
	for venueName, bySym := range funding {
		fund, ok := bySym[sym]
		if !ok || fund.Rate >= 0 {
			continue
		}
		mkt, ok := markets[venueName][sym]
		if !ok {
			continue
		}
		if !scannerPerpLegEligible(mkt, "cvps", sym, venueName, drops) {
			continue
		}
		out = append(out, crossVenuePerpSpotPerpCandidate{venue: venueName, rate: fund.Rate})
	}
	return out
}

func crossVenuePerpSpotBestNegativePerpVenue(candidates []crossVenuePerpSpotPerpCandidate) (string, float64) {
	bestVenue := ""
	bestRate := 0.0
	for _, c := range candidates {
		if bestVenue == "" || c.rate < bestRate || (c.rate == bestRate && c.venue < bestVenue) {
			bestVenue = c.venue
			bestRate = c.rate
		}
	}
	return bestVenue, bestRate
}

func buildCrossVenuePerpSpotOpportunity(
	snapshotID string,
	now time.Time,
	symbol, perpVenue, spotVenue string,
	funding map[string]map[string]CrossVenuePerpSpotFundingSnap,
	markets map[string]map[string]CrossVenuePerpSpotMarketSnap,
	available map[string]float64,
	borrowDailyRateByAsset, borrowLimitUSDTByAsset map[string]float64,
	cfg CrossVenuePerpSpotScannerConfig,
) (UnifiedOpportunity, bool) {
	cvCfg := crossVenuePerpSpotAsCrossVenueConfig(cfg)

	perpMkt := markets[perpVenue][symbol]
	spotMkt := markets[spotVenue][symbol]
	fund := funding[perpVenue][symbol]

	if fund.Rate >= 0 {
		return crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, crossVenuePerpSpotRejectPositiveFunding), true
	}
	if !spotMkt.HasSpot || spotMkt.SpotMid <= 0 {
		return crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, crossVenuePerpSpotRejectNoSpotMarket), true
	}
	if !perpMkt.HasPerp || perpMkt.PerpMid <= 0 {
		return UnifiedOpportunity{}, false
	}
	if !scannerBothLegsInstrumentValid(spotMkt, perpMkt) {
		return UnifiedOpportunity{}, false
	}

	baseAsset := baseAssetFromCanonicalSymbol(symbol)
	if !perpSpotAssetBorrowable(baseAsset, borrowDailyRateByAsset) {
		return crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, crossVenuePerpSpotRejectNotBorrowable), true
	}

	midDev := math.Abs(perpMkt.PerpMid/spotMkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		return UnifiedOpportunity{}, false
	}

	notional := crossVenueSpotPerpTradableNotionalForCrossVenue(
		cvCfg, spotMkt, perpMkt, spotVenue, perpVenue, available,
		func(v string) float64 { return crossVenuePerpSpotVenueReserve(v, cfg) },
	)
	if notional <= 0 {
		return crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, crossVenuePerpSpotRejectInsufficientDepth), true
	}
	if crossVenueSpotPerpPerpLiquidatedAtShock(cvCfg, notional) {
		return crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, crossVenuePerpSpotRejectMarginStress), true
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

	effectiveRate := crossVenueSpotPerpEffectiveFundingRate(-fund.Rate, fund.SpikeThreshold, cfg.PeakFundingHaircut)
	expectedFunding := notional * effectiveRate * float64(settlements)

	// 基差符号: 与 spot-perp 相反 —— spot-perp entry basis 取反后再走同一收敛/走阔 stress 函数。
	spotPerpEntryBasis := crossVenueSpotPerpEntryBasisUSDT(spotMkt.SpotMid, perpMkt.PerpMid, notional)
	expectedBasisPnL := -crossVenueSpotPerpExpectedBasisPnL(spotPerpEntryBasis, notional, cvCfg)

	openCost := crossVenuePerpSpotOpenCostUSDT(cfg, spotVenue, perpVenue, notional)
	closeCost := openCost
	slippage := crossVenueSpotPerpSlippageUSDT(cvCfg, notional, spotMkt, perpMkt)
	borrowCost := perpSpotBorrowCostUSDT(baseAsset, notional, holdHours, borrowDailyRateByAsset)

	spotMarginRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cvCfg, notional)

	rebalancePenalty := 0.0
	requiresRebalance := crossVenuePerpSpotRequiresRebalance(
		spotVenue, perpVenue, spotMarginRequired, perpMarginRequired, available, cfg,
	)
	if requiresRebalance {
		rebalancePenalty = notional * cfg.RebalanceTransferCostBps / 10000
	}

	netEV := expectedFunding + expectedBasisPnL - openCost - closeCost - slippage - borrowCost - rebalancePenalty

	refPrice := (spotMkt.SpotMid + perpMkt.PerpMid) / 2
	qty := notional / refPrice

	perpLegInstr := legInstrumentFields(perpMkt.PerpTickSize, perpMkt.PerpStepSize, perpMkt.PerpMinNotional)
	spotLegInstr := legInstrumentFields(spotMkt.SpotTickSize, spotMkt.SpotStepSize, spotMkt.SpotMinNotional)

	dataQuality := crossVenueSpotPerpDataQualityScore(fund)
	executionScore := crossVenueExecutionScore(slippage, notional)
	liquidityScore := crossVenueSpotPerpLiquidityScore(spotMkt, perpMkt, notional, holdHours)
	riskScore := crossVenuePerpSpotRiskScore(cfg, requiresRebalance, midDev, dataQuality)

	totalCapital := spotMarginRequired + perpMarginRequired
	capitalEfficiency := 0.0
	if totalCapital > 0 {
		capitalEfficiency = netEV / totalCapital
	}

	rejectReasons := make([]string, 0)
	if perpSpotBorrowOverLimit(baseAsset, notional, borrowLimitUSDTByAsset) {
		rejectReasons = append(rejectReasons, crossVenuePerpSpotRejectBorrowOverLimit)
	}
	if requiresRebalance {
		rejectReasons = append(rejectReasons, "requires_rebalance")
	}
	if netEV <= 0 {
		rejectReasons = append(rejectReasons, "net_ev<=0")
	}

	return UnifiedOpportunity{
		ID:           unifiedOpportunityID(UnifiedStrategyCrossVenuePerpSpot, symbol, snapshotID, perpVenue, spotVenue),
		StrategyType: UnifiedStrategyCrossVenuePerpSpot,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: perpVenue, Market: "perp", Side: "long", Qty: qty, Notional: notional, TickSize: perpLegInstr.tick, StepSize: perpLegInstr.step, MinNotional: perpLegInstr.minNotional},
			{Venue: spotVenue, Market: "spot", Side: "short", Qty: qty, Notional: notional, TickSize: spotLegInstr.tick, StepSize: spotLegInstr.step, MinNotional: spotLegInstr.minNotional},
		},
		ExpectedFundingUSDT:      expectedFunding,
		ExpectedOpenCostUSDT:     openCost,
		ExpectedCloseCostUSDT:    closeCost,
		ExpectedSlippageUSDT:     slippage,
		ExpectedBorrowCostUSDT:   borrowCost,
		ExpectedRollbackCostUSDT: rebalancePenalty,
		ExpectedNetEVUSDT:        netEV,
		ExpectedHoldHours:        holdHours,
		CapitalRequiredByVenue: map[string]float64{
			spotVenue: spotMarginRequired,
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

func crossVenuePerpSpotAsCrossVenueConfig(cfg CrossVenuePerpSpotScannerConfig) CrossVenueSpotPerpScannerConfig {
	return CrossVenueSpotPerpScannerConfig{
		Exec:                      cfg.Exec,
		SpotTakerFeeBps:           cfg.SpotTakerFeeBps,
		BasisConvergenceFraction:  cfg.BasisConvergenceFraction,
		BasisWideningStressBps:    cfg.BasisWideningStressBps,
		RebalanceTransferCostBps:  cfg.RebalanceTransferCostBps,
		RebalanceLatencyHours:     cfg.RebalanceLatencyHours,
		PeakFundingHaircut:        cfg.PeakFundingHaircut,
		PerpMaintenanceMarginRate: cfg.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     cfg.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        cfg.ReserveUSDTByVenue,
		DefaultReserveUSDT:        cfg.DefaultReserveUSDT,
		ExpectedHoldHours:         cfg.ExpectedHoldHours,
		PriceShockPcts:            cfg.PriceShockPcts,
	}
}

func crossVenuePerpSpotOpenCostUSDT(cfg CrossVenuePerpSpotScannerConfig, spotVenue, perpVenue string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	spotFee := cfg.SpotTakerFeeBps
	perpFee := crossVenueTakerFeeBps(cfg.Exec, perpVenue)
	return notional * (spotFee + perpFee) / 10000
}

func crossVenuePerpSpotVenueReserve(venueName string, cfg CrossVenuePerpSpotScannerConfig) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venueName]; ok {
			return math.Max(v, 0)
		}
	}
	return math.Max(cfg.DefaultReserveUSDT, 0)
}

func crossVenuePerpSpotRequiresRebalance(
	spotVenue, perpVenue string,
	spotMarginRequired, perpMarginRequired float64,
	available map[string]float64,
	cfg CrossVenuePerpSpotScannerConfig,
) bool {
	spotAvail := available[spotVenue] - crossVenuePerpSpotVenueReserve(spotVenue, cfg)
	perpAvail := available[perpVenue] - crossVenuePerpSpotVenueReserve(perpVenue, cfg)
	return spotAvail+1e-9 < spotMarginRequired || perpAvail+1e-9 < perpMarginRequired
}

func crossVenuePerpSpotRiskScore(cfg CrossVenuePerpSpotScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.40
	if requiresRebalance {
		risk += 0.20
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func crossVenuePerpSpotRejectedOpportunity(snapshotID, symbol, perpVenue, spotVenue, reason string) UnifiedOpportunity {
	return UnifiedOpportunity{
		ID:               unifiedOpportunityID(UnifiedStrategyCrossVenuePerpSpot, symbol, snapshotID, perpVenue, spotVenue),
		StrategyType:     UnifiedStrategyCrossVenuePerpSpot,
		Symbol:           symbol,
		RejectReasons:    []string{reason},
		RiskScore:        1,
		DataQualityScore: 0,
	}
}

func crossVenuePerpSpotLegNotional(opp UnifiedOpportunity) float64 {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return leg.Notional
		}
	}
	return 0
}
