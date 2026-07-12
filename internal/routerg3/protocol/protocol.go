// Package protocol 是 g3 分层路由预注册协议的"统一预注册与代码"强制点:与
// research/firstbattle/hierarchical_router_g3_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——protocol_test.go 解析 manifest 并断言与本文件
// 三方一致(节点顺序、叶子状态机、reason 码、司天监分桶、特征目录、发现预算、多重检验、失败标准、红队条数)。
//
// g3 新 generation、新 protocol hash;绑定但绝不修改 g1/g2 hash/result 与历史契约。
// 冻结协议后修改节点、司天监特征、成本、特征目录或候选定义都必须新 generation(新 protocol hash)。
package protocol

const (
	// ProtocolID / Generation 对应 manifest.protocol_id / manifest.generation。
	ProtocolID         = "firstbattle.hierarchical_router.g3"
	Generation         = "firstbattle-router-g3"
	RouterProtocolHash = "1628e1a575585a7a1c2b7da0e01eb54de9707997272a6301e3824505dece17fb"

	// 绑定但不篡改的被引用契约 hash(见 manifest.binds)。
	HistoryDataHash             = "f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c"
	HistoryRegimeContractHash   = "a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127"
	HistoryDataRequirementsHash = "07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7"

	// 绑定但不篡改的 g1 路由协议 hash 与零候选结论(见 manifest.closes_g1 / binds)。
	G1RouterProtocolHash   = "0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31"
	G1Generation           = "firstbattle-router-g1"
	G1CertifiedMoneyLeaves = 0
	G1ResearchPassLeaves   = 0
	G1SystemStatus         = "NO_GO"

	// 绑定但不篡改的 g2 路由协议 hash 与 162 组合 0 提名 NO_GO 结论(见 manifest.closes_g2 / binds)。
	G2RouterProtocolHash     = "395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec"
	G2Generation             = "firstbattle-router-g2"
	G2RegisteredSearchCombos = 162
	G2CandidateNominations   = 0
	G2ResearchPassLeaves     = 0
	G2SystemStatus           = "NO_GO"
	G2PrizeMechanism         = "funding_payer_flip_rebalance"

	// ExamActions 对应 manifest.time_partition_roles.exam_actions / audit.exam_actions。
	ExamActions = 0
)

// NodeOrder 是冻结的 g3 运行时节点顺序,与 manifest.node_order 逐字一致。
var NodeOrder = []string{
	"DataTruth",
	"SkyBureau",
	"Sorter",
	"MechanismFamily",
	"ConditionalLeaf",
}

// LeafStates 是冻结的叶子状态机取值集合,与 manifest.leaf_states 逐字一致。
var LeafStates = []string{
	"UNRESEARCHED",
	"RESEARCH_PASS",
	"EXAM_PASS",
	"SHADOW_PASS",
	"LIVE_APPROVED",
	"CLOSED",
	"UNTESTABLE",
}

// LeafStateOrder 是可晋级序,与 manifest.leaf_state_machine.order 一致。
var LeafStateOrder = []string{
	"UNRESEARCHED",
	"RESEARCH_PASS",
	"EXAM_PASS",
	"SHADOW_PASS",
	"LIVE_APPROVED",
}

// TerminalClosedStates 是不可复活的终止态,与 manifest.leaf_state_machine.terminal_closed 一致。
var TerminalClosedStates = []string{"CLOSED", "UNTESTABLE"}

// ReasonCodes 是冻结的 coarse reason 码全集,与 manifest.reason_codes 逐字一致。
var ReasonCodes = []string{
	"ROUTED_CANDIDATE",
	"SHADOW_CANDIDATE",
	"DATA_TRUTH_INVALID",
	"DATA_TRUTH_UNKNOWN",
	"SKY_OOD",
	"SKY_UNKNOWN",
	"SORTER_REJECTED",
	"MECHANISM_UNKNOWN",
	"MECHANISM_NO_PAYER",
	"CONDITIONAL_LEAF_AMBIGUOUS",
	"LEAF_DATA_MISSING",
	"LEAF_CLOSED",
	"NOT_CERTIFIED",
	"CONFLICT_NO_ARBITRATION",
	"PORTFOLIO_RISK_BLOCKED",
}

// CoarseTraceForbiddenSubstrings 是 trace 中绝不允许出现的泄漏子串(与 manifest.coarse_trace_rule)。
var CoarseTraceForbiddenSubstrings = []string{
	"net_edge",
	"pnl",
	"realized_usdt",
	"exam_id",
	"hidden_time_range",
	"threshold_values",
	"near_threshold_margins",
	"per_trade_returns",
}

// DataFamilies 是冻结的 7 族历史覆盖,与 manifest.data_families.families 逐字一致。
var DataFamilies = []string{
	"okx_1h",
	"okx_5m",
	"okx_funding",
	"bybit_funding",
	"rubik_taker_1h",
	"rubik_taker_5m",
	"rubik_lsratio_1h",
}

// OptionalNonblockingDataFamilies 保留 inventory 但不阻塞本代；不得进入 FeatureCatalog 或生成规则。
var OptionalNonblockingDataFamilies = []string{"rubik_taker_5m"}

// RuleGenerationExcludedDataFamilies 是本代明确不得生成规则的数据族。
var RuleGenerationExcludedDataFamilies = []string{"rubik_taker_5m"}

// FeatureCatalog 是冻结的本代可派生规则决策前特征目录(不含 outcome),与 manifest.feature_catalog 展平后的 name 一致。
var FeatureCatalog = []string{
	"symbol_return_1h",
	"symbol_return_4h",
	"symbol_return_24h",
	"symbol_realized_vol_1h",
	"symbol_realized_vol_4h",
	"symbol_realized_vol_24h",
	"okx_bybit_funding_spread",
	"okx_bybit_funding_sign_divergence",
	"rubik_taker_imbalance_1h",
	"rubik_lsratio_1h",
	"quote_volume_24h_log_bucket",
	"volatility_cost_ratio",
}

// FeatureDerivationOrder 冻结 v3 机制优先顺序；先四个可提供 payer 的机制特征，再进入 conditioner。
var FeatureDerivationOrder = []string{
	"okx_bybit_funding_spread",
	"okx_bybit_funding_sign_divergence",
	"rubik_taker_imbalance_1h",
	"rubik_lsratio_1h",
	"symbol_return_1h",
	"symbol_return_4h",
	"symbol_return_24h",
	"symbol_realized_vol_1h",
	"symbol_realized_vol_4h",
	"symbol_realized_vol_24h",
	"quote_volume_24h_log_bucket",
	"volatility_cost_ratio",
}

var CoarseSkyAxisLabels = []string{"low", "mid", "high"}
var CoarseSkyWildcardAxes = []string{"te", "dd"}
var G3SorterDomains = []string{"liquid"}

const MaxCoarseObservedGroups = 162

// MechanismAttribution 冻结 mechanism feature 到经济付款机制的逐字段归因。
type MechanismAttribution struct {
	Family              string
	Features            []string
	Mechanism           string
	Payer               string
	ForcedBehavior      string
	CannotWaitReason    string
	CompetitionDefense  string
	DeadlineTemplate    string
	InvalidatorTemplate []string
	DistinctFromG1G2    string
}

const FeatureMechanismAttributionVersion = "g3-feature-mechanism-attribution-v1"

// MechanismAttributions 顺序与 manifest.feature_to_economic_mechanism_attribution.mechanism_families 一致。
var MechanismAttributions = []MechanismAttribution{
	{
		Family:             "cross_venue_funding_pressure",
		Features:           []string{"okx_bybit_funding_spread", "okx_bybit_funding_sign_divergence"},
		Mechanism:          "cross_venue_funding_pressure_rebalance",
		Payer:              "cross_venue_carry_holders",
		ForcedBehavior:     "venue_carry_or_margin_rebalance",
		CannotWaitReason:   "funding_settlement",
		CompetitionDefense: "fragmented_venue_capital_and_capacity",
		DeadlineTemplate:   "next_relevant_cross_venue_funding_settlement_at_or_before_decision_time_plus_outcome_horizon",
		InvalidatorTemplate: []string{
			"cross_venue_funding_spread_converged",
			"cross_venue_funding_sign_divergence_resolved",
			"required_venue_unavailable_or_ood",
		},
		DistinctFromG1G2: "simultaneous_cross_venue_spread_or_sign_divergence; not g1 single_venue funding_post_settlement_release/receiver_release and not g2 adjacent funding_sign_flip/funding_payer_flip_rebalance",
	},
	{
		Family:             "urgent_taker_impact",
		Features:           []string{"rubik_taker_imbalance_1h"},
		Mechanism:          "urgent_taker_impact_reversion",
		Payer:              "urgent_aggressive_takers",
		ForcedBehavior:     "execution_urgency_creates_temporary_impact_then_inventory_absorption_or_reversion",
		CannotWaitReason:   "execution_urgency",
		CompetitionDefense: "temporary_impact_and_limited_capacity",
		DeadlineTemplate:   "decision_time_plus_min(outcome_horizon,4h)",
		InvalidatorTemplate: []string{
			"taker_imbalance_normalized_before_entry",
			"same_side_aggressive_flow_reaccelerated",
			"liquidity_or_capacity_ood",
		},
		DistinctFromG1G2: "not funding_post_settlement_release, funding_payer_flip_rebalance, or panic_liquidation",
	},
	{
		Family:             "crowded_leverage",
		Features:           []string{"rubik_lsratio_1h"},
		Mechanism:          "crowded_leverage_rebalance",
		Payer:              "crowded_leveraged_side",
		ForcedBehavior:     "margin_or_carry_pressure_driven_deleveraging_or_rebalance",
		CannotWaitReason:   "margin_or_carry_pressure",
		CompetitionDefense: "fragmented_positioning_and_limited_balance_sheet_capacity",
		DeadlineTemplate:   "decision_time_plus_min(outcome_horizon,24h)",
		InvalidatorTemplate: []string{
			"long_short_ratio_no_longer_crowded",
			"margin_or_carry_pressure_dissipated",
			"liquidity_or_capacity_ood",
		},
		DistinctFromG1G2: "not funding_post_settlement_release, funding_payer_flip_rebalance, or panic_liquidation",
	},
}

// ConditionerFeatures 只能限制支持域，不能独立提供 payer 或 mechanism。
var ConditionerFeatures = []string{
	"symbol_return_1h",
	"symbol_return_4h",
	"symbol_return_24h",
	"symbol_realized_vol_1h",
	"symbol_realized_vol_4h",
	"symbol_realized_vol_24h",
	"quote_volume_24h_log_bucket",
	"volatility_cost_ratio",
}

const (
	ConditionerRule                      = "conditioners_may_restrict_support_but_cannot_supply_payer_or_mechanism"
	ZeroMechanismFeaturesResolution      = "NARRATIVE_ONLY/CLOSED"
	OneMechanismFamilyResolution         = "ATTRIBUTE_TO_THAT_FAMILY"
	SameMechanismFamilyResolution        = "ATTRIBUTE_TO_THAT_FAMILY"
	DifferentMechanismFamiliesResolution = "AMBIGUOUS/CLOSED"
	ConditionerPlusMechanismResolution   = "ATTRIBUTE_TO_THAT_FAMILY"
	ClosedRulesStillChargeAlpha          = true
	MechanismGraveyardRule               = "exact id, alias, renamed variant, threshold variant, sign/flip restatement, or semantic equivalent is CLOSED and cannot be attributed to a G3 mechanism"
)

// G1G2MechanismGraveyard 冻结不可复活、改名或换皮的历史机制。
var G1G2MechanismGraveyard = []string{
	"funding_post_settlement_release",
	"negative_funding_receiver_release",
	"positive_funding_receiver_release",
	"panic_liquidation",
	"funding_payer_flip_rebalance",
}

// ForbiddenFeatureInputs 对应 manifest.feature_catalog.forbidden_feature_inputs。
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
}

// 司天监分桶(与 manifest.sky)——只用已收盘 bar、按 reverse-induction 分布拟合、不读收益。
const (
	SkyBinning       = "research_quantiles"
	SkyFitOn         = "reverse_induction_partition_only"
	SkyMissingAction = "OOD_NO_TRADE"
)

// SkyTimescalesHours 对应 manifest.sky.timescales_hours。
var SkyTimescalesHours = []int{1, 4, 24}

// SkyQuantiles 对应 manifest.sky.quantiles(两切点 -> 三桶)。
var SkyQuantiles = []float64{0.3333333333, 0.6666666667}

// SkyDimensions 是冻结的司天监维度名,与 manifest.sky.dimensions[].name 逐字一致。
var SkyDimensions = []string{
	"btc_return_24h",
	"btc_trend_efficiency_24h",
	"btc_realized_volatility_24h",
	"btc_drawdown_168h",
	"okx_btc_funding_state",
}

// 特征规则阈值(与 manifest.feature_catalog)。
const (
	MaxFeaturesPerRule       = 2
	MinFeaturesPerRule       = 1
	ThresholdSource          = "reverse_induction_partition_quantiles"
	Rubik1hMaxStalenessHours = 2
	Rubik1hStaleAction       = "MISSING"
)

// ThresholdQuantiles 对应 manifest.feature_catalog.threshold_quantiles。
var ThresholdQuantiles = []float64{0.25, 0.5, 0.75}

// 发现预算(与 manifest.discovery_budget)。
const (
	OutcomeHorizonDirectionSymmetry = "long_short_symmetric"
	DiscoveryCostBPS                = 12.0
	NeutralBandBPS                  = 3.0

	ForwardMaxFamilies            = 8
	ForwardMaxCandidatesPerFamily = 4

	ReverseInductionMaxFiles         = 512
	ReverseInductionMaxLines         = 5000000
	MaxSingleFeatureRules            = 162
	MaxDualFeatureRules              = 30
	MaxDerivedSearchRules            = 192
	ReverseInductionSearchSpaceID    = "firstbattle.g3.outcome_first_reverse_induction.v1"
	DerivationAlgorithmVersion       = "g3-outcome-first-derivation-v3-coarse-round-robin"
	DerivationAlgorithmConfigHash    = "e893269b83ee204bca6b2bb58bba0fec6147827dd4413cea361fe3151fa7fc69"
	ReverseInductionMaxCandidates    = 32
	ReverseInductionProgressEvery    = 100000
	DerivationInductionGrouping      = "coarse_sky_pattern(ret,rv,fund,te=*,dd=*)+sorter_domain+horizon+direction"
	DerivationMaxGroupsFormula       = "3^3_sky_axes * 1_sorter_domain * 3_horizons * 2_directions = 162"
	DerivationObservedGroupRule      = "enumerate_only_coarse_groups_with_at_least_one_induction_event_in_frozen_group_order"
	DerivationGroupOrder             = "lexicographic(ret,rv,fund,sorter_domain,horizon,direction)"
	DerivationRuleFires              = "full_sky_state_matches_coarse_pattern_with_te_dd_wildcards_and_exact_sorter_domain_horizon_direction"
	DerivationControlSkyMatch        = "controls_must_match_same_full_sky_state_even_when_rule_fires_on_coarse_pattern"
	DerivationFeatureOrderRule       = "feature_derivation_order_mechanism_first_then_conditioners"
	DerivationSingleRuleOrder        = "feature_then_quantile_then_operator_then_observed_group_round_robin_with_group_innermost"
	DerivationSingleCoverageRule     = "within_each_feature_quantile_operator_round_each_observed_group_receives_at_most_one_generated_attempt_before_next_round"
	DerivationSingleStageExit        = "stop_single_when_all_observed_groups_have_at_least_one_registered_single_rule_or_single_registered_count=162_or_single_stream_exhausted"
	DerivationDualEntryRule          = "enter_dual_only_after_single_stage_exit; dual_budget=min(30,192-total_registered_count)"
	DerivationDualRuleOrder          = "lexicographic_feature_pair_then_quantile_pair_then_operator_pair_then_observed_group_round_robin_with_group_innermost"
	DerivationDualCoverageRule       = "within_each_feature_pair_quantile_pair_operator_pair_round_each_observed_group_receives_at_most_one_generated_attempt_before_next_round"
	DerivationExecutionModel         = "bounded_streaming_one_attempt_at_a_time"
	DerivationAttemptPipeline        = "generate_next_in_frozen_order_then_canonicalize_then_dedupe_then_validate_then_attribute_then_append_ledger_then_register_if_valid"
	DerivationDedupeRule             = "compare canonical hash against retained registered hashes plus current_group attempt hashes; duplicate_is_ledgered_not_registered; discard nonregistered current_group hashes at group boundary"
	DerivationSelectionOrder         = "single_observed_group_coverage_before_dual_then_frozen_round_robin_orders"
	DerivationTruncationRule         = "single_cap=162; dual_cap=30; total_cap=192; stop_before_requesting_next_item_when_active_cap_or_total_cap_reached; never outcome_rerank"
	DerivationSingleStopRule         = "all_observed_groups_covered_by_registered_single_rule_or_single_registered_count_reaches_162_or_single_stream_exhausted"
	DerivationDualStopRule           = "dual_registered_count_reaches_30_or_total_registered_count_reaches_192_or_dual_stream_exhausted"
	DerivationNotConsideredRule      = "cartesian_items_after_a_stop_condition_are_not_considered_and_have_no_ledger_entry_or_alpha_charge"
	DerivationCandidateMemoryBound   = "O(max_derived_search_rules + current_group)"
	DerivationMaterializationRule    = "must_not_materialize_all_candidates_or_copy_full_cartesian_provenance"
	DerivationStreamingStateRule     = "retain_registered_rules_and_their_hashes_up_to_caps_plus_current_group attempt hashes only"
	DerivationProgressRule           = "emit_progress_at_default_progress_every_generated_attempts_and_each_round_boundary"
	DerivationCheckpointRule         = "checkpoint_after_each_round_and_progress_interval_with_next_enumerator_cursor,stage,observed_groups,covered_groups,per_group_registered_rule_counts,status_counts,registered_counts,bounded_registered_and_current_group_dedupe_hashes,ledger_hash"
	ReverseInductionMatchRule        = "same_sky_state_symbol_liquidity_direction_horizon_cluster"
	ReverseInductionControlSelection = "outcome_blind: within same sky_state,sorter_domain,symbol,liquidity,direction,horizon,cluster choose one-to-one nontrigger events in deterministic time_then_event_id order; never inspect winner/loss/neutral outcome"
	ControlsNontriggerOutcomeBlind   = true
	WinnerOnlyRequiresNontrigger     = true
	ReverseInductionVolatilityTol    = 0.0
	ReverseInductionLiquidityTol     = 0.0
	ReverseInductionPermutationDraws = 100000
	ReverseInductionBootstrapSamples = 1000
	ReverseInductionSeed             = int64(20260713)
	ReverseInductionExactPermMax     = 20
	ReverseInductionMatchWindowMs    = int64(604800000)
	ReverseInductionNeutralBandBPS   = 3.0
	G3SmokeMaxSymbols                = 20
	G3SmokeMaxFundingLines           = 500
	G3SmokeMaxPriceLines             = 25000

	ReverseInductionPermille = 400
	RecheckPermille          = 200
	ForwardPermille          = 200
	WalkForwardPermille      = 200
	PartitionEmbargoHours    = 168
	MinInductionClusters     = 10
	SorterMinQuoteVolume     = 500000.0
	SorterMinVolatilityCost  = 2.0
	G3MinForwardSamples      = 30
	G3MinForwardClusters     = 10
	G3MinRecheckSamples      = 20
	G3MinRecheckClusters     = 10
	G3WalkForwardFolds       = 5
	G3WFMinSamples           = 30
	G3WFMinClusters          = 10
	G3PurgeHours             = 168
	G3WFEmbargoHours         = 168

	MaxTotalCandidates = 64
)

// Alpha ledger 只记录实际流式生成并检查过的 attempt；未请求的笛卡尔后缀是 not_considered。
const (
	AlphaLedgerEntryOrder     = "bounded_streaming_generation_order"
	AlphaLedgerChargeRule     = "registered=one_test_charge; duplicate_or_invalid=zero_because_not_outcome_tested; not_considered=absent"
	AlphaLedgerCountSource    = "later_sealed_search_manifest"
	AlphaLedgerProvenanceRule = "store_only_per_attempt_ledger_fields_and_running_hash; never_copy_full_cartesian_provenance"
)

var AlphaLedgerStatuses = []string{"duplicate", "invalid", "registered"}

var AlphaLedgerMustRecord = []string{
	"attempt_ordinal",
	"enumerator_cursor",
	"canonical_rule_hash",
	"derivation_algorithm_config_hash",
	"status",
	"status_reason",
	"alpha_stage",
	"alpha_charge",
}

var DerivationStageReportingFields = []string{
	"stage",
	"observed_group_count",
	"covered_group_count",
	"per_group_registered_rule_counts",
	"single_registered_count",
	"dual_registered_count",
	"total_registered_count",
}

// OutcomeHorizonsHours 对应 manifest.discovery_budget.outcome_horizons_hours(1h/4h/24h 双向)。
var OutcomeHorizonsHours = []int{1, 4, 24}

// 多重检验预算——与 manifest.multiple_comparison_budget 一致。
const (
	FamilyWiseControl         = "Holm-Bonferroni"
	FamilyWiseAlpha           = 0.01
	ReverseInductionAlpha     = 0.004
	RecheckAlpha              = 0.002
	ForwardAlpha              = 0.002
	WalkForwardAlpha          = 0.002
	MaxDerivedSearchRulesTest = 192 // == MaxDerivedSearchRules;实际派生后每条规则都是一个检验
)

// RedTeamRequirementCount 对应 manifest.red_team.requirements 条数(g3 路由红队 21 项)。
const RedTeamRequirementCount = 21
