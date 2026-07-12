// Package entryproof freezes historical entry eligibility before outcome is known.
// OKX-only execution; evidence grades A/B certify, C/D discovery-only (ENTRY_UNPROVEN).
package entryproof

import g10protocolv2 "lingxi/internal/routerg10/protocolv2"

const (
	SchemaVersion = g10protocolv2.EntryProofSchemaV1
	ConfigSchema  = "lingxi.routerg10.entry-proof.config.v1"
	Generation    = "firstbattle-router-g10-personal-exclusion"
	ManifestSchemaVersion = "lingxi.routerg10.entry-proof.manifest.v1"
	CheckpointSchemaVersion = "lingxi.routerg10.entry-proof.checkpoint.v1"

	OutputProofsFile   = "g10_entry_proofs.jsonl"
	OutputManifestFile = "g10_entry_proof_manifest.json"

	ExecutionVenueOKX = g10protocolv2.ExecutionVenueOKXOnly

	EntryUnprovenStatus = g10protocolv2.EntryUnprovenStatus

	GradeRealFillOrWAL              = "A"
	GradeMakerAttemptAckCancel      = "B"
	GradeExecutableShadowOrTrade    = "C"
	GradePublicConservativeBoundary = "D"

	CancelRuleStateChangeImmediate = "state_change_immediate_cancel"
	NoChaseRuleAfterMiss           = "no_chase_after_miss"

	RuntimeGOMAXPROCS = 1
	ProgressEvery     = 500
)
