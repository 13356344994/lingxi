package gate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// fakeGate 起一个最小 Gate v4 mock,按 path 返回固定 JSON,验证解析逻辑(离线、确定性)。
func fakeGate(t *testing.T, routes map[string]string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		body, ok := routes[key]
		if !ok {
			http.Error(w, "no route: "+key, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestServerTimeParse(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathSpotTime: `{"server_time":1700000000}`,
	})
	ts, err := a.ServerTime(context.Background())
	if err != nil {
		t.Fatalf("ServerTime: %v", err)
	}
	if ts.Unix() != 1700000000 {
		t.Errorf("ServerTime 秒不符: %d", ts.Unix())
	}
}

func TestFundingSnapshotParse(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesContracts: `[
			{"name":"BTC_USDT","funding_rate":"-0.0012","funding_next_apply":1700028800,"funding_interval":28800,
			 "mark_price":"60005","index_price":"60010","last_price":"60000","in_delisting":false},
			{"name":"DELIST_USDT","funding_rate":"0.01","funding_next_apply":1700028800,"funding_interval":28800,
			 "mark_price":"1","index_price":"1","in_delisting":true}
		]`,
	})
	snaps, err := a.FetchFundingSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchFundingSnapshot: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("应跳过 in_delisting, got %d 条", len(snaps))
	}
	s := snaps[0]
	if s.Symbol != "BTC-USDT" {
		t.Errorf("符号未规范化: %s", s.Symbol)
	}
	if s.LastFundingRate != -0.0012 {
		t.Errorf("fundingRate 解析错误: %v", s.LastFundingRate)
	}
	if s.IntervalHours != 8 {
		t.Errorf("IntervalHours 应为 28800/3600=8, got %d", s.IntervalHours)
	}
	if !s.NextFundingTime.Equal(time.Unix(1700028800, 0).UTC()) {
		t.Errorf("nextFundingTime 解析错误: %v", s.NextFundingTime)
	}
}

func TestFundingSnapshotBestEffortSkipBad(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesContracts: `[
			{"name":"GOOD_USDT","funding_rate":"0.0001","funding_next_apply":1700028800,"funding_interval":14400,
			 "mark_price":"1","index_price":"1","in_delisting":false},
			{"name":"BAD","funding_rate":"x","funding_next_apply":0,"funding_interval":0,"in_delisting":false}
		]`,
	})
	snaps, err := a.FetchFundingSnapshot(context.Background())
	if err != nil {
		t.Fatalf("单币解析失败不应整体报错: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("应 best-effort 返回 1 条, got %d", len(snaps))
	}
	if snaps[0].IntervalHours != 4 {
		t.Errorf("14400s → 4h, got %d", snaps[0].IntervalHours)
	}
}

func TestBookTickerPerpContractConversion(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesContracts + "/HOME_USDT": `{"name":"HOME_USDT","quanto_multiplier":"100","order_price_round":"0.0001","order_size_round":1}`,
		pathFuturesOrderBook:                `{"asks":[{"p":"0.6","s":3}],"bids":[{"p":"0.5","s":2}]}`,
	})
	bt, err := a.FetchBookTicker(context.Background(), venue.MarketPerp, "HOME-USDT")
	if err != nil {
		t.Fatalf("FetchBookTicker: %v", err)
	}
	if bt.BidQty != 200 || bt.AskQty != 300 {
		t.Errorf("张→币应乘 100: %+v", bt)
	}
}

func TestLookupInstrumentCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathFuturesContracts+"/HOME_USDT" {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = w.Write([]byte(`{"name":"HOME_USDT","quanto_multiplier":"100","order_price_round":"0.0001","order_size_round":1}`))
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := a.lookupInstrument(context.Background(), venue.MarketPerp, "HOME-USDT")
	if err != nil {
		t.Fatalf("第一次 lookupInstrument: %v", err)
	}
	if first.ContractSize != 100 {
		t.Fatalf("quanto_multiplier 解析错误: %v", first.ContractSize)
	}
	if _, err := a.lookupInstrument(context.Background(), venue.MarketPerp, "HOME-USDT"); err != nil {
		t.Fatalf("第二次 lookupInstrument: %v", err)
	}
	if hits != 1 {
		t.Fatalf("第二次应命中缓存不发 HTTP, 实际请求 %d 次", hits)
	}

	a.t.nowFn = func() time.Time { return time.Now().Add(instCacheTTL + time.Minute) }
	if _, err := a.lookupInstrument(context.Background(), venue.MarketPerp, "HOME-USDT"); err != nil {
		t.Fatalf("TTL 过期后 lookupInstrument: %v", err)
	}
	if hits != 2 {
		t.Fatalf("TTL 过期应重新拉取, 实际请求 %d 次", hits)
	}
}

func TestFetchInstrumentsContractMetadata(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesContracts:  `[{"name":"HOME_USDT","quanto_multiplier":"100","order_price_round":"0.0001","order_size_round":1,"in_delisting":false}]`,
		pathSpotCurrencyPairs: `[{"id":"HOME_USDT","base":"HOME","quote":"USDT","trade_status":"tradable","amount_precision":2,"precision":4,"min_base_amount":"0.01","min_quote_amount":"1"}]`,
	})
	insts, err := a.FetchInstruments(context.Background())
	if err != nil {
		t.Fatalf("FetchInstruments: %v", err)
	}
	var perp, spot *venue.Instrument
	for i := range insts {
		if insts[i].Symbol == "HOME-USDT" && insts[i].Market == venue.MarketPerp {
			perp = &insts[i]
		}
		if insts[i].Symbol == "HOME-USDT" && insts[i].Market == venue.MarketSpot {
			spot = &insts[i]
		}
	}
	if perp == nil || perp.ContractSize != 100 || perp.QtyUnit != "contract" {
		t.Errorf("永续元数据错误: %+v", perp)
	}
	if spot == nil || spot.QtyUnit != "coin" {
		t.Errorf("现货元数据错误: %+v", spot)
	}
}

func TestFetchRecentQuoteVolumeSpot(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathSpotTickers: `[{"currency_pair":"BTC_USDT","quote_volume":"5555555.55"}]`,
	})
	vol, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketSpot, "BTC-USDT", 24*time.Hour)
	if err != nil || vol != 5555555.55 {
		t.Fatalf("spot quote_volume 解析错误 vol=%v err=%v", vol, err)
	}
}

func TestFetchRecentQuoteVolumePerp(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesTickers: `[{"contract":"BTC_USDT","volume_24h_settle":"8888888","volume_24h_quote":"7777777","volume_24h_base":"150","last":"60000"}]`,
	})
	vol, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketPerp, "BTC-USDT", 24*time.Hour)
	if err != nil || vol != 8888888 {
		t.Fatalf("永续 volume_24h_settle(USDT) 解析错误 vol=%v err=%v", vol, err)
	}
}

func TestFetchRecentQuoteVolumePerpFallback(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesTickers: `[{"contract":"BTC_USDT","volume_24h_settle":"","volume_24h_quote":"6666666","last":"60000"}]`,
	})
	vol, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketPerp, "BTC-USDT", 24*time.Hour)
	if err != nil || vol != 6666666 {
		t.Fatalf("永续 volume_24h_quote 回退错误 vol=%v err=%v", vol, err)
	}
}

func TestFetchRecentQuoteVolumeHTTPError(t *testing.T) {
	a := fakeGate(t, map[string]string{})
	_, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketSpot, "BTC-USDT", 24*time.Hour)
	if err == nil {
		t.Fatal("HTTP 404 应返回 error")
	}
}

func TestFetchRecentQuoteVolumeEmptyResponse(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathSpotTickers: `[]`,
	})
	_, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketSpot, "BTC-USDT", 24*time.Hour)
	if err == nil {
		t.Fatal("空响应应返回 error")
	}
}

// TestFetchCandlesParse 钉死 K 线解析:Gate candlesticks 为 oldest-first 对象数组,
// FetchCandles 应正确解析 ts/O/H/L/C/vol(张×multiplier→币数量)。
func TestFetchCandlesParse(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesCandlesticks: `[
			{"t":1700000000,"v":1210,"o":"60000","h":"60060","l":"59990","c":"60050","sum":"726050"},
			{"t":1700000060,"v":820,"o":"60050","h":"60100","l":"60000","c":"60100","sum":"492000"},
			{"t":1700000120,"v":1050,"o":"60100","h":"60150","l":"60080","c":"60120","sum":"1050000"}
		]`,
		pathFuturesContracts + "/BTC_USDT": `{"name":"BTC_USDT","quanto_multiplier":"0.01","order_size_round":1}`,
	})
	candles, err := a.FetchCandles(context.Background(), venue.MarketPerp, "BTC-USDT", "5m", 3)
	if err != nil {
		t.Fatalf("FetchCandles: %v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("应解析 3 根 K 线, got %d", len(candles))
	}
	if candles[0].OpenTime.Unix() != 1700000000 {
		t.Errorf("第一根应为最旧 ts, got %d", candles[0].OpenTime.Unix())
	}
	if candles[2].OpenTime.Unix() != 1700000120 {
		t.Errorf("最后一根应为最新 ts, got %d", candles[2].OpenTime.Unix())
	}
	c0 := candles[0]
	if c0.Open != 60000 || c0.High != 60060 || c0.Low != 59990 || c0.Close != 60050 || c0.Volume != 12.1 {
		t.Errorf("最旧 K 线 O/H/L/C/vol 解析错误: %+v", c0)
	}
	c2 := candles[2]
	if c2.Open != 60100 || c2.High != 60150 || c2.Low != 60080 || c2.Close != 60120 || c2.Volume != 10.5 {
		t.Errorf("最新 K 线 O/H/L/C/vol 解析错误: %+v", c2)
	}
}

func TestParseGateCandlesticks(t *testing.T) {
	rows := []gateCandlestick{
		{T: 1700000120, V: 1050, O: "60100", H: "60150", L: "60080", C: "60120"},
		{T: 1700000000, V: 1210, O: "60000", H: "60060", L: "59990", C: "60050"},
	}
	candles := parseGateCandlesticks(rows, 0.01)
	if len(candles) != 2 {
		t.Fatalf("got %d candles", len(candles))
	}
	if candles[0].OpenTime.Unix() != 1700000120 || candles[0].Volume != 10.5 {
		t.Errorf("第一行解析错误: %+v", candles[0])
	}
	if candles[1].Volume != 12.1 {
		t.Errorf("第二行 vol 换算错误: %+v", candles[1])
	}
}
