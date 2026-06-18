package app

import (
	"math"
	"strings"
	"time"
)

const (
	// attributionFixBoundaryUTC 归因修复边界(fab6afd);早于此 close_time 的 settlement 视为 untrusted_legacy。
	attributionFixBoundaryUTC = "2026-06-17T14:40:22Z"
	// abnormalTradePnLUSDT 单笔 |trade_pnl| 超过此值视为归因幻觉(如 HEI -801),不参与可信 PnL/风控/go-no-go。
	abnormalTradePnLUSDT = 50.0
)

var attributionFixBoundaryTime = mustParseAttributionFixBoundary()

func mustParseAttributionFixBoundary() time.Time {
	t, err := time.Parse(time.RFC3339, attributionFixBoundaryUTC)
	if err != nil {
		panic("invalid attributionFixBoundaryUTC: " + err.Error())
	}
	return t.UTC()
}

// settlementTrustInput 统一可信样本判据输入;Check* 为 false 时该字段不参与判定(缺字段安全)。
type settlementTrustInput struct {
	CloseTime                  time.Time
	FundingTruthStatus         string
	CheckFundingTruthStatus    bool
	AttributionIncomplete      bool
	CheckAttributionIncomplete bool
	TradePnL                   float64
	CheckTradePnL              bool
}

// isTrustedSettlement 判定 settlement 是否可信;任一 untrusted_legacy 条件满足则返回 false。
func isTrustedSettlement(in settlementTrustInput) bool {
	if in.CloseTime.IsZero() || in.CloseTime.Before(attributionFixBoundaryTime) {
		return false
	}
	if in.CheckFundingTruthStatus && strings.TrimSpace(in.FundingTruthStatus) == "" {
		return false
	}
	if in.CheckAttributionIncomplete && in.AttributionIncomplete {
		return false
	}
	if in.CheckTradePnL && math.Abs(in.TradePnL) > abnormalTradePnLUSDT {
		return false
	}
	return true
}

func crossVenueSettlementTrustInput(rec CrossVenueSettlement) settlementTrustInput {
	return settlementTrustInput{
		CloseTime:                  rec.CloseTime,
		FundingTruthStatus:         rec.FundingTruthStatus,
		CheckFundingTruthStatus:    true,
		AttributionIncomplete:      rec.AttributionIncomplete,
		CheckAttributionIncomplete: true,
		TradePnL:                   rec.TradePnL,
		CheckTradePnL:              true,
	}
}

func strategySettlementTrustInput(rec strategySettlementRecord) settlementTrustInput {
	in := settlementTrustInput{CloseTime: rec.CloseTime}
	switch rec.StrategyType {
	case UnifiedStrategyPerpPerp:
		in.CheckFundingTruthStatus = true
		in.FundingTruthStatus = rec.FundingTruthStatus
		in.CheckAttributionIncomplete = true
		in.AttributionIncomplete = rec.AttributionIncomplete
		in.CheckTradePnL = true
		in.TradePnL = rec.TradePnL
	case UnifiedStrategySingleVenueSpotPerp, UnifiedStrategySingleVenuePerpSpot:
		in.CheckTradePnL = true
		in.TradePnL = rec.TradePnL
	}
	return in
}

func isTrustedStrategySettlementRecord(rec strategySettlementRecord) bool {
	return isTrustedSettlement(strategySettlementTrustInput(rec))
}

func attributionRealizedTrustInput(rec AttributionRealizedRecord) settlementTrustInput {
	in := settlementTrustInput{CloseTime: rec.CloseTime}
	switch rec.Strategy {
	case UnifiedStrategyPerpPerp:
		in.CheckFundingTruthStatus = true
		in.FundingTruthStatus = rec.FundingTruthStatus
		in.CheckAttributionIncomplete = true
		in.AttributionIncomplete = !rec.AttributionComplete
		in.CheckTradePnL = true
		in.TradePnL = rec.RealizedTradePnLUSDT
	case UnifiedStrategySingleVenueSpotPerp, UnifiedStrategySingleVenuePerpSpot:
		in.CheckTradePnL = true
		in.TradePnL = rec.RealizedTradePnLUSDT
	}
	return in
}

func isTrustedAttributionRealized(rec AttributionRealizedRecord) bool {
	return isTrustedSettlement(attributionRealizedTrustInput(rec))
}
