package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	binanceSpotBaseURL    = "https://api.binance.com"
	binanceFuturesBaseURL = "https://fapi.binance.com"
)

// BinanceClient 是模块一的数据访问入口。
// 当前阶段只负责请求交易所基础元数据，不涉及下单、账户和风控动作。
type BinanceClient struct {
	spotBaseURL                      string
	futuresBaseURL                   string
	apiKey                           string
	secretKey                        string
	proxyURL                         string
	httpClient                       *http.Client
	historicalFundingRetryMax        int
	historicalPayloadCache           *historicalPayloadCache
	historicalReferenceKlineLookback int
	historicalEstimateKlineLookback  int
	historicalFundingFetchSlot       time.Duration
	historicalRequestMu              sync.Mutex
	historicalNextRequestAt          time.Time
	historicalStatsMu                sync.Mutex
	historicalStats                  historicalRequestStats
}

type historicalRequestStats struct {
	CacheHits      int
	CacheMisses    int
	RequestCount   int
	RetryCount     int
	StatusAccepted int
	Status403      int
	Status418      int
	Status429      int
	Status4xxOther int
	Status5xx      int
	TotalWait      time.Duration
	RateLimited    int
}

const (
	historicalRequestBaseSlotDefault = 0
)

// SpotExchangeInfoResponse 对应币安现货 exchangeInfo 的精简响应结构。
type SpotExchangeInfoResponse struct {
	Symbols []SpotSymbol `json:"symbols"`
}

// FuturesExchangeInfoResponse 对应币安 U 本位合约 exchangeInfo 的精简响应结构。
type FuturesExchangeInfoResponse struct {
	Symbols []FuturesSymbol `json:"symbols"`
}

// SpotSymbol 表示一个现货交易对的基础信息。
// 模块一只关心 symbol、status 和 quoteAsset 三个过滤字段。
type SpotSymbol struct {
	Symbol             string           `json:"symbol"`
	Status             string           `json:"status"`
	BaseAsset          string           `json:"baseAsset"`
	QuoteAsset         string           `json:"quoteAsset"`
	BaseAssetPrecision int              `json:"baseAssetPrecision"`
	Filters            []ExchangeFilter `json:"filters"`
}

// FuturesSymbol 表示一个 U 本位永续/交割合约的基础信息。
// 模块一重点使用 symbol、pair、status、contractType 和 marginAsset。
type FuturesSymbol struct {
	Symbol            string           `json:"symbol"`
	Pair              string           `json:"pair"`
	Status            string           `json:"status"`
	ContractType      string           `json:"contractType"`
	DeliveryDate      int64            `json:"deliveryDate"`
	MarginAsset       string           `json:"marginAsset"`
	QuantityPrecision int              `json:"quantityPrecision"`
	Filters           []ExchangeFilter `json:"filters"`
}

// ExchangeFilter 表示交易所返回的交易限制规则。
type ExchangeFilter struct {
	FilterType  string `json:"filterType"`
	MinQty      string `json:"minQty,omitempty"`
	MaxQty      string `json:"maxQty,omitempty"`
	StepSize    string `json:"stepSize,omitempty"`
	TickSize    string `json:"tickSize,omitempty"`
	MinNotional string `json:"minNotional,omitempty"`
	Notional    string `json:"notional,omitempty"`
}

// NewBinanceClient 创建一个可复用的币安 HTTP 客户端。
// 这里使用纯标准库，便于静态编译和快速落地 MVP。
func NewBinanceClient(timeout time.Duration) *BinanceClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &BinanceClient{
		spotBaseURL:    binanceSpotBaseURL,
		futuresBaseURL: binanceFuturesBaseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// NewAuthenticatedBinanceClient 创建带认证和代理能力的客户端。
func NewAuthenticatedBinanceClient(secret SecretConfig, timeout time.Duration) (*BinanceClient, error) {
	httpClient, err := NewHTTPClient(timeout, secret.ProxyURL)
	if err != nil {
		return nil, err
	}

	return &BinanceClient{
		spotBaseURL:    binanceSpotBaseURL,
		futuresBaseURL: binanceFuturesBaseURL,
		apiKey:         secret.APIKey,
		secretKey:      secret.Secret,
		proxyURL:       secret.ProxyURL,
		httpClient:     httpClient,
	}, nil
}

// ListGoldenSymbols 是模块一的核心对外方法。
// 它会并发拉取现货和 U 本位合约的 exchangeInfo，过滤后求交集，返回干净的黄金币种列表。
func (c *BinanceClient) ListGoldenSymbols(ctx context.Context) ([]string, error) {
	spotSymbols, futuresSymbols, err := c.fetchExchangeInfos(ctx)
	if err != nil {
		return nil, err
	}

	spotSet := buildSpotUSDTTradingSet(spotSymbols)
	perpetuals := filterUSDTPERPTradingFutures(futuresSymbols, time.Now().UTC())

	golden := make([]string, 0, len(perpetuals))
	seen := make(map[string]struct{}, len(perpetuals))
	for _, symbol := range perpetuals {
		pair := symbol.Pair
		if pair == "" {
			pair = symbol.Symbol
		}

		if _, ok := spotSet[pair]; !ok {
			continue
		}
		if _, duplicated := seen[pair]; duplicated {
			continue
		}

		seen[pair] = struct{}{}
		golden = append(golden, pair)
	}

	sort.Strings(golden)
	return golden, nil
}

// fetchExchangeInfos 并发抓取现货和 U 本位合约的 exchangeInfo。
// 这里采用轻量的 Master-Worker 思路：主流程发起两个子任务，并等待结果汇总。
func (c *BinanceClient) fetchExchangeInfos(ctx context.Context) ([]SpotSymbol, []FuturesSymbol, error) {
	var (
		wg            sync.WaitGroup
		spotSymbols   []SpotSymbol
		futuresSymbol []FuturesSymbol
		spotErr       error
		futuresErr    error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		spotSymbols, spotErr = c.fetchSpotExchangeInfo(ctx)
	}()

	go func() {
		defer wg.Done()
		futuresSymbol, futuresErr = c.fetchFuturesExchangeInfo(ctx)
	}()

	wg.Wait()

	if spotErr != nil {
		return nil, nil, spotErr
	}
	if futuresErr != nil {
		return nil, nil, futuresErr
	}

	return spotSymbols, futuresSymbol, nil
}

// fetchSpotExchangeInfo 拉取币安现货交易所元数据。
func (c *BinanceClient) fetchSpotExchangeInfo(ctx context.Context) ([]SpotSymbol, error) {
	var payload SpotExchangeInfoResponse
	if err := c.getJSON(ctx, c.spotBaseURL+"/api/v3/exchangeInfo", &payload); err != nil {
		return nil, fmt.Errorf("获取现货 exchangeInfo 失败: %w", err)
	}

	return payload.Symbols, nil
}

// fetchFuturesExchangeInfo 拉取币安 U 本位合约交易所元数据。
func (c *BinanceClient) fetchFuturesExchangeInfo(ctx context.Context) ([]FuturesSymbol, error) {
	var payload FuturesExchangeInfoResponse
	if err := c.getJSON(ctx, c.futuresBaseURL+"/fapi/v1/exchangeInfo", &payload); err != nil {
		return nil, fmt.Errorf("获取 U 本位合约 exchangeInfo 失败: %w", err)
	}

	return payload.Symbols, nil
}

// getJSON 是统一的 HTTP GET + JSON 反序列化辅助函数。
func (c *BinanceClient) getJSON(ctx context.Context, url string, target any) error {
	var lastErr error
	for _, candidateURL := range c.candidateRequestURLs(url) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidateURL, nil)
		if err != nil {
			return fmt.Errorf("创建请求失败: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求失败: %w", err)
			continue
		}

		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("HTTP 状态异常: %s", resp.Status)
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

func (c *BinanceClient) candidateRequestURLs(rawURL string) []string {
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
		return replaceBaseURLs(rawURL, c.spotBaseURL, spotBases)
	case strings.HasPrefix(rawURL, c.futuresBaseURL):
		return replaceBaseURLs(rawURL, c.futuresBaseURL, futuresBases)
	default:
		return []string{rawURL}
	}
}

func replaceBaseURLs(rawURL string, currentBase string, bases []string) []string {
	suffix := strings.TrimPrefix(rawURL, currentBase)
	result := make([]string, 0, len(bases))
	seen := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		if strings.TrimSpace(base) == "" {
			continue
		}
		candidate := base + suffix
		if _, duplicated := seen[candidate]; duplicated {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	if len(result) == 0 {
		return []string{rawURL}
	}
	return result
}

func (c *BinanceClient) waitHistoricalRequestSlot(ctx context.Context) error {
	if c == nil {
		return nil
	}
	slot := c.historicalFundingFetchSlot
	if slot <= 0 {
		return nil
	}

	c.historicalRequestMu.Lock()
	now := time.Now()
	releaseAt := c.historicalNextRequestAt
	if releaseAt.Before(now) {
		releaseAt = now
	}
	c.historicalNextRequestAt = releaseAt.Add(slot)
	c.historicalRequestMu.Unlock()

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

func (c *BinanceClient) historicalRequestSummary() string {
	if c == nil {
		return ""
	}
	c.historicalStatsMu.Lock()
	stats := c.historicalStats
	c.historicalStatsMu.Unlock()
	return fmt.Sprintf("cache(hit=%d,miss=%d);http(req=%d,retry=%d,202=%d,403=%d,418=%d,429=%d,4xx_other=%d,5xx=%d,rateLimited=%d);wait=%s",
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
		stats.TotalWait.Round(time.Millisecond))
}

// buildSpotUSDTTradingSet 将“现货中符合条件的交易对”构造成集合，便于后续 O(1) 求交集。
// 过滤条件：
// 1. 交易对以 USDT 结尾
// 2. 状态为 TRADING
// 3. 计价资产为 USDT
func buildSpotUSDTTradingSet(symbols []SpotSymbol) map[string]struct{} {
	result := make(map[string]struct{}, len(symbols))

	for _, symbol := range symbols {
		if !isValidExchangeSymbol(symbol.Symbol) {
			continue
		}
		if !strings.HasSuffix(symbol.Symbol, "USDT") {
			continue
		}
		if symbol.Status != "TRADING" {
			continue
		}
		if symbol.QuoteAsset != "USDT" {
			continue
		}

		result[symbol.Symbol] = struct{}{}
	}

	return result
}

// filterUSDTPERPTradingFutures 过滤出符合条件的 U 本位永续合约。
// 过滤条件：
// 1. 合约 symbol 以 USDT 结尾
// 2. 状态为 TRADING
// 3. 合约类型为 PERPETUAL
// 4. 保证金币种为 USDT
func filterUSDTPERPTradingFutures(symbols []FuturesSymbol, now time.Time) []FuturesSymbol {
	result := make([]FuturesSymbol, 0, len(symbols))

	for _, symbol := range symbols {
		if !isValidExchangeSymbol(symbol.Symbol) {
			continue
		}
		if !strings.HasSuffix(symbol.Symbol, "USDT") {
			continue
		}
		if symbol.Status != "TRADING" {
			continue
		}
		if symbol.ContractType != "PERPETUAL" {
			continue
		}
		if symbol.MarginAsset != "USDT" {
			continue
		}
		if hasUpcomingFuturesDelist(symbol, now) {
			continue
		}

		result = append(result, symbol)
	}

	return result
}

func hasUpcomingFuturesDelist(symbol FuturesSymbol, now time.Time) bool {
	if symbol.DeliveryDate <= 0 {
		return false
	}
	return symbol.DeliveryDate <= now.AddDate(1, 0, 0).UnixMilli()
}

func isValidExchangeSymbol(symbol string) bool {
	if !strings.HasSuffix(symbol, "USDT") {
		return false
	}
	for _, r := range symbol {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
