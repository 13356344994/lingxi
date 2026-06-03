package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载月度模拟的「逐时点资金费率回放状态机 +
// 组内候选择优」逻辑（同包搬移，行为不变），向单文件 ≤ 800 行门禁靠拢。

func (r *BacktestRunner) simulateMonthlyFundingGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, groups []monthlyEventGroup, periodLabel string) ([]BacktestTradeSample, []MonthlySimulationLogRecord, monthlySimulationStats, string, error) {
	hcfg := r.historicalConfig()
	equity := hcfg.InitialEquityUSDT
	peakEquity := hcfg.InitialEquityUSDT
	stopReason := "lookback_completed"
	consecutiveLosses := 0
	stats := monthlySimulationStats{
		GroupCount:       len(groups),
		SkipReasons:      make(map[string]int),
		CandidateReasons: make(map[string]int),
	}

	trades := make([]BacktestTradeSample, 0, len(groups))
	logs := make([]MonthlySimulationLogRecord, 0, len(groups)+1)
	processedGroups := 0

	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, nil, stats, "context_canceled", err
		}
		processedGroups++
		drawdownBefore := computeDrawdownRate(peakEquity, equity)
		if r.logger != nil && (processedGroups == len(groups) || processedGroups%monthlyEvaluationProgressStep == 0) {
			r.logger.Printf("月度模拟评估进度：%d/%d，已评估候选=%d，已成交=%d，已跳过轮次=%d", processedGroups, len(groups), stats.EvaluatedCandidates, stats.TradeCount, stats.SkippedGroups)
		}
		r.reportProgress(EngineStage("evaluate_groups"),
			fmt.Sprintf("正在评估 funding 时点: %d/%d，已成交=%d，已跳过=%d", processedGroups, len(groups), stats.TradeCount, stats.SkippedGroups),
			processedGroups, len(groups))
		if drawdownBefore >= hcfg.MaxDrawdownRate || equity <= hcfg.StopEquityUSDT {
			stopReason = "drawdown_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				EquityBefore: formatFloat(equity),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownBefore),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("权益回撤已达到 %.0f%% 或权益已跌到 %.2f USDT 以下，停止后续模拟", hcfg.MaxDrawdownRate*100, hcfg.StopEquityUSDT),
			})
			break
		}

		best, evaluated, groupReason, err := r.selectBestMonthlyCandidate(ctx, client, cache, group)
		if err != nil {
			return nil, nil, stats, nonEmpty(groupReason, "context_canceled"), err
		}
		stats.EvaluatedCandidates += len(evaluated)
		bestFound := false
		if best != nil {
			bestFound = true
		}
		for _, candidate := range evaluated {
			action := "evaluate_skip"
			reason := candidate.Reason
			extraNotes := []string{}
			if strings.TrimSpace(candidate.Reason) != "" {
				stats.CandidateReasons[candidate.Reason]++
			}
			switch {
			case candidate.Reason != "":
			case bestFound && candidate.Symbol == best.Symbol:
				action = "evaluate_selected"
				reason = "selected_for_trade"
			default:
				action = "evaluate_ranked_out"
				reason = "viable_but_not_top1"
				extraNotes = append(extraNotes, fmt.Sprintf("bestEstimatedNetPnL=%.6f", best.EstimatedNetPnL))
			}
			logs = append(logs, buildMonthlyCandidateLogRecord(candidate, action, equity, equity, drawdownBefore, reason, extraNotes...))
		}
		logs = append(logs, buildMonthlyGroupSummaryLogRecord(group, evaluated, best, equity, drawdownBefore, groupReason))
		if best == nil {
			stats.SkippedGroups++
			skipped := summarizeMonthlyGroupSkip(group, evaluated, groupReason, hcfg.GroupEvaluationTimeout)
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "skip",
				EquityBefore: formatFloat(equity),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownBefore),
				Reason:       nonEmpty(skipped.Reason, "no_viable_candidate"),
				Notes:        strings.Join(skipped.Notes, " | "),
			})
			stats.SkipReasons[nonEmpty(skipped.Reason, "no_viable_candidate")]++
			continue
		}

		equityBefore := equity
		equity += best.RealizedNetPnL
		stats.TradeCount++
		if equity > peakEquity {
			peakEquity = equity
		}
		drawdownRate := computeDrawdownRate(peakEquity, equity)
		drawdownAmount := math.Max(0, peakEquity-equity)
		if best.RealizedNetPnL < 0 {
			consecutiveLosses++
		} else {
			consecutiveLosses = 0
		}

		logs = append(logs, buildMonthlyTradeLogRecord(best, "trade", equityBefore, equity, drawdownRate))
		trades = append(trades, BacktestTradeSample{
			Scenario:      r.monthlyScenarioName("MonthlyMarketReplay"),
			Symbol:        best.Symbol,
			Period:        periodLabel,
			TradeID:       best.FundingTime.Format(time.RFC3339),
			FundingIncome: best.FundingIncome,
			TradingPnL:    best.SpotGrossPnL + best.FuturesGrossPnL - best.TotalFriction - best.ResidualFillHaircut,
			TotalFees:     best.TotalFees,
			Win:           best.RealizedNetPnL > 0,
			Drawdown:      -drawdownAmount,
			Notes:         strings.Join(best.Notes, " | "),
		})

		if r.logger != nil {
			r.logger.Printf("月度模拟成交 fundingTime=%s symbol=%s fundingRate=%.6f fundingIncome=%.6f openBasis=%.6f closeBasis=%.6f fees=%.6f friction=%.6f realizedNet=%.6f equity=%.6f drawdown=%.6f",
				best.FundingTime.Format(time.RFC3339), best.Symbol, best.FundingRate, best.FundingIncome, best.OpenBasisRate, best.CloseBasisRate, best.TotalFees, best.TotalFriction, best.RealizedNetPnL, equity, drawdownRate)
		}
		if hcfg.MaxSingleTradeLossUSDT > 0 && best.RealizedNetPnL <= -hcfg.MaxSingleTradeLossUSDT {
			stopReason = "single_trade_loss_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("单笔亏损 %.6f USDT 已达到阈值 %.6f USDT，停止后续模拟", -best.RealizedNetPnL, hcfg.MaxSingleTradeLossUSDT),
			})
			break
		}

		if consecutiveLosses >= hcfg.MaxConsecutiveLosses {
			stopReason = "consecutive_losses_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("连续亏损次数已达到 %d 次，停止后续模拟", hcfg.MaxConsecutiveLosses),
			})
			break
		}

		if drawdownRate >= hcfg.MaxDrawdownRate || equity <= hcfg.StopEquityUSDT {
			stopReason = "drawdown_limit_reached"
			logs = append(logs, MonthlySimulationLogRecord{
				RecordedAt:   time.Now().UTC().Format(time.RFC3339),
				FundingTime:  group.FundingTime.Format(time.RFC3339),
				Action:       "stop",
				Symbol:       best.Symbol,
				EquityBefore: formatFloat(equityBefore),
				EquityAfter:  formatFloat(equity),
				DrawdownRate: formatFloat(drawdownRate),
				Reason:       stopReason,
				Notes:        fmt.Sprintf("本轮交易后触发 %.0f%% 回撤或止损权益规则", hcfg.MaxDrawdownRate*100),
			})
			break
		}
	}

	if r.logger != nil && len(stats.SkipReasons) > 0 {
		r.logger.Printf("月度模拟轮次跳过汇总：%s", summarizeMonthlyReasonCounts(stats.SkipReasons, nil))
	}
	if r.logger != nil && len(stats.CandidateReasons) > 0 {
		r.logger.Printf("月度模拟候选过滤汇总：%s", summarizeMonthlyReasonCounts(stats.CandidateReasons, nil))
	}
	stats.CacheSummary = cache.summary()
	if r.logger != nil && stats.CacheSummary != "" {
		r.logger.Printf("月度模拟 K 线缓存摘要：%s", stats.CacheSummary)
	}
	return trades, logs, stats, stopReason, nil
}

func (r *BacktestRunner) selectBestMonthlyCandidate(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, group monthlyEventGroup) (*monthlyCandidate, []monthlyCandidate, string, error) {
	hcfg := r.historicalConfig()
	groupTimeout := adaptiveMonthlyGroupTimeout(hcfg.GroupEvaluationTimeout, len(group.Items))
	groupCtx, cancel := context.WithTimeout(ctx, groupTimeout)
	defer cancel()

	screeningItems, prefiltered := r.preselectMonthlyGroupItems(group)
	evaluated := make([]monthlyCandidate, 0, len(group.Items))
	evaluated = append(evaluated, prefiltered...)
	screened := make([]monthlyCandidate, 0, len(screeningItems))
	groupReason := ""
	logAgg := newMonthlyLogAggregation()
	type screeningResult struct {
		item      FundingRateHistoryItem
		candidate monthlyCandidate
		err       error
	}
	results := make(chan screeningResult, len(screeningItems))
	sem := make(chan struct{}, hcfg.ScreeningConcurrency)
	var wg sync.WaitGroup
	for _, item := range screeningItems {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-groupCtx.Done():
				results <- screeningResult{item: item, err: groupCtx.Err()}
				return
			}
			defer func() { <-sem }()

			candidateBuildTimeout := adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))
			candidateCtx, candidateCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
			candidateFundingTime := group.FundingTime
			if item.FundingTime > 0 {
				candidateFundingTime = time.UnixMilli(item.FundingTime).UTC()
			}
			candidate, err := r.buildMonthlyCandidate(candidateCtx, client, cache, candidateFundingTime, item)
			candidateCancel()
			results <- screeningResult{item: item, candidate: candidate, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			if errors.Is(result.err, context.DeadlineExceeded) || errors.Is(result.err, context.Canceled) {
				candidateFundingTime := group.FundingTime
				if result.item.FundingTime > 0 {
					candidateFundingTime = time.UnixMilli(result.item.FundingTime).UTC()
				}
				evaluated = append(evaluated, monthlyCandidate{
					Symbol:      result.item.Symbol,
					FundingTime: candidateFundingTime,
					FundingRate: parseFloat(result.item.FundingRate),
					Reason:      "candidate_build_timeout",
					Notes: []string{
						"conservativeSkip=true",
						fmt.Sprintf("timeout=%s", adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))),
						result.err.Error(),
					},
				})
				logAgg.add("candidate_build_timeout", result.item.Symbol)
				continue
			}
			if r.logger != nil {
				r.logger.Printf("历史模拟评估失败 fundingTime=%s symbol=%s err=%v", group.FundingTime.Format(time.RFC3339), result.item.Symbol, result.err)
			}
			continue
		}
		if result.candidate.Reason == "candidate_build_timeout" {
			logAgg.add("candidate_build_timeout", result.item.Symbol)
		}
		if strings.TrimSpace(result.candidate.Reason) != "" {
			evaluated = append(evaluated, result.candidate)
			continue
		}
		screened = append(screened, result.candidate)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, "context_canceled", err
	}

	if groupCtx.Err() != nil {
		if errors.Is(groupCtx.Err(), context.Canceled) {
			return nil, nil, "context_canceled", groupCtx.Err()
		}
		groupReason = "group_evaluation_timeout"
	}

	sort.SliceStable(screened, func(i, j int) bool {
		left := monthlyCandidateScreeningPriorityScore(screened[i])
		right := monthlyCandidateScreeningPriorityScore(screened[j])
		if left == right {
			return screened[i].Symbol < screened[j].Symbol
		}
		return left > right
	})

	if len(screened) > hcfg.DeepCandidateLimit {
		for index := hcfg.DeepCandidateLimit; index < len(screened); index++ {
			candidate := screened[index]
			candidate.Reason = "screening_topn_excluded"
			candidate.Notes = append(candidate.Notes,
				fmt.Sprintf("screeningLimit=%d", hcfg.DeepCandidateLimit),
				fmt.Sprintf("screeningRank=%d", index+1),
			)
			evaluated = append(evaluated, candidate)
		}
		screened = screened[:hcfg.DeepCandidateLimit]
	}

	for _, screenedCandidate := range screened {
		if groupCtx.Err() != nil {
			if errors.Is(groupCtx.Err(), context.Canceled) {
				return nil, nil, "context_canceled", groupCtx.Err()
			}
			groupReason = "group_evaluation_timeout"
			if r.logger != nil {
				r.logger.Printf("历史模拟评估超时 fundingTime=%s candidates=%d timeout=%s err=%v",
					group.FundingTime.Format(time.RFC3339), len(group.Items), groupTimeout, groupCtx.Err())
			}
			break
		}
		candidate := screenedCandidate
		candidateBuildTimeout := adaptiveMonthlyCandidateBuildTimeout(hcfg.CandidateBuildTimeout, len(group.Items))
		hydrateCtx, hydrateCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
		err := r.hydrateMonthlyCandidateFunding(hydrateCtx, client, cache, &candidate)
		hydrateCancel()
		if err != nil {
			candidate.Reason = monthlyBuildSkipReason("funding_rate_unavailable", err)
			candidate.Notes = append(candidate.Notes, err.Error())
			evaluated = append(evaluated, candidate)
			if candidate.Reason == "candidate_build_timeout" {
				logAgg.add("candidate_funding_timeout", candidate.Symbol)
			}
			continue
		}
		if reason, notes := r.validateMonthlyActualFunding(candidate); reason != "" {
			candidate.Reason = reason
			candidate.Notes = append(candidate.Notes, notes...)
			evaluated = append(evaluated, candidate)
			continue
		}
		pricingCtx, pricingCancel := context.WithTimeout(groupCtx, candidateBuildTimeout)
		err = r.completeMonthlyCandidateProxy(pricingCtx, client, cache, &candidate)
		pricingCancel()
		if err != nil {
			candidate.Reason = monthlyBuildSkipReason("reference_kline_unavailable", err)
			candidate.Notes = append(candidate.Notes, err.Error())
			evaluated = append(evaluated, candidate)
			if candidate.Reason == "candidate_build_timeout" {
				logAgg.add("candidate_pricing_timeout", candidate.Symbol)
			}
			continue
		}
		evaluated = append(evaluated, candidate)
	}

	sort.SliceStable(evaluated, func(i, j int) bool {
		if evaluated[i].EstimatedNetPnL == evaluated[j].EstimatedNetPnL {
			return evaluated[i].Symbol < evaluated[j].Symbol
		}
		return evaluated[i].EstimatedNetPnL > evaluated[j].EstimatedNetPnL
	})

	for index := range evaluated {
		if strings.TrimSpace(evaluated[index].Reason) != "" {
			continue
		}
		candidate := evaluated[index]
		finalizeTimeout := adaptiveMonthlyCandidateFinalizeTimeout(hcfg.CandidateFinalizeTimeout, len(group.Items))
		finalizeCtx, finalizeCancel := context.WithTimeout(groupCtx, finalizeTimeout)
		err := r.finalizeMonthlyCandidateExecution(finalizeCtx, client, cache, &candidate)
		finalizeCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				evaluated[index].Reason = "candidate_finalize_timeout"
				evaluated[index].Notes = append(evaluated[index].Notes,
					fmt.Sprintf("timeout=%s", finalizeTimeout),
					err.Error(),
				)
				logAgg.add("candidate_finalize_timeout", candidate.Symbol)
				continue
			}
			evaluated[index].Reason = "execution_kline_unavailable"
			evaluated[index].Notes = append(evaluated[index].Notes, err.Error())
			continue
		}
		evaluated[index] = candidate
		if strings.TrimSpace(evaluated[index].Reason) != "" {
			continue
		}
		if r.logger != nil {
			r.logMonthlyGroupAggregation(group, len(screeningItems), logAgg, groupReason)
		}
		return &evaluated[index], evaluated, groupReason, nil
	}
	if r.logger != nil {
		r.logMonthlyGroupAggregation(group, len(screeningItems), logAgg, groupReason)
	}
	return nil, evaluated, groupReason, nil
}

func (r *BacktestRunner) preselectMonthlyGroupItems(group monthlyEventGroup) ([]FundingRateHistoryItem, []monthlyCandidate) {
	limit := r.monthlyScreeningCandidateLimit(len(group.Items))
	if limit >= len(group.Items) {
		return group.Items, nil
	}
	selected := make([]FundingRateHistoryItem, 0, limit)
	excluded := make([]monthlyCandidate, 0, len(group.Items)-limit)
	for index, item := range group.Items {
		if index < limit {
			selected = append(selected, item)
			continue
		}
		candidateFundingTime := group.FundingTime
		if item.FundingTime > 0 {
			candidateFundingTime = time.UnixMilli(item.FundingTime).UTC()
		}
		excluded = append(excluded, monthlyCandidate{
			Symbol:         item.Symbol,
			FundingTime:    candidateFundingTime,
			FundingRate:    parseFloat(item.FundingRate),
			ScreeningScore: parseFloat(item.ScreeningScore),
			Reason:         "screening_prefilter_excluded",
			Notes: []string{
				fmt.Sprintf("prefilterLimit=%d", limit),
				fmt.Sprintf("prefilterRank=%d", index+1),
				fmt.Sprintf("screeningScore=%s", item.ScreeningScore),
				"screeningStage=entry_friction_liquidity_proxy",
			},
		})
	}
	return selected, excluded
}
