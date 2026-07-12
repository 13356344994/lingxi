// Package protocol 是 g6 小资金事件机会预注册协议的"统一预注册与代码"强制点:与
// research/firstbattle/hierarchical_router_g6_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——protocol_test.go 解析 manifest 并断言与本文件
// 三方一致(事件族、账户门、成本、分区、考试 commitment schema、红队条数)。
//
// g6 新 generation、新 protocol hash;绑定但绝不修改 g1/g2/g3/g4/g5 hash/result 与历史契约。
package protocol

const (
	ProtocolID         = "firstbattle.hierarchical_router.g6"
	Generation         = "firstbattle-router-g6"
	RouterProtocolHash = "d34b2798bec83cfcbb39eceec05cb644a74e362b741bb3326bf67bcbabfeb759"

	HistoryDataHash             = "f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c"
	HistoryRegimeContractHash   = "a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127"
	HistoryDataRequirementsHash = "07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7"

	G1RouterProtocolHash   = "0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31"
	G1Generation           = "firstbattle-router-g1"
	G1CertifiedMoneyLeaves = 0
	G1ResearchPassLeaves   = 0
	G1SystemStatus         = "NO_GO"

	G2RouterProtocolHash     = "395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec"
	G2Generation             = "firstbattle-router-g2"
	G2RegisteredSearchCombos = 162
	G2CandidateNominations   = 0
	G2ResearchPassLeaves     = 0
	G2SystemStatus           = "NO_GO"
	G2PrizeMechanism         = "funding_payer_flip_rebalance"

	G3RouterProtocolHash    = "1628e1a575585a7a1c2b7da0e01eb54de9707997272a6301e3824505dece17fb"
	G3Generation            = "firstbattle-router-g3"
	G3RegisteredSearchRules = 192
	G3CandidateNominations  = 0
	G3ResearchPassLeaves    = 0
	G3SystemStatus          = "NO_GO"

	G4RouterProtocolHash       = "55d5da85272d9d326c81570a677b47b66ede7da69b14fc58fe4c154c0a1d6425"
	G4Generation               = "firstbattle-router-g4"
	G4FullMarketCoverageOKX1H  = "401/401"
	G4FullMarketCoverageOKX5M  = "200/200"
	G4FrozenCandidates         = 1024
	G4ValidationPassCandidates = 0
	G4SystemStatus             = "NO_GO"

	G5RouterProtocolHash      = "bfe9f784039a89b773f2f9a0fb86d252c39ffa52b7f7202071e02bcc32333a7d"
	G5Generation              = "firstbattle-router-g5"
	G5FrozenPolicyCount       = 0
	G5TrainingStatus          = "TRAINING_REJECTED_DEGENERATE"
	G5ResearchPassPolicies    = 0
	G5SystemStatus            = "NO_GO"

	ExamActions              = 0
	ExamUnavailableStatus    = "EXAM_UNAVAILABLE"
	PublicCommitmentSchemaV1 = "lingxi.routerg6.public-commitments.v1"

	MaxAccountLeverage = 5

	MaxConcurrent30U  = 1
	MaxConcurrent100U = 2
	MaxConcurrent1000U = 3

	MaxEventStressLossFraction    = 0.005
	MaxAccountStressLossFraction  = 0.02

	EventIDFormula           = "symbol + decision_time + event_family"
	WindowNonoverlapRequired = true
	NonOverlapCooldownFormula = "profile_bar_duration * (1 + delay_bars + horizon_bar_count)"
	ShockClusterRequired     = true
	ShockClusterRule         = "group_adjacent_bars_by_common_market_shock_or_mechanism_episode"
	IndependentSampleUnit    = "non_overlap_shock_block"

	ResearchPermille    = 600
	ValidationPermille  = 200
	WalkForwardPermille = 200
	PartitionOrder      = "chronological_earliest_to_latest"
	PurgeEmbargoFormula = "max(profile.max_feature_lookback_hours, event_deadline_hours)"
	PurgeEmbargoRule    = "max(max_feature_lookback_hours, event_deadline_hours)"
	EntryPriceRule      = "next_bar_open_after_decision_close"
	ExitPriceRule       = "stop_error_or_deadline_bar_open"
	PositionNotionalRule = "stress_risk_budget_clipped_by_liquidity_min_notional_lot_floor_and_leverage"
	MinNotionalLotFloorRule = "skip_event_if_sized_notional_below_exchange_min_notional_or_lot_floor_no_upward_rounding"

	OKX1HProfileID               = "okx_1h"
	OKX1HPrimaryFamily           = "okx_1h"
	OKX1HMaxFeatureLookbackHours = 24
	OKX5MProfileID               = "okx_5m"
	OKX5MPrimaryFamily           = "okx_5m"
	OKX5MMaxFeatureLookbackHours = 4

	CostModelInherits             = "firstbattle.hierarchical_router.g4"
	CostProxyDisclaimer           = "conservative_execution_proxy_not_full_order_book_proof"
	RoundtripTakerFeeBPS          = 10.0
	BaseSpreadSlippageFloorBPS    = 2.0
	RollingQuoteVolumeWindowHours = 24
	CapacityImpactScaleBPS        = 10000.0
	CapacityImpactCapBPS          = 50.0
	CapacityImpactFormula         = "min(capacity_impact_cap_bps, capital_usdt / max(rolling_quote_volume_usdt, 1) * capacity_impact_scale_bps)"
	BarRangeToxicityScale         = 0.5
	BarRangeToxicityCapBPS        = 100.0
	BarRangeToxicityFormula       = "min(bar_range_toxicity_cap_bps, (high-low)/max(mid,1e-12) * 10000 * bar_range_toxicity_scale)"
	FundingAcrossSettlements      = true
	FundingFormula                = "sum_abs_settled_funding_rate_over_hold_period_when_available_else_zero"
	FeeNetFormula                 = "gross_bps - roundtrip_taker_fee_bps"
	ConservativeNetFormula        = "gross_bps - roundtrip_taker_fee_bps - base_spread_slippage_floor_bps - capacity_impact_bps - bar_range_toxicity_bps - funding_bps"
	CostStressMultiplier          = 1.5
	DelayPlusOneBarEntry          = true

	DiscoveryMode                  = "outcome_first_reverse_from_net_winner_events"
	InstitutionalModelForbidden    = true
	UnexplainedCandidateStatus     = "UNEXPLAINED_EVENT_CANDIDATE"
	ExperimentLedgerRequired       = true
	FreezeBeforeValidation         = true

	StopErrorRequired              = true
	DeadlineIsFallbackOnly         = true
	MechanicalPriceStopSecondaryOnly = true

	ValidationApplyOn                             = "validation_partition_only"
	ValidationNoExam                              = true
	ValidationFrozenBeforeReadingResults          = true
	ValidationMinEventCount                       = 50
	ValidationMinIndependentDays                  = 20
	ValidationMinNonOverlapShockBlocks            = 10
	ValidationBaseNetReturnMustBePositive         = true
	ValidationAllDelayNetReturnMustBePositive     = true
	ValidationCostStressNetReturnMustBePositive   = true
	ValidationAllEquityTiersMustPass              = true
	ValidationNoLeverageSolvencyBreach            = true
	ValidationDiscoveryScreenFDRRequired          = true
	ValidationDiscoveryScreenFamily               = "registered_event_cards_plus_ledger_attempts"
	ValidationPassStatus                          = "VALIDATION_PASS"
	ValidationFailStatus                          = "VALIDATION_FAIL"
	ValidationInconclusiveStatus                  = "INCONCLUSIVE"
	ValidationSparseSampleAction                  = "INCONCLUSIVE"
	ValidationMinEventCountInconclusiveNote       = "INCONCLUSIVE is not VALIDATION_PASS"
	ValidationSolvencyFailReason                  = "SOLVENCY"
	ValidationMaxSingleDayPositiveContributionFraction  = 0.35
	ValidationMaxSingleEventPositiveContributionFraction = 0.35

	WFPartition                         = "walk_forward"
	WFFoldCount                         = 5
	WFUsesProfilePurgeEmbargo           = true
	WFEligibility                       = "validation_pass_and_signed_hash_verified_only"
	WFSkippedValidationFailStatus       = "SKIPPED_VALIDATION_FAIL"
	DiscoveryDedupPerOutcomeHorizonDelay = true
	DiscoveryDedupTrackerKey             = "symbol|family|horizon|delay_bars"
	ReverseGroupingUnit                  = "non_overlap_shock_cluster_block"

	WFSizingUsesSurvivorFrozenEdge = true
	WFSizingPnLUsesRealizedTrigger = true
	WFSizingEdgeFloorBPS           = 1.0
	WFSizingEdgeSource             = "validation_survivor_frozen_conservative_stress_or_floor_bps"
	WFFrozenCardNoRetuning              = true
	WFScanIncludesAllTriggers         = true
	WFFoldNetReturnMustBePositive       = true
	WFWorstFoldNetReturnMustBePositive  = true
	WFLOFONetReturnMustBePositive       = true
	WFAllDelayNetReturnMustBePositive   = true
	WFCostStressNetReturnMustBePositive = true
	WFSignPValue                        = "exact_one_sided_binomial_sign_test_on_nonzero_daily_equity_delta"
	WFSignNumericMethod                 = "log_gamma_log_sum_exp"
	WFMultipleComparisonControl         = "Holm-Bonferroni"
	WFEventCardAttemptFamily            = "selected_event_card_plus_ledger_attempts"
	WFFamilyAlpha                       = 0.01
	WFDailyReplayMatchesScenarioFunding = true
	WFPassStatus                        = "WF_PASS"
	WFFailStatus                        = "WF_FAIL"
	WFInconclusiveStatus                = "INCONCLUSIVE"

	MechanismUnexplainedEventPassStatus = "UNEXPLAINED_EVENT_PASS"
	MechanismTradeAllowed               = false
	MechanismExamEligible               = false
	MechanismCertified                  = false
	MechanismAutoFromFeaturesForbidden  = true
	MechanismForcedFlowEvidenceRequired = true

	QueueRankingRule              = "conservative_edge / stress_loss / margin_time descending"
	OnePositionPerEvent           = true
	PendingFlushRule              = "execute_matured_before_scoring_discard_unexpired_at_eof"
	PendingFlushExecuteAt         = "current_bar_index"
	PendingExecuteIndexFormula    = "decision_bar_index + 1 + delay_bars"
	PendingStaleRevalidate        = true
	RiskGateAfterBreachRule       = "block_new_event_open_fail_closed_allow_stop_error_trim_close_flat"
	EffectiveLeverageCheckRule    = "mark_to_market_each_bar_sum_event_notional_over_equity"

	ExamRequiredPaperCount = 5
	ExamStandardPaperCount = 4
	ExamGoldenPaperCount   = 1
	ConsumptionRule        = "unseal_grade_or_crash_permanently_consumes_paper"
	GoldenLastRule         = "exactly_one_golden_at_final_queue"

	ExamFixtureSchemaV1                         = "lingxi.routerg6.exam-fixture.v1"
	ExamFixtureMinBlocks                        = 1
	ExamExpectedCommitmentHashFormula           = "sha256(event_card_hash\\nexperiment_hash\\nprotocol_hash)"
	ExamMinIndependentDays                      = 20
	ExamCostStressMultiplier                    = 1.5
	ExamBaseNetReturnMustBePositive             = true
	ExamAllDelayNetReturnMustBePositive         = true
	ExamNoLeverageSolvencyBreach                = true
	ExamCostStressNetReturnMustBePositive       = true
	ExamShockBlockLOORequired                   = true
	ExamMaxConcurrentByCapitalRequired        = true

	ExamBoundaryResearchPassStatus = "RESEARCH_PASS"
	ExamBoundaryNoGoStatus         = "NO_GO"
	ExamBoundaryAvailableStatus    = "AVAILABLE"

	RuntimeGOMAXPROCS            = 1
	RuntimeNicePriority          = 19
	RuntimeCheckpointRequired    = true
	RuntimeCheckpointShardRule   = "resume_from_last_completed_symbol_or_shock_block"
	RuntimeForbiddenFullServerScan = true

	ExamReplayRequired       = true
	ExamReplayGradesComputedOnly = true
)

var DataProfileIDs = []string{"okx_1h", "okx_5m"}

var OptionalNonblockingFamilies = []string{
	"okx_funding",
	"bybit_funding",
	"rubik_taker_1h",
	"rubik_lsratio_1h",
	"rubik_taker_5m",
}

var UntestableFamilies = []string{
	"liquidation",
	"depth",
	"open_interest",
	"xvenue",
}

var PrimaryOHLCVEventFamilies = []string{
	"ohlcv_extreme_continuation",
	"ohlcv_extreme_reversal",
	"session_liquidity_recovery",
	"btc_relative_dislocation",
}

var EventDirections = []string{"long", "short"}

var OKX1HOutcomeHorizonsHours = []int{1, 4, 24}

var OKX5MOutcomeHorizonsMinutes = []int{5, 15, 60, 240}

var DelayBars = []int{0, 1, 2, 3}

var CapitalTiersUSDT = []float64{30, 100, 1000}

var ValidationInitialEquities = CapitalTiersUSDT

var ValidationDelayBars = DelayBars

var EventCardHashChain = []string{"EventCardHash", "ExperimentHash"}

var EventCardRequiredFields = []string{
	"trigger_conditions",
	"direction_rule",
	"max_wait_time",
	"dynamic_deadline",
	"invalidator_conditions",
	"capacity_band",
	"capital_tiers",
	"data_requirements",
}

var StopErrorExitTriggers = []string{
	"event_complete",
	"payer_disappeared",
	"invalidator",
	"ood",
	"deadline",
}

var CoarseTraceMustNotContain = []string{
	"pnl",
	"exam_id",
	"start_time",
	"end_time",
	"cluster_id",
	"fixture_path",
	"hidden_range",
	"pass_threshold",
	"grade_detail",
}

var PublicCommitmentFields = []string{
	"id",
	"queue",
	"kind",
	"family",
	"alpha_spend",
	"data_hash",
	"challenge_hash",
	"protocol_hash",
	"status",
}

var ForbiddenPublicCommitmentFields = []string{
	"start",
	"end",
	"cluster_ids",
	"fixture_path",
	"stratum",
	"fixture",
	"fixture_size",
	"nonce",
	"observations",
}

var AllowedCommitmentStatuses = []string{
	"sealed",
	"running",
	"consumed_passed",
	"consumed_failed",
	"consumed_crashed",
}

// MaxConcurrentByCapital maps initial equity tier to max concurrent event positions.
var MaxConcurrentByCapital = map[int]int{
	30:   MaxConcurrent30U,
	100:  MaxConcurrent100U,
	1000: MaxConcurrent1000U,
}

// ExamScheduleTemplate is the frozen 4 standard + 1 golden queue template.
var ExamScheduleTemplate = []ExamScheduleSlot{
	{Queue: 1, Kind: "standard", Family: "high_volatility", AlphaSpend: 1},
	{Queue: 2, Kind: "standard", Family: "low_volatility_funding", AlphaSpend: 1},
	{Queue: 3, Kind: "standard", Family: "cross_sectional_breadth", AlphaSpend: 1},
	{Queue: 4, Kind: "standard", Family: "delay_stress", AlphaSpend: 1},
	{Queue: 5, Kind: "golden", Family: "golden", AlphaSpend: 1},
}

// ExamScheduleSlot describes one frozen slot in the public exam schedule template.
type ExamScheduleSlot struct {
	Queue      int
	Kind       string
	Family     string
	AlphaSpend int
}
