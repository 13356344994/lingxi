// Package exclusionmap builds the G10 Personal-Exclusion triple-exclusion map from
// frozen G6 event maps and strict PIT G10 announcements. Each historical event must
// carry metric-bound ExclusionProof for institution, retail, and bot axes.
package exclusionmap

import (
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

const (
	SchemaVersion         = g10protocolv2.TripleExclusionSchemaVersion
	SchemaVersionConfig   = "lingxi.routerg10.exclusion-map.config.v1"
	SchemaVersionLedger   = "lingxi.routerg10.exclusion-map.ledger.v1"
	SchemaVersionManifest = "lingxi.routerg10.exclusion-map.manifest.v1"
	SchemaVersionSummary  = "lingxi.routerg10.exclusion-map.summary.v1"

	Generation = g10protocolv2.Generation

	CheckpointSchemaVersion = "lingxi.routerg10.exclusion-map.checkpoint.v1"
	OutputFileName          = "triple_exclusion_map.json"
	LedgerFileName          = "exclusion_map_ledger.json"
	ManifestFileName        = "exclusion_map_manifest.json"
	SummaryFileName         = "exclusion_map_summary.json"

	RuntimeGOMAXPROCS = g10protocolv2.RuntimeGOMAXPROCS
	ProgressEvery     = 500

	RejectSmallOnly      = "SMALL_ONLY"
	RejectPublicCrowded  = "PUBLIC_CROWDED"
	RejectFixedSignal    = "FIXED_SIGNAL"
	RejectFirstInOnly    = "FIRST_IN_ONLY"
	RejectBarReplayMiss  = "BAR_REPLAY_MISS"
	RejectIncompletePIT  = "INCOMPLETE_ANNOUNCEMENT_PIT"
	RejectAxisIncomplete = "TRIPLE_AXIS_INCOMPLETE"
	RejectNoValidReason  = "NO_VALID_ACCEPT_REASON"

	AcceptPersonalConditionDependent = "PERSONAL_CONDITION_DEPENDENT"
	AcceptLowMotivationLowCapacity   = "LOW_MOTIVATION_LOW_CAPACITY"

	LedgerStatusAccepted = "ACCEPTED"
	LedgerStatusRejected = "REJECTED"
	LedgerStatusSkipped  = "SKIPPED"

	AxisInstitution = "institution"
	AxisRetail      = "retail"
	AxisBot         = "bot"
)

// TripleExclusionAxes mirrors protocolv2 frozen axes.
var TripleExclusionAxes = append([]string(nil), g10protocolv2.TripleExclusionAxes...)

// InstitutionEvidence lists frozen institution exclusion evidence types.
var InstitutionEvidence = append([]string(nil), g10protocolv2.InstitutionExclusionEvidence...)

// RetailEvidence lists frozen retail exclusion evidence types.
var RetailEvidence = append([]string(nil), g10protocolv2.RetailExclusionEvidence...)

// BotEvidence lists frozen bot exclusion evidence types.
var BotEvidence = append([]string(nil), g10protocolv2.BotExclusionEvidence...)

// ForbiddenWildEvidence lists evidence that cannot alone justify wild playbooks.
var ForbiddenWildEvidence = append([]string(nil), g10protocolv2.ForbiddenWildEvidence...)

// CrowdingStressLevels is the frozen micro-competitor stress ladder.
var CrowdingStressLevels = []int{1, 3, 5}
