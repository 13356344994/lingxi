package accountstate

import (
	"math"
	"reflect"
	"testing"
)

func fixtureConfig() Config {
	return Config{InitialCashUSDT: 100, RetainedCapitalBaseUSDT: 100, NotionalPerFractionUSDT: 100, EntryCostBPS: 10, ExitCostBPS: 10, MaintenanceMarginKnown: true, MaintenanceMarginRate: .01, FrozenCloseCostBPS: 10}
}
func target(fraction float64, delay int) *TargetIntent {
	return &TargetIntent{TargetSignedPositionFraction: fraction, TargetHorizonHours: 1, DelayBars: delay}
}
func step(t *testing.T, s *State, price, funding float64, intent *TargetIntent) TransitionTrace {
	t.Helper()
	trace, err := s.Step(Tick{DecisionMS: int64(s.bar + 1), MarkPrice: price, FundingRate: funding, Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	return trace
}
func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFixtureFlatLongShortAndHold(t *testing.T) {
	for _, fraction := range []float64{.5, -.5} {
		s, _ := New(fixtureConfig())
		trace := step(t, s, 100, 0, target(fraction, 0))
		want := OpenLong
		if fraction < 0 {
			want = OpenShort
		}
		if trace.DerivedVerb != want || s.position.Direction == Flat {
			t.Fatalf("open trace=%+v state=%+v", trace, s.position)
		}
		trace = step(t, s, 101, 0, target(fraction, 0))
		if trace.DerivedVerb != Hold || !almost(s.position.WeightedAverageEntryPrice, 100) {
			t.Fatalf("hold trace=%+v state=%+v", trace, s.position)
		}
	}
	s, _ := New(fixtureConfig())
	trace := step(t, s, 100, 0, target(0, 0))
	if trace.DerivedVerb != NoTrade {
		t.Fatalf("flat no-trade=%+v", trace)
	}
}

func TestFixtureWeightedEntryPartialRealizationAndClose(t *testing.T) {
	s, _ := New(fixtureConfig())
	step(t, s, 100, 0, target(.5, 0))
	trace := step(t, s, 120, 0, target(1, 0))
	if trace.DerivedVerb != Add || !almost(s.position.WeightedAverageEntryPrice, 110) || !almost(s.position.FixedNotionalUSDT, 100) {
		t.Fatalf("weighted add trace=%+v position=%+v", trace, s.position)
	}
	cashBeforeTrim := s.cash
	trace = step(t, s, 130, 0, target(.5, 0))
	if trace.DerivedVerb != Trim || !almost(s.position.WeightedAverageEntryPrice, 110) || !almost(s.position.FixedNotionalUSDT, 50) || s.cash <= cashBeforeTrim {
		t.Fatalf("partial realization trace=%+v position=%+v cash=%v", trace, s.position, s.cash)
	}
	trace = step(t, s, 130, 0, target(0, 0))
	if trace.DerivedVerb != Close || s.position.Direction != Flat {
		t.Fatalf("close trace=%+v position=%+v", trace, s.position)
	}
}

func TestFixtureReverseEqualsCloseThenOppositeOpen(t *testing.T) {
	reversed, _ := New(fixtureConfig())
	step(t, reversed, 100, 0, target(.5, 0))
	trace := step(t, reversed, 110, 0, target(-.5, 0))
	if trace.DerivedVerb != Reverse || reversed.position.Direction != Short {
		t.Fatalf("reverse trace=%+v pos=%+v", trace, reversed.position)
	}

	sequential, _ := New(fixtureConfig())
	step(t, sequential, 100, 0, target(.5, 0))
	step(t, sequential, 110, 0, target(0, 0))
	step(t, sequential, 110, 0, target(-.5, 0))
	if !almost(reversed.cash, sequential.cash) || reversed.position.Direction != sequential.position.Direction ||
		!almost(reversed.position.FixedNotionalUSDT, sequential.position.FixedNotionalUSDT) ||
		!almost(reversed.position.WeightedAverageEntryPrice, sequential.position.WeightedAverageEntryPrice) {
		t.Fatalf("reverse=%s/%+v sequential=%s/%+v", reversed, reversed.position, sequential, sequential.position)
	}
}

func TestFixtureFundingMirrorPendingAndEOF(t *testing.T) {
	long, _ := New(fixtureConfig())
	short, _ := New(fixtureConfig())
	step(t, long, 100, 0, target(.5, 0))
	step(t, short, 100, 0, target(-.5, 0))
	step(t, long, 100, .01, target(.5, 0))
	step(t, short, 100, .01, target(-.5, 0))
	if !almost(long.position.CumulativeSignedFundingUSDT, -short.position.CumulativeSignedFundingUSDT) || long.cash >= short.cash {
		t.Fatalf("funding long=%+v short=%+v", long.position, short.position)
	}
	s, _ := New(fixtureConfig())
	step(t, s, 100, 0, target(.5, 1))
	step(t, s, 110, 0, nil)
	if s.position.Direction != Long || !almost(s.position.WeightedAverageEntryPrice, 110) {
		t.Fatalf("pending maturity=%+v", s.position)
	}
	closing, _ := New(fixtureConfig())
	step(t, closing, 100, 0, target(.5, 0))
	step(t, closing, 100, 0, target(0, 1))
	cashBeforeMaturity := closing.cash
	step(t, closing, 110, 0, nil)
	if closing.position.Direction != Flat || closing.cash <= cashBeforeMaturity {
		t.Fatalf("pending close did not mark then execute: cash=%v pos=%+v", closing.cash, closing.position)
	}
	pending, _ := New(fixtureConfig())
	step(t, pending, 100, 0, target(.5, 3))
	discarded, err := pending.FinishEOF(100)
	if err != nil || len(discarded) != 1 || len(pending.pending) != 0 {
		t.Fatalf("EOF pending=%+v err=%v", discarded, err)
	}
}

func TestFixtureWithdrawalReserveHardGatesAndNoNegativeCash(t *testing.T) {
	s, _ := New(fixtureConfig())
	s.cash = 200
	step(t, s, 100, 0, target(0, 0))
	if !almost(s.withdrawals, 100) || !almost(s.cash, 100) {
		t.Fatalf("multi-tranche withdraw=%v cash=%v", s.withdrawals, s.cash)
	}

	unknown := fixtureConfig()
	unknown.MaintenanceMarginKnown = false
	s, _ = New(unknown)
	step(t, s, 100, 0, target(.5, 0))
	s.cash = 200
	trace := step(t, s, 100, 0, target(.5, 0))
	if s.withdrawals != 0 || !contains(trace.Events, "WITHDRAWAL_DEFERRED_MMR_UNKNOWN") {
		t.Fatalf("unknown MMR trace=%+v", trace)
	}

	s, _ = New(fixtureConfig())
	s.cash = 10
	trace = step(t, s, 100, 0, target(1, 0))
	if s.position.Direction != Flat || !contains(trace.Events, "OPEN_REJECTED_HARD_GATE") || s.cash < 0 {
		t.Fatalf("5x/no-negative trace=%+v state=%s", trace, s)
	}
}

func TestContextHashCanonicalSamplingAndSharedBaseline(t *testing.T) {
	s, _ := New(fixtureConfig())
	ctx, err := s.Context(100)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := HashAccountContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := HashAccountContext(ctx)
	if h1 != h2 || ctx.DecisionTimeUnrealizedReturn != 0 {
		t.Fatalf("context hash=%s/%s ctx=%+v", h1, h2, ctx)
	}
	if _, err := HashPosition(s.position); err != nil {
		t.Fatal(err)
	}
	if _, err := HashPending(Pending{Intent: *target(.5, 1), MaturesAtBar: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTargetIntent(*target(.5, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := HashDerivedVerb(Add); err != nil {
		t.Fatal(err)
	}
	first, err := CanonicalDiscoveryContexts("event-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := CanonicalDiscoveryContexts("event-a", 7)
	if !reflect.DeepEqual(first, second) || len(first) != 7 {
		t.Fatalf("canonical sampling unstable")
	}
	for _, c := range first {
		if c.Context.SchemaVersion != SchemaVersion || c.Context.DecisionTimeUnrealizedReturn != 0 {
			t.Fatalf("non-neutral canonical context=%+v", c)
		}
	}
	policy := func(AccountContext) (TargetIntent, error) { return *target(0, 0), nil }
	traces, err := RunBaseline(fixtureConfig(), []Tick{{MarkPrice: 100}}, policy)
	if err != nil || len(traces) != 1 || traces[0].TransitionHash == "" {
		t.Fatalf("shared runner traces=%+v err=%v", traces, err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
