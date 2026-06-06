package app

import (
	"testing"
	"time"
)

// core_path_test.go 为此前缺少同名直测的核心交易路径补上直接单测：开仓滑点(open_protocol.go)、
// 合约成交映射(engine_orders.go)、平仓窗口逆向价差(engine_cost_guard.go)、并发首错取消(R12)、
// 计划拆分与开仓方向盘口选择等。这些是会真实影响下单/裸腿的关键纯逻辑。

func TestFuturesExecutionToResultMapping(t *testing.T) {
	snapshot := OrderExecutionSnapshot{
		OrderID:       12345,
		Symbol:        "ETHUSDT",
		Status:        "FILLED",
		ClientOrderID: "cid-snap",
		ExecutedQty:   1.5,
		AvgPrice:      3000.25,
		EventTime:     time.UnixMilli(1700000000000).UTC(),
	}
	order := FuturesBatchOrderRequest{Symbol: "ETHUSDT", Side: "SELL", Quantity: "1.5", NewClientOrderID: "cid-order"}
	got := futuresExecutionToResult(snapshot, order)

	if got.OrderID != 12345 || got.Symbol != "ETHUSDT" || got.Status != "FILLED" {
		t.Fatalf("基础字段映射错误: %+v", got)
	}
	if got.ClientOrderID != "cid-snap" { // snapshot 优先
		t.Errorf("ClientOrderID = %s, want cid-snap(snapshot 优先)", got.ClientOrderID)
	}
	if got.Side != "SELL" || got.OrigQty != "1.5" { // 来自 order
		t.Errorf("Side/OrigQty 应来自 order: %+v", got)
	}
	if got.UpdateTime != 1700000000000 {
		t.Errorf("UpdateTime = %d, want 1700000000000", got.UpdateTime)
	}
}

func TestFuturesExecutionToResultFallsBackToOrderFields(t *testing.T) {
	// snapshot 缺 Symbol/ClientOrderID 时回退到 order 的值（nonEmpty 语义）。
	snapshot := OrderExecutionSnapshot{OrderID: 1, Status: "NEW", ExecutedQty: 0, AvgPrice: 0, EventTime: time.UnixMilli(1).UTC()}
	order := FuturesBatchOrderRequest{Symbol: "BTCUSDT", Side: "BUY", Quantity: "0.01", NewClientOrderID: "cid-order"}
	got := futuresExecutionToResult(snapshot, order)
	if got.Symbol != "BTCUSDT" {
		t.Errorf("Symbol 回退失败: %s", got.Symbol)
	}
	if got.ClientOrderID != "cid-order" {
		t.Errorf("ClientOrderID 回退失败: %s", got.ClientOrderID)
	}
}

func TestCalculateOpenSlippageZeroWhenFillsMatchTarget(t *testing.T) {
	candidate := FundingCandidate{Symbol: "ETHUSDT", MarkPrice: 3000}
	// 两腿都按 MarkPrice 成交 → 相对参考价无滑点。
	got := calculateOpenSlippage(candidate, fundingCarryDirectionPositive, 1.0, 3000, 3000)
	if got > 1e-9 || got < -1e-9 {
		t.Fatalf("成交价=参考价时滑点应为 0, got %.8f", got)
	}
}

func TestCalculateOpenSlippageNegativeOnAdverseFill(t *testing.T) {
	candidate := FundingCandidate{Symbol: "ETHUSDT", MarkPrice: 3000}
	// 正向 carry: 现货 BUY 成交价 3010(更高=不利) + 合约 SELL 成交价 2990(更低=不利)。
	// 约定: 滑点为负表示成本。两腿各 -10 → 合计 -20。
	got := calculateOpenSlippage(candidate, fundingCarryDirectionPositive, 1.0, 2990, 3010)
	if d := got - (-20.0); d > 1e-9 || d < -1e-9 {
		t.Fatalf("不利成交滑点(成本)应为 -20, got %.8f", got)
	}
}

func TestConfiguredSymbolMatch(t *testing.T) {
	if !configuredSymbolMatch("BTCUSDT,ETHUSDT", "ethusdt") {
		t.Error("应大小写无关匹配 ETHUSDT")
	}
	if configuredSymbolMatch("BTCUSDT", "ETHUSDT") {
		t.Error("不在列表里不应匹配")
	}
	if configuredSymbolMatch("", "ETHUSDT") {
		t.Error("空列表不应匹配")
	}
}

func TestSplitOpenSequencePlansByParallelConfig(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.ParallelOpenSymbols = "ETHUSDT"
	mk := func(sym string) openSequencePlan {
		return openSequencePlan{target: &MonitoredTarget{Candidate: FundingCandidate{Symbol: sym}}}
	}
	seq, par := splitOpenSequencePlans(cfg, []openSequencePlan{mk("BTCUSDT"), mk("ETHUSDT")})
	if len(par) != 1 || par[0].target.Candidate.Symbol != "ETHUSDT" {
		t.Fatalf("ETHUSDT 应进入并发组, par=%+v", par)
	}
	if len(seq) != 1 || seq[0].target.Candidate.Symbol != "BTCUSDT" {
		t.Fatalf("BTCUSDT 应进入顺序组, seq=%+v", seq)
	}
}

func TestFundingStabilitySampleCountAndInterval(t *testing.T) {
	if got := fundingStabilitySampleCount(100 * time.Millisecond); got != preOpenFundingStabilityMinSamples {
		t.Errorf("极短窗口应取最小采样, got %d", got)
	}
	if got := fundingStabilitySampleCount(1 * time.Second); got != preOpenFundingStabilitySamples {
		t.Errorf("充足窗口应取最大采样, got %d", got)
	}
	// 单采样无需间隔。
	if got := fundingStabilitySleepInterval(time.Second, 1); got != 0 {
		t.Errorf("单采样间隔应为 0, got %v", got)
	}
	// 间隔被夹在 [min,max] 之间。
	interval := fundingStabilitySleepInterval(10*time.Second, 3)
	if interval > preOpenFundingStabilityMaxInterval || interval < preOpenFundingStabilityMinInterval {
		t.Errorf("间隔应被夹取到 [%v,%v], got %v", preOpenFundingStabilityMinInterval, preOpenFundingStabilityMaxInterval, interval)
	}
}

func TestOpenDepthLevelsForDirection(t *testing.T) {
	spot := ParsedOrderBookDepth{
		Bids: []DepthLevel{{Price: 99, Quantity: 1}},
		Asks: []DepthLevel{{Price: 101, Quantity: 1}},
	}
	futures := ParsedOrderBookDepth{
		Bids: []DepthLevel{{Price: 98, Quantity: 1}},
		Asks: []DepthLevel{{Price: 102, Quantity: 1}},
	}
	// 正向: 现货吃 ask, 合约吃 bid。
	spotLevels, futuresLevels := openDepthLevelsForDirection(fundingCarryDirectionPositive, spot, futures)
	if spotLevels[0].Price != 101 || futuresLevels[0].Price != 98 {
		t.Fatalf("正向方向盘口选择错误: spot=%.0f futures=%.0f", spotLevels[0].Price, futuresLevels[0].Price)
	}
	// 负向: 现货吃 bid(卖空), 合约吃 ask。
	spotLevels, futuresLevels = openDepthLevelsForDirection(fundingCarryDirectionNegative, spot, futures)
	if spotLevels[0].Price != 99 || futuresLevels[0].Price != 102 {
		t.Fatalf("负向方向盘口选择错误: spot=%.0f futures=%.0f", spotLevels[0].Price, futuresLevels[0].Price)
	}
}

func TestDepthHasBothSides(t *testing.T) {
	full := ParsedOrderBookDepth{
		Bids: []DepthLevel{{Price: 99, Quantity: 1}},
		Asks: []DepthLevel{{Price: 101, Quantity: 1}},
	}
	if !depthHasBothSides(full) {
		t.Fatalf("两侧都有档位应判为有效")
	}
	if depthHasBothSides(ParsedOrderBookDepth{Asks: []DepthLevel{{Price: 101, Quantity: 1}}}) {
		t.Fatalf("缺买侧应判为无效(数据未就绪)")
	}
	if depthHasBothSides(ParsedOrderBookDepth{Bids: []DepthLevel{{Price: 99, Quantity: 1}}}) {
		t.Fatalf("缺卖侧应判为无效(数据未就绪)")
	}
	if depthHasBothSides(ParsedOrderBookDepth{}) {
		t.Fatalf("空快照应判为无效(数据未就绪)")
	}
}

// TestComputeAverageFillPriceMissingVsThin 钉死深度风控的关键判据：
// 盘口为空 → filled==0（数据缺失，checkTargetCost 据此降级为「观察」而非熔断整轮）；
// 盘口非空但量不足 → filled>0 且 complete=false（真薄盘，维持 coverage→风险熔断）。
func TestComputeAverageFillPriceMissingVsThin(t *testing.T) {
	requiredQty := 10.0

	// 数据缺失：无任何档位。
	if _, _, filled, complete := computeAverageFillPrice(nil, requiredQty); filled != 0 || complete {
		t.Fatalf("空盘口应 filled==0 且未完全成交, got filled=%.4f complete=%v", filled, complete)
	}
	// 数据缺失变体：有档位但数量全为 0，应等价于吃不到（filled==0）。
	zeroQty := []DepthLevel{{Price: 100, Quantity: 0}}
	if _, _, filled, complete := computeAverageFillPrice(zeroQty, requiredQty); filled != 0 || complete {
		t.Fatalf("全 0 数量盘口应 filled==0, got filled=%.4f complete=%v", filled, complete)
	}
	// 真薄盘：有档位但累计量 < 所需量 → filled>0 但未完全成交（保留真实流动性熔断）。
	thin := []DepthLevel{{Price: 100, Quantity: 3}}
	if _, _, filled, complete := computeAverageFillPrice(thin, requiredQty); !(filled > 0) || complete {
		t.Fatalf("薄盘应 filled>0 且未完全成交, got filled=%.4f complete=%v", filled, complete)
	}
	// 充足：累计量 ≥ 所需量 → 完全成交。
	enough := []DepthLevel{{Price: 100, Quantity: 12}}
	if _, _, filled, complete := computeAverageFillPrice(enough, requiredQty); !complete || filled+1e-9 < requiredQty {
		t.Fatalf("充足盘口应完全成交, got filled=%.4f complete=%v", filled, complete)
	}
}

func TestSummarizeOpenHedges(t *testing.T) {
	hedges := []OpenHedge{
		{Target: FundingCandidate{Symbol: "ethusdt"}, OpenSpotQuoteQty: 100.5},
		{Target: FundingCandidate{Symbol: " btcusdt "}, OpenSpotQuoteQty: 200.25},
		{Target: FundingCandidate{Symbol: ""}, OpenSpotQuoteQty: 5}, // 空 symbol 不计入名单但计名义
	}
	symbols, notional := summarizeOpenHedges(hedges)
	if symbols != "ETHUSDT, BTCUSDT" {
		t.Errorf("symbols = %q, want \"ETHUSDT, BTCUSDT\"", symbols)
	}
	if d := notional - 305.75; d > 1e-9 || d < -1e-9 {
		t.Errorf("notional = %.4f, want 305.75", notional)
	}
}
