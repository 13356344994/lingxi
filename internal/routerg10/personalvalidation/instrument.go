package personalvalidation

import (
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/exclusionmap"
)

// resolveDecisionMinNotional returns decision-visible min notional from bound entry proof
// or PIT instrument metadata. Playbook narrative is never used.
func resolveDecisionMinNotional(
	symbol string,
	decision time.Time,
	price float64,
	eventID string,
	proofs ProofBundle,
	instrumentCat datatruth.InstrumentCatalog,
	hasInstrumentCat bool,
) float64 {
	if ep, ok := proofs.entryProof(eventID); ok && ep.DynamicMinNotional > 0 {
		return ep.DynamicMinNotional
	}
	if hasInstrumentCat && price > 0 {
		if min, err := exclusionmap.PITDynamicMinNotionalUSDT(instrumentCat, symbol, decision, price); err == nil && min > 0 {
			return min
		}
	}
	return 0
}

func minNotionalForBrain(ev TriggerEvent, proofs ProofBundle) float64 {
	if ep, ok := proofs.entryProof(ev.EventID); ok && ep.DynamicMinNotional > 0 {
		return ep.DynamicMinNotional
	}
	return 0
}

func brainMinNotionalFromEvents(events []TriggerEvent, proofs ProofBundle) float64 {
	for _, ev := range events {
		if min := minNotionalForBrain(ev, proofs); min > 0 {
			return min
		}
	}
	return 0
}
