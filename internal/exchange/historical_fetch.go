package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lingxi/internal/core"
)

// 历史行情抓取（资金费率历史 / K 线）子系统：带本地负载缓存、限频与可重试，
// 供回测与历史校验消费。这些方法均挂在 BinanceClient 上，是「唯一摸网络的包」的一部分。

// LoadCachedFundingRateHistory 仅尝试从本地缓存读取资金费率历史，不触发网络请求。
func (c *BinanceClient) LoadCachedFundingRateHistory(symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, bool, error) {
	return c.loadCachedFundingRateHistory(symbol, startTimeMillis, endTimeMillis)
}

// FetchFundingRateHistory 抓取指定时间窗口内的资金费率历史（缓存优先 + 重试）。
func (c *BinanceClient) FetchFundingRateHistory(ctx context.Context, symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, error) {
	return c.fetchFundingRateHistory(ctx, symbol, startTimeMillis, endTimeMillis)
}

// FetchFundingRateAt 返回最接近指定结算时间的资金费率记录。
func (c *BinanceClient) FetchFundingRateAt(ctx context.Context, symbol string, fundingTime time.Time) (FundingRateHistoryItem, error) {
	return c.fetchFundingRateAt(ctx, symbol, fundingTime)
}

// FetchSpotKline / FetchFuturesKline 抓取起点处 1m K 线。
func (c *BinanceClient) FetchSpotKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchSpotKline(ctx, symbol, start)
}

func (c *BinanceClient) FetchFuturesKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchFuturesKline(ctx, symbol, start)
}

// FetchSpotKlineBefore / FetchFuturesKlineBefore 抓取起点前的参考 1m K 线。
func (c *BinanceClient) FetchSpotKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchSpotKlineBefore(ctx, symbol, start)
}

func (c *BinanceClient) FetchFuturesKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchFuturesKlineBefore(ctx, symbol, start)
}

// FetchFuturesPremiumKlineBefore / FetchFuturesIndexKlineBefore 抓取起点前的合约溢价/指数参考 K 线。
func (c *BinanceClient) FetchFuturesPremiumKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchFuturesPremiumKlineBefore(ctx, symbol, start)
}

func (c *BinanceClient) FetchFuturesIndexKlineBefore(ctx context.Context, pair string, start time.Time) (MarketKline, error) {
	return c.fetchFuturesIndexKlineBefore(ctx, pair, start)
}

func (c *BinanceClient) fundingRateHistoryEndpoint(symbol string, startTimeMillis int64, endTimeMillis int64) string {
	return fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&startTime=%d&endTime=%d&limit=1000",
		c.futuresBaseURL,
		url.QueryEscape(symbol),
		startTimeMillis,
		endTimeMillis,
	)
}

func (c *BinanceClient) loadCachedFundingRateHistory(symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, bool, error) {
	endpoint := c.fundingRateHistoryEndpoint(symbol, startTimeMillis, endTimeMillis)
	payload, ok, err := c.loadHistoricalPayload(endpoint)
	if err != nil || !ok {
		return nil, ok, err
	}
	var result []FundingRateHistoryItem
	if decodeErr := json.Unmarshal(payload, &result); decodeErr != nil {
		_ = c.invalidateHistoricalPayload(endpoint)
		return nil, false, nil
	}
	return result, true, nil
}

func (c *BinanceClient) fetchFundingRateHistory(ctx context.Context, symbol string, startTimeMillis int64, endTimeMillis int64) ([]FundingRateHistoryItem, error) {
	endpoint := c.fundingRateHistoryEndpoint(symbol, startTimeMillis, endTimeMillis)
	return c.fetchFundingRateHistoryWithRetry(ctx, endpoint)
}

func (c *BinanceClient) fetchFundingRateAt(ctx context.Context, symbol string, fundingTime time.Time) (FundingRateHistoryItem, error) {
	windowStart := fundingTime.Add(-24 * time.Hour).UnixMilli()
	windowEnd := fundingTime.Add(time.Minute).UnixMilli()
	items, err := c.fetchFundingRateHistory(ctx, symbol, windowStart, windowEnd)
	if err != nil {
		return FundingRateHistoryItem{}, err
	}
	for _, item := range items {
		if item.FundingTime == fundingTime.UnixMilli() {
			return item, nil
		}
	}
	for idx := len(items) - 1; idx >= 0; idx-- {
		if items[idx].FundingTime <= fundingTime.UnixMilli() {
			return items[idx], nil
		}
	}
	return FundingRateHistoryItem{}, fmt.Errorf("未找到 funding 结算记录")
}

func (c *BinanceClient) fetchSpotKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKline(ctx, c.spotBaseURL+"/api/v3/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesKline(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKline(ctx, c.futuresBaseURL+"/fapi/v1/klines", symbol, start)
}

func (c *BinanceClient) fetchSpotKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBefore(ctx, c.spotBaseURL+"/api/v3/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBefore(ctx, c.futuresBaseURL+"/fapi/v1/klines", symbol, start)
}

func (c *BinanceClient) fetchFuturesPremiumKlineBefore(ctx context.Context, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, c.futuresBaseURL+"/fapi/v1/premiumIndexKlines", "symbol", symbol, start, c.historicalEstimateLookback())
}

func (c *BinanceClient) fetchFuturesIndexKlineBefore(ctx context.Context, pair string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, c.futuresBaseURL+"/fapi/v1/indexPriceKlines", "pair", pair, start, c.historicalEstimateLookback())
}

func (c *BinanceClient) fetchKline(ctx context.Context, endpoint string, symbol string, start time.Time) (MarketKline, error) {
	queryURL := fmt.Sprintf("%s?symbol=%s&interval=1m&startTime=%d&limit=1", endpoint, url.QueryEscape(symbol), start.UnixMilli())
	var payload [][]any
	if err := c.getJSONWithMonthlyRetry(ctx, queryURL, &payload); err != nil {
		return MarketKline{}, err
	}
	if len(payload) == 0 {
		return MarketKline{}, fmt.Errorf("未返回 K 线")
	}
	return parseMarketKline(payload[0])
}

func (c *BinanceClient) fetchKlineBefore(ctx context.Context, endpoint string, symbol string, start time.Time) (MarketKline, error) {
	return c.fetchKlineBeforeWithLimit(ctx, endpoint, "symbol", symbol, start, c.historicalReferenceLookback())
}

func (c *BinanceClient) fetchKlineBeforeWithLimit(ctx context.Context, endpoint string, queryKey string, queryValue string, start time.Time, limit int) (MarketKline, error) {
	if limit <= 0 {
		limit = c.historicalReferenceLookback()
	}
	queryURL := fmt.Sprintf("%s?%s=%s&interval=1m&endTime=%d&limit=%d", endpoint, queryKey, url.QueryEscape(queryValue), start.UnixMilli()-1, limit)
	var payload [][]any
	if err := c.getJSONWithMonthlyRetry(ctx, queryURL, &payload); err != nil {
		return MarketKline{}, err
	}
	if len(payload) == 0 {
		return MarketKline{}, fmt.Errorf("未返回参考 K 线")
	}
	return parseMarketKline(payload[len(payload)-1])
}

func (c *BinanceClient) fetchFundingRateHistoryWithRetry(ctx context.Context, endpoint string) ([]FundingRateHistoryItem, error) {
	if payload, ok, err := c.loadHistoricalPayload(endpoint); err == nil && ok {
		var result []FundingRateHistoryItem
		if decodeErr := json.Unmarshal(payload, &result); decodeErr == nil {
			return result, nil
		}
		_ = c.invalidateHistoricalPayload(endpoint)
	}
	retryLimit := maxInt(0, c.historicalFundingRetryLimit())
	var lastPayload []byte
	lastStatusCode := 0
	var lastErr error
	for attempt := 0; attempt <= retryLimit; attempt++ {
		payload, statusCode, err := c.fetchHTTPPayload(ctx, endpoint)
		if err == nil && statusCode == http.StatusOK {
			var result []FundingRateHistoryItem
			if decodeErr := json.Unmarshal(payload, &result); decodeErr != nil {
				return nil, fmt.Errorf("解析 fundingRate 历史失败: %w", decodeErr)
			}
			_ = c.storeHistoricalPayload(endpoint, payload)
			return result, nil
		}
		lastPayload = payload
		lastStatusCode = statusCode
		if err != nil {
			lastErr = fmt.Errorf("获取 fundingRate 历史失败: 请求失败: %w", err)
		} else {
			lastErr = formatHistoricalHTTPStatusError("获取 fundingRate 历史失败", statusCode, payload)
		}
		if attempt >= retryLimit {
			break
		}
		if statusCode > 0 {
			if !isRetryableMonthlyFundingStatus(statusCode) {
				break
			}
		} else if !isRetryableMonthlyFundingError(err) {
			break
		}
		c.recordHistoricalRetry()
		if !sleepWithContext(ctx, monthlyFundingRetryDelay(attempt+1, statusCode)) {
			return nil, ctx.Err()
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if lastStatusCode != http.StatusOK {
		return nil, formatHistoricalHTTPStatusError("获取 fundingRate 历史失败", lastStatusCode, lastPayload)
	}
	return nil, fmt.Errorf("获取 fundingRate 历史失败: 未知错误")
}

func (c *BinanceClient) fetchHTTPPayload(ctx context.Context, rawURL string) ([]byte, int, error) {
	var lastErr error
	lastStatusCode := 0
	for _, candidateURL := range c.candidateRequestURLs(rawURL) {
		if err := c.waitPublicGETRequestSlot(ctx, candidateURL, c.historicalFundingFetchSlot); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidateURL, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("创建请求失败: %w", err)
		}
		c.applyPublicGETHeaders(req, candidateURL)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("读取响应失败: %w", readErr)
			lastStatusCode = resp.StatusCode
			continue
		}
		lastStatusCode = resp.StatusCode
		c.notePublicGETResponseStatus(resp.StatusCode)
		if resp.StatusCode == http.StatusOK {
			return body, resp.StatusCode, nil
		}
		lastErr = NewPublicGETHTTPError(candidateURL, resp.StatusCode, resp.Status, body)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知请求失败")
	}
	return nil, lastStatusCode, lastErr
}

func (c *BinanceClient) getJSONWithMonthlyRetry(ctx context.Context, rawURL string, target any) error {
	if payload, ok, err := c.loadHistoricalPayload(rawURL); err == nil && ok {
		if decodeErr := json.Unmarshal(payload, target); decodeErr == nil {
			return nil
		}
		_ = c.invalidateHistoricalPayload(rawURL)
	}
	payload, statusCode, err := c.fetchHTTPPayload(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	if statusCode != http.StatusOK {
		return formatHistoricalHTTPStatusError("HTTP 状态异常", statusCode, payload)
	}
	if decodeErr := json.Unmarshal(payload, target); decodeErr != nil {
		return fmt.Errorf("解析 JSON 失败: %w", decodeErr)
	}
	_ = c.storeHistoricalPayload(rawURL, payload)
	return nil
}

func formatHistoricalHTTPStatusError(prefix string, statusCode int, payload []byte) error {
	message := fmt.Sprintf("%s: %d %s", prefix, statusCode, http.StatusText(statusCode))
	snippet := strings.TrimSpace(string(payload))
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	if snippet != "" {
		message += " body=" + snippet
	}
	return fmt.Errorf("%s", message)
}

func (c *BinanceClient) historicalReferenceLookback() int {
	if c.historicalReferenceKlineLookback > 0 {
		return c.historicalReferenceKlineLookback
	}
	return core.DefaultEngineConfig().HistoricalConfig.ReferenceKlineLookback
}

func (c *BinanceClient) historicalEstimateLookback() int {
	if c.historicalEstimateKlineLookback > 0 {
		return c.historicalEstimateKlineLookback
	}
	return core.DefaultEngineConfig().HistoricalConfig.EstimateKlineLookback
}

func (c *BinanceClient) historicalFundingRetryLimit() int {
	if c.historicalFundingRetryMax > 0 {
		return c.historicalFundingRetryMax
	}
	return core.DefaultEngineConfig().HistoricalConfig.FundingRetryMax
}

func (c *BinanceClient) loadHistoricalPayload(rawURL string) ([]byte, bool, error) {
	if c == nil || c.historicalPayloadCache == nil {
		return nil, false, nil
	}
	payload, ok, err := c.historicalPayloadCache.Load(rawURL)
	if err == nil {
		c.recordHistoricalCacheLookup(ok)
	}
	return payload, ok, err
}

func (c *BinanceClient) storeHistoricalPayload(rawURL string, payload []byte) error {
	if c == nil || c.historicalPayloadCache == nil {
		return nil
	}
	return c.historicalPayloadCache.Save(rawURL, payload)
}

func (c *BinanceClient) invalidateHistoricalPayload(rawURL string) error {
	if c == nil || c.historicalPayloadCache == nil {
		return nil
	}
	return c.historicalPayloadCache.Remove(rawURL)
}

func isRetryableMonthlyFundingStatus(statusCode int) bool {
	return statusCode == http.StatusAccepted ||
		statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusGatewayTimeout
}

func isRetryableMonthlyFundingError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "tempor") ||
		strings.Contains(text, "reset by peer") ||
		strings.Contains(text, "eof")
}

func monthlyFundingRetryDelay(attempt int, statusCode int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 2 * time.Second
	switch statusCode {
	case http.StatusAccepted:
		base = 4 * time.Second
	case http.StatusTooManyRequests:
		base = 6 * time.Second
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		base = 3 * time.Second
	}
	delay := time.Duration(attempt) * base
	maxDelay := 20 * time.Second
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func parseMarketKline(row []any) (MarketKline, error) {
	if len(row) < 8 {
		return MarketKline{}, fmt.Errorf("K 线字段不足")
	}
	return MarketKline{
		OpenTime:         parseAnyInt64(row[0]),
		Open:             parseAnyFloat(row[1]),
		High:             parseAnyFloat(row[2]),
		Low:              parseAnyFloat(row[3]),
		Close:            parseAnyFloat(row[4]),
		QuoteAssetVolume: parseAnyFloat(row[7]),
	}, nil
}
