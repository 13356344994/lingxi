package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"lingxi/internal/fillmodel"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载月度模拟的「点位预选打分 + 候选代理补全 +
// 残差成交模型 + 最终执行结算」逻辑（同包搬移，行为不变），向单文件 ≤ 800 行门禁靠拢。

func (r *BacktestRunner) estimateMonthlyPreselectScore(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, fundingTime time.Time, entryTime time.Time, estimatedFundingRate float64, estimatedMarkPrice float64) float64 {
	direction := fundingDirectionFromRate(estimatedFundingRate)
	if direction == "" {
		return 0
	}
	hcfg := r.historicalConfig()
	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, symbol)
	spotEntryRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	futuresEntryRef, err := cache.getFuturesKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	spotConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, symbol, entryTime, true)
	if err != nil {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	spotEntryPrice, spotEntryQuoteVolume := convertHistoricalSpotMetricsToUSDT(
		historicalKlineReferencePrice(spotEntryRef, true),
		spotEntryRef.QuoteAssetVolume,
		spotConversion,
	)
	futuresEntryPrice := historicalKlineReferencePrice(futuresEntryRef, true)
	if spotEntryPrice <= 0 || futuresEntryPrice <= 0 {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	hedgeQty := math.Min(r.monthlyPerLegNotional()/spotEntryPrice, r.monthlyPerLegNotional()/futuresEntryPrice)
	if hedgeQty <= 0 {
		return historicalFundingPriorityScore(FundingRateHistoryItem{EstimatedFundingRate: formatFloat(estimatedFundingRate)}, hcfg.FundingDirectionMode)
	}
	effectiveMarkPrice := estimatedMarkPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = futuresEntryPrice
	}
	spotEntryNotional := hedgeQty * spotEntryPrice
	futuresEntryNotional := hedgeQty * futuresEntryPrice
	fundingIncome := historicalFundingIncome(direction, hedgeQty, effectiveMarkPrice, estimatedFundingRate, maxInt(1, r.monthlyHoldingCycles()))
	borrowInterestHours := historicalBorrowInterestHours(direction, entryTime, fundingTime, hcfg.BorrowInterestMinBillingHours)
	borrowInterestCost := historicalBorrowInterestCost(direction, spotEntryNotional, r.hourlyBorrowInterestRateForSymbol(client, symbol), borrowInterestHours)
	spotSlip := estimateHistoricalSlipRate(spotEntryRef)
	futuresSlip := estimateHistoricalSlipRate(futuresEntryRef)
	spotImpactMultiplier := historicalSpotImpactMultiplier(hcfg, direction, true)
	spotImpact := estimateMarketImpactRate(spotEntryNotional*spotImpactMultiplier, spotEntryQuoteVolume)
	futuresImpact := estimateMarketImpactRate(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	entryCost := spotEntryNotional*(spotSlip+spotImpact+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(futuresSlip+futuresImpact+hcfg.FuturesTakerFeeRate)
	openBasisPenalty := computeAdverseBasisPenalty(computeBasisRate(spotEntryPrice, futuresEntryPrice), spotEntryNotional, futuresEntryNotional)
	spotLiquidityShare := ratioOrZero(spotEntryNotional, spotEntryQuoteVolume) * spotImpactMultiplier
	futuresLiquidityShare := ratioOrZero(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	liquidityPenalty := 0.0
	liquidityShareLimit := r.monthlyLiquidityShareLimit()
	severeLiquidityShareLimit := r.monthlySevereLiquidityShareLimit(liquidityShareLimit)
	if spotLiquidityShare > severeLiquidityShareLimit || futuresLiquidityShare > severeLiquidityShareLimit {
		liquidityPenalty += r.monthlyPerLegNotional()
	} else if spotLiquidityShare > liquidityShareLimit || futuresLiquidityShare > liquidityShareLimit {
		liquidityPenalty += r.monthlyPerLegNotional() * 0.25
	}
	return fundingIncome - entryCost - borrowInterestCost - openBasisPenalty - liquidityPenalty
}

func (r *BacktestRunner) completeMonthlyCandidateProxy(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	hcfg := r.historicalConfig()
	entryTime := candidate.FundingTime.Add(-hcfg.FundingPhaseLead)
	exitTime := candidate.ExitTime
	if exitTime.IsZero() {
		exitTime = r.monthlyExitTime(candidate.FundingTime)
		candidate.ExitTime = exitTime
	}
	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, candidate.Symbol)
	spotEntryRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return err
	}
	futuresEntryRef, err := cache.getFuturesKlineBefore(ctx, client, candidate.Symbol, entryTime)
	if err != nil {
		return err
	}
	spotExitRef, err := cache.getSpotKlineBefore(ctx, client, spotQuoteCtx.SpotPair, exitTime)
	if err != nil {
		return err
	}
	futuresExitRef, err := cache.getFuturesKlineBefore(ctx, client, candidate.Symbol, exitTime)
	if err != nil {
		return err
	}
	entryConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, entryTime, true)
	if err != nil {
		candidate.Reason = "cross_quote_fx_unavailable"
		candidate.Notes = append(candidate.Notes, err.Error(), "pricingModel=entry_proxy_screening")
		return nil
	}
	exitConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, exitTime, true)
	if err != nil {
		candidate.Reason = "cross_quote_fx_unavailable"
		candidate.Notes = append(candidate.Notes, err.Error(), "pricingModel=entry_proxy_screening")
		return nil
	}
	if entryConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate || exitConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate {
		candidate.Reason = "cross_quote_deviation_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
			fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
			fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
			fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
			fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
			fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
			fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
			fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
			fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.6f", hcfg.CrossStableQuoteMaxDeviationRate),
			"pricingModel=entry_proxy_screening",
		)
		return nil
	}

	spotEntryPrice, spotEntryQuoteVolume := convertHistoricalSpotMetricsToUSDT(spotEntryRef.Close, spotEntryRef.QuoteAssetVolume, entryConversion)
	futuresEntryPrice := futuresEntryRef.Close
	if spotEntryPrice <= 0 || futuresEntryPrice <= 0 {
		candidate.Reason = "invalid_market_data"
		candidate.Notes = append(candidate.Notes, "存在非正价格", "pricingModel=entry_proxy_screening")
		return nil
	}
	spotExitPrice, spotExitQuoteVolume := convertHistoricalSpotMetricsToUSDT(spotExitRef.Close, spotExitRef.QuoteAssetVolume, exitConversion)

	perLegNotional := r.monthlyPerLegNotional()
	hedgeQty, qtyReason := monthlyHedgeQty(perLegNotional, spotEntryPrice, futuresEntryPrice, SymbolTradeRules{}, SymbolTradeRules{})
	if hedgeQty <= 0 {
		candidate.Reason = "invalid_quantity"
		if qtyReason == "" {
			qtyReason = "对冲数量无法计算"
		}
		candidate.Notes = append(candidate.Notes, qtyReason, fmt.Sprintf("perLegNotional=%.4f", perLegNotional))
		return nil
	}

	spotEntryNotional := hedgeQty * spotEntryPrice
	futuresEntryNotional := hedgeQty * futuresEntryPrice
	spotExitNotional := hedgeQty * spotExitRef.Close
	futuresExitNotional := hedgeQty * futuresExitRef.Close
	effectiveMarkPrice := futuresEntryPrice
	if effectiveMarkPrice <= 0 {
		effectiveMarkPrice = 1
	}
	if candidate.Direction == "" {
		candidate.Direction = fundingDirectionFromRate(candidate.EstimatedFundingRate)
	}
	spotEntryImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, true)
	spotExitImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, false)
	borrowInterestHours := historicalBorrowInterestHours(candidate.Direction, entryTime, exitTime, hcfg.BorrowInterestMinBillingHours)
	candidate.HedgeQty = hedgeQty
	candidate.SpotEntryPrice = spotEntryPrice
	candidate.FuturesEntryPrice = futuresEntryPrice
	candidate.SpotExitPrice = spotExitPrice
	candidate.FuturesExitPrice = futuresExitRef.Close
	candidate.SpotEntryQuoteVolume = spotEntryQuoteVolume
	candidate.FuturesEntryQuoteVolume = futuresEntryRef.QuoteAssetVolume
	candidate.SpotExitQuoteVolume = spotExitQuoteVolume
	candidate.FuturesExitQuoteVolume = futuresExitRef.QuoteAssetVolume
	candidate.EstimatedFundingIncome = historicalFundingIncome(candidate.Direction, hedgeQty, effectiveMarkPrice, candidate.EstimatedFundingRate, maxInt(candidate.HoldingCycles, 1))
	candidate.FundingIncome = historicalFundingIncome(candidate.Direction, hedgeQty, effectiveMarkPrice, candidate.FundingRate, maxInt(candidate.HoldingCycles, 1))
	candidate.OpenBasisRate = computeBasisRate(spotEntryPrice, futuresEntryPrice)
	candidate.CloseBasisRate = computeBasisRate(candidate.SpotExitPrice, candidate.FuturesExitPrice)
	candidate.SpotEntrySlipRate = estimateHistoricalSlipRate(spotEntryRef)
	candidate.FuturesEntrySlipRate = estimateHistoricalSlipRate(futuresEntryRef)
	candidate.SpotExitSlipRate = estimateHistoricalSlipRate(spotExitRef)
	candidate.FuturesExitSlipRate = estimateHistoricalSlipRate(futuresExitRef)
	candidate.SpotEntryImpactRate = estimateMarketImpactRate(spotEntryNotional*spotEntryImpactMultiplier, spotEntryQuoteVolume)
	candidate.FuturesEntryImpactRate = estimateMarketImpactRate(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	candidate.SpotExitImpactRate = estimateMarketImpactRate(spotExitNotional*spotExitImpactMultiplier, spotExitQuoteVolume)
	candidate.FuturesExitImpactRate = estimateMarketImpactRate(futuresExitNotional, futuresExitRef.QuoteAssetVolume)
	borrowInterestBaseNotional := hedgeQty * effectiveMarkPrice
	if borrowInterestBaseNotional <= 0 {
		borrowInterestBaseNotional = spotEntryNotional
	}
	borrowInterestCost := historicalBorrowInterestCost(candidate.Direction, borrowInterestBaseNotional, r.hourlyBorrowInterestRateForSymbol(client, candidate.Symbol), borrowInterestHours)
	candidate.SpotEntryFee = spotEntryNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesEntryFee = futuresEntryNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotExitFee = spotExitNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesExitFee = futuresExitNotional * hcfg.FuturesTakerFeeRate
	candidate.TotalFees = candidate.SpotEntryFee + candidate.FuturesEntryFee + candidate.SpotExitFee + candidate.FuturesExitFee + borrowInterestCost
	candidate.TotalFriction = spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate) +
		spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate)
	candidate.ProjectedExitCost = spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate+hcfg.SpotTakerFeeRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.OpenBasisPenalty = computeAdverseBasisPenalty(candidate.OpenBasisRate, spotEntryNotional, futuresEntryNotional)
	entryExecutionCost := spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.EstimatedNetPnL = candidate.EstimatedFundingIncome - candidate.OpenBasisPenalty - candidate.ProjectedExitCost - entryExecutionCost - borrowInterestCost
	candidate.SpotGrossPnL = 0
	candidate.FuturesGrossPnL = 0
	candidate.RealizedNetPnL = candidate.FundingIncome - candidate.TotalFriction - candidate.TotalFees
	residualFill := r.monthlyResidualFillHaircut(hcfg,
		candidate,
		math.Max(spotEntryNotional, futuresEntryNotional),
		math.Max(spotExitNotional, futuresExitNotional))
	candidate.ResidualFillHaircut = residualFill.HaircutUSDT
	candidate.EstimatedNetPnL -= residualFill.HaircutUSDT
	candidate.RealizedNetPnL -= residualFill.HaircutUSDT
	liquidityShareLimit := r.monthlyLiquidityShareLimit()
	severeLiquidityShareLimit := r.monthlySevereLiquidityShareLimit(liquidityShareLimit)
	candidate.MinLiquidityRequired = math.Max(spotEntryNotional*spotEntryImpactMultiplier, futuresEntryNotional) / liquidityShareLimit
	rawSpotLiquidityShare := ratioOrZero(spotEntryNotional, spotEntryQuoteVolume)
	spotLiquidityShare := rawSpotLiquidityShare * spotEntryImpactMultiplier
	futuresLiquidityShare := ratioOrZero(futuresEntryNotional, futuresEntryRef.QuoteAssetVolume)
	liquidityRisk := EvaluateLiquidityShareRisk(math.Max(spotLiquidityShare, futuresLiquidityShare), liquidityShareLimit, severeLiquidityShareLimit)
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
		fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
		fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
		fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
		fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
		fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
		fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
		fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
		"direction="+string(candidate.Direction),
		fmt.Sprintf("estimatedFundingRate=%.6f", candidate.EstimatedFundingRate),
		fmt.Sprintf("actualFundingRate=%.6f", candidate.FundingRate),
		fmt.Sprintf("estimatedFundingIncome=%.6f", candidate.EstimatedFundingIncome),
		fmt.Sprintf("actualFundingIncome=%.6f", candidate.FundingIncome),
		fmt.Sprintf("holdingCycles=%d", maxInt(candidate.HoldingCycles, 1)),
		fmt.Sprintf("exitTime=%s", candidate.ExitTime.UTC().Format(time.RFC3339)),
		fmt.Sprintf("spotEntrySlip=%.6f", candidate.SpotEntrySlipRate),
		fmt.Sprintf("futuresEntrySlip=%.6f", candidate.FuturesEntrySlipRate),
		fmt.Sprintf("spotExitSlip=%.6f", candidate.SpotExitSlipRate),
		fmt.Sprintf("futuresExitSlip=%.6f", candidate.FuturesExitSlipRate),
		fmt.Sprintf("spotEntryImpact=%.6f", candidate.SpotEntryImpactRate),
		fmt.Sprintf("futuresEntryImpact=%.6f", candidate.FuturesEntryImpactRate),
		fmt.Sprintf("spotExitImpact=%.6f", candidate.SpotExitImpactRate),
		fmt.Sprintf("futuresExitImpact=%.6f", candidate.FuturesExitImpactRate),
		fmt.Sprintf("spotEntryImpactMultiplier=%.2f", spotEntryImpactMultiplier),
		fmt.Sprintf("spotExitImpactMultiplier=%.2f", spotExitImpactMultiplier),
		fmt.Sprintf("borrowInterestHours=%d", borrowInterestHours),
		fmt.Sprintf("borrowInterestCost=%.6f", borrowInterestCost),
		fmt.Sprintf("openBasisPenalty=%.6f", candidate.OpenBasisPenalty),
		fmt.Sprintf("projectedExitCost=%.6f", candidate.ProjectedExitCost),
		fmt.Sprintf("residualFillHaircut=%.6f", residualFill.HaircutUSDT),
		fmt.Sprintf("rawSpotLiquidityShare=%.6f", rawSpotLiquidityShare),
		fmt.Sprintf("spotLiquidityShare=%.6f", spotLiquidityShare),
		fmt.Sprintf("futuresLiquidityShare=%.6f", futuresLiquidityShare),
		fmt.Sprintf("liquidityShareLimit=%.6f", liquidityShareLimit),
		fmt.Sprintf("severeLiquidityShareLimit=%.6f", severeLiquidityShareLimit),
		fmt.Sprintf("liquidityRiskCategory=%s", liquidityRisk.Category),
		fmt.Sprintf("liquidityRiskSeverity=%s", liquidityRisk.Severity),
		fmt.Sprintf("liquidityRiskAction=%s", liquidityRisk.Action),
		"pricingModel=entry_proxy_screening",
		"exitCostModel=historical_exit_proxy",
	)

	if spotLiquidityShare > severeLiquidityShareLimit || futuresLiquidityShare > severeLiquidityShareLimit {
		candidate.Reason = "entry_liquidity_extremely_poor"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotEntryQuoteVol=%.6f", spotEntryQuoteVolume),
			fmt.Sprintf("futuresEntryQuoteVol=%.6f", futuresEntryRef.QuoteAssetVolume),
		)
		return nil
	}
	if spotLiquidityShare > liquidityShareLimit || futuresLiquidityShare > liquidityShareLimit {
		candidate.Reason = "entry_liquidity_share_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotEntryQuoteVol=%.6f", spotEntryQuoteVolume),
			fmt.Sprintf("futuresEntryQuoteVol=%.6f", futuresEntryRef.QuoteAssetVolume),
		)
		return nil
	}
	safetyBufferUSDT := r.monthlySafetyBuffer(entryExecutionCost, candidate.ProjectedExitCost, candidate.OpenBasisPenalty)
	if candidate.EstimatedNetPnL <= safetyBufferUSDT {
		candidate.Reason = "estimated_net_below_safety_buffer"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("safetyBufferUSDT=%.6f", safetyBufferUSDT),
			fmt.Sprintf("entryExecutionCost=%.6f", entryExecutionCost),
			fmt.Sprintf("safetyBufferGap=%.6f", safetyBufferUSDT-candidate.EstimatedNetPnL),
			fmt.Sprintf("estimatedNetPnL=%.6f", candidate.EstimatedNetPnL),
		)
		return nil
	}
	return nil
}

// newResidualCostModel 是「成交/费用模型」的唯一装配点：把原始 HistoricalConfig 的残腿参数
// 归一后构造出 fillmodel.CostModel。回测主路径与实盘引擎都从这里取模型，从结构上保证两侧
// 共注入同一套残腿/部分成交成本口径，根除「回测与实盘对不上」的漂移（Gate 3）。
func newResidualCostModel(hcfg HistoricalConfig) fillmodel.CostModel {
	return fillmodel.ResidualCostModel{Params: normalizedResidualParams(hcfg)}
}

// normalizedResidualParams 复用与 historicalConfig() 完全一致的归一规则（完全未配置时默认开启
// haircut、缺省填充率 0.99、拒单概率落在 [0,1] 且未配置时默认 0.01、紧急对冲乘数下限 4.0），
// 让回测与实盘从同一原始配置得到逐字节一致的成本模型参数。
func normalizedResidualParams(hcfg HistoricalConfig) fillmodel.ResidualParams {
	unset := hcfg == HistoricalConfig{}
	params := fillmodel.ResidualParams{
		Enabled:                  hcfg.ResidualFillHaircutEnabled,
		ExpectedLegFillRatio:     hcfg.ExpectedLegFillRatio,
		LegRejectProbability:     hcfg.LegRejectProbability,
		EmergencyHedgeMultiplier: hcfg.EmergencyHedgeSpreadMultiplier,
	}
	if unset {
		params.Enabled = true
	}
	if params.ExpectedLegFillRatio <= 0 || params.ExpectedLegFillRatio > 1 {
		params.ExpectedLegFillRatio = 0.99
	}
	if params.LegRejectProbability < 0 {
		params.LegRejectProbability = 0
	}
	if params.LegRejectProbability > 1 {
		params.LegRejectProbability = 1
	}
	if params.LegRejectProbability == 0 && unset {
		params.LegRejectProbability = 0.01
	}
	if params.EmergencyHedgeMultiplier < 1 {
		params.EmergencyHedgeMultiplier = 4.0
	}
	return params
}

// residualCostModel 把历史回测配置装配成共享的 fillmodel.CostModel，
// 回测与实盘由此共用同一套残腿/部分成交成本口径，从结构上避免漂移（Gate 3）。
func (r *BacktestRunner) residualCostModel(hcfg historicalConfig) fillmodel.CostModel {
	return fillmodel.ResidualCostModel{Params: fillmodel.ResidualParams{
		Enabled:                  hcfg.ResidualFillHaircutEnabled,
		ExpectedLegFillRatio:     hcfg.ExpectedLegFillRatio,
		LegRejectProbability:     hcfg.LegRejectProbability,
		EmergencyHedgeMultiplier: hcfg.EmergencyHedgeSpreadMultiplier,
	}}
}

// monthlyResidualFillHaircut 基于候选当前已填充的滑点/冲击/费率字段，调用共享成本模型计算残腿 haircut。
func (r *BacktestRunner) monthlyResidualFillHaircut(hcfg historicalConfig, candidate *monthlyCandidate, openNotional float64, closeNotional float64) fillmodel.ResidualBreakdown {
	roundTripPerSideFee := hcfg.SpotTakerFeeRate + hcfg.FuturesTakerFeeRate
	openFrictionRate := candidate.SpotEntrySlipRate + candidate.SpotEntryImpactRate +
		candidate.FuturesEntrySlipRate + candidate.FuturesEntryImpactRate + roundTripPerSideFee
	closeFrictionRate := candidate.SpotExitSlipRate + candidate.SpotExitImpactRate +
		candidate.FuturesExitSlipRate + candidate.FuturesExitImpactRate + roundTripPerSideFee
	return r.residualCostModel(hcfg).ResidualFillCost(fillmodel.RoundTrip{
		OpenNotional:      openNotional,
		CloseNotional:     closeNotional,
		OpenFrictionRate:  openFrictionRate,
		CloseFrictionRate: closeFrictionRate,
		OpenBasisRate:     candidate.OpenBasisRate,
		CloseBasisRate:    candidate.CloseBasisRate,
	})
}

func (r *BacktestRunner) finalizeMonthlyCandidateExecution(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, candidate *monthlyCandidate) error {
	hcfg := r.historicalConfig()
	entryTime := candidate.FundingTime.Add(-hcfg.FundingPhaseLead)
	exitTime := candidate.ExitTime
	if exitTime.IsZero() {
		exitTime = r.monthlyExitTime(candidate.FundingTime)
		candidate.ExitTime = exitTime
	}

	spotQuoteCtx := resolveHistoricalSpotQuoteContext(client, candidate.Symbol)
	spotEntryExec, err := cache.getSpotKline(ctx, client, spotQuoteCtx.SpotPair, entryTime)
	if err != nil {
		return fmt.Errorf("entry_spot_execution: %w", err)
	}
	futuresEntryExec, err := cache.getFuturesKline(ctx, client, candidate.Symbol, entryTime)
	if err != nil {
		return fmt.Errorf("entry_futures_execution: %w", err)
	}
	spotExitExec, err := cache.getSpotKline(ctx, client, spotQuoteCtx.SpotPair, exitTime)
	if err != nil {
		return fmt.Errorf("exit_spot_execution: %w", err)
	}
	futuresExitExec, err := cache.getFuturesKline(ctx, client, candidate.Symbol, exitTime)
	if err != nil {
		return fmt.Errorf("exit_futures_execution: %w", err)
	}
	entryConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, entryTime, false)
	if err != nil {
		return fmt.Errorf("entry_spot_fx_execution: %w", err)
	}
	exitConversion, err := historicalSpotQuoteConversionAt(ctx, client, cache, candidate.Symbol, exitTime, false)
	if err != nil {
		return fmt.Errorf("exit_spot_fx_execution: %w", err)
	}
	if entryConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate || exitConversion.DeviationRate > hcfg.CrossStableQuoteMaxDeviationRate {
		candidate.Reason = "cross_quote_deviation_too_high"
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
			fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
			fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
			fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
			fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
			fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
			fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
			fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
			fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.6f", hcfg.CrossStableQuoteMaxDeviationRate),
			"executionModel=exact_entry_exit_open",
		)
		return nil
	}

	candidate.SpotEntryPrice, candidate.SpotEntryQuoteVolume = convertHistoricalSpotMetricsToUSDT(spotEntryExec.Open, spotEntryExec.QuoteAssetVolume, entryConversion)
	candidate.FuturesEntryPrice = futuresEntryExec.Open
	candidate.SpotExitPrice, candidate.SpotExitQuoteVolume = convertHistoricalSpotMetricsToUSDT(spotExitExec.Open, spotExitExec.QuoteAssetVolume, exitConversion)
	candidate.FuturesExitPrice = futuresExitExec.Open
	candidate.FuturesEntryQuoteVolume = futuresEntryExec.QuoteAssetVolume
	candidate.FuturesExitQuoteVolume = futuresExitExec.QuoteAssetVolume
	if candidate.Direction == "" {
		candidate.Direction = fundingDirectionFromRate(candidate.EstimatedFundingRate)
	}
	spotEntryImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, true)
	spotExitImpactMultiplier := historicalSpotImpactMultiplier(hcfg, candidate.Direction, false)
	borrowInterestHours := historicalBorrowInterestHours(candidate.Direction, entryTime, exitTime, hcfg.BorrowInterestMinBillingHours)

	spotEntryNotional := candidate.HedgeQty * candidate.SpotEntryPrice
	futuresEntryNotional := candidate.HedgeQty * candidate.FuturesEntryPrice
	spotExitNotional := candidate.HedgeQty * candidate.SpotExitPrice
	futuresExitNotional := candidate.HedgeQty * candidate.FuturesExitPrice

	candidate.OpenBasisRate = computeBasisRate(candidate.SpotEntryPrice, candidate.FuturesEntryPrice)
	candidate.CloseBasisRate = computeBasisRate(candidate.SpotExitPrice, candidate.FuturesExitPrice)
	candidate.SpotGrossPnL = candidate.Direction.spotGrossPnL(candidate.HedgeQty, candidate.SpotEntryPrice, candidate.SpotExitPrice)
	candidate.FuturesGrossPnL = candidate.Direction.futuresGrossPnL(candidate.HedgeQty, candidate.FuturesEntryPrice, candidate.FuturesExitPrice)
	borrowInterestCost := historicalBorrowInterestCost(candidate.Direction, spotEntryNotional, r.hourlyBorrowInterestRateForSymbol(client, candidate.Symbol), borrowInterestHours)
	candidate.TotalFees = spotEntryNotional*hcfg.SpotTakerFeeRate +
		futuresEntryNotional*hcfg.FuturesTakerFeeRate +
		spotExitNotional*hcfg.SpotTakerFeeRate +
		futuresExitNotional*hcfg.FuturesTakerFeeRate +
		borrowInterestCost
	candidate.SpotEntryFee = spotEntryNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesEntryFee = futuresEntryNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotExitFee = spotExitNotional * hcfg.SpotTakerFeeRate
	candidate.FuturesExitFee = futuresExitNotional * hcfg.FuturesTakerFeeRate
	candidate.SpotEntryImpactRate = estimateMarketImpactRate(spotEntryNotional*spotEntryImpactMultiplier, candidate.SpotEntryQuoteVolume)
	candidate.SpotExitImpactRate = estimateMarketImpactRate(spotExitNotional*spotExitImpactMultiplier, candidate.SpotExitQuoteVolume)
	candidate.TotalFriction = spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate) +
		spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate)
	candidate.ProjectedExitCost = spotExitNotional*(candidate.SpotExitSlipRate+candidate.SpotExitImpactRate+hcfg.SpotTakerFeeRate) +
		futuresExitNotional*(candidate.FuturesExitSlipRate+candidate.FuturesExitImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.OpenBasisPenalty = computeAdverseBasisPenalty(candidate.OpenBasisRate, spotEntryNotional, futuresEntryNotional)
	entryExecutionCost := spotEntryNotional*(candidate.SpotEntrySlipRate+candidate.SpotEntryImpactRate+hcfg.SpotTakerFeeRate) +
		futuresEntryNotional*(candidate.FuturesEntrySlipRate+candidate.FuturesEntryImpactRate+hcfg.FuturesTakerFeeRate)
	candidate.EstimatedNetPnL = candidate.EstimatedFundingIncome - candidate.OpenBasisPenalty - candidate.ProjectedExitCost - entryExecutionCost - borrowInterestCost
	actualFundingIncome, err := r.sumMonthlyFundingIncome(ctx, client, cache, candidate.Symbol, candidate.Direction, candidate.HedgeQty, candidate.FuturesEntryPrice, candidate.FundingTime)
	if err != nil {
		return fmt.Errorf("funding_income_accumulate: %w", err)
	}
	candidate.FundingIncome = actualFundingIncome
	candidate.RealizedNetPnL = candidate.FundingIncome + candidate.SpotGrossPnL + candidate.FuturesGrossPnL - candidate.TotalFriction - candidate.TotalFees
	residualFill := r.monthlyResidualFillHaircut(hcfg,
		candidate,
		math.Max(spotEntryNotional, futuresEntryNotional),
		math.Max(spotExitNotional, futuresExitNotional))
	candidate.ResidualFillHaircut = residualFill.HaircutUSDT
	candidate.EstimatedNetPnL -= residualFill.HaircutUSDT
	candidate.RealizedNetPnL -= residualFill.HaircutUSDT
	candidate.Notes = append(candidate.Notes,
		fmt.Sprintf("spotPair=%s", entryConversion.SpotPair),
		fmt.Sprintf("spotQuoteAsset=%s", entryConversion.QuoteAsset),
		fmt.Sprintf("spotEntryFXPair=%s", entryConversion.FXSymbol),
		fmt.Sprintf("spotExitFXPair=%s", exitConversion.FXSymbol),
		fmt.Sprintf("spotEntryFXRateToUSDT=%.6f", entryConversion.RateToUSDT),
		fmt.Sprintf("spotExitFXRateToUSDT=%.6f", exitConversion.RateToUSDT),
		fmt.Sprintf("spotEntryFXDeviationRate=%.6f", entryConversion.DeviationRate),
		fmt.Sprintf("spotExitFXDeviationRate=%.6f", exitConversion.DeviationRate),
		"direction="+string(candidate.Direction),
		"executionModel=exact_entry_exit_open",
		fmt.Sprintf("spotEntryImpactMultiplier=%.2f", spotEntryImpactMultiplier),
		fmt.Sprintf("spotExitImpactMultiplier=%.2f", spotExitImpactMultiplier),
		fmt.Sprintf("borrowInterestHours=%d", borrowInterestHours),
		fmt.Sprintf("borrowInterestCost=%.6f", borrowInterestCost),
		fmt.Sprintf("holdingCycles=%d", maxInt(candidate.HoldingCycles, 1)),
		fmt.Sprintf("exitTime=%s", candidate.ExitTime.UTC().Format(time.RFC3339)),
		fmt.Sprintf("residualFillHaircut=%.6f", residualFill.HaircutUSDT),
		fmt.Sprintf("residualPartialFillCost=%.6f", residualFill.PartialFillCostUSDT),
		fmt.Sprintf("residualLegRejectCost=%.6f", residualFill.LegRejectCostUSDT),
		fmt.Sprintf("expectedLegFillRatio=%.4f", hcfg.ExpectedLegFillRatio),
		fmt.Sprintf("legRejectProbability=%.4f", hcfg.LegRejectProbability),
	)
	return nil
}
