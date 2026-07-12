// Package reversetapes reconstructs decision-before ExecutableProfitTapes from
// triple-excluded, entry-eligible historical winners. Future outcomes are labels
// only; proofs must bind from verified upstream artifacts, never self-declared.
package reversetapes

import g10protocolv2 "lingxi/internal/routerg10/protocolv2"

const (
	SchemaVersionTape     = "lingxi.routerg10.executable-profit-tape.v1"
	SchemaVersionConfig   = "lingxi.routerg10.reverse-tapes.config.v1"
	SchemaVersionLedger   = "lingxi.routerg10.reverse-tapes.ledger.v1"
	SchemaVersionManifest = "lingxi.routerg10.reverse-tapes.manifest.v1"
	SchemaVersionSummary  = "lingxi.routerg10.reverse-tapes.summary.v1"

	Generation = g10protocolv2.Generation
	DiscoveryMode = g10protocolv2.DiscoveryMode

	CheckpointSchemaVersion = "lingxi.routerg10.reverse-tapes.checkpoint.v1"

	OutputTapesFile    = "executable_profit_tapes.jsonl"
	OutputDiscoveryFile = "discovery_only_tapes.jsonl"
	LedgerFileName     = "reverse_tapes_ledger.json"
	ManifestFileName   = "reverse_tapes_manifest.json"
	SummaryFileName    = "reverse_tapes_summary.json"

	RuntimeGOMAXPROCS = g10protocolv2.RuntimeGOMAXPROCS
	ProgressEvery     = 500

	TapeStatusExecutableCertified = "EXECUTABLE_CERTIFIED"
	TapeStatusDiscoveryOnly       = "DISCOVERY_ONLY"

	LedgerStatusAccepted  = "ACCEPTED"
	LedgerStatusRejected  = "REJECTED"
	LedgerStatusDiscovery = "DISCOVERY_ONLY"

	RejectCannotEnter           = "CANNOT_ENTER"
	RejectCannotExit              = "CANNOT_EXIT"
	RejectEntryUnproven           = "ENTRY_UNPROVEN"
	RejectExitUnproven            = "EXIT_UNPROVEN"
	RejectExclusionNotAccepted    = "EXCLUSION_NOT_ACCEPTED"
	RejectMissingProofBinding     = "MISSING_PROOF_BINDING"
	RejectProofHashMismatch       = "PROOF_HASH_MISMATCH"
	RejectSkipFrozenAction        = "SKIP_FROZEN_ACTION"
	RejectAnnouncementLookahead   = "ANNOUNCEMENT_LOOKAHEAD"
	RejectShockOverlap            = "SHOCK_CLUSTER_OVERLAP"
	RejectActionHindsight         = "ACTION_HINDSIGHT"
	RejectFillOnlySurvivorship    = "FILL_ONLY_SURVIVORSHIP"
	RejectPaperWinnerNoFill       = "PAPER_WINNER_NO_FILL"
	RejectIncompletePIT           = "INCOMPLETE_ANNOUNCEMENT_PIT"

	FamilyAnnouncementShock   = "cross_venue_announcement_shock"
	FamilyMechanicalFlowTail    = "mechanical_flow_tail"
	FamilyMinLotNonlinearity    = "min_lot_nonlinearity"
	FamilyPrepositionedCapital  = "prepositioned_capital"
	FamilyRareDynamicAbandon    = "rare_dynamic_abandon"
	FamilyUnknown               = "unknown"

	DirectionLong  = "LONG"
	DirectionShort = "SHORT"

	MissedProfitableMoveStatus = g10protocolv2.MissedProfitableMoveStatus
)

var ExecutionActionsCompared = append([]string(nil), g10protocolv2.ExecutionActionsCompared...)

var PersonalTapeFamilies = []string{
	FamilyAnnouncementShock,
	FamilyMechanicalFlowTail,
	FamilyMinLotNonlinearity,
	FamilyPrepositionedCapital,
	FamilyRareDynamicAbandon,
}
