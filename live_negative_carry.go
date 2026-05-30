package main

import (
	"fmt"
	"math"
	"strings"
)

const liveFundingModePositive = "positive_only"
const liveFundingModeNegative = "negative_only"
const liveFundingModeBoth = "both"

type spotExecutionVenue string

const (
	spotExecutionVenueSpot        spotExecutionVenue = "spot"
	spotExecutionVenueCrossMargin spotExecutionVenue = "cross_margin"
)

func normalizeLiveFundingDirectionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case liveFundingModePositive:
		return liveFundingModePositive
	case liveFundingModeNegative:
		return liveFundingModeNegative
	case liveFundingModeBoth:
		return liveFundingModeBoth
	default:
		return liveFundingModePositive
	}
}

func isValidLiveFundingDirectionMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case liveFundingModePositive, liveFundingModeNegative, liveFundingModeBoth:
		return true
	default:
		return false
	}
}

func liveFundingDirectionEnabled(mode string, direction fundingCarryDirection) bool {
	switch normalizeLiveFundingDirectionMode(mode) {
	case liveFundingModeNegative:
		return direction == fundingCarryDirectionNegative
	case liveFundingModeBoth:
		return direction == fundingCarryDirectionPositive || direction == fundingCarryDirectionNegative
	default:
		return direction == fundingCarryDirectionPositive
	}
}

func liveFundingMagnitudePasses(cfg EngineConfig, direction fundingCarryDirection, rate float64, profile symbolRiskProfile) bool {
	if !liveFundingDirectionEnabled(cfg.StrategyConfig.LiveFundingDirectionMode, direction) {
		return false
	}
	magnitude := math.Abs(rate)
	if magnitude <= cfg.StrategyConfig.MinEstimatedFundingRate*profile.MinEstimatedRateMultiplier {
		return false
	}
	if magnitude >= cfg.StrategyConfig.MaxEstimatedFundingRate {
		return false
	}
	return true
}

func liveFundingPriorityScore(mode string, estimatedRate float64, basisRate float64) float64 {
	switch normalizeLiveFundingDirectionMode(mode) {
	case liveFundingModeNegative:
		return math.Max(-estimatedRate, 0) - math.Max(-basisRate, 0)
	case liveFundingModeBoth:
		return math.Abs(estimatedRate) - math.Abs(basisRate)
	default:
		return math.Max(estimatedRate, 0) - math.Max(basisRate, 0)
	}
}

func conservativeLiveFundingRate(lastFundingRate float64, premiumEstimate float64, interestRate float64) float64 {
	conservativeRate := premiumEstimate + interestRate
	switch {
	case conservativeRate > 0 && lastFundingRate > 0:
		return math.Min(lastFundingRate, conservativeRate)
	case conservativeRate < 0 && lastFundingRate < 0:
		return math.Max(lastFundingRate, conservativeRate)
	case conservativeRate == 0:
		return 0
	default:
		return 0
	}
}

func (d fundingCarryDirection) openFuturesSide() string {
	switch d {
	case fundingCarryDirectionNegative:
		return "BUY"
	default:
		return "SELL"
	}
}

func (d fundingCarryDirection) closeFuturesSide() string {
	switch d {
	case fundingCarryDirectionNegative:
		return "SELL"
	default:
		return "BUY"
	}
}

func (d fundingCarryDirection) openSpotSide() string {
	switch d {
	case fundingCarryDirectionNegative:
		return "SELL"
	default:
		return "BUY"
	}
}

func (d fundingCarryDirection) closeSpotSide() string {
	switch d {
	case fundingCarryDirectionNegative:
		return "BUY"
	default:
		return "SELL"
	}
}

func (d fundingCarryDirection) spotVenue() spotExecutionVenue {
	switch d {
	case fundingCarryDirectionNegative:
		return spotExecutionVenueCrossMargin
	default:
		return spotExecutionVenueSpot
	}
}

func (d fundingCarryDirection) futuresPositionSign() float64 {
	switch d {
	case fundingCarryDirectionNegative:
		return 1
	default:
		return -1
	}
}

func (d fundingCarryDirection) expectedFundingIncome(referencePrice float64, hedgeQty float64, estimatedRate float64) float64 {
	return historicalFundingIncome(d, hedgeQty, referencePrice, estimatedRate, 1)
}

func (d fundingCarryDirection) positionLabel(spotAsset string, symbol string) string {
	switch d {
	case fundingCarryDirectionNegative:
		return fmt.Sprintf("margin-short(%s)+long(%s)", spotAsset, symbol)
	default:
		return fmt.Sprintf("spot(%s)+short(%s)", spotAsset, symbol)
	}
}

func resolvedFundingCarryDirection(direction fundingCarryDirection, referenceRate float64) fundingCarryDirection {
	switch direction {
	case fundingCarryDirectionPositive, fundingCarryDirectionNegative:
		return direction
	default:
		return fundingDirectionFromRate(referenceRate)
	}
}

func (h OpenHedge) carryDirection() fundingCarryDirection {
	if h.Direction != "" {
		return resolvedFundingCarryDirection(h.Direction, h.Target.EstimatedRate)
	}
	return resolvedFundingCarryDirection(h.Target.Direction, h.Target.EstimatedRate)
}
