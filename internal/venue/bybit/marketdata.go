package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// marketdata.go 实现 Bybit v5 公共行情(无需签名),全部经本包自建 transport 直连 REST。
// 这是 Stage 1 活体验证的主体:可对真实 Bybit API 拉到 instruments/funding/盘口/OI/成交额/服务器时间。

// defaultFundingCap 是 Bybit 未单独暴露逐币资金费上下限时的保守占位(撞限判定用)。
// TODO(Stage3): 以 instruments/risk 接口的真实 cap 替换。
const defaultFundingCap = 0.02

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func msStringToTime(s string) time.Time {
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return msToTime(ms)
}

// ServerTime 拉 /v5/market/time(result.timeNano 纳秒字符串)。
func (a *Adapter) ServerTime(ctx context.Context) (time.Time, error) {
	var res struct {
		TimeSecond string `json:"timeSecond"`
		TimeNano   string `json:"timeNano"`
	}
	if err := a.t.publicGet(ctx, pathServerTime, nil, &res); err != nil {
		return time.Time{}, err
	}
	if nano, err := strconv.ParseInt(strings.TrimSpace(res.TimeNano), 10, 64); err == nil && nano > 0 {
		return time.Unix(0, nano).UTC(), nil
	}
	if sec, err := strconv.ParseInt(strings.TrimSpace(res.TimeSecond), 10, 64); err == nil && sec > 0 {
		return time.Unix(sec, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("bybit: 服务器时间解析失败")
}

// bybitInstrument 是 instruments-info 单条(linear/spot 共用关心字段)。
type bybitInstrument struct {
	Symbol          string `json:"symbol"`
	ContractType    string `json:"contractType"`
	BaseCoin        string `json:"baseCoin"`
	QuoteCoin       string `json:"quoteCoin"`
	Status          string `json:"status"`
	FundingInterval int    `json:"fundingInterval"` // 分钟(linear)
	PriceFilter     struct {
		TickSize string `json:"tickSize"`
	} `json:"priceFilter"`
	LotSizeFilter struct {
		QtyStep        string `json:"qtyStep"`
		BasePrecision  string `json:"basePrecision"`
		MinOrderQty    string `json:"minOrderQty"`
		MinNotionalVal string `json:"minNotionalValue"`
		MinOrderAmt    string `json:"minOrderAmt"`
	} `json:"lotSizeFilter"`
}

// FetchInstruments 拉 linear(永续)+ spot(现货)元数据并翻译为中性 Instrument(带游标分页)。
func (a *Adapter) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	out := make([]venue.Instrument, 0, 1024)
	perp, err := a.fetchInstrumentCategory(ctx, categoryLinear)
	if err != nil {
		return nil, err
	}
	for _, it := range perp {
		if it.ContractType != "" && it.ContractType != "LinearPerpetual" {
			continue // 只要 USDT 本位永续
		}
		canonical, _ := a.ToCanonical(it.Symbol, venue.MarketPerp)
		out = append(out, venue.Instrument{
			Symbol:       canonical,
			Market:       venue.MarketPerp,
			BaseAsset:    strings.ToUpper(it.BaseCoin),
			QuoteAsset:   strings.ToUpper(it.QuoteCoin),
			TickSize:     parseFloat(it.PriceFilter.TickSize),
			StepSize:     parseFloat(it.LotSizeFilter.QtyStep),
			MinNotional:  parseFloat(it.LotSizeFilter.MinNotionalVal),
			ContractSize: 1, // USDT 永续按币数量
			QtyUnit:      "coin",
		})
	}
	// 现货失败不致命(Stage 1 验证以永续为主),仅记录性跳过。
	if spot, spotErr := a.fetchInstrumentCategory(ctx, "spot"); spotErr == nil {
		for _, it := range spot {
			canonical, _ := a.ToCanonical(it.Symbol, venue.MarketSpot)
			minNotional := parseFloat(it.LotSizeFilter.MinNotionalVal)
			if minNotional == 0 {
				minNotional = parseFloat(it.LotSizeFilter.MinOrderAmt)
			}
			out = append(out, venue.Instrument{
				Symbol:       canonical,
				Market:       venue.MarketSpot,
				BaseAsset:    strings.ToUpper(it.BaseCoin),
				QuoteAsset:   strings.ToUpper(it.QuoteCoin),
				TickSize:     parseFloat(it.PriceFilter.TickSize),
				StepSize:     parseFloat(it.LotSizeFilter.BasePrecision),
				MinNotional:  minNotional,
				ContractSize: 1,
				QtyUnit:      "coin",
			})
		}
	}
	return out, nil
}

// fetchInstrumentCategory 拉某 category 的 instruments-info 全量(游标翻页,防漏币)。
func (a *Adapter) fetchInstrumentCategory(ctx context.Context, category string) ([]bybitInstrument, error) {
	out := make([]bybitInstrument, 0, 512)
	cursor := ""
	for page := 0; page < 50; page++ { // 上限 50 页兜底,防异常游标导致死循环
		q := url.Values{}
		q.Set("category", category)
		q.Set("limit", "1000")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			List           []bybitInstrument `json:"list"`
			NextPageCursor string            `json:"nextPageCursor"`
		}
		if err := a.t.publicGet(ctx, pathInstrumentsInfo, q, &res); err != nil {
			return nil, err
		}
		out = append(out, res.List...)
		if res.NextPageCursor == "" || res.NextPageCursor == cursor {
			break
		}
		cursor = res.NextPageCursor
	}
	return out, nil
}

// FetchFundingSnapshot 拉 linear tickers,映射为中性资金费快照。
// 注:Bybit tickers.fundingRate 为「下次结算预测费率」;EstimatedSettlePrice 公共接口未提供(置 0);
// 逐币 cap/interval 公共接口不直供,先用默认占位(TODO Stage3 以 instruments/risk 富化)。
func (a *Adapter) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	q := url.Values{}
	q.Set("category", categoryLinear)
	var res struct {
		List []struct {
			Symbol          string `json:"symbol"`
			LastPrice       string `json:"lastPrice"`
			IndexPrice      string `json:"indexPrice"`
			MarkPrice       string `json:"markPrice"`
			FundingRate     string `json:"fundingRate"`
			NextFundingTime string `json:"nextFundingTime"`
		} `json:"list"`
	}
	if err := a.t.publicGet(ctx, pathTickers, q, &res); err != nil {
		return nil, err
	}
	// 逐币资金费周期:best-effort 自 instruments 富化(失败则默认 8h)。
	intervalBySym := a.fundingIntervalMap(ctx)
	out := make([]venue.FundingSnapshot, 0, len(res.List))
	for _, it := range res.List {
		canonical, _ := a.ToCanonical(it.Symbol, venue.MarketPerp)
		interval := 8
		if h, ok := intervalBySym[strings.ToUpper(it.Symbol)]; ok && h > 0 {
			interval = h
		}
		out = append(out, venue.FundingSnapshot{
			Symbol:          canonical,
			MarkPrice:       parseFloat(it.MarkPrice),
			IndexPrice:      parseFloat(it.IndexPrice),
			LastFundingRate: parseFloat(it.FundingRate),
			NextFundingTime: msStringToTime(it.NextFundingTime),
			IntervalHours:   interval,
			RateCap:         defaultFundingCap,
			RateFloor:       -defaultFundingCap,
		})
	}
	return out, nil
}

// fundingIntervalMap 尽力而为返回 symbol→资金费周期(小时)。失败返回空表(调用方回退默认)。
func (a *Adapter) fundingIntervalMap(ctx context.Context) map[string]int {
	m := make(map[string]int)
	insts, err := a.fetchInstrumentCategory(ctx, categoryLinear)
	if err != nil {
		return m
	}
	for _, it := range insts {
		if it.FundingInterval > 0 {
			m[strings.ToUpper(it.Symbol)] = it.FundingInterval / 60
		}
	}
	return m
}

// FetchFundingRateHistory 拉某永续标的最近 limit 条历史资金费率(新→旧)。
// 公共接口,无需签名;用于估计费率持续性(多结算期望值开仓门槛)。
func (a *Adapter) FetchFundingRateHistory(ctx context.Context, s venue.Symbol, limit int) ([]float64, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("limit", strconv.Itoa(limit))
	var res struct {
		List []struct {
			FundingRate string `json:"fundingRate"`
		} `json:"list"`
	}
	if err := a.t.publicGet(ctx, pathFundingHistory, q, &res); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(res.List))
	for _, it := range res.List {
		out = append(out, parseFloat(it.FundingRate))
	}
	return out, nil
}

// FetchBookTicker 用 orderbook limit=1 取最优买卖一档(linear)。
func (a *Adapter) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	raw, _ := a.ToVenue(s, m)
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("limit", "1")
	var res bybitOrderbook
	if err := a.t.publicGet(ctx, pathOrderbook, q, &res); err != nil {
		return venue.BookTicker{}, err
	}
	bt := venue.BookTicker{Symbol: s, Market: m}
	if len(res.Bids) > 0 && len(res.Bids[0]) >= 2 {
		bt.BidPrice = parseFloat(res.Bids[0][0])
		bt.BidQty = parseFloat(res.Bids[0][1])
	}
	if len(res.Asks) > 0 && len(res.Asks[0]) >= 2 {
		bt.AskPrice = parseFloat(res.Asks[0][0])
		bt.AskQty = parseFloat(res.Asks[0][1])
	}
	return bt, nil
}

// FetchDepth 用 orderbook 取多档盘口。
func (a *Adapter) FetchDepth(ctx context.Context, m venue.Market, s venue.Symbol, limit int) (venue.Depth, error) {
	raw, _ := a.ToVenue(s, m)
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("limit", strconv.Itoa(limit))
	var res bybitOrderbook
	if err := a.t.publicGet(ctx, pathOrderbook, q, &res); err != nil {
		return venue.Depth{}, err
	}
	depth := venue.Depth{Symbol: s, Market: m}
	for _, lv := range res.Bids {
		if len(lv) >= 2 {
			depth.Bids = append(depth.Bids, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: parseFloat(lv[1])})
		}
	}
	for _, lv := range res.Asks {
		if len(lv) >= 2 {
			depth.Asks = append(depth.Asks, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: parseFloat(lv[1])})
		}
	}
	return depth, nil
}

// bybitOrderbook 是 /v5/market/orderbook 的 result(b=买,a=卖,元素 [price,size])。
type bybitOrderbook struct {
	Symbol string     `json:"s"`
	Bids   [][]string `json:"b"`
	Asks   [][]string `json:"a"`
	TS     int64      `json:"ts"`
}

// FetchOpenInterest 取最近一条持仓量(base coin 计)。
func (a *Adapter) FetchOpenInterest(ctx context.Context, s venue.Symbol) (float64, error) {
	raw, _ := a.ToVenue(s, venue.MarketPerp)
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("intervalTime", "5min")
	q.Set("limit", "1")
	var res struct {
		List []struct {
			OpenInterest string `json:"openInterest"`
			Timestamp    string `json:"timestamp"`
		} `json:"list"`
	}
	if err := a.t.publicGet(ctx, pathOpenInterest, q, &res); err != nil {
		return 0, err
	}
	if len(res.List) == 0 {
		return 0, nil
	}
	return parseFloat(res.List[0].OpenInterest), nil
}

// FetchRecentQuoteVolume 用 kline.turnover(成交额=quote 计)在 window 内求和。
func (a *Adapter) FetchRecentQuoteVolume(ctx context.Context, m venue.Market, s venue.Symbol, window time.Duration) (float64, error) {
	raw, _ := a.ToVenue(s, m)
	if window <= 0 {
		window = time.Hour
	}
	// 选取一个使蜡烛数 ≤ 200 的周期:短窗用 1min,长窗用 60min。
	interval := "1"
	stepMin := 1.0
	if window > 3*time.Hour {
		interval = "60"
		stepMin = 60
	}
	count := int(window.Minutes()/stepMin) + 1
	if count > 200 {
		count = 200
	}
	if count < 1 {
		count = 1
	}
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(count))
	var res struct {
		List [][]string `json:"list"` // [start,open,high,low,close,volume,turnover]
	}
	if err := a.t.publicGet(ctx, pathKline, q, &res); err != nil {
		return 0, err
	}
	sum := 0.0
	for _, row := range res.List {
		if len(row) >= 7 {
			sum += parseFloat(row[6])
		}
	}
	return sum, nil
}
