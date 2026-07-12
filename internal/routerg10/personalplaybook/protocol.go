// Package personalplaybook derives G10 Personal-Exclusion wild playbooks from
// verified EXECUTABLE_CERTIFIED reverse tapes. Discovery-only tapes are recorded
// separately and never certified. Old router-g10-playbooks (residual-map) is untouched.
package personalplaybook

import g10protocolv2 "lingxi/internal/routerg10/protocolv2"

const (
	SchemaVersionPlaybook = "lingxi.router-g10-personal-playbooks.playbook.v1"
	SchemaVersionLedger   = "lingxi.router-g10-personal-playbooks.ledger.v1"
	SchemaVersionManifest = "lingxi.router-g10-personal-playbooks.manifest.v1"
	SchemaVersionSummary  = "lingxi.router-g10-personal-playbooks.summary.v1"
	SchemaVersionConfig   = "lingxi.router-g10-personal-playbooks.config.v1"

	CheckpointSchemaVersion = "lingxi.router-g10-personal-playbooks.checkpoint.v1"

	Generation    = g10protocolv2.Generation
	DiscoveryMode = g10protocolv2.DiscoveryMode

	OutputPlaybooksFile   = "g10_personal_wild_playbooks.json"
	OutputDiscoveryFile   = "discovery_only_playbooks.jsonl"
	LedgerFileName        = "g10_personal_playbook_ledger.json"
	ManifestFileName      = "personal_playbook_manifest.json"
	SummaryFileName       = "g10_personal_playbooks_summary.json"

	LedgerStagePersonalWild = "personal_wild_playbooks_from_reverse_tapes"

	MaxPlaybookCandidates = 64
	RuntimeGOMAXPROCS     = g10protocolv2.RuntimeGOMAXPROCS
	ProgressEvery         = 500

	StatusPersonalWildCandidate = "personal_wild_candidate"
	StatusNotPersonalEnough     = g10protocolv2.NotPersonalEnoughStatus
	StatusDiscoveryOnly         = "DISCOVERY_ONLY"

	ValidationStagePending = "full_playbook_verification_pending"

	LedgerStatusCandidate       = StatusPersonalWildCandidate
	LedgerStatusNotPersonal     = StatusNotPersonalEnough
	LedgerStatusDiscoveryOnly   = StatusDiscoveryOnly
	LedgerStatusDuplicate       = "duplicate"
	LedgerStatusInvalid         = "invalid"
	LedgerStatusDeduped         = "skipped_dedup"
	LedgerStatusTapeInvalid     = "tape_invalid"
	LedgerStatusNotCertifiable  = "not_certifiable"

	RejectNotExecutableCertified = "NOT_EXECUTABLE_CERTIFIED"
	RejectProofBindingIncomplete = "PROOF_BINDING_INCOMPLETE"
	RejectTapeHashMismatch       = "TAPE_HASH_MISMATCH"
	RejectManifestMismatch       = "MANIFEST_MISMATCH"

	ReasonFixedPublicKline     = "fixed_public_kline_pattern"
	ReasonEasyReplication      = "easy_replication_across_symbols"
	ReasonOnlySmall            = "only_small_market"
	ReasonMissingTripleExclude = "missing_triple_exclusion"
	ReasonUnknownTapeFamily    = "unknown_personal_tape_family"
)

var PlaybookHashChain = append([]string(nil), g10protocolv2.PlaybookHashChain...)

var ExecutionActionsCompared = append([]string(nil), g10protocolv2.ExecutionActionsCompared...)

var ForbiddenWildEvidence = append([]string(nil), g10protocolv2.ForbiddenWildEvidence...)
