package app

import (
	"fmt"
	"log"
	"path/filepath"
	"testing"
	"time"
)

func TestFastWxRestore60FromMarketMids(t *testing.T) {
	fastWx60Mu.Lock()
	fastWx60Hist = map[string][]fastMidPoint{}
	fastWx60Last = map[string]time.Time{}
	fastWx60Mu.Unlock()
	t.Cleanup(func() {
		fastWx60Mu.Lock()
		fastWx60Hist = map[string][]fastMidPoint{}
		fastWx60Last = map[string]time.Time{}
		fastWx60Mu.Unlock()
	})

	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	first := fastHotMidsRow{RecordedAt: now.Add(-55 * time.Minute), Mids: map[string]float64{}}
	last := fastHotMidsRow{RecordedAt: now, Mids: map[string]float64{}}
	for i := 0; i < 25; i++ {
		sym := fmt.Sprintf("T%d-USDT", i)
		first.Mids[sym] = 100
		last.Mids[sym] = 102
	}
	path := filepath.Join(dir, fastMarketMidsFile)
	if err := appendJSONLRecord(path, first); err != nil {
		t.Fatal(err)
	}
	if err := appendJSONLRecord(path, last); err != nil {
		t.Fatal(err)
	}
	e := &OKXDirectionalEngine{recordDir: dir, logger: log.Default()}
	e.fastWxRestore60(now)
	riot, breadth, ok := fastWxRiotBreadth(now)
	if !ok || riot != 1 || breadth != 1 {
		t.Fatalf("恢复后天气不正确: riot=%v breadth=%v ok=%v", riot, breadth, ok)
	}
}

func TestFastSwapVolumeUSDT(t *testing.T) {
	sym, got, ok := fastSwapVolumeUSDT("BTC-USDT-SWAP", 2, 60000)
	if !ok || sym != "BTC-USDT" || got != 120000 {
		t.Fatalf("sym=%q got=%v ok=%v", sym, got, ok)
	}
	if _, _, ok := fastSwapVolumeUSDT("BTC-USDT", 2, 60000); ok {
		t.Fatal("现货不应进入SWAP总量")
	}
}
