// Package protocol 是 g4 净利润机会地图预注册协议的"统一预注册与代码"强制点:与
// research/firstbattle/hierarchical_router_g4_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——protocol_test.go 解析 manifest 并断言与本文件
// 三方一致(数据 profile、成本模型、地图维度、发现预算、validation 门、机制归因、严格 WF、红队条数)。
//
// g4 新 generation、新 protocol hash;绑定但绝不修改 g1/g2/g3 hash/result 与历史契约。
// 本阶段不预先冻结具体方向策略；只追加两个最小机制模板与严格 WF 认证政策。
package protocol

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"lingxi/internal/datatruth"
)

const (
	ProtocolID         = "firstbattle.hierarchical_router.g4"
	Generation         = "firstbattle-router-g4"
	RouterProtocolHash = "55d5da85272d9d326c81570a677b47b66ede7da69b14fc58fe4c154c0a1d6425"

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

	ExamActions = 0
)

var DataProfileIDs = []string{"okx_1h", "okx_5m"}

var CompatiblePriorProfitMapProtocolHashes = []string{
	"d13883e328f2d7a4d618f20fa77c2c5b66ac46220210dfd5dc17c249ccb6ab91",
}

const ProfitMapCompatibilityScope = "map artifacts only; candidate, validation, mechanism and certification outputs must use current G4 protocol"

func ProfitMapProtocolCompatible(hash string) bool {
	return hash == RouterProtocolHash || slices.Contains(CompatiblePriorProfitMapProtocolHashes, hash)
}

var OptionalNonblockingFamilies = []string{
	"okx_funding",
	"bybit_funding",
	"rubik_taker_1h",
	"rubik_lsratio_1h",
	"rubik_taker_5m",
}

const (
	OKX1HProfileID               = "okx_1h"
	OKX1HPrimaryFamily           = "okx_1h"
	OKX1HMaxFeatureLookbackHours = 24
	OKX5MProfileID               = "okx_5m"
	OKX5MPrimaryFamily           = "okx_5m"
	OKX5MMaxFeatureLookbackHours = 4
	PurgeEmbargoRule             = "max(max_feature_lookback_hours, outcome_horizon_hours)"
	EntryPriceRule               = "next_bar_open_after_decision_close"
	ExitPriceRule                = "horizon_end_bar_open"
)

var OKX1HOutcomeHorizonsHours = []int{1, 4, 24}

var OKX5MOutcomeHorizonsMinutes = []int{5, 15, 60, 240}

// OKX5MMaxOutcomeHorizonHours is the largest okx_5m profile hold (4h) for purge/embargo.
const OKX5MMaxOutcomeHorizonHours = 4

const (
	DiscoveryPermille   = 600
	ValidationPermille  = 200
	WalkForwardPermille = 200
	PartitionOrder      = "chronological_earliest_to_latest"
	PurgeEmbargoFormula = "max(profile.max_feature_lookback_hours, outcome_horizon_hours)"
)

var CapitalTiersUSDT = []int{30, 100, 1000}

var ReturnFields = []string{"gross", "fee_net", "conservative_net"}

const (
	DirectionSymmetry = "long_short_symmetric"
	NeutralBandBPS    = 3.0

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
)

var ProfitMapDimensions = []string{
	"sky_coarse_market",
	"liquidity_bucket",
	"symbol_class",
	"horizon",
	"direction",
	"capital_tier",
}

var CoarseSkyAxisLabels = []string{"low", "mid", "high"}
var FundingCoarseAxisLabels = []string{"low", "mid", "high", "unknown"}
var CoarseSkyWildcardAxes = []string{"te", "dd"}

const FundingMissingAction = "unknown_nonblocking"

var ProfitMapAggregates = []string{
	"opportunity_count",
	"gross_bps_mean",
	"fee_net_bps_mean",
	"conservative_net_bps_mean",
	"gross_positive_rate",
	"fee_net_positive_rate",
	"conservative_net_positive_rate",
	"cost_wipeout_rate",
}

const (
	ScanMaxFilesRequiredCLI           = true
	ScanMaxLinesRequiredCLI           = true
	ScanCatalogSymbolLimitRequiredCLI = true
	DefaultCatalogSymbolLimit         = 0
	ScanCheckpointRequired            = true
	ScanStreamingOutputRequired       = true
	ScanProgressEveryLines            = 100000
	ScanCheckpointEverySymbols        = 25
	MapQuantileReservoirPerCell       = 32
	MapWinnerReservoirPerCell         = 32
	LiquidityBucketRule               = "floor_log10_average_quote_volume_24h_clamp_0_9"
	SymbolClassRule                   = "discovery_partition_volume_tertile_major_mid_long_tail"
	FullMarketScope                   = "FULL_MARKET"
	TruncatedSmokeScope               = "TRUNCATED_SMOKE"
	TruncatedFormalPathPolicy         = "TRUNCATED_SMOKE forbidden from candidate validation, mechanism attribution, strict WF, exam, or certified leaf"
)

var FullMarketRequiredSummaryFields = []string{
	"Available",
	"Selected",
	"Truncated",
	"FullMarket",
	"Scope",
}

const (
	MaxFeaturesPerRule      = 2
	MinFeaturesPerRule      = 1
	ThresholdSource         = "discovery_partition_quantiles"
	FeatureDecisionTimeOnly = true

	SymbolRealizedVolFormula           = "sqrt(mean(squared_simple_returns))*10000"
	SymbolRealizedVolMinBars           = 1
	VolatilityCostRatioFormula         = "symbol_realized_vol_rms_bps / roundtrip_taker_fee_bps"
	VolatilityCostRatioVolHorizonOKX1H = "1h"
	VolatilityCostRatioVolHorizonOKX5M = "5m"
)

var ThresholdQuantiles = []float64{0.25, 0.5, 0.75}

var SharedFeatures = []string{
	"quote_volume_24h_log_bucket",
	"volatility_cost_ratio",
}

// AttributionFeatures may be enumerated in discovery but membership can only
// nominate a hypothesis; it can never prove a mechanism or payer.
var AttributionFeatures = []string{
	"btc_relative_return",
	"btc_beta_proxy",
	"time_of_day_session",
	"profile_quote_volume_24h_log_bucket",
}

const (
	AttributionFeaturesDiscoveryEligible      = true
	AttributionFeatureMembershipNeverExplains = true
)

var OKX1HProfileFeatures = []string{
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
}

var OKX5MProfileFeatures = []string{
	"symbol_return_5m",
	"symbol_return_15m",
	"symbol_return_1h",
	"symbol_return_4h",
	"symbol_realized_vol_5m",
	"symbol_realized_vol_15m",
	"symbol_realized_vol_1h",
	"symbol_realized_vol_4h",
}

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

const (
	MaxTotalCandidates           = 1024
	MaxSingleFeatureRules        = 900
	MaxDualFeatureRules          = 124
	ReverseMineSearchSpaceID     = "firstbattle.g4.profit_map_reverse_mine.v1"
	DerivationAlgorithmVersion   = "g4-reverse-mine-feature-cell-round-robin-v2"
	DiscoveryExecutionModel      = "bounded_streaming_one_attempt_at_a_time"
	DiscoveryAttemptPipeline     = "generate_next_in_frozen_order_then_canonicalize_then_dedupe_then_validate_then_append_ledger_then_register_if_valid"
	FreezeBeforeValidation       = true
	PayerRequiredAtDiscovery     = false
	UnexplainedCandidateAllowed  = true
	UnexplainedStatus            = "UNEXPLAINED_CANDIDATE"
	DiscoveryCheckpointRequired  = true
	DiscoveryProgressEvery       = 100000
	DiscoveryEnumerationOrder    = "allocation_round_then_feature_slot_then_profile_then_next_fair_cell"
	DiscoveryFeatureFairnessRule = "within each profile every available feature receives registered allocation N before any feature receives registered allocation N+1"
	DiscoveryCellFairnessRule    = "next cell is round-robin across profile/horizon/direction/capital_tier/cell strata; no feature-major prefix exhaustion"
)

var DiscoveryFairnessDimensions = []string{"feature", "cell"}

var DiscoveryCellStrata = []string{"profile", "horizon", "direction", "capital_tier", "cell"}

var DiscoveryFairnessCheckpointFields = []string{
	"allocation_round",
	"feature_slot",
	"profile_cursor",
	"per_profile_feature_registered_counts",
	"per_stratum_cell_cursor",
}

var DiscoveryOperatorOrder = []string{"lte_quantile", "gte_quantile"}

var DiscoveryLedgerStatuses = []string{"duplicate", "invalid", "registered", "unexplained"}

var DiscoveryLedgerMustRecord = []string{
	"attempt_ordinal",
	"enumerator_cursor",
	"canonical_rule_hash",
	"profile_id",
	"status",
	"status_reason",
	"ledger_stage",
}

const (
	ValidationApplyOn                            = "validation_partition_only"
	ValidationFrozenBeforeReadingResults         = true
	ValidationDiscoveryScreenMethod              = "deterministic_gates_then_bh_fdr"
	ValidationNoFinalHolm                        = true
	ValidationStrictCertificationDeferred        = true
	ValidationMinOpportunities                   = 20
	ValidationMinUniqueDays                      = 10
	ValidationMinUniqueSymbols                   = 3
	ValidationMinFeeNetMeanBPS                   = 0.0
	ValidationMinConservativeNetMeanBPS          = 0.0
	ValidationMaxSingleEventContributionFraction = 0.25
	ValidationMaxDayPositiveContributionFraction = 0.35
	ValidationDelayPlusOneBarRequired            = true
	ValidationCostStressMultiplier               = 1.5
	ValidationCostStressMustRemainNonNegative    = true
	ValidationSparseSampleAction                 = "INCONCLUSIVE"
	ValidationPassStatus                         = "VALIDATION_PASS"
	ValidationFailStatus                         = "VALIDATION_FAIL"

	ValidationMultipleComparisonControl    = "Benjamini-Hochberg-FDR"
	ValidationFDRCandidateFamily           = "all_frozen_candidates_on_validation_partition"
	ValidationFDRQ                         = 0.10
	ValidationMinIndependentBlocks         = 8
	ValidationBlockUnit                    = "utc_anchored_nonoverlap_block"
	ValidationObservationWindowHours       = 24
	ValidationEmbargoGap                   = "candidate_horizon"
	ValidationHorizonIndependence          = "construct_and_test_each_candidate_horizon_separately"
	ValidationOverlapRule                  = "next_observation_start >= previous_observation_end + candidate_horizon"
	ValidationSignPValue                   = "exact_one_sided_binomial_sign_test_positive_on_nonzero_block_conservative_net"
	ValidationSignNumericMethod            = "log_gamma_log_sum_exp"
	ValidationIntegerCombinationsForbidden = true
)

const PhenomenonIDIgnoreCapitalTier = true

var PhenomenonIDKeyDimensions = []string{
	"profile_id",
	"sky_coarse_market",
	"horizon",
	"direction",
	"conditions",
}

var PhenomenonIDSubpathDimensions = []string{
	"liquidity_bucket",
	"symbol_class",
}

var FeatureCatalogSummaryFields = []string{
	"declared_distinct_feature_names",
	"available_profile_feature_slots",
	"covered_feature_names",
	"missing_declared_feature_names",
}

var G1G2G3MechanismGraveyard = []string{
	"funding_post_settlement_release",
	"negative_funding_receiver_release",
	"positive_funding_receiver_release",
	"panic_liquidation",
	"funding_payer_flip_rebalance",
	"cross_venue_funding_pressure_rebalance",
	"urgent_taker_impact_reversion",
	"crowded_leverage_rebalance",
}

const (
	AttributionVersion = "g4-minimal-mechanism-attribution-v2-hypothesis-only"

	AttributionExplained           = "EXPLAINED"
	AttributionMechanismHypothesis = "MECHANISM_HYPOTHESIS"
	AttributionUnexplained         = "UNEXPLAINED"
	AttributionAmbiguous           = "AMBIGUOUS"
	AttributionGraveyard           = "GRAVEYARD"
	FeatureMembershipAutoExplained = false
	G4IndependentEvidenceAvailable = false
	AutomaticExplainedCount        = 0

	CrossAssetTemplateID      = "cross_asset_beta_rebalance_lag"
	CrossAssetMechanism       = "cross_asset_beta_rebalance_lag"
	CrossAssetPayer           = "delayed_beta_hedgers"
	CrossAssetForcedBehavior  = "benchmark_or_beta_rebalance_deadline"
	CrossAssetDeadline        = "benchmark_or_beta_rebalance_deadline"
	CrossAssetCapacityDefense = "fragmented_alt_liquidity"
	SessionTemplateID         = "scheduled_session_liquidity_transfer"
	SessionMechanism          = "scheduled_session_liquidity_transfer"
	SessionPayer              = "time_constrained_session_flow"
	SessionForcedBehavior     = "session_execution_window"
	SessionDeadline           = "session_execution_window_end"
	SessionCapacityDefense    = "not_preregistered"
	CrossTemplateResolution   = AttributionAmbiguous
	NarrativeRejectionRule    = "feature conjunction may nominate only MECHANISM_HYPOTHESIS; names, prose, correlation, price/vol/liquidity, attribution-feature membership, or G1-G3 graveyard feature families cannot establish EXPLAINED payer or mechanism"

	UnexplainedResearchPassStatus   = "UNEXPLAINED_RESEARCH_PASS"
	HypothesisResearchResultStatus  = "MECHANISM_HYPOTHESIS_RESEARCH_RESULT"
	UnexplainedResearchPassPolicy   = "strictly_no_trade_no_exam_no_certified_leaf"
	HypothesisResearchResultPolicy  = "strictly_no_trade_no_exam_no_certified_leaf"
	AmbiguousPostValidationPolicy   = "CLOSED"
	GraveyardPostValidationPolicy   = "CLOSED"
	StrictWFEligibility             = "validation_pass_and_future_independent_evidence_status_EXPLAINED_only"
	CurrentAutomaticStrictWFEntries = 0

	StrictWFPartition                          = "walk_forward"
	StrictWFUsesProfilePurgeEmbargo            = true
	StrictWFMinFirings                         = 30
	StrictWFMinUniqueDays                      = 10
	StrictWFMinUniqueSymbols                   = 5
	StrictWFMinIndependentBlocks               = 10
	StrictWFMeanComparison                     = "strictly_greater_than_zero"
	StrictWFMaxSingleEventContributionFraction = 0.25
	StrictWFMaxCapacityFailFraction            = 0.5
	StrictWFCapacityFailComparison             = "strictly_less_than"
	StrictWFDayBlockUnit                       = "utc_anchored_nonoverlap_block"
	StrictWFObservationWindowHours             = 24
	StrictWFEmbargoGap                         = "candidate_horizon"
	StrictWFHorizonIndependence                = "construct_and_test_each_candidate_horizon_separately"
	StrictWFOverlapRule                        = "next_observation_start >= previous_observation_end + candidate_horizon"
	StrictWFSignPValue                         = "exact_two_sided_binomial_sign_test_on_nonzero_block_conservative_net"
	StrictWFSignNumericMethod                  = "log_gamma_log_sum_exp"
	StrictWFIntegerCombinationsForbidden       = true
	StrictWFMultipleComparisonControl          = "Holm-Bonferroni"
	StrictWFFamily                             = "all_explained_validation_pass_candidates_entering_strict_wf"
	StrictWFFamilyAlpha                        = 0.01
	StrictWFPassState                          = "RESEARCH_PASS"
	StrictWFGateFailureState                   = "CLOSED"
	StrictWFInsufficientSampleState            = "INCONCLUSIVE"
	StrictWFResearchPassPolicy                 = "research_only_no_trade_no_exam_no_certified_money_leaf"

	RedTeamRequirementCount = 26
)

var AttributionStatuses = []string{
	AttributionExplained,
	AttributionMechanismHypothesis,
	AttributionUnexplained,
	AttributionAmbiguous,
	AttributionGraveyard,
}

var AttributionEvaluationOrder = []string{
	"claimed_mechanism_in_graveyard=>GRAVEYARD",
	"contains_g1_g2_g3_graveyard_feature_family=>UNEXPLAINED",
	"matches_both_templates=>AMBIGUOUS",
	"matches_exactly_one_template_and_claim_is_empty_or_equal=>MECHANISM_HYPOTHESIS",
	"future_independent_evidence_satisfies_contract=>EXPLAINED",
	"otherwise=>UNEXPLAINED",
}

var IndependentEvidenceMustInclude = []string{
	"independent_forced_flow_observation",
	"identifiable_real_counterparty_or_payer_evidence",
	"evidence_time_and_source_binding",
}

var IndependentEvidenceForbiddenSources = []string{
	"candidate_feature_membership",
	"candidate_outcome",
	"validation_result",
	"mechanism_name_or_narrative",
}

const IndependentEvidenceFutureBinding = "new immutable evidence artifact and new protocol generation required"

var GraveyardFeatureFamilies = []string{
	"okx_bybit_funding_spread",
	"okx_bybit_funding_sign_divergence",
	"rubik_taker_imbalance_1h",
	"rubik_lsratio_1h",
}

var UnexplainedFeatureClasses = []string{
	"price_return",
	"realized_volatility",
	"liquidity_only",
	"g1_g2_g3_graveyard_feature_family",
}

// MechanismTemplate is a minimal, mechanically matchable attribution template.
type MechanismTemplate struct {
	ID                  string
	RequiredAllFeatures []string
	RequiredAnyFeatures []string
	Mechanism           string
	Payer               string
	ForcedBehavior      string
	Deadline            string
	CapacityDefense     string
	MatchStatus         string
	Trade               bool
	Exam                bool
	CertifiedLeaf       bool
}

var MechanismTemplates = []MechanismTemplate{
	{
		ID:                  CrossAssetTemplateID,
		RequiredAllFeatures: []string{"btc_relative_return", "btc_beta_proxy"},
		RequiredAnyFeatures: []string{},
		Mechanism:           CrossAssetMechanism,
		Payer:               CrossAssetPayer,
		ForcedBehavior:      CrossAssetForcedBehavior,
		Deadline:            CrossAssetDeadline,
		CapacityDefense:     CrossAssetCapacityDefense,
		MatchStatus:         AttributionMechanismHypothesis,
	},
	{
		ID:                  SessionTemplateID,
		RequiredAllFeatures: []string{"time_of_day_session"},
		RequiredAnyFeatures: []string{"quote_volume_24h_log_bucket", "profile_quote_volume_24h_log_bucket"},
		Mechanism:           SessionMechanism,
		Payer:               SessionPayer,
		ForcedBehavior:      SessionForcedBehavior,
		Deadline:            SessionDeadline,
		CapacityDefense:     SessionCapacityDefense,
		MatchStatus:         AttributionMechanismHypothesis,
	},
}

var StrictWFPositiveMeanFields = []string{
	"fee_net_bps",
	"conservative_net_bps",
	"delay_plus_one_bar_conservative_net_bps",
	"cost_stress_conservative_net_bps",
}

var StrictWFCertificationStates = []string{
	StrictWFPassState,
	StrictWFGateFailureState,
	StrictWFInsufficientSampleState,
}

// ClassifyAttribution is the G4 automatic path. Feature membership can produce
// only MECHANISM_HYPOTHESIS; automatic EXPLAINED is closed in this generation.
func ClassifyAttribution(features []string, claimedMechanism string) string {
	return ClassifyAttributionWithIndependentEvidence(features, claimedMechanism, false)
}

// ClassifyAttributionWithIndependentEvidence reserves EXPLAINED for a future
// generation that binds independent forced-flow and real-counterparty evidence.
func ClassifyAttributionWithIndependentEvidence(features []string, claimedMechanism string, independentEvidence bool) string {
	if slices.Contains(G1G2G3MechanismGraveyard, claimedMechanism) {
		return AttributionGraveyard
	}
	for _, feature := range features {
		if slices.Contains(GraveyardFeatureFamilies, feature) {
			return AttributionUnexplained
		}
	}

	matched := make([]MechanismTemplate, 0, len(MechanismTemplates))
	for _, template := range MechanismTemplates {
		if templateMatches(features, template) {
			matched = append(matched, template)
		}
	}
	if len(matched) > 1 {
		return AttributionAmbiguous
	}
	if len(matched) == 1 && (claimedMechanism == "" || claimedMechanism == matched[0].Mechanism) {
		if independentEvidence {
			return AttributionExplained
		}
		return AttributionMechanismHypothesis
	}
	return AttributionUnexplained
}

func templateMatches(features []string, template MechanismTemplate) bool {
	for _, required := range template.RequiredAllFeatures {
		if !slices.Contains(features, required) {
			return false
		}
	}
	if len(template.RequiredAnyFeatures) == 0 {
		return true
	}
	for _, alternative := range template.RequiredAnyFeatures {
		if slices.Contains(features, alternative) {
			return true
		}
	}
	return false
}

// DiscoveryCell is the frozen cell identity used by the protocol's
// feature×cell two-dimensional round-robin schedule.
type DiscoveryCell struct {
	ProfileID   string
	Horizon     string
	Direction   string
	CapitalTier int
	CellID      string
}

// FeatureCellAssignment is one registered-allocation slot. Invalid or duplicate
// attempts are ledgered by the implementation but do not consume this slot.
type FeatureCellAssignment struct {
	AllocationRound int
	Feature         string
	Cell            DiscoveryCell
}

// FeatureCellRoundRobin freezes the fair allocation skeleton: within each
// profile every feature gets allocation N before any gets N+1, while cells
// rotate across horizon/direction/capital/cell strata.
func FeatureCellRoundRobin(profileFeatures map[string][]string, cells []DiscoveryCell, limit int) []FeatureCellAssignment {
	if limit <= 0 {
		return nil
	}
	profiles := make([]string, 0, len(profileFeatures))
	for profile := range profileFeatures {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)

	cellsByProfile := make(map[string][]DiscoveryCell, len(profiles))
	for _, profile := range profiles {
		cellsByProfile[profile] = fairDiscoveryCells(profile, cells)
	}
	cellCursor := make(map[string]int, len(profiles))
	assignments := make([]FeatureCellAssignment, 0, limit)
	for allocationRound := 1; len(assignments) < limit; allocationRound++ {
		added := false
		maxFeatures := 0
		for _, profile := range profiles {
			if len(profileFeatures[profile]) > maxFeatures {
				maxFeatures = len(profileFeatures[profile])
			}
		}
		for featureSlot := 0; featureSlot < maxFeatures && len(assignments) < limit; featureSlot++ {
			for _, profile := range profiles {
				features := profileFeatures[profile]
				profileCells := cellsByProfile[profile]
				if featureSlot >= len(features) || len(profileCells) == 0 {
					continue
				}
				cell := profileCells[cellCursor[profile]%len(profileCells)]
				cellCursor[profile]++
				assignments = append(assignments, FeatureCellAssignment{
					AllocationRound: allocationRound,
					Feature:         features[featureSlot],
					Cell:            cell,
				})
				added = true
				if len(assignments) >= limit {
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return assignments
}

func fairDiscoveryCells(profile string, cells []DiscoveryCell) []DiscoveryCell {
	groups := map[string][]DiscoveryCell{}
	for _, cell := range cells {
		if cell.ProfileID != profile {
			continue
		}
		key := cell.Horizon + "\x00" + cell.Direction + "\x00" +
			strconv.Itoa(cell.CapitalTier)
		groups[key] = append(groups[key], cell)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		sort.Slice(groups[key], func(i, j int) bool {
			return groups[key][i].CellID < groups[key][j].CellID
		})
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var ordered []DiscoveryCell
	for round := 0; ; round++ {
		added := false
		for _, key := range keys {
			if round < len(groups[key]) {
				ordered = append(ordered, groups[key][round])
				added = true
			}
		}
		if !added {
			return ordered
		}
	}
}

// UTCAnchoredBlock is one non-overlapping strict-WF observation block.
type UTCAnchoredBlock struct {
	Start time.Time
	End   time.Time
}

// UTCAnchoredNonOverlapBlocks builds 24h UTC-anchored blocks with a candidate
// horizon embargo after each observation window. Call separately per horizon.
func UTCAnchoredNonOverlapBlocks(start, end time.Time, horizon time.Duration) []UTCAnchoredBlock {
	if !end.After(start) || horizon < 0 {
		return nil
	}
	const window = 24 * time.Hour
	cursor := start.UTC().Truncate(window)
	if cursor.Before(start.UTC()) {
		cursor = cursor.Add(window)
	}
	var blocks []UTCAnchoredBlock
	for cursor.Add(window).Compare(end.UTC()) <= 0 {
		block := UTCAnchoredBlock{Start: cursor, End: cursor.Add(window)}
		blocks = append(blocks, block)
		earliestNext := block.End.Add(horizon)
		cursor = cursor.Add(window)
		for cursor.Before(earliestNext) {
			cursor = cursor.Add(window)
		}
	}
	return blocks
}

// PhenomenonCondition is one frozen predicate in a phenomenon identity.
type PhenomenonCondition struct {
	FeatureKey string  `json:"feature_key"`
	Op         string  `json:"op"`
	Threshold  float64 `json:"threshold"`
	Quantile   float64 `json:"quantile"`
}

// PhenomenonIdentity keys one deduplicated phenomenon. Capital tier,
// liquidity_bucket, and symbol_class are subpaths and must not split identity.
type PhenomenonIdentity struct {
	ProfileID       string
	SkyCoarseMarket string
	Horizon         string
	Direction       string
	Conditions      []PhenomenonCondition
}

// FeatureCatalogSummary separates declared names, available slots, coverage,
// and missing declared features for discovery audit receipts.
type FeatureCatalogSummary struct {
	DeclaredDistinctFeatureNames []string            `json:"declared_distinct_feature_names"`
	AvailableProfileFeatureSlots map[string][]string `json:"available_profile_feature_slots"`
	CoveredFeatureNames          []string            `json:"covered_feature_names"`
	MissingDeclaredFeatureNames  []string            `json:"missing_declared_feature_names"`
}

// SymbolRealizedVolRMSBPS freezes sqrt(mean(squared simple returns))*10000.
// A single non-zero return yields a non-zero RMS; flat returns yield zero.
func SymbolRealizedVolRMSBPS(simpleReturns []float64) float64 {
	if len(simpleReturns) < SymbolRealizedVolMinBars {
		return math.NaN()
	}
	var sumSq float64
	for _, r := range simpleReturns {
		sumSq += r * r
	}
	return math.Sqrt(sumSq/float64(len(simpleReturns))) * 10000
}

// VolatilityCostRatioFromRMS divides the profile primary RMS vol by roundtrip fee.
func VolatilityCostRatioFromRMS(volRMSBPS float64) float64 {
	if math.IsNaN(volRMSBPS) || math.IsInf(volRMSBPS, 0) || volRMSBPS <= 0 {
		return math.NaN()
	}
	return volRMSBPS / RoundtripTakerFeeBPS
}

// ValidateReverseMineBinding rejects drift from frozen protocol search-space ids.
func ValidateReverseMineBinding(searchSpaceID, derivationAlgorithmVersion string) error {
	if searchSpaceID != ReverseMineSearchSpaceID {
		return fmt.Errorf("search_space_id %q != protocol %q", searchSpaceID, ReverseMineSearchSpaceID)
	}
	if derivationAlgorithmVersion != DerivationAlgorithmVersion {
		return fmt.Errorf("derivation_algorithm_version %q != protocol %q", derivationAlgorithmVersion, DerivationAlgorithmVersion)
	}
	return nil
}

// DeclaredDistinctFeatureNames returns the union of all preregistered feature names.
func DeclaredDistinctFeatureNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(names []string) {
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	add(SharedFeatures)
	add(OKX1HProfileFeatures)
	add(OKX5MProfileFeatures)
	add(AttributionFeatures)
	sort.Strings(out)
	return out
}

// BuildFeatureCatalogSummary compares declared names against available slots and coverage.
func BuildFeatureCatalogSummary(availableByProfile map[string][]string, covered []string) FeatureCatalogSummary {
	declared := DeclaredDistinctFeatureNames()
	coveredSet := map[string]bool{}
	for _, name := range covered {
		coveredSet[name] = true
	}
	var missing []string
	for _, name := range declared {
		if !coveredSet[name] {
			missing = append(missing, name)
		}
	}
	coveredNames := append([]string(nil), covered...)
	sort.Strings(coveredNames)
	slots := map[string][]string{}
	for profile, names := range availableByProfile {
		cp := append([]string(nil), names...)
		sort.Strings(cp)
		slots[profile] = cp
	}
	return FeatureCatalogSummary{
		DeclaredDistinctFeatureNames: declared,
		AvailableProfileFeatureSlots: slots,
		CoveredFeatureNames:          coveredNames,
		MissingDeclaredFeatureNames:  missing,
	}
}

// PhenomenonID hashes profile/coarseSky/horizon/direction/conditions and ignores
// capital tier plus liquidity/symbol_class subpaths.
func PhenomenonID(id PhenomenonIdentity) (string, error) {
	conds := append([]PhenomenonCondition(nil), id.Conditions...)
	sort.Slice(conds, func(i, j int) bool {
		if conds[i].FeatureKey != conds[j].FeatureKey {
			return conds[i].FeatureKey < conds[j].FeatureKey
		}
		if conds[i].Op != conds[j].Op {
			return conds[i].Op < conds[j].Op
		}
		if conds[i].Threshold != conds[j].Threshold {
			return conds[i].Threshold < conds[j].Threshold
		}
		return conds[i].Quantile < conds[j].Quantile
	})
	payload := struct {
		ProfileID       string                `json:"profile_id"`
		SkyCoarseMarket string                `json:"sky_coarse_market"`
		Horizon         string                `json:"horizon"`
		Direction       string                `json:"direction"`
		Conditions      []PhenomenonCondition `json:"conditions"`
	}{
		ProfileID:       id.ProfileID,
		SkyCoarseMarket: id.SkyCoarseMarket,
		Horizon:         id.Horizon,
		Direction:       id.Direction,
		Conditions:      conds,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// MaxDayPositiveContributionFraction aggregates positive conservative net by day/block.
func MaxDayPositiveContributionFraction(dayPositive map[string]float64) float64 {
	var totalPositive, maxDay float64
	for _, amount := range dayPositive {
		if amount <= 0 {
			continue
		}
		totalPositive += amount
		if amount > maxDay {
			maxDay = amount
		}
	}
	if totalPositive <= 0 {
		return 0
	}
	return maxDay / totalPositive
}

// ExactOneSidedPositiveBinomialSignP is the one-sided positive sign p-value.
func ExactOneSidedPositiveBinomialSignP(positive, negative int) (float64, error) {
	if positive < 0 || negative < 0 {
		return 0, errors.New("sign counts must be non-negative")
	}
	n := positive + negative
	if n == 0 {
		return 1, nil
	}
	logSum := math.Inf(-1)
	for k := positive; k <= n; k++ {
		logSum = logAddExp(logSum, logBinomialPMF(n, k))
	}
	p := math.Exp(logSum)
	if p > 1 {
		p = 1
	}
	return p, nil
}

// BenjaminiHochbergFDR returns which hypotheses pass BH-FDR at level q.
func BenjaminiHochbergFDR(pValues []float64, q float64) ([]bool, error) {
	if q <= 0 || q > 1 {
		return nil, errors.New("fdr q must be in (0,1]")
	}
	n := len(pValues)
	if n == 0 {
		return nil, nil
	}
	type indexed struct {
		idx int
		p   float64
	}
	order := make([]indexed, n)
	for i, p := range pValues {
		if math.IsNaN(p) || p < 0 || p > 1 {
			return nil, fmt.Errorf("invalid p-value at index %d: %v", i, p)
		}
		order[i] = indexed{idx: i, p: p}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].p == order[j].p {
			return order[i].idx < order[j].idx
		}
		return order[i].p < order[j].p
	})
	pass := make([]bool, n)
	maxK := -1
	for k := n; k >= 1; k-- {
		threshold := float64(k) / float64(n) * q
		if order[k-1].p <= threshold {
			maxK = k
			break
		}
	}
	if maxK < 0 {
		return pass, nil
	}
	for i := 0; i < maxK; i++ {
		pass[order[i].idx] = true
	}
	return pass, nil
}

// ExactTwoSidedBinomialSignP computes the exact p-value without integer
// combination counts. lgamma and log-sum-exp remain safe far beyond int64 nCk.
func ExactTwoSidedBinomialSignP(positive, negative int) (float64, error) {
	if positive < 0 || negative < 0 {
		return 0, errors.New("sign counts must be non-negative")
	}
	n := positive + negative
	if n == 0 {
		return 1, nil
	}
	observedLogP := logBinomialPMF(n, positive)
	logSum := math.Inf(-1)
	for k := 0; k <= n; k++ {
		logP := logBinomialPMF(n, k)
		if logP <= observedLogP+1e-12 {
			logSum = logAddExp(logSum, logP)
		}
	}
	p := math.Exp(logSum)
	if p > 1 {
		p = 1
	}
	return p, nil
}

func logBinomialPMF(n, k int) float64 {
	logN, _ := math.Lgamma(float64(n + 1))
	logK, _ := math.Lgamma(float64(k + 1))
	logNK, _ := math.Lgamma(float64(n - k + 1))
	return logN - logK - logNK - float64(n)*math.Ln2
}

func logAddExp(a, b float64) float64 {
	if math.IsInf(a, -1) {
		return b
	}
	if math.IsInf(b, -1) {
		return a
	}
	if a < b {
		a, b = b, a
	}
	return a + math.Log1p(math.Exp(b-a))
}

// FullMarketSummary is the required manifest/summary scope contract.
type FullMarketSummary struct {
	Available  int
	Selected   int
	Truncated  bool
	FullMarket bool
	Scope      string
}

// ValidateFullMarketScope rejects silent truncation. Any selected subset must
// be explicitly allowed and labeled TRUNCATED_SMOKE.
func ValidateFullMarketScope(summary FullMarketSummary, catalogSymbolLimit int, allowTruncatedSmoke bool) error {
	if summary.Available < 0 || summary.Selected < 0 || summary.Selected > summary.Available {
		return errors.New("invalid Available/Selected counts")
	}
	truncated := summary.Selected < summary.Available
	if truncated {
		if !allowTruncatedSmoke {
			if catalogSymbolLimit == 0 {
				return errors.New("catalog_symbol_limit=0 silent truncation forbidden")
			}
			return errors.New("truncation requires allow_truncated_smoke")
		}
		if !summary.Truncated || summary.FullMarket || summary.Scope != TruncatedSmokeScope {
			return errors.New("truncated selection must be TRUNCATED_SMOKE")
		}
		return nil
	}
	if summary.Truncated || !summary.FullMarket || summary.Scope != FullMarketScope {
		return errors.New("complete selection must be FULL_MARKET")
	}
	return nil
}

// FormalPathAllowed prevents smoke subsets from entering validation,
// mechanism attribution, strict WF, exam, or certification.
func FormalPathAllowed(scope string) bool {
	return scope == FullMarketScope
}
