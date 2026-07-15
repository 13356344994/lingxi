package pathvalidation

import "testing"

func TestBenjaminiHochbergRejectsOnlyTheSmallPValues(t *testing.T) {
	p := []float64{0.001, 0.01, 0.02, 0.5, 0.9}
	reject := BenjaminiHochberg(p, 0.10)
	if !reject[0] || !reject[1] {
		t.Fatalf("expected the smallest p-values to be rejected: %+v", reject)
	}
	if reject[3] || reject[4] {
		t.Fatalf("expected large p-values to survive: %+v", reject)
	}
}

func TestHolmBonferroniIsStricterThanBH(t *testing.T) {
	p := []float64{0.001, 0.01, 0.02, 0.5, 0.9}
	bh := BenjaminiHochberg(p, 0.10)
	holm := HolmBonferroni(p, 0.10)
	bhCount, holmCount := 0, 0
	for i := range p {
		if bh[i] {
			bhCount++
		}
		if holm[i] {
			holmCount++
		}
	}
	if holmCount > bhCount {
		t.Fatalf("Holm rejected more than BH at the same level: holm=%d bh=%d", holmCount, bhCount)
	}
}

func TestOneSidedSignTestPValueMonotonic(t *testing.T) {
	low := oneSidedSignTestPValue(6, 10)
	high := oneSidedSignTestPValue(9, 10)
	if !(high < low) {
		t.Fatalf("expected a higher positive-row proportion to produce a smaller p-value: low=%.6f high=%.6f", low, high)
	}
}

func TestIsInactivityFailureDetectsDormantConfirmedPath(t *testing.T) {
	if !IsInactivityFailure(2, 10000) {
		t.Fatal("expected a path matching only 2 of 10000 leaf_wf-eligible rows to be flagged inactive")
	}
	if IsInactivityFailure(500, 10000) {
		t.Fatal("did not expect an actively-firing path to be flagged inactive")
	}
	if IsInactivityFailure(2, 10) {
		t.Fatal("did not expect inactivity to fire when leaf_wf itself has too little eligible volume to judge")
	}
}

// oneSymbolPerFold deterministically finds one distinct instrument name that
// hashes into each of the 10 symbol-hash folds, so fold-gate tests never
// depend on incidental hash collisions among a small fixed name list.
func oneSymbolPerFold(t *testing.T) [SymbolHashFolds]string {
	t.Helper()
	var out [SymbolHashFolds]string
	found := 0
	for i := 0; found < SymbolHashFolds && i < 100000; i++ {
		name := "SYNTH-" + itoa(i) + "-USDT-SWAP"
		fold := symbolFold(name)
		if out[fold] == "" {
			out[fold] = name
			found++
		}
	}
	if found != SymbolHashFolds {
		t.Fatal("could not find a representative instrument for every symbol-hash fold")
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func TestEvaluateGatesToleratesASingleBadFold(t *testing.T) {
	// 9 of the 10 symbol-hash folds are net positive, 1 is strongly negative:
	// the frozen gate (>=8/10 positive) must still pass, matching the "never
	// eliminate on a single large drawdown fold alone" requirement.
	symbols := oneSymbolPerFold(t)
	rows := make([]PathRow, 0, 200)
	for fold := 0; fold < SymbolHashFolds; fold++ {
		reward, stress := 50.0, 50.0
		if fold == 0 {
			reward, stress = -20.0, -20.0
		}
		for d := 0; d < 20; d++ {
			rows = append(rows, PathRow{
				Instrument: symbols[fold], DecisionMS: int64(d) * dayMS(),
				NetRewardBase: reward, NetRewardStress: stress,
				NetRewardDelay: [4]float64{reward, reward, reward, reward}, DelayOK: [4]bool{true, true, true, true},
			})
		}
	}
	report := EvaluateGates(rows, map[int64]int{})
	if !report.MeetsSymbolFolds {
		t.Fatalf("9/10 positive folds must clear the >=8/10 gate: %+v", report)
	}
	if !report.MeetsBaseNetPositive {
		t.Fatal("expected the aggregate to remain net positive despite one bad fold")
	}
}

func dayMS() int64 { return 24 * hourMS }
