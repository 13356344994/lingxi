package app

import (
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestSelectBybitCandidatesFiltersAndRanks(t *testing.T) {
	now := time.Now()
	snaps := []venue.FundingSnapshot{
		{Symbol: "AAA-USDT", LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now},
		{Symbol: "BBB-USDT", LastFundingRate: 0.003, IntervalHours: 8, NextFundingTime: now},
		{Symbol: "CCC-USDT", LastFundingRate: 0.0002, IntervalHours: 8, NextFundingTime: now}, // 低于阈值
		{Symbol: "DDD-USDT", LastFundingRate: -0.005, IntervalHours: 8, NextFundingTime: now}, // 负费率(本所不做)
		{Symbol: "EEE-USDT", LastFundingRate: 0.002, IntervalHours: 8, NextFundingTime: now},
	}
	cfg := BybitExecConfig{MinFundingRate: 0.0005, TopN: 2}
	got := selectBybitCandidates(snaps, cfg)
	if len(got) != 2 {
		t.Fatalf("应选 2 个, got %d", len(got))
	}
	if got[0].Symbol != "BBB-USDT" || got[1].Symbol != "EEE-USDT" {
		t.Errorf("排序错误: %+v", got)
	}
}

func TestSelectBybitCandidatesExcludesNegativeAndBelowThreshold(t *testing.T) {
	snaps := []venue.FundingSnapshot{
		{Symbol: "NEG-USDT", LastFundingRate: -0.01},
		{Symbol: "LOW-USDT", LastFundingRate: 0.0001},
	}
	got := selectBybitCandidates(snaps, BybitExecConfig{MinFundingRate: 0.0005, TopN: 5})
	if len(got) != 0 {
		t.Fatalf("负费率与低于阈值都应排除, got %+v", got)
	}
}

func TestPlanDeltaNeutralOpenHappyPath(t *testing.T) {
	c := BybitCandidate{Symbol: "BTC-USDT", FundingRate: 0.005, IntervalHours: 8}
	spotInstr := venue.Instrument{Symbol: "BTC-USDT", Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5}
	perpInstr := venue.Instrument{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5}
	cfg := BybitExecConfig{MinNetProfitRate: 0.0005, SpotTakerFee: 0.001, PerpTakerFee: 0.0006}
	// perCandidate=2000 → 每腿 1000;价 60000 → target qty 0.01666 → 取整 step 0.001 → 0.016
	plan, ok, reason := planBybitDeltaNeutralOpen(c, 60000, 60000, spotInstr, perpInstr, 2000, cfg)
	if !ok {
		t.Fatalf("应可下单, reason=%s plan=%+v", reason, plan)
	}
	if plan.BaseQty != 0.016 {
		t.Errorf("BaseQty 取整错误: %v", plan.BaseQty)
	}
	// 毛资金费 = perpNotional(0.016*60000=960) * 0.005 = 4.8
	if !approxEqBybit(plan.EstGrossFunding, 0.016*60000*0.005) {
		t.Errorf("毛资金费错误: %v", plan.EstGrossFunding)
	}
	wantFee := 960*0.001*2 + 960*0.0006*2
	if !approxEqBybit(plan.EstFeeCost, wantFee) {
		t.Errorf("费用错误: got %v want %v", plan.EstFeeCost, wantFee)
	}
	if plan.DeployedUSDT <= 0 || plan.EstNetProfitRate <= 0 {
		t.Errorf("部署/净利率异常: %+v", plan)
	}
}

func TestPlanRejectsBelowMinNotional(t *testing.T) {
	c := BybitCandidate{Symbol: "BTC-USDT", FundingRate: 0.003}
	spotInstr := venue.Instrument{StepSize: 0.001, MinNotional: 100000}
	perpInstr := venue.Instrument{StepSize: 0.001, MinNotional: 5}
	cfg := BybitExecConfig{MinNetProfitRate: 0.0005, SpotTakerFee: 0.001, PerpTakerFee: 0.0006}
	_, ok, reason := planBybitDeltaNeutralOpen(c, 60000, 60000, spotInstr, perpInstr, 2000, cfg)
	if ok {
		t.Fatal("现货名义低于最小下单额应放弃")
	}
	if reason != "现货名义低于最小下单额" {
		t.Errorf("原因不符: %s", reason)
	}
}

func TestPlanRejectsBelowNetProfitThreshold(t *testing.T) {
	c := BybitCandidate{Symbol: "BTC-USDT", FundingRate: 0.0006} // 费率刚过开仓阈值,但吃不住手续费
	spotInstr := venue.Instrument{StepSize: 0.001, MinNotional: 5}
	perpInstr := venue.Instrument{StepSize: 0.001, MinNotional: 5}
	cfg := BybitExecConfig{MinNetProfitRate: 0.0005, SpotTakerFee: 0.001, PerpTakerFee: 0.0006}
	plan, ok, reason := planBybitDeltaNeutralOpen(c, 60000, 60000, spotInstr, perpInstr, 2000, cfg)
	if ok {
		t.Fatalf("净利率应低于阈值被放弃, plan=%+v", plan)
	}
	if reason != "成本后净利率低于阈值" {
		t.Errorf("原因不符: %s", reason)
	}
}

func TestPlanRejectsZeroQty(t *testing.T) {
	c := BybitCandidate{Symbol: "BTC-USDT", FundingRate: 0.003}
	spotInstr := venue.Instrument{StepSize: 1, MinNotional: 5}
	perpInstr := venue.Instrument{StepSize: 1, MinNotional: 5}
	cfg := BybitExecConfig{MinNetProfitRate: 0.0005}
	// 每腿 1000 / 60000 = 0.0166,step=1 取整 → 0 → 放弃
	_, ok, _ := planBybitDeltaNeutralOpen(c, 60000, 60000, spotInstr, perpInstr, 2000, cfg)
	if ok {
		t.Fatal("取整后数量为 0 应放弃")
	}
}

func approxEqBybit(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-9
}
