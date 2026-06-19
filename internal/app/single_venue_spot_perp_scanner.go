package app

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	singleVenueSpotPerpRejectNegativeFunding   = "negative_funding_v1_not_supported"
	singleVenueSpotPerpRejectNoSpotMarket      = "no_spot_market"
	singleVenueSpotPerpRejectInsufficientDepth = "insufficient_depth"
	singleVenueSpotPerpRejectMarginStress      = "margin_stress_liquidation"
)

// single_venue_spot_perp_scanner.go 单所 spot-perp 正 carry 扫描器(纯函数, plan-only, 不接入实盘 tick)。

// SingleVenueSpotPerpFundingSnap 是某所某 symbol 的永续资金费快照(扫描输入)。
type SingleVenueSpotPerpFundingSnap = CrossVenueSpotPerpFundingSnap

// SingleVenueSpotPerpMarketSnap 是某所某 symbol 的现货/永续中间价与深度容量(扫描输入)。
type SingleVenueSpotPerpMarketSnap = CrossVenueSpotPerpMarketSnap

// SingleVenueSpotPerpScannerConfig 单所 spot-perp 扫描器成本与风控参数(v1 保守默认, 口径对齐跨所版)。
type SingleVenueSpotPerpScannerConfig struct {
	Exec CrossVenueExecutorConfig `json:"exec"`

	SpotTakerFeeBps           float64            `json:"spot_taker_fee_bps"`
	PeakFundingHaircut        float64            `json:"peak_funding_haircut"`
	BasisConvergenceFraction  float64            `json:"basis_convergence_fraction"`
	BasisWideningStressBps    float64            `json:"basis_widening_stress_bps"`
	RebalanceTransferCostBps  float64            `json:"rebalance_transfer_cost_bps"`
	PerpMaintenanceMarginRate float64            `json:"perp_maintenance_margin_rate"`
	PerpWalletBufferRatio     float64            `json:"perp_wallet_buffer_ratio"`
	ReserveUSDTByVenue        map[string]float64 `json:"reserve_usdt_by_venue,omitempty"`
	DefaultReserveUSDT        float64            `json:"default_reserve_usdt"`
	ExpectedHoldHours         float64            `json:"expected_hold_hours"`
	FundingCaptureProbability float64            `json:"funding_capture_probability"`
	PriceShockPcts            []float64          `json:"price_shock_pcts"`
}

// DefaultSingleVenueSpotPerpScannerConfig 返回 v1 保守默认(对齐 go_no_go / 跨所 spot-perp 口径)。
func DefaultSingleVenueSpotPerpScannerConfig(exec CrossVenueExecutorConfig) SingleVenueSpotPerpScannerConfig {
	return SingleVenueSpotPerpScannerConfig{
		Exec:                      exec,
		SpotTakerFeeBps:           crossVenueSpotPerpDefaultSpotFeeBps,
		PeakFundingHaircut:        crossVenueSpotPerpDefaultPeakFundingHaircut,
		BasisConvergenceFraction:  crossVenueSpotPerpDefaultBasisConvergence,
		BasisWideningStressBps:    crossVenueSpotPerpDefaultBasisWidenStressBps,
		RebalanceTransferCostBps:  crossVenueSpotPerpDefaultRebalanceCostBps,
		PerpMaintenanceMarginRate: crossVenueSpotPerpDefaultPerpMaintMarginRate,
		PerpWalletBufferRatio:     crossVenueSpotPerpDefaultPerpWalletBuffer,
		PriceShockPcts:            []float64{0.20, 0.30},
	}
}

// SingleVenueSpotPerpScanInput 是纯函数扫描器输入。
type SingleVenueSpotPerpScanInput struct {
	SnapshotID           string
	Now                  time.Time
	Symbols              []string
	Venues               []string
	FundingByVenue       map[string]map[string]SingleVenueSpotPerpFundingSnap
	MarketsByVenue       map[string]map[string]SingleVenueSpotPerpMarketSnap
	AvailableUSDTByVenue map[string]float64
	Config               SingleVenueSpotPerpScannerConfig
	FundingRateVolPerHourByVenue map[string]map[string]float64
}

// ScanSingleVenueSpotPerpOpportunities 扫描单所 spot-perp 机会(纯函数, plan-only)。
// v1: 仅正费率 carry(同所买现货 + 2x 空永续), 不借币、不做空现货。
func ScanSingleVenueSpotPerpOpportunities(in SingleVenueSpotPerpScanInput) []UnifiedOpportunity {
	cfg := normalizeSingleVenueSpotPerpScannerConfig(in.Config)
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	symbols := in.Symbols
	if len(symbols) == 0 {
		symbols = singleVenueSpotPerpCollectSymbols(in.FundingByVenue, in.MarketsByVenue)
	}
	venues := in.Venues
	if len(venues) == 0 {
		venues = singleVenueSpotPerpCollectVenues(in.FundingByVenue, in.MarketsByVenue)
	}

	out := make([]UnifiedOpportunity, 0)
	drops := newScannerInstrumentDropLog()
	for _, venueName := range venues {
		for _, sym := range symbols {
			sym = strings.TrimSpace(sym)
			if sym == "" {
				continue
			}
			opp, ok := buildSingleVenueSpotPerpOpportunity(
				in.SnapshotID, now, sym, venueName,
				in.FundingByVenue, in.MarketsByVenue, in.AvailableUSDTByVenue, cfg, drops,
				in.FundingRateVolPerHourByVenue,
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

// AppendSingleVenueSpotPerpCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendSingleVenueSpotPerpCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		notional := singleVenueSpotPerpLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

func normalizeSingleVenueSpotPerpScannerConfig(cfg SingleVenueSpotPerpScannerConfig) SingleVenueSpotPerpScannerConfig {
	normalized := CrossVenueSpotPerpScannerConfig{
		Exec:                      cfg.Exec,
		SpotTakerFeeBps:           cfg.SpotTakerFeeBps,
		PeakFundingHaircut:        cfg.PeakFundingHaircut,
		BasisConvergenceFraction:  cfg.BasisConvergenceFraction,
		BasisWideningStressBps:    cfg.BasisWideningStressBps,
		RebalanceTransferCostBps:  cfg.RebalanceTransferCostBps,
		PerpMaintenanceMarginRate: cfg.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     cfg.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        cfg.ReserveUSDTByVenue,
		DefaultReserveUSDT:        cfg.DefaultReserveUSDT,
		ExpectedHoldHours:         cfg.ExpectedHoldHours,
		FundingCaptureProbability: cfg.FundingCaptureProbability,
		PriceShockPcts:            cfg.PriceShockPcts,
	}
	normalized = normalizeCrossVenueSpotPerpScannerConfig(normalized)
	return SingleVenueSpotPerpScannerConfig{
		Exec:                      normalized.Exec,
		SpotTakerFeeBps:           normalized.SpotTakerFeeBps,
		PeakFundingHaircut:        normalized.PeakFundingHaircut,
		BasisConvergenceFraction:  normalized.BasisConvergenceFraction,
		BasisWideningStressBps:    normalized.BasisWideningStressBps,
		RebalanceTransferCostBps:  normalized.RebalanceTransferCostBps,
		PerpMaintenanceMarginRate: normalized.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     normalized.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        normalized.ReserveUSDTByVenue,
		DefaultReserveUSDT:        normalized.DefaultReserveUSDT,
		ExpectedHoldHours:         normalized.ExpectedHoldHours,
		FundingCaptureProbability: normalized.FundingCaptureProbability,
		PriceShockPcts:            normalized.PriceShockPcts,
	}
}

func singleVenueSpotPerpCollectSymbols(
	funding map[string]map[string]SingleVenueSpotPerpFundingSnap,
	markets map[string]map[string]SingleVenueSpotPerpMarketSnap,
) []string {
	return crossVenueSpotPerpCollectSymbols(funding, markets)
}

func singleVenueSpotPerpCollectVenues(
	funding map[string]map[string]SingleVenueSpotPerpFundingSnap,
	markets map[string]map[string]SingleVenueSpotPerpMarketSnap,
) []string {
	seen := make(map[string]struct{})
	for venueName := range funding {
		seen[venueName] = struct{}{}
	}
	for venueName := range markets {
		seen[venueName] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for venueName := range seen {
		out = append(out, venueName)
	}
	sort.Strings(out)
	return out
}

func buildSingleVenueSpotPerpOpportunity(
	snapshotID string,
	now time.Time,
	symbol, venueName string,
	funding map[string]map[string]SingleVenueSpotPerpFundingSnap,
	markets map[string]map[string]SingleVenueSpotPerpMarketSnap,
	available map[string]float64,
	cfg SingleVenueSpotPerpScannerConfig,
	drops *scannerInstrumentDropLog,
	fundingVolByVenue map[string]map[string]float64,
) (UnifiedOpportunity, bool) {
	cvCfg := singleVenueSpotPerpAsCrossVenueConfig(cfg)

	fundBySym, okFund := funding[venueName]
	mktBySym, okMkt := markets[venueName]
	if !okFund || !okMkt {
		return UnifiedOpportunity{}, false
	}
	fund, okFundSym := fundBySym[symbol]
	mkt, okMktSym := mktBySym[symbol]
	if !okFundSym || !okMktSym {
		return UnifiedOpportunity{}, false
	}

	if fund.Rate <= 0 {
		return singleVenueSpotPerpRejectedOpportunity(snapshotID, symbol, venueName, singleVenueSpotPerpRejectNegativeFunding, fund, now, 0), true
	}
	if !mkt.HasSpot || mkt.SpotMid <= 0 {
		return singleVenueSpotPerpRejectedOpportunity(snapshotID, symbol, venueName, singleVenueSpotPerpRejectNoSpotMarket, fund, now, fund.Rate), true
	}
	if !mkt.HasPerp || mkt.PerpMid <= 0 {
		return UnifiedOpportunity{}, false
	}
	if !scannerBothLegsInstrumentValid(mkt, mkt) {
		if !scannerMarketLegInstrumentValid(mkt.SpotTickSize, mkt.SpotStepSize) {
			drops.skip("svsp", symbol, venueName, "现货")
		} else {
			drops.skip("svsp", symbol, venueName, "永续")
		}
		return UnifiedOpportunity{}, false
	}

	midDev := math.Abs(mkt.PerpMid/mkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		return UnifiedOpportunity{}, false
	}

	notional := crossVenueSpotPerpTradableNotionalForSingleVenue(
		cvCfg, mkt, mkt, venueName, available,
		func(v string) float64 { return singleVenueSpotPerpVenueReserve(v, cfg) },
	)
	if notional <= 0 {
		return singleVenueSpotPerpRejectedOpportunity(snapshotID, symbol, venueName, singleVenueSpotPerpRejectInsufficientDepth, fund, now, fund.Rate), true
	}
	if crossVenueSpotPerpPerpLiquidatedAtShock(cvCfg, notional) {
		return singleVenueSpotPerpRejectedOpportunity(snapshotID, symbol, venueName, singleVenueSpotPerpRejectMarginStress, fund, now, fund.Rate), true
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

	effectiveRate := crossVenueSpotPerpEffectiveFundingRate(fund.Rate, fund.SpikeThreshold, cfg.PeakFundingHaircut, cfg.Exec.CarryNoHistoryHaircut)

	entryBasisUSDT := crossVenueSpotPerpEntryBasisUSDT(mkt.SpotMid, mkt.PerpMid, notional)
	expectedBasisPnL := crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional, cvCfg)

	openCost := singleVenueSpotPerpOpenCostUSDT(cfg, venueName, notional)
	closeCost := openCost
	slippage := crossVenueSpotPerpSlippageUSDT(cvCfg, notional, mkt, mkt)

	vol, volOK := carryFundingVolFromMap(fundingVolByVenue, venueName, symbol)
	entrySlippage := carryScannerEntrySlippageUSDT(cvCfg, notional, mkt, mkt)
	if ok, _ := carryScannerFundingEntryEligible(now, fund, holdHours, notional, effectiveRate, expectedBasisPnL, openCost, entrySlippage, cfg.Exec.FundingEntryGate(), vol, volOK); !ok {
		return UnifiedOpportunity{}, false
	}

	maxNotional := notional
	notional, _, ok := carrySizedNotionalAfterConfidence(maxNotional, cfg.Exec, fund, vol, volOK)
	if !ok {
		return UnifiedOpportunity{}, false
	}

	expectedFunding := carryScannerExpectedFundingUSDT(notional, effectiveRate, settlements, cfg.FundingCaptureProbability)
	entryBasisUSDT = crossVenueSpotPerpEntryBasisUSDT(mkt.SpotMid, mkt.PerpMid, notional)
	expectedBasisPnL = crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional, cvCfg)
	openCost = singleVenueSpotPerpOpenCostUSDT(cfg, venueName, notional)
	closeCost = openCost
	slippage = crossVenueSpotPerpSlippageUSDT(cvCfg, notional, mkt, mkt)
	spotCashRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cvCfg, notional)
	totalCapital := spotCashRequired + perpMarginRequired
	rebalancePenalty := 0.0
	requiresRebalance := singleVenueSpotPerpRequiresRebalance(venueName, spotCashRequired, perpMarginRequired, available, cfg)
	if requiresRebalance {
		rebalancePenalty = notional * cfg.RebalanceTransferCostBps / 10000
	}
	spotLegInstr := legInstrumentFields(mkt.SpotTickSize, mkt.SpotStepSize, mkt.SpotMinNotional)
	perpLegInstr := legInstrumentFields(mkt.PerpTickSize, mkt.PerpStepSize, mkt.PerpMinNotional)
	closeCost += carryScannerDustResidualCostUSDT([]float64{spotLegInstr.minNotional, perpLegInstr.minNotional}, false)
	netEV := expectedFunding + expectedBasisPnL - openCost - closeCost - slippage - rebalancePenalty

	dataQuality := crossVenueSpotPerpDataQualityScore(fund)
	executionScore := crossVenueExecutionScore(slippage, notional)
	liquidityScore := crossVenueSpotPerpLiquidityScore(mkt, mkt, notional, holdHours)
	riskScore := singleVenueSpotPerpRiskScore(cfg, requiresRebalance, midDev, dataQuality)

	capitalEfficiency := 0.0
	if totalCapital > 0 {
		capitalEfficiency = netEV / totalCapital
	}

	rejectReasons := make([]string, 0)
	if requiresRebalance {
		rejectReasons = append(rejectReasons, "requires_rebalance")
	}
	if netEV <= 0 {
		rejectReasons = append(rejectReasons, "net_ev<=0")
	}

	refPrice := (mkt.SpotMid + mkt.PerpMid) / 2
	qty := notional / refPrice

	opp := UnifiedOpportunity{
		ID:           unifiedOpportunityID(UnifiedStrategySingleVenueSpotPerp, symbol, snapshotID, venueName, venueName),
		StrategyType: UnifiedStrategySingleVenueSpotPerp,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: venueName, Market: "spot", Side: "buy", Qty: qty, Notional: notional, TickSize: spotLegInstr.tick, StepSize: spotLegInstr.step, MinNotional: spotLegInstr.minNotional},
			{Venue: venueName, Market: "perp", Side: "short", Qty: qty, Notional: notional, TickSize: perpLegInstr.tick, StepSize: perpLegInstr.step, MinNotional: perpLegInstr.minNotional},
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
			venueName: totalCapital,
		},
		RiskScore:              riskScore,
		ExecutionScore:         executionScore,
		LiquidityScore:         liquidityScore,
		DataQualityScore:       dataQuality,
		CapitalEfficiencyScore: capitalEfficiency,
		RejectReasons:          rejectReasons,
		RequiresRebalance:      requiresRebalance,
	}
	return singleVenueSpotPerpFinalizeOpportunity(opp, fund, now), true
}

func singleVenueSpotPerpFinalizeOpportunity(opp UnifiedOpportunity, fund CrossVenueSpotPerpFundingSnap, now time.Time) UnifiedOpportunity {
	applyUnifiedFundingSnapDisplay(&opp, fund, now, fund.Rate)
	return opp
}

func singleVenueSpotPerpAsCrossVenueConfig(cfg SingleVenueSpotPerpScannerConfig) CrossVenueSpotPerpScannerConfig {
	return CrossVenueSpotPerpScannerConfig{
		Exec:                      cfg.Exec,
		SpotTakerFeeBps:           cfg.SpotTakerFeeBps,
		PeakFundingHaircut:        cfg.PeakFundingHaircut,
		BasisConvergenceFraction:  cfg.BasisConvergenceFraction,
		BasisWideningStressBps:    cfg.BasisWideningStressBps,
		RebalanceTransferCostBps:  cfg.RebalanceTransferCostBps,
		PerpMaintenanceMarginRate: cfg.PerpMaintenanceMarginRate,
		PerpWalletBufferRatio:     cfg.PerpWalletBufferRatio,
		ReserveUSDTByVenue:        cfg.ReserveUSDTByVenue,
		DefaultReserveUSDT:        cfg.DefaultReserveUSDT,
		ExpectedHoldHours:         cfg.ExpectedHoldHours,
		FundingCaptureProbability: cfg.FundingCaptureProbability,
		PriceShockPcts:            cfg.PriceShockPcts,
	}
}

func singleVenueSpotPerpOpenCostUSDT(cfg SingleVenueSpotPerpScannerConfig, venueName string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	spotFee := cfg.SpotTakerFeeBps
	perpFee := crossVenueTakerFeeBps(cfg.Exec, venueName)
	return notional * (spotFee + perpFee) / 10000
}

func singleVenueSpotPerpVenueReserve(venueName string, cfg SingleVenueSpotPerpScannerConfig) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venueName]; ok {
			return math.Max(v, 0)
		}
	}
	return math.Max(cfg.DefaultReserveUSDT, 0)
}

func singleVenueSpotPerpRequiresRebalance(
	venueName string,
	spotCashRequired, perpMarginRequired float64,
	available map[string]float64,
	cfg SingleVenueSpotPerpScannerConfig,
) bool {
	totalRequired := spotCashRequired + perpMarginRequired
	avail := available[venueName] - singleVenueSpotPerpVenueReserve(venueName, cfg)
	return avail+1e-9 < totalRequired
}

func singleVenueSpotPerpRiskScore(cfg SingleVenueSpotPerpScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.30
	if requiresRebalance {
		risk += 0.15
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func singleVenueSpotPerpRejectedOpportunity(snapshotID, symbol, venueName, reason string, fund CrossVenueSpotPerpFundingSnap, now time.Time, displayRate float64) UnifiedOpportunity {
	return unifiedRejectedOpportunityWithFunding(
		UnifiedStrategySingleVenueSpotPerp, snapshotID, symbol, venueName, venueName, reason, fund, now, displayRate,
	)
}

func singleVenueSpotPerpLegNotional(opp UnifiedOpportunity) float64 {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return leg.Notional
		}
	}
	return 0
}
