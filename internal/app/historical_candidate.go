package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载月度模拟的「单候选构建 + 实际资金费率水合 +
// 校验」逻辑（同包搬移，行为不变），向单文件 ≤ 800 行门禁靠拢。

func (r *BacktestRunner) buildMonthlyCandidate(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, fundingTime time.Time, item FundingRateHistoryItem) (monthlyCandidate, error) {
	hcfg := r.historicalConfig()
	entryTime := fundingTime.Add(-hcfg.FundingPhaseLead)
	requestedHoldingCycles := r.monthlyHoldingCycles()
	fundingSchedule, err := cache.getFundingSchedule(ctx, client, item.Symbol, fundingTime, requestedHoldingCycles)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, "funding_schedule_incomplete", err, hcfg.CandidateBuildTimeout), nil
	}
	holdingCycles := len(fundingSchedule)
	exitTime := time.UnixMilli(fundingSchedule[len(fundingSchedule)-1].FundingTime).UTC()
	premiumEntryRef, err := cache.getFuturesPremiumKlineBefore(ctx, client, item.Symbol, entryTime)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, monthlyBuildSkipReason("premium_index_kline_unavailable", err), err, hcfg.CandidateBuildTimeout), nil
	}
	indexEntryRef, err := cache.getFuturesIndexKlineBefore(ctx, client, item.Symbol, entryTime)
	if err != nil {
		return monthlySkippedCandidate(item, fundingTime, monthlyBuildSkipReason("index_price_kline_unavailable", err), err, hcfg.CandidateBuildTimeout), nil
	}

	actualFundingLoaded := strings.TrimSpace(item.FundingRate) != ""
	fundingRate := parseFloat(item.FundingRate)
	markPrice := parseFloat(item.MarkPrice)
	preFundingBasisRate := 0.0
	if indexEntryRef.Close > 0 && markPrice > 0 {
		preFundingBasisRate = (markPrice - indexEntryRef.Close) / indexEntryRef.Close
	}
	if markPrice <= 0 && indexEntryRef.Close > 0 {
		markPrice = indexEntryRef.Close * (1 + premiumEntryRef.Close)
	}
	effectiveMarkPrice := markPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = indexEntryRef.Close
	}
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = 1
	}
	roughHedgeQty := r.monthlyPerLegNotional() / effectiveMarkPrice
	estimatedFundingRate := parseFloat(item.EstimatedFundingRate)
	if estimatedFundingRate == 0 {
		estimatedFundingRate = conservativeHistoricalFundingRate(premiumEntryRef.Close, preFundingBasisRate)
	}
	direction := fundingDirectionFromRate(estimatedFundingRate)
	estimatedFundingIncome := historicalFundingIncome(direction, roughHedgeQty, effectiveMarkPrice, estimatedFundingRate, holdingCycles)
	fundingIncome := 0.0
	fundingRateJump := 0.0
	if actualFundingLoaded {
		fundingIncome = historicalFundingIncome(direction, roughHedgeQty, effectiveMarkPrice, fundingRate, holdingCycles)
		fundingRateJump = math.Abs(fundingRate - estimatedFundingRate)
	}

	baseNotes := []string{
		"fundingDirectionMode=" + normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode),
		"direction=" + string(direction),
		fmt.Sprintf("requestedHoldingCycles=%d", requestedHoldingCycles),
		fmt.Sprintf("fundingScheduleLen=%d", holdingCycles),
		fmt.Sprintf("exitTime=%s", exitTime.UTC().Format(time.RFC3339)),
		fmt.Sprintf("premiumIndexClose=%.6f", premiumEntryRef.Close),
		fmt.Sprintf("markIndexBasis=%.6f", preFundingBasisRate),
		fmt.Sprintf("screeningScore=%s", item.ScreeningScore),
		fmt.Sprintf("actualFundingLoaded=%t", actualFundingLoaded),
		"screeningStage=entry_friction_liquidity_proxy",
		"estimatedFundingModel=conservative_premium_basis_haircut",
	}
	if holdingCycles < requestedHoldingCycles {
		baseNotes = append(baseNotes,
			"holdingCyclesShortened=true",
			fmt.Sprintf("fundingScheduleShortfall=%d", requestedHoldingCycles-holdingCycles),
		)
	}
	if holdingCycles > 1 {
		baseNotes = append(baseNotes, fmt.Sprintf("fundingInterval=%s", time.UnixMilli(fundingSchedule[1].FundingTime).UTC().Sub(time.UnixMilli(fundingSchedule[0].FundingTime).UTC())))
	}
	if actualFundingLoaded {
		baseNotes = append(baseNotes, fmt.Sprintf("fundingRateJump=%.6f", fundingRateJump))
	}

	if direction == "" {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			ScreeningScore:         parseFloat(item.ScreeningScore),
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "estimated_funding_non_positive",
			Notes:                  append([]string(nil), baseNotes...),
		}, nil
	}
	if !historicalFundingDirectionEnabled(hcfg.FundingDirectionMode, direction) {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "estimated_funding_direction_disabled",
			Notes:                  append([]string(nil), baseNotes...),
		}, nil
	}
	if math.Abs(estimatedFundingRate) >= hcfg.MaxFundingRate || (actualFundingLoaded && math.Abs(fundingRate) >= hcfg.MaxFundingRate) {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "funding_rate_extreme",
			Notes: append(baseNotes,
				fmt.Sprintf("maxFundingRate=%.6f", hcfg.MaxFundingRate),
			),
		}, nil
	}
	if actualFundingLoaded && fundingRateJump >= hcfg.MaxFundingRateJump {
		return monthlyCandidate{
			Symbol:                 item.Symbol,
			Direction:              direction,
			FundingTime:            fundingTime,
			ExitTime:               exitTime,
			HoldingCycles:          holdingCycles,
			EstimatedFundingRate:   estimatedFundingRate,
			FundingRate:            fundingRate,
			EstimatedFundingIncome: estimatedFundingIncome,
			FundingIncome:          fundingIncome,
			Reason:                 "funding_jump_too_large",
			Notes: append(baseNotes,
				fmt.Sprintf("maxFundingRateJump=%.6f", hcfg.MaxFundingRateJump),
			),
		}, nil
	}
	return monthlyCandidate{
		Symbol:                 item.Symbol,
		Direction:              direction,
		FundingTime:            fundingTime,
		ExitTime:               exitTime,
		HoldingCycles:          holdingCycles,
		EstimatedFundingRate:   estimatedFundingRate,
		ScreeningScore:         parseFloat(item.ScreeningScore),
		FundingRate:            fundingRate,
		EstimatedFundingIncome: estimatedFundingIncome,
		FundingIncome:          fundingIncome,
		Notes:                  append([]string(nil), baseNotes...),
	}, nil
}

func (r *BacktestRunner) hydrateMonthlyCandidateFunding(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	item, err := cache.getFundingRateAt(ctx, client, candidate.Symbol, candidate.FundingTime)
	if err != nil {
		return err
	}
	candidate.FundingRate = parseFloat(item.FundingRate)
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("actualFundingRate=%.6f", candidate.FundingRate),
		fmt.Sprintf("actualFundingTime=%s", time.UnixMilli(item.FundingTime).UTC().Format(time.RFC3339)),
	)
	return nil
}

func (r *BacktestRunner) validateMonthlyActualFunding(candidate monthlyCandidate) (string, []string) {
	hcfg := r.historicalConfig()
	actualDirection := fundingDirectionFromRate(candidate.FundingRate)
	if candidate.Direction != "" && actualDirection != "" && actualDirection != candidate.Direction {
		return "funding_direction_reversed", []string{
			"estimatedDirection=" + string(candidate.Direction),
			"actualDirection=" + string(actualDirection),
		}
	}
	if math.Abs(candidate.FundingRate) >= hcfg.MaxFundingRate {
		return "funding_rate_extreme", []string{fmt.Sprintf("maxFundingRate=%.6f", hcfg.MaxFundingRate)}
	}
	fundingRateJump := math.Abs(candidate.FundingRate - candidate.EstimatedFundingRate)
	if fundingRateJump >= hcfg.MaxFundingRateJump {
		return "funding_jump_too_large", []string{
			fmt.Sprintf("fundingRateJump=%.6f", fundingRateJump),
			fmt.Sprintf("maxFundingRateJump=%.6f", hcfg.MaxFundingRateJump),
		}
	}
	return "", nil
}
