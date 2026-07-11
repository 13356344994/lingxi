package portfolio

import (
	"testing"

	"lingxi/internal/mechanism"
	"lingxi/internal/money"
)

func validProposal(id, cluster string, notional, net, stress float64, seconds int64) Proposal {
	return Proposal{
		ID:                  id,
		Cluster:             cluster,
		Direction:           mechanism.DirectionLong,
		Notional:            money.FromUSDT(notional),
		LotNotional:         money.FromUSDT(10),
		ConservativeNetUSDT: money.FromUSDT(net),
		StressLossUSDT:      money.FromUSDT(stress),
		MarginOccupation:    money.FromUSDT(10),
		MarginTimeSec:       seconds,
	}
}

func TestRunAuctionRejectsUntradeables(t *testing.T) {
	props := []Proposal{
		validProposal("ood", "a", 100, 10, 5, 10),
		validProposal("red", "a", 100, 10, 5, 10),
		validProposal("neg", "a", 100, -5, 5, 10),
		validProposal("zero_notional", "a", 0, 10, 5, 10),
		validProposal("good", "a", 100, 10, 5, 10),
	}
	props[0].OutOfDistribution = true
	props[1].DataRed = true
	res := RunAuction(props, Budget{AccountNotional: money.FromUSDT(1000), ClusterNotional: map[string]money.Micros{"a": money.FromUSDT(1000)}})
	if len(res.Accepted) != 1 || res.Accepted[0].Proposal.ID != "good" {
		t.Fatalf("only 'good' should be accepted, got %+v", res.Accepted)
	}
	if len(res.Rejected) != 4 {
		t.Fatalf("expected 4 rejections, got %d", len(res.Rejected))
	}
	reasons := map[string]string{}
	for _, r := range res.Rejected {
		reasons[r.Proposal.ID] = r.Reason
	}
	if reasons["ood"] != RejectOutOfDistribution || reasons["red"] != RejectDataRed ||
		reasons["neg"] != RejectNoPositiveNet || reasons["zero_notional"] != RejectNonPositiveNotional {
		t.Fatalf("reject reasons mismatch: %+v", reasons)
	}
}

// TestRunAuctionRanksByEfficiency 高效率提案优先获配。
func TestRunAuctionRanksByEfficiency(t *testing.T) {
	// low: net 10 / (loss 10 * time 10) 效率低；high: net 10 / (loss 1 * time 1) 效率高。
	props := []Proposal{
		validProposal("low", "a", 600, 10, 10, 10),
		validProposal("high", "a", 600, 10, 1, 1),
	}
	res := RunAuction(props, Budget{AccountNotional: money.FromUSDT(1000), ClusterNotional: map[string]money.Micros{"a": money.FromUSDT(1000)}})
	if len(res.Accepted) < 1 || res.Accepted[0].Proposal.ID != "high" {
		t.Fatalf("high-efficiency proposal should rank first, got %+v", res.Accepted)
	}
}

// TestRunAuctionRespectsClusterBudget 簇预算独立于账户预算约束分配。
func TestRunAuctionRespectsClusterBudget(t *testing.T) {
	props := []Proposal{
		validProposal("p1", "a", 300, 20, 1, 1),
		validProposal("p2", "a", 300, 10, 1, 1),
	}
	// 账户预算充足(1000)，但簇 a 只有 400 → 第一个拿 300，第二个只能拿 100。
	res := RunAuction(props, Budget{AccountNotional: money.FromUSDT(1000), ClusterNotional: map[string]money.Micros{"a": money.FromUSDT(400)}})
	if res.UsedCluster["a"] != money.FromUSDT(400) {
		t.Fatalf("cluster a should be fully used (400), got %s", res.UsedCluster["a"])
	}
	var p2 AcceptedProposal
	for _, a := range res.Accepted {
		if a.Proposal.ID == "p2" {
			p2 = a
		}
	}
	if p2.AllocatedNotional != money.FromUSDT(100) {
		t.Fatalf("p2 should be clipped to 100 by cluster budget, got %s", p2.AllocatedNotional)
	}
}

// TestRunAuctionUnlistedClusterGetsNothing 未列入预算的簇不予分配。
func TestRunAuctionUnlistedClusterGetsNothing(t *testing.T) {
	props := []Proposal{
		validProposal("p", "ghost", 100, 10, 1, 1),
	}
	res := RunAuction(props, Budget{AccountNotional: money.FromUSDT(1000), ClusterNotional: map[string]money.Micros{"a": money.FromUSDT(1000)}})
	if len(res.Accepted) != 0 || len(res.Rejected) != 1 || res.Rejected[0].Reason != RejectBudgetExhausted {
		t.Fatalf("unlisted cluster should get nothing: %+v", res)
	}
}

func TestRunAuctionNeverClipsWithoutLotGrid(t *testing.T) {
	p := validProposal("p", "a", 100, 10, 1, 1)
	p.LotNotional = 0
	res := RunAuction(
		[]Proposal{p},
		Budget{AccountNotional: money.FromUSDT(95), ClusterNotional: map[string]money.Micros{"a": money.FromUSDT(95)}},
	)
	if len(res.Accepted) != 0 || len(res.Rejected) != 1 || res.Rejected[0].Reason != RejectBudgetExhausted {
		t.Fatalf("unknown lot grid must not be clipped into an invalid order: %+v", res)
	}
}

// TestStopErrorOnlyDependsOnThesisDeath 止错只看论点死亡，不看账户。
func TestStopErrorOnlyDependsOnThesisDeath(t *testing.T) {
	cases := []struct {
		name   string
		h      ThesisHealth
		exit   bool
		reason string
	}{
		{"alive", ThesisHealth{NowUnixMs: 100, DeadlineUnixMs: 1000}, false, ""},
		{"invalidator", ThesisHealth{NowUnixMs: 100, DeadlineUnixMs: 1000, InvalidatorFired: true}, true, StopErrorReasonInvalidator},
		{"deadline", ThesisHealth{NowUnixMs: 1000, DeadlineUnixMs: 1000}, true, StopErrorReasonDeadline},
		{"payer_gone", ThesisHealth{NowUnixMs: 100, DeadlineUnixMs: 1000, PayerGone: true}, true, StopErrorReasonPayerGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateStopError(StopErrorHolding, tc.h)
			if d.Exit != tc.exit {
				t.Fatalf("exit = %v, want %v", d.Exit, tc.exit)
			}
			if tc.exit && d.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", d.Reason, tc.reason)
			}
		})
	}

	// 一旦 exit 不可逆。
	d := EvaluateStopError(StopErrorExit, ThesisHealth{NowUnixMs: 1, DeadlineUnixMs: 1000})
	if !d.Exit {
		t.Fatal("exit state must be sticky")
	}
}

// TestSolvencyIndependentAndOrdersByMarginalRisk 偿付只看缓冲，按边际风险最大优先削减。
func TestSolvencyIndependentAndOrdersByMarginalRisk(t *testing.T) {
	// 权益充足 → 不击穿。
	ok := EvaluateSolvency(SolvencyInputs{SolvencyEquity: money.FromUSDT(1000), RequiredBuffer: money.FromUSDT(500)})
	if ok.Breached {
		t.Fatal("sufficient equity must not breach")
	}

	in := SolvencyInputs{
		SolvencyEquity: money.FromUSDT(100),
		RequiredBuffer: money.FromUSDT(250),
		Positions: []PositionRisk{
			{ID: "small", MarginalStressLoss: money.FromUSDT(10), ReducibleNotional: money.FromUSDT(50)},
			{ID: "big", MarginalStressLoss: money.FromUSDT(100), ReducibleNotional: money.FromUSDT(500)},
			{ID: "mid", MarginalStressLoss: money.FromUSDT(60), ReducibleNotional: money.FromUSDT(10)},
		},
	}
	d := EvaluateSolvency(in)
	if !d.Breached {
		t.Fatal("shortfall should breach")
	}
	if d.Shortfall != money.FromUSDT(150) {
		t.Fatalf("shortfall = %s, want 150", d.Shortfall)
	}
	// big 虽可削 500 名义，但只恢复 100 缓冲，不能拿名义直接抵 150 权益缺口；
	// 必须再削 mid，累计恢复 160 缓冲后才停止。
	if len(d.ReduceOrder) != 2 || d.ReduceOrder[0] != "big" || d.ReduceOrder[1] != "mid" {
		t.Fatalf("reduce order should be [big mid] by marginal risk, got %v", d.ReduceOrder)
	}
	if d.RecoveredBuffer != money.FromUSDT(160) || d.ReduceNotional != money.FromUSDT(510) {
		t.Fatalf("recovery/notional accounting mismatch: %+v", d)
	}
}
