// Package personalvalidation implements G10 Personal-Exclusion historical validation.
// G10 v1 validation in internal/routerg10/validation remains unchanged.
package personalvalidation

import (
	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

const (
	SchemaVersionReport     = "lingxi.router-g10-personal-validation.report.v1"
	SchemaVersionSurvivors  = "lingxi.router-g10-personal-validation.survivors.v1"
	SchemaVersionReceipt    = "lingxi.router-g10-personal-validation.receipt.v1"
	SchemaVersionConfig     = "lingxi.router-g10-personal-validation.config.v1"
	SchemaVersionCheckpoint = "lingxi.router-g10-personal-validation.checkpoint.v1"
	SchemaVersionLayer      = "lingxi.router-g10-personal-validation.layer.v1"

	Generation   = g10protocolv2.Generation
	ProtocolHash = g10protocolv2.RouterProtocolHash

	RuntimeGOMAXPROCS = g10protocolv2.RuntimeGOMAXPROCS
	ProgressEvery     = 500

	OutputReportFile    = "g10_personal_validation_report.json"
	OutputSurvivorsFile = "g10_personal_validation_survivors.json"
	OutputReceiptFile   = "personal_validation_receipt.json"
	OutputLayerFile     = "g10_personal_validation_layers.json"

	StatusInconclusive   = "INCONCLUSIVE"
	StatusValidationPass = "VALIDATION_PASS"
	StatusValidationFail = "VALIDATION_FAIL"

	LayerHistoricalPhenomenon      = "HISTORICAL_PHENOMENON"
	LayerEntryProven               = "ENTRY_PROVEN"
	LayerCostBounded               = "COST_BOUNDED"
	LayerPersonalExecutionVerified = "PERSONAL_EXECUTION_VERIFIED"
	LayerWFPass                    = "WF_PASS"

	ReasonCoverage            = "COVERAGE"
	ReasonNoEdge              = "NO_EDGE"
	ReasonCost                = "COST"
	ReasonDelay               = "DELAY"
	ReasonConcentration       = "CONCENTRATION"
	ReasonSolvency            = "SOLVENCY"
	ReasonCapacity            = "CAPACITY"
	ReasonBrainDegeneracy     = "BRAIN_DEGENERATE"
	ReasonPITContamination    = "PIT_CONTAMINATION"
	ReasonMakerSelectionBias  = "MAKER_SELECTION_BIAS"
	ReasonCrowding            = "CROWDING"
	ReasonExitUnproven        = "EXIT_UNPROVEN"
	ReasonEntryUnproven       = "ENTRY_UNPROVEN"
	ReasonInsufficientEvidence = "INSUFFICIENT_EVIDENCE"

	SurvivorStatus = g10protocolv2.UnexplainedPersonalEdgeStatus

	InconclusiveNotZeroEdgeNote = "INCONCLUSIVE is not zero edge; evidence insufficient for pass/fail"

	MaxEventTerminalLossFraction   = g10protocolv2.MaxEventTerminalLossFraction
	MaxAccountTerminalRiskFraction = g10protocolv2.MaxAccountTerminalRiskFraction

	ValidationMinEventCount            = 50
	ValidationMinIndependentDays       = 20
	ValidationMinNonOverlapShockBlocks = 10
	CostStressMultiplier               = 1.5
	P90SlippageStressMultiplier        = 1.9

	CrowdingStressLevels = "1,3,5"
)

var ValidationDelayBars = []int{0, 1, 2}

var CapitalTiersUSDT = append([]float64(nil), g10constitution.CapitalTiersUSDT...)

var MakerFillQuadrants = append([]string(nil), g10protocolv2.MakerFillQuadrants...)
