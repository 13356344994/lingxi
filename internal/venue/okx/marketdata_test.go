package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// fakeOKX 起一个最小 OKX v5 mock,按 path 返回固定 data 信封,验证解析逻辑(离线、确定性)。
func fakeOKX(t *testing.T, routes map[string]string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		body, ok := routes[key]
		if !ok {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "TESTKEY", APISecret: "TESTSECRET", Passphrase: "TESTPASS", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestServerTimeParse(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathServerTime: `{"code":"0","msg":"","data":[{"ts":"1700000000123"}]}`,
	})
	ts, err := a.ServerTime(context.Background())
	if err != nil {
		t.Fatalf("ServerTime: %v", err)
	}
	if ts.UnixMilli() != 1700000000123 {
		t.Errorf("ServerTime 毫秒不符: %d", ts.UnixMilli())
	}
}

func TestFundingSnapshotParse(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathTickers: `{"code":"0","msg":"","data":[
			{"instId":"BTC-USDT-SWAP","markPx":"60005","idxPx":"60010","last":"60000"}
		]}`,
		pathFundingRate: `{"code":"0","msg":"","data":[
			{"instId":"BTC-USDT-SWAP","fundingRate":"-0.0012","fundingTime":"1700000000000","nextFundingTime":"1700028800000"}
		]}`,
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
		t.Errorf("结算周期推导错误: got %dh want 8h", s.IntervalHours)
	}
	if !s.NextFundingTime.Equal(time.UnixMilli(1700028800000).UTC()) {
		t.Errorf("nextFundingTime 解析错误: %v", s.NextFundingTime)
	}
}

// TestBookTickerParse 钉死永续 bidSz/askSz 的张→币换算:SWAP ticker 数量单位是「张」,
// 不乘 ctVal 会让流动性判断差一个 ctVal 倍数。
func TestBookTickerParse(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathTicker:      `{"code":"0","msg":"","data":[{"instId":"HOME-USDT-SWAP","bidPx":"0.5","bidSz":"2.5","askPx":"0.6","askSz":"1.2"}]}`,
		pathInstruments: `{"code":"0","msg":"","data":[{"instId":"HOME-USDT-SWAP","instType":"SWAP","baseCcy":"HOME","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"1","minSz":"1","ctVal":"100","ctValCcy":"HOME"}]}`,
	})
	bt, err := a.FetchBookTicker(context.Background(), venue.MarketPerp, "HOME-USDT")
	if err != nil {
		t.Fatalf("FetchBookTicker: %v", err)
	}
	if bt.BidPrice != 0.5 || bt.BidQty != 250 || bt.AskPrice != 0.6 || bt.AskQty != 120 {
		t.Errorf("永续盘口应乘 ctVal=100 换算为币数量: %+v", bt)
	}
}

// TestBookTickerSpotNoConversion 现货数量已是基础币,不应做任何换算。
func TestBookTickerSpotNoConversion(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathTicker: `{"code":"0","msg":"","data":[{"instId":"BTC-USDT","bidPx":"59999.5","bidSz":"2.5","askPx":"60000.5","askSz":"1.2"}]}`,
	})
	bt, err := a.FetchBookTicker(context.Background(), venue.MarketSpot, "BTC-USDT")
	if err != nil {
		t.Fatalf("FetchBookTicker: %v", err)
	}
	if bt.BidQty != 2.5 || bt.AskQty != 1.2 {
		t.Errorf("现货盘口不应换算: %+v", bt)
	}
}

func TestBalancesParseSigned(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathBalance: `{"code":"0","msg":"","data":[{"details":[
			{"ccy":"USDT","cashBal":"1000.5","availBal":"1000","frozenBal":"0.5","eqUsd":"1000.5"}
		]}]}`,
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

// TestLookupInstrumentCached 钉死 instruments 缓存:第二次调用命中缓存,不再发 HTTP
// (下单热路径每单省一次公网往返与失败点)。
func TestLookupInstrumentCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathInstruments {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"HOME-USDT-SWAP","instType":"SWAP","baseCcy":"HOME","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"1","minSz":"1","ctVal":"100","ctValCcy":"HOME"}]}`))
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
		t.Fatalf("ctVal 解析错误: %v", first.ContractSize)
	}
	second, err := a.lookupInstrument(context.Background(), venue.MarketPerp, "HOME-USDT")
	if err != nil {
		t.Fatalf("第二次 lookupInstrument: %v", err)
	}
	if second.ContractSize != 100 {
		t.Fatalf("缓存返回错误: %+v", second)
	}
	if hits != 1 {
		t.Fatalf("第二次应命中缓存不发 HTTP, 实际请求 %d 次", hits)
	}

	// TTL 过期后应重新拉取。
	a.t.nowFn = func() time.Time { return time.Now().Add(instCacheTTL + time.Minute) }
	if _, err := a.lookupInstrument(context.Background(), venue.MarketPerp, "HOME-USDT"); err != nil {
		t.Fatalf("TTL 过期后 lookupInstrument: %v", err)
	}
	if hits != 2 {
		t.Fatalf("TTL 过期应重新拉取, 实际请求 %d 次", hits)
	}
}

// TestFundingSnapshotBestEffort 钉死 best-effort 容错:单币失败被跳过(不污染整体),
// 失败过半才整体报错。
func TestFundingSnapshotBestEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathTickers:
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instId":"BTC-USDT-SWAP","markPx":"60000","idxPx":"60000","last":"60000"},
				{"instId":"ETH-USDT-SWAP","markPx":"3000","idxPx":"3000","last":"3000"},
				{"instId":"BAD-USDT-SWAP","markPx":"1","idxPx":"1","last":"1"}
			]}`))
		case pathFundingRate:
			if r.URL.Query().Get("instId") == "BAD-USDT-SWAP" {
				_, _ = w.Write([]byte(`{"code":"51001","msg":"Instrument ID does not exist","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"fundingRate":"0.0001","fundingTime":"1700000000000","nextFundingTime":"1700028800000"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snaps, err := a.FetchFundingSnapshot(context.Background())
	if err != nil {
		t.Fatalf("单币失败不应让整体报错: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("应跳过失败币返回 2 条, got %d", len(snaps))
	}
}

// TestFundingSnapshotFailsWhenMajorityFail 失败过半应整体报错且错误带计数。
func TestFundingSnapshotFailsWhenMajorityFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathTickers:
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instId":"AAA-USDT-SWAP","markPx":"1","idxPx":"1","last":"1"},
				{"instId":"BBB-USDT-SWAP","markPx":"1","idxPx":"1","last":"1"}
			]}`))
		case pathFundingRate:
			_, _ = w.Write([]byte(`{"code":"51001","msg":"Instrument ID does not exist","data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.FetchFundingSnapshot(context.Background()); err == nil {
		t.Fatal("全部失败应整体报错")
	}
}

func TestPositionsSignParse(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathPositions: `{"code":"0","msg":"","data":[
			{"instId":"BTC-USDT-SWAP","pos":"5","posSide":"short","avgPx":"60000","markPx":"60010","upl":"-5","lever":"3","mgnMode":"cross"}
		]}`,
		pathInstruments: `{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USDT","state":"live","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"0.01","ctValCcy":"BTC"}]}`,
	})
	pos, err := a.FetchPositions(context.Background())
	if err != nil {
		t.Fatalf("FetchPositions: %v", err)
	}
	if len(pos) != 1 {
		t.Fatalf("应 1 条持仓, got %d", len(pos))
	}
	if pos[0].PositionAmt >= 0 {
		t.Errorf("short 应为负 size: %v", pos[0].PositionAmt)
	}
	if pos[0].Leverage != 3 {
		t.Errorf("杠杆解析错误: %v", pos[0].Leverage)
	}
}
