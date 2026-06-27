package binance

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestFetchBookTickerAndDepth(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, PathFuturesBookTicker):
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"60000","bidQty":"1","askPrice":"60001","askQty":"2"}`))
		case strings.Contains(r.URL.Path, PathFuturesDepth):
			_, _ = w.Write([]byte(`{"bids":[["60000","1"]],"asks":[["60001","2"],["60002","3"]]}`))
		default:
			http.NotFound(w, r)
		}
	})
	bt, err := a.FetchBookTicker(context.Background(), venue.MarketPerp, "BTC-USDT")
	if err != nil || bt.BidPrice != 60000 || bt.AskQty != 2 {
		t.Fatalf("bookTicker 解析错误: %+v err=%v", bt, err)
	}
	depth, err := a.FetchDepth(context.Background(), venue.MarketPerp, "BTC-USDT", 5)
	if err != nil || len(depth.Asks) != 2 || depth.Bids[0].Qty != 1 {
		t.Fatalf("depth 解析错误: %+v err=%v", depth, err)
	}
}

func TestFetchOpenInterest(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openInterest":"12345.67","symbol":"BTCUSDT"}`))
	})
	oi, err := a.FetchOpenInterest(context.Background(), "BTC-USDT")
	if err != nil || oi != 12345.67 {
		t.Fatalf("openInterest=%v err=%v", oi, err)
	}
}

func TestFetchRecentQuoteVolumeAggregatesKlines(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("interval"), "m") && r.URL.Query().Get("interval") != "1h" {
			t.Fatalf("interval=%q", r.URL.Query().Get("interval"))
		}
		_, _ = w.Write([]byte(`[
			[0,"0","0","0","0","0","100","1000","0,0,0,0"],
			[0,"0","0","0","0","0","200","2500","0,0,0,0"]
		]`))
	})
	vol, err := a.FetchRecentQuoteVolume(context.Background(), venue.MarketPerp, "BTC-USDT", 2*time.Minute)
	if err != nil || vol != 3500 {
		t.Fatalf("quote volume 求和错误 vol=%v err=%v", vol, err)
	}
}

func TestInstrumentCacheHitSkipsSecondHTTP(t *testing.T) {
	var hits atomic.Int32
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "exchangeInfo") {
			hits.Add(1)
			_, _ = w.Write([]byte(futuresExchangeInfoJSON()))
		}
	})
	ctx := context.Background()
	if _, err := a.lookupInstrument(ctx, venue.MarketPerp, "BTC-USDT"); err != nil {
		t.Fatalf("lookupInstrument #1: %v", err)
	}
	if _, err := a.lookupInstrument(ctx, venue.MarketPerp, "BTC-USDT"); err != nil {
		t.Fatalf("lookupInstrument #2: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("缓存命中后应只请求 1 次 exchangeInfo, 实际 %d", hits.Load())
	}
}

func TestKlineIntervalForMinutes(t *testing.T) {
	interval, limit := klineIntervalForMinutes(45)
	if interval != "1m" || limit != 45 {
		t.Fatalf("45m 应 1m x45, got %s limit=%d", interval, limit)
	}
	interval, limit = klineIntervalForMinutes(200)
	if interval != "5m" {
		t.Fatalf("200m 应 5m, got %s limit=%d", interval, limit)
	}
}

// TestFetchCandlesParse 钉死 K 线解析:币安 klines 为 oldest-first 数组,
// FetchCandles 应正确解析 ts/O/H/L/C/vol 并保持顺序。
func TestFetchCandlesParse(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, PathFuturesKlines) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("interval") != "5m" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("query=%v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`[
			[1700000000000,"60000","60060","59990","60050","12.1",1700000299999,"726050",100,"500","0","0"],
			[1700000060000,"60050","60100","60000","60100","8.2",1700000899999,"492000",80,"400","0","0"],
			[1700000120000,"60100","60150","60080","60120","10.5",1700001499999,"1050000",90,"450","0","0"]
		]`))
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

func TestParseBinanceKlines(t *testing.T) {
	rows := [][]any{
		{float64(1700000120000), "60100", "60150", "60080", "60120", "10.5"},
		{float64(1700000000000), "60000", "60060", "59990", "60050", "12.1"},
	}
	candles := parseBinanceKlines(rows)
	if len(candles) != 2 {
		t.Fatalf("got %d candles", len(candles))
	}
	if candles[0].OpenTime.UnixMilli() != 1700000120000 || candles[0].Volume != 10.5 {
		t.Errorf("第一行解析错误: %+v", candles[0])
	}
}
