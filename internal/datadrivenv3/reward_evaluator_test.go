package datadrivenv3

import (
	"math"
	"reflect"
	"testing"
)

func knownCost() RewardCostInput {
	return RewardCostInput{Known: true, RoundTripFeeBPS: 10, SpreadBPS: 2, CapitalUSDT: 100, RollingQuoteVolumeUSDT: 1e9, BarHigh: 101, BarLow: 99, BarMid: 100}
}
func baseReward(direction Direction) RewardInput {
	return RewardInput{BaselineID: "shared", Direction: direction, EntryPrice: 100, ExitPrice: 110, PositionNotional: 50, EntryMS: 1, ExitMS: 3, Matured: true, Funding: []FundingSettlement{{TimeMS: 2, Rate: .01}}, Cost: knownCost(), Cash: 100, MarkedEquity: 100, RetainedCapitalBase: 20, MaintenanceMarginKnown: true}
}
func TestRewardLongShortAndSignedFunding(t *testing.T) {
	long, err := EvaluateReward(baseReward(DirectionLong))
	if err != nil {
		t.Fatal(err)
	}
	shortInput := baseReward(DirectionShort)
	short, err := EvaluateReward(shortInput)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(long.MTM-5) > 1e-9 || math.Abs(short.MTM+5) > 1e-9 {
		t.Fatalf("mtm long=%v short=%v", long.MTM, short.MTM)
	}
	if long.FundingCashflow != -.5 || short.FundingCashflow != .5 {
		t.Fatalf("funding long=%v short=%v", long.FundingCashflow, short.FundingCashflow)
	}
}
func TestRewardUnknownCostPendingAndEOF(t *testing.T) {
	in := baseReward(DirectionLong)
	in.Cost.Known = false
	got, _ := EvaluateReward(in)
	if !got.NoTrade || !got.UnknownCost {
		t.Fatalf("unknown cost=%+v", got)
	}
	in = baseReward(DirectionLong)
	in.Matured = false
	got, _ = EvaluateReward(in)
	if !got.Pending {
		t.Fatalf("pending=%+v", got)
	}
	in.EOF = true
	got, _ = EvaluateReward(in)
	if got.Pending || !got.EOFDiscarded {
		t.Fatalf("eof=%+v", got)
	}
}
func TestRewardWithdrawalMultiTrancheAndMMRUnknown(t *testing.T) {
	in := baseReward(DirectionLong)
	in.ExitPrice = 100
	in.Funding = nil
	in.TotalAbsNotional = 50
	in.MarkedEquity = 100
	in.Cash = 100
	in.WithdrawalTranches = 3
	got, err := EvaluateReward(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Withdrawals, []float64{10, 10, 10}) || got.RequiredReserve != 10 || got.EndCash >= 100 {
		t.Fatalf("tranches=%+v", got)
	}
	in.MaintenanceMarginKnown = false
	got, _ = EvaluateReward(in)
	if !got.WithdrawalDeferred || len(got.Withdrawals) != 0 {
		t.Fatalf("unknown mmr=%+v", got)
	}
}
func TestRewardPartialFreeEquityFiveXAndNoNegativeCash(t *testing.T) {
	in := baseReward(DirectionLong)
	in.ExitPrice = 100
	in.Funding = nil
	in.TotalAbsNotional = 250
	in.MarkedEquity = 55
	in.Cash = 55
	in.RetainedCapitalBase = 20
	in.WithdrawalTranches = 1
	got, _ := EvaluateReward(in)
	if got.RequiredReserve != 50 || len(got.Withdrawals) != 1 || got.Withdrawals[0] <= 0 || got.Withdrawals[0] > 5 {
		t.Fatalf("free equity=%+v", got)
	}
	in.Cash = 0
	in.MarkedEquity = 0
	got, _ = EvaluateReward(in)
	if got.EndCash < 0 || !got.Ruin {
		t.Fatalf("negative cash guard=%+v", got)
	}
}
func TestBaselineUsesSharedEvaluator(t *testing.T) {
	in := baseReward(DirectionShort)
	direct, _ := EvaluateReward(in)
	baseline, _ := EvaluateBaseline("fixed-short", in)
	direct.BaselineID = "fixed-short"
	if !reflect.DeepEqual(direct, baseline) {
		t.Fatalf("baseline drift direct=%+v baseline=%+v", direct, baseline)
	}
}

func TestExecutionCostComponentsAndStress(t *testing.T) {
	base := RewardCostInput{Known: true, RoundTripFeeBPS: 10, SpreadBPS: 1, CapitalUSDT: 100, RollingQuoteVolumeUSDT: 100000, BarHigh: 101, BarLow: 99, BarMid: 100}
	normal, err := executionCostBPS(base)
	if err != nil {
		t.Fatal(err)
	}
	base.StressMultiplier = 1.5
	stress, err := executionCostBPS(base)
	if err != nil {
		t.Fatal(err)
	}
	if normal <= 12 || math.Abs(stress-normal*1.5) > 1e-12 {
		t.Fatalf("cost normal=%v stress=%v", normal, stress)
	}
}
