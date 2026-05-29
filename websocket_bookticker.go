package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// BookTickerCache 保存最新的最优买卖一快照。
type BookTickerCache struct {
	mu      sync.RWMutex
	tickers map[string]BookTicker
}

// DepthCache 保存最新的部分深度快照。
type DepthCache struct {
	signalMu sync.Mutex
	depths   sync.Map
	signals  sync.Map
}

type depthSignal struct {
	ch       chan struct{}
	refCount int
}

// DepthLevel 表示一档已经完成数值解析的盘口。
type DepthLevel struct {
	Price    float64
	Quantity float64
}

// ParsedOrderBookDepth 表示适合高频计算的数值化深度快照。
type ParsedOrderBookDepth struct {
	LastUpdateID int64
	Bids         []DepthLevel
	Asks         []DepthLevel
}

// NewBookTickerCache 创建一个线程安全的盘口缓存。
func NewBookTickerCache() *BookTickerCache {
	return &BookTickerCache{
		tickers: make(map[string]BookTicker),
	}
}

// NewDepthCache 创建一个线程安全的深度缓存。
func NewDepthCache() *DepthCache {
	return &DepthCache{}
}

// Update 写入最新快照。
func (c *BookTickerCache) Update(ticker BookTicker) {
	c.mu.Lock()
	c.tickers[ticker.Symbol] = ticker
	c.mu.Unlock()
}

// Get 返回指定 symbol 的最新盘口。
func (c *BookTickerCache) Get(symbol string) (BookTicker, bool) {
	c.mu.RLock()
	ticker, ok := c.tickers[symbol]
	c.mu.RUnlock()
	return ticker, ok
}

// MissingSymbols 返回尚未完成 bookTicker 预热的 symbol 列表。
func (c *BookTickerCache) MissingSymbols(symbols []string) []string {
	missing := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if _, ok := c.Get(symbol); ok {
			continue
		}
		missing = append(missing, symbol)
	}
	return missing
}

// Update 写入最新深度快照。
func (c *DepthCache) Update(symbol string, depth ParsedOrderBookDepth) {
	c.depths.Store(symbol, depth)
	if signal, ok := c.loadSignal(symbol); ok {
		select {
		case signal.ch <- struct{}{}:
		default:
		}
	}
}

// Get 返回指定 symbol 的最新深度快照。
func (c *DepthCache) Get(symbol string) (ParsedOrderBookDepth, bool) {
	value, ok := c.depths.Load(symbol)
	if !ok {
		return ParsedOrderBookDepth{}, false
	}
	depth, ok := value.(ParsedOrderBookDepth)
	return depth, ok
}

// Subscribe 返回指定 symbol 的深度更新信号以及释放订阅的函数。
func (c *DepthCache) Subscribe(symbol string) (<-chan struct{}, func()) {
	c.signalMu.Lock()
	defer c.signalMu.Unlock()
	if current, ok := c.loadSignal(symbol); ok {
		current.refCount++
		return current.ch, func() {
			c.releaseSignal(symbol)
		}
	}

	created := &depthSignal{
		ch:       make(chan struct{}, 1),
		refCount: 1,
	}
	actual, loaded := c.signals.LoadOrStore(symbol, created)
	if loaded {
		current := actual.(*depthSignal)
		current.refCount++
		return current.ch, func() {
			c.releaseSignal(symbol)
		}
	}
	return created.ch, func() {
		c.releaseSignal(symbol)
	}
}

func (c *DepthCache) loadSignal(symbol string) (*depthSignal, bool) {
	value, ok := c.signals.Load(symbol)
	if !ok {
		return nil, false
	}
	signal, ok := value.(*depthSignal)
	return signal, ok
}

func (c *DepthCache) releaseSignal(symbol string) {
	c.signalMu.Lock()
	defer c.signalMu.Unlock()
	value, ok := c.signals.Load(symbol)
	if !ok {
		return
	}
	signal, ok := value.(*depthSignal)
	if !ok {
		return
	}
	signal.refCount--
	if signal.refCount > 0 {
		return
	}
	c.signals.Delete(symbol)
}

// MissingSymbols 返回尚未完成深度预热的 symbol 列表。
func (c *DepthCache) MissingSymbols(symbols []string) []string {
	missing := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if _, ok := c.Get(symbol); ok {
			continue
		}
		missing = append(missing, symbol)
	}
	return missing
}

// StartBookTickerStreams 启动现货和合约的实时盘口订阅。
func (e *FundingRateSniperEngine) StartBookTickerStreams(ctx context.Context, targets []*MonitoredTarget) {
	if len(targets) == 0 {
		return
	}

	e.spotTickerCache = NewBookTickerCache()
	e.futuresTickerCache = NewBookTickerCache()
	e.spotDepthCache = NewDepthCache()
	e.futuresDepthCache = NewDepthCache()

	spotSymbols := make([]string, 0, len(targets))
	futuresSymbols := make([]string, 0, len(targets))
	for _, target := range targets {
		spotSymbols = append(spotSymbols, target.Candidate.Pair)
		futuresSymbols = append(futuresSymbols, target.Candidate.Symbol)
	}

	go e.runBookTickerStream(ctx, binanceSpotCombinedWSURL, spotSymbols, e.spotTickerCache)
	go e.runBookTickerStream(ctx, binanceFuturesCombinedWSURL, futuresSymbols, e.futuresTickerCache)
	go e.runPartialDepthStream(ctx, binanceSpotCombinedWSURL, spotSymbols, e.spotDepthCache)
	go e.runPartialDepthStream(ctx, binanceFuturesCombinedWSURL, futuresSymbols, e.futuresDepthCache)
}

func (e *FundingRateSniperEngine) runBookTickerStream(ctx context.Context, baseURL string, symbols []string, cache *BookTickerCache) {
	if len(symbols) == 0 {
		return
	}

	streams := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		lower := strings.ToLower(symbol)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		streams = append(streams, lower+"@bookTicker")
	}

	endpoint := baseURL + strings.Join(streams, "/")
	retryDelay := 200 * time.Millisecond

	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := e.dialWebSocket(endpoint)
		if err != nil {
			e.logger.Printf("WebSocket 连接失败: %v", err)
			if !sleepWithContext(ctx, retryDelay) {
				return
			}
			continue
		}
		stopClose := closeWebSocketOnContextDone(ctx, conn)

		err = e.consumeBookTickerStream(ctx, conn, cache)
		stopClose()
		_ = conn.Close()
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("WebSocket 订阅中断，准备重连: %v", err)
		}

		if !sleepWithContext(ctx, retryDelay) {
			return
		}
	}
}

func (e *FundingRateSniperEngine) runPartialDepthStream(ctx context.Context, baseURL string, symbols []string, cache *DepthCache) {
	if len(symbols) == 0 {
		return
	}

	streams := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		lower := strings.ToLower(symbol)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		streams = append(streams, fmt.Sprintf("%s@depth%d@100ms", lower, e.cfg.StrategyConfig.DepthLevels))
	}

	endpoint := baseURL + strings.Join(streams, "/")
	retryDelay := 200 * time.Millisecond

	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := e.dialWebSocket(endpoint)
		if err != nil {
			e.logger.Printf("深度 WebSocket 连接失败: %v", err)
			if !sleepWithContext(ctx, retryDelay) {
				return
			}
			continue
		}
		stopClose := closeWebSocketOnContextDone(ctx, conn)

		err = e.consumeDepthStream(ctx, conn, cache)
		stopClose()
		_ = conn.Close()
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("深度 WebSocket 订阅中断，准备重连: %v", err)
		}

		if !sleepWithContext(ctx, retryDelay) {
			return
		}
	}
}

func (e *FundingRateSniperEngine) dialWebSocket(endpoint string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	if strings.TrimSpace(e.client.proxyURL) != "" {
		proxyURL, err := url.Parse(e.client.proxyURL)
		if err != nil {
			return nil, fmt.Errorf("解析 WebSocket 代理失败: %w", err)
		}
		dialer.Proxy = http.ProxyURL(proxyURL)
	}

	conn, _, err := dialer.Dial(endpoint, nil)
	if err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	return conn, nil
}

func (e *FundingRateSniperEngine) consumeBookTickerStream(ctx context.Context, conn *websocket.Conn, cache *BookTickerCache) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var envelope struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("解析 WebSocket 包装消息失败: %w", err)
		}

		var ticker BookTicker
		if err := json.Unmarshal(envelope.Data, &ticker); err != nil {
			return fmt.Errorf("解析 bookTicker 消息失败: %w", err)
		}

		cache.Update(ticker)
	}
}

func (e *FundingRateSniperEngine) consumeDepthStream(ctx context.Context, conn *websocket.Conn, cache *DepthCache) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var envelope struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("解析深度 WebSocket 包装消息失败: %w", err)
		}

		var depth struct {
			LastUpdateID int64      `json:"lastUpdateId"`
			Bids         [][]string `json:"bids"`
			Asks         [][]string `json:"asks"`
		}
		if err := json.Unmarshal(envelope.Data, &depth); err != nil {
			return fmt.Errorf("解析 depth 消息失败: %w", err)
		}

		symbol := streamSymbol(envelope.Stream)
		if symbol == "" {
			continue
		}

		cache.Update(symbol, ParsedOrderBookDepth{
			LastUpdateID: depth.LastUpdateID,
			Bids:         parseDepthLevels(depth.Bids),
			Asks:         parseDepthLevels(depth.Asks),
		})
	}
}

func streamSymbol(stream string) string {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return ""
	}
	parts := strings.SplitN(stream, "@", 2)
	if len(parts) == 0 {
		return ""
	}
	return strings.ToUpper(parts[0])
}

func parseDepthLevels(levels [][]string) []DepthLevel {
	parsed := make([]DepthLevel, 0, len(levels))
	for _, level := range levels {
		if len(level) < 2 {
			continue
		}
		price := parseFloat(level[0])
		qty := parseFloat(level[1])
		if price <= 0 || qty <= 0 {
			continue
		}
		parsed = append(parsed, DepthLevel{
			Price:    price,
			Quantity: qty,
		})
	}
	return parsed
}

func sleepWithContext(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
