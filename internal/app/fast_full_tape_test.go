package app

import (
	"testing"
	"time"
)

// TestFastFullTapeChunks 分片纯函数:片大小/片数上限/尾部截断。
func TestFastFullTapeChunks(t *testing.T) {
	syms := make([]string, 0, 370)
	for i := 0; i < 370; i++ {
		syms = append(syms, string(rune('A'+i%26))+"-USDT")
	}
	chunks := fastFullTapeChunks(syms, 150, 8)
	if len(chunks) != 3 {
		t.Fatalf("370币/150 应 3 片,得 %d", len(chunks))
	}
	if len(chunks[0]) != 150 || len(chunks[2]) != 70 {
		t.Fatalf("片大小错: %d/%d/%d", len(chunks[0]), len(chunks[1]), len(chunks[2]))
	}
	// 片数上限截断
	if got := fastFullTapeChunks(syms, 100, 2); len(got) != 2 {
		t.Fatalf("maxConns=2 应截断为 2 片,得 %d", len(got))
	}
	if fastFullTapeChunks(nil, 150, 8) != nil {
		t.Fatal("空清单应返回 nil")
	}
}

// TestParseOKXSwapSymbols SWAP tickers 解析:只收 USDT 本位,canon 形,排序。
func TestParseOKXSwapSymbols(t *testing.T) {
	body := []byte(`{"data":[
		{"instId":"ETH-USDT-SWAP"},
		{"instId":"BTC-USDT-SWAP"},
		{"instId":"BTC-USD-SWAP"},
		{"instId":"XRP-USDC-SWAP"}
	]}`)
	syms := parseOKXSwapSymbols(body)
	if len(syms) != 2 || syms[0] != "BTC-USDT" || syms[1] != "ETH-USDT" {
		t.Fatalf("解析错: %v", syms)
	}
	if parseOKXSwapSymbols([]byte("not json")) != nil {
		t.Fatal("坏 JSON 应返回 nil")
	}
}

// TestFastFullTapeInHot 增量视野标注:热度表 600s 粘性内=已覆盖。
func TestFastFullTapeInHot(t *testing.T) {
	fastHotSetMu.Lock()
	fastHotSet = map[string]time.Time{}
	fastHotSetMu.Unlock()
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	fastNoteHot("HOT-USDT", base.Add(-5*time.Minute))
	fastNoteHot("COLD-USDT", base.Add(-11*time.Minute))
	if !fastFullTapeInHot("HOT-USDT", base) {
		t.Fatal("5min 前热过(<600s 粘性)应算已覆盖")
	}
	if fastFullTapeInHot("COLD-USDT", base) {
		t.Fatal("11min 前热过(>600s)不应算已覆盖")
	}
	if fastFullTapeInHot("NEVER-USDT", base) {
		t.Fatal("从未热过不应算已覆盖")
	}
}
