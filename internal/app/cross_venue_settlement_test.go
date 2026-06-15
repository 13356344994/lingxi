package app

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

func TestAggregateCrossVenueSettlementsCSTDayBoundary(t *testing.T) {
	// 锚点:2026-06-15 10:30 CST = 2026-06-15 02:30 UTC
	now := time.Date(2026, 6, 15, 2, 30, 0, 0, time.UTC)
	dayStart := beijingCSTDayStart(now)
	// 北京 00:00 = UTC 前一日 16:00
	wantDayStart := time.Date(2026, 6, 14, 16, 0, 0, 0, time.UTC)
	if !dayStart.Equal(wantDayStart) {
		t.Fatalf("dayStart=%v want %v", dayStart, wantDayStart)
	}

	records := []CrossVenueSettlement{
		// 昨日 23:00 CST → 不计入今日
		{Symbol: "A-USDT", CloseTime: time.Date(2026, 6, 14, 15, 0, 0, 0, time.UTC), NetPnL: 10},
		// 今日 00:00 CST(=16:00 UTC) → 计入
		{Symbol: "B-USDT", CloseTime: time.Date(2026, 6, 14, 16, 0, 0, 0, time.UTC), NetPnL: 1.5},
		// 今日 04:00 CST → 计入
		{Symbol: "C-USDT", CloseTime: time.Date(2026, 6, 14, 20, 0, 0, 0, time.UTC), NetPnL: -0.25},
		// 未来(>now) → 不计入
		{Symbol: "D-USDT", CloseTime: time.Date(2026, 6, 15, 3, 0, 0, 0, time.UTC), NetPnL: 99},
	}

	sum := aggregateCrossVenueSettlementsInRange(records, dayStart, now)
	if sum.TradeCount != 2 {
		t.Fatalf("trade_count=%d want 2", sum.TradeCount)
	}
	wantNet := 1.25
	if math.Abs(sum.TotalNetPnL-wantNet) > 1e-9 {
		t.Fatalf("total_net_pnl=%v want %v", sum.TotalNetPnL, wantNet)
	}
	if math.Abs(sum.BySymbol["B-USDT"]-1.5) > 1e-9 || math.Abs(sum.BySymbol["C-USDT"]-(-0.25)) > 1e-9 {
		t.Fatalf("by_symbol=%v", sum.BySymbol)
	}
	if _, ok := sum.BySymbol["A-USDT"]; ok {
		t.Fatalf("yesterday record must be excluded")
	}
}

func TestAggregateCrossVenueSettlementsByVenueInRange(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 30, 0, 0, time.UTC)
	dayStart := beijingCSTDayStart(now)

	records := []CrossVenueSettlement{
		// 昨日 → 不计
		{
			Symbol: "OLD-USDT", HighVenue: "binance", LowVenue: "gate",
			CloseTime: dayStart.Add(-time.Minute), NetPnL: 99,
			HighLegFunding: 1, LowLegFunding: 1, TotalFees: 0.1,
		},
		// 今日 binance/gate: net = funding(2+3) + trade(1) - fees(0.4) - rollback(0.2) - rebalance(0.4) = 5.0
		{
			Symbol: "BTC-USDT", HighVenue: "binance", LowVenue: "gate",
			CloseTime: dayStart.Add(30 * time.Minute),
			HighLegFunding: 2, LowLegFunding: 3, TradePnL: 1,
			TotalFees: 0.4, RollbackCost: 0.2, RebalanceCost: 0.4, NetPnL: 5.0,
		},
		// 今日 okx/bybit: net = 0.5 + (-0.1) + 0 - 0.2 = 0.2
		{
			Symbol: "ETH-USDT", HighVenue: "okx", LowVenue: "bybit",
			CloseTime: dayStart.Add(2 * time.Hour),
			HighLegFunding: 0.5, LowLegFunding: -0.1, TotalFees: 0.2, NetPnL: 0.2,
		},
		// 未来 → 不计
		{
			Symbol: "FUT-USDT", HighVenue: "binance", LowVenue: "gate",
			CloseTime: now.Add(time.Hour), NetPnL: 50,
		},
	}

	sum := aggregateCrossVenueSettlementsByVenueInRange(records, dayStart, now)
	if sum.TradeCount != 2 {
		t.Fatalf("trade_count=%d want 2", sum.TradeCount)
	}
	wantTotal := 5.2
	if math.Abs(sum.TotalNetPnL-wantTotal) > 1e-9 {
		t.Fatalf("total_net_pnl=%v want %v", sum.TotalNetPnL, wantTotal)
	}

	// binance high leg: 2 + (1 - 0.4 - 0.2 - 0.4)/2 = 2 + 0 = 2.0
	wantBinance := 2.0
	if math.Abs(sum.ByVenue["binance"]-wantBinance) > 1e-9 {
		t.Fatalf("binance=%v want %v", sum.ByVenue["binance"], wantBinance)
	}
	// gate low leg: 3 + 0 = 3.0
	wantGate := 3.0
	if math.Abs(sum.ByVenue["gate"]-wantGate) > 1e-9 {
		t.Fatalf("gate=%v want %v", sum.ByVenue["gate"], wantGate)
	}
	// okx: 0.5 + (0 - 0.2)/2 = 0.4
	wantOKX := 0.4
	if math.Abs(sum.ByVenue["okx"]-wantOKX) > 1e-9 {
		t.Fatalf("okx=%v want %v", sum.ByVenue["okx"], wantOKX)
	}
	// bybit: -0.1 + (-0.1) = -0.2
	wantBybit := -0.2
	if math.Abs(sum.ByVenue["bybit"]-wantBybit) > 1e-9 {
		t.Fatalf("bybit=%v want %v", sum.ByVenue["bybit"], wantBybit)
	}

	venueSum := 0.0
	for _, v := range sum.ByVenue {
		venueSum = money.SumUSDT(venueSum, v)
	}
	if math.Abs(venueSum-wantTotal) > 1e-9 {
		t.Fatalf("by_venue sum=%v want total=%v (attribution must conserve net_pnl)", venueSum, wantTotal)
	}
}

func TestTodayCSTEarningsFromLedger(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC) // 14:00 CST
	e := &CrossVenueExecutor{
		recordDir: dir,
		nowFn:     func() time.Time { return now },
	}
	dayStart := beijingCSTDayStart(now)
	appendSettlement := func(sym string, close time.Time, net float64) {
		rec := CrossVenueSettlement{Symbol: sym, CloseTime: close, NetPnL: net}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		path := e.settlementsLedgerPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	appendSettlement("OLD", dayStart.Add(-time.Minute), 5)
	appendSettlement("NEW", dayStart.Add(time.Minute), 2.2)

	got, err := e.TodayCSTEarnings(now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TradeCount != 1 || math.Abs(got.TotalNetPnL-2.2) > 1e-9 {
		t.Fatalf("earnings=%+v want count=1 net=2.2", got)
	}
}

func TestBuildCrossVenuePositionDetails(t *testing.T) {
	opened := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	now := opened.Add(90 * time.Minute)
	ps := []CrossVenuePosition{
		{Symbol: "BTC-USDT", Status: CrossVenueStatusClosed},
		{
			Symbol: "ETH-USDT", Status: CrossVenueStatusOpen,
			HighVenue: "bybit", LowVenue: "okx",
			HighLeg: CrossVenueLegState{Side: "sell", Qty: 0.5, EntryPrice: 2500},
			LowLeg:  CrossVenueLegState{Side: "buy", Qty: 0.5, EntryPrice: 2498},
			NotionalUSDT: 1250, OpenSpreadAPR: 0.42, EstFundingUSDT: 1.23,
			OpenedAt: opened,
		},
	}
	details := buildCrossVenuePositionDetails(ps, now)
	if len(details) != 1 {
		t.Fatalf("details=%d want 1 active", len(details))
	}
	d := details[0]
	if d.Symbol != "ETH-USDT" || d.HighLegQty != 0.5 || d.HoldDurationSec != 5400 {
		t.Fatalf("detail=%+v", d)
	}
}

func TestBuildCrossVenueSettlement_NetPnLDoesNotDoubleCountSlippage(t *testing.T) {
	// 模拟 delta-neutral 对冲:高腿空 entry=100 exit=99.5,低腿多 entry=100 exit=100.5,各 1 币
	// 滑点字段为负(执行劣于 mid),但 trade_pnl 已用实际成交价,net_pnl 不得再扣滑点。
	pos := CrossVenuePosition{
		Symbol:             "BTC-USDT",
		HighVenue:          "okx",
		LowVenue:           "gate",
		NotionalUSDT:       100,
		HighLegFundingUSDT: 0.5,
		LowLegFundingUSDT:  -0.1,
		OpenedAt:           time.Now().Add(-24 * time.Hour),
		CloseReason:        "test",
		HighLeg: CrossVenueLegState{
			EntryPrice: 100,
			ExitPrice:  99.5,
			ClosedQty:  1,
		},
		LowLeg: CrossVenueLegState{
			EntryPrice: 100,
			ExitPrice:  100.5,
			ClosedQty:  1,
		},
		CrossVenueAttribution: CrossVenueAttribution{
			OpenFeesUSDT:      0.04,
			CloseFeesUSDT:     0.04,
			OpenSlippageUSDT:  -0.20, // 仅展示
			CloseSlippageUSDT: -0.15, // 仅展示
		},
	}
	rec := buildCrossVenueSettlement(pos, time.Now())

	// trade_pnl: 高腿 (100-99.5)*1=0.5, 低腿 (100.5-100)*1=0.5 → 1.0
	wantTrade := 1.0
	if math.Abs(rec.TradePnL-wantTrade) > 1e-9 {
		t.Fatalf("trade_pnl=%v want %v", rec.TradePnL, wantTrade)
	}
	wantFunding := 0.4
	if math.Abs(rec.RealizedFunding-wantFunding) > 1e-9 {
		t.Fatalf("realized_funding=%v want %v", rec.RealizedFunding, wantFunding)
	}
	wantFees := 0.08
	if math.Abs(rec.TotalFees-wantFees) > 1e-9 {
		t.Fatalf("total_fees=%v want %v", rec.TotalFees, wantFees)
	}
	// net = 0.4 + 1.0 - 0.08 = 1.32 (滑点 -0.35 不参与)
	wantNet := 1.32
	if math.Abs(rec.NetPnL-wantNet) > 1e-9 {
		t.Fatalf("net_pnl=%v want %v (must not subtract slippage again)", rec.NetPnL, wantNet)
	}
	// 若错误地再扣滑点,net 会偏离正确值
	wrongNet := rec.NetPnL + rec.OpenSlippage + rec.CloseSlippage
	if math.Abs(wrongNet-wantNet) < 1e-6 {
		t.Fatalf("net_pnl appears to include slippage adjustment: got %v", rec.NetPnL)
	}
}

func TestBuildCrossVenueSettlement_RollbackCost(t *testing.T) {
	// 第一腿空 entry=100,回滚 buy=102(价涨,空腿真亏 2),开仓费 0.05、回滚费 0.05。
	// 价格亏损 (100-102)*1=-2 由 trade_pnl 承载;rollback_cost 只记回滚手续费 0.05,不得再含价格 PnL
	// (否则与 trade_pnl 抵消,真实裸腿亏损被抹掉)。真实净利 = -2 - 0.05(开) - 0.05(回滚) = -2.10。
	pos := CrossVenuePosition{
		Symbol:    "ETH-USDT",
		HighVenue: "okx",
		LowVenue:  "gate",
		HighLeg: CrossVenueLegState{
			EntryPrice: 100,
			ExitPrice:  102,
			ClosedQty:  1,
		},
		CrossVenueAttribution: CrossVenueAttribution{
			OpenFeesUSDT:     0.05,
			RollbackCostUSDT: 0.05, // 仅回滚手续费
		},
	}
	rec := buildCrossVenueSettlement(pos, time.Now())
	if math.Abs(rec.RollbackCost-0.05) > 1e-9 {
		t.Fatalf("rollback_cost=%v want 0.05 (fee only)", rec.RollbackCost)
	}
	// trade_pnl from high leg: (100-102)*1 = -2
	if math.Abs(rec.TradePnL-(-2.0)) > 1e-9 {
		t.Fatalf("trade_pnl=%v want -2", rec.TradePnL)
	}
	// net = 0 + (-2) - 0.05(open) - 0.05(rollback) = -2.10
	wantNet := -2.10
	if math.Abs(rec.NetPnL-wantNet) > 1e-9 {
		t.Fatalf("net_pnl=%v want %v", rec.NetPnL, wantNet)
	}
}

func TestRecordCrossVenueExecution_RollbackAccumulates(t *testing.T) {
	pos := CrossVenuePosition{
		HighLeg: CrossVenueLegState{EntryPrice: 100},
	}
	e := &CrossVenueExecutor{}
	res := venue.OrderResult{
		ExecutedQty: 1,
		AvgPrice:    101,
		Fee:         0.1,
		FeeAsset:    "USDT",
	}
	e.recordCrossVenueExecution(&pos, cvExecRollbackHigh, res, 100)
	// rollback_cost 只记回滚手续费(价格 PnL 走 trade_pnl,避免重复计数)
	wantRollback := 0.1
	if math.Abs(pos.RollbackCostUSDT-wantRollback) > 1e-9 {
		t.Fatalf("rollback_cost=%v want %v (fee only)", pos.RollbackCostUSDT, wantRollback)
	}
	if pos.CloseFeesUSDT != 0 {
		t.Fatalf("rollback fee must not double-count in close_fees, got close_fees=%v", pos.CloseFeesUSDT)
	}
	// ExitPrice/ClosedQty 必须被填(让 trade_pnl 承载裸腿价格 PnL)
	if pos.HighLeg.ClosedQty != 1 || pos.HighLeg.ExitPrice != 101 {
		t.Fatalf("leg close tracking: qty=%v exit=%v", pos.HighLeg.ClosedQty, pos.HighLeg.ExitPrice)
	}
}

func TestCrossVenueFeeToUSDT(t *testing.T) {
	if got := crossVenueFeeToUSDT(1.5, "USDT", 0, nil); got != 1.5 {
		t.Fatalf("stablecoin fee: got %v", got)
	}
	if got := crossVenueFeeToUSDT(0.001, "BTC", 50000, nil); got != 50 {
		t.Fatalf("base asset fee: got %v want 50", got)
	}
}

func TestAppendCrossVenueSettlementAndAPI(t *testing.T) {
	dir := t.TempDir()
	e := &CrossVenueExecutor{
		recordDir: dir,
		nowFn:     func() time.Time { return time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC) },
	}
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             "okx",
		LowVenue:              "gate",
		HighLegFundingUSDT:    1,
		OpenedAt:              time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
		CloseReason:           "test close",
		HighLeg:               CrossVenueLegState{EntryPrice: 100, ExitPrice: 99, ClosedQty: 1},
		LowLeg:                CrossVenueLegState{EntryPrice: 100, ExitPrice: 101, ClosedQty: 1},
		CrossVenueAttribution: CrossVenueAttribution{OpenFeesUSDT: 0.05, CloseFeesUSDT: 0.05},
	}
	e.appendCrossVenueSettlement(pos)

	path := filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("settlements file not created: %v", err)
	}
	resp, err := e.SettlementsSnapshot(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 1 {
		t.Fatalf("records=%d want 1", len(resp.Records))
	}
	if resp.Summary.TotalNetPnL == 0 && resp.Records[0].NetPnL != 0 {
		t.Fatalf("summary total_net_pnl not populated")
	}
}

// TestCrossVenuePositionZeroAttributionByteCompatible 锁定:零归因/未平仓的持仓序列化后不得出现
// 任何归因/平仓快照键。否则旧 positions.json(无这些键)重算 state_envelope 校验和会失配,
// 导致新二进制拒绝自动接管在场实盘仓位(必须靠内嵌+omitempty 保证字节兼容)。
func TestCrossVenuePositionZeroAttributionByteCompatible(t *testing.T) {
	pos := CrossVenuePosition{
		Symbol:    "X-USDT",
		HighVenue: "gate",
		LowVenue:  "okx",
		HighLeg:   CrossVenueLegState{Venue: "gate", Side: "sell", Qty: 1, EntryPrice: 10},
		LowLeg:    CrossVenueLegState{Venue: "okx", Side: "buy", Qty: 1, EntryPrice: 10},
		Status:    CrossVenueStatusOpen,
	}
	data, err := json.Marshal(pos)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"attribution", "open_fees_usdt", "close_fees_usdt", "open_slippage_usdt", "close_slippage_usdt", "rollback_cost_usdt", "rebalance_cost_usdt", "exit_price", "closed_qty"} {
		if strings.Contains(string(data), k) {
			t.Fatalf("零值持仓序列化不得含键 %q(破坏旧台账校验和兼容),got: %s", k, data)
		}
	}
}

func TestCalculateSlippageSignConvention(t *testing.T) {
	// 买入价高于 mid → 负滑点(成本)
	slip := CalculateSlippage(100, 101, 1, true)
	if slip >= 0 {
		t.Fatalf("buy slippage should be negative when paid above mid, got %v", slip)
	}
	// 卖出价低于 mid → 负滑点(成本)
	slip = CalculateSlippage(100, 99, 1, false)
	if slip >= 0 {
		t.Fatalf("sell slippage should be negative when sold below mid, got %v", slip)
	}
}
