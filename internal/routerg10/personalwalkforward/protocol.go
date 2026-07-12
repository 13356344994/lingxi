// Package personalwalkforward implements G10 Personal-Exclusion walk-forward validation.
// G10 v1 walkforward in internal/routerg10/walkforward remains unchanged.
package personalwalkforward

import (
	g10protocol "lingxi/internal/routerg10/protocol"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

const (
	SchemaVersionWFReport     = "lingxi.router-g10-personal-wf.report.v1"
	SchemaVersionWFSummary    = "lingxi.router-g10-personal-wf.summary.v1"
	SchemaVersionStageReceipt = "lingxi.router-g10-personal-wf.stage-receipt.v1"
	SchemaVersionConfig       = "lingxi.router-g10-personal-wf.config.v1"
	SchemaVersionCheckpoint   = "lingxi.router-g10-personal-wf.checkpoint.v1"

	Generation   = g10protocolv2.Generation
	ProtocolHash = g10protocolv2.RouterProtocolHash
	RuntimeGOMAXPROCS = g10protocolv2.RuntimeGOMAXPROCS
	ProgressEvery     = 500

	WFFoldCount               = g10protocol.WFFoldCount
	WFMaxDrawdownFraction     = 0.30
	WFMaxCapacityFailFraction = 0.50

	StatusSkippedValidationFail = "SKIPPED_VALIDATION_FAIL"
	StatusWFPass                = g10protocol.WFPassStatus
	StatusWFFail                = "WF_FAIL"
	StatusInconclusive          = g10personalvalidation.StatusInconclusive

	ReasonValidationNotPass   = "VALIDATION_NOT_PASS"
	ReasonValidationTamper    = "VALIDATION_TAMPER"
	ReasonPlaybookUnavailable = "PLAYBOOK_UNAVAILABLE"
	ReasonFoldNetReturn       = "FOLD_NET_RETURN"
	ReasonLOFO                = "LOFO"
	ReasonDay                 = "DAY_ROBUSTNESS"
	ReasonShock               = "SHOCK_ROBUSTNESS"
	ReasonSymbol              = "SYMBOL_ROBUSTNESS"
	ReasonInsufficientEvents  = "INSUFFICIENT_EVENTS"
	ReasonBrainDegeneracy     = g10personalvalidation.ReasonBrainDegeneracy

	OutputWFReportFile  = "g10_personal_wf_report.json"
	OutputWFSummaryFile = "g10_personal_wf_summary.json"
	OutputReceiptFile   = "g10_personal_wf_stage_receipt.json"
)
