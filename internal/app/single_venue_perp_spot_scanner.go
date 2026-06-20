package app

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	singleVenuePerpSpotRejectPositiveFunding   = "positive_funding_v1_not_supported"
	singleVenuePerpSpotRejectNoSpotMarket      = "no_spot_market"
	singleVenuePerpSpotRejectInsufficientDepth = "insufficient_depth"
	singleVenuePerpSpotRejectMarginStress      = "margin_stress_liquidation"
	singleVenuePerpSpotRejectNotBorrowable     = "not_borrowable"
	singleVenuePerpSpotRejectBorrowOverLimit   = "borrow_over_limit"
)

// single_venue_perp_spot_scanner.go 单所 perp-spot 负 carry 扫描器(纯函数, plan-only, 不接入实盘 tick)。
// 结构: 同所多永续 + 借币空现货, 捕获负资金费并扣除借币利息。

// SingleVenuePerpSpotFundingSnap 是某所某 symbol 的永续资金费快照(扫描输入)。
type SingleVenuePerpSpotFundingSnap = CrossVenueSpotPerpFundingSnap

// SingleVenuePerpSpotMarketSnap 是某所某 symbol 的现货/永续中间价与深度容量(扫描输入)。
type SingleVenuePerpSpotMarketSnap = CrossVenueSpotPerpMarketSnap

// SingleVenuePerpSpotScannerConfig 单所 perp-spot 扫描器成本与风控参数(v1 保守默认, 口径对齐跨所版)。
type SingleVenuePerpSpotScannerConfig struct {
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

// DefaultSingleVenuePerpSpotScannerConfig 返回 v1 保守默认(对齐 go_no_go / 跨所 perp-spot 口径)。
func DefaultSingleVenuePerpSpotScannerConfig(exec CrossVenueExecutorConfig) SingleVenuePerpSpotScannerConfig {
	return SingleVenuePerpSpotScannerConfig{
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

// SingleVenuePerpSpotScanInput 是纯函数扫描器输入。
type SingleVenuePerpSpotScanInput struct {
	SnapshotID                        string
	Now                               time.Time
	Symbols                           []string
	Venues                            []string
	FundingByVenue                    map[string]map[string]SingleVenuePerpSpotFundingSnap
	MarketsByVenue                    map[string]map[string]SingleVenuePerpSpotMarketSnap
	AvailableUSDTByVenue              map[string]float64
	BorrowDailyRateByAsset            map[string]float64
	BorrowLimitUSDTByAsset            map[string]float64
	BorrowKnownAvailable              *BorrowKnownAvailableRegistry
	BorrowDefaultDailyRateWhenUnknown float64
	BorrowDisabledVenues              map[string]struct{}
	Config                            SingleVenuePerpSpotScannerConfig
	FundingRateVolPerHourByVenue      map[string]map[string]float64
}

// ScanSingleVenuePerpSpotOpportunities 扫描单所 perp-spot 负 carry 机会(纯函数, plan-only)。
// v1: 仅负费率 carry(同所多永续 + 借币空现货), 需借币表提供日利率与限额。
func ScanSingleVenuePerpSpotOpportunities(in SingleVenuePerpSpotScanInput) []UnifiedOpportunity {
	cfg := normalizeSingleVenuePerpSpotScannerConfig(in.Config)
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	symbols := in.Symbols
	if len(symbols) == 0 {
		symbols = singleVenuePerpSpotCollectSymbols(in.FundingByVenue, in.MarketsByVenue)
	}
	venues := in.Venues
	if len(venues) == 0 {
		venues = singleVenuePerpSpotCollectVenues(in.FundingByVenue, in.MarketsByVenue)
	}

	out := make([]UnifiedOpportunity, 0)
	borrowDisabled := in.BorrowDisabledVenues
	for _, venueName := range venues {
		if _, disabled := borrowDisabled[strings.ToLower(strings.TrimSpace(venueName))]; disabled {
			continue
		}
		for _, sym := range symbols {
			sym = strings.TrimSpace(sym)
			if sym == "" {
				continue
			}
			opp, ok := buildSingleVenuePerpSpotOpportunity(
				in.SnapshotID, now, sym, venueName,
				in.FundingByVenue, in.MarketsByVenue, in.AvailableUSDTByVenue,
				in.BorrowDailyRateByAsset, in.BorrowLimitUSDTByAsset,
				in.BorrowKnownAvailable, in.BorrowDefaultDailyRateWhenUnknown,
				in.BorrowDisabledVenues,
				cfg,
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

// AppendSingleVenuePerpSpotCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendSingleVenuePerpSpotCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		notional := singleVenuePerpSpotLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

func normalizeSingleVenuePerpSpotScannerConfig(cfg SingleVenuePerpSpotScannerConfig) SingleVenuePerpSpotScannerConfig {
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
	return SingleVenuePerpSpotScannerConfig{
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

func singleVenuePerpSpotCollectSymbols(
	funding map[string]map[string]SingleVenuePerpSpotFundingSnap,
	markets map[string]map[string]SingleVenuePerpSpotMarketSnap,
) []string {
	return crossVenueSpotPerpCollectSymbols(funding, markets)
}

func singleVenuePerpSpotCollectVenues(
	funding map[string]map[string]SingleVenuePerpSpotFundingSnap,
	markets map[string]map[string]SingleVenuePerpSpotMarketSnap,
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

func buildSingleVenuePerpSpotOpportunity(
	snapshotID string,
	now time.Time,
	symbol, venueName string,
	funding map[string]map[string]SingleVenuePerpSpotFundingSnap,
	markets map[string]map[string]SingleVenuePerpSpotMarketSnap,
	available map[string]float64,
	borrowDailyRateByAsset, borrowLimitUSDTByAsset map[string]float64,
	borrowKnown *BorrowKnownAvailableRegistry,
	borrowDefaultDailyRateWhenUnknown float64,
	borrowDisabled map[string]struct{},
	cfg SingleVenuePerpSpotScannerConfig,
	fundingVolByVenue map[string]map[string]float64,
) (UnifiedOpportunity, bool) {
	cvCfg := singleVenuePerpSpotAsCrossVenueConfig(cfg)

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

	if fund.Rate >= 0 {
		if !cfg.Exec.SingleVenueReverseFundingEnabled {
			return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectPositiveFunding, fund, now, fund.Rate), true
		}
		return buildSingleVenuePerpSpotReverseOpportunity(
			snapshotID, now, symbol, venueName, fund, mkt, available,
			cfg, fundingVolByVenue, cvCfg,
		)
	}
	if !mkt.HasSpot || mkt.SpotMid <= 0 {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectNoSpotMarket, fund, now, -fund.Rate), true
	}
	if !mkt.HasPerp || mkt.PerpMid <= 0 {
		return UnifiedOpportunity{}, false
	}

	baseAsset := baseAssetFromCanonicalSymbol(symbol)
	borrowState := perpSpotBorrowState(venueName, baseAsset, borrowKnown, borrowDisabled, now)
	if borrowState == BorrowStateUnavailable {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectNotBorrowable, fund, now, -fund.Rate), true
	}

	midDev := math.Abs(mkt.PerpMid/mkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		return UnifiedOpportunity{}, false
	}

	notional := crossVenueSpotPerpTradableNotionalForSingleVenue(
		cvCfg, mkt, mkt, venueName, available,
		func(v string) float64 { return singleVenuePerpSpotVenueReserve(v, cfg) },
	)
	if notional <= 0 {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectInsufficientDepth, fund, now, -fund.Rate), true
	}
	if crossVenueSpotPerpPerpLiquidatedAtShock(cvCfg, notional) {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectMarginStress, fund, now, -fund.Rate), true
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

	// 负费率: 多头永续收钱, 捕获量 = effective_rate(|rate|) (对 fund.Rate<0 取 -rate 走尖峰衰减口径)。
	effectiveRate := crossVenueSpotPerpEffectiveFundingRate(-fund.Rate, fund.SpikeThreshold, cfg.PeakFundingHaircut, cfg.Exec.CarryNoHistoryHaircut)

	spotPerpEntryBasis := crossVenueSpotPerpEntryBasisUSDT(mkt.SpotMid, mkt.PerpMid, notional)
	expectedBasisPnL := crossVenuePerpSpotExpectedBasisPnL(spotPerpEntryBasis, notional, cvCfg)

	openCost := singleVenuePerpSpotOpenCostUSDT(cfg, venueName, notional)
	closeCost := openCost
	slippage := crossVenueSpotPerpSlippageUSDT(cvCfg, notional, mkt, mkt)
	borrowCost := perpSpotBorrowCostUSDT(baseAsset, notional, holdHours, borrowDailyRateByAsset, borrowDefaultDailyRateWhenUnknown)

	vol, volOK := carryFundingVolFromMap(fundingVolByVenue, venueName, symbol)
	entrySlippage := carryScannerEntrySlippageUSDT(cvCfg, notional, mkt, mkt)
	if ok, _ := carryScannerFundingEntryEligible(now, fund, holdHours, notional, effectiveRate, expectedBasisPnL, openCost+borrowCost, entrySlippage, cfg.Exec.FundingEntryGate(), vol, volOK); !ok {
		return UnifiedOpportunity{}, false
	}

	maxNotional := notional
	notional, _, ok := carrySizedNotionalAfterConfidence(maxNotional, cfg.Exec, fund, vol, volOK)
	if !ok {
		return UnifiedOpportunity{}, false
	}

	expectedFunding := carryScannerExpectedFundingUSDT(notional, effectiveRate, settlements, cfg.FundingCaptureProbability)
	spotPerpEntryBasis = crossVenueSpotPerpEntryBasisUSDT(mkt.SpotMid, mkt.PerpMid, notional)
	expectedBasisPnL = crossVenuePerpSpotExpectedBasisPnL(spotPerpEntryBasis, notional, cvCfg)
	openCost = singleVenuePerpSpotOpenCostUSDT(cfg, venueName, notional)
	closeCost = openCost
	slippage = crossVenueSpotPerpSlippageUSDT(cvCfg, notional, mkt, mkt)
	borrowCost = perpSpotBorrowCostUSDT(baseAsset, notional, holdHours, borrowDailyRateByAsset, borrowDefaultDailyRateWhenUnknown)
	spotMarginRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cvCfg, notional)
	totalCapital := spotMarginRequired + perpMarginRequired
	rebalancePenalty := 0.0
	requiresRebalance := singleVenuePerpSpotRequiresRebalance(venueName, spotMarginRequired, perpMarginRequired, available, cfg)
	if requiresRebalance {
		rebalancePenalty = notional * cfg.RebalanceTransferCostBps / 10000
	}
	spotLegInstr := legInstrumentFields(mkt.SpotTickSize, mkt.SpotStepSize, mkt.SpotMinNotional)
	perpLegInstr := legInstrumentFields(mkt.PerpTickSize, mkt.PerpStepSize, mkt.PerpMinNotional)
	closeCost += carryScannerDustResidualCostUSDT([]float64{spotLegInstr.minNotional, perpLegInstr.minNotional}, false)
	netEV := expectedFunding + expectedBasisPnL - openCost - closeCost - slippage - borrowCost - rebalancePenalty

	dataQuality := crossVenueSpotPerpDataQualityScore(fund)
	executionScore := crossVenueExecutionScore(slippage, notional)
	liquidityScore := crossVenueSpotPerpLiquidityScore(mkt, mkt, notional, holdHours)
	riskScore := singleVenuePerpSpotRiskScore(cfg, requiresRebalance, midDev, dataQuality)

	capitalEfficiency := 0.0
	if totalCapital > 0 {
		capitalEfficiency = netEV / totalCapital
	}

	rejectReasons := make([]string, 0)
	if borrowState == BorrowStateUnknown {
		rejectReasons = append(rejectReasons, perpSpotBorrowUnknownFailOpen)
	}
	if perpSpotBorrowOverLimit(baseAsset, notional, borrowLimitUSDTByAsset) {
		rejectReasons = append(rejectReasons, singleVenuePerpSpotRejectBorrowOverLimit)
	}
	if requiresRebalance {
		rejectReasons = append(rejectReasons, "requires_rebalance")
	}
	if netEV <= 0 {
		rejectReasons = append(rejectReasons, "net_ev<=0")
	}

	refPrice := (mkt.SpotMid + mkt.PerpMid) / 2
	qty := notional / refPrice

	opp := UnifiedOpportunity{
		ID:           unifiedOpportunityID(UnifiedStrategySingleVenuePerpSpot, symbol, snapshotID, venueName, venueName),
		StrategyType: UnifiedStrategySingleVenuePerpSpot,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: venueName, Market: "perp", Side: "long", Qty: qty, Notional: notional, TickSize: perpLegInstr.tick, StepSize: perpLegInstr.step, MinNotional: perpLegInstr.minNotional},
			{Venue: venueName, Market: "spot", Side: "short", Qty: qty, Notional: notional, TickSize: spotLegInstr.tick, StepSize: spotLegInstr.step, MinNotional: spotLegInstr.minNotional},
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
	applyUnifiedFundingSnapDisplay(&opp, fund, now, -fund.Rate)
	return opp, true
}

// buildSingleVenuePerpSpotReverseOpportunity 正费率反向 carry:空永续 + 买现货(无需借币)。
func buildSingleVenuePerpSpotReverseOpportunity(
	snapshotID string,
	now time.Time,
	symbol, venueName string,
	fund SingleVenuePerpSpotFundingSnap,
	mkt SingleVenuePerpSpotMarketSnap,
	available map[string]float64,
	cfg SingleVenuePerpSpotScannerConfig,
	fundingVolByVenue map[string]map[string]float64,
	cvCfg CrossVenueSpotPerpScannerConfig,
) (UnifiedOpportunity, bool) {
	if !mkt.HasSpot || mkt.SpotMid <= 0 {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectNoSpotMarket, fund, now, fund.Rate), true
	}
	if !mkt.HasPerp || mkt.PerpMid <= 0 {
		return UnifiedOpportunity{}, false
	}
	if !scannerBothLegsInstrumentValid(mkt, mkt) {
		return UnifiedOpportunity{}, false
	}
	midDev := math.Abs(mkt.PerpMid/mkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		return UnifiedOpportunity{}, false
	}
	notional := crossVenueSpotPerpTradableNotionalForSingleVenue(cvCfg, mkt, mkt, venueName, available, func(v string) float64 { return singleVenuePerpSpotVenueReserve(v, cfg) })
	if notional <= 0 {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectInsufficientDepth, fund, now, fund.Rate), true
	}
	if crossVenueSpotPerpPerpLiquidatedAtShock(cvCfg, notional) {
		return singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, singleVenuePerpSpotRejectMarginStress, fund, now, fund.Rate), true
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
	openCost := singleVenuePerpSpotOpenCostUSDT(cfg, venueName, notional)
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
	openCost = singleVenuePerpSpotOpenCostUSDT(cfg, venueName, notional)
	closeCost = openCost
	slippage = crossVenueSpotPerpSlippageUSDT(cvCfg, notional, mkt, mkt)
	spotCashRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cvCfg, notional)
	totalCapital := spotCashRequired + perpMarginRequired
	rebalancePenalty := 0.0
	requiresRebalance := singleVenuePerpSpotRequiresRebalance(venueName, spotCashRequired, perpMarginRequired, available, cfg)
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
	riskScore := singleVenuePerpSpotReverseRiskScore(cfg, requiresRebalance, midDev, dataQuality)
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
		ID:           unifiedOpportunityID(UnifiedStrategySingleVenuePerpSpot, symbol, snapshotID, venueName, venueName),
		StrategyType: UnifiedStrategySingleVenuePerpSpot,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: venueName, Market: "perp", Side: "short", Qty: qty, Notional: notional, TickSize: perpLegInstr.tick, StepSize: perpLegInstr.step, MinNotional: perpLegInstr.minNotional},
			{Venue: venueName, Market: "spot", Side: "buy", Qty: qty, Notional: notional, TickSize: spotLegInstr.tick, StepSize: spotLegInstr.step, MinNotional: spotLegInstr.minNotional},
		},
		ExpectedFundingUSDT:      expectedFunding,
		ExpectedOpenCostUSDT:     openCost,
		ExpectedCloseCostUSDT:    closeCost,
		ExpectedSlippageUSDT:     slippage,
		ExpectedBorrowCostUSDT:   0,
		ExpectedRollbackCostUSDT: rebalancePenalty,
		ExpectedNetEVUSDT:        netEV,
		ExpectedHoldHours:        holdHours,
		CapitalRequiredByVenue:   map[string]float64{venueName: totalCapital},
		RiskScore:                riskScore,
		ExecutionScore:           executionScore,
		LiquidityScore:           liquidityScore,
		DataQualityScore:         dataQuality,
		CapitalEfficiencyScore:   capitalEfficiency,
		RejectReasons:            rejectReasons,
		RequiresRebalance:        requiresRebalance,
	}
	applyUnifiedFundingSnapDisplay(&opp, fund, now, fund.Rate)
	return opp, true
}

func singleVenuePerpSpotReverseRiskScore(cfg SingleVenuePerpSpotScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.30
	if requiresRebalance {
		risk += 0.15
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func singleVenuePerpSpotAsCrossVenueConfig(cfg SingleVenuePerpSpotScannerConfig) CrossVenueSpotPerpScannerConfig {
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

func singleVenuePerpSpotOpenCostUSDT(cfg SingleVenuePerpSpotScannerConfig, venueName string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	spotFee := cfg.SpotTakerFeeBps
	perpFee := crossVenueTakerFeeBps(cfg.Exec, venueName)
	return notional * (spotFee + perpFee) / 10000
}

func singleVenuePerpSpotVenueReserve(venueName string, cfg SingleVenuePerpSpotScannerConfig) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venueName]; ok {
			return math.Max(v, 0)
		}
	}
	return math.Max(cfg.DefaultReserveUSDT, 0)
}

func singleVenuePerpSpotRequiresRebalance(
	venueName string,
	spotMarginRequired, perpMarginRequired float64,
	available map[string]float64,
	cfg SingleVenuePerpSpotScannerConfig,
) bool {
	totalRequired := spotMarginRequired + perpMarginRequired
	avail := available[venueName] - singleVenuePerpSpotVenueReserve(venueName, cfg)
	return avail+1e-9 < totalRequired
}

func singleVenuePerpSpotRiskScore(cfg SingleVenuePerpSpotScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.35
	if requiresRebalance {
		risk += 0.15
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func singleVenuePerpSpotRejectedOpportunity(snapshotID, symbol, venueName, reason string, fund CrossVenueSpotPerpFundingSnap, now time.Time, displayRate float64) UnifiedOpportunity {
	return unifiedRejectedOpportunityWithFunding(
		UnifiedStrategySingleVenuePerpSpot, snapshotID, symbol, venueName, venueName, reason, fund, now, displayRate,
	)
}

func singleVenuePerpSpotLegNotional(opp UnifiedOpportunity) float64 {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return leg.Notional
		}
	}
	return 0
}

func perpSpotBorrowOverLimit(baseAsset string, notional float64, borrowLimitUSDTByAsset map[string]float64) bool {
	if borrowLimitUSDTByAsset == nil || notional <= 0 {
		return false
	}
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	limit, ok := borrowLimitUSDTByAsset[baseAsset]
	return ok && limit > 0 && notional > limit+1e-9
}
