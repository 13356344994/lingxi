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
