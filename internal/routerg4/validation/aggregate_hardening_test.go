package validation

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestFiringAccumulatorMemoryBounded(t *testing.T) {
	acc := newFiringAccumulator()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 100_000; i++ {
		sample := FiringSample{
			Symbol:          fmt.Sprintf("SYM-%d", i%7),
			DecisionTime:    base.Add(time.Duration(i) * time.Minute),
			Day:             base.Add(time.Duration(i) * time.Minute).Format("2006-01-02"),
			ConservativeBPS: float64(i%101 - 50),
		}
		acc.add(sample, fmt.Sprintf("sample-%d", i))
	}
	if len(acc.reservoir) != maxConservativeReservoir {
		t.Fatalf("reservoir=%d, want %d", len(acc.reservoir), maxConservativeReservoir)
	}
	if acc.count != 100_000 {
		t.Fatalf("count=%d", acc.count)
	}
	if len(acc.symbols) != 7 {
		t.Fatalf("symbols=%d", len(acc.symbols))
	}
}

func TestHashPriorityQuantileDeterministic(t *testing.T) {
	build := func(reverse bool) *firingAccumulator {
		acc := newFiringAccumulator()
		for n := 0; n < 10_000; n++ {
			i := n
			if reverse {
				i = 9_999 - n
			}
			acc.add(FiringSample{
				Symbol:          "S",
				DecisionTime:    time.Unix(int64(i), 0).UTC(),
				Day:             "2024-01-01",
				ConservativeBPS: float64(i % 997),
			}, fmt.Sprintf("id-%d", i))
		}
		return acc
	}
	a := build(false)
	b := build(true)
	as, bs := a.finalize(), b.finalize()
	if as.ConservativeP10BPS != bs.ConservativeP10BPS {
		t.Fatalf("p10 drift: %v vs %v", as.ConservativeP10BPS, bs.ConservativeP10BPS)
	}
	if len(a.reservoir) != len(b.reservoir) {
		t.Fatal("reservoir size drift")
	}
	byPriority := func(acc *firingAccumulator) map[string]float64 {
		out := map[string]float64{}
		for _, sample := range acc.reservoir {
			out[sample.Priority] = sample.Value
		}
		return out
	}
	am, bm := byPriority(a), byPriority(b)
	for priority, value := range am {
		if bm[priority] != value {
			t.Fatalf("reservoir selection drift at %s", priority)
		}
	}
}

func TestCheckpointRestoresReservoirExactly(t *testing.T) {
	acc := newFiringAccumulator()
	for i := 0; i < 2_000; i++ {
		acc.add(FiringSample{
			Symbol:          fmt.Sprintf("S%d", i%3),
			DecisionTime:    time.Unix(int64(i), 0).UTC(),
			Day:             fmt.Sprintf("day-%d", i%11),
			GrossBPS:        float64(i),
			FeeNetBPS:       float64(i - 1),
			ConservativeBPS: float64(i%100 - 40),
		}, fmt.Sprintf("id-%d", i))
	}
	before := acc.finalize()
	snapshot := exportCheckpointCandidateStats([]*firingAccumulator{acc})
	restored := restoreAccumulatorsFromCheckpoint(snapshot)[0]
	after := restored.finalize()
	if before != after {
		t.Fatalf("stats changed after checkpoint restore:\nbefore=%+v\nafter=%+v", before, after)
	}
	if len(restored.reservoir) > maxConservativeReservoir {
		t.Fatalf("restored reservoir unbounded: %d", len(restored.reservoir))
	}
	if math.Abs(restored.positiveSum-acc.positiveSum) > 1e-12 ||
		restored.maxPositive != acc.maxPositive {
		t.Fatal("positive contribution scalars changed after restore")
	}
}
