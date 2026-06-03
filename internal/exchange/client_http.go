package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"lingxi/internal/core"
)

// 本文件从 client.go 拆出：仅承载 BinanceClient 的底层 HTTP 取数、UsedWeight 权重窗口预算管理、
// 候选 URL/代理回退、公共 GET 限频与冷却，以及历史请求统计等网络底盘（同包搬移、行为不变），
// 让 client.go 收敛到「类型 + 构造 + 公共门面」职责并满足单文件 ≤ 800 行门禁。

func (c *BinanceClient) getJSON(ctx context.Context, url string, target any) error {
	var lastErr error
	for _, candidateURL := range c.candidateRequestURLs(url) {
		if err := c.waitPublicGETRequestSlot(ctx, candidateURL, 0); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidateURL, nil)
		if err != nil {
			return fmt.Errorf("创建请求失败: %w", err)
		}
		c.applyPublicGETHeaders(req, candidateURL)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求失败: %w", err)
			continue
		}

		func() {
			defer resp.Body.Close()
			c.noteUsedWeightFromHeaders(resp.Header)
			c.notePublicGETResponseStatus(resp.StatusCode)
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				lastErr = NewPublicGETHTTPError(candidateURL, resp.StatusCode, resp.Status, body)
				return
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(target); decodeErr != nil {
				lastErr = fmt.Errorf("解析 JSON 失败: %w", decodeErr)
				return
			}
			lastErr = nil
		}()
		if lastErr == nil {
			return nil
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("未知请求失败")
	}
	return lastErr
}

func (c *BinanceClient) noteUsedWeightFromHeaders(header http.Header) {
	if c == nil || header == nil {
		return
	}
	usedWeight := ParseHeaderInt(header.Get("X-MBX-USED-WEIGHT-1M"))
	if usedWeight < 0 {
		return
	}
	c.usedWeightMu.Lock()
	c.resetUsedWeightWindowLocked(time.Now().UTC())
	c.usedWeight1m = usedWeight
	c.usedWeightReserved1m = 0
	if c.usedWeightLimit1m <= 0 {
		c.usedWeightLimit1m = defaultUsedWeightLimit1m
	}
	if c.usedWeightBlockThreshold <= 0 {
		c.usedWeightBlockThreshold = int(float64(c.usedWeightLimit1m) * 0.8)
	}
	c.usedWeightMu.Unlock()
}

func (c *BinanceClient) usedWeightStatus() (int, int) {
	if c == nil {
		return 0, 0
	}
	c.usedWeightMu.Lock()
	defer c.usedWeightMu.Unlock()
	c.resetUsedWeightWindowLocked(time.Now().UTC())
	if c.usedWeightLimit1m <= 0 {
		c.usedWeightLimit1m = defaultUsedWeightLimit1m
	}
	if c.usedWeightBlockThreshold <= 0 {
		c.usedWeightBlockThreshold = int(float64(c.usedWeightLimit1m) * 0.8)
	}
	return c.usedWeight1m, c.usedWeightBlockThreshold
}

func (c *BinanceClient) resetUsedWeightWindowLocked(now time.Time) {
	if c == nil {
		return
	}
	currentMinute := now.UTC().Truncate(time.Minute)
	if c.usedWeightWindowMinuteUTC.IsZero() {
		c.usedWeightWindowMinuteUTC = currentMinute
		return
	}
	if currentMinute.After(c.usedWeightWindowMinuteUTC) {
		c.usedWeightWindowMinuteUTC = currentMinute
		c.usedWeight1m = 0
		c.usedWeightReserved1m = 0
	}
}

func (c *BinanceClient) reserveUsedWeightBudget(units int) (int, int, bool) {
	if c == nil {
		return 0, 0, true
	}
	if units <= 0 {
		units = 1
	}
	c.usedWeightMu.Lock()
	defer c.usedWeightMu.Unlock()
	c.resetUsedWeightWindowLocked(time.Now().UTC())
	if c.usedWeightLimit1m <= 0 {
		c.usedWeightLimit1m = defaultUsedWeightLimit1m
	}
	if c.usedWeightBlockThreshold <= 0 {
		c.usedWeightBlockThreshold = int(float64(c.usedWeightLimit1m) * 0.8)
	}
	projected := c.usedWeight1m + c.usedWeightReserved1m + units
	if c.usedWeightBlockThreshold > 0 && projected >= c.usedWeightBlockThreshold {
		return projected, c.usedWeightBlockThreshold, false
	}
	c.usedWeightReserved1m += units
	return projected, c.usedWeightBlockThreshold, true
}

func (c *BinanceClient) releaseUsedWeightBudget(units int) {
	if c == nil {
		return
	}
	if units <= 0 {
		units = 1
	}
	c.usedWeightMu.Lock()
	defer c.usedWeightMu.Unlock()
	c.resetUsedWeightWindowLocked(time.Now().UTC())
	c.usedWeightReserved1m = maxIntValue(c.usedWeightReserved1m-units, 0)
}

func (c *BinanceClient) candidateRequestURLs(rawURL string) []string {
	if c == nil {
		return []string{rawURL}
	}
	spotBases := []string{
		c.spotBaseURL,
		"https://api1.binance.com",
		"https://api2.binance.com",
		"https://api3.binance.com",
	}
	futuresBases := []string{
		c.futuresBaseURL,
		"https://fapi1.binance.com",
		"https://fapi2.binance.com",
		"https://fapi3.binance.com",
	}

	switch {
	case strings.HasPrefix(rawURL, c.spotBaseURL):
		if !ShouldUseBinanceFallbackBases(c.spotBaseURL, binanceSpotBaseURL) {
			return []string{rawURL}
		}
		return ReplaceBaseURLs(rawURL, c.spotBaseURL, spotBases)
	case strings.HasPrefix(rawURL, c.futuresBaseURL):
		if !ShouldUseBinanceFallbackBases(c.futuresBaseURL, binanceFuturesBaseURL) {
			return []string{rawURL}
		}
		return ReplaceBaseURLs(rawURL, c.futuresBaseURL, futuresBases)
	default:
		return []string{rawURL}
	}
}

func (c *BinanceClient) applyNetworkConfig(cfg core.NetworkConfig) {
	if c == nil {
		return
	}
	c.publicGETBaseSlot = MaxRequestGovernorDuration(cfg.PublicGETBaseSlot, 0)
	c.publicGETCooldown403 = MaxRequestGovernorDuration(cfg.PublicGETCooldown403, 0)
	c.publicGETCooldown418 = MaxRequestGovernorDuration(cfg.PublicGETCooldown418, 0)
	c.publicGETUseAPIKey = cfg.PublicGETUseAPIKey
	c.publicGETAPIKeyWhitelist = ParsePublicGETWhitelist(cfg.PublicGETAPIKeyWhitelist)
}

func (c *BinanceClient) waitPublicGETRequestSlot(ctx context.Context, rawURL string, extraSlot time.Duration) error {
	if c == nil {
		return nil
	}
	slot := c.publicGETBaseSlot
	if extraSlot > slot {
		slot = extraSlot
	}

	c.publicGETMu.Lock()
	now := time.Now()
	releaseAt := now
	if c.publicGETCooldownUntil.After(releaseAt) {
		releaseAt = c.publicGETCooldownUntil
	}
	if c.publicGETNextRequestAt.After(releaseAt) {
		releaseAt = c.publicGETNextRequestAt
	}
	if slot > 0 {
		c.publicGETNextRequestAt = releaseAt.Add(slot)
	} else {
		c.publicGETNextRequestAt = releaseAt
	}
	c.publicGETMu.Unlock()

	wait := time.Until(releaseAt)
	if wait <= 0 {
		return nil
	}
	c.recordHistoricalWait(wait)
	if !sleepWithContext(ctx, wait) {
		return ctx.Err()
	}
	return nil
}

func (c *BinanceClient) applyPublicGETHeaders(req *http.Request, rawURL string) {
	if req == nil || c == nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	if !c.shouldAttachAPIKeyToPublicGET(rawURL) {
		return
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	c.recordPublicGETAPIKeyAttached()
}

func (c *BinanceClient) shouldAttachAPIKeyToPublicGET(rawURL string) bool {
	if c == nil || !c.publicGETUseAPIKey || strings.TrimSpace(c.apiKey) == "" {
		return false
	}
	lowerURL := strings.ToLower(strings.TrimSpace(rawURL))
	if len(c.publicGETAPIKeyWhitelist) == 0 {
		return false
	}
	for _, token := range c.publicGETAPIKeyWhitelist {
		if token != "" && strings.Contains(lowerURL, token) {
			return true
		}
	}
	return false
}

func (c *BinanceClient) notePublicGETResponseStatus(statusCode int) {
	c.recordHistoricalRequestStatus(statusCode)
	penalty := c.publicGETPenalty(statusCode)
	if penalty <= 0 {
		return
	}
	c.publicGETMu.Lock()
	until := time.Now().UTC().Add(penalty)
	if until.After(c.publicGETCooldownUntil) {
		c.publicGETCooldownUntil = until
	}
	c.publicGETMu.Unlock()
	c.recordPublicGETCooldown(statusCode)
}

func (c *BinanceClient) publicGETPenalty(statusCode int) time.Duration {
	switch statusCode {
	case http.StatusForbidden:
		return c.publicGETCooldown403
	case http.StatusTeapot:
		return c.publicGETCooldown418
	case http.StatusTooManyRequests, http.StatusAccepted:
		if c.publicGETCooldown403 > 0 {
			return MaxRequestGovernorDuration(c.publicGETCooldown403/2, time.Second)
		}
		return 2 * time.Second
	default:
		return 0
	}
}

func (c *BinanceClient) recordHistoricalCacheLookup(hit bool) {
	if c == nil {
		return
	}
	c.historicalStatsMu.Lock()
	defer c.historicalStatsMu.Unlock()
	if hit {
		c.historicalStats.CacheHits++
		return
	}
	c.historicalStats.CacheMisses++
}

func (c *BinanceClient) recordHistoricalRequestStatus(statusCode int) {
	if c == nil {
		return
	}
	c.historicalStatsMu.Lock()
	defer c.historicalStatsMu.Unlock()
	c.historicalStats.RequestCount++
	switch {
	case statusCode == http.StatusAccepted:
		c.historicalStats.StatusAccepted++
		c.historicalStats.RateLimited++
	case statusCode == http.StatusForbidden:
		c.historicalStats.Status403++
	case statusCode == http.StatusTeapot:
		c.historicalStats.Status418++
	case statusCode == http.StatusTooManyRequests:
		c.historicalStats.Status429++
		c.historicalStats.RateLimited++
	case statusCode >= 400 && statusCode < 500:
		c.historicalStats.Status4xxOther++
	case statusCode >= 500:
		c.historicalStats.Status5xx++
	}
}

func (c *BinanceClient) recordHistoricalRetry() {
	if c == nil {
		return
	}
	c.historicalStatsMu.Lock()
	c.historicalStats.RetryCount++
	c.historicalStatsMu.Unlock()
}

func (c *BinanceClient) recordHistoricalWait(wait time.Duration) {
	if c == nil || wait <= 0 {
		return
	}
	c.historicalStatsMu.Lock()
	c.historicalStats.TotalWait += wait
	c.historicalStatsMu.Unlock()
}

func (c *BinanceClient) recordPublicGETCooldown(statusCode int) {
	if c == nil {
		return
	}
	c.historicalStatsMu.Lock()
	defer c.historicalStatsMu.Unlock()
	switch statusCode {
	case http.StatusForbidden:
		c.historicalStats.Cooldown403++
	case http.StatusTeapot:
		c.historicalStats.Cooldown418++
	}
}

func (c *BinanceClient) recordPublicGETAPIKeyAttached() {
	if c == nil {
		return
	}
	c.historicalStatsMu.Lock()
	c.historicalStats.APIKeyAttached++
	c.historicalStatsMu.Unlock()
}

func (c *BinanceClient) historicalRequestSummary() string {
	if c == nil {
		return ""
	}
	c.historicalStatsMu.Lock()
	stats := c.historicalStats
	c.historicalStatsMu.Unlock()
	return fmt.Sprintf("cache(hit=%d,miss=%d);http(req=%d,retry=%d,202=%d,403=%d,418=%d,429=%d,4xx_other=%d,5xx=%d,rateLimited=%d);governor(cooldown403=%d,cooldown418=%d,apikey=%d);wait=%s",
		stats.CacheHits,
		stats.CacheMisses,
		stats.RequestCount,
		stats.RetryCount,
		stats.StatusAccepted,
		stats.Status403,
		stats.Status418,
		stats.Status429,
		stats.Status4xxOther,
		stats.Status5xx,
		stats.RateLimited,
		stats.Cooldown403,
		stats.Cooldown418,
		stats.APIKeyAttached,
		stats.TotalWait.Round(time.Millisecond))
}
