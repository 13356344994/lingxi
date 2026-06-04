package app

import (
	"context"
	"sort"
	"strings"
	"time"
)

// resident_market_feed.go 是 S2「持仓期常驻监控」的行情地基（M-S2.1）。
//
// 现状（已核对代码）：bookTicker/partial depth 行情流在 monitorTargets 内以 monitorCtx 启动、在开仓
// 预备时点(stopAt)即关闭，因此"开仓→持有→平仓"全程没有新鲜行情。ResidentMarketFeed 把行情流的生命
// 周期与单周期 monitorCtx 解耦：它常驻进程、跟随**托管敞口集合**(SSOT: activeOpenHedges + exposureLocks)，
// 在持有期持续订阅两腿 bookTicker，为以下提供地基：
//   - feedMarket 失明信号（连接级"距上一帧"，HoldMonitor 已纳入观测）；
//   - 持有期新鲜双腿盘口（S2.3 两腿名义复核的价格源）。
//
// M-S2.1 仍是**纯观测**：只多订阅一条随托管集合变动的行情流并写入 hold* 缓存，不改变任何交易决策。

const residentMarketFeedRefreshInterval = 2 * time.Second

// startResidentMarketFeed 启动持仓期常驻行情流（进程内只起一次，跟随 runCtx 生命周期）。
func (e *FundingRateSniperEngine) startResidentMarketFeed(ctx context.Context) {
	if e == nil {
		return
	}
	e.residentMarketFeedOnce.Do(func() {
		go e.runResidentMarketFeed(ctx)
	})
}

// runResidentMarketFeed 周期性比对托管集合；集合变化时取消旧订阅、按新集合重连两腿 bookTicker。
// 重连用去抖（仅在集合 key 变化时触发），避免频繁断连刷屏；空集合时不持有任何连接。
func (e *FundingRateSniperEngine) runResidentMarketFeed(ctx context.Context) {
	ticker := time.NewTicker(residentMarketFeedRefreshInterval)
	defer ticker.Stop()

	var (
		feedCancel context.CancelFunc
		currentKey string
	)
	defer func() {
		if feedCancel != nil {
			feedCancel()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		spotSymbols, futuresSymbols := e.heldMarketSubscriptionSymbols()
		key := residentFeedSubscriptionKey(spotSymbols, futuresSymbols)
		if key == currentKey {
			continue
		}

		// 集合变化：先取消旧订阅。
		if feedCancel != nil {
			feedCancel()
			feedCancel = nil
		}
		currentKey = key

		if len(spotSymbols) == 0 && len(futuresSymbols) == 0 {
			continue
		}

		feedCtx, cancel := context.WithCancel(ctx)
		feedCancel = cancel
		if len(spotSymbols) > 0 {
			subs := append(spotSymbols, spotTickerHeartbeatSymbol)
			go e.runBookTickerStream(feedCtx, binanceSpotCombinedWSURL, subs, e.holdSpotTickerCache)
		}
		if len(futuresSymbols) > 0 {
			subs := append(futuresSymbols, futuresTickerHeartbeatSymbol)
			go e.runBookTickerStream(feedCtx, binanceFuturesCombinedWSURL, subs, e.holdFuturesTickerCache)
		}
		if e.logger != nil {
			e.logger.Printf("持仓期常驻行情流订阅更新: spot=%v futures=%v", spotSymbols, futuresSymbols)
		}
	}
}

// heldMarketSubscriptionSymbols 返回当前托管敞口对应的现货对与合约 symbol（去重、排序，不含心跳符号）。
func (e *FundingRateSniperEngine) heldMarketSubscriptionSymbols() (spotSymbols []string, futuresSymbols []string) {
	targets := e.managedExposureTargets()
	if len(targets) == 0 {
		return nil, nil
	}
	spotSeen := make(map[string]struct{}, len(targets))
	futuresSeen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if pair := strings.ToUpper(strings.TrimSpace(target.Pair)); pair != "" {
			if _, ok := spotSeen[pair]; !ok {
				spotSeen[pair] = struct{}{}
				spotSymbols = append(spotSymbols, pair)
			}
		}
		if symbol := strings.ToUpper(strings.TrimSpace(target.Symbol)); symbol != "" {
			if _, ok := futuresSeen[symbol]; !ok {
				futuresSeen[symbol] = struct{}{}
				futuresSymbols = append(futuresSymbols, symbol)
			}
		}
	}
	sort.Strings(spotSymbols)
	sort.Strings(futuresSymbols)
	return spotSymbols, futuresSymbols
}

// residentFeedSubscriptionKey 是纯函数：把两腿订阅集合压成稳定 key，用于判定订阅是否需要重连。
func residentFeedSubscriptionKey(spotSymbols []string, futuresSymbols []string) string {
	if len(spotSymbols) == 0 && len(futuresSymbols) == 0 {
		return ""
	}
	spot := append([]string(nil), spotSymbols...)
	futures := append([]string(nil), futuresSymbols...)
	sort.Strings(spot)
	sort.Strings(futures)
	return "s:" + strings.Join(spot, ",") + "|f:" + strings.Join(futures, ",")
}

// bookTickerMid 返回盘口中间价；仅一侧有效时退回该侧，两侧均无效返回 0。
func bookTickerMid(ticker BookTicker) float64 {
	bid := parseFloat(ticker.BidPrice)
	ask := parseFloat(ticker.AskPrice)
	switch {
	case bid > 0 && ask > 0:
		return (bid + ask) / 2
	case bid > 0:
		return bid
	case ask > 0:
		return ask
	default:
		return 0
	}
}

// heldSpotMid 返回持仓期常驻行情流里某现货对的最新中间价（无数据返回 0,false）。
func (e *FundingRateSniperEngine) heldSpotMid(pair string) (float64, bool) {
	if e == nil || e.holdSpotTickerCache == nil {
		return 0, false
	}
	ticker, ok := e.holdSpotTickerCache.Get(strings.ToUpper(strings.TrimSpace(pair)))
	if !ok {
		return 0, false
	}
	mid := bookTickerMid(ticker)
	return mid, mid > 0
}

// heldFuturesMid 返回持仓期常驻行情流里某合约 symbol 的最新中间价（无数据返回 0,false）。
func (e *FundingRateSniperEngine) heldFuturesMid(symbol string) (float64, bool) {
	if e == nil || e.holdFuturesTickerCache == nil {
		return 0, false
	}
	ticker, ok := e.holdFuturesTickerCache.Get(strings.ToUpper(strings.TrimSpace(symbol)))
	if !ok {
		return 0, false
	}
	mid := bookTickerMid(ticker)
	return mid, mid > 0
}
