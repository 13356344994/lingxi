package app

import (
	"context"
	"testing"

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
	plan, err := exec.PlanCycle(context.Background())
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
	plan, err := exec.PlanCycle(context.Background())
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

func TestShadowPlanCycleNoCandidates(t *testing.T) {
	f := &fakeBybitMarket{
		snaps: []venue.FundingSnapshot{{Symbol: "X-USDT", LastFundingRate: -0.01}},
	}
	exec := NewBybitShadowExecutor(f, BybitExecConfig{AllocatedUSDT: 1000, MinFundingRate: 0.0005}, nil)
	plan, err := exec.PlanCycle(context.Background())
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	if len(plan.Accepted) != 0 || len(plan.Candidates) != 0 {
		t.Fatalf("无正费率候选时应空: %+v", plan)
	}
}
