package walkforward

import (
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

func TestScanIncludesAllTriggeredOutcomes(t *testing.T) {
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	bars := syntheticWFMixedWFBars(base, time.Hour, 200)
	spec, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	boundaries := syntheticWFBoundaries(base, time.Duration(len(bars))*time.Hour)
	th := syntheticThresholds(spec.ID)
	card := testScanCard()
	tracker := eventmap.NewWindowTracker()
	events, err := ScanCardSymbolWalkForward(bars, "AAA-USDT-SWAP", card, ScanConfig{
		Spec: spec, Boundaries: boundaries, Thresholds: th, BTCBars: bars,
		ShockClusterAt: func(time.Time) string { return "shock-a" },
	}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected triggered events")
	}
	pos, neg := 0, 0
	for _, ev := range events {
		if ev.ConservativeBPS > 0 {
			pos++
		} else if ev.ConservativeBPS < 0 {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		t.Fatalf("must include winner and loss outcomes: pos=%d neg=%d total=%d", pos, neg, len(events))
	}
}

func TestPureNoiseWalkForwardFailsGates(t *testing.T) {
	base := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	bars := syntheticWFNoiseBars(base, time.Hour, 200)
	spec, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	boundaries := syntheticWFBoundaries(base, time.Duration(len(bars))*time.Hour)
	th := syntheticThresholds(spec.ID)
	card := testReversalScanCard()
	tracker := eventmap.NewWindowTracker()
	events, err := ScanCardSymbolWalkForward(bars, "AAA-USDT-SWAP", card, ScanConfig{
		Spec: spec, Boundaries: boundaries, Thresholds: th, BTCBars: bars,
		ShockClusterAt: func(time.Time) string { return "shock-a" },
	}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 5 {
		t.Fatalf("expected noise triggers, got %d", len(events))
	}
	barsBySymbol := map[string][]profitmap.Bar{"AAA-USDT-SWAP": bars}
	sizing := FrozenSizingInput{ConservativeMeanBPS: 5, StressConservativeMeanBPS: 1, ReferenceNotional: 100}
	scenarios, daily, _ := ReplayFoldEvents(events, barsBySymbol, nil, 100, sizing)
	if len(scenarios) == 0 {
		t.Fatal("expected replay scenarios")
	}
	folds := []FoldResult{{
		FoldIndex: 0, CapitalTier: 100, Scenarios: scenarios,
		BaseNetReturnFraction: scenarios[0].NetReturnFraction,
		NetReturnPositive:     scenarios[0].NetReturnPositive,
	}}
	status, _, _ := EvaluateWFGates(folds, nil, daily, nil)
	if status == StatusWFPass {
		t.Fatal("pure noise must not pass WF gates")
	}
}

func testScanCard() reverseevent.FrozenEventCard {
	return reverseevent.FrozenEventCard{
		EventFamily:  eventmap.FamilyExtremeContinuation,
		Horizon:      "1h",
		Direction:    "long",
		DelayBars:    0,
		CapitalTiers: []int{30},
		TriggerConditions: reverseevent.TriggerSpec{
			EventFamily: eventmap.FamilyExtremeContinuation,
			Conditions: []reverseevent.TriggerCondition{
				{Field: "abs_return_bps", Op: "gte", Value: 1},
				{Field: "rms_bps", Op: "gte", Value: 1},
				{Field: "return_sign", Op: "eq_prior_sign"},
			},
		},
	}
}

func testReversalScanCard() reverseevent.FrozenEventCard {
	return reverseevent.FrozenEventCard{
		EventFamily:  eventmap.FamilyExtremeReversal,
		Horizon:      "1h",
		Direction:    "long",
		DelayBars:    0,
		CapitalTiers: []int{30},
		TriggerConditions: reverseevent.TriggerSpec{
			EventFamily: eventmap.FamilyExtremeReversal,
			Conditions: []reverseevent.TriggerCondition{
				{Field: "abs_return_bps", Op: "gte", Value: 1},
				{Field: "rms_bps", Op: "gte", Value: 1},
				{Field: "return_sign", Op: "ne_prior_sign"},
			},
		},
	}
}

func syntheticWFMixedWFBars(base time.Time, step time.Duration, n int) []eventmap.Bar {
	bars := make([]eventmap.Bar, n)
	prevClose := 100.0
	wfStart := n * 4 / 5
	wfDown := wfStart + 18
	for i := range bars {
		bars[i].OpenTime = base.Add(time.Duration(i) * step)
		bars[i].Open = prevClose
		growth := 1.0
		switch {
		case i >= wfDown:
			growth = 0.93
		case i >= wfStart:
			growth = 1.03
		}
		bars[i].Close = prevClose * growth
		bars[i].High = bars[i].Close * 1.002
		bars[i].Low = bars[i].Open * 0.998
		bars[i].QuoteVolume = 1e7
		bars[i].Confirmed = true
		prevClose = bars[i].Close
	}
	return bars
}

func syntheticWFTrendBars(base time.Time, step time.Duration, n int, growth float64) []eventmap.Bar {
	bars := make([]eventmap.Bar, n)
	prevClose := 100.0
	for i := range bars {
		bars[i].OpenTime = base.Add(time.Duration(i) * step)
		bars[i].Open = prevClose
		bars[i].Close = prevClose * growth
		bars[i].High = bars[i].Close * 1.002
		bars[i].Low = bars[i].Open * 0.998
		bars[i].QuoteVolume = 1e7
		bars[i].Confirmed = true
		prevClose = bars[i].Close
	}
	return bars
}

func syntheticWFNoiseBars(base time.Time, step time.Duration, n int) []eventmap.Bar {
	bars := syntheticWFTrendBars(base, step, n, 1.0)
	for i := range bars {
		if i%2 == 0 {
			bars[i].Close = bars[i].Open * 1.01
		} else {
			bars[i].Close = bars[i].Open * 0.99
		}
		bars[i].High = bars[i].Close * 1.002
		bars[i].Low = bars[i].Open * 0.998
		if i > 0 {
			bars[i].Open = bars[i-1].Close
		}
	}
	return bars
}

func syntheticWFBoundaries(base time.Time, total time.Duration) eventmap.Boundaries {
	research := total * 6 / 10
	validation := total * 2 / 10
	return eventmap.Boundaries{
		Start:           base,
		DiscoveryEnd:    base.Add(research),
		ValidationStart: base.Add(research),
		ValidationEnd:   base.Add(research + validation),
		WFStart:         base.Add(research + validation),
		End:             base.Add(total),
	}
}

func syntheticThresholds(profileID string) eventmap.FrozenThresholds {
	return eventmap.FrozenThresholds{
		ProfileID: profileID, ExtremeReturnAbsBPS: 1, ExtremeRMSBPS: 1,
		LowLiquidityQuote: 1e12, VolumeSpikeQuote: 1e12, BTCRelativeAbsBPS: 1e6,
		BTCShockReturnAbsBPS: 1e6,
	}
}
