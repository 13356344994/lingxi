package validation

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

func TestDelayPlusOneBarDiffersFromNormal(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := makeTrendBars(base, 20, 1.0)
	// Make entry bars differ so delay+1bar changes gross.
	bars[6].Open = 100
	bars[6].Close = 100
	bars[7].Open = 110
	bars[7].Close = 110
	bars[8].Open = 105
	bars[8].Close = 105
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	horizon := spec.Horizons[0]
	costIn := profitmap.CostInputs{
		RollingQuoteVolumeUSDT: 1e7,
		BarHigh:                111, BarLow: 99, BarMid: 100,
	}
	normal, ok := computeOutcome(bars, 5, horizon, profitmap.DirectionLong, 30, costIn, nil, 0)
	if !ok {
		t.Fatal("normal outcome missing")
	}
	delayed, ok := computeOutcome(bars, 5, horizon, profitmap.DirectionLong, 30, costIn, nil, 1)
	if !ok {
		t.Fatal("delayed outcome missing")
	}
	if math.Abs(normal.ConservativeBPS-delayed.ConservativeBPS) < 1e-9 {
		t.Fatalf("delay+1bar must change conservative net: normal=%v delayed=%v", normal.ConservativeBPS, delayed.ConservativeBPS)
	}
}

func TestCostStressMultiplierApplied(t *testing.T) {
	gross := 20.0
	feeNet := gross - protocol.RoundtripTakerFeeBPS
	conservative := gross - protocol.RoundtripTakerFeeBPS - protocol.BaseSpreadSlippageFloorBPS - 5
	stress := profitmap.StressConservativeNet(gross, feeNet, conservative)
	if stress >= conservative {
		t.Fatalf("stress must reduce conservative net: cons=%v stress=%v", conservative, stress)
	}
}

func TestStreamValidationPartitionOnly(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := makeTrendBars(base, 800, 1.001)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	boundaries, err := profitmap.ComputeBoundaries(spec, bars)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	err = StreamValidationSymbol(bars, "TEST", ScanConfig{
		Spec: spec, Boundaries: boundaries,
		SkyCutpoints: frozenTestCutpoints(), SkySeries: sky.Series{},
		SymbolClass:      profitmap.SymbolClassCutpoints{Frozen: true, Upper: 1e12},
		SymbolClassLabel: profitmap.SymbolClassMajor,
	}, func(ev EventContext) error {
		part := boundaries.PartitionAt(ev.DecisionTime)
		counts[part]++
		if part != profitmap.PartitionValidation {
			t.Fatalf("stream emitted non-validation partition %q", part)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts[profitmap.PartitionValidation] == 0 {
		t.Fatal("expected validation opportunities")
	}
	if counts[profitmap.PartitionDiscovery] != 0 {
		t.Fatal("discovery partition must not be emitted")
	}
}

func TestStreamValidationSymbolCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := StreamValidationSymbol(
		makeTrendBars(time.Now().UTC(), 800, 1.001),
		"TEST",
		ScanConfig{Context: ctx},
		func(EventContext) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func makeTrendBars(base time.Time, n int, factor float64) []profitmap.Bar {
	var bars []profitmap.Bar
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		bars = append(bars, profitmap.Bar{
			OpenTime: open, Open: price, High: price * 1.01, Low: price * 0.99,
			Close: price * factor, QuoteVolume: 5e6, Confirmed: true,
		})
		price *= factor
	}
	return bars
}

func frozenTestCutpoints() sky.Cutpoints {
	cuts := map[string][]float64{}
	for _, dim := range sky.Dimensions {
		cuts[dim] = []float64{-1, 1}
	}
	return sky.Cutpoints{
		Partition: sky.FitPartition, Quantiles: []float64{0.33, 0.67},
		Cuts: cuts, FitSamples: 10, Frozen: true,
	}
}
