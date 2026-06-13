package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
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
