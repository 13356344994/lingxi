package gate

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

// marketdata.go 实现 Gate v4 公共行情(无需签名),全部经本包自建 transport 直连 REST。

const (
	defaultFundingCap = 0.02 // Gate 未单独暴露逐币 cap 时的保守占位
	instCacheTTL      = 10 * time.Minute
)

type instCacheEntry struct {
	inst      venue.Instrument
	expiresAt time.Time
}

func secToTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// ServerTime 拉 /api/v4/spot/time(server_time Unix 秒)。
func (a *Adapter) ServerTime(ctx context.Context) (time.Time, error) {
	var res struct {
		ServerTime int64 `json:"server_time"`
	}
	if err := a.t.publicGet(ctx, pathSpotTime, nil, &res); err != nil {
		return time.Time{}, err
	}
	return secToTime(res.ServerTime), nil
}

type gateFuturesContract struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	QuantoMultiplier string `json:"quanto_multiplier"`
	OrderPriceRound  string `json:"order_price_round"`
	OrderSizeMin     int64  `json:"order_size_min"`
	OrderSizeRound   int64  `json:"order_size_round"`
	MarkPrice        string `json:"mark_price"`
	IndexPrice       string `json:"index_price"`
	LastPrice        string `json:"last_price"`
	FundingRate      string `json:"funding_rate"`
	FundingNextApply int64  `json:"funding_next_apply"`
	FundingInterval  int64  `json:"funding_interval"`
	InDelisting      bool   `json:"in_delisting"`
	PositionSize     int64  `json:"position_size"`
	TradeSize        int64  `json:"trade_size"`
}

type gateSpotPair struct {
	ID              string `json:"id"`
	Base            string `json:"base"`
	Quote           string `json:"quote"`
	MinBaseAmount   string `json:"min_base_amount"`
	MinQuoteAmount  string `json:"min_quote_amount"`
	AmountPrecision int    `json:"amount_precision"`
	Precision       int    `json:"precision"`
	TradeStatus     string `json:"trade_status"`
}

// FetchInstruments 拉 USDT 永续(contracts)+ 现货(currency_pairs)元数据并翻译为中性 Instrument。
func (a *Adapter) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	out := make([]venue.Instrument, 0, 1024)
	var contracts []gateFuturesContract
	if err := a.t.publicGet(ctx, pathFuturesContracts, nil, &contracts); err != nil {
		return nil, err
	}
	for _, c := range contracts {
		if !strings.HasSuffix(strings.ToUpper(c.Name), "_USDT") {
			continue
		}
		canonical, _ := a.ToCanonical(c.Name, venue.MarketPerp)
		mult := parseFloat(c.QuantoMultiplier)
		if mult <= 0 {
			mult = 1
		}
		stepContracts := float64(c.OrderSizeRound)
		if stepContracts <= 0 {
			stepContracts = 1
		}
		rawStatus := "tradable"
		if c.InDelisting {
			rawStatus = "in_delisting"
		}
		out = append(out, venue.Instrument{
			Symbol:           canonical,
			Market:           venue.MarketPerp,
			BaseAsset:        strings.ToUpper(strings.TrimSuffix(c.Name, "_USDT")),
			QuoteAsset:       "USDT",
			TickSize:         parseFloat(c.OrderPriceRound),
			StepSize:         stepContracts,
			MinNotional:      0,
			ContractSize:     mult,
			QtyUnit:          "contract",
			RawTradingStatus: rawStatus,
			NonTrading:       c.InDelisting,
		})
	}
	var pairs []gateSpotPair
	if err := a.t.publicGet(ctx, pathSpotCurrencyPairs, nil, &pairs); err == nil {
		for _, p := range pairs {
			if !strings.EqualFold(p.Quote, "USDT") {
				continue
			}
			canonical, _ := a.ToCanonical(p.ID, venue.MarketSpot)
			step := math.Pow10(-p.AmountPrecision)
			if step <= 0 {
				step = parseFloat(p.MinBaseAmount)
			}
			out = append(out, venue.Instrument{
				Symbol:           canonical,
				Market:           venue.MarketSpot,
				BaseAsset:        strings.ToUpper(p.Base),
				QuoteAsset:       strings.ToUpper(p.Quote),
				TickSize:         math.Pow10(-p.Precision),
				StepSize:         step,
				MinNotional:      parseFloat(p.MinQuoteAmount),
				ContractSize:     1,
				QtyUnit:          "coin",
				RawTradingStatus: p.TradeStatus,
				NonTrading:       exchange.ExplicitNonTradingGateSpotStatus(p.TradeStatus),
			})
		}
	}
	return out, nil
}

// FetchFundingSnapshot 单端点拉全量 USDT 永续资金费(/futures/usdt/contracts)。
// in_delisting=true 跳过;单币解析失败 best-effort 跳过,不让坏字段毁掉全量扫描。
func (a *Adapter) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	var contracts []gateFuturesContract
	if err := a.t.publicGet(ctx, pathFuturesContracts, nil, &contracts); err != nil {
		return nil, err
	}
	out := make([]venue.FundingSnapshot, 0, len(contracts))
	skipped := 0
	for _, c := range contracts {
		if c.InDelisting {
			continue
		}
		if !strings.HasSuffix(strings.ToUpper(c.Name), "_USDT") {
			continue
		}
		snap, err := parseFundingSnapshot(a, c)
		if err != nil {
			skipped++
			continue
		}
		out = append(out, snap)
	}
	if len(out) == 0 && skipped > len(contracts)/2 && len(contracts) > 0 {
		return nil, fmt.Errorf("gate: funding snapshot 解析失败过半(成功 0 / 跳过 %d / 共 %d)", skipped, len(contracts))
	}
	return out, nil
}

func parseFundingSnapshot(a *Adapter, c gateFuturesContract) (venue.FundingSnapshot, error) {
	canonical, err := a.ToCanonical(c.Name, venue.MarketPerp)
	if err != nil {
		return venue.FundingSnapshot{}, err
	}
	intervalHours := 8
	if c.FundingInterval > 0 {
		intervalHours = int(c.FundingInterval / 3600)
		if intervalHours <= 0 {
			intervalHours = 8
		}
	}
	rate := parseFloat(c.FundingRate)
	if c.Name == "" {
		return venue.FundingSnapshot{}, fmt.Errorf("gate: 空 contract 名")
	}
	return venue.FundingSnapshot{
		Symbol:          canonical,
		MarkPrice:       parseFloat(c.MarkPrice),
		IndexPrice:      parseFloat(c.IndexPrice),
		LastFundingRate: rate,
		NextFundingTime: secToTime(c.FundingNextApply),
		IntervalHours:   intervalHours,
		RateCap:         defaultFundingCap,
		RateFloor:       -defaultFundingCap,
	}, nil
}

// gateOrderBook 是**合约** order_book 格式:档位为对象 {p,s}(s=张数整数)。
type gateOrderBook struct {
	ID   int64 `json:"id"`
	Asks []struct {
		P string `json:"p"`
		S int64  `json:"s"`
	} `json:"asks"`
	Bids []struct {
		P string `json:"p"`
		S int64  `json:"s"`
	} `json:"bids"`
}

// gateSpotOrderBook 是**现货** order_book 格式:档位为数组 ["价","量"](量=币数量字符串)。
// 踩坑实录:现货与合约格式不同,共用 gateOrderBook 解析现货必报 "cannot unmarshal array"(实弹自检发现)。
type gateSpotOrderBook struct {
	ID   int64       `json:"id"`
	Asks [][2]string `json:"asks"`
	Bids [][2]string `json:"bids"`
}

// bookLevel 是两种格式归一后的档位(价格、币数量)。
type bookLevel struct {
	price float64
	qty   float64
}

func (ob gateOrderBook) levels(mult float64) (asks, bids []bookLevel) {
	for _, lv := range ob.Asks {
		asks = append(asks, bookLevel{price: parseFloat(lv.P), qty: float64(lv.S) * mult})
	}
	for _, lv := range ob.Bids {
		bids = append(bids, bookLevel{price: parseFloat(lv.P), qty: float64(lv.S) * mult})
	}
	return
}

func (ob gateSpotOrderBook) levels() (asks, bids []bookLevel) {
	for _, lv := range ob.Asks {
		asks = append(asks, bookLevel{price: parseFloat(lv[0]), qty: parseFloat(lv[1])})
	}
	for _, lv := range ob.Bids {
		bids = append(bids, bookLevel{price: parseFloat(lv[0]), qty: parseFloat(lv[1])})
	}
	return
}

// fetchOrderBookLevels 按市场选择正确格式拉档位并归一(合约张数已×multiplier)。
func (a *Adapter) fetchOrderBookLevels(ctx context.Context, m venue.Market, raw string, limit int, mult float64) (asks, bids []bookLevel, err error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	if m == venue.MarketPerp {
		q.Set("contract", raw)
		var ob gateOrderBook
		if err := a.t.publicGet(ctx, pathFuturesOrderBook, q, &ob); err != nil {
			return nil, nil, err
		}
		asks, bids = ob.levels(mult)
		return asks, bids, nil
	}
	q.Set("currency_pair", raw)
	var ob gateSpotOrderBook
	if err := a.t.publicGet(ctx, pathSpotOrderBook, q, &ob); err != nil {
		return nil, nil, err
	}
	asks, bids = ob.levels()
	return asks, bids, nil
}

// FetchBookTicker 用 order_book limit=1 取最优买卖一档;合约张数×quanto_multiplier 换算为币数量。
func (a *Adapter) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.BookTicker{}, err
	}
	mult := 1.0
	if m == venue.MarketPerp {
		if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
			mult = inst.ContractSize
		}
	}
	asks, bids, err := a.fetchOrderBookLevels(ctx, m, raw, 1, mult)
	if err != nil {
		return venue.BookTicker{}, err
	}
	bt := venue.BookTicker{Symbol: s, Market: m}
	if len(bids) > 0 {
		bt.BidPrice = bids[0].price
		bt.BidQty = bids[0].qty
	}
	if len(asks) > 0 {
		bt.AskPrice = asks[0].price
		bt.AskQty = asks[0].qty
	}
	return bt, nil
}

// FetchDepth 用 order_book 取多档盘口;合约张→币换算。
func (a *Adapter) FetchDepth(ctx context.Context, m venue.Market, s venue.Symbol, limit int) (venue.Depth, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Depth{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	mult := 1.0
	if m == venue.MarketPerp {
		if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
			mult = inst.ContractSize
		}
	}
	asks, bids, err := a.fetchOrderBookLevels(ctx, m, raw, limit, mult)
	if err != nil {
		return venue.Depth{}, err
	}
	depth := venue.Depth{Symbol: s, Market: m}
	for _, lv := range bids {
		depth.Bids = append(depth.Bids, venue.DepthLevel{Price: lv.price, Qty: lv.qty})
	}
	for _, lv := range asks {
		depth.Asks = append(depth.Asks, venue.DepthLevel{Price: lv.price, Qty: lv.qty})
	}
	return depth, nil
}

// FetchOpenInterest 用单合约 contracts 响应的 position_size(张) × quanto_multiplier 换算为币数量 OI。
func (a *Adapter) FetchOpenInterest(ctx context.Context, s venue.Symbol) (float64, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return 0, err
	}
	path := pathFuturesContracts + "/" + url.PathEscape(raw)
	var c gateFuturesContract
	if err := a.t.publicGet(ctx, path, nil, &c); err != nil {
		return 0, err
	}
	// position_size 为全市场持仓张数(官方 contracts 字段);比 trade_size(24h 成交量)更可靠作 OI 代理。
	if c.PositionSize == 0 {
		return 0, venue.ErrUnsupported // Gate 单合约响应无可靠 OI 字段时不上报
	}
	mult := parseFloat(c.QuantoMultiplier)
	if mult <= 0 {
		if inst, ierr := a.lookupInstrument(ctx, venue.MarketPerp, s); ierr == nil && inst.ContractSize > 0 {
			mult = inst.ContractSize
		} else {
			return 0, venue.ErrUnsupported
		}
	}
	return float64(c.PositionSize) * mult, nil
}

type gateCandlestick struct {
	T   float64 `json:"t"`
	V   int64   `json:"v"`
	O   string  `json:"o"`
	H   string  `json:"h"`
	L   string  `json:"l"`
	C   string  `json:"c"`
	Sum string  `json:"sum"`
}

// FetchCandles 用 /api/v4/futures/usdt/candlesticks 拉 USDT 永续历史 K 线。
// bar 为 Gate interval(如 "1m","5m","1h");limit 上限 2000。
// Gate 返回 oldest-first;合约成交量 v(张) × quanto_multiplier 换算为币数量。
func (a *Adapter) FetchCandles(ctx context.Context, m venue.Market, s venue.Symbol, bar string, limit int) ([]venue.Candle, error) {
	if m != venue.MarketPerp {
		return nil, fmt.Errorf("gate: FetchCandles 仅支持永续")
	}
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return nil, err
	}
	bar = strings.TrimSpace(bar)
	if bar == "" {
		return nil, fmt.Errorf("gate: 空 bar")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	mult := 1.0
	if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
		mult = inst.ContractSize
	}
	q := url.Values{}
	q.Set("contract", raw)
	q.Set("interval", bar)
	q.Set("limit", strconv.Itoa(limit))
	var rows []gateCandlestick
	if err := a.t.publicGet(ctx, pathFuturesCandlesticks, q, &rows); err != nil {
		return nil, err
	}
	return parseGateCandlesticks(rows, mult), nil
}

// parseGateCandlesticks 把 Gate candlesticks 解析为 oldest-first 的 []venue.Candle(纯解析,可单测)。
func parseGateCandlesticks(rows []gateCandlestick, mult float64) []venue.Candle {
	if mult <= 0 {
		mult = 1
	}
	out := make([]venue.Candle, 0, len(rows))
	for _, row := range rows {
		if row.T <= 0 {
			continue
		}
		out = append(out, venue.Candle{
			OpenTime: secToTime(int64(row.T)),
			Open:     parseFloat(row.O),
			High:     parseFloat(row.H),
			Low:      parseFloat(row.L),
			Close:    parseFloat(row.C),
			Volume:   float64(row.V) * mult,
		})
	}
	return out
}

// FetchRecentQuoteVolume 用 tickers 端点取 24h USDT 报价量。
// spot: quote_volume(计价货币=USDT);futures: volume_24h_settle(USDT 结算货币成交额)。
// 若 settle 缺失,回退 volume_24h_quote,再回退 volume_24h_base×last。
func (a *Adapter) FetchRecentQuoteVolume(ctx context.Context, m venue.Market, s venue.Symbol, window time.Duration) (float64, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return 0, err
	}
	if m == venue.MarketPerp {
		q := url.Values{}
		q.Set("contract", raw)
		var rows []struct {
			Volume24hSettle string `json:"volume_24h_settle"`
			Volume24hQuote  string `json:"volume_24h_quote"`
			Volume24hBase   string `json:"volume_24h_base"`
			Last            string `json:"last"`
		}
		if err := a.t.publicGet(ctx, pathFuturesTickers, q, &rows); err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			return 0, fmt.Errorf("gate: futures ticker 空响应 contract=%s", raw)
		}
		row := rows[0]
		vol := parseFloat(row.Volume24hSettle)
		if vol <= 0 {
			vol = parseFloat(row.Volume24hQuote)
		}
		if vol <= 0 {
			vol = parseFloat(row.Volume24hBase) * parseFloat(row.Last)
		}
		return vol, nil
	}
	q := url.Values{}
	q.Set("currency_pair", raw)
	var rows []struct {
		QuoteVolume string `json:"quote_volume"`
	}
	if err := a.t.publicGet(ctx, pathSpotTickers, q, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("gate: spot ticker 空响应 currency_pair=%s", raw)
	}
	return parseFloat(rows[0].QuoteVolume), nil
}

// lookupInstrument 拉单条合约/现货元数据(PlaceOrder/盘口换算用),带进程级 TTL 缓存。
func (a *Adapter) lookupInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	key := m.String() + "/" + string(s)
	a.instMu.RLock()
	if e, ok := a.instCache[key]; ok && a.t.nowFn().Before(e.expiresAt) {
		a.instMu.RUnlock()
		return e.inst, nil
	}
	a.instMu.RUnlock()

	inst, err := a.fetchInstrument(ctx, m, s)
	if err != nil {
		return venue.Instrument{}, err
	}
	a.instMu.Lock()
	if a.instCache == nil {
		a.instCache = make(map[string]instCacheEntry)
	}
	a.instCache[key] = instCacheEntry{inst: inst, expiresAt: a.t.nowFn().Add(instCacheTTL)}
	a.instMu.Unlock()
	return inst, nil
}

func (a *Adapter) fetchInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Instrument{}, err
	}
	if m == venue.MarketPerp {
		path := pathFuturesContracts + "/" + url.PathEscape(raw)
		var c gateFuturesContract
		if err := a.t.publicGet(ctx, path, nil, &c); err != nil {
			return venue.Instrument{}, err
		}
		canonical, _ := a.ToCanonical(c.Name, venue.MarketPerp)
		mult := parseFloat(c.QuantoMultiplier)
		if mult <= 0 {
			mult = 1
		}
		step := float64(c.OrderSizeRound)
		if step <= 0 {
			step = 1
		}
		rawStatus := "tradable"
		if c.InDelisting {
			rawStatus = "in_delisting"
		}
		return venue.Instrument{
			Symbol:           canonical,
			Market:           venue.MarketPerp,
			BaseAsset:        strings.ToUpper(strings.TrimSuffix(c.Name, "_USDT")),
			QuoteAsset:       "USDT",
			TickSize:         parseFloat(c.OrderPriceRound),
			StepSize:         step,
			ContractSize:     mult,
			QtyUnit:          "contract",
			RawTradingStatus: rawStatus,
			NonTrading:       c.InDelisting,
		}, nil
	}
	q := url.Values{}
	q.Set("currency_pair", raw)
	var pairs []gateSpotPair
	if err := a.t.publicGet(ctx, pathSpotCurrencyPairs, q, &pairs); err != nil {
		return venue.Instrument{}, err
	}
	if len(pairs) == 0 {
		return venue.Instrument{}, fmt.Errorf("gate: 找不到现货 %s", raw)
	}
	p := pairs[0]
	canonical, _ := a.ToCanonical(p.ID, venue.MarketSpot)
	step := math.Pow10(-p.AmountPrecision)
	return venue.Instrument{
		Symbol:           canonical,
		Market:           venue.MarketSpot,
		BaseAsset:        strings.ToUpper(p.Base),
		QuoteAsset:       strings.ToUpper(p.Quote),
		TickSize:         math.Pow10(-p.Precision),
		StepSize:         step,
		ContractSize:     1,
		QtyUnit:          "coin",
		RawTradingStatus: p.TradeStatus,
		NonTrading:       exchange.ExplicitNonTradingGateSpotStatus(p.TradeStatus),
	}, nil
}

// refreshInstrumentCache 供测试/批量预热用(非接口方法)。
func (a *Adapter) refreshInstrumentCache(ctx context.Context) error {
	insts, err := a.FetchInstruments(ctx)
	if err != nil {
		return err
	}
	a.instMu.Lock()
	defer a.instMu.Unlock()
	if a.instCache == nil {
		a.instCache = make(map[string]instCacheEntry)
	}
	exp := a.t.nowFn().Add(instCacheTTL)
	for _, inst := range insts {
		key := inst.Market.String() + "/" + string(inst.Symbol)
		a.instCache[key] = instCacheEntry{inst: inst, expiresAt: exp}
	}
	return nil
}
