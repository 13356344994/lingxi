package app

import (
	"context"
	"log"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// TestAppendBybitSettlementWritesUnifiedSchema 钉死 PROJECT_AUDIT C-3 修复:Bybit 平仓后
// 必须写入与币安同 schema 的结算台账(settlements.json),让两所真实成本可用同一口径对比。
func TestAppendBybitSettlementWritesUnifiedSchema(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	c := &RuntimeController{store: store, logger: log.Default()}

	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	pos := BybitLivePosition{
		Symbol:          venue.Symbol("MBOX-USDT"),
		BaseQty:         5048,
		SpotEntryPrice:  0.00595,
		PerpEntryPrice:  0.00593,
		SpotNotional:    30.03,
		PerpNotional:    29.97,
		FundingRate:     0.0001,
		NextFundingTime: now.Add(-time.Minute),
		IdemPrefix:      "lx-MBOXUSDT-1781017200",
	}
	closeRes := bybitCloseResult{
		RealFunding:   0.012,
		SpotPricePnL:  0.5,
		PerpPricePnL:  -0.3,
		PricePnL:      0.2,
		TotalFeesUSDT: 0.08,
		RealizedPnL:   0.132,
		FundingOK:     true,
	}

	c.appendBybitSettlement(pos, closeRes, 4, now)

	records, err := store.ListSettlementRecords(10)
	if err != nil {
		t.Fatalf("ListSettlementRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("期望 1 条结算记录, 实际 %d", len(records))
	}
	rec := records[0]
	if rec.CycleID != "lx-MBOXUSDT-1781017200" {
		t.Fatalf("CycleID 应为幂等前缀, 实际 %q", rec.CycleID)
	}
	if rec.FundingIncome != "0.012" {
		t.Fatalf("FundingIncome 应为真实资金费, 实际 %q", rec.FundingIncome)
	}
	if rec.NetPnL != "0.132" {
		t.Fatalf("NetPnL 应为实现盈亏, 实际 %q", rec.NetPnL)
	}
	if rec.FundingIncomePending {
		t.Fatal("FundingOK=true 时不应标记资金费待结")
	}
	if rec.SpotTradingPnL != "0.5" || rec.FuturesTradingPnL != "-0.3" {
		t.Fatalf("两腿价差盈亏拆分错误: spot=%q perp=%q", rec.SpotTradingPnL, rec.FuturesTradingPnL)
	}
}

// TestAppendBybitSettlementFundingPendingFallsBackToEstimate 资金费流水查不到时退回估算口径并标 pending。
func TestAppendBybitSettlementFundingPendingFallsBackToEstimate(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	c := &RuntimeController{store: store, logger: log.Default()}

	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	pos := BybitLivePosition{
		Symbol:       venue.Symbol("MBOX-USDT"),
		PerpNotional: 100,
		FundingRate:  -0.0002, // 负 carry 也按 |费率| 估收入
		Negative:     true,
	}
	c.appendBybitSettlement(pos, bybitCloseResult{FundingOK: false}, 2, now)

	records, err := store.ListSettlementRecords(10)
	if err != nil {
		t.Fatalf("ListSettlementRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("期望 1 条结算记录, 实际 %d", len(records))
	}
	rec := records[0]
	if !rec.FundingIncomePending {
		t.Fatal("FundingOK=false 时必须标记资金费待结(估算口径)")
	}
	if rec.FundingIncome != "0.04" { // 2 × 0.0002 × 100
		t.Fatalf("估算资金费错误: %q", rec.FundingIncome)
	}
}

// stubBybitVenue 是最小 bybitFullVenue 桩:只有 QueryOrder 可注入,其余方法返回零值。
type stubBybitVenue struct {
	queryOrder func(m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
}

func (s *stubBybitVenue) FetchFundingSnapshot(context.Context) ([]venue.FundingSnapshot, error) {
	return nil, nil
}
func (s *stubBybitVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return nil, nil
}
func (s *stubBybitVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{}, nil
}
func (s *stubBybitVenue) FetchTradingCostProfile(context.Context, []venue.Symbol) (venue.CostProfile, error) {
	return venue.CostProfile{}, nil
}
func (s *stubBybitVenue) PlaceOrder(context.Context, venue.OrderRequest) (venue.OrderResult, error) {
	return venue.OrderResult{}, nil
}
func (s *stubBybitVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}
func (s *stubBybitVenue) QueryOrder(_ context.Context, m venue.Market, sym venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	return s.queryOrder(m, sym, clientOrderID)
}
func (s *stubBybitVenue) FetchPositions(context.Context) ([]venue.Position, error) { return nil, nil }
func (s *stubBybitVenue) FetchBalances(context.Context) (venue.Balances, error) {
	return venue.Balances{}, nil
}
func (s *stubBybitVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return nil, nil
}

// TestBybitProbeOrphanOwnershipAdoptsOwnOrder 钉死孤儿仓收编(PROJECT_AUDIT C-10):
// 未登记永续持仓若能用确定性 orderLinkId 回查到本机已成交开仓单,应收编为「立即到期待平」记录。
func TestBybitProbeOrphanOwnershipAdoptsOwnOrder(t *testing.T) {
	sym := venue.Symbol("MBOX-USDT")
	funding := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	ownID := bybitLegClientID(bybitIdemPrefix(sym, funding), "op")

	stub := &stubBybitVenue{queryOrder: func(_ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
		if clientOrderID == ownID {
			return venue.OrderResult{Status: "filled", ExecutedQty: 5048}, nil
		}
		return venue.OrderResult{Status: "unknown"}, nil
	}}
	c := &RuntimeController{logger: log.Default()}
	adopted, ok := c.bybitProbeOrphanOwnership(context.Background(), stub, venue.Position{
		Symbol: sym, PositionAmt: 5048, EntryPrice: 0.006,
	})
	if !ok {
		t.Fatal("命中本机 orderLinkId 的孤儿仓必须被收编")
	}
	if !adopted.Negative {
		t.Fatal("永续多头孤儿仓应识别为负 carry 持仓")
	}
	if !adopted.ForceClose {
		t.Fatal("收编持仓必须标记 ForceClose(不可续持,直接平)")
	}
	if adopted.SettlementsHeld != 0 {
		t.Fatalf("收编持仓结算次数应为真实口径 0(不再用 1<<30 哨兵污染台账), 实际 %d", adopted.SettlementsHeld)
	}
	if !adopted.NextFundingTime.Before(time.Now().UTC()) {
		t.Fatal("收编持仓必须立即到期待平")
	}
	if adopted.BaseQty != 5048 || adopted.PerpEntryPrice != 0.006 {
		t.Fatalf("收编持仓字段错误: %+v", adopted)
	}
}

// TestBybitProbeOrphanOwnershipLeavesUnknownAlone 回查不命中(人工持仓)绝不收编。
func TestBybitProbeOrphanOwnershipLeavesUnknownAlone(t *testing.T) {
	stub := &stubBybitVenue{queryOrder: func(_ venue.Market, _ venue.Symbol, _ string) (venue.OrderResult, error) {
		return venue.OrderResult{Status: "unknown"}, nil
	}}
	c := &RuntimeController{logger: log.Default()}
	if _, ok := c.bybitProbeOrphanOwnership(context.Background(), stub, venue.Position{
		Symbol: venue.Symbol("BTC-USDT"), PositionAmt: -1, EntryPrice: 50000,
	}); ok {
		t.Fatal("回查不命中的持仓不得收编(可能是人工持仓)")
	}
}

// TestBybitPersistPositionFallsBackToMemory 钉死台账落盘失败的内存兜底(PROJECT_AUDIT C-10):
// upsert 失败 → 进入 pending 队列;flush 时磁盘恢复 → 补落盘;磁盘仍坏 → 并入管理视图不失管。
func TestBybitPersistPositionFallsBackToMemory(t *testing.T) {
	dir := t.TempDir()
	store, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("newBybitPositionStore() error = %v", err)
	}
	logs, err := NewRuntimeLogStore(filepath.Join(t.TempDir(), "runtime_events.jsonl"))
	if err != nil {
		t.Fatalf("NewRuntimeLogStore() error = %v", err)
	}
	c := &RuntimeController{logger: log.Default(), logs: logs}
	pos := BybitLivePosition{Symbol: venue.Symbol("MBOX-USDT"), BaseQty: 1}

	// 让落盘失败:把台账路径变成目录。
	badStore, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("newBybitPositionStore() error = %v", err)
	}
	badStore.path = dir // 写文件到目录路径必失败
	if c.bybitPersistPosition(badStore, pos, "新开持仓") {
		t.Fatal("写目录路径必须失败")
	}
	if len(c.bybitPendingPersist) != 1 {
		t.Fatalf("失败持仓必须进入内存兜底, 实际 %d", len(c.bybitPendingPersist))
	}

	// 磁盘仍坏:flush 后持仓必须仍出现在管理视图。
	merged := c.bybitFlushPendingPersist(badStore, nil)
	if len(merged) != 1 || merged[0].Symbol != pos.Symbol {
		t.Fatalf("磁盘故障时内存兜底持仓必须并入管理视图, 实际 %+v", merged)
	}

	// 磁盘恢复:flush 应补落盘并清空兜底。
	merged = c.bybitFlushPendingPersist(store, nil)
	if len(merged) != 1 {
		t.Fatalf("补落盘后持仓应在视图中, 实际 %+v", merged)
	}
	if len(c.bybitPendingPersist) != 0 {
		t.Fatal("补落盘成功后内存兜底应清空")
	}
	saved, err := store.load()
	if err != nil || len(saved) != 1 {
		t.Fatalf("补落盘后磁盘应有 1 笔持仓, err=%v n=%d", err, len(saved))
	}
}
