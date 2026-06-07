package exchange

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

	"lingxi/internal/core"
)

const (
	binanceSpotBaseURL    = "https://api.binance.com"
	binanceFuturesBaseURL = "https://fapi.binance.com"
)

// BinanceClient 是与币安交互的数据访问入口：现货/合约元数据、账户费率、
// 历史行情抓取（带缓存/限频/回退），是「唯一摸网络的包」的核心。
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
	historicalPayloadCache           *HistoricalPayloadCache
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

// AccountTradingCostProfile 描述账户真实的现货/合约吃单费率与借贷成本画像。
type AccountTradingCostProfile struct {
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

// ClientConfig 用于按显式基址/凭证/HTTP 客户端构造 BinanceClient，
// 主要服务于集成与测试场景（指向 mock server），避免上层直接触碰未导出字段。
type ClientConfig struct {
	SpotBaseURL    string
	FuturesBaseURL string
	APIKey         string
	SecretKey      string
	ProxyURL       string
	HTTPClient     *http.Client
}

// SeedUsedWeightWindow 用于测试预置 1m 权重窗口状态（已用/上限/阻断阈值/窗口起点）。
// windowMinuteUTC 为零值时保持窗口起点不变。
func (c *BinanceClient) SeedUsedWeightWindow(used int, limit int, blockThreshold int, windowMinuteUTC time.Time) {
	if c == nil {
		return
	}
	c.usedWeightMu.Lock()
	c.usedWeight1m = used
	c.usedWeightLimit1m = limit
	c.usedWeightBlockThreshold = blockThreshold
	if !windowMinuteUTC.IsZero() {
		c.usedWeightWindowMinuteUTC = windowMinuteUTC
	}
	c.usedWeightMu.Unlock()
}

// NewClient 按 ClientConfig 构造一个 BinanceClient（不应用默认网络治理参数）。
func NewClient(cfg ClientConfig) *BinanceClient {
	return &BinanceClient{
		spotBaseURL:                cfg.SpotBaseURL,
		futuresBaseURL:             cfg.FuturesBaseURL,
		apiKey:                     cfg.APIKey,
		secretKey:                  cfg.SecretKey,
		proxyURL:                   cfg.ProxyURL,
		httpClient:                 cfg.HTTPClient,
		futuresCommissionRateCache: make(map[string]futuresCommissionRate),
	}
}

// NewBinanceClient 创建一个可复用的币安 HTTP 客户端。
func NewBinanceClient(timeout time.Duration) *BinanceClient {
	networkCfg := core.DefaultEngineConfig().NetworkConfig
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

func NewBinanceClientWithNetworkConfig(networkCfg core.NetworkConfig) (*BinanceClient, error) {
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
func NewAuthenticatedBinanceClient(secret SecretConfig, networkCfg core.NetworkConfig) (*BinanceClient, error) {
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

// 导出访问器/委托：让上层（引擎、网关、WS、回测）只经导出表面访问客户端，
// 不直接触碰未导出字段与方法。
func (c *BinanceClient) SpotBaseURL() string      { return c.spotBaseURL }
func (c *BinanceClient) FuturesBaseURL() string   { return c.futuresBaseURL }
func (c *BinanceClient) APIKey() string           { return c.apiKey }
func (c *BinanceClient) SecretKey() string        { return c.secretKey }
func (c *BinanceClient) ProxyURL() string         { return c.proxyURL }
func (c *BinanceClient) HTTPClient() *http.Client { return c.httpClient }

// GetJSON 是 getJSON 的导出入口（公共 GET + 限频/回退）。
func (c *BinanceClient) GetJSON(ctx context.Context, url string, target any) error {
	return c.getJSON(ctx, url, target)
}

// NoteUsedWeightFromHeaders 导出 used-weight 头解析，供网关在收到响应后回填配额。
func (c *BinanceClient) NoteUsedWeightFromHeaders(header http.Header) {
	c.noteUsedWeightFromHeaders(header)
}

// ReserveUsedWeightBudget / ReleaseUsedWeightBudget 导出 1m 权重预算的预留/释放。
func (c *BinanceClient) ReserveUsedWeightBudget(units int) (int, int, bool) {
	return c.reserveUsedWeightBudget(units)
}

func (c *BinanceClient) ReleaseUsedWeightBudget(units int) {
	c.releaseUsedWeightBudget(units)
}

// FetchExchangeInfos 导出现货/合约 exchangeInfo 的并发拉取。
func (c *BinanceClient) FetchExchangeInfos(ctx context.Context) ([]SpotSymbol, []FuturesSymbol, error) {
	return c.fetchExchangeInfos(ctx)
}

// FetchPremiumIndex 导出 premiumIndex 拉取。
func (c *BinanceClient) FetchPremiumIndex(ctx context.Context) ([]PremiumIndexItem, error) {
	return c.fetchPremiumIndex(ctx)
}

// FetchFundingInfo 导出 fundingInfo(资金费上下限/结算周期)拉取。
func (c *BinanceClient) FetchFundingInfo(ctx context.Context) ([]FundingInfoItem, error) {
	return c.fetchFundingInfo(ctx)
}

// ResolveAccountTradingCostProfile 导出账户费率/借贷成本画像解析。
func (c *BinanceClient) ResolveAccountTradingCostProfile(ctx context.Context, spotSymbols []string, futuresSymbols []string, borrowAssets []string) (AccountTradingCostProfile, error) {
	return c.resolveAccountTradingCostProfile(ctx, spotSymbols, futuresSymbols, borrowAssets)
}

// ApplyNetworkConfig 导出网络配置应用（限速 slot / 冷却 / API Key 白名单）。
func (c *BinanceClient) ApplyNetworkConfig(cfg core.NetworkConfig) {
	c.applyNetworkConfig(cfg)
}

// HistoricalRequestSummary 导出历史抓取的统计摘要。
func (c *BinanceClient) HistoricalRequestSummary() string {
	return c.historicalRequestSummary()
}

// HistoricalSpotPairForFuture / HistoricalSpotMetaForFuture 导出历史回测用的合约->现货映射查询。
func (c *BinanceClient) HistoricalSpotPairForFuture(symbol string) string {
	return c.historicalSpotPairForFuture(symbol)
}

func (c *BinanceClient) HistoricalSpotMetaForFuture(symbol string) (SpotSymbol, bool) {
	return c.historicalSpotMetaForFuture(symbol)
}

// SetHistoricalPayloadCache 注入历史负载缓存（回测路径使用）。
func (c *BinanceClient) SetHistoricalPayloadCache(cache *HistoricalPayloadCache) {
	c.historicalPayloadCache = cache
}

// SetHistoricalFetchTuning 配置历史抓取相关的调参（回测路径使用），
// 避免上层直接触碰未导出字段。
func (c *BinanceClient) SetHistoricalFetchTuning(fundingRetryMax int, referenceKlineLookback int, estimateKlineLookback int, fundingFetchSlot time.Duration) {
	if c == nil {
		return
	}
	c.historicalFundingRetryMax = fundingRetryMax
	c.historicalReferenceKlineLookback = referenceKlineLookback
	c.historicalEstimateKlineLookback = estimateKlineLookback
	c.historicalFundingFetchSlot = fundingFetchSlot
}

// 以下 setter 主要供测试夹具注入（指向 mock server / 预置历史映射），
// 也允许集成场景覆盖默认基址与凭证。
func (c *BinanceClient) SetSpotBaseURL(url string)     { c.spotBaseURL = url }
func (c *BinanceClient) SetFuturesBaseURL(url string)  { c.futuresBaseURL = url }
func (c *BinanceClient) SetAPIKey(key string)          { c.apiKey = key }
func (c *BinanceClient) SetHTTPClient(hc *http.Client) { c.httpClient = hc }

func (c *BinanceClient) SetHistoricalSpotPairByFuture(m map[string]string) {
	c.historicalSpotPairByFuture = m
}

func (c *BinanceClient) SetHistoricalSpotMeta(m map[string]SpotSymbol) {
	c.historicalSpotMeta = m
}

// ListGoldenSymbols 并发拉取现货和 U 本位合约的 exchangeInfo，过滤后求交集，返回干净的黄金币种列表。
func (c *BinanceClient) ListGoldenSymbols(ctx context.Context) ([]string, error) {
	return c.ListGoldenSymbolsWithPreferredQuotes(ctx, PreferredSpotQuoteAssets(core.DefaultEngineConfig().StrategyConfig.PreferredSpotQuoteAssets))
}

func (c *BinanceClient) ListGoldenSymbolsWithPreferredQuotes(ctx context.Context, preferredQuotes []string) ([]string, error) {
	spotSymbols, futuresSymbols, err := c.fetchExchangeInfos(ctx)
	if err != nil {
		return nil, err
	}

	goldenMap := BuildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, preferredQuotes, time.Now().UTC())
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

func (c *BinanceClient) fetchFundingInfo(ctx context.Context) ([]FundingInfoItem, error) {
	var payload []FundingInfoItem
	if err := c.getJSON(ctx, c.futuresBaseURL+"/fapi/v1/fundingInfo", &payload); err != nil {
		return nil, fmt.Errorf("获取 fundingInfo 失败: %w", err)
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
	cloned.Set("recvWindow", strconv.FormatInt(core.DefaultEngineConfig().ExecutionConfig.RecvWindow, 10))
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

// marginMaxBorrowable 是 GET /sapi/v1/margin/maxBorrowable 的回执:amount=当前最大可借(base 单位)。
type marginMaxBorrowable struct {
	Amount      string `json:"amount"`
	BorrowLimit string `json:"borrowLimit"`
}

// MaxBorrowable 查询全仓杠杆账户某资产的当前最大可借数量(base 单位)。R29 借币容量 pre-open 守卫用:
// 负向 carry 开仓需借入 base 资产做空现货,开仓前据此判断「借得到 + 留足余量」,借不到则不开(不留裸腿)。
func (c *BinanceClient) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	token := strings.ToUpper(strings.TrimSpace(asset))
	if token == "" {
		return 0, fmt.Errorf("maxBorrowable: asset 不能为空")
	}
	params := url.Values{}
	params.Set("asset", token)
	params.Set("isIsolated", "FALSE")
	var payload marginMaxBorrowable
	if err := c.signedGetJSON(ctx, c.spotBaseURL+"/sapi/v1/margin/maxBorrowable", params, &payload); err != nil {
		return 0, err
	}
	return parseFloat(payload.Amount), nil
}

// isRateLimitError 判断错误是否为币安限流/封禁(429 Too Many Requests / 418 banned)。
// 用于在批量探测费率时一旦触发限流就立即停手，避免把账户 IP 越查越封。
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "418") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "Too Many Requests") ||
		strings.Contains(msg, "banned")
}

func (c *BinanceClient) resolveAccountTradingCostProfile(ctx context.Context, spotSymbols []string, futuresSymbols []string, borrowAssets []string) (AccountTradingCostProfile, error) {
	profile := AccountTradingCostProfile{
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

	// 合约 taker/maker 费率是按账户 VIP 等级【全市场统一】的，这里也只取 max。因此【绝不能】把整个
	// golden 宇宙(数百个 symbol)逐个 /fapi/v1/commissionRate 探测——签名请求不走公共 GET 限流器，
	// 几百个瞬时请求会直接撞 2400/min 限额，从 429 升级成 418 封 IP。只探测少量代表 symbol 即可代表账户档位；
	// 单个 symbol 的费率在真正交易时再按需 fetch+缓存。
	const maxFuturesCommissionProbeSymbols = 6
	futuresSeen := make(map[string]struct{}, len(futuresSymbols))
	probed := 0
	for _, symbol := range futuresSymbols {
		normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
		if normalizedSymbol == "" {
			continue
		}
		if _, ok := futuresSeen[normalizedSymbol]; ok {
			continue
		}
		futuresSeen[normalizedSymbol] = struct{}{}
		if probed >= maxFuturesCommissionProbeSymbols {
			break
		}
		probed++
		commission, err := c.fetchFuturesCommissionRate(ctx, normalizedSymbol)
		if err != nil {
			errs = append(errs, fmt.Sprintf("futures_commission[%s]=%v", normalizedSymbol, err))
			// 一旦被限流/封禁，立即停手，避免把 IP 越查越封。已探测到的样本足以代表账户费率档位。
			if isRateLimitError(err) {
				break
			}
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
