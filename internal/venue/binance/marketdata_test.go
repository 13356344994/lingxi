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
