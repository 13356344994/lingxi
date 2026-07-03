package app

// 爆仓单采集器单测(record-only 动机层):推送解析→落盘字段完整;坏数据不炸不落盘。

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
)

func newFastLiqTestEngine(t *testing.T) *OKXDirectionalEngine {
	t.Helper()
	cfg := core.DefaultEngineConfig().OKXDirectional
	cfg.FastLiqCollectorEnabled = true
	dir := t.TempDir()
	return &OKXDirectionalEngine{
		cfg:       cfg,
		venueName: "test_liq_" + t.Name(),
		recordDir: dir,
		logger:    log.New(io.Discard, "", 0),
		nowFn:     time.Now,
	}
}

func TestFastLiq_ParseAndPersist(t *testing.T) {
	e := newFastLiqTestEngine(t)
	payload := []byte(`{
		"arg":{"channel":"liquidation-orders","instType":"SWAP"},
		"data":[{
			"instId":"CELO-USDT-SWAP","instType":"SWAP",
			"details":[
				{"posSide":"long","side":"sell","sz":"13","bkPx":"0.2551","bkLoss":"0","ts":"1783000000000"},
				{"posSide":"short","side":"buy","sz":"2","bkPx":"0.2600","bkLoss":"1.5","ts":"1783000001000"}
			]
		}]
	}`)
	st := &fastWSState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{}}
	e.fastWSHandle(st, payload)

	raw, err := os.ReadFile(filepath.Join(e.recordDir, fastLiqEventFile))
	if err != nil {
		t.Fatalf("读事件文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 条事件,得到 %d", len(lines))
	}
	var ev fastLiqEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("事件不是合法 JSON: %v", err)
	}
	if ev.Symbol != "CELO-USDT" || ev.PosSide != "long" || ev.Side != "sell" || ev.Qty != 13 || ev.BkPx != 0.2551 {
		t.Fatalf("字段错: %+v", ev)
	}
	var ev2 fastLiqEvent
	_ = json.Unmarshal([]byte(lines[1]), &ev2)
	if ev2.PosSide != "short" || ev2.BkLoss != 1.5 || ev2.ExchTs != "1783000001000" {
		t.Fatalf("第二条字段错: %+v", ev2)
	}
}

func TestFastLiq_BadDataIgnored(t *testing.T) {
	e := newFastLiqTestEngine(t)
	st := &fastWSState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{}}
	// 坏 payload / 空 instId / 零张数:都不落盘、不 panic
	e.fastWSHandle(st, []byte(`{"arg":{"channel":"liquidation-orders"},"data":[{"instId":"","details":[{"sz":"5"}]}]}`))
	e.fastWSHandle(st, []byte(`{"arg":{"channel":"liquidation-orders"},"data":[{"instId":"X-USDT-SWAP","details":[{"sz":"0"}]}]}`))
	e.fastWSHandle(st, []byte(`not json`))
	if _, err := os.Stat(filepath.Join(e.recordDir, fastLiqEventFile)); !os.IsNotExist(err) {
		t.Fatalf("坏数据不应产生事件文件")
	}
}
