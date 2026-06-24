package app

import (
	"context"
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
			CloseTime:      dayStart.Add(30 * time.Minute),
			HighLegFunding: 2, LowLegFunding: 3, TradePnL: 1,
			TotalFees: 0.4, RollbackCost: 0.2, RebalanceCost: 0.4, NetPnL: 5.0,
		},
		// 今日 okx/bybit: net = 0.5 + (-0.1) + 0 - 0.2 = 0.2
		{
			Symbol: "ETH-USDT", HighVenue: "okx", LowVenue: "bybit",
			CloseTime:      dayStart.Add(2 * time.Hour),
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
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	ps := []CrossVenuePosition{
		{Symbol: "BTC-USDT", Status: CrossVenueStatusClosed},
		{
			Symbol: "ETH-USDT", Status: CrossVenueStatusOpen,
			HighVenue: "bybit", LowVenue: "okx",
			HighLeg:      CrossVenueLegState{Side: "sell", Qty: 0.5, EntryPrice: 2500},
			LowLeg:       CrossVenueLegState{Side: "buy", Qty: 0.5, EntryPrice: 2498},
			NotionalUSDT: 1250, OpenSpreadAPR: 0.42, EstFundingUSDT: 1.23,
			CrossVenueAttribution: CrossVenueAttribution{OpenFeesUSDT: 0.15},
			OpenedAt:              opened,
		},
	}
	details := buildCrossVenuePositionDetails(cfg, ps, now)
	if len(details) != 1 {
		t.Fatalf("details=%d want 1 active", len(details))
	}
	d := details[0]
	if d.Symbol != "ETH-USDT" || d.HighLegQty != 0.5 || d.HoldDurationSec != 5400 {
		t.Fatalf("detail=%+v", d)
	}
	if math.Abs(d.OpenFeesUSDT-0.15) > 1e-9 {
		t.Fatalf("open_fees_usdt=%v want 0.15", d.OpenFeesUSDT)
	}
	wantEstClose := crossVenueEstCloseFeeUSDT(cfg, &ps[1])
	if math.Abs(d.EstCloseFeesUSDT-wantEstClose) > 1e-9 {
		t.Fatalf("est_close_fees_usdt=%v want %v", d.EstCloseFeesUSDT, wantEstClose)
	}
	wantNet := 1.23 - 0.15 - wantEstClose
	if math.Abs(d.NetUSDT-wantNet) > 1e-9 {
		t.Fatalf("net_usdt=%v want %v", d.NetUSDT, wantNet)
	}
}

func TestCrossVenueEstCloseFeeUSDT(t *testing.T) {
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	pos := &CrossVenuePosition{
		HighVenue: "bybit", LowVenue: "okx",
		NotionalUSDT: 1000,
	}
	got := crossVenueEstCloseFeeUSDT(cfg, pos)
	want := 1000*(5.5/10000) + 1000*(5.0/10000)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("est_close=%v want %v", got, want)
	}
	pos.NotionalUSDT = 0
	pos.HighLeg = CrossVenueLegState{Qty: 2, EntryPrice: 500}
	got = crossVenueEstCloseFeeUSDT(cfg, pos)
	want = 1000*(5.5/10000) + 1000*(5.0/10000)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("fallback notional est_close=%v want %v", got, want)
	}
	if crossVenueEstCloseFeeUSDT(cfg, nil) != 0 {
		t.Fatal("nil pos should be 0")
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

func TestSummarizeCrossVenueSettlementsUsesVenueAttribution(t *testing.T) {
	records := []CrossVenueSettlement{
		{
			Symbol: "BTC-USDT", HighVenue: "binance", LowVenue: "gate",
			HighLegFunding: 2, LowLegFunding: 3, TradePnL: 1,
			TotalFees: 0.4, RollbackCost: 0.2, RebalanceCost: 0.4, NetPnL: 5.0,
		},
	}
	sum := summarizeCrossVenueSettlements(records)
	if math.Abs(sum.TotalNetPnL-5.0) > 1e-9 {
		t.Fatalf("total=%v", sum.TotalNetPnL)
	}
	if math.Abs(sum.ByVenueNetPnL["binance"]-2.0) > 1e-9 {
		t.Fatalf("binance attribution=%v want 2.0", sum.ByVenueNetPnL["binance"])
	}
	if math.Abs(sum.ByVenueNetPnL["gate"]-3.0) > 1e-9 {
		t.Fatalf("gate attribution=%v want 3.0", sum.ByVenueNetPnL["gate"])
	}
}

func TestBuildCrossVenueSettlementUsesEstimatedFundingWhenMissing(t *testing.T) {
	opened := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	closeTime := opened.Add(10 * time.Hour)
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "binance", LowVenue: "gate",
		OpenedAt: opened, HighIntervalHours: 8, LowIntervalHours: 8,
		HighNextFundingTime:     opened.Add(4 * time.Hour),
		LowNextFundingTime:      opened.Add(4 * time.Hour),
		EstFundingUSDT:          1.75,
		FundingTruthStatus:      "missing",
		RealizedFundingComplete: false,
	}
	rec := buildCrossVenueSettlement(pos, closeTime)
	if rec.FundingTruthStatus != "estimated" {
		t.Fatalf("funding_truth=%q want estimated", rec.FundingTruthStatus)
	}
	if math.Abs(rec.RealizedFunding-1.75) > 1e-9 {
		t.Fatalf("realized_funding=%v want 1.75", rec.RealizedFunding)
	}
	if math.Abs(rec.NetPnL-1.75) > 1e-9 {
		t.Fatalf("net_pnl=%v want 1.75 (estimated funding included)", rec.NetPnL)
	}
}

func TestBuildCrossVenueSettlement_PartialEstimatesMissingLeg(t *testing.T) {
	// partial:高费腿真实 (0.6),低费腿真实资费拉取失败 → LowLegFundingUSDT 停留 0。
	// 整仓估算 EstFundingUSDT=0.5(隐含低费腿估算 -0.1)。
	// 期望:realized = 0.5(= 真实腿 0.6 + 缺失腿估算 -0.1),而非旧逻辑的 0.6(缺失腿计 0 → 多计)。
	pos := CrossVenuePosition{
		Symbol:                  "BTC-USDT",
		HighVenue:               "binance",
		LowVenue:                "gate",
		OpenedAt:                time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		HighLegFundingUSDT:      0.6,
		LowLegFundingUSDT:       0,
		EstFundingUSDT:          0.5,
		FundingTruthStatus:      "partial",
		RealizedFundingComplete: false,
	}
	rec := buildCrossVenueSettlement(pos, pos.OpenedAt.Add(10*time.Hour))

	if math.Abs(rec.RealizedFunding-0.5) > 1e-9 {
		t.Fatalf("realized_funding=%v want 0.5 (real leg + estimated missing leg)", rec.RealizedFunding)
	}
	// 不得退化为只算真实腿(旧 bug:缺失腿计 0)。
	if math.Abs(rec.RealizedFunding-0.6) < 1e-9 {
		t.Fatalf("realized_funding=%v under-counts: missing leg still treated as 0", rec.RealizedFunding)
	}
	// 不得重复计数真实腿(真实腿 + 整仓估算 = 1.1)。
	if math.Abs(rec.RealizedFunding-1.1) < 1e-9 {
		t.Fatalf("realized_funding=%v double-counts the real leg", rec.RealizedFunding)
	}
	if rec.FundingTruthStatus != "partial" {
		t.Fatalf("funding_truth=%q want partial (kept, missing leg estimated)", rec.FundingTruthStatus)
	}
	if rec.RealizedFundingComplete {
		t.Fatal("realized_funding_complete must stay false for partial")
	}
	// net = realized(0.5) + trade(0) - fees(0) = 0.5
	if math.Abs(rec.NetPnL-0.5) > 1e-9 {
		t.Fatalf("net_pnl=%v want 0.5", rec.NetPnL)
	}
}

func TestBuildCrossVenueSettlement_PartialWithoutEstimateKeepsRealLeg(t *testing.T) {
	// 无整仓估算(EstFundingUSDT=0)时无估算可用 → 保持原两腿之和(不抹掉真实腿)。
	pos := CrossVenuePosition{
		Symbol:             "BTC-USDT",
		HighVenue:          "binance",
		LowVenue:           "gate",
		OpenedAt:           time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		HighLegFundingUSDT: 0.6,
		LowLegFundingUSDT:  0,
		EstFundingUSDT:     0,
		FundingTruthStatus: "partial",
	}
	rec := buildCrossVenueSettlement(pos, pos.OpenedAt.Add(10*time.Hour))
	if math.Abs(rec.RealizedFunding-0.6) > 1e-9 {
		t.Fatalf("realized_funding=%v want 0.6 (real leg preserved when no estimate available)", rec.RealizedFunding)
	}
	if rec.FundingTruthStatus != "partial" {
		t.Fatalf("funding_truth=%q want partial", rec.FundingTruthStatus)
	}
}

func TestBuildCrossVenueSettlement_CompleteUsesRealLegsOnly(t *testing.T) {
	// 回归:fully-real(complete)路径不受 partial 改动影响,仍只用两腿真实值,EstFundingUSDT 不介入。
	pos := CrossVenuePosition{
		Symbol:                  "BTC-USDT",
		HighVenue:               "binance",
		LowVenue:                "gate",
		OpenedAt:                time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		HighLegFundingUSDT:      0.6,
		LowLegFundingUSDT:       -0.1,
		EstFundingUSDT:          99, // 即便估算很大也不得被采用
		FundingTruthStatus:      "complete",
		RealizedFundingComplete: true,
	}
	rec := buildCrossVenueSettlement(pos, pos.OpenedAt.Add(10*time.Hour))
	if math.Abs(rec.RealizedFunding-0.5) > 1e-9 {
		t.Fatalf("realized_funding=%v want 0.5 (real legs only)", rec.RealizedFunding)
	}
	if !rec.RealizedFundingComplete || rec.FundingTruthStatus != "complete" {
		t.Fatalf("complete path changed: complete=%v status=%q", rec.RealizedFundingComplete, rec.FundingTruthStatus)
	}
}

func TestAppendCrossVenueSettlementIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	e := &CrossVenueExecutor{
		recordDir: dir,
		nowFn:     func() time.Time { return now },
		logger:    testCrossVenueLogger(),
	}
	pos := CrossVenuePosition{
		Symbol:                  "BTC-USDT",
		HighVenue:               "okx",
		LowVenue:                "gate",
		OpenedAt:                now.Add(-time.Hour),
		IdemPrefix:              "cv-btc-okx-1",
		NotionalUSDT:            100,
		HighLegFundingUSDT:      1,
		LowLegFundingUSDT:       -0.2,
		RealizedFundingComplete: true,
		FundingTruthStatus:      "complete",
	}
	e.appendCrossVenueSettlement(pos)
	e.appendCrossVenueSettlement(pos)
	records, err := e.loadAllCrossVenueSettlements()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(records))
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
	e.recordCrossVenueExecution(context.Background(), &pos, cvExecRollbackHigh, res, 100)
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

func TestCrossVenueLegSettlementCount(t *testing.T) {
	open := time.Date(2026, 6, 24, 0, 58, 0, 0, time.UTC)
	first := time.Date(2026, 6, 24, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		close    time.Time
		interval int
		want     int
	}{
		{"closed before settlement", open.Add(1 * time.Minute), 4, 0},
		{"held through one", time.Date(2026, 6, 24, 1, 2, 0, 0, time.UTC), 4, 1},
		{"closed exactly at settlement counts", first, 4, 1},
		{"two settlements 4h", time.Date(2026, 6, 24, 5, 1, 0, 0, time.UTC), 4, 2},
		{"stale firstFunding before open advances", open.Add(2 * time.Minute), 4, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ff := first
			if c.name == "stale firstFunding before open advances" {
				ff = time.Date(2026, 6, 23, 21, 0, 0, 0, time.UTC) // 早于 open,需推进到 01:00
			}
			got := crossVenueLegSettlementCount(open, c.close, ff, c.interval)
			if got != c.want {
				t.Fatalf("count=%d want %d", got, c.want)
			}
		})
	}
}

func TestCrossVenueDiscreteFundingEstimate(t *testing.T) {
	open := time.Date(2026, 6, 24, 0, 58, 0, 0, time.UTC)
	first := time.Date(2026, 6, 24, 1, 0, 0, 0, time.UTC)
	pos := CrossVenuePosition{
		Symbol: "TAIKO-USDT", HighVenue: "gate", LowVenue: "bybit",
		OpenedAt: open, NotionalUSDT: 1000,
		HighIntervalHours: 4, LowIntervalHours: 4,
		HighNextFundingTime: first, LowNextFundingTime: first,
		HighFundingRate: 0.001, LowFundingRate: -0.002,
	}
	// 跨 1 次结算:high=1000*0.001*1=+1.0,low=-1000*(-0.002)*1=+2.0 → 3.0
	got := crossVenueDiscreteFundingEstimate(pos, time.Date(2026, 6, 24, 1, 2, 0, 0, time.UTC))
	if math.Abs(got-3.0) > 1e-9 {
		t.Fatalf("discrete=%v want 3.0", got)
	}
	// 平在结算前 → 0 次 → 估算 0
	got = crossVenueDiscreteFundingEstimate(pos, open.Add(1*time.Minute))
	if math.Abs(got) > 1e-9 {
		t.Fatalf("discrete=%v want 0 (closed before settlement)", got)
	}
}

func TestBuildCrossVenueSettlement_DiscreteEstimateWhenLegsZeroHeldThrough(t *testing.T) {
	// P0 回归:统一捕获模型超短持有,真实资费轮询在结算前抓到 0、平仓前未再轮询 → 两腿恒 0、状态被标 complete。
	// 但持仓确实跨越了结算瞬时 → 必须用离散结算估算兜底,而不是把真实收到的资费记成 0。
	open := time.Date(2026, 6, 24, 0, 58, 0, 0, time.UTC)
	first := time.Date(2026, 6, 24, 1, 0, 0, 0, time.UTC)
	closeTime := time.Date(2026, 6, 24, 1, 2, 0, 0, time.UTC)
	pos := CrossVenuePosition{
		Symbol: "TAIKO-USDT", HighVenue: "gate", LowVenue: "bybit",
		OpenedAt: open, NotionalUSDT: 1000,
		HighIntervalHours: 4, LowIntervalHours: 4,
		HighNextFundingTime: first, LowNextFundingTime: first,
		HighFundingRate: 0.001, LowFundingRate: -0.002,
		HighLegFundingUSDT: 0, LowLegFundingUSDT: 0,
		FundingTruthStatus:      "complete", // 早于结算的轮询误标 complete@0
		RealizedFundingComplete: true,
	}
	rec := buildCrossVenueSettlement(pos, closeTime)
	if math.Abs(rec.RealizedFunding-3.0) > 1e-9 {
		t.Fatalf("realized_funding=%v want 3.0 (discrete estimate), 资费被记 0 的 bug 复现", rec.RealizedFunding)
	}
	if rec.FundingTruthStatus != "estimated" {
		t.Fatalf("funding_truth=%q want estimated", rec.FundingTruthStatus)
	}
	if rec.RealizedFundingComplete {
		t.Fatal("complete must be false for estimated fallback")
	}
}

func TestBuildCrossVenueSettlement_NoOverrideWhenLegRealNonZero(t *testing.T) {
	// 任一腿已抓到真实非零值 → 绝不被离散估算覆盖(真实优先)。
	open := time.Date(2026, 6, 24, 0, 58, 0, 0, time.UTC)
	first := time.Date(2026, 6, 24, 1, 0, 0, 0, time.UTC)
	closeTime := time.Date(2026, 6, 24, 1, 2, 0, 0, time.UTC)
	pos := CrossVenuePosition{
		Symbol: "TAIKO-USDT", HighVenue: "gate", LowVenue: "bybit",
		OpenedAt: open, NotionalUSDT: 1000,
		HighIntervalHours: 4, LowIntervalHours: 4,
		HighNextFundingTime: first, LowNextFundingTime: first,
		HighFundingRate: 0.001, LowFundingRate: -0.002,
		HighLegFundingUSDT: 0.5, LowLegFundingUSDT: 0,
		FundingTruthStatus:      "partial",
		RealizedFundingComplete: false,
	}
	rec := buildCrossVenueSettlement(pos, closeTime)
	if rec.FundingTruthStatus == "estimated" && math.Abs(rec.RealizedFunding-3.0) < 1e-9 {
		t.Fatalf("real leg 0.5 被离散估算覆盖了,真实优先被破坏:realized=%v status=%q", rec.RealizedFunding, rec.FundingTruthStatus)
	}
}

func TestBuildCrossVenueSettlement_NoOverrideWhenNotHeldThrough(t *testing.T) {
	// 没跨结算瞬时(开/平都在结算前)→ 不兜底,资费就是 0。
	open := time.Date(2026, 6, 24, 0, 58, 0, 0, time.UTC)
	first := time.Date(2026, 6, 24, 1, 0, 0, 0, time.UTC)
	closeTime := open.Add(1 * time.Minute) // 00:59,早于 01:00 结算
	pos := CrossVenuePosition{
		Symbol: "TAIKO-USDT", HighVenue: "gate", LowVenue: "bybit",
		OpenedAt: open, NotionalUSDT: 1000,
		HighIntervalHours: 4, LowIntervalHours: 4,
		HighNextFundingTime: first, LowNextFundingTime: first,
		TargetFirstFundingTime:  first,
		HighFundingRate:         0.001, LowFundingRate: -0.002,
		HighLegFundingUSDT:      0, LowLegFundingUSDT: 0,
		FundingTruthStatus:      "complete",
		RealizedFundingComplete: true,
	}
	rec := buildCrossVenueSettlement(pos, closeTime)
	if math.Abs(rec.RealizedFunding) > 1e-9 {
		t.Fatalf("realized_funding=%v want 0 (未跨结算不应兜底)", rec.RealizedFunding)
	}
}

func TestCrossVenueCloseForcesFlat(t *testing.T) {
	force := []string{"强平距离不足,全平", "flatten_all", "紧急平仓", "emergency flatten", "liquidation risk"}
	for _, r := range force {
		if !crossVenueCloseForcesFlat(r) {
			t.Fatalf("reason %q 应判为强制变平", r)
		}
	}
	hedged := []string{"计划离场:目标结算+缓冲已到", "换仓", "基差止损:累计不利基差", "未来净值<=0", "open_new_retry", "rollback_new", ""}
	for _, r := range hedged {
		if crossVenueCloseForcesFlat(r) {
			t.Fatalf("reason %q 应保持对冲(非强制变平)", r)
		}
	}
}

func TestCrossVenueSettlementSane(t *testing.T) {
	// 物理不可能:net 远超 5×名义 → 脏记录
	if crossVenueSettlementSane(CrossVenueSettlement{NotionalUSDT: 46, NetPnL: -801.56, TradePnL: -801.56}) {
		t.Fatal("HEI 型脏记录(net=-801/名义46)应被判脏")
	}
	// 正常对冲结算(哪怕较大滑点)放行
	if !crossVenueSettlementSane(CrossVenueSettlement{NotionalUSDT: 90, NetPnL: -0.89, TradePnL: -0.80, CloseSlippage: -0.55}) {
		t.Fatal("正常 TAIKO 型结算被误杀")
	}
	// 无名义无法判定 → 放行
	if !crossVenueSettlementSane(CrossVenueSettlement{NotionalUSDT: 0, NetPnL: -999}) {
		t.Fatal("无名义记录应放行")
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
