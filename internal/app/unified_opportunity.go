package app

import (
	"fmt"
	"strings"
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
	Venue    string  `json:"venue"`
	Market   string  `json:"market"`
	Side     string  `json:"side"`
	Qty      float64 `json:"qty"`
	Notional float64 `json:"notional"`
}

// UnifiedOpportunity 是三类资金费机会的统一语义结构(shadow/组合层用,不改变现有 CrossVenueOpportunity)。
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
}

func unifiedOpportunityID(strategyType, symbol, snapshotID, highVenue, lowVenue string) string {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		snapshotID = "nosnap"
	}
	return fmt.Sprintf("%s:%s:%s:%s", strategyType, symbol, highVenue+"+"+lowVenue, snapshotID)
}

func crossVenuePerVenueMarginUSDT(cfg CrossVenueExecutorConfig, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	lev := cfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	marginFactor := cfg.MarginSafetyFactor
	if marginFactor <= 0 {
		marginFactor = 1.5
	}
	return notional / lev * marginFactor
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

// UnifiedOpportunityFromCrossVenue 把现有 CrossVenueOpportunity 映射为 UnifiedOpportunity(strategy_type=perp_perp)。
// 不修改输入 opp,仅做字段映射与派生。
func UnifiedOpportunityFromCrossVenue(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, rejectReasons []string, notionalUSDT, refPrice float64) UnifiedOpportunity {
	if notionalUSDT <= 0 {
		notionalUSDT = opp.TradableNotional
	}
	if notionalUSDT <= 0 {
		notionalUSDT = cfg.MaxPositionUSDT
	}
	perVenueMargin := crossVenuePerVenueMarginUSDT(cfg, notionalUSDT)
	qty := crossVenueLegQty(notionalUSDT, refPrice, opp.TradableQty)
	reasons := append([]string(nil), rejectReasons...)
	if opp.NoTradeReason != "" && len(reasons) == 0 {
		reasons = append(reasons, opp.NoTradeReason)
	}
	return UnifiedOpportunity{
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
}
