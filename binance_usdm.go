package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	futuresCommissionMu              sync.Mutex
	futuresCommissionRateCache       map[string]futuresCommissionRate
	historicalFundingRetryMax        int
	historicalPayloadCache           *historicalPayloadCache
	historicalReferenceKlineLookback int
	historicalEstimateKlineLookback  int
	historicalFundingFetchSlot       time.Duration
	publicGETBaseSlot                time.Duration
	publicGETCooldown403             time.Duration
	publicGETCooldown418             time.Duration
	publicGETUseAPIKey               bool
	publicGETAPIKeyWhitelist         []string
	publicGETMu                      sync.Mutex
	publicGETNextRequestAt           time.Time
	publicGETCooldownUntil           time.Time
	usedWeightMu                     sync.Mutex
	usedWeight1m                     int
	usedWeightLimit1m                int
	usedWeightBlockThreshold         int
	usedWeightWindowMinuteUTC        time.Time
	usedWeightReserved1m             int
	historicalSpotPairByFuture       map[string]string
	historicalSpotMeta               map[string]SpotSymbol
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
	Cooldown403    int
	Cooldown418    int
	APIKeyAttached int
}

type accountTradingCostProfile struct {
	SpotTakerFeeRate            float64
	FuturesTakerFeeRate         float64
	BorrowHourlyRateByAsset     map[string]float64
	SpotFeeSymbol               string
	FuturesFeeSymbol            string
	BorrowInterestResolvedCount int
}

type spotTradeFee struct {
	Symbol          string `json:"symbol"`
	MakerCommission string `json:"makerCommission"`
	TakerCommission string `json:"takerCommission"`
}

type futuresCommissionRate struct {
	Symbol              string `json:"symbol"`
	MakerCommissionRate string `json:"makerCommissionRate"`
	TakerCommissionRate string `json:"takerCommissionRate"`
}

type marginNextHourlyInterestRate struct {
	Asset                  string `json:"asset"`
	NextHourlyInterestRate string `json:"nextHourlyInterestRate"`
}

const (
	historicalRequestBaseSlotDefault = 0
	defaultUsedWeightLimit1m         = 6000
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
	FilterType        string `json:"filterType"`
	MinQty            string `json:"minQty,omitempty"`
	MaxQty            string `json:"maxQty,omitempty"`
	StepSize          string `json:"stepSize,omitempty"`
	TickSize          string `json:"tickSize,omitempty"`
	MinNotional       string `json:"minNotional,omitempty"`
	Notional          string `json:"notional,omitempty"`
	MultiplierUp      string `json:"multiplierUp,omitempty"`
	MultiplierDown    string `json:"multiplierDown,omitempty"`
	BidMultiplierUp   string `json:"bidMultiplierUp,omitempty"`
	BidMultiplierDown string `json:"bidMultiplierDown,omitempty"`
	AskMultiplierUp   string `json:"askMultiplierUp,omitempty"`
	AskMultiplierDown string `json:"askMultiplierDown,omitempty"`
	AvgPriceMins      int    `json:"avgPriceMins,omitempty"`
}

// NewBinanceClient 创建一个可复用的币安 HTTP 客户端。
// 这里使用纯标准库，便于静态编译和快速落地 MVP。
func NewBinanceClient(timeout time.Duration) *BinanceClient {
	networkCfg := DefaultEngineConfig().NetworkConfig
	if timeout > 0 {
		networkCfg.HTTPTimeout = timeout
	}
	client, err := NewBinanceClientWithNetworkConfig(networkCfg)
	if err == nil {
		return client
	}

	httpTimeout := networkCfg.HTTPTimeout
	if httpTimeout <= 0 {
		httpTimeout = 10 * time.Second
	}
	client = &BinanceClient{
		spotBaseURL:                binanceSpotBaseURL,
		futuresBaseURL:             binanceFuturesBaseURL,
		futuresCommissionRateCache: make(map[string]futuresCommissionRate),
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
	}
	client.applyNetworkConfig(networkCfg)
	return client
}

func NewBinanceClientWithNetworkConfig(networkCfg NetworkConfig) (*BinanceClient, error) {
	httpClient, err := NewHTTPClient(networkCfg, "")
	if err != nil {
		return nil, err
	}
	client := &BinanceClient{
		spotBaseURL:                binanceSpotBaseURL,
		futuresBaseURL:             binanceFuturesBaseURL,
		httpClient:                 httpClient,
		futuresCommissionRateCache: make(map[string]futuresCommissionRate),
	}
	client.applyNetworkConfig(networkCfg)
	return client, nil
}

// NewAuthenticatedBinanceClient 创建带认证和代理能力的客户端。
func NewAuthenticatedBinanceClient(secret SecretConfig, networkCfg NetworkConfig) (*BinanceClient, error) {
	httpClient, err := NewHTTPClient(networkCfg, secret.ProxyURL)
	if err != nil {
		return nil, err
	}

	client := &BinanceClient{
		spotBaseURL:                binanceSpotBaseURL,
		futuresBaseURL:             binanceFuturesBaseURL,
		apiKey:                     secret.APIKey,
		secretKey:                  secret.Secret,
		proxyURL:                   secret.ProxyURL,
		httpClient:                 httpClient,
		futuresCommissionRateCache: make(map[string]futuresCommissionRate),
	}
	client.applyNetworkConfig(networkCfg)
	return client, nil
}

// ListGoldenSymbols 是模块一的核心对外方法。
// 它会并发拉取现货和 U 本位合约的 exchangeInfo，过滤后求交集，返回干净的黄金币种列表。
func (c *BinanceClient) ListGoldenSymbols(ctx context.Context) ([]string, error) {
	return c.ListGoldenSymbolsWithPreferredQuotes(ctx, preferredSpotQuoteAssets(DefaultEngineConfig().StrategyConfig.PreferredSpotQuoteAssets))
}

func (c *BinanceClient) ListGoldenSymbolsWithPreferredQuotes(ctx context.Context, preferredQuotes []string) ([]string, error) {
	spotSymbols, futuresSymbols, err := c.fetchExchangeInfos(ctx)
	if err != nil {
		return nil, err
	}

	goldenMap := buildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, preferredQuotes, time.Now().UTC())
	spotMeta := make(map[string]SpotSymbol, len(spotSymbols))
	for _, symbol := range spotSymbols {
		spotMeta[symbol.Symbol] = symbol
	}
	c.historicalSpotPairByFuture = make(map[string]string, len(goldenMap))
	c.historicalSpotMeta = make(map[string]SpotSymbol, len(goldenMap))
	for futureSymbol, spotPair := range goldenMap {
		c.historicalSpotPairByFuture[futureSymbol] = spotPair
		if meta, ok := spotMeta[spotPair]; ok {
			c.historicalSpotMeta[futureSymbol] = meta
		}
	}
	golden := make([]string, 0, len(goldenMap))
	for symbol := range goldenMap {
		golden = append(golden, symbol)
	}
	sort.Strings(golden)
	return golden, nil
}

func (c *BinanceClient) historicalSpotPairForFuture(symbol string) string {
	if c == nil {
		return ""
	}
	return c.historicalSpotPairByFuture[strings.ToUpper(strings.TrimSpace(symbol))]
}

func (c *BinanceClient) historicalSpotMetaForFuture(symbol string) (SpotSymbol, bool) {
	if c == nil {
		return SpotSymbol{}, false
	}
	meta, ok := c.historicalSpotMeta[strings.ToUpper(strings.TrimSpace(symbol))]
	return meta, ok
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

func (c *BinanceClient) fetchPremiumIndex(ctx context.Context) ([]PremiumIndexItem, error) {
	var payload []PremiumIndexItem
	if err := c.getJSON(ctx, c.futuresBaseURL+"/fapi/v1/premiumIndex", &payload); err != nil {
		return nil, fmt.Errorf("获取 premiumIndex 失败: %w", err)
	}
	return payload, nil
}

func (c *BinanceClient) signedGetJSON(ctx context.Context, endpoint string, params url.Values, target any) error {
	if c == nil {
		return fmt.Errorf("binance client 为空")
	}
	if strings.TrimSpace(c.apiKey) == "" || strings.TrimSpace(c.secretKey) == "" {
		return fmt.Errorf("缺少 API Key/Secret，无法查询账户真实费率")
	}
	if params == nil {
		params = url.Values{}
	}
	cloned := url.Values{}
	for key, values := range params {
		copied := append([]string(nil), values...)
		cloned[key] = copied
	}
	cloned.Set("recvWindow", strconv.FormatInt(DefaultEngineConfig().ExecutionConfig.RecvWindow, 10))
	cloned.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query := cloned.Encode()
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	_, _ = mac.Write([]byte(query))
	query += "&signature=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query, nil)
	if err != nil {
		return fmt.Errorf("创建签名请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("签名请求失败: %w", err)
	}
	defer resp.Body.Close()
	c.noteUsedWeightFromHeaders(resp.Header)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取签名响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("签名请求状态异常: %d %s body=%s", resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("解析签名 JSON 失败: %w", err)
	}
	return nil
}

func (c *BinanceClient) fetchSpotTradeFees(ctx context.Context) ([]spotTradeFee, error) {
	var payload []spotTradeFee
	if err := c.signedGetJSON(ctx, c.spotBaseURL+"/sapi/v1/asset/tradeFee", nil, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *BinanceClient) fetchFuturesCommissionRate(ctx context.Context, symbol string) (futuresCommissionRate, error) {
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	if normalizedSymbol == "" {
		return futuresCommissionRate{}, fmt.Errorf("symbol 为空")
	}
	c.futuresCommissionMu.Lock()
	if c.futuresCommissionRateCache == nil {
		c.futuresCommissionRateCache = make(map[string]futuresCommissionRate)
	}
	if cached, ok := c.futuresCommissionRateCache[normalizedSymbol]; ok {
		c.futuresCommissionMu.Unlock()
		return cached, nil
	}
	c.futuresCommissionMu.Unlock()

	var payload futuresCommissionRate
	params := url.Values{}
	params.Set("symbol", normalizedSymbol)
	if err := c.signedGetJSON(ctx, c.futuresBaseURL+"/fapi/v1/commissionRate", params, &payload); err != nil {
		return futuresCommissionRate{}, err
	}
	payload.Symbol = strings.ToUpper(strings.TrimSpace(payload.Symbol))
	c.futuresCommissionMu.Lock()
	c.futuresCommissionRateCache[normalizedSymbol] = payload
	c.futuresCommissionMu.Unlock()
	return payload, nil
}

func (c *BinanceClient) fetchNextHourlyMarginInterestRates(ctx context.Context, assets []string) (map[string]float64, error) {
	result := make(map[string]float64)
	normalized := make([]string, 0, len(assets))
	seen := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		token := strings.ToUpper(strings.TrimSpace(asset))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		normalized = append(normalized, token)
	}
	for start := 0; start < len(normalized); start += 20 {
		end := start + 20
		if end > len(normalized) {
			end = len(normalized)
		}
		var payload []marginNextHourlyInterestRate
		params := url.Values{}
		params.Set("assets", strings.Join(normalized[start:end], ","))
		params.Set("isIsolated", "FALSE")
		if err := c.signedGetJSON(ctx, c.spotBaseURL+"/sapi/v1/margin/next-hourly-interest-rate", params, &payload); err != nil {
			return nil, err
		}
		for _, item := range payload {
			asset := strings.ToUpper(strings.TrimSpace(item.Asset))
			if asset == "" {
				continue
			}
			result[asset] = parseFloat(item.NextHourlyInterestRate)
		}
	}
	return result, nil
}

func (c *BinanceClient) resolveAccountTradingCostProfile(ctx context.Context, spotSymbols []string, futuresSymbols []string, borrowAssets []string) (accountTradingCostProfile, error) {
	profile := accountTradingCostProfile{
		BorrowHourlyRateByAsset: make(map[string]float64),
	}
	spotSet := make(map[string]struct{}, len(spotSymbols))
	for _, symbol := range spotSymbols {
		spotSet[strings.ToUpper(strings.TrimSpace(symbol))] = struct{}{}
	}
	errs := make([]string, 0, 3)

	if spotFees, err := c.fetchSpotTradeFees(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("spot_trade_fee=%v", err))
	} else {
		for _, item := range spotFees {
			symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
			if len(spotSet) > 0 {
				if _, ok := spotSet[symbol]; !ok {
					continue
				}
			}
			taker := parseFloat(item.TakerCommission)
			if taker > profile.SpotTakerFeeRate {
				profile.SpotTakerFeeRate = taker
				profile.SpotFeeSymbol = symbol
			}
		}
		if profile.SpotTakerFeeRate <= 0 && len(spotFees) > 0 {
			profile.SpotTakerFeeRate = parseFloat(spotFees[0].TakerCommission)
			profile.SpotFeeSymbol = strings.ToUpper(strings.TrimSpace(spotFees[0].Symbol))
		}
	}

	futuresSeen := make(map[string]struct{}, len(futuresSymbols))
	for _, symbol := range futuresSymbols {
		normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
		if normalizedSymbol == "" {
			continue
		}
		if _, ok := futuresSeen[normalizedSymbol]; ok {
			continue
		}
		futuresSeen[normalizedSymbol] = struct{}{}
		commission, err := c.fetchFuturesCommissionRate(ctx, normalizedSymbol)
		if err != nil {
			errs = append(errs, fmt.Sprintf("futures_commission[%s]=%v", normalizedSymbol, err))
			continue
		}
		taker := parseFloat(commission.TakerCommissionRate)
		if taker > profile.FuturesTakerFeeRate {
			profile.FuturesTakerFeeRate = taker
			profile.FuturesFeeSymbol = strings.ToUpper(strings.TrimSpace(commission.Symbol))
		}
	}

	if len(borrowAssets) > 0 {
		rates, err := c.fetchNextHourlyMarginInterestRates(ctx, borrowAssets)
		if err != nil {
			errs = append(errs, fmt.Sprintf("margin_interest=%v", err))
		} else {
			profile.BorrowHourlyRateByAsset = rates
			profile.BorrowInterestResolvedCount = len(rates)
		}
	}
	if len(errs) > 0 {
		return profile, fmt.Errorf("账户真实成本存在部分查询失败: %s", strings.Join(errs, " | "))
	}
	return profile, nil
}

// getJSON 是统一的 HTTP GET + JSON 反序列化辅助函数。
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
				lastErr = newPublicGETHTTPError(candidateURL, resp.StatusCode, resp.Status, body)
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
	usedWeight := parseHeaderInt(header.Get("X-MBX-USED-WEIGHT-1M"))
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

func parseHeaderInt(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

type publicGETHTTPError struct {
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func newPublicGETHTTPError(rawURL string, statusCode int, status string, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	return publicGETHTTPError{
		URL:        rawURL,
		StatusCode: statusCode,
		Status:     strings.TrimSpace(status),
		Body:       snippet,
	}
}

func (e publicGETHTTPError) Error() string {
	message := fmt.Sprintf("HTTP 状态异常: %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if strings.TrimSpace(e.Status) != "" {
		message = fmt.Sprintf("HTTP 状态异常: %s", e.Status)
	}
	if strings.TrimSpace(e.Body) != "" {
		message += " body=" + e.Body
	}
	return message
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

func (c *BinanceClient) applyNetworkConfig(cfg NetworkConfig) {
	if c == nil {
		return
	}
	c.publicGETBaseSlot = maxRequestGovernorDuration(cfg.PublicGETBaseSlot, 0)
	c.publicGETCooldown403 = maxRequestGovernorDuration(cfg.PublicGETCooldown403, 0)
	c.publicGETCooldown418 = maxRequestGovernorDuration(cfg.PublicGETCooldown418, 0)
	c.publicGETUseAPIKey = cfg.PublicGETUseAPIKey
	c.publicGETAPIKeyWhitelist = parsePublicGETWhitelist(cfg.PublicGETAPIKeyWhitelist)
}

func parsePublicGETWhitelist(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
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
			return maxRequestGovernorDuration(c.publicGETCooldown403/2, time.Second)
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

func maxRequestGovernorDuration(a time.Duration, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}

func preferredSpotQuoteAssets(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		asset := strings.ToUpper(strings.TrimSpace(part))
		if asset == "" {
			continue
		}
		if _, duplicated := seen[asset]; duplicated {
			continue
		}
		seen[asset] = struct{}{}
		result = append(result, asset)
	}
	if len(result) == 0 {
		return []string{"FDUSD", "USDC", "USDT"}
	}
	return result
}

func buildPreferredSpotSymbolsByBase(symbols []SpotSymbol, preferredQuotes []string) map[string]SpotSymbol {
	quoteRank := make(map[string]int, len(preferredQuotes))
	for idx, asset := range preferredQuotes {
		quoteRank[strings.ToUpper(strings.TrimSpace(asset))] = idx
	}

	result := make(map[string]SpotSymbol, len(symbols))
	bestRank := make(map[string]int, len(symbols))
	for _, symbol := range symbols {
		if !isValidExchangeSymbol(symbol.Symbol) || symbol.Status != "TRADING" {
			continue
		}
		rank, ok := quoteRank[strings.ToUpper(strings.TrimSpace(symbol.QuoteAsset))]
		if !ok {
			continue
		}
		baseAsset := strings.ToUpper(strings.TrimSpace(symbol.BaseAsset))
		if baseAsset == "" {
			continue
		}
		current, exists := result[baseAsset]
		if !exists || rank < bestRank[baseAsset] || (rank == bestRank[baseAsset] && symbol.Symbol < current.Symbol) {
			result[baseAsset] = symbol
			bestRank[baseAsset] = rank
		}
	}
	return result
}

func futuresBaseAsset(symbol FuturesSymbol) string {
	pair := strings.ToUpper(strings.TrimSpace(symbol.Pair))
	marginAsset := strings.ToUpper(strings.TrimSpace(symbol.MarginAsset))
	if pair == "" {
		pair = strings.ToUpper(strings.TrimSpace(symbol.Symbol))
	}
	if marginAsset != "" && strings.HasSuffix(pair, marginAsset) {
		baseAsset := strings.TrimSuffix(pair, marginAsset)
		if baseAsset != "" {
			return baseAsset
		}
	}
	for _, quoteAsset := range []string{"USDT", "FDUSD", "USDC"} {
		if strings.HasSuffix(pair, quoteAsset) {
			baseAsset := strings.TrimSuffix(pair, quoteAsset)
			if baseAsset != "" {
				return baseAsset
			}
		}
	}
	return pair
}

func buildGoldenSymbolSpotPairs(spotSymbols []SpotSymbol, futuresSymbols []FuturesSymbol, preferredQuotes []string, now time.Time) map[string]string {
	spotByBase := buildPreferredSpotSymbolsByBase(spotSymbols, preferredQuotes)
	perpetuals := filterUSDTPERPTradingFutures(futuresSymbols, now)
	result := make(map[string]string, len(perpetuals))
	for _, symbol := range perpetuals {
		baseAsset := futuresBaseAsset(symbol)
		if baseAsset == "" {
			continue
		}
		spotSymbol, ok := spotByBase[baseAsset]
		if !ok {
			continue
		}
		result[symbol.Symbol] = spotSymbol.Symbol
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
	if strings.TrimSpace(symbol) == "" {
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
