package app

import (
	"fmt"
	"strings"
	"time"
)

const (
	UnifiedStrategyPerpPerp            = "perp_perp"
	UnifiedStrategySingleVenueSpotPerp = "single_venue_spot_perp"
	UnifiedStrategyCrossVenueSpotPerp  = "cross_venue_spot_perp"
	UnifiedStrategySingleVenuePerpSpot = "single_venue_perp_spot"
	UnifiedStrategyCrossVenuePerpSpot  = "cross_venue_perp_spot"
)

// UnifiedOpportunityLeg 是统一机会结构的一条交易腿。
type UnifiedOpportunityLeg struct {
	Venue       string  `json:"venue"`
	Market      string  `json:"market"`
	Side        string  `json:"side"`
	Qty         float64 `json:"qty"`
	Notional    float64 `json:"notional"`
	TickSize    float64 `json:"tick_size,omitempty"`
	StepSize    float64 `json:"step_size,omitempty"`
	MinNotional float64 `json:"min_notional,omitempty"`
}

// UnifiedOpportunity 是三类资金费机会的统一语义结构(组合层用,不改变现有 CrossVenueOpportunity)。
type UnifiedOpportunity struct {
	ID                       string                  `json:"id"`
	StrategyType             string                  `json:"strategy_type"`
	Symbol                   string                  `json:"symbol"`
	Legs                     []UnifiedOpportunityLeg `json:"legs"`
	ExpectedFundingUSDT      float64                 `json:"expected_funding_usdt"`
	ExpectedOpenCostUSDT     float64                 `json:"expected_open_cost_usdt"`
	ExpectedCloseCostUSDT    float64                 `json:"expected_close_cost_usdt"`
	ExpectedSlippageUSDT     float64                 `json:"expected_slippage_usdt"`
	ExpectedBorrowCostUSDT   float64                 `json:"expected_borrow_cost_usdt"`
	ExpectedRollbackCostUSDT float64                 `json:"expected_rollback_cost_usdt"`
	ExpectedNetEVUSDT        float64                 `json:"expected_net_ev_usdt"`
	ExpectedHoldHours        float64                 `json:"expected_hold_hours"`
	CapitalRequiredByVenue   map[string]float64      `json:"capital_required_by_venue"`
	RiskScore                float64                 `json:"risk_score"`
	ExecutionScore           float64                 `json:"execution_score"`
	LiquidityScore           float64                 `json:"liquidity_score"`
	DataQualityScore         float64                 `json:"data_quality_score"`
	CapitalEfficiencyScore   float64                 `json:"capital_efficiency_score"`
	RejectReasons            []string                `json:"reject_reasons"`
	RequiresRebalance        bool                    `json:"requires_rebalance,omitempty"`
	// CandidateOnly 为 true 时仅作候选对比展示，不由统一轮动 OPEN/SWITCH 执行(路线 A: perp-perp)。
	// 零值 false 表示可执行，避免误伤未标记的四类 carry 候选(Go bool 零值为 false，故不用 CanExecute)。
	CandidateOnly bool `json:"candidate_only,omitempty"`
	// 以下字段供排行榜/诊断展示(扫描器从 live snap 透传,不参与闸门/EV 决策)。
	RawFundingRate       float64 `json:"raw_funding_rate,omitempty"`
	FundingRatePerHour   float64 `json:"funding_rate_per_hour,omitempty"`
	FundingAPR           float64 `json:"funding_apr,omitempty"`
	SpreadAPR            float64 `json:"spread_apr,omitempty"`
	HighFundingRate      float64 `json:"high_funding_rate,omitempty"`
	LowFundingRate       float64 `json:"low_funding_rate,omitempty"`
	FundingIntervalHours int     `json:"funding_interval_hours,omitempty"`
	NextFundingTime      string  `json:"next_funding_time,omitempty"`
	HighNextFundingTime  string  `json:"high_next_funding_time,omitempty"`
	LowNextFundingTime   string  `json:"low_next_funding_time,omitempty"`
}

func applyUnifiedFundingSnapDisplay(opp *UnifiedOpportunity, fund CrossVenueSpotPerpFundingSnap, now time.Time, rawRate float64) {
	if opp == nil {
		return
	}
	if rawRate == 0 {
		return
	}
	opp.RawFundingRate = rawRate
	ih := fund.IntervalHours
	if ih <= 0 {
		ih = 8
	}
	opp.FundingIntervalHours = ih
	opp.FundingRatePerHour = rawRate / float64(ih)
	opp.FundingAPR = fundingAnnualizedAPR(rawRate, ih)
	if !fund.NextFundingTime.IsZero() {
		opp.NextFundingTime = fund.NextFundingTime.UTC().Format(time.RFC3339)
	}
	_ = now
}

func applyUnifiedCrossVenueDisplay(opp *UnifiedOpportunity, cv CrossVenueOpportunity) {
	if opp == nil {
		return
	}
	opp.SpreadAPR = cv.SpreadAPR
	opp.HighFundingRate = cv.HighFundingRate
	opp.LowFundingRate = cv.LowFundingRate
	opp.FundingAPR = cv.SpreadAPR
	ih := cv.HighIntervalHours
	if ih <= 0 {
		ih = cv.LowIntervalHours
	}
	if ih <= 0 {
		ih = 8
	}
	opp.FundingIntervalHours = ih
	if cv.HighIntervalHours > 0 && cv.HighFundingRate != 0 {
		opp.FundingRatePerHour = cv.HighFundingRate / float64(cv.HighIntervalHours)
	}
	if !cv.HighNextFundingTime.IsZero() {
		opp.HighNextFundingTime = cv.HighNextFundingTime.UTC().Format(time.RFC3339)
		opp.NextFundingTime = opp.HighNextFundingTime
	}
	if !cv.LowNextFundingTime.IsZero() {
		opp.LowNextFundingTime = cv.LowNextFundingTime.UTC().Format(time.RFC3339)
	}
}

func unifiedOpportunityID(strategyType, symbol, snapshotID, highVenue, lowVenue string) string {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		snapshotID = "nosnap"
	}
	return fmt.Sprintf("%s:%s:%s:%s", strategyType, symbol, highVenue+"+"+lowVenue, snapshotID)
}

// crossVenuePerVenueMarginUSDT 单腿永续保证金 = 名义 / MaxEffectiveLeverage。
// MaxEffectiveLeverage 即真实可部署杠杆; MarginSafetyFactor 不参与 sizing(见 config 注释)。
func crossVenuePerVenueMarginUSDT(cfg CrossVenueExecutorConfig, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	lev := cfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	return notional / lev
}

func unifiedRiskScoreFromCrossVenue(opp CrossVenueOpportunity) float64 {
	quality := (opp.StabilityScore*0.35 + opp.ExecutionScore*0.25 + opp.RecoveryQualityScore*0.25 + opp.HistoryQualityScore*0.15)
	if quality <= 0 {
		quality = 0.5
	}
	return clamp01(1 - quality)
}

func crossVenueLegQty(notional, refPrice, tradableQty float64) float64 {
	if tradableQty > 0 {
		return tradableQty
	}
	if notional > 0 && refPrice > 0 {
		return notional / refPrice
	}
	return 0
}

func opportunityHasTradableLegs(opp UnifiedOpportunity) bool {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return true
		}
	}
	return false
}

// UnifiedOpportunityFromCrossVenue 把现有 CrossVenueOpportunity 映射为 UnifiedOpportunity(strategy_type=perp_perp)。
// 不修改输入 opp,仅做字段映射与派生。
func crossVenueOpportunityExplicitlyZeroNotional(opp CrossVenueOpportunity) bool {
	if opp.TradableNotional > 0 {
		return false
	}
	// scanner finalize 因 net_ev<=0 或 SizeMultiplier=0 显式清零名义时,勿回退 MaxPosition。
	return opp.SizeMultiplier == 0 || opp.NoTradeReason != ""
}

func UnifiedOpportunityFromCrossVenue(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, rejectReasons []string, notionalUSDT, refPrice float64) UnifiedOpportunity {
	if notionalUSDT <= 0 {
		notionalUSDT = opp.TradableNotional
	}
	if notionalUSDT <= 0 && !crossVenueOpportunityExplicitlyZeroNotional(opp) {
		notionalUSDT = cfg.MaxPositionUSDT
	}
	perVenueMargin := crossVenuePerVenueMarginUSDT(cfg, notionalUSDT)
	qty := crossVenueLegQty(notionalUSDT, refPrice, opp.TradableQty)
	reasons := append([]string(nil), rejectReasons...)
	if opp.NoTradeReason != "" && len(reasons) == 0 {
		reasons = append(reasons, opp.NoTradeReason)
	}
	out := UnifiedOpportunity{
		ID:           unifiedOpportunityID(UnifiedStrategyPerpPerp, opp.Symbol, opp.SnapshotID, opp.HighVenue, opp.LowVenue),
		StrategyType: UnifiedStrategyPerpPerp,
		Symbol:       opp.Symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: opp.HighVenue, Market: "perp", Side: "short", Qty: qty, Notional: notionalUSDT},
			{Venue: opp.LowVenue, Market: "perp", Side: "long", Qty: qty, Notional: notionalUSDT},
		},
		ExpectedFundingUSDT:      opp.SpreadEV,
		ExpectedOpenCostUSDT:     opp.OpenCost,
		ExpectedCloseCostUSDT:    opp.CloseCost,
		ExpectedSlippageUSDT:     opp.ExpectedSlippage,
		ExpectedBorrowCostUSDT:   0,
		ExpectedRollbackCostUSDT: 0,
		ExpectedNetEVUSDT:        opp.NetEV,
		ExpectedHoldHours:        opp.ExpectedHoldHours,
		CapitalRequiredByVenue: map[string]float64{
			opp.HighVenue: perVenueMargin,
			opp.LowVenue:  perVenueMargin,
		},
		RiskScore:              unifiedRiskScoreFromCrossVenue(opp),
		ExecutionScore:         opp.ExecutionScore,
		LiquidityScore:         opp.LiquidityScore,
		DataQualityScore:       opp.HistoryQualityScore,
		CapitalEfficiencyScore: opp.CapitalEfficiencyEV,
		RejectReasons:          reasons,
	}
	applyUnifiedCrossVenueDisplay(&out, opp)
	return out
}
