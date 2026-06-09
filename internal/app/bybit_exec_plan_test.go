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

func TestSelectBybitCandidatesNegativeCarryWhenEnabled(t *testing.T) {
	now := time.Now()
	snaps := []venue.FundingSnapshot{
		{Symbol: "POS-USDT", LastFundingRate: 0.001, NextFundingTime: now},
		{Symbol: "NEGBIG-USDT", LastFundingRate: -0.008, NextFundingTime: now},
		{Symbol: "NEGSMALL-USDT", LastFundingRate: -0.0006, NextFundingTime: now},
		{Symbol: "NEGTINY-USDT", LastFundingRate: -0.0001, NextFundingTime: now}, // |费率| 低于阈值,排除
	}
	cfg := BybitExecConfig{MinFundingRate: 0.0005, TopN: 5, EnableNegativeCarry: true}
	got := selectBybitCandidates(snaps, cfg)
	if len(got) != 3 {
		t.Fatalf("应选 3 个(正 1 + 负 2,排除低于阈值的负), got %d: %+v", len(got), got)
	}
	// 按 |费率| 降序:NEGBIG(0.008) > POS(0.001) > NEGSMALL(0.0006)。
	if got[0].Symbol != "NEGBIG-USDT" || !got[0].Negative {
		t.Fatalf("最肥候选应为 NEGBIG 且标记负 carry, got %+v", got[0])
	}
	if got[1].Symbol != "POS-USDT" || got[1].Negative {
		t.Fatalf("次候选应为 POS 且正 carry, got %+v", got[1])
	}
	if got[2].Symbol != "NEGSMALL-USDT" || !got[2].Negative {
		t.Fatalf("第三候选应为 NEGSMALL 且负 carry, got %+v", got[2])
	}
}

func TestPlanDeltaNeutralOpenNegativeUsesAbsFundingAndSetsDirection(t *testing.T) {
	c := BybitCandidate{Symbol: "HOME-USDT", FundingRate: -0.004, Negative: true, NextFundingTime: time.Now()}
	spotInstr := venue.Instrument{Symbol: "HOME-USDT", Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 1}
	perpInstr := venue.Instrument{Symbol: "HOME-USDT", Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 1}
	cfg := BybitExecConfig{SpotTakerFee: 0.0002, PerpTakerFee: 0.0002, MinNetProfitRate: -1, EnableNegativeCarry: true}
	plan, ok, reason := planBybitDeltaNeutralOpen(c, 100, 100, spotInstr, perpInstr, 200, cfg)
	if !ok {
		t.Fatalf("应可下单, reason=%s", reason)
	}
	if !plan.Negative {
		t.Fatal("计划应标记为负 carry")
	}
	// 毛资金费按 |费率| 计:名义 100 × 0.004 = 0.4(收益为正,而非 -0.4)。
	if plan.EstGrossFunding <= 0 {
		t.Fatalf("负 carry 毛资金费应为正(按 |费率|), got %.6f", plan.EstGrossFunding)
	}
	if plan.EstGrossFunding != plan.PerpNotional*0.004 {
		t.Fatalf("毛资金费应= 名义×|费率|=%.6f, got %.6f", plan.PerpNotional*0.004, plan.EstGrossFunding)
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
