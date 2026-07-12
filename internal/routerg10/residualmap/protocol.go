// Package residualmap builds the G10 small-capacity personal residual opportunity map
// from frozen G6 historical events and the local personal execution fingerprint.
package residualmap

import g10protocol "lingxi/internal/routerg10/protocol"

const (
	SchemaVersion        = "lingxi.routerg10.residual-map.v1"
	SchemaVersionConfig  = "lingxi.routerg10.residual-map.config.v1"
	SchemaVersionLedger  = "lingxi.routerg10.residual-map.ledger.v1"
	SchemaVersionManifest = "lingxi.routerg10.residual-map.manifest.v1"
	SchemaVersionSummary = "lingxi.routerg10.residual-map.summary.v1"

	Generation = g10protocol.Generation

	CheckpointSchemaVersion = "lingxi.routerg10.residual-map.checkpoint.v1"
	OutputFileName          = "personal_residual_map.json"
	LedgerFileName          = "residual_map_ledger.json"
	ManifestFileName        = "residual_map_manifest.json"
	SummaryFileName         = "residual_map_summary.json"

	RuntimeGOMAXPROCS = g10protocol.RuntimeGOMAXPROCS
	ProgressEvery     = 1000

	CandidateStatus = g10protocol.PersonalWildCandidateStatus

	RejectPublicScalable       = "PUBLIC_SCALABLE"
	RejectSubsecondSpeed       = "SUBSECOND_SPEED_REQUIRED"
	RejectPublicSimpleFactor   = "PUBLIC_SIMPLE_FACTOR"
	RejectFingerprintNegative  = "FINGERPRINT_NEGATIVE"
	RejectUnknownCriticalCosts = "UNKNOWN_CRITICAL_COSTS"
	RejectUserTierNegative     = "USER_TIER_NEGATIVE"
	RejectNoLargeTierFail      = "NO_LARGE_TIER_FAIL"
	RejectBarReplayMiss        = "BAR_REPLAY_MISS"
	RejectDuplicate            = "DUPLICATE_EVENT"

	LedgerStatusAccepted = "ACCEPTED"
	LedgerStatusRejected = "REJECTED"
	LedgerStatusSkipped  = "SKIPPED"

	LargeTierFailCapacity   = "capacity"
	LargeTierFailImpact     = "impact"
	LargeTierFailMarginTime = "margin_time"
	LargeTierFailExecution  = "execution_complexity"
)

// TierLabels mirrors protocol.ResidualTiersUSDT.
var TierLabels = append([]string(nil), g10protocol.ResidualTiersUSDT...)

// TierNumericUSDT is the numeric capital for non-actual tiers.
var TierNumericUSDT = append([]float64(nil), g10protocol.ResidualTierNumericUSDT...)

// UserTierLabels are tiers that may qualify as the personal user tier.
var UserTierLabels = []string{"actual", "30", "100", "1000"}

// LargeTierLabels are institutional-scale tiers that must fail for a personal residual.
var LargeTierLabels = []string{"10000", "100000"}
