package app

import (
	"testing"
	"time"
)

// TestParseBybitTickers v5.5:bybit tickers 解析(canon 规范化/bid-ask 中价/last 回退/非 USDT 拒)。
func TestParseBybitTickers(t *testing.T) {
	body := []byte(`{"retCode":0,"result":{"category":"linear","list":[
		{"symbol":"BTCUSDT","bid1Price":"62000","ask1Price":"62002","lastPrice":"62001"},
		{"symbol":"HMSTRUSDT","bid1Price":"","ask1Price":"","lastPrice":"0.00028"},
		{"symbol":"BTCPERP","bid1Price":"62000","ask1Price":"62002","lastPrice":"62001"},
		{"symbol":"BADUSDT","bid1Price":"0","ask1Price":"0","lastPrice":"0"}]}}`)
	m := parseBybitTickers(body)
	if len(m) != 2 {
		t.Fatalf("应解析 2 条, got %d: %v", len(m), m)
	}
	if m["BTC-USDT"] != 62001 {
		t.Fatalf("BTC mid want 62001, got %v", m["BTC-USDT"])
	}
	if m["HMSTR-USDT"] != 0.00028 {
		t.Fatalf("last 回退错: %v", m["HMSTR-USDT"])
	}
	if parseBybitTickers([]byte(`not json`)) != nil {
		t.Fatal("坏 payload 应返回 nil")
	}
}

// TestFastXMidGapBps v5.5/5.6:价差计算/过期保护/三锚标注。
func TestFastXMidGapBps(t *testing.T) {
	now := time.Now().UTC()
	fastXMidStore("bybit", map[string]float64{"GAP-USDT": 1.0000}, now.Add(-3*time.Second))
	fastXMidStore("bybit", map[string]float64{"STALE-USDT": 1.0000}, now.Add(-30*time.Second))
	fastXMidStore("gate", map[string]float64{"GAP-USDT": 1.0020}, now.Add(-2*time.Second))
	// OKX 1.0010 vs Bybit 1.0000 → +10bps(OKX 溢价)
	g, ok := fastXMidGapBps("GAP-USDT", 1.0010, now)
	if !ok || g < 9.9 || g > 10.1 {
		t.Fatalf("价差 want ≈+10bps, got %v ok=%v", g, ok)
	}
	// 过期(>15s)→ 不可用
	if _, ok := fastXMidGapBps("STALE-USDT", 1.0010, now); ok {
		t.Fatal("过期标尺应返回不可用")
	}
	// 无该币
	if _, ok := fastXMidGapBps("NONE-USDT", 1.0, now); ok {
		t.Fatal("无数据应返回不可用")
	}
	// 三锚标注:bybit +10bps、gate ≈-10bps、binance 无数据=nil
	var by, bn, gt *float64
	fastAnnotateXGaps("GAP-USDT", 1.0010, now, &by, &bn, &gt)
	if by == nil || *by < 9.9 || *by > 10.1 {
		t.Fatalf("bybit 锚错: %v", by)
	}
	if bn != nil {
		t.Fatal("binance 无数据应为 nil")
	}
	if gt == nil || *gt > -9.9 || *gt < -10.1 {
		t.Fatalf("gate 锚错(OKX 应折价≈-10bps): %v", gt)
	}
}

// TestParseBinanceGateTickers v5.6:Binance/Gate 批发 tickers 解析。
func TestParseBinanceGateTickers(t *testing.T) {
	bn := parseBinanceBookTickers([]byte(`[
		{"symbol":"BTCUSDT","bidPrice":"62000","askPrice":"62002"},
		{"symbol":"ETHUSD_PERP","bidPrice":"3000","askPrice":"3001"},
		{"symbol":"XUSDT","bidPrice":"0","askPrice":"0"}]`))
	if len(bn) != 1 || bn["BTC-USDT"] != 62001 {
		t.Fatalf("binance 解析错: %v", bn)
	}
	gt := parseGateTickers([]byte(`[
		{"contract":"BTC_USDT","highest_bid":"62000","lowest_ask":"62002","last":"62001"},
		{"contract":"HMSTR_USDT","highest_bid":"","lowest_ask":"","last":"0.00028"},
		{"contract":"BTC_USD","highest_bid":"1","lowest_ask":"2","last":"1"}]`))
	if len(gt) != 2 || gt["BTC-USDT"] != 62001 || gt["HMSTR-USDT"] != 0.00028 {
		t.Fatalf("gate 解析错: %v", gt)
	}
}
