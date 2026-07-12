package walkforward

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
)

func TestFutureLossesStillTradeWithFrozenEdge(t *testing.T) {
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
	exitIndex := decisionIndex + 1 + horizon.BarCount
	_, _, winnerConservative, _, _, ok := computeOutcome(
		bars, decisionIndex, horizon, "long", 100,
		profitmap.CostInputs{RollingQuoteVolumeUSDT: 1e8, BarHigh: 110, BarLow: 99, BarMid: 100},
		nil, 0,
	)
	if !ok {
		t.Fatal("expected winner outcome")
	}
	events := []WFEvent{
		{
			EventID: "win", Symbol: "AAA-USDT-SWAP", Direction: "long",
			EventFamily: eventmap.FamilyExtremeContinuation,
			DecisionTime: bars[decisionIndex].OpenTime, DecisionBarIndex: decisionIndex,
			DeadlineBarIndex: exitIndex, CompleteAtBar: exitIndex,
			HorizonBarCount: horizon.BarCount, CardDelayBars: 0,
			ConservativeBPS: winnerConservative, StressBPS: winnerConservative * 0.5,
			ReferenceNotional: 100,
		},
		{
			EventID: "loss", Symbol: "AAA-USDT-SWAP", Direction: "long",
			EventFamily: eventmap.FamilyExtremeContinuation,
			DecisionTime: bars[decisionIndex+1].OpenTime, DecisionBarIndex: decisionIndex + 1,
			DeadlineBarIndex: exitIndex + 1, CompleteAtBar: exitIndex + 1,
			HorizonBarCount: horizon.BarCount, CardDelayBars: 0,
			ConservativeBPS: -20, StressBPS: 10,
			ReferenceNotional: 100,
		},
	}
	sizing := FrozenSizingFromSurvivor(eventvalidation.SurvivorEntry{
		FrozenConservativeMeanBPS:       5,
		FrozenStressConservativeMeanBPS: 1,
		TierResults: []eventvalidation.TierResult{{
			CapitalTier: 100,
			Firing: eventvalidation.FiringStats{
				ConservativeMeanBPS:       5,
				StressConservativeMeanBPS: 1,
			},
		}},
	}, 100)
	if sizing.ConservativeMeanBPS <= 0 {
		t.Fatal("frozen sizing edge must stay positive")
	}
	barsBySymbol := map[string][]profitmap.Bar{"AAA-USDT-SWAP": bars}
	scenarios, _, stats := ReplayFoldEvents(events, barsBySymbol, nil, 100, sizing)
	if stats.EventsTraded == 0 {
		t.Fatal("future-loss triggers must still trade when frozen survivor edge is positive")
	}
	if len(scenarios) == 0 || !scenarios[0].NetReturnPositive {
		t.Fatalf("realized mixed replay should reflect actual triggers, traded=%d", stats.EventsTraded)
	}
}

func TestFrozenSizingUsesSurvivorNotPerEventBPS(t *testing.T) {
	entry := eventvalidation.SurvivorEntry{
		FrozenConservativeMeanBPS:       8,
		FrozenStressConservativeMeanBPS: 2,
	}
	ev := WFEvent{ConservativeBPS: -50, StressBPS: 50, ReferenceNotional: 30, HorizonBarCount: 1, CardDelayBars: 0}
	candidate := wfEventToCandidate(ev, 0, FrozenSizingFromSurvivor(entry, 30))
	if candidate.ConservativeEdgeUSDT <= 0 {
		t.Fatal("queue sizing must not use per-event realized conservative bps")
	}
	want := 8.0 * 30.0 / 10000.0
	if math.Abs(candidate.ConservativeEdgeUSDT-want) > 1e-9 {
		t.Fatalf("edge usdt=%v want %v from survivor", candidate.ConservativeEdgeUSDT, want)
	}
}

func TestFrozenSizingFloorWhenSurvivorMissing(t *testing.T) {
	sizing := FrozenSizingFromSurvivor(eventvalidation.SurvivorEntry{}, 30)
	if sizing.ConservativeMeanBPS != protocol.WFSizingEdgeFloorBPS {
		t.Fatalf("floor bps=%v want %v", sizing.ConservativeMeanBPS, protocol.WFSizingEdgeFloorBPS)
	}
}
