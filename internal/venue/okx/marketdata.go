package okx

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

// marketdata.go 实现 OKX v5 公共行情(无需签名),全部经本包自建 transport 直连 REST。

const (
	defaultFundingCap       = 0.02 // OKX 未单独暴露逐币 cap 时的保守占位
	fundingFetchConcurrency = 8    // 全量 funding-rate 并发上限(兜底;实际速率由全局 pacing 控制)
	// funding-rate 端点限频 20 req/2s(=10/s)。全局 pacing 取 130ms ≈ 7.7 req/s,留安全余量,
	// 避免靠 transport 的 50011 重试硬扛(既慢又脆)。
	fundingFetchInterval = 130 * time.Millisecond
)

type tickerInfo struct {
	mark, index, last float64
}

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

// ServerTime 拉 /api/v5/public/time(data[0].ts 毫秒字符串)。
func (a *Adapter) ServerTime(ctx context.Context) (time.Time, error) {
	var rows []struct {
		TS string `json:"ts"`
	}
	if err := a.t.publicGet(ctx, pathServerTime, nil, &rows); err != nil {
		return time.Time{}, err
	}
	if len(rows) == 0 {
		return time.Time{}, fmt.Errorf("okx: 服务器时间响应为空")
	}
	return msStringToTime(rows[0].TS), nil
}

type okxInstrument struct {
	InstID    string `json:"instId"`
	InstType  string `json:"instType"`
	BaseCcy   string `json:"baseCcy"`   // 仅 SPOT 有值
	QuoteCcy  string `json:"quoteCcy"`  // 仅 SPOT 有值;SWAP 为空(踩坑实录:曾用它过滤 USDT 永续 → 全军覆没)
	SettleCcy string `json:"settleCcy"` // SWAP 结算币;USDT 本位过滤用它
	State     string `json:"state"`
	TickSz    string `json:"tickSz"`
	LotSz     string `json:"lotSz"`
	MinSz     string `json:"minSz"`
	CtVal     string `json:"ctVal"`
	CtValCcy  string `json:"ctValCcy"` // SWAP 合约面值币种(=基础币)
}

// FetchInstruments 拉 SWAP(永续)+ SPOT(现货)元数据并翻译为中性 Instrument。
func (a *Adapter) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	out := make([]venue.Instrument, 0, 1024)
	swap, err := a.fetchInstrumentsByType(ctx, instTypeSwap)
	if err != nil {
		return nil, err
	}
	for _, it := range swap {
		// SWAP 行情 quoteCcy 为空(现货专属字段),USDT 本位必须看 settleCcy。
		if !strings.EqualFold(it.SettleCcy, "USDT") {
			continue // 只要 USDT 本位永续
		}
		canonical, _ := a.ToCanonical(it.InstID, venue.MarketPerp)
		ctVal := parseFloat(it.CtVal)
		if ctVal <= 0 {
			ctVal = 1
		}
		// SWAP 的 baseCcy 同样为空,基础币用 ctValCcy(合约面值币种)。
		base := strings.ToUpper(it.CtValCcy)
		if base == "" || base == "USD" {
			if i := strings.IndexByte(it.InstID, '-'); i > 0 {
				base = strings.ToUpper(it.InstID[:i])
			}
		}
		out = append(out, venue.Instrument{
			Symbol:           canonical,
			Market:           venue.MarketPerp,
			BaseAsset:        base,
			QuoteAsset:       "USDT",
			TickSize:         parseFloat(it.TickSz),
			StepSize:         parseFloat(it.LotSz),
			MinNotional:      0,
			ContractSize:     ctVal,
			QtyUnit:          "contract",
			RawTradingStatus: it.State,
			NonTrading:       exchange.ExplicitNonTradingOKXState(it.State),
		})
	}
	// 现货失败不致命,仅跳过。
	if spot, spotErr := a.fetchInstrumentsByType(ctx, instTypeSpot); spotErr == nil {
		for _, it := range spot {
			canonical, _ := a.ToCanonical(it.InstID, venue.MarketSpot)
			out = append(out, venue.Instrument{
				Symbol:           canonical,
				Market:           venue.MarketSpot,
				BaseAsset:        strings.ToUpper(it.BaseCcy),
				QuoteAsset:       strings.ToUpper(it.QuoteCcy),
				TickSize:         parseFloat(it.TickSz),
				StepSize:         parseFloat(it.LotSz),
				MinNotional:      0,
				ContractSize:     1,
				QtyUnit:          "coin",
				RawTradingStatus: it.State,
				NonTrading:       exchange.ExplicitNonTradingOKXState(it.State),
			})
		}
	}
	return out, nil
}

func (a *Adapter) fetchInstrumentsByType(ctx context.Context, instType string) ([]okxInstrument, error) {
	q := url.Values{}
	q.Set("instType", instType)
	var rows []okxInstrument
	if err := a.t.publicGet(ctx, pathInstruments, q, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// FetchFundingSnapshot 拉全量 USDT 本位 SWAP 资金费快照。
// 流程:先 tickers 拿 mark/index 价,再逐 instId 调 funding-rate(限频 20 req/2s)。
// 全量扫描约 20–30s,可接受(引擎扫描为低频操作)。
func (a *Adapter) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	q := url.Values{}
	q.Set("instType", instTypeSwap)
	var tickers []struct {
		InstID string `json:"instId"`
		MarkPx string `json:"markPx"`
		IdxPx  string `json:"idxPx"`
		Last   string `json:"last"`
	}
	if err := a.t.publicGet(ctx, pathTickers, q, &tickers); err != nil {
		return nil, err
	}

	priceByInst := make(map[string]tickerInfo, len(tickers))
	instIDs := make([]string, 0, len(tickers))
	for _, tk := range tickers {
		if !strings.HasSuffix(strings.ToUpper(tk.InstID), "-USDT-SWAP") {
			continue
		}
		instIDs = append(instIDs, tk.InstID)
		priceByInst[strings.ToUpper(tk.InstID)] = tickerInfo{
			mark:  parseFloat(tk.MarkPx),
			index: parseFloat(tk.IdxPx),
			last:  parseFloat(tk.Last),
		}
	}

	type fundingRow struct {
		instID string
		rate   venue.FundingSnapshot
		err    error
	}
	ch := make(chan fundingRow, len(instIDs))
	sem := make(chan struct{}, fundingFetchConcurrency)
	// 全局 pacing:所有 goroutine 共享同一个 ticker,取到令牌才发请求,把整体速率压在
	// 限频之下;并发信号量只是兜底(防止单请求阻塞时 goroutine 无界堆积)。
	pacer := time.NewTicker(fundingFetchInterval)
	defer pacer.Stop()
	var wg sync.WaitGroup
	for _, instID := range instIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			select {
			case <-ctx.Done():
				ch <- fundingRow{instID: id, err: ctx.Err()}
				return
			case <-pacer.C:
			}
			snap, err := a.fetchFundingRate(ctx, id, priceByInst[strings.ToUpper(id)])
			ch <- fundingRow{instID: id, rate: snap, err: err}
		}(instID)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	// best-effort 容错:全量扫描是发现机会用的,漏掉个别币比整体失败好得多——
	// 250 个币逐个拉,任一失败即整体报错在概率上必然经常失败。单币失败记数跳过,
	// 仅当失败数超过总数一半(系统性故障,如断网/被封)才整体报错。
	out := make([]venue.FundingSnapshot, 0, len(instIDs))
	failed := 0
	var lastErr error
	for row := range ch {
		if row.err != nil {
			failed++
			lastErr = row.err
			continue
		}
		out = append(out, row.rate)
	}
	if failed > len(instIDs)/2 {
		return nil, fmt.Errorf("okx: funding snapshot 失败过半(成功 %d / 失败 %d / 共 %d), 最后错误: %w",
			len(out), failed, len(instIDs), lastErr)
	}
	return out, nil
}

func (a *Adapter) fetchFundingRate(ctx context.Context, instID string, px tickerInfo) (venue.FundingSnapshot, error) {
	q := url.Values{}
	q.Set("instId", instID)
	var rows []struct {
		FundingRate     string `json:"fundingRate"`
		FundingTime     string `json:"fundingTime"`
		NextFundingTime string `json:"nextFundingTime"`
	}
	if err := a.t.publicGet(ctx, pathFundingRate, q, &rows); err != nil {
		return venue.FundingSnapshot{}, err
	}
	if len(rows) == 0 {
		return venue.FundingSnapshot{}, fmt.Errorf("okx: funding-rate 空响应 instId=%s", instID)
	}
	it := rows[0]
	canonical, _ := a.ToCanonical(instID, venue.MarketPerp)
	fundingTime := msStringToTime(it.FundingTime)
	nextFundingTime := msStringToTime(it.NextFundingTime)
	intervalHours := 8
	if !fundingTime.IsZero() && !nextFundingTime.IsZero() {
		delta := nextFundingTime.Sub(fundingTime)
		if delta > 0 {
			intervalHours = int(math.Round(delta.Hours()))
			if intervalHours <= 0 {
				intervalHours = 8
			}
		}
	}
	mark := px.mark
	if mark == 0 {
		mark = px.last
	}
	return venue.FundingSnapshot{
		Symbol:          canonical,
		MarkPrice:       mark,
		IndexPrice:      px.index,
		LastFundingRate: parseFloat(it.FundingRate),
		NextFundingTime: nextFundingTime,
		IntervalHours:   intervalHours,
		RateCap:         defaultFundingCap,
		RateFloor:       -defaultFundingCap,
	}, nil
}

// FetchBookTicker 用 /api/v5/market/ticker 取最优买卖一档。
// SWAP 的 bidSz/askSz 单位是「张」,须乘 ctVal 换算为币数量,与 FetchDepth/FetchOpenInterest 口径一致,
// 否则引擎用它判断流动性会差一个 ctVal 倍数(如 HOME ctVal=100 即差 100 倍)。
func (a *Adapter) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.BookTicker{}, err
	}
	q := url.Values{}
	q.Set("instId", raw)
	var rows []struct {
		BidPx string `json:"bidPx"`
		BidSz string `json:"bidSz"`
		AskPx string `json:"askPx"`
		AskSz string `json:"askSz"`
	}
	if err := a.t.publicGet(ctx, pathTicker, q, &rows); err != nil {
		return venue.BookTicker{}, err
	}
	ctVal := 1.0
	if m == venue.MarketPerp {
		if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
			ctVal = inst.ContractSize
		}
	}
	bt := venue.BookTicker{Symbol: s, Market: m}
	if len(rows) > 0 {
		bt.BidPrice = parseFloat(rows[0].BidPx)
		bt.BidQty = parseFloat(rows[0].BidSz) * ctVal
		bt.AskPrice = parseFloat(rows[0].AskPx)
		bt.AskQty = parseFloat(rows[0].AskSz) * ctVal
	}
	return bt, nil
}

// FetchDepth 用 /api/v5/market/books 取多档盘口。永续 sz 为张,现货为基础币。
func (a *Adapter) FetchDepth(ctx context.Context, m venue.Market, s venue.Symbol, limit int) (venue.Depth, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Depth{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 400 {
		limit = 400
	}
	q := url.Values{}
	q.Set("instId", raw)
	q.Set("sz", strconv.Itoa(limit))
	var rows []struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := a.t.publicGet(ctx, pathBooks, q, &rows); err != nil {
		return venue.Depth{}, err
	}
	depth := venue.Depth{Symbol: s, Market: m}
	if len(rows) == 0 {
		return depth, nil
	}
	ctVal := 1.0
	if m == venue.MarketPerp {
		if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
			ctVal = inst.ContractSize
		}
	}
	for _, lv := range rows[0].Bids {
		if len(lv) >= 2 {
			qty := parseFloat(lv[1])
			if m == venue.MarketPerp {
				qty *= ctVal
			}
			depth.Bids = append(depth.Bids, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: qty})
		}
	}
	for _, lv := range rows[0].Asks {
		if len(lv) >= 2 {
			qty := parseFloat(lv[1])
			if m == venue.MarketPerp {
				qty *= ctVal
			}
			depth.Asks = append(depth.Asks, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: qty})
		}
	}
	return depth, nil
}

// FetchOpenInterest 取最近持仓量(转换为币数量:张数 × ctVal)。
func (a *Adapter) FetchOpenInterest(ctx context.Context, s venue.Symbol) (float64, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return 0, err
	}
	q := url.Values{}
	q.Set("instId", raw)
	var rows []struct {
		OI string `json:"oi"`
	}
	if err := a.t.publicGet(ctx, pathOpenInterest, q, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	contracts := parseFloat(rows[0].OI)
	ctVal := 1.0
	if inst, ierr := a.lookupInstrument(ctx, venue.MarketPerp, s); ierr == nil && inst.ContractSize > 0 {
		ctVal = inst.ContractSize
	}
	return contracts * ctVal, nil
}

// FetchRecentQuoteVolume 用 /api/v5/market/ticker 取 24h 计价货币成交量(USDT 报价量)。
// 优先 volCcy24h(计价/结算货币口径;USDT 本位 spot/SWAP 即 USDT 成交额)。
// 若 volCcy24h 缺失或为 0 且为永续,回退 vol24h(张)×ctVal×last 换算 USDT。
// window 由调用方传入(流动性 sizing 固定 24h);ticker 端点本身为滚动 24h 统计。
func (a *Adapter) FetchRecentQuoteVolume(ctx context.Context, m venue.Market, s venue.Symbol, window time.Duration) (float64, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return 0, err
	}
	q := url.Values{}
	q.Set("instId", raw)
	var rows []struct {
		VolCcy24h string `json:"volCcy24h"`
		Vol24h    string `json:"vol24h"`
		Last      string `json:"last"`
	}
	if err := a.t.publicGet(ctx, pathTicker, q, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("okx: ticker 空响应 instId=%s", raw)
	}
	vol := parseFloat(rows[0].VolCcy24h)
	if vol <= 0 && m == venue.MarketPerp {
		// 少数 SWAP volCcy24h 可能非 USDT 口径;用 张×面值×last 换算为 USDT 报价量。
		contracts := parseFloat(rows[0].Vol24h)
		last := parseFloat(rows[0].Last)
		ctVal := 1.0
		if inst, ierr := a.lookupInstrument(ctx, m, s); ierr == nil && inst.ContractSize > 0 {
			ctVal = inst.ContractSize
		}
		vol = contracts * ctVal * last
	}
	return vol, nil
}

// instCacheTTL:ctVal/lotSz/tickSz 是合约定义,上线后几乎不变(改面值等于换合约),
// 10 分钟 TTL 已足够保守;命中缓存可让下单热路径(开仓窗口仅 240s)省掉一次公网往返与失败点。
const instCacheTTL = 10 * time.Minute

type instCacheEntry struct {
	inst      venue.Instrument
	expiresAt time.Time
}

// lookupInstrument 拉单条 instruments 元数据(PlaceOrder/盘口换算用),带进程级 TTL 缓存。
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

// fetchInstrument 实拉单条 instruments(缓存未命中路径)。
func (a *Adapter) fetchInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	instType := instTypeSpot
	if m == venue.MarketPerp {
		instType = instTypeSwap
	}
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Instrument{}, err
	}
	q := url.Values{}
	q.Set("instType", instType)
	q.Set("instId", raw)
	var rows []okxInstrument
	if err := a.t.publicGet(ctx, pathInstruments, q, &rows); err != nil {
		return venue.Instrument{}, err
	}
	if len(rows) == 0 {
		return venue.Instrument{}, fmt.Errorf("okx: 找不到 instruments instId=%s", raw)
	}
	it := rows[0]
	ctVal := parseFloat(it.CtVal)
	if m == venue.MarketSpot || ctVal <= 0 {
		ctVal = 1
	}
	qtyUnit := "coin"
	if m == venue.MarketPerp {
		qtyUnit = "contract"
	}
	canonical, _ := a.ToCanonical(it.InstID, m)
	return venue.Instrument{
		Symbol:           canonical,
		Market:           m,
		BaseAsset:        strings.ToUpper(it.BaseCcy),
		QuoteAsset:       strings.ToUpper(it.QuoteCcy),
		TickSize:         parseFloat(it.TickSz),
		StepSize:         parseFloat(it.LotSz),
		ContractSize:     ctVal,
		QtyUnit:          qtyUnit,
		RawTradingStatus: it.State,
		NonTrading:       exchange.ExplicitNonTradingOKXState(it.State),
	}, nil
}
