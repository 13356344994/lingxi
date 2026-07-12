// Package personalbrain implements G10 Personal-Exclusion Brain v2: per-bar
// CAN_ENTER/CANNOT/MISSED_CORRECTLY gating, frozen BOUNDED_MAKER /
// CONDITIONAL_MARKETABLE_LIMIT / SKIP execution, independent solvency, and
// BrainTrace v2 with full proof binding. G10 v1 brain remains unchanged.
package personalbrain

import (
	g10constitution "lingxi/internal/routerg10/constitution"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

const (
	SchemaVersionBar     = "lingxi.routerg10.personal-brain.bar.v1"
	SchemaVersionFixture = "lingxi.routerg10.personal-brain.fixture.v1"
	SchemaVersionReplay  = "lingxi.routerg10.personal-brain.replay.v1"

	Generation   = g10protocolv2.Generation
	ProtocolHash = g10protocolv2.RouterProtocolHash

	TraceSchemaVersion = g10protocolv2.BrainTraceSchemaV2

	MaxEventTerminalLossFraction   = g10protocolv2.MaxEventTerminalLossFraction
	MaxAccountTerminalRiskFraction = g10protocolv2.MaxAccountTerminalRiskFraction

	EnterCanEnter          = "CAN_ENTER"
	EnterCannotEnter       = "CANNOT_ENTER"
	EnterMissedCorrectly   = "MISSED_CORRECTLY"

	ActionBoundedMaker              = g10executionpolicy.ActionBoundedMaker
	ActionConditionalMarketableLimit = g10executionpolicy.ActionConditionalMarketableLimit
	ActionSkip                      = g10executionpolicy.ActionSkip

	MissedProfitableMoveStatus = g10protocolv2.MissedProfitableMoveStatus
	ToxicFillRevalidateStatus  = g10protocolv2.ToxicFillRevalidateStatus
	ToxicFillExitStatus        = g10protocolv2.ToxicFillExitStatus
	EntryUnprovenStatus        = g10protocolv2.EntryUnprovenStatus

	SolvencyStateOK       = "ok"
	SolvencyStateWarning  = "warning"
	SolvencyStateBreached = "breached"

	StopErrorHolding = "holding"
	StopErrorExit    = "exit"

	ThesisHealthy     = "healthy"
	ThesisWeakening   = "weakening"
	ThesisInvalidated = "invalidated"

	RejectConcurrencyFull       = "CONCURRENCY_FULL"
	RejectBelowMinNotional      = "BELOW_MIN_NOTIONAL"
	RejectEntryUnproven         = "ENTRY_UNPROVEN"
	RejectExitUnproven          = "EXIT_UNPROVEN"
	RejectCrowdingStop          = "CROWDING_STOP"
	RejectCapitalExpansionStop  = "CAPITAL_EXPANSION_STOP"
	RejectSolvencyForced        = "SOLVENCY_FORCED"
	RejectStopErrorForced       = "STOP_ERROR_FORCED"
	RejectPlaybookNoMatch       = "PLAYBOOK_NO_MATCH"
	RejectToxicTopUpForbidden   = "TOXIC_TOP_UP_FORBIDDEN"
	RejectNoChaseAfterMiss      = "NO_CHASE_AFTER_MISS"
	RejectMakerGateForbid       = "MAKER_GATE_FORBID"
	RejectExecutionNotAllowed   = "EXECUTION_NOT_ALLOWED"
	RejectTerminalRiskCeiling   = "TERMINAL_RISK_CEILING"
	RejectThesisInvalid         = "THESIS_INVALID"

	TradeAllowedDefault = g10protocolv2.TradeAllowedDefault
)

// PositionActions mirrors protocolv2 brain position actions.
var PositionActions = append([]string(nil), g10protocolv2.BrainPositionActions...)

// ExecutionActionsCompared mirrors frozen execution comparison set.
var ExecutionActionsCompared = append([]string(nil), g10protocolv2.ExecutionActionsCompared...)

// StopErrorExitTriggers mirrors protocolv2 exit triggers.
var StopErrorExitTriggers = append([]string(nil), g10protocolv2.StopErrorExitTriggers...)

// TraceRequiredFields mirrors BrainTraceV2RequiredFields.
var TraceRequiredFields = append([]string(nil), g10protocolv2.BrainTraceV2RequiredFields...)

// PathMatrixRequired is the G10P brain v2 path coverage matrix.
var PathMatrixRequired = []string{
	"can_enter_bounded_maker_long",
	"cannot_enter_unproven",
	"missed_correctly_maker_timeout",
	"conditional_marketable_limit_long",
	"execution_skip",
	"toxic_fill_revalidate",
	"toxic_fill_exit",
	"thesis_invalid_exit",
	"solvency_trim_independent",
	"deadline_stop_error_exit",
	"crowding_stop",
	"capital_expansion_stop",
	"terminal_risk_sizing_down",
	"announcement_pit_visible",
	"below_min_notional_skip",
	"no_chase_after_miss",
}

// CapitalTiers mirrors personal constitution tiers.
var CapitalTiers = append([]float64(nil), g10constitution.CapitalTiersUSDT...)
