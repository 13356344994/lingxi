// Package counterfactual compares the frozen eight Brain actions for one event
// using personal conservative net, stress, margin time, delay, min lot, and capacity.
package counterfactual

import g10protocol "lingxi/internal/routerg10/protocol"

const (
	SchemaVersionTable    = "lingxi.routerg10.counterfactual.table.v1"
	SchemaVersionDecision = "lingxi.routerg10.counterfactual.decision.v1"
	SchemaVersionFixture  = "lingxi.routerg10.counterfactual.fixture.v1"

	Generation = g10protocol.Generation

	RejectBreakevenFail      = "OPEN_BREAKEVEN_FAIL"
	RejectStressInsolvent    = "STRESS_INSOLVENT"
	RejectOOD                = "DATA_OOD"
	RejectFingerprintUnknown = "FINGERPRINT_UNKNOWN"
	RejectNearZeroEdge       = "NEAR_ZERO_EDGE_MICRO_ONLY"
	RejectMinLotInfeasible   = "MIN_LOT_INFEASIBLE"
	RejectNoMatchingEvent    = "NO_MATCHING_EVENT"
	RejectMemoryVeto         = "MEMORY_NEGATIVE_MAJORITY_VETO"
	RejectInsufficientSample = "MEMORY_INSUFFICIENT_SAMPLE"
	RejectBelowMinNotional   = "BELOW_MIN_NOTIONAL"
	RejectConcurrencyFull    = "CONCURRENCY_FULL"

	NearZeroEdgeThresholdBPS = g10protocol.NearZeroEdgeThresholdBPS
	CostStressMultiplier     = g10protocol.CostStressMultiplier
)

// ActionsCompared mirrors protocol.CounterfactualActions.
var ActionsCompared = append([]string(nil), g10protocol.CounterfactualActions...)

// DelayBars mirrors protocol.DelayBars.
var DelayBars = append([]int(nil), g10protocol.DelayBars...)

// NoEventAllowed mirrors protocol.BrainNoMatchingEventAllowed.
var NoEventAllowed = append([]string(nil), g10protocol.BrainNoMatchingEventAllowed...)

// OpenActions mirrors protocol.BrainOpenActions.
var OpenActions = append([]string(nil), g10protocol.BrainOpenActions...)
