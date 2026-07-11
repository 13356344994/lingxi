package datatruth

import (
	"testing"
	"time"
)

func TestObserveMarksDuplicateOutOfOrderGapAndLate(t *testing.T) {
	base := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	env1, err := NewEnvelope("okx", "trades", "BTC-USDT-SWAP", 1, base, base.Add(time.Second), base.Add(2*time.Second), map[string]any{"px": 100, "sz": 1})
	if err != nil {
		t.Fatal(err)
	}
	st, a1 := Observe(StreamState{}, env1, ObserveOptions{LateAfter: time.Minute})
	if !a1.Accepted || len(a1.Quality) != 1 || a1.Quality[0] != QualityOK {
		t.Fatalf("first event should be clean: %+v", a1)
	}

	st2, dup := Observe(st, env1, ObserveOptions{})
	if dup.Accepted || !dup.Duplicate {
		t.Fatalf("duplicate should be rejected: %+v", dup)
	}
	if st2.LastSequence != st.LastSequence {
		t.Fatalf("duplicate mutated last sequence: %d != %d", st2.LastSequence, st.LastSequence)
	}

	env3, err := NewEnvelope("okx", "trades", "BTC-USDT-SWAP", 3, base, base.Add(2*time.Hour), base.Add(2*time.Hour+time.Second), map[string]any{"px": 102})
	if err != nil {
		t.Fatal(err)
	}
	st, gap := Observe(st, env3, ObserveOptions{LateAfter: time.Minute})
	if !gap.Gap || gap.GapFrom != 2 || gap.GapTo != 2 {
		t.Fatalf("expected sequence gap 2..2: %+v", gap)
	}
	if !hasQuality(gap.Quality, QualityLate) {
		t.Fatalf("expected late mark: %+v", gap.Quality)
	}

	env2, err := NewEnvelope("okx", "trades", "BTC-USDT-SWAP", 2, base, base.Add(time.Second), base.Add(2*time.Second), map[string]any{"px": 101})
	if err != nil {
		t.Fatal(err)
	}
	_, oo := Observe(st, env2, ObserveOptions{})
	if !oo.OutOfOrder || !oo.Accepted {
		t.Fatalf("late-arriving unique event should be accepted but marked out-of-order: %+v", oo)
	}
}

func TestHashJSONCanonicalizesMapOrder(t *testing.T) {
	h1, _, err := HashJSON(map[string]any{"b": 2, "a": []any{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := HashJSON(map[string]any{"a": []any{"x", "y"}, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash should be stable across map order: %s != %s", h1, h2)
	}
}

func TestDecisionSchemaRequiresDecisionIDAndHashes(t *testing.T) {
	s := DecisionSchema{
		DecisionID:       "dec:BTC_001",
		DataHash:         "data",
		FeatureHash:      "feature",
		ExperimentHash:   "experiment",
		InputEventHashes: []string{"e2", "e1"},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid decision schema rejected: %v", err)
	}
	s.DecisionID = "bad id with spaces"
	if err := s.Validate(); err == nil {
		t.Fatal("expected invalid decision_id to fail")
	}
}

func hasQuality(q []QualityMark, want QualityMark) bool {
	for _, got := range q {
		if got == want {
			return true
		}
	}
	return false
}
