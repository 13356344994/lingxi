package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的本地 K 线/资金费率缓存
// （monthlyKlineCache：现货/合约/指数/溢价 K 线与 funding 序列的并发安全缓存与命中统计），
// 同包搬移、行为不变，让主文件收敛到「编排 + 模拟状态机」职责并向单文件 ≤ 800 行门禁靠拢。

type monthlyKlineCache struct {
	mu      sync.Mutex
	spot    map[string]MarketKline
	futures map[string]MarketKline
	index   map[string]MarketKline
	premium map[string]MarketKline
	funding map[string]FundingRateHistoryItem
	series  map[string][]FundingRateHistoryItem
	hits    map[string]int
	loads   map[string]int
}

func newMonthlyKlineCache() *monthlyKlineCache {
	return &monthlyKlineCache{
		spot:    make(map[string]MarketKline),
		futures: make(map[string]MarketKline),
		index:   make(map[string]MarketKline),
		premium: make(map[string]MarketKline),
		funding: make(map[string]FundingRateHistoryItem),
		series:  make(map[string][]FundingRateHistoryItem),
		hits:    make(map[string]int),
		loads:   make(map[string]int),
	}
}

func normalizeFundingHistoryItems(items []FundingRateHistoryItem) []FundingRateHistoryItem {
	if len(items) == 0 {
		return nil
	}
	cloned := append([]FundingRateHistoryItem(nil), items...)
	sort.Slice(cloned, func(i, j int) bool {
		if cloned[i].FundingTime == cloned[j].FundingTime {
			return cloned[i].Symbol < cloned[j].Symbol
		}
		return cloned[i].FundingTime < cloned[j].FundingTime
	})
	normalized := cloned[:0]
	for _, item := range cloned {
		item.Symbol = strings.ToUpper(strings.TrimSpace(item.Symbol))
		if len(normalized) > 0 && normalized[len(normalized)-1].FundingTime == item.FundingTime {
			normalized[len(normalized)-1] = item
			continue
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func mergeFundingHistoryItems(existing []FundingRateHistoryItem, incoming []FundingRateHistoryItem) []FundingRateHistoryItem {
	if len(existing) == 0 {
		return normalizeFundingHistoryItems(incoming)
	}
	if len(incoming) == 0 {
		return normalizeFundingHistoryItems(existing)
	}
	merged := append(append([]FundingRateHistoryItem(nil), existing...), incoming...)
	return normalizeFundingHistoryItems(merged)
}

func (c *monthlyKlineCache) storeFundingHistory(symbol string, items []FundingRateHistoryItem) {
	if c == nil || len(items) == 0 {
		return
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	normalized := normalizeFundingHistoryItems(items)
	c.mu.Lock()
	c.series[normalizedSymbol] = mergeFundingHistoryItems(c.series[normalizedSymbol], normalized)
	for _, item := range normalized {
		key := normalizedSymbol + ":" + strconv.FormatInt(item.FundingTime, 10)
		c.funding[key] = item
	}
	c.mu.Unlock()
}

func (c *monthlyKlineCache) fundingHistory(symbol string) []FundingRateHistoryItem {
	if c == nil {
		return nil
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.series[normalizedSymbol]
	if len(items) == 0 {
		return nil
	}
	return append([]FundingRateHistoryItem(nil), items...)
}

func (c *monthlyKlineCache) getFundingSchedule(ctx context.Context, client *BinanceClient, symbol string, fundingTime time.Time, cycles int) ([]FundingRateHistoryItem, error) {
	if cycles <= 0 {
		return nil, nil
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	series := c.fundingHistory(normalizedSymbol)
	if len(series) < cycles {
		lookahead := time.Duration(maxInt(24, cycles*24)) * time.Hour
		items, err := client.FetchFundingRateHistory(ctx, normalizedSymbol, fundingTime.Add(-24*time.Hour).UnixMilli(), fundingTime.Add(lookahead).UnixMilli())
		if err != nil && len(series) == 0 {
			return nil, err
		}
		if len(items) > 0 {
			c.storeFundingHistory(normalizedSymbol, items)
			series = c.fundingHistory(normalizedSymbol)
		}
	}
	if len(series) == 0 {
		return nil, fmt.Errorf("未找到 funding 结算序列")
	}
	targetMillis := fundingTime.UnixMilli()
	startIndex := sort.Search(len(series), func(i int) bool {
		return series[i].FundingTime >= targetMillis
	})
	if startIndex >= len(series) || series[startIndex].FundingTime != targetMillis {
		return nil, fmt.Errorf("未找到 fundingTime=%s 对应的结算记录", fundingTime.UTC().Format(time.RFC3339))
	}
	if len(series)-startIndex < cycles {
		available := len(series) - startIndex
		if available <= 0 {
			return nil, fmt.Errorf("funding 结算序列不足: symbol=%s requested=%d available=%d", normalizedSymbol, cycles, available)
		}
		return append([]FundingRateHistoryItem(nil), series[startIndex:]...), nil
	}
	return append([]FundingRateHistoryItem(nil), series[startIndex:startIndex+cycles]...), nil
}

func (c *monthlyKlineCache) fundingRateBefore(symbol string, fundingTime time.Time) (FundingRateHistoryItem, bool) {
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	series := c.fundingHistory(normalizedSymbol)
	if len(series) == 0 {
		return FundingRateHistoryItem{}, false
	}
	targetMillis := fundingTime.UnixMilli()
	index := sort.Search(len(series), func(i int) bool {
		return series[i].FundingTime >= targetMillis
	})
	if index <= 0 {
		return FundingRateHistoryItem{}, false
	}
	return series[index-1], true
}

func (c *monthlyKlineCache) getSpotKline(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.spot[key]; ok {
		c.hits["spot_exec"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["spot_exec"]++
	c.mu.Unlock()

	kline, err := client.FetchSpotKline(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.spot[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesKline(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.futures[key]; ok {
		c.hits["futures_exec"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["futures_exec"]++
	c.mu.Unlock()

	kline, err := client.FetchFuturesKline(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.futures[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getSpotKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.spot[key]; ok {
		c.hits["spot_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["spot_before"]++
	c.mu.Unlock()

	kline, err := client.FetchSpotKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.spot[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.futures[key]; ok {
		c.hits["futures_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["futures_before"]++
	c.mu.Unlock()

	kline, err := client.FetchFuturesKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.futures[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesPremiumKlineBefore(ctx context.Context, client *BinanceClient, symbol string, start time.Time) (MarketKline, error) {
	key := "before:premium:" + symbol + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.premium[key]; ok {
		c.hits["premium_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["premium_before"]++
	c.mu.Unlock()

	kline, err := client.FetchFuturesPremiumKlineBefore(ctx, symbol, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.premium[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFuturesIndexKlineBefore(ctx context.Context, client *BinanceClient, pair string, start time.Time) (MarketKline, error) {
	key := "before:index:" + pair + ":" + strconv.FormatInt(start.UnixMilli(), 10)
	c.mu.Lock()
	if kline, ok := c.index[key]; ok {
		c.hits["index_before"]++
		c.mu.Unlock()
		return kline, nil
	}
	c.loads["index_before"]++
	c.mu.Unlock()

	kline, err := client.FetchFuturesIndexKlineBefore(ctx, pair, start)
	if err != nil {
		return MarketKline{}, err
	}

	c.mu.Lock()
	c.index[key] = kline
	c.mu.Unlock()
	return kline, nil
}

func (c *monthlyKlineCache) getFundingRateAt(ctx context.Context, client *BinanceClient, symbol string, fundingTime time.Time) (FundingRateHistoryItem, error) {
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	key := normalizedSymbol + ":" + strconv.FormatInt(fundingTime.UnixMilli(), 10)
	c.mu.Lock()
	if item, ok := c.funding[key]; ok {
		c.hits["funding_actual"]++
		c.mu.Unlock()
		return item, nil
	}
	c.loads["funding_actual"]++
	c.mu.Unlock()
	if series := c.fundingHistory(normalizedSymbol); len(series) > 0 {
		targetMillis := fundingTime.UnixMilli()
		index := sort.Search(len(series), func(i int) bool {
			return series[i].FundingTime >= targetMillis
		})
		if index < len(series) && series[index].FundingTime == targetMillis {
			c.mu.Lock()
			c.funding[key] = series[index]
			c.hits["funding_actual"]++
			c.mu.Unlock()
			return series[index], nil
		}
		if index > 0 {
			c.mu.Lock()
			c.funding[key] = series[index-1]
			c.hits["funding_actual"]++
			c.mu.Unlock()
			return series[index-1], nil
		}
	}

	item, err := client.FetchFundingRateAt(ctx, normalizedSymbol, fundingTime)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}

	c.mu.Lock()
	c.funding[key] = item
	c.mu.Unlock()
	return item, nil
}

func (c *monthlyKlineCache) summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := []string{"premium_before", "index_before", "spot_before", "futures_before", "spot_exec", "futures_exec", "funding_actual"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		loads := c.loads[key]
		hits := c.hits[key]
		if loads == 0 && hits == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(loads=%d,hits=%d)", key, loads, hits))
	}
	return strings.Join(parts, ";")
}
