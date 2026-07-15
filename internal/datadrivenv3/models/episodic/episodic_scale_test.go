// Production-scale bounding tests: 100k-row scalability, hash-priority
// selection determinism under input reordering, and candidate-pool /
// LOO-query boundary conditions around MaxCandidatePoolSize and
// MaxLOOQueries. Every fixture here is a pure function of a loop index —
// no dataset shard, reward signal, or peer-owner output is ever read.
package episodic

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

// bigFixture deterministically builds n synthetic training examples spread
// 40 days apart in decision time (matching the spacing convention used by
// the other synthetic fixtures in this package), far enough apart that a
// probe query placed well before the entire range is 24h-embargo-free
// against every one of them.
func bigFixture(n int) []modelapi.TrainingExample {
	base := int64(1_000_000_000_000)
	examples := make([]modelapi.TrainingExample, 0, n)
	for i := 0; i < n; i++ {
		x := float64(i%5) - 2
		lev := float64((i / 5) % 3)
		target := flat()
		switch i % 3 {
		case 0:
			target = long()
		case 1:
			target = short()
		}
		examples = append(examples, example(
			fmt.Sprintf("scale-%06d", i),
			market(fmt.Sprintf("scale-event-%06d", i), base+int64(i)*40*msPerDay, x, i%11 == 0),
			account(lev),
			target,
		))
	}
	return examples
}

func shuffledCopy(examples []modelapi.TrainingExample, seed int64) []modelapi.TrainingExample {
	out := append([]modelapi.TrainingExample(nil), examples...)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// --- 100k-row scalability ----------------------------------------------------

// TestSyntheticScalability100kBoundedPool fits over 100,000 synthetic
// examples — far beyond MaxCandidatePoolSize and MaxLOOQueries — and checks
// that the frozen candidate pool is capped at exactly
// MaxCandidatePoolSize, never at the input size.
func TestSyntheticScalability100kBoundedPool(t *testing.T) {
	examples := bigFixture(100_000)
	m := New()

	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatalf("fit over 100000 synthetic examples failed: %v", err)
	}
	if m.LabelsRead() {
		t.Fatal("labels_read must stay false")
	}

	// Probe decision time is far before the entire fixture range: no pool
	// row can fall inside the 24h embargo window, so ConsideredCount below
	// reflects only the frozen pool cap, not embargoing.
	probe := market("scale-probe", int64(500_000_000_000), 0, false)
	_, evidence, err := m.PredictWithEvidence(context.Background(), probe, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.EmbargoedCount != 0 {
		t.Fatalf("expected zero embargoed rows for the far-past probe, got %d", evidence.EmbargoedCount)
	}
	if evidence.ConsideredCount != MaxCandidatePoolSize {
		t.Fatalf("considered = %d, want exactly the frozen pool cap %d despite 100000 input examples", evidence.ConsideredCount, MaxCandidatePoolSize)
	}
	if evidence.NeighborCount != m.SelectedK() {
		t.Fatalf("neighbor count = %d, want selected k = %d", evidence.NeighborCount, m.SelectedK())
	}
	k := m.SelectedK()
	if k != 16 && k != 32 && k != 64 {
		t.Fatalf("selected k %d not on frozen grid", k)
	}
	fs := m.SelectedFeatureSet()
	if fs != string(FeatureSetRequiredOnly) && fs != string(FeatureSetRequiredPlusOptional) {
		t.Fatalf("selected feature set %q not on frozen grid", fs)
	}
}

// --- selection determinism under input reordering ----------------------------

// TestSyntheticSelectionOrderIndependenceAtScale freezes the same 20,000-row
// example set (well above MaxCandidatePoolSize, so real truncation occurs)
// under three different input orderings and checks that candidate-pool
// membership and order are bit-for-bit identical. It also checks that Fit's
// canonical input identity is independent of input order.
func TestSyntheticSelectionOrderIndependenceAtScale(t *testing.T) {
	canonical := bigFixture(20_000)
	orderings := [][]modelapi.TrainingExample{
		canonical,
		shuffledCopy(canonical, 7),
		shuffledCopy(canonical, 999),
		reversedCopy(canonical),
	}

	wantPool := selectPool(buildRows(canonical), MaxCandidatePoolSize)
	wantHash := examplesHash(canonical)
	for i, ordering := range orderings {
		gotPool := selectPool(buildRows(ordering), MaxCandidatePoolSize)
		if !reflect.DeepEqual(gotPool, wantPool) {
			t.Fatalf("ordering %d: frozen candidate pool depends on input example order", i)
		}
		if gotHash := examplesHash(ordering); gotHash != wantHash {
			t.Fatalf("ordering %d: canonical input hash depends on input example order: %q vs %q", i, gotHash, wantHash)
		}
	}
}

func reversedCopy(examples []modelapi.TrainingExample) []modelapi.TrainingExample {
	out := make([]modelapi.TrainingExample, len(examples))
	for i, e := range examples {
		out[len(examples)-1-i] = e
	}
	return out
}

// --- candidate pool / LOO query boundary conditions --------------------------

// TestSyntheticCandidatePoolBoundary directly exercises selectPool (the
// white-box selection primitive shared by pool freezing and LOO-query
// selection) at n one below, at, and one above MaxCandidatePoolSize.
func TestSyntheticCandidatePoolBoundary(t *testing.T) {
	testSelectPoolBoundary(t, MaxCandidatePoolSize)
}

// TestSyntheticLOOQueryBoundary exercises the same primitive at the
// MaxLOOQueries boundary used to bound Fit's embargoed LOO pseudo-query set.
func TestSyntheticLOOQueryBoundary(t *testing.T) {
	testSelectPoolBoundary(t, MaxLOOQueries)
}

func testSelectPoolBoundary(t *testing.T, limit int) {
	for _, n := range []int{limit - 1, limit, limit + 1} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			examples := bigFixture(n)
			rows := buildRows(examples)
			pool := selectPool(rows, limit)

			want := n
			if want > limit {
				want = limit
			}
			if len(pool) != want {
				t.Fatalf("pool size = %d, want %d", len(pool), want)
			}

			// Order independence: shuffling the input rows must not change
			// which rows are selected, nor their resulting order.
			shuffled := append([]row(nil), rows...)
			r := rand.New(rand.NewSource(int64(n) + 1))
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			poolFromShuffled := selectPool(shuffled, limit)
			if !reflect.DeepEqual(pool, poolFromShuffled) {
				t.Fatalf("pool selection depends on input row order")
			}

			// Pool must be exactly the lowest-hash-priority rows: nothing
			// excluded may have a strictly lower priority than something
			// included.
			if n > limit {
				included := make(map[string]bool, len(pool))
				maxIncludedPriority := ""
				for _, r := range pool {
					included[r.eventID] = true
					if r.priority > maxIncludedPriority {
						maxIncludedPriority = r.priority
					}
				}
				for _, r := range rows {
					if !included[r.eventID] && r.priority < maxIncludedPriority {
						t.Fatalf("excluded row has strictly lower priority than an included row")
					}
				}
			}
		})
	}
}

// TestSyntheticSelectPoolEdgeCases covers degenerate inputs: an empty row
// set, a single row, and a zero limit.
func TestSyntheticSelectPoolEdgeCases(t *testing.T) {
	if got := selectPool(nil, MaxCandidatePoolSize); len(got) != 0 {
		t.Fatalf("selectPool(nil) = %d rows, want 0", len(got))
	}
	rows := buildRows(bigFixture(1))
	if got := selectPool(rows, MaxCandidatePoolSize); len(got) != 1 {
		t.Fatalf("selectPool(single row) = %d rows, want 1", len(got))
	}
	if got := selectPool(rows, 0); len(got) != 0 {
		t.Fatalf("selectPool with limit=0 = %d rows, want 0", len(got))
	}
}

// --- unchanged-contract guard --------------------------------------------------

// TestSyntheticPoolCapsMatchSpecAndLeaveGridUnchanged pins the two new
// bounding constants to their requested values and confirms the frozen k
// grid, feature-set grid, attempt budget, and 24h embargo window were left
// untouched by this change.
func TestSyntheticPoolCapsMatchSpecAndLeaveGridUnchanged(t *testing.T) {
	if MaxCandidatePoolSize != 8192 {
		t.Fatalf("MaxCandidatePoolSize = %d, want 8192", MaxCandidatePoolSize)
	}
	if MaxLOOQueries != 1024 {
		t.Fatalf("MaxLOOQueries = %d, want 1024", MaxLOOQueries)
	}
	if len(KGrid) != 3 || KGrid[0] != 16 || KGrid[1] != 32 || KGrid[2] != 64 {
		t.Fatalf("k grid changed: %v", KGrid)
	}
	if len(FeatureSets) != 2 || FeatureSets[0] != FeatureSetRequiredOnly || FeatureSets[1] != FeatureSetRequiredPlusOptional {
		t.Fatalf("feature set grid changed: %v", FeatureSets)
	}
	if MaxAttempts != 16 {
		t.Fatalf("attempt budget changed: %d", MaxAttempts)
	}
	if EmbargoWindowMS != 24*60*60*1000 {
		t.Fatalf("embargo window changed: %d", EmbargoWindowMS)
	}
}

func TestSyntheticManifestBindings(t *testing.T) {
	m := New()
	if _, err := m.Fit(context.Background(), mixedFixtures()); err != nil {
		t.Fatal(err)
	}
	t.Logf("episodic_owner_code_hash=%s", CodeHash())
	t.Logf("model_hash=%s", m.ModelHash())
	t.Logf("attempt_ledger_head_hash=%s", m.LedgerHead())
}
