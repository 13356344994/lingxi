package walkforward

import (
	"context"
	"fmt"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	g10validation "lingxi/internal/routerg10/validation"
	"lingxi/internal/routerg10/playbook"
)

// ScanConfig bounds one symbol walk_forward stream.
type ScanConfig struct {
	Context        context.Context
	Spec           eventmap.ProfileSpec
	ProfileID      string
	Boundaries     eventmap.Boundaries
	Thresholds     eventmap.FrozenThresholds
	BTCBars        []eventmap.Bar
	ShockClusterAt func(time.Time) string
	Funding        []profitmap.FundingPoint
	MaxLines       int
	Fingerprint    g10validation.FingerprintBinding
}

// ScanPlaybookSymbolWalkForward evaluates walk_forward partition triggers for one playbook.
func ScanPlaybookSymbolWalkForward(
	bars []eventmap.Bar,
	symbol string,
	pb playbook.FrozenEventPlaybook,
	cfg ScanConfig,
	tracker *eventmap.WindowTracker,
) ([]WFEvent, error) {
	events, err := g10validation.ScanPlaybookSymbol(bars, symbol, pb, g10validation.ScanConfig{
		Context: cfg.Context, Spec: cfg.Spec, ProfileID: cfg.ProfileID,
		Boundaries: cfg.Boundaries, Thresholds: cfg.Thresholds, BTCBars: cfg.BTCBars,
		ShockClusterAt: cfg.ShockClusterAt, Funding: cfg.Funding, MaxLines: cfg.MaxLines,
		Fingerprint: cfg.Fingerprint.Fingerprint,
	}, tracker)
	if err != nil {
		return nil, err
	}
	out := make([]WFEvent, len(events))
	for i, ev := range events {
		out[i] = WFEvent(ev)
	}
	return out, nil
}

// ReplayFoldEvents replays one fold's events for one capital tier across delay/cost scenarios.
func ReplayFoldEvents(
	events []WFEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	sizingInput FrozenSizingInput,
	pb playbook.FrozenEventPlaybook,
	fp g10validation.FingerprintBinding,
) ([]TierScenarioResult, DailyEquityReplay, ReplayStats) {
	replayEvents := make([]g10validation.ReplayEvent, len(events))
	for i, ev := range events {
		replayEvents[i] = g10validation.ReplayEvent(ev)
	}
	scenarios, daily, stats, _ := g10validation.ReplayTierEvents(
		replayEvents, barsBySymbol, fundingBySymbol, initialEquity, sizingInput, pb, fp.Fingerprint,
	)
	return scenarios, daily, stats
}

// PurgeEmbargoDuration returns max(profile lookback, event deadline) for one playbook.
func PurgeEmbargoDuration(spec eventmap.ProfileSpec, pb playbook.FrozenEventPlaybook) time.Duration {
	lookback := spec.MaxFeatureLookback
	deadline := spec.BarDuration
	if horizon, ok := horizonFor(spec, pb.Horizon); ok {
		deadline = horizon.Duration
	}
	if lookback > deadline {
		return lookback
	}
	return deadline
}

func horizonFor(spec eventmap.ProfileSpec, label string) (profitmap.HorizonSpec, bool) {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h, true
		}
	}
	return profitmap.HorizonSpec{}, false
}

// RejectNonWalkForwardPartition rejects rows outside walk_forward partition.
func RejectNonWalkForwardPartition(partition string) error {
	if partition != eventmap.PartitionWalkFwd {
		return fmt.Errorf("walkforward: partition leak %q", partition)
	}
	return nil
}
