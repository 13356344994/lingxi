package walkforward

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

func TestReplayExitUsesDeadlineOpen(t *testing.T) {
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]eventmap.Bar, 12)
	for i := range bars {
		bars[i].OpenTime = base.Add(time.Duration(i) * time.Hour)
		bars[i].Open = 100 + float64(i)
		bars[i].Close = bars[i].Open + 0.5
		bars[i].High = bars[i].Close + 0.1
		bars[i].Low = bars[i].Open - 0.1
		bars[i].QuoteVolume = 1e7
	}
	spec, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	horizon := spec.Horizons[0]
	decisionIndex := 5
	delay := 0
	entryIndex := decisionIndex + 1 + delay
	exitIndex := entryIndex + horizon.BarCount
	_, _, conservative, _, _, ok := computeOutcome(
		bars, decisionIndex, horizon, "long", 100,
		profitmap.CostInputs{RollingQuoteVolumeUSDT: 1e8, BarHigh: 110, BarLow: 99, BarMid: 100},
		nil, delay,
	)
	if !ok {
		t.Fatal("expected computeOutcome")
	}
	events := []WFEvent{{
		EventID: "e1", Symbol: "AAA-USDT-SWAP", Direction: "long",
		EventFamily: eventmap.FamilyExtremeContinuation,
		DecisionTime: bars[decisionIndex].OpenTime, DecisionBarIndex: decisionIndex,
		DeadlineBarIndex: exitIndex, CompleteAtBar: exitIndex,
		HorizonBarCount: horizon.BarCount, CardDelayBars: delay,
		ConservativeBPS: conservative, StressBPS: conservative * 0.5,
		ReferenceNotional: 100, MarginTimeSec: int64(horizon.Duration / time.Second),
	}}
	barsBySymbol := map[string][]profitmap.Bar{"AAA-USDT-SWAP": bars}
	sizing := FrozenSizingInput{
		ConservativeMeanBPS: conservative, StressConservativeMeanBPS: conservative * 0.5,
		ReferenceNotional: 100,
	}
	scenarios, _, _ := ReplayFoldEvents(events, barsBySymbol, nil, 100, sizing)
	if len(scenarios) == 0 {
		t.Fatal("expected replay scenarios")
	}
	baseScenario := scenarios[0]
	wantReturn := conservative / 10000
	if math.Abs(baseScenario.NetReturnFraction-wantReturn) > 0.02 {
		t.Fatalf("replay return=%v want~%v", baseScenario.NetReturnFraction, wantReturn)
	}
}
