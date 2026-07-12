// Package executionpolicy freezes BOUNDED_MAKER / CONDITIONAL_MARKETABLE_LIMIT / SKIP
// comparisons at decision time before outcome is known.
package executionpolicy

import g10protocolv2 "lingxi/internal/routerg10/protocolv2"

const (
	SchemaVersion = g10protocolv2.ExecutionPolicySchemaV1
	ConfigSchema  = "lingxi.routerg10.execution-policy.config.v1"
	Generation    = "firstbattle-router-g10-personal-exclusion"
	ManifestSchemaVersion = "lingxi.routerg10.execution-policy.manifest.v1"
	CheckpointSchemaVersion = "lingxi.routerg10.execution-policy.checkpoint.v1"

	OutputPoliciesFile = "g10_execution_policies.jsonl"
	OutputManifestFile = "g10_execution_policy_manifest.json"

	ActionBoundedMaker              = "BOUNDED_MAKER"
	ActionConditionalMarketableLimit = "CONDITIONAL_MARKETABLE_LIMIT"
	ActionSkip                      = "SKIP"

	QuadrantFilledProfitable              = "filled_profitable"
	QuadrantFilledUnprofitable            = "filled_unprofitable"
	QuadrantUnfilledSubsequentlyProfitable = "unfilled_subsequently_profitable"
	QuadrantUnfilledNotProfitable         = "unfilled_not_profitable"

	MissedProfitableMoveStatus = g10protocolv2.MissedProfitableMoveStatus
	ToxicFillRevalidateStatus  = g10protocolv2.ToxicFillRevalidateStatus
	ToxicFillExitStatus        = g10protocolv2.ToxicFillExitStatus

	FastFillThresholdMS = 200

	RuntimeGOMAXPROCS = 1
	ProgressEvery     = 500
)
