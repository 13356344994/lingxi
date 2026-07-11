package replay

import (
	"strings"
	"testing"
	"time"
)

// 与 settlements.jsonl 写盘端字段名一致的小样本(内存构造,不读真实巨量文件)。
const settlementSample = `{"time":"2026-07-10T12:02:00Z","symbol":"BTC-USDT-SWAP","side":"long","entry":100,"exit":101,"notional":1000,"realized_usdt":9,"hold_seconds":120,"fee_usdt":0.8,"entry_slip_bps":5,"exit_slip_bps":5,"open_via_taker":true,"fast_event_id":"ev1","regime":"storm","reason":"tp"}
not-json-garbage
{"time":"2026-07-10T12:10:00Z","symbol":"ETH-USDT-SWAP","side":"short","entry":50,"exit":49,"notional":500,"realized_usdt":-4,"hold_minutes":3,"fee_usdt":0.4}`

func TestLoadSettlementTrades(t *testing.T) {
	trades, stats, err := LoadSettlementTrades(strings.NewReader(settlementSample), 0)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if stats.Lines != 3 || stats.Decoded != 2 || stats.Malformed != 1 || stats.Truncated {
		t.Fatalf("坏行应跳过不中断: %+v", stats)
	}
	if len(trades) != 2 {
		t.Fatalf("want 2 trades got %d", len(trades))
	}

	tr := trades[0]
	if tr.Side != SideLong || tr.Style != ExecTaker || tr.EpisodeID != "ev1" {
		t.Fatalf("字段映射错误: %+v", tr)
	}
	wantOpen := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if !tr.OpenTime.Equal(wantOpen) {
		t.Fatalf("hold_seconds 应反推 OpenTime: want %v got %v", wantOpen, tr.OpenTime)
	}
	// entry_slip 5bps(多头买贵)→ EntryMid = 100/1.0005。
	if !almostEqual(tr.EntryMid, 100/1.0005, 1e-9) {
		t.Fatalf("EntryMid 反推错误: got %v", tr.EntryMid)
	}
	r := ReplayTrade(tr)
	if !almostEqual(r.NetUSDT, 9-0.8, 1e-9) {
		t.Fatalf("realized 覆盖毛 PnL 后 net want 8.2 got %v", r.NetUSDT)
	}
	if r.EntrySlipUSDT <= 0 || r.ExitSlipUSDT <= 0 {
		t.Fatalf("正 slip bps 应归因为正滑点成本: %+v", r)
	}

	tr2 := trades[1]
	if tr2.Style != ExecMaker || !tr2.OpenTime.Equal(tr2.CloseTime.Add(-3*time.Minute)) {
		t.Fatalf("第二行映射错误: %+v", tr2)
	}
	if r2 := ReplayTrade(tr2); !almostEqual(r2.NetUSDT, -4.4, 1e-9) {
		t.Fatalf("net want -4.4 got %v", ReplayTrade(tr2).NetUSDT)
	}
}

func TestDecodeJSONLMaxLinesAndEarlyStop(t *testing.T) {
	_, stats, err := LoadSettlementTrades(strings.NewReader(settlementSample), 1)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if stats.Decoded != 1 || !stats.Truncated {
		t.Fatalf("maxLines=1 应截断: %+v", stats)
	}

	var seen int
	stats2, err := DecodeJSONL(strings.NewReader(settlementSample), 0, func(SettlementRecord) bool {
		seen++
		return false // accept 返回 false 提前停止
	})
	if err != nil || seen != 1 || !stats2.Truncated {
		t.Fatalf("accept=false 应提前停止: seen=%d stats=%+v err=%v", seen, stats2, err)
	}
}

const makerAttemptSample = `{"recorded_at":"2026-07-10T12:00:00Z","venue":"okx","symbol":"SOL-USDT-SWAP","side":"long","maker_order_price":99.9,"maker_wait_ms":800,"maker_filled":false,"fill_mid":100,"spread_bps":4,"entry_confirm_path":"maker_not_filled"}
{"recorded_at":"2026-07-10T12:01:00Z","venue":"okx","symbol":"SOL-USDT-SWAP","side":"long","maker_order_price":99.9,"maker_filled":true,"fill_price":99.95,"fill_mid":100,"maker_edge_bps":5,"entry_confirm_path":"poll_fill"}`

func TestMakerAttemptRecordMapping(t *testing.T) {
	var recs []MakerAttemptRecord
	stats, err := DecodeJSONL(strings.NewReader(makerAttemptSample), 0, func(r MakerAttemptRecord) bool {
		recs = append(recs, r)
		return true
	})
	if err != nil || stats.Decoded != 2 {
		t.Fatalf("解码失败: stats=%+v err=%v", stats, err)
	}

	unfilled := recs[0].Trade(1000)
	if unfilled.FillRatio != 0 || unfilled.Style != ExecMaker || unfilled.EntryMid != 100 {
		t.Fatalf("未成交尝试映射错误: %+v", unfilled)
	}
	// 补齐出场 mid 后可回放为未成交反事实:mid 100→101 的 1% × 1000 = 10。
	unfilled.ExitMid = 101
	if r := ReplayTrade(unfilled); !r.Unfilled || !almostEqual(r.CounterfactualGrossUSDT, 10, 1e-9) {
		t.Fatalf("未成交反事实 want 10 got %+v", r)
	}

	filled := recs[1].Trade(1000)
	if filled.FillRatio != 1 || filled.EntryPrice != 99.95 {
		t.Fatalf("成交尝试应取 fill_price: %+v", filled)
	}
}
