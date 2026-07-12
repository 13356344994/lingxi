// Package brain implements the G10 final runtime decision engine: per-bar dynamic
// direction/time/sizing across eight frozen actions with independent StopError and
// Solvency guards. Account accounting inherits G5/G6 fixed-notional semantics.
package brain

import g10protocol "lingxi/internal/routerg10/protocol"

const (
	SchemaVersionBar       = "lingxi.routerg10.brain.bar.v1"
	SchemaVersionFixture   = "lingxi.routerg10.brain.fixture.v1"
	SchemaVersionReplay    = "lingxi.routerg10.brain.replay.v1"

	Generation   = g10protocol.Generation
	ProtocolHash = g10protocol.RouterProtocolHash

	MaxAccountLeverage           = g10protocol.MaxAccountLeverage
	MaxEventStressLossFraction   = g10protocol.MaxEventStressLossFraction
	MaxAccountStressLossFraction = g10protocol.MaxAccountStressLossFraction

	RejectConcurrencyFull    = "CONCURRENCY_FULL"
	RejectBelowMinNotional   = "BELOW_MIN_NOTIONAL"
	RejectCapacityClip       = "CAPACITY_CLIP"
	RejectCorrelationClip    = "CORRELATION_CLIP"
	RejectSolvencyForced     = "SOLVENCY_FORCED"
	RejectStopErrorForced    = "STOP_ERROR_FORCED"
	RejectPlaybookNoMatch    = "PLAYBOOK_NO_MATCH"

	SolvencyStateOK       = "ok"
	SolvencyStateWarning  = "warning"
	SolvencyStateBreached = "breached"

	StopErrorHolding = "holding"
	StopErrorExit    = "exit"

	ThesisHealthy     = "healthy"
	ThesisWeakening   = "weakening"
	ThesisInvalidated = "invalidated"
)

// BrainActions mirrors protocol.BrainActions.
var BrainActions = append([]string(nil), g10protocol.BrainActions...)

// PathMatrixRequired mirrors protocol.BrainTracePathMatrixRequired.
var PathMatrixRequired = append([]string(nil), g10protocol.BrainTracePathMatrixRequired...)

// StopErrorExitTriggers mirrors protocol.StopErrorExitTriggers.
var StopErrorExitTriggers = append([]string(nil), g10protocol.StopErrorExitTriggers...)

// CapitalTiers mirrors protocol.MicroCapitalTiersUSDT.
var CapitalTiers = append([]float64(nil), g10protocol.MicroCapitalTiersUSDT...)
