package app

import (
	"testing"
	"time"
)

// TestParseBybitTickers v5.5/5.7:bybit tickers 解析(canon 规范化/bid-ask 中价/last 回退/
// 非 USDT 拒/富字段 funding/OI 免费提取)。
func TestParseBybitTickers(t *testing.T) {
	now := time.Now().UTC()
	body := []byte(`{"retCode":0,"result":{"category":"linear","list":[
		{"symbol":"BTCUSDT","bid1Price":"62000","ask1Price":"62002","lastPrice":"62001","fundingRate":"0.0001","openInterest":"5000","indexPrice":"62010","markPrice":"62005"},
		{"symbol":"HMSTRUSDT","bid1Price":"","ask1Price":"","lastPrice":"0.00028","fundingRate":"-0.002","openInterest":"1000000"},
		{"symbol":"BTCPERP","bid1Price":"62000","ask1Price":"62002","lastPrice":"62001"},
		{"symbol":"BADUSDT","bid1Price":"0","ask1Price":"0","lastPrice":"0"}]}}`)
	mids, metas := parseBybitTickers(body, now)
	if len(mids) != 2 || mids["BTC-USDT"] != 62001 || mids["HMSTR-USDT"] != 0.00028 {
		t.Fatalf("mids 解析错: %v", mids)
	}
	m := metas["BTC-USDT"]
	if m.Funding != 0.0001 || m.OI != 5000 || m.Index != 62010 || m.Mark != 62005 {
		t.Fatalf("富字段解析错: %+v", m)
	}
	if metas["HMSTR-USDT"].Funding != -0.002 {
		t.Fatalf("负资费解析错: %+v", metas["HMSTR-USDT"])
	}
	if a, _ := parseBybitTickers([]byte(`not json`), now); a != nil {
		t.Fatal("坏 payload 应返回 nil")
	}
}

// TestParseBinanceGateOKX v5.6/5.7:Binance/Gate/OKX现货/OKX OI 批发解析。
func TestParseBinanceGateOKX(t *testing.T) {
	now := time.Now().UTC()
	bn := parseBinanceBookTickers([]byte(`[
		{"symbol":"BTCUSDT","bidPrice":"62000","askPrice":"62002"},
		{"symbol":"ETHUSD_PERP","bidPrice":"3000","askPrice":"3001"},
		{"symbol":"XUSDT","bidPrice":"0","askPrice":"0"}]`))
	if len(bn) != 1 || bn["BTC-USDT"] != 62001 {
		t.Fatalf("binance 解析错: %v", bn)
	}
	gt, gtm := parseGateTickers([]byte(`[
		{"contract":"BTC_USDT","highest_bid":"62000","lowest_ask":"62002","last":"62001","funding_rate_indicative":"0.0003","index_price":"62010"},
		{"contract":"HMSTR_USDT","highest_bid":"","lowest_ask":"","last":"0.00028"},
		{"contract":"BTC_USD","highest_bid":"1","lowest_ask":"2","last":"1"}]`), now)
	if len(gt) != 2 || gt["BTC-USDT"] != 62001 || gt["HMSTR-USDT"] != 0.00028 {
		t.Fatalf("gate mids 解析错: %v", gt)
	}
	if gtm["BTC-USDT"].Funding != 0.0003 || gtm["BTC-USDT"].Index != 62010 {
		t.Fatalf("gate 富字段错: %+v", gtm["BTC-USDT"])
	}
	sp := parseOKXSpotTickers([]byte(`{"data":[
		{"instId":"BTC-USDT","bidPx":"62000","askPx":"62002","last":"62001"},
		{"instId":"BTC-USDC","bidPx":"62000","askPx":"62002","last":"62001"}]}`))
	if len(sp) != 1 || sp["BTC-USDT"] != 62001 {
		t.Fatalf("okx 现货解析错: %v", sp)
	}
	oi := parseOKXOpenInterest([]byte(`{"data":[
		{"instId":"BTC-USDT-SWAP","oi":"123456"},
		{"instId":"BTC-USD-SWAP","oi":"999"}]}`), now)
	if len(oi) != 1 || oi["BTC-USDT"].OI != 123456 {
		t.Fatalf("okx OI 解析错: %v", oi)
	}
}

// TestFastXMidGapBps v5.5-5.7:价差计算/过期保护/四锚标注/快照。
func TestFastXMidGapBps(t *testing.T) {
	now := time.Now().UTC()
	fastXMidStore("bybit", map[string]float64{"GAP-USDT": 1.0000}, now.Add(-3*time.Second))
	fastXMidStore("bybit", map[string]float64{"STALE-USDT": 1.0000}, now.Add(-30*time.Second))
	fastXMidStore("gate", map[string]float64{"GAP-USDT": 1.0020}, now.Add(-2*time.Second))
	fastXMidStore("okxspot", map[string]float64{"GAP-USDT": 1.0005}, now.Add(-2*time.Second))
	g, ok := fastXMidGapBps("GAP-USDT", 1.0010, now)
	if !ok || g < 9.9 || g > 10.1 {
		t.Fatalf("价差 want ≈+10bps, got %v ok=%v", g, ok)
	}
	if _, ok := fastXMidGapBps("STALE-USDT", 1.0010, now); ok {
		t.Fatal("过期标尺应返回不可用")
	}
	if _, ok := fastXMidGapBps("NONE-USDT", 1.0, now); ok {
		t.Fatal("无数据应返回不可用")
	}
	var by, bn, gt, spot *float64
	fastAnnotateXGaps("GAP-USDT", 1.0010, now, &by, &bn, &gt, &spot)
	if by == nil || *by < 9.9 || *by > 10.1 {
		t.Fatalf("bybit 锚错: %v", by)
	}
	if bn != nil {
		t.Fatal("binance 无数据应为 nil")
	}
	if gt == nil || *gt > -9.9 || *gt < -10.1 {
		t.Fatalf("gate 锚错(OKX 应折价≈-10bps): %v", gt)
	}
	if spot == nil || *spot < 4.9 || *spot > 5.1 {
		t.Fatalf("现货基差锚错(应≈+5bps): %v", spot)
	}
	// 快照:含 funding/OI,过期(>10min)剔除
	fastXMetaStore("bybit", map[string]fastXTickMeta{
		"GAP-USDT":  {Funding: 0.0001, OI: 5000, TS: now.Add(-1 * time.Minute)},
		"OLD-USDT":  {Funding: 0.0002, OI: 1, TS: now.Add(-20 * time.Minute)},
	})
	row := fastXMetaSnapshot(now)
	if v, ok := row.Venues["bybit"]["GAP-USDT"]; !ok || v[0] != 0.0001 || v[1] != 5000 {
		t.Fatalf("快照字段错: %v", row.Venues["bybit"])
	}
	if _, ok := row.Venues["bybit"]["OLD-USDT"]; ok {
		t.Fatal("过期 meta 不应进快照")
	}
}
