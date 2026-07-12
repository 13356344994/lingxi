package actionspace

import (
	"math"
	"testing"

	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestFlatOpenTransitions(t *testing.T) {
	flat := PositionState{Side: SideFlat, EquityUSDT: BaseEquityUSDT}
	for _, verb := range []string{VerbOpenLong, VerbOpenShort} {
		action := Action{Verb: verb, SizeFraction: 0.5, DelayBars: 0, TargetHorizon: "1h"}
		if err := ValidateTransition(flat, action); err != nil {
			t.Fatalf("%s should be valid from flat: %v", verb, err)
		}
		state, turnover, err := ApplyTransition(flat, action, 100)
		if err != nil || turnover <= 0 {
			t.Fatalf("%s apply failed: state=%+v turnover=%v err=%v", verb, state, turnover, err)
		}
	}
}

func TestInvalidTransitionsFail(t *testing.T) {
	flat := PositionState{Side: SideFlat, EquityUSDT: BaseEquityUSDT}
	long := PositionState{Side: SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: BaseEquityUSDT}
	for _, action := range []Action{
		{Verb: VerbTrim, SizeFraction: 0.1, TargetHorizon: "1h"},
		{Verb: VerbHold, SizeFraction: 0, TargetHorizon: "1h"},
		{Verb: VerbClose, SizeFraction: 0, TargetHorizon: "1h"},
	} {
		if err := ValidateTransition(flat, action); err == nil {
			t.Fatalf("flat must reject %s", action.Verb)
		}
	}
	if err := ValidateTransition(long, Action{Verb: VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"}); err == nil {
		t.Fatal("long must reject open_long")
	}
}

func TestHoldIncreaseTrimCloseReverse(t *testing.T) {
	long := PositionState{Side: SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: BaseEquityUSDT}
	cases := []struct {
		verb string
		size float64
	}{
		{VerbHold, 0},
		{VerbIncrease, 0.25},
		{VerbTrim, 0.25},
		{VerbClose, 0},
		{VerbReverse, 0.5},
	}
	for _, tc := range cases {
		action := Action{Verb: tc.verb, SizeFraction: tc.size, TargetHorizon: "1h", DelayBars: 0}
		if err := ValidateTransition(long, action); err != nil {
			t.Fatalf("%s invalid: %v", tc.verb, err)
		}
		if _, _, err := ApplyTransition(long, action, 101); err != nil {
			t.Fatalf("%s apply failed: %v", tc.verb, err)
		}
	}
}

func TestSizeCapAndLeverage(t *testing.T) {
	long := PositionState{Side: SideLong, SizeFraction: 0.75, BasisPrice: 100, EquityUSDT: BaseEquityUSDT}
	action := Action{Verb: VerbIncrease, SizeFraction: 0.5, TargetHorizon: "1h"}
	if _, _, err := ApplyTransition(long, action, 100); err == nil {
		t.Fatal("increase beyond 1.0 must fail")
	}
	state := PositionState{Side: SideLong, SizeFraction: 1.0, BasisPrice: 100, EquityUSDT: BaseEquityUSDT}
	max := state.EquityUSDT * float64(g5protocol.MaxAccountLeverage)
	if NotionalUSDT(state) != max {
		t.Fatalf("notional=%v want %v", NotionalUSDT(state), max)
	}
}

func TestEnumerateFlatAndPositioned(t *testing.T) {
	horizons := []string{"1h", "4h"}
	flat, err := EnumerateActions("TEST", PositionState{Side: SideFlat, EquityUSDT: BaseEquityUSDT}, horizons)
	if err != nil || len(flat) == 0 {
		t.Fatalf("flat enumeration failed: len=%d err=%v", len(flat), err)
	}
	long, err := EnumerateActions("TEST", PositionState{Side: SideLong, SizeFraction: 0.5, EquityUSDT: BaseEquityUSDT}, horizons)
	if err != nil || len(long) == 0 {
		t.Fatalf("long enumeration failed")
	}
	if len(long) <= len(flat) {
		t.Fatalf("positioned state should expand action grid")
	}
}

func TestMarkEquityIncremental(t *testing.T) {
	state := PositionState{Side: SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: BaseEquityUSDT, FixedNotionalUSDT: DeployNotionalUSDT(BaseEquityUSDT, 0.5)}
	start := MarkEquity(state, 100)
	end := MarkEquity(state, 110)
	if end <= start {
		t.Fatalf("long mark must increase with price: %v -> %v", start, end)
	}
}

func TestFixedNotionalIndependentOfEquity(t *testing.T) {
	state := PositionState{Side: SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: 2000, FixedNotionalUSDT: DeployNotionalUSDT(BaseEquityUSDT, 0.5)}
	want := DeployNotionalUSDT(BaseEquityUSDT, 0.5)
	if NotionalUSDT(state) != want {
		t.Fatalf("notional=%v want frozen %v", NotionalUSDT(state), want)
	}
}

func TestIncreaseWeightedBasisAndNotional(t *testing.T) {
	long := PositionState{Side: SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: BaseEquityUSDT, FixedNotionalUSDT: DeployNotionalUSDT(BaseEquityUSDT, 0.5)}
	action := Action{Verb: VerbIncrease, SizeFraction: 0.25, TargetHorizon: "1h"}
	post, turnover, err := ApplyTransition(long, action, 110)
	if err != nil {
		t.Fatal(err)
	}
	wantBasis := (100*0.5 + 110*0.25) / 0.75
	if math.Abs(post.BasisPrice-wantBasis) > 1e-6 {
		t.Fatalf("basis=%v want %v", post.BasisPrice, wantBasis)
	}
	wantNotional := DeployNotionalUSDT(BaseEquityUSDT, 0.75)
	if math.Abs(post.FixedNotionalUSDT-wantNotional) > 1e-6 {
		t.Fatalf("notional=%v want %v turnover=%v", post.FixedNotionalUSDT, wantNotional, turnover)
	}
}
