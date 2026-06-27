package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// fakeBybit 起一个最小 Bybit v5 mock,按 path 返回固定 result 信封,验证解析逻辑(离线、确定性)。
func fakeBybit(t *testing.T, routes map[string]string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "no route", http.StatusNotFound)
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
	a := fakeBybit(t, map[string]string{
		pathServerTime: `{"retCode":0,"retMsg":"OK","result":{"timeSecond":"1700000000","timeNano":"1700000000123456789"}}`,
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
	a := fakeBybit(t, map[string]string{
		pathTickers: `{"retCode":0,"retMsg":"OK","result":{"category":"linear","list":[
			{"symbol":"BTCUSDT","lastPrice":"60000","indexPrice":"60010","markPrice":"60005","fundingRate":"-0.0012","nextFundingTime":"1700000000000"}
		]}}`,
		// fundingIntervalMap 会再调 instruments-info;给个空 list 让其回退默认 8h。
		pathInstrumentsInfo: `{"retCode":0,"retMsg":"OK","result":{"list":[],"nextPageCursor":""}}`,
	})
	snaps, err := a.FetchFundingSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchFundingSnapshot: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("应解析 1 条, got %d", len(snaps))
	}
	s := snaps[0]
	if s.Symbol != "BTC-USDT" {
		t.Errorf("符号未规范化: %s", s.Symbol)
	}
	if s.LastFundingRate != -0.0012 {
		t.Errorf("fundingRate 解析错误: %v", s.LastFundingRate)
	}
	if s.MarkPrice != 60005 {
		t.Errorf("markPrice 解析错误: %v", s.MarkPrice)
	}
	if s.IntervalHours != 8 {
		t.Errorf("应回退默认 8h, got %d", s.IntervalHours)
	}
	if !s.NextFundingTime.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Errorf("nextFundingTime 解析错误: %v", s.NextFundingTime)
	}
}

func TestBookTickerParse(t *testing.T) {
	a := fakeBybit(t, map[string]string{
		pathOrderbook: `{"retCode":0,"retMsg":"OK","result":{"s":"BTCUSDT","b":[["59999.5","2.5"]],"a":[["60000.5","1.2"]],"ts":1700000000000,"u":1}}`,
	})
	bt, err := a.FetchBookTicker(context.Background(), venue.MarketPerp, "BTC-USDT")
	if err != nil {
		t.Fatalf("FetchBookTicker: %v", err)
	}
	if bt.BidPrice != 59999.5 || bt.BidQty != 2.5 || bt.AskPrice != 60000.5 || bt.AskQty != 1.2 {
		t.Errorf("盘口解析错误: %+v", bt)
	}
}

func TestBalancesParseSigned(t *testing.T) {
	a := fakeBybit(t, map[string]string{
		pathWalletBalance: `{"retCode":0,"retMsg":"OK","result":{"list":[{"accountType":"UNIFIED","coin":[
			{"coin":"USDT","walletBalance":"1000.5","locked":"0.5","availableToWithdraw":"1000","usdValue":"1000.5"}
		]}]}}`,
	})
	bal, err := a.FetchBalances(context.Background())
	if err != nil {
		t.Fatalf("FetchBalances: %v", err)
	}
	u, ok := bal.ByAsset["USDT"]
	if !ok {
		t.Fatalf("USDT 余额缺失")
	}
	if u.Free != 1000 || u.WalletUSD != 1000.5 {
		t.Errorf("余额解析错误: %+v", u)
	}
}

func TestFetchInstrumentsSpotBorrowableFromMarginTrading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathInstrumentsInfo {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("category") {
		case categoryLinear:
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
				{"symbol":"BTCUSDT","contractType":"LinearPerpetual","baseCoin":"BTC","quoteCoin":"USDT","status":"Trading",
				"priceFilter":{"tickSize":"0.01"},"lotSizeFilter":{"qtyStep":"0.001","minNotionalValue":"5"}}
			],"nextPageCursor":""}}`))
		case "spot":
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
				{"symbol":"HOMEUSDT","baseCoin":"HOME","quoteCoin":"USDT","status":"Trading","marginTrading":"utaOnly",
				"priceFilter":{"tickSize":"0.0001"},"lotSizeFilter":{"basePrecision":"0.1","minNotionalValue":"5"}},
				{"symbol":"HUSDT","baseCoin":"H","quoteCoin":"USDT","status":"Trading","marginTrading":"none",
				"priceFilter":{"tickSize":"0.0001"},"lotSizeFilter":{"basePrecision":"0.1","minNotionalValue":"5"}},
				{"symbol":"BOTHUSDT","baseCoin":"BOTH","quoteCoin":"USDT","status":"Trading","marginTrading":"both",
				"priceFilter":{"tickSize":"0.0001"},"lotSizeFilter":{"basePrecision":"0.1","minNotionalValue":"5"}}
			],"nextPageCursor":""}}`))
		default:
			http.Error(w, "unknown category", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	instrs, err := a.FetchInstruments(context.Background())
	if err != nil {
		t.Fatalf("FetchInstruments: %v", err)
	}
	spotBorrow := map[venue.Symbol]bool{}
	for _, in := range instrs {
		if in.Market != venue.MarketSpot {
			continue
		}
		spotBorrow[in.Symbol] = in.SpotBorrowable
	}
	if got, want := spotBorrow["HOME-USDT"], true; got != want {
		t.Errorf("utaOnly 应可借: got %v want %v", got, want)
	}
	if got, want := spotBorrow["BOTH-USDT"], true; got != want {
		t.Errorf("both 应可借: got %v want %v", got, want)
	}
	if got, want := spotBorrow["H-USDT"], false; got != want {
		t.Errorf("none 应不可借: got %v want %v", got, want)
	}
}

func TestPositionsSignParse(t *testing.T) {
	a := fakeBybit(t, map[string]string{
		pathPositionList: `{"retCode":0,"retMsg":"OK","result":{"list":[
			{"symbol":"BTCUSDT","side":"Sell","size":"0.5","avgPrice":"60000","markPrice":"60010","unrealisedPnl":"-5","leverage":"3","tradeMode":0}
		]}}`,
	})
	pos, err := a.FetchPositions(context.Background())
	if err != nil {
		t.Fatalf("FetchPositions: %v", err)
	}
	if len(pos) != 1 {
		t.Fatalf("应 1 条持仓, got %d", len(pos))
	}
	if pos[0].PositionAmt != -0.5 {
		t.Errorf("Sell 应为负 size: %v", pos[0].PositionAmt)
	}
	if pos[0].Leverage != 3 {
		t.Errorf("杠杆解析错误: %v", pos[0].Leverage)
	}
}

func TestFetchInstrumentsSpotFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathInstrumentsInfo {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("category") {
		case categoryLinear:
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
				{"symbol":"BTCUSDT","contractType":"LinearPerpetual","baseCoin":"BTC","quoteCoin":"USDT","status":"Trading",
				"priceFilter":{"tickSize":"0.01"},"lotSizeFilter":{"qtyStep":"0.001","minNotionalValue":"5"}}
			],"nextPageCursor":""}}`))
		case categorySpot:
			_, _ = w.Write([]byte(`{"retCode":10001,"retMsg":"spot unavailable","result":{}}`))
		default:
			http.Error(w, "unknown category", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.FetchInstruments(context.Background())
	if err == nil {
		t.Fatal("spot 拉取失败应返回 error")
	}
	if !strings.Contains(err.Error(), "spot instruments-info") {
		t.Errorf("error 应提及 spot instruments-info: %v", err)
	}
}

func TestFetchBookTickerCategoryByMarket(t *testing.T) {
	var gotCategory string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathOrderbook {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		gotCategory = r.URL.Query().Get("category")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"s":"BTCUSDT","b":[["59999.5","2.5"]],"a":[["60000.5","1.2"]],"ts":1700000000000,"u":1}}`))
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gotCategory = ""
	if _, err := a.FetchBookTicker(context.Background(), venue.MarketPerp, "BTC-USDT"); err != nil {
		t.Fatalf("FetchBookTicker perp: %v", err)
	}
	if gotCategory != categoryLinear {
		t.Errorf("MarketPerp category=%q want %q", gotCategory, categoryLinear)
	}

	gotCategory = ""
	if _, err := a.FetchBookTicker(context.Background(), venue.MarketSpot, "BTC-USDT"); err != nil {
		t.Fatalf("FetchBookTicker spot: %v", err)
	}
	if gotCategory != categorySpot {
		t.Errorf("MarketSpot category=%q want %q", gotCategory, categorySpot)
	}
}

func TestFetchInstrumentsSpotStepSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathInstrumentsInfo {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("category") {
		case categoryLinear:
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[],"nextPageCursor":""}}`))
		case categorySpot:
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
				{"symbol":"QTYUSDT","baseCoin":"QTY","quoteCoin":"USDT","status":"Trading","marginTrading":"none",
				"priceFilter":{"tickSize":"0.01"},"lotSizeFilter":{"qtyStep":"0.001","basePrecision":"0.1","minNotionalValue":"5"}},
				{"symbol":"BASEUSDT","baseCoin":"BASE","quoteCoin":"USDT","status":"Trading","marginTrading":"none",
				"priceFilter":{"tickSize":"0.0001"},"lotSizeFilter":{"basePrecision":"0.1","minNotionalValue":"5"}}
			],"nextPageCursor":""}}`))
		default:
			http.Error(w, "unknown category", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	instrs, err := a.FetchInstruments(context.Background())
	if err != nil {
		t.Fatalf("FetchInstruments: %v", err)
	}
	spotStep := map[venue.Symbol]float64{}
	for _, in := range instrs {
		if in.Market == venue.MarketSpot {
			spotStep[in.Symbol] = in.StepSize
		}
	}
	if got, want := spotStep["QTY-USDT"], 0.001; got != want {
		t.Errorf("qtyStep 优先: got %v want %v", got, want)
	}
	if got, want := spotStep["BASE-USDT"], 0.1; got != want {
		t.Errorf("basePrecision 回退: got %v want %v", got, want)
	}
}

// TestFetchCandlesParse 钉死 K 线解析:Bybit 返回 newest-first 字符串数组,
// FetchCandles 应转为 oldest-first 并正确解析 ts/O/H/L/C/vol。
func TestFetchCandlesParse(t *testing.T) {
	a := fakeBybit(t, map[string]string{
		pathKline: `{"retCode":0,"retMsg":"OK","result":{"category":"linear","list":[
			["1700000120000","60100","60150","60080","60120","10.5","1050000"],
			["1700000060000","60050","60100","60000","60100","8.2","492000"],
			["1700000000000","60000","60060","59990","60050","12.1","726050"]
		]}}`,
	})
	candles, err := a.FetchCandles(context.Background(), venue.MarketPerp, "BTC-USDT", "5m", 3)
	if err != nil {
		t.Fatalf("FetchCandles: %v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("应解析 3 根 K 线, got %d", len(candles))
	}
	if candles[0].OpenTime.UnixMilli() != 1700000000000 {
		t.Errorf("第一根应为最旧 ts, got %d", candles[0].OpenTime.UnixMilli())
	}
	if candles[2].OpenTime.UnixMilli() != 1700000120000 {
		t.Errorf("最后一根应为最新 ts, got %d", candles[2].OpenTime.UnixMilli())
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

func TestParseBybitKlines(t *testing.T) {
	rows := [][]string{
		{"1700000120000", "60100", "60150", "60080", "60120", "10.5", "1050000"},
		{"1700000000000", "60000", "60060", "59990", "60050", "12.1", "726050"},
	}
	candles := parseBybitKlines(rows)
	if len(candles) != 2 {
		t.Fatalf("got %d candles", len(candles))
	}
	if candles[0].OpenTime.UnixMilli() != 1700000000000 || candles[0].Volume != 12.1 {
		t.Errorf("反转后第一根解析错误: %+v", candles[0])
	}
}

func TestBarToBybitInterval(t *testing.T) {
	if got, err := barToBybitInterval("5m"); err != nil || got != "5" {
		t.Fatalf("5m -> 5: got=%q err=%v", got, err)
	}
	if got, err := barToBybitInterval("1h"); err != nil || got != "60" {
		t.Fatalf("1h -> 60: got=%q err=%v", got, err)
	}
}
