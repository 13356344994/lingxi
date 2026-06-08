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

// 本文件从 historical_monthly_backtest.go 拆出：仅承载月度模拟的「时间轴分组 + 稀疏组合并 +
// premiumIndex 粗筛」逻辑（同包搬移，行为不变），让主文件收敛到「类型 + 编排」职责并向单文件
// ≤ 800 行门禁靠拢。

func (r *BacktestRunner) buildMonthlyTimelineGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbols []string, start time.Time, end time.Time) ([]monthlyEventGroup, monthlyFundingFetchStats, error) {
	hcfg := r.historicalConfig()
	groups, stats, err := r.fetchMonthlyFundingGroups(ctx, client, cache, symbols, start, end)
	if err != nil {
		return nil, stats, err
	}
	groups = mergeSparseMonthlyEventGroups(groups, monthlyFundingSparseGroupMinItems, monthlyFundingGroupMergeWindow, hcfg.FundingDirectionMode)
	totalObservations := 0
	for _, group := range groups {
		totalObservations += len(group.Items)
	}
	if totalObservations == 0 {
		return nil, stats, fmt.Errorf("指定区间内没有可评估的 funding 结算记录")
	}
	processedObservations := 0
	preselectConcurrency := maxInt(1, minInt(hcfg.FundingFetchConcurrency, maxInt(1, hcfg.ScreeningConcurrency)))
	enrichedGroups := make([]monthlyEventGroup, 0, len(groups))

	for index, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		fundingTime := group.FundingTime
		entryTime := fundingTime.Add(-hcfg.FundingPhaseLead)
		type result struct {
			symbol string
			item   FundingRateHistoryItem
			err    error
		}
		results := make(chan result, len(group.Items))
		sem := make(chan struct{}, preselectConcurrency)
		var wg sync.WaitGroup

		for _, actualItem := range group.Items {
			actualItem := actualItem
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					results <- result{symbol: actualItem.Symbol, err: ctx.Err()}
					return
				}
				defer func() { <-sem }()

				symbolCtx, cancel := context.WithTimeout(ctx, hcfg.CandidateBuildTimeout)
				defer cancel()

				item, buildErr := r.buildMonthlyTimelinePreselectItem(symbolCtx, client, cache, actualItem.Symbol, fundingTime, entryTime)
				if buildErr == nil {
					item.FundingRate = actualItem.FundingRate
					item.FundingTime = actualItem.FundingTime
				}
				results <- result{symbol: actualItem.Symbol, item: item, err: buildErr}
			}()
		}
		go func() {
			wg.Wait()
			close(results)
		}()

		items := make([]FundingRateHistoryItem, 0, len(group.Items))
		for result := range results {
			processedObservations++
			if result.err != nil {
				stats.FailureCount++
				reason := classifyMonthlyFetchError(result.err)
				stats.FailureReasons[reason]++
				if len(stats.FailureSamples[reason]) < 5 {
					stats.FailureSamples[reason] = append(stats.FailureSamples[reason], result.symbol)
				}
			} else {
				items = append(items, result.item)
			}
			if r.logger != nil && (processedObservations == totalObservations || processedObservations%monthlyFundingFetchProgressStep == 0) {
				r.logger.Printf("月度模拟 funding 结算预选进度：%d/%d，symbolFetchSuccess=%d，失败=%d", processedObservations, totalObservations, stats.SuccessSymbols, stats.FailureCount)
			}
			r.reportProgress(EngineStage("fetch_funding"),
				fmt.Sprintf("正在按真实 funding 结算构建历史候选: %d/%d，symbolFetchSuccess=%d，失败=%d", processedObservations, totalObservations, stats.SuccessSymbols, stats.FailureCount),
				processedObservations, totalObservations)
		}

		sort.SliceStable(items, func(i, j int) bool {
			left := monthlyPreselectPriorityScore(items[i], hcfg.FundingDirectionMode)
			right := monthlyPreselectPriorityScore(items[j], hcfg.FundingDirectionMode)
			if left == right {
				return items[i].Symbol < items[j].Symbol
			}
			return left > right
		})
		rawCount := len(items)
		items = trimMonthlyFundingGroupItems(items, hcfg.FundingGroupTopSymbolsPerTime)
		if len(items) > 0 {
			enrichedGroups = append(enrichedGroups, monthlyEventGroup{
				FundingTime: fundingTime,
				RawCount:    rawCount,
				Items:       items,
			})
		}
		if r.logger != nil {
			r.logger.Printf("月度模拟真实 funding 结算预选完成：index=%d/%d fundingTime=%s raw=%d retained=%d",
				index+1, len(groups), fundingTime.Format(time.RFC3339), rawCount, len(items))
		}
	}

	if len(enrichedGroups) == 0 {
		return nil, stats, fmt.Errorf("所有 funding 结算时点都未构建出候选")
	}
	if r.logger != nil {
		r.logger.Printf("月度模拟真实 funding 结算预选汇总：时点=%d，symbolFetchSuccess=%d，失败=%d，失败分类=%s", len(enrichedGroups), stats.SuccessSymbols, stats.FailureCount, summarizeMonthlyReasonCounts(stats.FailureReasons, stats.FailureSamples))
	}
	return enrichedGroups, stats, nil
}

func mergeSparseMonthlyEventGroups(groups []monthlyEventGroup, minItems int, maxWindow time.Duration, fundingDirectionMode string) []monthlyEventGroup {
	if len(groups) <= 1 || minItems <= 1 {
		return groups
	}
	merged := make([]monthlyEventGroup, 0, len(groups))
	pendingItems := make([]FundingRateHistoryItem, 0, minItems)
	pendingRawCount := 0
	var pendingStart time.Time
	var pendingEnd time.Time
	resetPending := func() {
		pendingItems = pendingItems[:0]
		pendingRawCount = 0
		pendingStart = time.Time{}
		pendingEnd = time.Time{}
	}
	flushPending := func(anchor time.Time) {
		if pendingRawCount == 0 {
			return
		}
		// 合并阶段同样早于点位预估，只做确定性的字典序排序、不按已实现费率截断；
		// 真正的 top-N 择优由下游点位预估预选完成。
		items := append([]FundingRateHistoryItem(nil), pendingItems...)
		sortFundingItemsBySymbol(items)
		merged = append(merged, monthlyEventGroup{
			FundingTime: anchor,
			RawCount:    pendingRawCount,
			Items:       items,
		})
		resetPending()
	}
	appendPending := func(group monthlyEventGroup) {
		if pendingRawCount == 0 {
			pendingStart = group.FundingTime
		}
		pendingEnd = group.FundingTime
		pendingRawCount += group.RawCount
		pendingItems = append(pendingItems, group.Items...)
	}

	for _, group := range groups {
		if group.RawCount >= minItems {
			if pendingRawCount > 0 {
				if maxWindow <= 0 || group.FundingTime.Sub(pendingStart) <= maxWindow {
					items := append([]FundingRateHistoryItem(nil), pendingItems...)
					items = append(items, group.Items...)
					sortFundingItemsBySymbol(items)
					merged = append(merged, monthlyEventGroup{
						FundingTime: group.FundingTime,
						RawCount:    pendingRawCount + group.RawCount,
						Items:       items,
					})
					resetPending()
					continue
				}
				flushPending(pendingEnd)
			}
			merged = append(merged, group)
			continue
		}
		if pendingRawCount > 0 && maxWindow > 0 && group.FundingTime.Sub(pendingStart) > maxWindow {
			flushPending(pendingEnd)
		}
		appendPending(group)
	}
	if pendingRawCount > 0 {
		flushPending(pendingEnd)
	}
	return merged
}

type monthlyCoarseSymbol struct {
	Symbol               string
	EstimatedFundingRate float64
	FundingRateJump      float64
	BasisRate            float64
	ScanScoreRate        float64
	PriorityScore        float64
}

func (r *BacktestRunner) coarseScreenMonthlySymbols(ctx context.Context, client *BinanceClient, symbols []string, snapshotTime time.Time) ([]string, error) {
	limit := r.monthlyCoarseSymbolLimit(len(symbols))
	if limit <= 0 || limit >= len(symbols) {
		return append([]string(nil), symbols...), nil
	}
	items, err := client.FetchPremiumIndex(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 拉取失败，回退到全量 symbol，err=%v", err)
		}
		return append([]string(nil), symbols...), nil
	}
	if len(items) == 0 {
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 返回为空，回退到全量 symbol")
		}
		return append([]string(nil), symbols...), nil
	}
	hcfg := r.historicalConfig()
	symbolSet := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if normalized == "" {
			continue
		}
		symbolSet[normalized] = struct{}{}
	}
	candidates := make([]monthlyCoarseSymbol, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	staleSkipped := 0
	directionSkipped := 0
	jumpSkipped := 0
	for _, item := range items {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if _, ok := symbolSet[symbol]; !ok {
			continue
		}
		seen[symbol] = struct{}{}
		if r.cfg.NetworkConfig.MaxPremiumIndexStaleness > 0 {
			staleness := premiumIndexStaleness(snapshotTime, item)
			if staleness > r.cfg.NetworkConfig.MaxPremiumIndexStaleness {
				staleSkipped++
				continue
			}
		}
		// 回测恒传 false:R7 保守开关仅作用于实盘开仓,回测保持历史可复现。
		estimatedFundingRate, _, fundingJump, basisRate, scanScoreRate := estimateScanFundingSignal(item, fundingSignalConfig{})
		priorityScore := historicalFundingPriorityScore(FundingRateHistoryItem{
			Symbol:               symbol,
			EstimatedFundingRate: formatFloat(estimatedFundingRate),
		}, hcfg.FundingDirectionMode)
		if priorityScore <= 0 {
			directionSkipped++
			continue
		}
		if hcfg.MaxFundingRateJump > 0 && fundingJump >= hcfg.MaxFundingRateJump {
			jumpSkipped++
			continue
		}
		candidates = append(candidates, monthlyCoarseSymbol{
			Symbol:               symbol,
			EstimatedFundingRate: estimatedFundingRate,
			FundingRateJump:      fundingJump,
			BasisRate:            basisRate,
			ScanScoreRate:        scanScoreRate,
			PriorityScore:        priorityScore,
		})
	}
	if len(candidates) == 0 {
		if r.logger != nil {
			r.logger.Printf("月度模拟 coarse 粗筛降级：premiumIndex 没筛出候选，回退到全量 symbol，matched=%d stale=%d direction=%d jump=%d",
				len(seen), staleSkipped, directionSkipped, jumpSkipped)
		}
		return append([]string(nil), symbols...), nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].PriorityScore == candidates[j].PriorityScore {
			if candidates[i].ScanScoreRate == candidates[j].ScanScoreRate {
				if candidates[i].FundingRateJump == candidates[j].FundingRateJump {
					if math.Abs(candidates[i].BasisRate) == math.Abs(candidates[j].BasisRate) {
						return candidates[i].Symbol < candidates[j].Symbol
					}
					return math.Abs(candidates[i].BasisRate) < math.Abs(candidates[j].BasisRate)
				}
				return candidates[i].FundingRateJump < candidates[j].FundingRateJump
			}
			return candidates[i].ScanScoreRate > candidates[j].ScanScoreRate
		}
		return candidates[i].PriorityScore > candidates[j].PriorityScore
	})
	retained := minInt(limit, len(candidates))
	selected := make([]string, 0, limit)
	selectedSet := make(map[string]struct{}, limit)
	for index := 0; index < retained; index++ {
		selected = append(selected, candidates[index].Symbol)
		selectedSet[candidates[index].Symbol] = struct{}{}
	}
	for _, symbol := range symbols {
		if len(selected) >= limit {
			break
		}
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if _, ok := selectedSet[normalized]; ok {
			continue
		}
		selected = append(selected, normalized)
		selectedSet[normalized] = struct{}{}
	}
	if r.logger != nil {
		topSymbol := ""
		topScore := 0.0
		if len(candidates) > 0 {
			topSymbol = candidates[0].Symbol
			topScore = candidates[0].PriorityScore
		}
		r.logger.Printf("月度模拟 coarse 粗筛完成：golden=%d matched=%d retained=%d/%d fallbackFill=%d stale=%d direction=%d jump=%d top=%s score=%.6f",
			len(symbols), len(seen), len(selected), limit, maxInt(len(selected)-retained, 0), staleSkipped, directionSkipped, jumpSkipped, topSymbol, topScore)
	}
	r.reportProgress(EngineStage("coarse_screen"),
		fmt.Sprintf("正在按 premiumIndex 粗筛历史币池: %d/%d，保留=%d", len(symbols), len(symbols), len(selected)),
		len(symbols), len(symbols))
	return selected, nil
}

func monthlyFundingTimeline(start time.Time, end time.Time) []time.Time {
	if !end.After(start) {
		return nil
	}
	cursor := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	for cursor.Before(start.UTC()) {
		cursor = cursor.Add(8 * time.Hour)
	}
	timeline := make([]time.Time, 0, int(end.Sub(start)/(8*time.Hour))+2)
	for !cursor.After(end.UTC()) {
		timeline = append(timeline, cursor)
		cursor = cursor.Add(8 * time.Hour)
	}
	return timeline
}
