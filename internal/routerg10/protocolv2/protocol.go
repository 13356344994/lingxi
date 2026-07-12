// Package protocolv2 is the G10 Personal-Exclusion v2 preregistration enforcement point.
// It must stay in sync with
// research/firstbattle/hierarchical_router_g10_personal_exclusion_preregistration.json.
// G10 v1 protocol hash and constants remain in internal/routerg10/protocol only.
package protocolv2

import (
	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocol "lingxi/internal/routerg10/protocol"
)

const (
	ProtocolID         = "firstbattle.hierarchical_router.g10.personal_exclusion"
	Generation         = "firstbattle-router-g10-personal-exclusion"
	RouterProtocolHash = "74d043b13fe46468baaa1f670112aeef3b446d0538956936fefc831a0dc39dc8"

	ConstitutionSchemaVersion = g10constitution.SchemaVersion

	HistoryDataHash             = g10protocol.HistoryDataHash
	HistoryRegimeContractHash   = g10protocol.HistoryRegimeContractHash
	HistoryDataRequirementsHash = g10protocol.HistoryDataRequirementsHash

	G1RouterProtocolHash = g10protocol.G1RouterProtocolHash
	G1Generation         = g10protocol.G1Generation
	G1SystemStatus       = g10protocol.G1SystemStatus

	G2RouterProtocolHash         = g10protocol.G2RouterProtocolHash
	G2Generation                 = g10protocol.G2Generation
	G2RegisteredSearchCombos     = g10protocol.G2RegisteredSearchCombos
	G2SystemStatus               = g10protocol.G2SystemStatus
	G2PrizeMechanism             = g10protocol.G2PrizeMechanism

	G3RouterProtocolHash    = g10protocol.G3RouterProtocolHash
	G3Generation            = g10protocol.G3Generation
	G3RegisteredSearchRules = g10protocol.G3RegisteredSearchRules
	G3SystemStatus          = g10protocol.G3SystemStatus

	G4RouterProtocolHash       = g10protocol.G4RouterProtocolHash
	G4Generation               = g10protocol.G4Generation
	G4FullMarketCoverageOKX1H  = g10protocol.G4FullMarketCoverageOKX1H
	G4FullMarketCoverageOKX5M  = g10protocol.G4FullMarketCoverageOKX5M
	G4FrozenCandidates         = g10protocol.G4FrozenCandidates
	G4ValidationPassCandidates = g10protocol.G4ValidationPassCandidates
	G4SystemStatus             = g10protocol.G4SystemStatus

	G5RouterProtocolHash   = g10protocol.G5RouterProtocolHash
	G5Generation           = g10protocol.G5Generation
	G5FrozenPolicyCount    = g10protocol.G5FrozenPolicyCount
	G5TrainingStatus       = g10protocol.G5TrainingStatus
	G5SystemStatus         = g10protocol.G5SystemStatus

	G6RouterProtocolHash  = g10protocol.G6RouterProtocolHash
	G6Generation          = g10protocol.G6Generation
	G6EventCards          = g10protocol.G6EventCards
	G6ValidationPassCards = g10protocol.G6ValidationPassCards
	G6SystemStatus        = g10protocol.G6SystemStatus
	G6ExamStatus          = g10protocol.G6ExamStatus

	G10RouterProtocolHash              = g10protocol.RouterProtocolHash
	G10Generation                      = g10protocol.Generation
	G10ZeroCandidatesInterpretation    = "execution_fingerprint_technical_block_not_market_absence"
	G10LegacyMaxEventStressLossFraction   = 0.005
	G10LegacyMaxAccountStressLossFraction = 0.02

	MaxEventTerminalLossFraction   = g10constitution.MaxEventTerminalLossFraction
	MaxAccountTerminalRiskFraction = g10constitution.MaxAccountTerminalRiskFraction
	RiskCeilingIsNotSizingTarget   = g10constitution.RiskCeilingIsNotSizingTarget
	SizingDirection                = g10constitution.SizingDirection

	ExecutionVenueOKXOnly            = g10constitution.ExecutionVenueOKXOnly
	CrossVenueExecutionForbidden     = true
	AllowLargeFloatingLoss           = g10constitution.AllowLargeFloatingLoss
	AllowOvernightHold               = g10constitution.AllowOvernightHold
	AllowCrossFundingSettlement      = g10constitution.AllowCrossFundingSettlement
	MultiVenuePresetCapitalFuture    = g10constitution.MultiVenuePresetCapitalFuture
	NoManualConfirmationRequired     = g10constitution.NoManualConfirmationRequired
	LongTermIdleCapacityAllowed      = g10constitution.LongTermIdleCapacityAllowed
	SolvencyIndependentRequired      = g10constitution.SolvencyIndependentRequired
	ProhibitLossChasing              = g10constitution.ProhibitLossChasing
	ProhibitUpwardRounding           = g10constitution.ProhibitUpwardRounding
	ProhibitTopUpAfterToxicFill      = g10constitution.ProhibitTopUpAfterToxicFill
	ActualEquityTierLabel            = g10constitution.ActualEquityTierLabel

	MaxConcurrent30U   = g10constitution.MaxConcurrent30U
	MaxConcurrent100U  = g10constitution.MaxConcurrent100U
	MaxConcurrent1000U = g10constitution.MaxConcurrent1000U

	TripleExclusionSchemaVersion        = "lingxi.routerg10.triple-exclusion.v1"
	CrossVenuePITAnnouncementsSchemaV1    = "lingxi.routerg10.cross-venue-pit-announcements.v1"
	EntryProofSchemaV1                  = "lingxi.routerg10.entry-proof.v1"
	ExecutionPolicySchemaV1             = "lingxi.routerg10.execution-policy.v1"
	ExitProofSchemaV1                   = "lingxi.routerg10.exit-proof.v1"
	BrainTraceSchemaV2                  = "lingxi.routerg10.brain-trace.v2"

	EntryUnprovenStatus          = "ENTRY_UNPROVEN"
	MissedProfitableMoveStatus   = "MISSED_PROFITABLE_MOVE"
	ToxicFillRevalidateStatus    = "TOXIC_FILL_REVALIDATE"
	ToxicFillExitStatus          = "TOXIC_FILL_EXIT"
	NotPersonalEnoughStatus      = "NOT_PERSONAL_ENOUGH"
	UnexplainedPersonalEdgeStatus = g10protocol.MechanismUnexplainedPersonalEdgeStatus

	CrossVenueVisibilityRule = "first_seen_at_lte_decision_time_only"
	CrossVenueFailOpenForbidden = true

	ExamActions           = 0
	ExamUnavailableStatus = g10protocol.ExamUnavailableStatus
	TradeAllowedDefault   = false
	ExamEligible          = false
	Certified             = false

	FullHistoryRequired      = true
	SmokeSubstituteForbidden = true

	RuntimeGOMAXPROCS            = g10protocol.RuntimeGOMAXPROCS
	RuntimeNicePriority          = g10protocol.RuntimeNicePriority
	RuntimeCheckpointRequired    = g10protocol.RuntimeCheckpointRequired
	RuntimeCheckpointShardRule   = g10protocol.RuntimeCheckpointShardRule
	RuntimeForbiddenFullServerScan = g10protocol.RuntimeForbiddenFullServerScan

	ResearchPermille    = g10protocol.ResearchPermille
	ValidationPermille  = g10protocol.ValidationPermille
	WalkForwardPermille = g10protocol.WalkForwardPermille
	PartitionOrder      = g10protocol.PartitionOrder

	DiscoveryMode = "outcome_first_reverse_from_triple_excluded_entry_eligible_winners"
)

var CapitalTiersUSDT = append([]float64(nil), g10constitution.CapitalTiersUSDT...)

var CrossVenuePITVenues = append([]string(nil), g10constitution.CrossVenuePITVenues...)

var CrossVenuePITInfoVenues = append([]string{"okx"}, CrossVenuePITVenues...)

var MaxConcurrentByCapital = map[int]int{
	int(CapitalTiersUSDT[0]): MaxConcurrent30U,
	int(CapitalTiersUSDT[1]): MaxConcurrent100U,
	int(CapitalTiersUSDT[2]): MaxConcurrent1000U,
}

var TripleExclusionAxes = []string{"institution", "retail", "bot"}

var InstitutionExclusionEvidence = []string{
	"capacity_cliff",
	"absolute_profit_insufficient",
	"capital_idle_too_long",
	"custody_authorization_limit",
	"large_order_impact",
}

var RetailExclusionEvidence = []string{
	"long_idle_required",
	"cannot_chase_price",
	"must_tolerate_large_drawdown_with_thesis_exit",
	"min_lot_nonlinearity",
	"complex_operational_discipline",
}

var BotExclusionEvidence = []string{
	"event_sparsity",
	"fixed_threshold_failure",
	"conditional_combo_changes_per_event",
	"queue_crowding",
	"post_publication_decay",
	"crowding_stress_1_3_5_micro_competitors",
}

var ForbiddenWildEvidence = []string{
	"SMALL_BUT_COMPETITIVE",
	"PUBLIC_AND_CROWDED",
	"fixed_public_signal_only",
	"small_market_size_only",
}

var CrossVenueAnnouncementEventTypes = []string{
	"listing",
	"delisting",
	"maintenance",
	"suspension",
	"trading_state_change",
}

var CrossVenueAnnouncementRequiredFields = []string{
	"published_at",
	"first_seen_at",
	"effective_at",
	"source_hash",
}

var CrossVenueForbiddenBackfill = []string{
	"web_update_time",
	"future_effective_time",
	"current_universe",
}

var EntryProofRequiredFields = []string{
	"decision_visible_info",
	"order_type",
	"price",
	"min_sz",
	"lot_sz",
	"ct_val",
	"ct_type",
	"dynamic_min_notional",
	"latency",
	"executable_depth",
	"participation_rate",
	"partial_fill",
	"queue_estimate",
	"max_wait",
	"cancel_rule",
	"no_chase_rule",
	"evidence_grade",
}

var EntryProofDiscoveryAllowedGrades = []string{"A", "B", "C", "D"}
var EntryProofCertificationAllowedGrades = []string{"A", "B"}

var ExecutionActionsCompared = []string{
	"BOUNDED_MAKER",
	"CONDITIONAL_MARKETABLE_LIMIT",
	"SKIP",
}

var MakerFillQuadrants = []string{
	"filled_profitable",
	"filled_unprofitable",
	"unfilled_subsequently_profitable",
	"unfilled_not_profitable",
}

var ConditionalMarketableLimitRequires = []string{
	"max_price_cap",
	"max_slippage",
	"min_remaining_edge",
	"timeout_cancel",
}

var ExitProofRequiredFields = []string{
	"exit_depth_sufficient",
	"can_exit_before_delist_or_maintenance",
	"extreme_slippage_bound",
	"partial_close_feasible",
}

var BrainPositionActions = []string{
	"NO_TRADE",
	"LONG",
	"SHORT",
	"HOLD",
	"ADD",
	"TRIM",
	"CLOSE",
	"REVERSE",
}

var BrainOpenActions = []string{"LONG", "SHORT"}

var BrainNoMatchingEventAllowed = []string{"HOLD", "TRIM", "CLOSE"}

var StopErrorExitTriggers = []string{
	"event_complete",
	"payer_disappeared",
	"invalidator",
	"ood",
	"deadline",
	"thesis_health_fail",
	"toxic_fill_exit",
}

var BrainTraceV2RequiredFields = []string{
	"state_hash",
	"constitution_hash",
	"exclusion_proof_hash",
	"announcement_visibility",
	"entry_proof_hash",
	"execution_policy_hash",
	"maker_selection_bias",
	"fill_or_miss_reason",
	"toxic_fill_reason",
	"exit_proof_hash",
	"crowding_stress",
	"terminal_risk_usage",
	"final_action",
	"trace_hash",
}

var PlaybookHashChain = []string{
	"ConstitutionHash",
	"ExclusionProofHash",
	"EntryProofHash",
	"ExecutionPolicyHash",
	"ExitProofHash",
	"AnnouncementTapeHash",
	"PlaybookHash",
}
