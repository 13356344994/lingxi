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

// TestFastXMidGapBps v5.5:价差计算与过期保护。
func TestFastXMidGapBps(t *testing.T) {
	now := time.Now().UTC()
	fastXMidMu.Lock()
	fastXMid["GAP-USDT"] = fastXMidPt{mid: 1.0000, ts: now.Add(-3 * time.Second)}
	fastXMid["STALE-USDT"] = fastXMidPt{mid: 1.0000, ts: now.Add(-30 * time.Second)}
	fastXMidMu.Unlock()
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
}
