package mechanism

import (
	"math"
	"testing"

	"lingxi/internal/money"
)

func basePanicCard() MechanismEventCard {
	return MechanismEventCard{
		ID:         "panic-1",
		Instrument: "X-USDT-SWAP",
		Regime:     Regime{Kind: RegimePanicLiquidation, DeadlineUnixMs: 10_000, Descriptor: "cascade"},
		ForcedFlow: ForcedFlow{
			Payer:             PayerLongLiquidations,
			Side:              DirectionShort,
			ObservedNotional:  money.FromUSDT(1_000_000),
			RemainingNotional: money.FromUSDT(800_000),
			Decaying:          false,
		},
		Transmission:  Transmission{Cluster: "cluster-a", AbsorptionRatio: 0.2},
		Executability: Executability{TakerFeasible: true, LotNotional: money.FromUSDT(10), TopDepthNotional: money.FromUSDT(500_000)},
		Quality:       Quality{ObservationConfidence: 0.7},
	}
}

// TestPanicRelayFollowsForcedFlow 接力延续：被迫抛压仍充沛、吸收低 → 顺被迫流方向（做空）。
func TestPanicRelayFollowsForcedFlow(t *testing.T) {
	ev := PanicLiquidationExpert{}.Evaluate(basePanicCard())
	if !ev.Accepted {
		t.Fatalf("expected accept, got reject %q", ev.RejectReason)
	}
	if ev.Thesis.Direction != DirectionShort {
		t.Fatalf("relay should follow forced flow (short), got %q", ev.Thesis.Direction)
	}
	if ev.Thesis.Payer != PayerLongLiquidations {
		t.Fatalf("payer mismatch: %q", ev.Thesis.Payer)
	}
	if ev.Thesis.Capacity <= 0 {
		t.Fatal("capacity should be positive")
	}
}

// TestPanicExhaustionReverses 竭尽反转：被迫流衰减 + 剩余极少 + 吸收高 → 逆被迫流（做多）。
func TestPanicExhaustionReverses(t *testing.T) {
	c := basePanicCard()
	c.ForcedFlow.Decaying = true
	c.ForcedFlow.RemainingNotional = money.FromUSDT(50_000) // < 20% of observed 1,000,000
	c.Transmission.AbsorptionRatio = 0.8
	ev := PanicLiquidationExpert{}.Evaluate(c)
	if !ev.Accepted {
		t.Fatalf("expected accept, got reject %q", ev.RejectReason)
	}
	if ev.Thesis.Direction != DirectionLong {
		t.Fatalf("exhaustion should reverse forced flow (long), got %q", ev.Thesis.Direction)
	}
}

func TestPanicRejectsRegimeMismatchAndOOD(t *testing.T) {
	c := basePanicCard()
	c.Regime.Kind = RegimeRuleEvent
	if ev := (PanicLiquidationExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectRegimeMismatch {
		t.Fatalf("regime mismatch not rejected: %+v", ev)
	}

	c = basePanicCard()
	c.Quality.OutOfDistribution = true
	if ev := (PanicLiquidationExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectOutOfDistribution {
		t.Fatalf("OOD not rejected: %+v", ev)
	}

	c = basePanicCard()
	c.ForcedFlow.Payer = PayerUnknown
	if ev := (PanicLiquidationExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectNoPayer {
		t.Fatalf("missing payer not rejected: %+v", ev)
	}

	c = basePanicCard()
	c.Transmission.AbsorptionRatio = math.NaN()
	if ev := (PanicLiquidationExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectMissingEvidence {
		t.Fatalf("non-finite absorption not rejected: %+v", ev)
	}
}

func TestFundingSettlementUnwind(t *testing.T) {
	c := MechanismEventCard{
		ID:     "fund-1",
		Regime: Regime{Kind: RegimeFundingSettlement, DeadlineUnixMs: 5_000},
		ForcedFlow: ForcedFlow{
			Payer:             PayerFundingReceivers,
			Side:              DirectionShort,
			RemainingNotional: money.FromUSDT(300_000),
		},
		Transmission:  Transmission{Cluster: "cluster-b"},
		Executability: Executability{TakerFeasible: true, LotNotional: money.FromUSDT(10)},
		Quality:       Quality{ObservationConfidence: 0.6},
		Signals:       map[string]float64{signalUnwindWindowActive: 1, signalFundingRate: -0.003},
	}
	ev := FundingSettlementUnwindExpert{}.Evaluate(c)
	if !ev.Accepted {
		t.Fatalf("expected accept, got reject %q", ev.RejectReason)
	}
	if ev.Thesis.Direction != DirectionShort {
		t.Fatalf("direction should follow forced side, got %q", ev.Thesis.Direction)
	}

	// 窗口未激活 → 缺期限被拒。
	c.Signals[signalUnwindWindowActive] = 0
	if ev := (FundingSettlementUnwindExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectNoDeadline {
		t.Fatalf("inactive window should reject as no_deadline: %+v", ev)
	}

	// 费率过小 → 缺被迫动机（容量拒绝）。
	c.Signals[signalUnwindWindowActive] = 1
	c.Signals[signalFundingRate] = 0.0001
	if ev := (FundingSettlementUnwindExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectNoCapacity {
		t.Fatalf("tiny funding should reject: %+v", ev)
	}

	delete(c.Signals, signalFundingRate)
	if ev := (FundingSettlementUnwindExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectMissingEvidence {
		t.Fatalf("missing funding signal should reject: %+v", ev)
	}
}

func TestTieredLiquidationRelayVsExhaustion(t *testing.T) {
	base := MechanismEventCard{
		ID:     "tier-1",
		Regime: Regime{Kind: RegimeTieredLiquidation, DeadlineUnixMs: 8_000},
		ForcedFlow: ForcedFlow{
			Payer:             PayerShortLiquidations,
			Side:              DirectionLong,
			RemainingNotional: money.FromUSDT(200_000),
		},
		Transmission:  Transmission{Cluster: "cluster-c", AbsorptionRatio: 0.7},
		Executability: Executability{TakerFeasible: true, LotNotional: money.FromUSDT(10)},
		Quality:       Quality{ObservationConfidence: 0.5},
		Signals:       map[string]float64{signalTiersRemaining: 3},
	}
	// 还有档 → 接力顺被迫流（做多）。
	if ev := (TieredLiquidationExpert{}).Evaluate(base); !ev.Accepted || ev.Thesis.Direction != DirectionLong {
		t.Fatalf("relay should follow forced flow long: %+v", ev)
	}
	// 最后一档 + 吸收高 → 竭尽反转（做空）。
	base.Signals[signalTiersRemaining] = 0
	if ev := (TieredLiquidationExpert{}).Evaluate(base); !ev.Accepted || ev.Thesis.Direction != DirectionShort {
		t.Fatalf("last-tier exhaustion should reverse to short: %+v", ev)
	}

	delete(base.Signals, signalTiersRemaining)
	if ev := (TieredLiquidationExpert{}).Evaluate(base); ev.Accepted || ev.RejectReason != RejectMissingEvidence {
		t.Fatalf("missing tier count must not fabricate last-tier exhaustion: %+v", ev)
	}
}

func TestRuleEventRequiresDeadline(t *testing.T) {
	c := MechanismEventCard{
		ID:     "rule-1",
		Regime: Regime{Kind: RegimeRuleEvent, DeadlineUnixMs: 20_000, Descriptor: "delisting"},
		ForcedFlow: ForcedFlow{
			Payer:             PayerForcedClosers,
			Side:              DirectionShort,
			RemainingNotional: money.FromUSDT(100_000),
		},
		Transmission:  Transmission{Cluster: "cluster-d"},
		Executability: Executability{TakerFeasible: true, LotNotional: money.FromUSDT(10)},
		Quality:       Quality{ObservationConfidence: 0.4},
	}
	if ev := (RuleEventExpert{}).Evaluate(c); !ev.Accepted || ev.Thesis.Direction != DirectionShort {
		t.Fatalf("rule event should accept short: %+v", ev)
	}
	c.Regime.DeadlineUnixMs = 0
	if ev := (RuleEventExpert{}).Evaluate(c); ev.Accepted || ev.RejectReason != RejectNoDeadline {
		t.Fatalf("rule without deadline must be rejected: %+v", ev)
	}
}

// TestExpertsSatisfyInterface 确保四个专家都实现 Expert 接口。
func TestExpertsSatisfyInterface(t *testing.T) {
	var experts = []Expert{
		PanicLiquidationExpert{},
		FundingSettlementUnwindExpert{},
		TieredLiquidationExpert{},
		RuleEventExpert{},
	}
	for _, e := range experts {
		if e.Name() == "" {
			t.Fatal("expert name must be non-empty")
		}
	}
}
