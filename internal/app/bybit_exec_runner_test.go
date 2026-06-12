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

// fakeBybitMarket 是 bybitMarketReader 的离线 fake(确定性,不触网)。
type fakeBybitMarket struct {
	snaps   []venue.FundingSnapshot
	instr   []venue.Instrument
	tickers map[instrumentKey]venue.BookTicker
	cost    venue.CostProfile
}

func (f *fakeBybitMarket) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	return f.snaps, nil
}
func (f *fakeBybitMarket) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	return f.instr, nil
}
func (f *fakeBybitMarket) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	return f.tickers[instrumentKey{s, m}], nil
}
func (f *fakeBybitMarket) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	return f.cost, nil
}

func TestShadowPlanCycleAcceptsProfitable(t *testing.T) {
	sym := venue.Symbol("AAA-USDT")
	f := &fakeBybitMarket{
		snaps: []venue.FundingSnapshot{
			{Symbol: sym, LastFundingRate: 0.006, IntervalHours: 8},
			{Symbol: "LOW-USDT", LastFundingRate: 0.0001},
		},
		instr: []venue.Instrument{
			{Symbol: sym, Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5},
			{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5},
		},
		tickers: map[instrumentKey]venue.BookTicker{
			{sym, venue.MarketSpot}: {BidPrice: 99.9, AskPrice: 100.1},
			{sym, venue.MarketPerp}: {BidPrice: 99.95, AskPrice: 100.05},
		},
		cost: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.0006},
	}
	cfg := BybitExecConfig{AllocatedUSDT: 4000, TopN: 3, MinFundingRate: 0.0005, MinNetProfitRate: 0.0005}
	exec := NewBybitShadowExecutor(f, cfg, nil)
	plan, err := exec.PlanCycle(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 1 {
		t.Fatalf("应通过 1 个, got %d (rejected=%+v)", len(plan.Accepted), plan.Rejected)
	}
	if plan.Accepted[0].Symbol != sym {
		t.Errorf("符号错误: %s", plan.Accepted[0].Symbol)
	}
	if plan.Accepted[0].BaseQty <= 0 {
		t.Errorf("数量应为正: %v", plan.Accepted[0].BaseQty)
	}
}

func TestShadowPlanCycleRejectsMissingSpotLeg(t *testing.T) {
	sym := venue.Symbol("PERPONLY-USDT")
	f := &fakeBybitMarket{
		snaps: []venue.FundingSnapshot{{Symbol: sym, LastFundingRate: 0.01, IntervalHours: 8}},
		instr: []venue.Instrument{
			{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5},
		},
		tickers: map[instrumentKey]venue.BookTicker{
			{sym, venue.MarketPerp}: {BidPrice: 100, AskPrice: 100},
		},
		cost: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.0006},
	}
	cfg := BybitExecConfig{AllocatedUSDT: 4000, TopN: 3, MinFundingRate: 0.0005, MinNetProfitRate: 0.0005}
	exec := NewBybitShadowExecutor(f, cfg, nil)
	plan, err := exec.PlanCycle(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 0 || len(plan.Rejected) != 1 {
		t.Fatalf("缺现货腿应放弃: accepted=%+v rejected=%+v", plan.Accepted, plan.Rejected)
	}
	if plan.Rejected[0].Reason != "缺少现货合约元数据(无法做多现货腿)" {
		t.Errorf("原因不符: %s", plan.Rejected[0].Reason)
	}
}

// TestPlanCycleDividesBySlotsNotCandidates 钉死资金占用率修复:资金按「可开槽位」均分而非候选数。
// 2 个候选 + 1 个槽位时,开出的那笔必须拿到全部预算(修复前只拿一半,资金闲置);
// 同时钉死排序按「费率/小时」归一化:1h 间隔 0.30% (0.30%/h) 要排在 8h 间隔 0.40% (0.05%/h) 前面。
func TestPlanCycleDividesBySlotsNotCandidates(t *testing.T) {
	fast := venue.Symbol("FAST-USDT") // 1h 结算,费率/小时更高
	slow := venue.Symbol("SLOW-USDT") // 8h 结算,原始费率更高
	mkInstr := func(sym venue.Symbol) []venue.Instrument {
		return []venue.Instrument{
			{Symbol: sym, Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5},
			{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5},
		}
	}
	f := &fakeBybitMarket{
		snaps: []venue.FundingSnapshot{
			{Symbol: fast, LastFundingRate: 0.003, IntervalHours: 1},
			{Symbol: slow, LastFundingRate: 0.004, IntervalHours: 8},
		},
		instr: append(mkInstr(fast), mkInstr(slow)...),
		tickers: map[instrumentKey]venue.BookTicker{
			{fast, venue.MarketSpot}: {BidPrice: 100, AskPrice: 100},
			{fast, venue.MarketPerp}: {BidPrice: 100, AskPrice: 100},
			{slow, venue.MarketSpot}: {BidPrice: 100, AskPrice: 100},
			{slow, venue.MarketPerp}: {BidPrice: 100, AskPrice: 100},
		},
		cost: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.0006},
	}
	cfg := BybitExecConfig{AllocatedUSDT: 60, TopN: 3, MinFundingRate: 0.0002, MinNetProfitRate: -0.005}
	exec := NewBybitShadowExecutor(f, cfg, nil)

	// 1 个槽位:最优候选拿满 60(每腿 30),而不是被 2 个候选稀释成 30(每腿 15)。
	plan, err := exec.PlanCycle(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 2 {
		t.Fatalf("两个候选都应通过定量, got %d (rejected=%+v)", len(plan.Accepted), plan.Rejected)
	}
	if plan.Accepted[0].Symbol != fast {
		t.Fatalf("应按费率/小时排序,FAST(0.30%%/h) 应排在 SLOW(0.05%%/h) 前: got %s", plan.Accepted[0].Symbol)
	}
	if got := plan.Accepted[0].SpotNotional; got < 29 || got > 31 {
		t.Fatalf("1 槽位时最优候选应拿满预算(每腿≈30), got %.2f", got)
	}

	// 不限槽位:按候选数均分(每候选 30,每腿 15)。
	plan2, err := exec.PlanCycle(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if got := plan2.Accepted[0].SpotNotional; got < 14 || got > 16 {
		t.Fatalf("不限槽位时应按候选数均分(每腿≈15), got %.2f", got)
	}
}

// TestPlanBybitLeverageSizing 钉死永续腿杠杆定量:lev=2 时总资金 = 现货名义 + 永续名义/2,
// 60U → 每腿名义 40(放大 33%)、永续保证金 20、DeployedUSDT=60;lev=1 行为不变(每腿 30)。
func TestPlanBybitLeverageSizing(t *testing.T) {
	c := BybitCandidate{Symbol: "AAA-USDT", FundingRate: 0.003, IntervalHours: 1}
	instr := venue.Instrument{Symbol: "AAA-USDT", StepSize: 0.001, MinNotional: 5}
	base := BybitExecConfig{MinNetProfitRate: -0.005, SpotTakerFee: 0.001, PerpTakerFee: 0.00055}

	cfg2 := base
	cfg2.PerpLeverage = 2
	plan, ok, reason := planBybitDeltaNeutralOpen(c, 100, 100, instr, instr, 60, cfg2)
	if !ok {
		t.Fatalf("应可开仓: %s", reason)
	}
	if plan.SpotNotional < 39.9 || plan.SpotNotional > 40.1 {
		t.Fatalf("lev=2 每腿名义应≈40, got %.2f", plan.SpotNotional)
	}
	if plan.PerpMarginUSDT < 19.9 || plan.PerpMarginUSDT > 20.1 {
		t.Fatalf("lev=2 永续保证金应≈20, got %.2f", plan.PerpMarginUSDT)
	}
	if plan.DeployedUSDT < 59.8 || plan.DeployedUSDT > 60.2 {
		t.Fatalf("lev=2 总部署应≈60, got %.2f", plan.DeployedUSDT)
	}
	if plan.PerpLeverage != 2 {
		t.Fatalf("计划应携带杠杆 2, got %d", plan.PerpLeverage)
	}

	plan1, ok, reason := planBybitDeltaNeutralOpen(c, 100, 100, instr, instr, 60, base)
	if !ok {
		t.Fatalf("lev=1 应可开仓: %s", reason)
	}
	if plan1.SpotNotional < 29.9 || plan1.SpotNotional > 30.1 {
		t.Fatalf("lev=1(默认)每腿名义应≈30(行为不变), got %.2f", plan1.SpotNotional)
	}
}

func TestShadowPlanCycleNoCandidates(t *testing.T) {
	f := &fakeBybitMarket{
		snaps: []venue.FundingSnapshot{{Symbol: "X-USDT", LastFundingRate: -0.01}},
	}
	exec := NewBybitShadowExecutor(f, BybitExecConfig{AllocatedUSDT: 1000, MinFundingRate: 0.0005}, nil)
	plan, err := exec.PlanCycle(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 0 || len(plan.Candidates) != 0 {
		t.Fatalf("无正费率候选时应空: %+v", plan)
	}
}

// fakeBybitMarketWithHist 在 fakeBybitMarket 之上加历史费率能力(EV 门用)。
type fakeBybitMarketWithHist struct {
	*fakeBybitMarket
	hist map[venue.Symbol][]float64
}

func (f *fakeBybitMarketWithHist) FetchFundingRateHistory(_ context.Context, s venue.Symbol, _ int) ([]float64, error) {
	return f.hist[s], nil
}

// TestPlanCycleEVGate 钉死多结算 EV 门:
// ① 单次结算净利为负但历史费率粘性强(连续有利)→ EV 为正 → 应开仓(修复「被迫开负门交学费」);
// ② 历史显示一次性尖峰(其余全 0)→ EV 为负 → 拒绝并说明原因。
func TestPlanCycleEVGate(t *testing.T) {
	sym := venue.Symbol("STICKY-USDT")
	mk := func(history []float64) *fakeBybitMarketWithHist {
		return &fakeBybitMarketWithHist{
			fakeBybitMarket: &fakeBybitMarket{
				snaps: []venue.FundingSnapshot{{Symbol: sym, LastFundingRate: 0.003, IntervalHours: 1}},
				instr: []venue.Instrument{
					{Symbol: sym, Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5},
					{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5},
				},
				tickers: map[instrumentKey]venue.BookTicker{
					{sym, venue.MarketSpot}: {BidPrice: 100, AskPrice: 100},
					{sym, venue.MarketPerp}: {BidPrice: 100, AskPrice: 100},
				},
				cost: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.00055},
			},
			hist: map[venue.Symbol][]float64{sym: history},
		}
	}
	// 单次结算:毛 0.003×30=0.09,费 30×0.002+30×0.0011=0.093 → 单次门(正阈值)必拒。
	cfg := BybitExecConfig{
		AllocatedUSDT: 60, TopN: 3,
		MinFundingRate: 0.0002, MinNetProfitRate: 0.0005, // 正单次门:旧逻辑下永远开不出
		SpotTakerFee: 0.001, PerpTakerFee: 0.00055,
		EVGateEnabled: true, RollEnabled: true, MaxHoldSettlements: 12,
	}

	// ① 粘性历史:24 条全部 0.003 → 期望 12 次(上限)×0.003×30=1.08 ≫ 费 0.093 → 通过。
	sticky := make([]float64, 24)
	for i := range sticky {
		sticky[i] = 0.003
	}
	plan, err := NewBybitShadowExecutor(mk(sticky), cfg, nil).PlanCycle(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 1 {
		t.Fatalf("粘性费率应通过 EV 门(单次门为负也该开): rejected=%+v", plan.Rejected)
	}

	// ② 一次性尖峰:历史其余全 0 → 期望 1 次×0.003×30=0.09 < 费 0.093 → 拒绝。
	spike := make([]float64, 24)
	spike[0] = 0.003
	plan, err = NewBybitShadowExecutor(mk(spike), cfg, nil).PlanCycle(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 0 || len(plan.Rejected) != 1 {
		t.Fatalf("尖峰费率 EV 为负应拒绝: accepted=%+v", plan.Accepted)
	}
	if !strings.Contains(plan.Rejected[0].Reason, "多结算期望值为负") {
		t.Fatalf("拒绝原因应为 EV 为负: %s", plan.Rejected[0].Reason)
	}
}

// TestBybitFavorableRunStats 钉死连跑统计:[有,有,无,有,无,有] → 3 段连跑(2,1,1),均长 4/3,有利 4 条。
func TestBybitFavorableRunStats(t *testing.T) {
	h := []float64{0.003, 0.003, 0, 0.003, -0.001, 0.003}
	avgRun, avgRate, fav := bybitFavorableRunStats(h, false, 0.0002)
	if fav != 4 {
		t.Fatalf("有利样本应为 4, got %d", fav)
	}
	if avgRun < 4.0/3-1e-9 || avgRun > 4.0/3+1e-9 {
		t.Fatalf("平均连跑应为 4/3, got %v", avgRun)
	}
	if avgRate < 0.003-1e-12 || avgRate > 0.003+1e-12 {
		t.Fatalf("有利均值费率应为 0.003, got %v", avgRate)
	}
}

// fakeBybitVenue 组合行情+交易 fake,满足 bybitFullVenue(manage cycle 集成测试用)。
type fakeBybitVenue struct {
	*fakeBybitMarket
	*fakeBybitTrader
}

// FetchInstruments 消除两个嵌入类型的方法歧义(取交易侧)。
func (f *fakeBybitVenue) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	return f.fakeBybitTrader.FetchInstruments(ctx)
}

// TestBybitManageCyclePreSettleExit 钉死「结算前离场」根因修复:
// Bybit 结算后把下期预测费率重置回基准正底值(0.00125%),旧逻辑在结算后 20s 复核永远读到它 → 永远续持,
// 真实费率转负后连续倒贴(MBOX 实锤付了两期)。新逻辑在结算前 150s 复核已收敛的预测费率:
// ① 费率转负 → 结算前平仓离场(一分不付);② 费率仍正 → 保持持仓穿过结算。
func TestBybitManageCyclePreSettleExit(t *testing.T) {
	run := func(t *testing.T, nextRate float64) (closed bool) {
		now := time.Now().UTC()
		sym := venue.Symbol("MBOX-USDT")
		dir := t.TempDir()
		store, err := newBybitPositionStore(dir)
		if err != nil {
			t.Fatalf("newBybitPositionStore: %v", err)
		}
		pos := BybitLivePosition{
			Symbol: sym, BaseQty: 10, SpotEntryPrice: 100, PerpEntryPrice: 100,
			SpotNotional: 1000, PerpNotional: 1000, FundingRate: 0.0026,
			NextFundingTime: now.Add(100 * time.Second), // 距结算 100s < 150s:在结算前复核窗口内
			SettlementsHeld: 7, IdemPrefix: "lx-MBOXUSDT-1", OpenedAt: now.Add(-8 * time.Hour),
		}
		if err := store.upsert(pos); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		market := &fakeBybitMarket{
			snaps: []venue.FundingSnapshot{{Symbol: sym, LastFundingRate: nextRate, IntervalHours: 1, NextFundingTime: pos.NextFundingTime}},
		}
		trader := newFakeBybitTrader()
		trader.positions = []venue.Position{{Symbol: sym, PositionAmt: -10}}
		trader.balances = venue.Balances{ByAsset: map[string]venue.Balance{"MBOX": {Free: 10}}}
		logs, err := NewRuntimeLogStore(filepath.Join(t.TempDir(), "runtime_events.jsonl"))
		if err != nil {
			t.Fatalf("NewRuntimeLogStore: %v", err)
		}
		c := &RuntimeController{logger: log.Default(), logs: logs, bybitPendingPersist: map[venue.Symbol]BybitLivePosition{}}
		cfg := DefaultEngineConfig()
		cfg.BybitExecutor.RollEnabled = true
		cfg.BybitExecutor.MaxHoldSettlements = 12
		cfg.BybitExecutor.LeadSeconds = 240
		planner := NewBybitShadowExecutor(market, bybitExecConfigFromEngine(cfg), nil)
		executor := newTestExecutor(trader)
		c.bybitManageCycle(context.Background(), cfg, store, planner, executor, &fakeBybitVenue{market, trader}, true)
		left, err := store.load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return len(left) == 0
	}

	if !run(t, -0.0017) {
		t.Fatalf("下期费率转负时应在结算前离场(平仓+删台账)")
	}
	if run(t, 0.0026) {
		t.Fatalf("下期费率仍有利时应保持持仓穿过结算,不应平仓")
	}
}

// TestBybitEffectivePositionNotionalCap 钉死名义硬顶随分配资金伸缩:静态配置 100 在调仓到 127U 后
// 必须让位给 1.6×127=203.2(否则满额两腿 ≈167 永远被拦,实锤空转一夜);配置更大时用配置;0=不限。
func TestBybitEffectivePositionNotionalCap(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.BybitExecutor.MaxPositionNotionalUSDT = 100
	cfg.AllocatedCapitalUSDT = 127
	if got := bybitEffectivePositionNotionalCap(cfg); got < 203.1 || got > 203.3 {
		t.Fatalf("应取 1.6×127=203.2, got %v", got)
	}
	cfg.AllocatedCapitalUSDT = 50
	if got := bybitEffectivePositionNotionalCap(cfg); got != 100 {
		t.Fatalf("配置值更大时应用配置 100, got %v", got)
	}
	cfg.BybitExecutor.MaxPositionNotionalUSDT = 0
	if got := bybitEffectivePositionNotionalCap(cfg); got != 0 {
		t.Fatalf("0 应保持不限语义, got %v", got)
	}
}

// TestBybitRollSwitchReason 钉死续持换仓判定:① 在开仓窗口内、费率/小时 ≥ 当前下期 3 倍的新候选 → 换仓;
// ② 同一标的不换(纯烧手续费);③ 新候选不在开仓窗口内不换(换过去也要闲置等窗口)。
func TestBybitRollSwitchReason(t *testing.T) {
	now := time.Now().UTC()
	held := BybitLivePosition{Symbol: "MBOX-USDT", BaseQty: 6266}
	heldSnap := venue.FundingSnapshot{Symbol: "MBOX-USDT", LastFundingRate: 0.0000125, IntervalHours: 1} // 0.00125%/h
	lead := 30 * time.Minute
	mkMarket := func(sym venue.Symbol, next time.Time) *fakeBybitMarket {
		return &fakeBybitMarket{
			snaps: []venue.FundingSnapshot{{Symbol: sym, LastFundingRate: 0.002, IntervalHours: 1, NextFundingTime: next}},
			instr: []venue.Instrument{
				{Symbol: sym, Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5},
				{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5},
			},
			tickers: map[instrumentKey]venue.BookTicker{
				{sym, venue.MarketSpot}: {BidPrice: 100, AskPrice: 100},
				{sym, venue.MarketPerp}: {BidPrice: 100, AskPrice: 100},
			},
			cost: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.0006},
		}
	}
	cfg := BybitExecConfig{AllocatedUSDT: 60, TopN: 3, MinFundingRate: 0.0002, MinNetProfitRate: -0.005}
	c := &RuntimeController{}

	// ① 肥候选在窗口内(0.20%/h ≥ 0.00125%/h × 3)→ 应换仓。
	planner := NewBybitShadowExecutor(mkMarket("FAT-USDT", now.Add(10*time.Minute)), cfg, nil)
	if reason := c.bybitRollSwitchReason(context.Background(), planner, held, heldSnap, now, lead); reason == "" {
		t.Fatalf("窗口内的显著更肥候选应触发换仓")
	}
	// ② 同一标的不换。
	planner = NewBybitShadowExecutor(mkMarket("MBOX-USDT", now.Add(10*time.Minute)), cfg, nil)
	if reason := c.bybitRollSwitchReason(context.Background(), planner, held, heldSnap, now, lead); reason != "" {
		t.Fatalf("同标的不应换仓: %s", reason)
	}
	// ③ 不在开仓窗口(结算还有 2h,lead=30min)不换。
	planner = NewBybitShadowExecutor(mkMarket("FAT-USDT", now.Add(2*time.Hour)), cfg, nil)
	if reason := c.bybitRollSwitchReason(context.Background(), planner, held, heldSnap, now, lead); reason != "" {
		t.Fatalf("窗口外候选不应换仓: %s", reason)
	}
}
