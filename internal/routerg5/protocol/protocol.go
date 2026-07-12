// Package protocol 是 g5 动态策略预注册协议的"统一预注册与代码"强制点:与
// research/firstbattle/hierarchical_router_g5_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——protocol_test.go 解析 manifest 并断言与本文件
// 三方一致(动作空间、特征、成本、分区、考试 commitment schema、红队条数)。
//
// g5 新 generation、新 protocol hash;绑定但绝不修改 g1/g2/g3/g4 hash/result 与历史契约。
package protocol

const (
	ProtocolID         = "firstbattle.hierarchical_router.g5"
	Generation         = "firstbattle-router-g5"
	RouterProtocolHash = "bfe9f784039a89b773f2f9a0fb86d252c39ffa52b7f7202071e02bcc32333a7d"

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

	ExamActions              = 0
	ExamUnavailableStatus    = "EXAM_UNAVAILABLE"
	PublicCommitmentSchemaV1 = "lingxi.routerg5.public-commitments.v1"

	MaxAccountLeverage = 5
	TopK               = 5
	TrainingSeed       = 42
	MaxModelAttempts   = 256

	DiscoveryTrainingLabelReservoir = 100000
	DiscoveryLambdaHoldoutPermille  = 200
	SGDEpochs                       = 50
	SGDLearningRate                 = 0.01
	TrainingLabelSelection          = "deterministic_hash_priority_unbiased_all_verbs"

	ResearchPermille    = 600
	ValidationPermille  = 200
	WalkForwardPermille = 200
	PartitionOrder      = "chronological_earliest_to_latest"
	PurgeEmbargoFormula = "max(profile.max_feature_lookback_hours, action_horizon_hours)"
	PurgeEmbargoRule    = "max(max_feature_lookback_hours, action_horizon_hours)"
	EntryPriceRule      = "next_bar_open_after_decision_close"
	ExitPriceRule       = "horizon_end_bar_open"
	PositionNotionalRule = "fixed_notional_usdt_frozen_at_entry_no_equity_rescale"

	OKX1HProfileID               = "okx_1h"
	OKX1HPrimaryFamily           = "okx_1h"
	OKX1HMaxFeatureLookbackHours = 24
	OKX5MProfileID               = "okx_5m"
	OKX5MPrimaryFamily           = "okx_5m"
	OKX5MMaxFeatureLookbackHours = 4
	OKX5MMaxActionHorizonHours   = 4

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

	ModelFamily       = "regularized_linear_pairwise_ranking_action_value"
	FeaturePolicyMode = "continuous_soft_sky_sorter_cross_sectional_ranks"
	PolicySizeRule    = "continuous_risk_budget_clipped_by_liquidity_capacity_and_leverage"

	ValidationApplyOn                             = "validation_partition_only"
	ValidationNoExam                              = true
	ValidationFrozenBeforeReadingResults          = true
	ValidationMinActiveDays                       = 20
	ValidationMaxDrawdownFraction                 = 0.30
	ValidationMaxSymbolPositiveContributionFraction = 0.35
	ValidationBaseNetReturnMustBePositive         = true
	ValidationAllDelayNetReturnMustBePositive       = true
	ValidationAllEquityTiersMustPass                = true
	ValidationNoLeverageSolvencyBreach              = true
	ValidationPassStatus                          = "VALIDATION_PASS"
	ValidationFailStatus                          = "VALIDATION_FAIL"
	ValidationInconclusiveStatus                  = "INCONCLUSIVE"
	ValidationSparseSampleAction                  = "INCONCLUSIVE"
	ValidationSolvencyFailReason                  = "SOLVENCY"
	ValidationMinActiveDaysInconclusiveNote         = "INCONCLUSIVE is not VALIDATION_PASS"

	WFPartition                         = "walk_forward"
	WFFoldCount                         = 5
	WFUsesProfilePurgeEmbargo           = true
	WFEligibility                       = "validation_pass_and_signed_hash_verified_only"
	WFSkippedValidationFailStatus       = "SKIPPED_VALIDATION_FAIL"
	WFPassStatus                        = "WF_PASS"
	WFFailStatus                        = "WF_FAIL"
	WFInconclusiveStatus                = "INCONCLUSIVE"
	WFFrozenPolicyNoRetuning            = true
	WFFoldNetReturnMustBePositive       = true
	WFWorstFoldNetReturnMustBePositive  = true
	WFLOFONetReturnMustBePositive       = true
	WFAllDelayNetReturnMustBePositive   = true
	WFCostStressNetReturnMustBePositive = true
	WFSignPValue                        = "exact_one_sided_binomial_sign_test_on_nonzero_daily_equity_delta"
	WFSignNumericMethod                 = "log_gamma_log_sum_exp"
	WFMultipleComparisonControl         = "Holm-Bonferroni"
	WFPolicyAttemptFamily               = "selected_policy_plus_ledger_attempts"
	WFFamilyAlpha                       = 0.01
	WFDailyReplayMatchesScenarioFunding = true

	MechanismUnexplainedPolicyPassStatus = "UNEXPLAINED_POLICY_PASS"
	MechanismTradeAllowed                = false
	MechanismExamEligible                = false
	MechanismCertified                   = false
	MechanismAutoFromFeaturesForbidden   = true
	MechanismForcedFlowEvidenceRequired  = true

	PendingFlushRule              = "execute_matured_before_scoring_discard_unexpired_at_eof"
	PendingExecuteIndexFormula    = "decision_bar_index + 1 + delay_bars"
	PendingStaleRevalidate        = true
	RiskGateAfterBreachRule       = "block_open_increase_reverse_pending_fail_closed_allow_trim_close_flat"
	ClusterPnLIncrementalRule     = "cluster_pnl_accumulates_symbol_pnl_bar_delta_not_lifetime"
	DailyReplayEOFTailRule        = "accrue_equity_delta_to_last_utc_day_and_symbol_pnl_increment_to_cluster_after_hold_funding_and_mtm"
	DailyReplayNoFinalDecisionRule = "skip_eof_tail_accrual_explicitly_when_no_prior_decision"
	ProposalCostFields            = "cost_estimate_bps,capacity_headroom,liquidity_score"
	ExecutionCostFormula          = "turnover_usdt * (roundtrip_taker_fee_bps + base_spread_slippage_floor_bps + cost_estimate_bps) * stress_multiplier / 10000"
	FundingOptionalNonblocking    = true
	FundingIncludedInCostEstimate = "conservative_proxy_when_funding_data_absent"
	FundingStressIncluded         = true

	ExamRequiredPaperCount = 5
	ExamStandardPaperCount = 4
	ExamGoldenPaperCount   = 1
	ConsumptionRule        = "unseal_grade_or_crash_permanently_consumes_paper"
	GoldenLastRule         = "exactly_one_golden_at_final_queue"

	ExamFixtureSchemaV1                         = "lingxi.routerg5.exam-fixture.v1"
	ExamFixtureMinBlocks                        = 1
	ExamExpectedCommitmentHashFormula           = "sha256(policy_hash\\nfeature_hash\\nexperiment_hash\\nprotocol_hash)"
	ExamMinActiveDays                           = 20
	ExamMaxDrawdownFraction                     = 0.30
	ExamMaxSymbolPositiveContributionFraction   = 0.35
	ExamCostStressMultiplier                    = 1.5
	ExamBaseNetReturnMustBePositive             = true
	ExamAllDelayNetReturnMustBePositive         = true
	ExamNoLeverageSolvencyBreach                = true
	ExamCostStressNetReturnMustBePositive       = true
	ExamClusterLOORequired                      = true
	ExamTopKRequired                            = true

	ExamBoundaryResearchPassStatus = "RESEARCH_PASS"
	ExamBoundaryNoGoStatus         = "NO_GO"
	ExamBoundaryAvailableStatus    = "AVAILABLE"
)

var ProposalCostFieldList = []string{"cost_estimate_bps", "capacity_headroom", "liquidity_score"}

var DataProfileIDs = []string{"okx_1h", "okx_5m"}

var OptionalNonblockingFamilies = []string{
	"okx_funding",
	"bybit_funding",
	"rubik_taker_1h",
	"rubik_lsratio_1h",
	"rubik_taker_5m",
}

var OKX1HOutcomeHorizonsHours = []int{1, 4, 24}

var OKX5MOutcomeHorizonsMinutes = []int{5, 15, 60, 240}

var ActionVerbs = []string{
	"flat",
	"open_long",
	"open_short",
	"hold",
	"increase",
	"trim",
	"close",
	"reverse",
}

var DelayBars = []int{0, 1, 2, 3}

var OracleSizeFractions = []float64{0.0, 0.1, 0.25, 0.5, 1.0}

var PolicySizeRange = []float64{0.0, 1.0}

var ValidationInitialEquities = []float64{30, 100, 1000}

var OracleReferenceCapitalUSDT = ValidationInitialEquities[len(ValidationInitialEquities)-1]

const OracleReferenceCapitalNote = "oracle/feature 1000U reference is conservative capacity upper bound for 30/100 validation tiers; not optimized for small capital"

var ValidationDelayBars = []int{0, 1, 2, 3}

var LambdaGrid = []float64{0.0001, 0.001, 0.01, 0.1, 1.0}

var PolicyHashChain = []string{"FeatureHash", "PolicyHash", "ExperimentHash"}

var ActionEncodingDirections = []string{"flat", "long", "short"}

var ActionEncodingStates = []string{"flat", "long", "short"}

var ActionEncodingNumericFields = []string{"size_fraction", "delay_bars", "horizon_index"}

var ActionEncodingInteractionRule = "context_continuous_x_action_verb_direction_start_state_numeric"

var FixedActionDetectionRule = "representative_context_and_start_state_argmax_uniqueness"

var DegeneratePolicyRejections = []string{
	"fixed_direction",
	"fixed_horizon",
	"fixed_verb",
	"hold_forever",
	"never_close",
	"always_reverse",
}

var EffectiveLeverageCheckRule = "mark_to_market_each_bar_sum_fixed_notional_over_equity"

var PendingNotionalRescaleRule = "execution_rescale_size_fraction_to_approved_notional_usdt"

var ExamReplayRequired = true

var ExamReplayGradesComputedOnly = true

var SkyOutputs = []string{"btc_return", "btc_vol", "btc_trend", "btc_drawdown", "confidence"}

var SorterOutputs = []string{"liquidity_score", "vol_cost_penalty", "capacity_headroom"}

var CrossSectionalRanks = []string{"return_rank", "vol_rank", "liquidity_rank", "beta_rank"}

var ForbiddenFeatureInputs = []string{
	"pnl",
	"outcome",
	"forward_return",
	"net_edge",
	"realized_pnl",
	"settlements",
	"sealed_exam",
	"post_decision_features",
	"current_open_bar",
	"current_universe_backfill",
	"reward_cube_label",
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
