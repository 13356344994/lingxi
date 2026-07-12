package g2prize

import (
	"math"
	"math/rand"
	"testing"

	"lingxi/internal/routerg2/protocol"
)

func TestExactPermutationBoundaryUsesProtocolConstant(t *testing.T) {
	diffs := make([]float64, protocol.BackwardExactPermutationMaxPairs)
	for i := range diffs {
		diffs[i] = 1
	}
	got := pairedPermutationPValue(diffs, 1, rand.New(rand.NewSource(1)))
	want := math.Pow(0.5, float64(len(diffs)))
	if got != want {
		t.Fatalf("exact p=%v want=%v", got, want)
	}
}

func TestPermutationPlaceboRejectsNoiseMatchedToControls(t *testing.T) {
	// Winners and controls drawn from the same distribution: no true
	// differential edge, only what looks like one from a single draw.
	diffs := []float64{3, -2, 4, -5, 1, -1, 2, -3}
	rng := rand.New(rand.NewSource(11))
	p := pairedPermutationPValue(diffs, 5000, rng)
	if p < 0.2 {
		t.Fatalf("placebo-like symmetric diffs should not produce a small p-value, got %v", p)
	}
}

func TestPermutationPlaceboAcceptsConsistentDirectionalDiffs(t *testing.T) {
	diffs := []float64{10, 12, 9, 11, 13, 10, 14, 9, 11, 12}
	rng := rand.New(rand.NewSource(11))
	p := pairedPermutationPValue(diffs, 5000, rng)
	if p > 0.05 {
		t.Fatalf("a consistently positive, direction-stable diff set should produce a small p-value, got %v", p)
	}
}

func TestClusterBlockBootstrapProducesWiderCIForHeterogeneousClusters(t *testing.T) {
	// Two clusters with wildly divergent per-cluster means: resampling by
	// cluster means each draw looks like "all cluster c1" or "all cluster
	// c2" (or a mix), producing a wide spread across draws.
	heterogeneous := []pairSample{
		{Cluster: "c1", Diff: 100}, {Cluster: "c2", Diff: -100},
	}
	// Six clusters whose per-cluster means are all close together:
	// whichever clusters get resampled, the draw mean stays near the
	// shared value, producing a narrow spread.
	homogeneous := []pairSample{
		{Cluster: "c1", Diff: 10}, {Cluster: "c2", Diff: 11}, {Cluster: "c3", Diff: 9},
		{Cluster: "c4", Diff: 10}, {Cluster: "c5", Diff: 12}, {Cluster: "c6", Diff: 9},
	}
	ciWide := percentileCI(clusterBlockBootstrap(heterogeneous, 2000, 1))
	ciNarrow := percentileCI(clusterBlockBootstrap(homogeneous, 2000, 1))
	if (ciWide.High - ciWide.Low) <= (ciNarrow.High - ciNarrow.Low) {
		t.Fatalf("heterogeneous cluster means should bootstrap a wider CI than homogeneous cluster means: wide=%+v narrow=%+v", ciWide, ciNarrow)
	}
}

func TestComboSeedOffsetIsStableAndDistinctPerKey(t *testing.T) {
	a := comboSeedOffset("leaf_a")
	b := comboSeedOffset("leaf_b")
	if a == b {
		t.Fatal("distinct combination keys must draw from distinct seed offsets")
	}
	if a != comboSeedOffset("leaf_a") {
		t.Fatal("seed offset for the same key must be stable across calls")
	}
}

func TestIsFiniteRejectsNaNAndInf(t *testing.T) {
	if isFinite(nan()) {
		t.Fatal("NaN must not be finite")
	}
	if isFinite(inf()) {
		t.Fatal("+Inf must not be finite")
	}
	if !isFinite(1.5) {
		t.Fatal("1.5 must be finite")
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func inf() float64 {
	one, zero := 1.0, 0.0
	return one / zero
}
