package app

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// TestPlanBinanceCapitalSweep 钉死币安清币计划:杂币卖出、借币负债买入还币、白名单(USDT/BNB)绝不动。
func TestPlanBinanceCapitalSweep(t *testing.T) {
	spotFree := map[string]float64{
		"ENA":  988,
		"BNB":  2.5, // 白名单:留着付手续费
		"USDT": 120, // 目标币
		"PEPE": 0,   // 零余额跳过
		"DOGE": 31.4159,
	}
	margin := CrossMarginAccountState{Assets: map[string]CrossMarginAssetBalance{
		"TAO":  {Asset: "TAO", Free: 0.01, Borrowed: 1.2, Interest: 0.003},
		"BNB":  {Asset: "BNB", Free: 0.5},                                   // 白名单不卖
		"USDT": {Asset: "USDT", Free: 300, Borrowed: 14.52, Interest: 0.01}, // 借的 USDT 要先还
		"ARB":  {Asset: "ARB", Free: 15},
	}}
	actions := planBinanceCapitalSweep(spotFree, margin, binanceSweepKeepAssets)

	byKey := map[string]capitalSweepAction{}
	for _, a := range actions {
		byKey[a.Account+"/"+a.Asset+"/"+a.Action] = a
	}
	if a, ok := byKey["spot/ENA/sell"]; !ok || a.Qty != 988 {
		t.Fatalf("应市价卖出现货 ENA 988, actions=%+v", actions)
	}
	if a, ok := byKey["spot/DOGE/sell"]; !ok || a.Qty != 31.4159 {
		t.Fatalf("应市价卖出现货 DOGE, actions=%+v", actions)
	}
	if a, ok := byKey["cross_margin/TAO/repay_buy"]; !ok || a.Qty < 1.203-1e-9 || a.Qty > 1.203+1e-9 {
		t.Fatalf("应买入 TAO 负债 1.203(借币+利息)还币, actions=%+v", actions)
	}
	if _, ok := byKey["cross_margin/ARB/sell"]; !ok {
		t.Fatalf("应卖出全仓 ARB 余额, actions=%+v", actions)
	}
	if a, ok := byKey["cross_margin/USDT/repay_direct"]; !ok || a.Qty < 14.53-1e-9 || a.Qty > 14.53+1e-9 {
		t.Fatalf("应用既有余额直接归还 USDT 借款 14.53(借款+利息), actions=%+v", actions)
	}
	for key := range byKey {
		if strings.Contains(key, "/BNB/") || strings.Contains(key, "/PEPE/") || strings.Contains(key, "/USDT/sell") {
			t.Fatalf("白名单/零余额/USDT 卖出不应出现在计划里: %s", key)
		}
	}
}

// TestPlanBybitCapitalSweep 钉死 Bybit 清币计划:杂币卖出,MNT/USDT 永不动。
func TestPlanBybitCapitalSweep(t *testing.T) {
	balances := venue.Balances{ByAsset: map[string]venue.Balance{
		"MBOX": {Free: 5610},
		"MNT":  {Free: 12}, // 白名单:手续费抵扣币
		"USDT": {Free: 88}, // 目标币
		"ROAM": {Free: 0},  // 零余额
	}}
	actions := planBybitCapitalSweep(balances, bybitSweepKeepAssets)
	if len(actions) != 1 {
		t.Fatalf("只应清扫 MBOX 一项, got %+v", actions)
	}
	if actions[0].Asset != "MBOX" || actions[0].Qty != 5610 || actions[0].Action != sweepActionSell {
		t.Fatalf("应市价卖出 MBOX 5610, got %+v", actions[0])
	}
}

// fakeSweepVenue 是 Bybit 清币测试桩。
type fakeSweepVenue struct {
	balances    venue.Balances
	instruments []venue.Instrument
	positions   []venue.Position
	placed      []venue.OrderRequest
}

func (f *fakeSweepVenue) FetchBalances(_ context.Context) (venue.Balances, error) {
	return f.balances, nil
}
func (f *fakeSweepVenue) FetchInstruments(_ context.Context) ([]venue.Instrument, error) {
	return f.instruments, nil
}
func (f *fakeSweepVenue) FetchPositions(_ context.Context) ([]venue.Position, error) {
	return f.positions, nil
}
func (f *fakeSweepVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	f.placed = append(f.placed, req)
	return venue.OrderResult{Status: "filled", ExecutedQty: req.Qty}, nil
}

// TestSweepBybitResidualAssetsSellsNonKeepCoins 钉死端到端:台账空仓时把非白名单余额按 step 向下取整市价卖出。
func TestSweepBybitResidualAssetsSellsNonKeepCoins(t *testing.T) {
	f := &fakeSweepVenue{
		balances: venue.Balances{ByAsset: map[string]venue.Balance{
			"MBOX": {Free: 5610.7},
			"MNT":  {Free: 12},
			"USDT": {Free: 88},
		}},
		instruments: []venue.Instrument{
			{Symbol: "MBOX-USDT", Market: venue.MarketSpot, StepSize: 1, BaseAsset: "MBOX"},
		},
	}
	report, err := sweepBybitResidualAssetsToUSDT(context.Background(), f, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("清币应成功: %v", err)
	}
	if len(f.placed) != 1 {
		t.Fatalf("应只下 1 笔 MBOX 卖单, placed=%+v", f.placed)
	}
	o := f.placed[0]
	if o.Symbol != "MBOX-USDT" || o.Side != venue.SideSell || o.Market != venue.MarketSpot || o.Qty != 5610 {
		t.Fatalf("应市价卖出 MBOX 5610(向下取整), got %+v", o)
	}
	joined := strings.Join(report, ";")
	if !strings.Contains(joined, "MBOX") {
		t.Fatalf("报告应包含 MBOX 卖出记录: %v", report)
	}
}

// TestSweepBybitClosesOrphanPerpPositions 钉死:台账为空但交易所有存量永续仓位(残腿/孤儿)时,
// 清币先把它市价 reduceOnly 平掉,再清现货杂币。
func TestSweepBybitClosesOrphanPerpPositions(t *testing.T) {
	f := &fakeSweepVenue{
		positions: []venue.Position{{Symbol: "DEXE-USDT", PositionAmt: -1.07}},
		balances:  venue.Balances{ByAsset: map[string]venue.Balance{"USDT": {Free: 60}}},
	}
	report, err := sweepBybitResidualAssetsToUSDT(context.Background(), f, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("清币应成功: %v", err)
	}
	if len(f.placed) != 1 {
		t.Fatalf("应只下 1 笔永续平仓单, placed=%+v", f.placed)
	}
	o := f.placed[0]
	if o.Market != venue.MarketPerp || o.Side != venue.SideBuy || !o.ReduceOnly || o.Qty != 1.07 {
		t.Fatalf("空头应市价买回 1.07 reduceOnly, got %+v", o)
	}
	if !strings.Contains(strings.Join(report, ";"), "DEXE") {
		t.Fatalf("报告应包含 DEXE 平仓记录: %v", report)
	}
}

// TestApplyTradingCapitalTargetSetsAllocated 钉死「页面调仓 127U 电报却报 60U」修复:
// 调仓目标必须同步进 AllocatedCapitalUSDT(Bybit 执行器唯一读取的分配资金字段)。
func TestApplyTradingCapitalTargetSetsAllocated(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.AllocatedCapitalUSDT = 60
	applyTradingCapitalTargetToConfig(&cfg, 127)
	if cfg.AllocatedCapitalUSDT != 127 {
		t.Fatalf("AllocatedCapitalUSDT 应更新为调仓目标 127, got %v", cfg.AllocatedCapitalUSDT)
	}
}

// TestVenueCapitalOverrideRoundTrip 钉死分配资金覆盖文件的写读与回退语义。
func TestVenueCapitalOverrideRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadVenueCapitalOverride(dir); ok {
		t.Fatalf("无覆盖文件时应回退 config")
	}
	if err := writeVenueCapitalOverride(dir, 127); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	v, ok := loadVenueCapitalOverride(dir)
	if !ok || v != 127 {
		t.Fatalf("覆盖读取应为 127, got %v ok=%v", v, ok)
	}
}

// TestCloseBybitLedgerPositionsForSweepClosesAndClears 钉死:调仓时台账在场持仓走正规平仓
// (CloseDeltaNeutral)并删台账,报告含实现盈亏;平仓后台账为空,后续清币不再被拒。
func TestCloseBybitLedgerPositionsForSweepClosesAndClears(t *testing.T) {
	dir := t.TempDir()
	store, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("newBybitPositionStore() error = %v", err)
	}
	pos := BybitLivePosition{Symbol: "MBOX-USDT", BaseQty: 10, SpotEntryPrice: 100, PerpEntryPrice: 100, IdemPrefix: "lx-MBOXUSDT-1", OpenedAt: time.Now().UTC().Add(-time.Hour)}
	if err := store.upsert(pos); err != nil {
		t.Fatalf("写台账失败: %v", err)
	}
	logs, err := NewRuntimeLogStore(filepath.Join(t.TempDir(), "runtime_events.jsonl"))
	if err != nil {
		t.Fatalf("NewRuntimeLogStore() error = %v", err)
	}
	tr := newFakeBybitTrader()
	tr.positions = []venue.Position{{Symbol: "MBOX-USDT", PositionAmt: -10}}
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"MBOX": {Free: 10}}}

	c := &RuntimeController{logger: log.Default(), logs: logs}
	c.cfg.NetworkConfig.RecordDir = dir

	report, err := c.closeBybitLedgerPositionsForSweep(context.Background(), tr)
	if err != nil {
		t.Fatalf("正规平仓应成功: %v", err)
	}
	if len(report) != 1 || !strings.Contains(report[0], "MBOX-USDT") {
		t.Fatalf("报告应含 MBOX 平仓记录: %v", report)
	}
	left, err := store.load()
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("平仓后台账应为空, got %+v", left)
	}
}

// TestSweepBybitRefusesWithOpenPositions 钉死安全闸:本地台账有在场持仓时拒绝清币(防卖掉对冲现货腿)。
func TestSweepBybitRefusesWithOpenPositions(t *testing.T) {
	dir := t.TempDir()
	store, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	if err := store.upsert(BybitLivePosition{Symbol: "MBOX-USDT", BaseQty: 5614, OpenedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("写台账失败: %v", err)
	}
	f := &fakeSweepVenue{balances: venue.Balances{ByAsset: map[string]venue.Balance{"MBOX": {Free: 5614}}}}
	if _, err := sweepBybitResidualAssetsToUSDT(context.Background(), f, dir, nil); err == nil {
		t.Fatal("台账有在场持仓时应拒绝清币")
	}
	if len(f.placed) != 0 {
		t.Fatalf("拒绝清币时不应下任何单, placed=%+v", f.placed)
	}
}
