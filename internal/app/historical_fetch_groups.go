package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载月度模拟的「funding 历史批量拉取/缓存预热 +
// 时间轴点位预选项构造 + 实验资金对齐」逻辑（同包搬移，行为不变），向单文件 ≤ 800 行门禁靠拢。

func (r *BacktestRunner) buildMonthlyTimelinePreselectItem(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, fundingTime time.Time, entryTime time.Time) (FundingRateHistoryItem, error) {
	premiumEntryRef, err := cache.getFuturesPremiumKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}
	indexEntryRef, err := cache.getFuturesIndexKlineBefore(ctx, client, symbol, entryTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}

	preFundingBasisRate := 0.0
	estimatedMarkPrice := indexEntryRef.Close
	if premiumEntryRef.Close > -1 && indexEntryRef.Close > 0 {
		estimatedMarkPrice = indexEntryRef.Close * (1 + premiumEntryRef.Close)
	}
	if indexEntryRef.Close > 0 && estimatedMarkPrice > 0 {
		preFundingBasisRate = (estimatedMarkPrice - indexEntryRef.Close) / indexEntryRef.Close
	}
	estimatedFundingRate := conservativeHistoricalFundingRate(premiumEntryRef.Close, preFundingBasisRate)
	if previousFunding, ok := cache.fundingRateBefore(symbol, fundingTime); ok {
		estimatedFundingRate = alignHistoricalEstimatedFundingRate(estimatedFundingRate, parseFloat(previousFunding.FundingRate))
	}
	screeningScore := r.estimateMonthlyPreselectScore(ctx, client, cache, symbol, fundingTime, entryTime, estimatedFundingRate, estimatedMarkPrice)

	return FundingRateHistoryItem{
		Symbol:               symbol,
		FundingTime:          fundingTime.UnixMilli(),
		MarkPrice:            formatFloat(estimatedMarkPrice),
		EstimatedFundingRate: formatFloat(estimatedFundingRate),
		ScreeningScore:       formatFloat(screeningScore),
	}, nil
}

func alignHistoricalExperimentCapital(cfg EngineConfig) EngineConfig {
	perLeg := cfg.HistoricalConfig.PerLegNotionalUSDT
	if perLeg <= 0 || cfg.HistoricalConfig.InitialEquityUSDT <= 0 {
		return cfg
	}
	requiredShare := perLeg / cfg.HistoricalConfig.InitialEquityUSDT
	if requiredShare > 1 {
		requiredShare = 1
	}
	if requiredShare > cfg.HistoricalConfig.MaxPerLegEquityShare {
		cfg.HistoricalConfig.MaxPerLegEquityShare = requiredShare
	}
	return cfg
}

func (r *BacktestRunner) fetchMonthlyFundingGroups(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbols []string, start time.Time, end time.Time) ([]monthlyEventGroup, monthlyFundingFetchStats, error) {
	hcfg := r.historicalConfig()
	startMillis := start.UnixMilli()
	endMillis := end.UnixMilli()

	type result struct {
		symbol string
		items  []FundingRateHistoryItem
		err    error
	}

	grouped := make(map[int64][]FundingRateHistoryItem)
	stats := monthlyFundingFetchStats{
		FailureReasons: make(map[string]int),
		FailureSamples: make(map[string][]string),
	}
	successCount := 0
	processedCount := 0
	consecutive202Batches := 0
	consecutive429Batches := 0
	consecutive403Batches := 0
	consecutive418Batches := 0

	batchSize := monthlyFundingFetchBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	batchConcurrency := maxInt(1, hcfg.FundingFetchConcurrency)
	missingSymbols := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		items, ok, err := client.LoadCachedFundingRateHistory(symbol, startMillis, endMillis)
		processedCount++
		switch {
		case err != nil:
			stats.FailureCount++
			reason := classifyMonthlyFetchError(err)
			stats.FailureReasons[reason]++
			if len(stats.FailureSamples[reason]) < 5 {
				stats.FailureSamples[reason] = append(stats.FailureSamples[reason], symbol)
			}
			missingSymbols = append(missingSymbols, symbol)
		case ok:
			successCount++
			stats.SuccessSymbols++
			if cache != nil {
				cache.storeFundingHistory(symbol, items)
			}
			for _, item := range items {
				grouped[item.FundingTime] = append(grouped[item.FundingTime], item)
			}
		default:
			missingSymbols = append(missingSymbols, symbol)
		}
		if r.logger != nil && (processedCount == len(symbols) || processedCount%monthlyFundingFetchProgressStep == 0) {
			r.logger.Printf("月度模拟 funding 缓存预热进度：%d/%d，成功=%d，待补抓=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, len(missingSymbols), stats.FailureCount)
		}
		r.reportProgress(EngineStage("fetch_funding"),
			fmt.Sprintf("正在加载 funding 历史缓存: %d/%d，成功=%d，待补抓=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, len(missingSymbols), stats.FailureCount),
			processedCount, len(symbols))
	}

	processedCount = successCount + stats.FailureCount
	for batchStart := 0; batchStart < len(missingSymbols); batchStart += batchSize {
		batchEnd := minInt(len(missingSymbols), batchStart+batchSize)
		batchSymbols := missingSymbols[batchStart:batchEnd]
		results := make(chan result, len(batchSymbols))
		sem := make(chan struct{}, batchConcurrency)
		batchFailures := make(map[string]int)
		var wg sync.WaitGroup

		for _, symbol := range batchSymbols {
			wg.Add(1)
			go func(sym string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				symbolCtx, cancel := context.WithTimeout(ctx, hcfg.FundingFetchTimeout)
				defer cancel()

				items, err := client.FetchFundingRateHistory(symbolCtx, sym, startMillis, endMillis)
				results <- result{symbol: sym, items: items, err: err}
			}(symbol)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		for result := range results {
			processedCount++
			if result.err != nil {
				stats.FailureCount++
				reason := classifyMonthlyFetchError(result.err)
				stats.FailureReasons[reason]++
				batchFailures[reason]++
				if len(stats.FailureSamples[reason]) < 5 {
					stats.FailureSamples[reason] = append(stats.FailureSamples[reason], result.symbol)
				}
			} else {
				successCount++
				stats.SuccessSymbols++
				if cache != nil {
					cache.storeFundingHistory(result.symbol, result.items)
				}
				for _, item := range result.items {
					grouped[item.FundingTime] = append(grouped[item.FundingTime], item)
				}
			}
			if r.logger != nil && (processedCount == len(symbols) || processedCount%monthlyFundingFetchProgressStep == 0) {
				r.logger.Printf("月度模拟 funding 拉取进度：%d/%d，成功=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, stats.FailureCount)
			}
			r.reportProgress(EngineStage("fetch_funding"),
				fmt.Sprintf("正在拉取 funding 历史: %d/%d，成功=%d，失败=%d", processedCount, len(symbols), stats.SuccessSymbols, stats.FailureCount),
				processedCount, len(symbols))
		}
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		if shouldAbortMonthlyFundingFetch(batchFailures, len(batchSymbols), &consecutive202Batches, &consecutive429Batches, &consecutive403Batches, &consecutive418Batches) {
			reasons := summarizeMonthlyReasonCounts(batchFailures, nil)
			if r.logger != nil {
				r.logger.Printf("月度模拟 funding 提前中止：batch=%d-%d reasons=%s", batchStart+1, batchEnd, reasons)
			}
			return nil, stats, fmt.Errorf("连续批次遭遇访问拒绝，已提前中止本轮 funding 拉取: %s", reasons)
		}
		nextBatchConcurrency := adaptiveMonthlyFundingFetchConcurrency(hcfg.FundingFetchConcurrency, batchConcurrency, len(batchSymbols), batchFailures)
		if r.logger != nil && nextBatchConcurrency != batchConcurrency {
			r.logger.Printf("月度模拟 funding 并发调整：batch=%d-%d current=%d next=%d reasons=%s",
				batchStart+1, batchEnd, batchConcurrency, nextBatchConcurrency, summarizeMonthlyReasonCounts(batchFailures, nil))
		}
		batchConcurrency = nextBatchConcurrency
		if batchEnd < len(symbols) && ctx.Err() == nil {
			pause := adaptiveMonthlyFundingBatchPause(batchFailures, len(batchSymbols))
			if pause > 0 && r.logger != nil {
				r.logger.Printf("月度模拟 funding 批次暂停：batch=%d-%d pause=%s next_concurrency=%d reasons=%s",
					batchStart+1, batchEnd, pause, batchConcurrency, summarizeMonthlyReasonCounts(batchFailures, nil))
			}
			if pause > 0 && !sleepWithContext(ctx, pause) {
				return nil, stats, ctx.Err()
			}
		}
	}

	if successCount == 0 {
		return nil, stats, fmt.Errorf("所有 symbol 的 fundingRate 拉取都失败了")
	}
	stats.RequestSummary = client.HistoricalRequestSummary()
	if r.logger != nil {
		r.logger.Printf("月度模拟 fundingRate 拉取汇总：成功=%d 失败=%d 失败分类=%s", stats.SuccessSymbols, stats.FailureCount, summarizeMonthlyReasonCounts(stats.FailureReasons, stats.FailureSamples))
		if strings.TrimSpace(stats.RequestSummary) != "" {
			r.logger.Printf("月度模拟历史请求摘要：%s", stats.RequestSummary)
		}
		successRate := stats.successRate(len(symbols))
		if successRate < monthlyFundingDataCompleteFloor {
			r.logger.Printf("月度模拟 funding 数据完整性不足：successRate=%.4f threshold=%.2f", successRate, monthlyFundingDataCompleteFloor)
		}
	}

	times := make([]int64, 0, len(grouped))
	for fundingTime := range grouped {
		times = append(times, fundingTime)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	// 注意：此处的 items 只有已实现的 FundingRate，没有点位预估信号。
	// 因此绝不能按已实现费率排序/截断来选币（那是前视偏差）。这里只做确定性的字典序排序，
	// 并仅按 coarse 宇宙上限兜底截断；真正按「点位预估」择优 top-N 的工作交给下游 buildMonthlyTimelineGroups 的预选阶段。
	universeCap := r.monthlyPreEstimateUniverseCap()
	groups := make([]monthlyEventGroup, 0, len(times))
	for _, fundingTime := range times {
		items := grouped[fundingTime]
		sortFundingItemsBySymbol(items)
		rawCount := len(items)
		items = trimMonthlyFundingGroupItems(items, universeCap)
		groups = append(groups, monthlyEventGroup{
			FundingTime: time.UnixMilli(fundingTime).UTC(),
			RawCount:    rawCount,
			Items:       items,
		})
	}
	return groups, stats, nil
}
