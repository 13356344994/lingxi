package protocol

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/researchboundary"
)

const (
	g4ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g4_preregistration.json"
	g3ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g3_preregistration.json"
	g2ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	g2StageSummaryRelPath = "../../../research/firstbattle/g2_stage_summary.json"
	g3StageSummaryRelPath = "../../../research/firstbattle/g3_stage_summary.json"
	regimeManifestRelPath = "../../../research/firstbattle/history_regime_contract.json"
	reqManifestRelPath    = "../../../research/firstbattle/history_data_requirements.json"
)

type dataProfileManifest struct {
	ID                      string   `json:"id"`
	PrimaryFamily           string   `json:"primary_family"`
	OutcomeHorizonsHours    []int    `json:"outcome_horizons_hours"`
	OutcomeHorizonsMinutes  []int    `json:"outcome_horizons_minutes"`
	MaxFeatureLookbackHours int      `json:"max_feature_lookback_hours"`
	PurgeEmbargoRule        string   `json:"purge_embargo_rule"`
	EntryPriceRule          string   `json:"entry_price_rule"`
	ExitPriceRule           string   `json:"exit_price_rule"`
	OptionalFeatureFamilies []string `json:"optional_feature_families"`
}

type mechanismTemplateManifest struct {
	ID                  string   `json:"id"`
	RequiredAllFeatures []string `json:"required_all_features"`
	RequiredAnyFeatures []string `json:"required_any_features"`
	Mechanism           string   `json:"mechanism"`
	Payer               string   `json:"payer"`
	ForcedBehavior      string   `json:"forced_behavior"`
	Deadline            string   `json:"deadline"`
	CapacityDefense     string   `json:"capacity_defense"`
	MatchStatus         string   `json:"match_status"`
	Trade               bool     `json:"trade"`
	Exam                bool     `json:"exam"`
	CertifiedLeaf       bool     `json:"certified_leaf"`
}

type g4Manifest struct {
	ProtocolID string `json:"protocol_id"`
	Generation string `json:"generation"`

	ClosesG1 struct {
		G1Generation           string `json:"g1_generation"`
		G1RouterProtocolHash   string `json:"g1_router_protocol_hash"`
		G1CertifiedMoneyLeaves int    `json:"g1_certified_money_leaves"`
		G1ResearchPassLeaves   int    `json:"g1_research_pass_leaves"`
		G1SystemStatus         string `json:"g1_system_status"`
	} `json:"closes_g1"`

	ClosesG2 struct {
		G2Generation                   string `json:"g2_generation"`
		G2RouterProtocolHash           string `json:"g2_router_protocol_hash"`
		G2RegisteredSearchCombinations int    `json:"g2_registered_search_combinations"`
		G2CandidateNominations         int    `json:"g2_candidate_nominations"`
		G2ResearchPassLeaves           int    `json:"g2_research_pass_leaves"`
		G2SystemStatus                 string `json:"g2_system_status"`
	} `json:"closes_g2"`

	ClosesG3 struct {
		G3Generation                           string   `json:"g3_generation"`
		G3RouterProtocolHash                   string   `json:"g3_router_protocol_hash"`
		G3RegisteredSearchRules                int      `json:"g3_registered_search_rules"`
		G3CandidateNominations                 int      `json:"g3_candidate_nominations"`
		G3ResearchPassLeaves                   int      `json:"g3_research_pass_leaves"`
		G3SystemStatus                         string   `json:"g3_system_status"`
		CompatiblePriorProfitMapProtocolHashes []string `json:"compatible_prior_profit_map_protocol_hashes"`
		ProfitMapCompatibilityScope            string   `json:"profit_map_compatibility_scope"`
	} `json:"closes_g3"`

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
		G2RouterProtocolHash        string `json:"g2_router_protocol_hash"`
		G3RouterProtocolHash        string `json:"g3_router_protocol_hash"`
	} `json:"binds"`

	DataProfiles struct {
		OptionalNonblockingFamilies []string              `json:"optional_nonblocking_families"`
		Profiles                    []dataProfileManifest `json:"profiles"`
	} `json:"data_profiles"`

	TimePartitionRoles struct {
		PartitionOrder        string `json:"partition_order"`
		DiscoveryPermille     int    `json:"discovery_permille"`
		ValidationPermille    int    `json:"validation_permille"`
		WalkForwardPermille   int    `json:"walk_forward_permille"`
		PurgeEmbargoFormula   string `json:"purge_embargo_formula"`
		PerProfileIndependent bool   `json:"per_profile_independent"`
		ExamActions           int    `json:"exam_actions"`
	} `json:"time_partition_roles"`

	CapitalTiersUSDT []int `json:"capital_tiers_usdt"`

	ReturnFields struct {
		Fields            []string `json:"fields"`
		DirectionSymmetry string   `json:"direction_symmetry"`
		NeutralBandBPS    float64  `json:"neutral_band_bps"`
	} `json:"return_fields"`

	CostModel struct {
		ProxyDisclaimer               string  `json:"proxy_disclaimer"`
		RoundtripTakerFeeBPS          float64 `json:"roundtrip_taker_fee_bps"`
		BaseSpreadSlippageFloorBPS    float64 `json:"base_spread_slippage_floor_bps"`
		RollingQuoteVolumeWindowHours int     `json:"rolling_quote_volume_window_hours"`
		CapacityImpactScaleBPS        float64 `json:"capacity_impact_scale_bps"`
		CapacityImpactCapBPS          float64 `json:"capacity_impact_cap_bps"`
		CapacityImpactFormula         string  `json:"capacity_impact_formula"`
		BarRangeToxicityScale         float64 `json:"bar_range_toxicity_scale"`
		BarRangeToxicityCapBPS        float64 `json:"bar_range_toxicity_cap_bps"`
		BarRangeToxicityFormula       string  `json:"bar_range_toxicity_formula"`
		FundingAcrossSettlements      bool    `json:"funding_across_settlements"`
		FundingFormula                string  `json:"funding_formula"`
		FeeNetFormula                 string  `json:"fee_net_formula"`
		ConservativeNetFormula        string  `json:"conservative_net_formula"`
		CostStressMultiplier          float64 `json:"cost_stress_multiplier"`
		DelayPlusOneBarEntry          bool    `json:"delay_plus_one_bar_entry"`
	} `json:"cost_model"`

	ProfitMap struct {
		Dimensions    []string `json:"dimensions"`
		SkyCoarseAxes struct {
			Ret               []string `json:"ret"`
			Rv                []string `json:"rv"`
			Fund              []string `json:"fund"`
			Te                string   `json:"te"`
			Dd                string   `json:"dd"`
			FundMissingAction string   `json:"fund_missing_action"`
		} `json:"sky_coarse_axes"`
		LiquidityBucketRule string   `json:"liquidity_bucket_rule"`
		SymbolClassRule     string   `json:"symbol_class_rule"`
		AggregatesPerCell   []string `json:"aggregates_per_cell"`
		ScanBounds          struct {
			MaxFilesRequiredCLI           bool `json:"max_files_required_cli"`
			MaxLinesRequiredCLI           bool `json:"max_lines_required_cli"`
			CatalogSymbolLimitRequiredCLI bool `json:"catalog_symbol_limit_required_cli"`
			DefaultCatalogSymbolLimit     int  `json:"default_catalog_symbol_limit"`
			CheckpointRequired            bool `json:"checkpoint_required"`
			StreamingOutputRequired       bool `json:"streaming_output_required"`
			ProgressEveryLines            int  `json:"progress_every_lines"`
			CheckpointEverySymbols        int  `json:"checkpoint_every_symbols"`
			QuantileReservoirPerCell      int  `json:"quantile_reservoir_per_cell"`
			WinnerReservoirPerCell        int  `json:"winner_reservoir_per_cell"`
		} `json:"scan_bounds"`
		FullMarketSemantics struct {
			RequiredSummaryFields     []string `json:"required_summary_fields"`
			FullMarketScope           string   `json:"full_market_scope"`
			TruncatedScope            string   `json:"truncated_scope"`
			TruncatedFormalPathPolicy string   `json:"truncated_formal_path_policy"`
		} `json:"full_market_semantics"`
	} `json:"profit_map"`

	FeatureCatalog struct {
		MaxFeaturesPerRule                        int                 `json:"max_features_per_rule"`
		MinFeaturesPerRule                        int                 `json:"min_features_per_rule"`
		ThresholdSource                           string              `json:"threshold_source"`
		ThresholdQuantiles                        []float64           `json:"threshold_quantiles"`
		ForbiddenFeatureInputs                    []string            `json:"forbidden_feature_inputs"`
		SharedFeatures                            []string            `json:"shared_features"`
		AttributionFeatures                       []string            `json:"attribution_features"`
		AttributionFeaturesDiscoveryEligible      bool                `json:"attribution_features_discovery_eligible"`
		AttributionFeatureMembershipNeverExplains bool                `json:"attribution_feature_membership_never_explains_mechanism"`
		SymbolRealizedVolFormula                  string              `json:"symbol_realized_vol_formula"`
		SymbolRealizedVolMinBars                  int                 `json:"symbol_realized_vol_min_bars"`
		VolatilityCostRatioFormula                string              `json:"volatility_cost_ratio_formula"`
		VolatilityCostRatioVolHorizon             map[string]string   `json:"volatility_cost_ratio_vol_horizon"`
		ProfileFeatures                           map[string][]string `json:"profile_features"`
	} `json:"feature_catalog"`

	DiscoveryBudget struct {
		MaxTotalCandidates          int       `json:"max_total_candidates"`
		MaxSingleFeatureRules       int       `json:"max_single_feature_rules"`
		MaxDualFeatureRules         int       `json:"max_dual_feature_rules"`
		AllowedSearchSpaceID        string    `json:"allowed_search_space_id"`
		DerivationAlgorithmVersion  string    `json:"derivation_algorithm_version"`
		ThresholdQuantiles          []float64 `json:"threshold_quantiles"`
		OperatorOrder               []string  `json:"operator_order"`
		ExecutionModel              string    `json:"execution_model"`
		AttemptPipeline             string    `json:"attempt_pipeline"`
		FreezeBeforeValidation      bool      `json:"freeze_before_validation"`
		PayerRequiredAtDiscovery    bool      `json:"payer_required_at_discovery"`
		UnexplainedCandidateAllowed bool      `json:"unexplained_candidate_allowed"`
		UnexplainedStatus           string    `json:"unexplained_status"`
		CheckpointRequired          bool      `json:"checkpoint_required"`
		DefaultProgressEvery        int       `json:"default_progress_every"`
		EnumerationFairness         struct {
			Dimensions       []string `json:"dimensions"`
			CellStrata       []string `json:"cell_strata"`
			Order            string   `json:"order"`
			FeatureRule      string   `json:"feature_rule"`
			CellRule         string   `json:"cell_rule"`
			CheckpointFields []string `json:"checkpoint_fields"`
		} `json:"enumeration_fairness"`
		Ledger struct {
			Required                           bool     `json:"required"`
			AppendOnly                         bool     `json:"append_only"`
			OneEntryPerGeneratedCheckedAttempt bool     `json:"one_entry_per_generated_checked_attempt"`
			EntryOrder                         string   `json:"entry_order"`
			Statuses                           []string `json:"statuses"`
			MustRecord                         []string `json:"must_record"`
			CannotDeleteGeneratedAttempts      bool     `json:"cannot_delete_generated_attempts"`
		} `json:"ledger"`
	} `json:"discovery_budget"`

	ValidationGates struct {
		ApplyOn                                        string  `json:"apply_on"`
		FrozenBeforeReadingResults                     bool    `json:"frozen_before_reading_results"`
		DiscoveryScreenMethod                          string  `json:"discovery_screen_method"`
		NoFinalHolm                                    bool    `json:"no_final_holm"`
		StrictCertificationDeferred                    bool    `json:"strict_certification_deferred"`
		MinOpportunities                               int     `json:"min_opportunities"`
		MinUniqueDays                                  int     `json:"min_unique_days"`
		MinUniqueSymbols                               int     `json:"min_unique_symbols"`
		MinFeeNetMeanBPS                               float64 `json:"min_fee_net_mean_bps"`
		MinConservativeNetMeanBPS                      float64 `json:"min_conservative_net_mean_bps"`
		MaxSingleEventContributionFraction             float64 `json:"max_single_event_contribution_fraction"`
		MaxDayPositiveContributionFraction             float64 `json:"max_day_positive_contribution_fraction"`
		DelayPlusOneBarRequired                        bool    `json:"delay_plus_one_bar_required"`
		CostStressMultiplier                           float64 `json:"cost_stress_multiplier"`
		CostStressMustRemainNonNegativeConservativeNet bool    `json:"cost_stress_must_remain_non_negative_conservative_net"`
		SparseSampleAction                             string  `json:"sparse_sample_action"`
		PassStatus                                     string  `json:"pass_status"`
		FailStatus                                     string  `json:"fail_status"`
		DayBlockSignTest                               struct {
			Required                          bool    `json:"required"`
			BlockUnit                         string  `json:"block_unit"`
			ObservationWindowHours            int     `json:"observation_window_hours"`
			EmbargoGap                        string  `json:"embargo_gap"`
			HorizonIndependence               string  `json:"horizon_independence"`
			OverlapRule                       string  `json:"overlap_rule"`
			MinNonzeroBlocks                  int     `json:"min_nonzero_blocks"`
			PValue                            string  `json:"p_value"`
			NumericMethod                     string  `json:"numeric_method"`
			IntegerCombinationCountsForbidden bool    `json:"integer_combination_counts_forbidden"`
			MultipleComparisonControl         string  `json:"multiple_comparison_control"`
			Family                            string  `json:"family"`
			FamilyQ                           float64 `json:"family_q"`
		} `json:"day_block_sign_test"`
	} `json:"validation_gates"`

	PhenomenonID struct {
		IgnoreCapitalTier bool     `json:"ignore_capital_tier"`
		KeyDimensions     []string `json:"key_dimensions"`
		SubpathDimensions []string `json:"subpath_dimensions"`
	} `json:"phenomenon_id"`

	FeatureCatalogSummary struct {
		RequiredFields []string `json:"required_fields"`
	} `json:"feature_catalog_summary"`

	MechanismAttribution struct {
		Version                        string   `json:"version"`
		Statuses                       []string `json:"statuses"`
		DiscoveryRequiresPayer         bool     `json:"discovery_requires_payer"`
		FeatureMembershipAutoExplained bool     `json:"feature_membership_auto_explained"`
		G4IndependentEvidenceAvailable bool     `json:"g4_independent_evidence_available"`
		AutomaticExplainedCount        int      `json:"automatic_explained_count"`
		EvaluationOrder                []string `json:"evaluation_order"`
		IndependentEvidenceContract    struct {
			RequiredForExplained       bool     `json:"required_for_explained"`
			CurrentGenerationSatisfied bool     `json:"current_generation_satisfied"`
			MustInclude                []string `json:"must_include"`
			MustNotBeDerivedFrom       []string `json:"must_not_be_derived_from"`
			FutureBinding              string   `json:"future_binding"`
		} `json:"independent_evidence_contract"`
		GraveyardFeatureFamilies  []string                    `json:"graveyard_feature_families"`
		UnexplainedFeatureClasses []string                    `json:"unexplained_feature_classes"`
		Templates                 []mechanismTemplateManifest `json:"templates"`
		CrossTemplateResolution   string                      `json:"cross_template_resolution"`
		NarrativeRejectionRule    string                      `json:"narrative_rejection_rule"`
	} `json:"mechanism_attribution"`

	PostValidationRouting struct {
		ValidationPassUnexplainedStatus string `json:"validation_pass_unexplained_status"`
		ValidationPassHypothesisStatus  string `json:"validation_pass_hypothesis_status"`
		UnexplainedPolicy               string `json:"unexplained_policy"`
		HypothesisPolicy                string `json:"hypothesis_policy"`
		AmbiguousPolicy                 string `json:"ambiguous_policy"`
		GraveyardPolicy                 string `json:"graveyard_policy"`
		StrictWFEligibility             string `json:"strict_wf_eligibility"`
		CurrentAutomaticStrictWFEntries int    `json:"current_automatic_strict_wf_entries"`
	} `json:"post_validation_routing"`

	StrictWalkForward struct {
		Partition                          string   `json:"partition"`
		UsesProfilePurgeEmbargo            bool     `json:"uses_profile_purge_embargo"`
		Eligibility                        string   `json:"eligibility"`
		CurrentAutomaticEntries            int      `json:"current_automatic_entries"`
		MinFirings                         int      `json:"min_firings"`
		MinUniqueDays                      int      `json:"min_unique_days"`
		MinUniqueSymbols                   int      `json:"min_unique_symbols"`
		MinIndependentBlocks               int      `json:"min_independent_blocks"`
		StrictPositiveMeanFields           []string `json:"strict_positive_mean_fields"`
		MeanComparison                     string   `json:"mean_comparison"`
		MaxSingleEventContributionFraction float64  `json:"max_single_event_contribution_fraction"`
		MaxCapacityFailFraction            float64  `json:"max_capacity_fail_fraction"`
		CapacityFailComparison             string   `json:"capacity_fail_comparison"`
		DayBlockSignTest                   struct {
			Required                          bool    `json:"required"`
			BlockUnit                         string  `json:"block_unit"`
			ObservationWindowHours            int     `json:"observation_window_hours"`
			EmbargoGap                        string  `json:"embargo_gap"`
			HorizonIndependence               string  `json:"horizon_independence"`
			OverlapRule                       string  `json:"overlap_rule"`
			PValue                            string  `json:"p_value"`
			NumericMethod                     string  `json:"numeric_method"`
			IntegerCombinationCountsForbidden bool    `json:"integer_combination_counts_forbidden"`
			MultipleComparisonControl         string  `json:"multiple_comparison_control"`
			Family                            string  `json:"family"`
			FamilyAlpha                       float64 `json:"family_alpha"`
		} `json:"day_block_sign_test"`
		CertificationStates     []string `json:"certification_states"`
		PassState               string   `json:"pass_state"`
		GateFailureState        string   `json:"gate_failure_state"`
		InsufficientSampleState string   `json:"insufficient_sample_state"`
		ResearchPassPolicy      string   `json:"research_pass_policy"`
		ExamActions             int      `json:"exam_actions"`
	} `json:"strict_walk_forward"`

	G1G2G3MechanismGraveyard []string `json:"g1_g2_g3_mechanism_graveyard"`

	RedTeam struct {
		Requirements []struct {
			ID          string `json:"id"`
			Requirement string `json:"requirement"`
			EnforcedBy  string `json:"enforced_by"`
		} `json:"requirements"`
	} `json:"red_team"`

	Audit struct {
		ProtocolHash *string `json:"protocol_hash"`
		ExamActions  int     `json:"exam_actions"`
	} `json:"audit"`
}

func loadG4Manifest(t *testing.T) g4Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g4ManifestRelPath))
	if err != nil {
		t.Fatalf("read g4 manifest: %v", err)
	}
	var m g4Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g4 manifest: %v", err)
	}
	return m
}

func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG4Manifest(t)

	if m.ProtocolID != ProtocolID {
		t.Errorf("protocol_id = %q, want %q", m.ProtocolID, ProtocolID)
	}
	if m.Generation != Generation {
		t.Errorf("generation = %q, want %q", m.Generation, Generation)
	}
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != RouterProtocolHash {
		t.Errorf("audit.protocol_hash = %v, want %q", m.Audit.ProtocolHash, RouterProtocolHash)
	}
	if m.Audit.ExamActions != ExamActions || m.TimePartitionRoles.ExamActions != ExamActions {
		t.Errorf("exam_actions drift: audit=%d partition=%d want %d",
			m.Audit.ExamActions, m.TimePartitionRoles.ExamActions, ExamActions)
	}

	assertStrSlice(t, "data_profiles optional_nonblocking", m.DataProfiles.OptionalNonblockingFamilies, OptionalNonblockingFamilies)
	if len(m.DataProfiles.Profiles) != len(DataProfileIDs) {
		t.Fatalf("profiles length = %d, want %d", len(m.DataProfiles.Profiles), len(DataProfileIDs))
	}
	for i, id := range DataProfileIDs {
		if m.DataProfiles.Profiles[i].ID != id {
			t.Errorf("profiles[%d].id = %q, want %q", i, m.DataProfiles.Profiles[i].ID, id)
		}
	}
	p1 := m.DataProfiles.Profiles[0]
	if p1.PrimaryFamily != OKX1HPrimaryFamily ||
		p1.MaxFeatureLookbackHours != OKX1HMaxFeatureLookbackHours ||
		p1.PurgeEmbargoRule != PurgeEmbargoRule ||
		p1.EntryPriceRule != EntryPriceRule ||
		p1.ExitPriceRule != ExitPriceRule {
		t.Errorf("okx_1h profile drifted: %+v", p1)
	}
	assertIntSlice(t, "okx_1h outcome_horizons_hours", p1.OutcomeHorizonsHours, OKX1HOutcomeHorizonsHours)
	p2 := m.DataProfiles.Profiles[1]
	if p2.PrimaryFamily != OKX5MPrimaryFamily ||
		p2.MaxFeatureLookbackHours != OKX5MMaxFeatureLookbackHours {
		t.Errorf("okx_5m profile drifted: %+v", p2)
	}
	assertIntSlice(t, "okx_5m outcome_horizons_minutes", p2.OutcomeHorizonsMinutes, OKX5MOutcomeHorizonsMinutes)

	if m.TimePartitionRoles.PartitionOrder != PartitionOrder ||
		m.TimePartitionRoles.DiscoveryPermille != DiscoveryPermille ||
		m.TimePartitionRoles.ValidationPermille != ValidationPermille ||
		m.TimePartitionRoles.WalkForwardPermille != WalkForwardPermille ||
		m.TimePartitionRoles.PurgeEmbargoFormula != PurgeEmbargoFormula ||
		!m.TimePartitionRoles.PerProfileIndependent {
		t.Errorf("time_partition_roles drifted: %+v", m.TimePartitionRoles)
	}

	assertIntSlice(t, "capital_tiers_usdt", m.CapitalTiersUSDT, CapitalTiersUSDT)
	assertStrSlice(t, "return_fields.fields", m.ReturnFields.Fields, ReturnFields)
	if m.ReturnFields.DirectionSymmetry != DirectionSymmetry || m.ReturnFields.NeutralBandBPS != NeutralBandBPS {
		t.Errorf("return_fields drifted")
	}

	assertStrSlice(t, "profit_map.dimensions", m.ProfitMap.Dimensions, ProfitMapDimensions)
	assertStrSlice(t, "profit_map.sky_coarse_axes.ret", m.ProfitMap.SkyCoarseAxes.Ret, CoarseSkyAxisLabels)
	assertStrSlice(t, "profit_map.sky_coarse_axes.rv", m.ProfitMap.SkyCoarseAxes.Rv, CoarseSkyAxisLabels)
	assertStrSlice(t, "profit_map.sky_coarse_axes.fund", m.ProfitMap.SkyCoarseAxes.Fund, FundingCoarseAxisLabels)
	if m.ProfitMap.SkyCoarseAxes.FundMissingAction != FundingMissingAction {
		t.Errorf("fund missing action drifted")
	}
	if m.ProfitMap.SkyCoarseAxes.Te != "*" || m.ProfitMap.SkyCoarseAxes.Dd != "*" {
		t.Errorf("wildcard sky axes drifted")
	}
	assertStrSlice(t, "profit_map.aggregates_per_cell", m.ProfitMap.AggregatesPerCell, ProfitMapAggregates)
	if m.ProfitMap.LiquidityBucketRule != LiquidityBucketRule || m.ProfitMap.SymbolClassRule != SymbolClassRule {
		t.Errorf("profit map bucket/class rules drifted")
	}
	sb := m.ProfitMap.ScanBounds
	if !sb.MaxFilesRequiredCLI || !sb.MaxLinesRequiredCLI || !sb.CatalogSymbolLimitRequiredCLI ||
		sb.DefaultCatalogSymbolLimit != DefaultCatalogSymbolLimit ||
		!sb.CheckpointRequired || !sb.StreamingOutputRequired ||
		sb.ProgressEveryLines != ScanProgressEveryLines ||
		sb.CheckpointEverySymbols != ScanCheckpointEverySymbols ||
		sb.QuantileReservoirPerCell != MapQuantileReservoirPerCell ||
		sb.WinnerReservoirPerCell != MapWinnerReservoirPerCell {
		t.Errorf("scan_bounds drifted: %+v", sb)
	}
	fm := m.ProfitMap.FullMarketSemantics
	assertStrSlice(t, "full_market_semantics.required_summary_fields", fm.RequiredSummaryFields, FullMarketRequiredSummaryFields)
	if fm.FullMarketScope != FullMarketScope ||
		fm.TruncatedScope != TruncatedSmokeScope ||
		fm.TruncatedFormalPathPolicy != TruncatedFormalPathPolicy {
		t.Errorf("full_market_semantics drifted: %+v", fm)
	}

	if m.FeatureCatalog.MaxFeaturesPerRule != MaxFeaturesPerRule ||
		m.FeatureCatalog.MinFeaturesPerRule != MinFeaturesPerRule ||
		m.FeatureCatalog.ThresholdSource != ThresholdSource {
		t.Errorf("feature_catalog rule bounds drifted")
	}
	assertFloatSlice(t, "feature_catalog.threshold_quantiles", m.FeatureCatalog.ThresholdQuantiles, ThresholdQuantiles)
	assertStrSlice(t, "feature_catalog.forbidden_feature_inputs", m.FeatureCatalog.ForbiddenFeatureInputs, ForbiddenFeatureInputs)
	assertStrSlice(t, "feature_catalog.shared_features", m.FeatureCatalog.SharedFeatures, SharedFeatures)
	assertStrSlice(t, "feature_catalog.attribution_features", m.FeatureCatalog.AttributionFeatures, AttributionFeatures)
	if m.FeatureCatalog.AttributionFeaturesDiscoveryEligible != AttributionFeaturesDiscoveryEligible ||
		m.FeatureCatalog.AttributionFeatureMembershipNeverExplains != AttributionFeatureMembershipNeverExplains {
		t.Errorf("attribution feature discovery/explanation policy drifted")
	}
	if m.FeatureCatalog.SymbolRealizedVolFormula != SymbolRealizedVolFormula ||
		m.FeatureCatalog.SymbolRealizedVolMinBars != SymbolRealizedVolMinBars ||
		m.FeatureCatalog.VolatilityCostRatioFormula != VolatilityCostRatioFormula ||
		m.FeatureCatalog.VolatilityCostRatioVolHorizon["okx_1h"] != VolatilityCostRatioVolHorizonOKX1H ||
		m.FeatureCatalog.VolatilityCostRatioVolHorizon["okx_5m"] != VolatilityCostRatioVolHorizonOKX5M {
		t.Errorf("feature vol/ratio formulas drifted")
	}
	assertStrSlice(t, "feature_catalog.profile_features.okx_1h", m.FeatureCatalog.ProfileFeatures["okx_1h"], OKX1HProfileFeatures)
	assertStrSlice(t, "feature_catalog.profile_features.okx_5m", m.FeatureCatalog.ProfileFeatures["okx_5m"], OKX5MProfileFeatures)

	db := m.DiscoveryBudget
	checkInt(t, "discovery_budget.max_total_candidates", db.MaxTotalCandidates, MaxTotalCandidates)
	checkInt(t, "discovery_budget.max_single_feature_rules", db.MaxSingleFeatureRules, MaxSingleFeatureRules)
	checkInt(t, "discovery_budget.max_dual_feature_rules", db.MaxDualFeatureRules, MaxDualFeatureRules)
	if db.AllowedSearchSpaceID != ReverseMineSearchSpaceID ||
		db.DerivationAlgorithmVersion != DerivationAlgorithmVersion ||
		db.ExecutionModel != DiscoveryExecutionModel ||
		db.AttemptPipeline != DiscoveryAttemptPipeline ||
		!db.FreezeBeforeValidation ||
		db.PayerRequiredAtDiscovery != PayerRequiredAtDiscovery ||
		!db.UnexplainedCandidateAllowed ||
		db.UnexplainedStatus != UnexplainedStatus ||
		!db.CheckpointRequired ||
		db.DefaultProgressEvery != DiscoveryProgressEvery {
		t.Errorf("discovery_budget drifted")
	}
	assertFloatSlice(t, "discovery_budget.threshold_quantiles", db.ThresholdQuantiles, ThresholdQuantiles)
	assertStrSlice(t, "discovery_budget.operator_order", db.OperatorOrder, DiscoveryOperatorOrder)
	fair := db.EnumerationFairness
	assertStrSlice(t, "enumeration_fairness.dimensions", fair.Dimensions, DiscoveryFairnessDimensions)
	assertStrSlice(t, "enumeration_fairness.cell_strata", fair.CellStrata, DiscoveryCellStrata)
	assertStrSlice(t, "enumeration_fairness.checkpoint_fields", fair.CheckpointFields, DiscoveryFairnessCheckpointFields)
	if fair.Order != DiscoveryEnumerationOrder ||
		fair.FeatureRule != DiscoveryFeatureFairnessRule ||
		fair.CellRule != DiscoveryCellFairnessRule {
		t.Errorf("enumeration_fairness drifted: %+v", fair)
	}
	if !db.Ledger.Required || !db.Ledger.AppendOnly ||
		!db.Ledger.OneEntryPerGeneratedCheckedAttempt ||
		!db.Ledger.CannotDeleteGeneratedAttempts ||
		db.Ledger.EntryOrder != "bounded_streaming_generation_order" {
		t.Errorf("discovery ledger drifted")
	}
	assertStrSlice(t, "discovery ledger statuses", db.Ledger.Statuses, DiscoveryLedgerStatuses)
	assertStrSlice(t, "discovery ledger must_record", db.Ledger.MustRecord, DiscoveryLedgerMustRecord)

	vg := m.ValidationGates
	if vg.ApplyOn != ValidationApplyOn ||
		!vg.FrozenBeforeReadingResults ||
		vg.DiscoveryScreenMethod != ValidationDiscoveryScreenMethod ||
		!vg.NoFinalHolm ||
		!vg.StrictCertificationDeferred ||
		vg.MinOpportunities != ValidationMinOpportunities ||
		vg.MinUniqueDays != ValidationMinUniqueDays ||
		vg.MinUniqueSymbols != ValidationMinUniqueSymbols ||
		vg.MinFeeNetMeanBPS != ValidationMinFeeNetMeanBPS ||
		vg.MinConservativeNetMeanBPS != ValidationMinConservativeNetMeanBPS ||
		vg.MaxSingleEventContributionFraction != ValidationMaxSingleEventContributionFraction ||
		vg.MaxDayPositiveContributionFraction != ValidationMaxDayPositiveContributionFraction ||
		!vg.DelayPlusOneBarRequired ||
		vg.CostStressMultiplier != ValidationCostStressMultiplier ||
		!vg.CostStressMustRemainNonNegativeConservativeNet ||
		vg.SparseSampleAction != ValidationSparseSampleAction ||
		vg.PassStatus != ValidationPassStatus ||
		vg.FailStatus != ValidationFailStatus {
		t.Errorf("validation_gates drifted: %+v", vg)
	}
	sign := vg.DayBlockSignTest
	if !sign.Required ||
		sign.BlockUnit != ValidationBlockUnit ||
		sign.ObservationWindowHours != ValidationObservationWindowHours ||
		sign.EmbargoGap != ValidationEmbargoGap ||
		sign.HorizonIndependence != ValidationHorizonIndependence ||
		sign.OverlapRule != ValidationOverlapRule ||
		sign.MinNonzeroBlocks != ValidationMinIndependentBlocks ||
		sign.PValue != ValidationSignPValue ||
		sign.NumericMethod != ValidationSignNumericMethod ||
		sign.IntegerCombinationCountsForbidden != ValidationIntegerCombinationsForbidden ||
		sign.MultipleComparisonControl != ValidationMultipleComparisonControl ||
		sign.Family != ValidationFDRCandidateFamily ||
		sign.FamilyQ != ValidationFDRQ {
		t.Errorf("validation_gates.day_block_sign_test drifted: %+v", sign)
	}

	if !m.PhenomenonID.IgnoreCapitalTier {
		t.Errorf("phenomenon_id.ignore_capital_tier must be true")
	}
	assertStrSlice(t, "phenomenon_id.key_dimensions", m.PhenomenonID.KeyDimensions, PhenomenonIDKeyDimensions)
	assertStrSlice(t, "phenomenon_id.subpath_dimensions", m.PhenomenonID.SubpathDimensions, PhenomenonIDSubpathDimensions)
	assertStrSlice(t, "feature_catalog_summary.required_fields", m.FeatureCatalogSummary.RequiredFields, FeatureCatalogSummaryFields)

	assertMechanismAttributionManifest(t, m)
	assertPostValidationRoutingManifest(t, m)
	assertStrictWalkForwardManifest(t, m)
	assertStrSlice(t, "g1_g2_g3_mechanism_graveyard", m.G1G2G3MechanismGraveyard, G1G2G3MechanismGraveyard)

	if len(m.RedTeam.Requirements) != RedTeamRequirementCount {
		t.Errorf("red_team.requirements = %d items, want %d", len(m.RedTeam.Requirements), RedTeamRequirementCount)
	}
}

func TestProfilePartitionNoDrift(t *testing.T) {
	m := loadG4Manifest(t)
	if m.TimePartitionRoles.DiscoveryPermille+m.TimePartitionRoles.ValidationPermille+m.TimePartitionRoles.WalkForwardPermille != 1000 {
		t.Fatalf("partition permille must sum to 1000")
	}
	for _, p := range m.DataProfiles.Profiles {
		if p.PurgeEmbargoRule != PurgeEmbargoRule {
			t.Errorf("profile %q purge_embargo_rule = %q, want %q", p.ID, p.PurgeEmbargoRule, PurgeEmbargoRule)
		}
	}
}

func TestCostModelNoDrift(t *testing.T) {
	m := loadG4Manifest(t)
	c := m.CostModel
	if c.ProxyDisclaimer != CostProxyDisclaimer ||
		c.RoundtripTakerFeeBPS != RoundtripTakerFeeBPS ||
		c.BaseSpreadSlippageFloorBPS != BaseSpreadSlippageFloorBPS ||
		c.RollingQuoteVolumeWindowHours != RollingQuoteVolumeWindowHours ||
		c.CapacityImpactScaleBPS != CapacityImpactScaleBPS ||
		c.CapacityImpactCapBPS != CapacityImpactCapBPS ||
		c.CapacityImpactFormula != CapacityImpactFormula ||
		c.BarRangeToxicityScale != BarRangeToxicityScale ||
		c.BarRangeToxicityCapBPS != BarRangeToxicityCapBPS ||
		c.BarRangeToxicityFormula != BarRangeToxicityFormula ||
		!c.FundingAcrossSettlements ||
		c.FundingFormula != FundingFormula ||
		c.FeeNetFormula != FeeNetFormula ||
		c.ConservativeNetFormula != ConservativeNetFormula ||
		c.CostStressMultiplier != CostStressMultiplier ||
		!c.DelayPlusOneBarEntry {
		t.Errorf("cost_model drifted: %+v", c)
	}
}

func TestValidationBHFRDiscoveryScreen(t *testing.T) {
	m := loadG4Manifest(t)
	if m.ValidationGates.DiscoveryScreenMethod != ValidationDiscoveryScreenMethod {
		t.Errorf("discovery screen method = %q, want %q", m.ValidationGates.DiscoveryScreenMethod, ValidationDiscoveryScreenMethod)
	}
	if !m.ValidationGates.NoFinalHolm {
		t.Errorf("validation must not apply final Holm at protocol stage")
	}
	if m.ValidationGates.DayBlockSignTest.MultipleComparisonControl != ValidationMultipleComparisonControl ||
		m.ValidationGates.DayBlockSignTest.FamilyQ != ValidationFDRQ ||
		m.ValidationGates.DayBlockSignTest.MinNonzeroBlocks != ValidationMinIndependentBlocks {
		t.Errorf("validation BH-FDR screen drifted: %+v", m.ValidationGates.DayBlockSignTest)
	}
	if m.ValidationGates.MinUniqueDays != 10 {
		t.Errorf("validation min_unique_days = %d, want unified 10", m.ValidationGates.MinUniqueDays)
	}
}

func TestSymbolRealizedVolRMSOneBarNonZero(t *testing.T) {
	vol := SymbolRealizedVolRMSBPS([]float64{0.01})
	if vol <= 0 || math.IsNaN(vol) {
		t.Fatalf("single-bar RMS vol = %v, want > 0", vol)
	}
	if math.Abs(vol-100) > 1e-9 {
		t.Fatalf("single-bar RMS vol = %v, want 100 bps for 1%% return", vol)
	}
	ratio := VolatilityCostRatioFromRMS(vol)
	if math.IsNaN(ratio) || ratio <= 0 {
		t.Fatalf("volatility_cost_ratio from RMS = %v, want finite > 0", ratio)
	}
	if math.Abs(ratio-vol/RoundtripTakerFeeBPS) > 1e-12 {
		t.Fatalf("volatility_cost_ratio = %v, want %v", ratio, vol/RoundtripTakerFeeBPS)
	}
}

func TestBenjaminiHochbergFDRDiscoveryScreen(t *testing.T) {
	pValues := []float64{0.001, 0.04, 0.08, 0.20}
	pass, err := BenjaminiHochbergFDR(pValues, ValidationFDRQ)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass) != len(pValues) {
		t.Fatalf("pass length = %d, want %d", len(pass), len(pValues))
	}
	if !pass[0] || !pass[1] {
		t.Fatalf("expected first two hypotheses to pass BH-FDR q=%v, got %v", ValidationFDRQ, pass)
	}
	if pass[2] || pass[3] {
		t.Fatalf("expected last two hypotheses to fail BH-FDR q=%v, got %v", ValidationFDRQ, pass)
	}
}

func TestMaxDayPositiveContributionCap(t *testing.T) {
	got := MaxDayPositiveContributionFraction(map[string]float64{
		"2026-01-01": 35,
		"2026-01-02": 65,
	})
	if math.Abs(got-0.65) > 1e-12 {
		t.Fatalf("max day contribution = %v, want 0.65", got)
	}
	if ValidationMaxDayPositiveContributionFraction != 0.35 ||
		ValidationMaxSingleEventContributionFraction != 0.25 {
		t.Fatal("contribution caps drifted")
	}
}

func TestPhenomenonIDDedupeIgnoresCapitalTier(t *testing.T) {
	base := PhenomenonIdentity{
		ProfileID: "okx_1h", SkyCoarseMarket: "mid", Horizon: "4h", Direction: "long",
		Conditions: []PhenomenonCondition{{FeatureKey: "symbol_return_4h", Op: "gte_quantile", Threshold: 12, Quantile: 0.75}},
	}
	idA, err := PhenomenonID(base)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := PhenomenonID(base)
	if err != nil {
		t.Fatal(err)
	}
	if idA != idB {
		t.Fatalf("same phenomenon ids differ: %q vs %q", idA, idB)
	}
	other := base
	other.Conditions = []PhenomenonCondition{{FeatureKey: "symbol_return_1h", Op: "gte_quantile", Threshold: 5, Quantile: 0.5}}
	idC, err := PhenomenonID(other)
	if err != nil {
		t.Fatal(err)
	}
	if idA == idC {
		t.Fatal("different conditions must not dedupe to same phenomenon id")
	}
}

func TestFeatureCatalogSummaryFields(t *testing.T) {
	summary := BuildFeatureCatalogSummary(map[string][]string{
		"okx_1h": {"symbol_return_1h"},
	}, []string{"symbol_return_1h"})
	if len(summary.DeclaredDistinctFeatureNames) == 0 {
		t.Fatal("declared distinct feature names must be non-empty")
	}
	if len(summary.AvailableProfileFeatureSlots["okx_1h"]) != 1 {
		t.Fatalf("available slots = %v", summary.AvailableProfileFeatureSlots)
	}
	if len(summary.CoveredFeatureNames) != 1 {
		t.Fatalf("covered names = %v", summary.CoveredFeatureNames)
	}
	if len(summary.MissingDeclaredFeatureNames) == 0 {
		t.Fatal("missing declared should be non-empty when only one feature covered")
	}
	assertStrSlice(t, "feature catalog summary fields", FeatureCatalogSummaryFields, []string{
		"declared_distinct_feature_names",
		"available_profile_feature_slots",
		"covered_feature_names",
		"missing_declared_feature_names",
	})
}

func TestReverseMineProtocolVersionBind(t *testing.T) {
	if err := ValidateReverseMineBinding(ReverseMineSearchSpaceID, DerivationAlgorithmVersion); err != nil {
		t.Fatalf("protocol binding rejected canonical values: %v", err)
	}
	if err := ValidateReverseMineBinding("other.space", DerivationAlgorithmVersion); err == nil {
		t.Fatal("wrong search space must fail binding")
	}
	if err := ValidateReverseMineBinding(ReverseMineSearchSpaceID, "other-algorithm"); err == nil {
		t.Fatal("wrong derivation algorithm must fail binding")
	}
}

func TestValidationBlocksMeetMinimum(t *testing.T) {
	start := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	for _, horizon := range []time.Duration{time.Hour, 4 * time.Hour} {
		blocks := UTCAnchoredNonOverlapBlocks(start, end, horizon)
		if len(blocks) < ValidationMinIndependentBlocks {
			t.Fatalf("horizon=%s blocks=%d want >= %d", horizon, len(blocks), ValidationMinIndependentBlocks)
		}
	}
}

func TestExactOneSidedPositiveSignP(t *testing.T) {
	got, err := ExactOneSidedPositiveBinomialSignP(8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got <= 0 || got > 1 {
		t.Fatalf("one-sided p = %v out of range", got)
	}
	extreme, err := ExactOneSidedPositiveBinomialSignP(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if extreme >= got {
		t.Fatalf("10/0 positive one-sided p=%v should be more extreme than 8/2 p=%v", extreme, got)
	}
}

func TestMechanismAttributionNoDrift(t *testing.T) {
	assertMechanismAttributionManifest(t, loadG4Manifest(t))
}

func assertMechanismAttributionManifest(t *testing.T, m g4Manifest) {
	t.Helper()
	a := m.MechanismAttribution
	if a.Version != AttributionVersion ||
		a.DiscoveryRequiresPayer ||
		a.FeatureMembershipAutoExplained != FeatureMembershipAutoExplained ||
		a.G4IndependentEvidenceAvailable != G4IndependentEvidenceAvailable ||
		a.AutomaticExplainedCount != AutomaticExplainedCount {
		t.Errorf("mechanism attribution version/discovery payer drifted: %+v", a)
	}
	assertStrSlice(t, "mechanism_attribution.statuses", a.Statuses, AttributionStatuses)
	assertStrSlice(t, "mechanism_attribution.evaluation_order", a.EvaluationOrder, AttributionEvaluationOrder)
	assertStrSlice(t, "mechanism_attribution.graveyard_feature_families", a.GraveyardFeatureFamilies, GraveyardFeatureFamilies)
	assertStrSlice(t, "mechanism_attribution.unexplained_feature_classes", a.UnexplainedFeatureClasses, UnexplainedFeatureClasses)
	evidence := a.IndependentEvidenceContract
	if !evidence.RequiredForExplained ||
		evidence.CurrentGenerationSatisfied ||
		evidence.FutureBinding != IndependentEvidenceFutureBinding {
		t.Errorf("independent evidence contract drifted: %+v", evidence)
	}
	assertStrSlice(t, "independent_evidence_contract.must_include", evidence.MustInclude, IndependentEvidenceMustInclude)
	assertStrSlice(t, "independent_evidence_contract.must_not_be_derived_from", evidence.MustNotBeDerivedFrom, IndependentEvidenceForbiddenSources)
	if a.CrossTemplateResolution != CrossTemplateResolution ||
		a.NarrativeRejectionRule != NarrativeRejectionRule {
		t.Errorf("mechanism attribution resolution/narrative rule drifted")
	}
	if len(a.Templates) != len(MechanismTemplates) {
		t.Fatalf("mechanism templates length = %d, want %d", len(a.Templates), len(MechanismTemplates))
	}
	for i, want := range MechanismTemplates {
		got := a.Templates[i]
		if got.ID != want.ID ||
			got.Mechanism != want.Mechanism ||
			got.Payer != want.Payer ||
			got.ForcedBehavior != want.ForcedBehavior ||
			got.Deadline != want.Deadline ||
			got.CapacityDefense != want.CapacityDefense ||
			got.MatchStatus != AttributionMechanismHypothesis ||
			got.Trade || got.Exam || got.CertifiedLeaf {
			t.Errorf("mechanism templates[%d] scalar fields drifted: got %+v want %+v", i, got, want)
		}
		assertStrSlice(t, "mechanism template required_all_features", got.RequiredAllFeatures, want.RequiredAllFeatures)
		assertStrSlice(t, "mechanism template required_any_features", got.RequiredAnyFeatures, want.RequiredAnyFeatures)
	}
}

func TestMechanismHypothesisNeverAutoExplained(t *testing.T) {
	tests := []struct {
		name             string
		features         []string
		claimedMechanism string
		want             string
	}{
		{
			name:     "cross asset exact conjunction",
			features: []string{"btc_relative_return", "btc_beta_proxy"},
			want:     AttributionMechanismHypothesis,
		},
		{
			name:     "session exact conjunction with common volume",
			features: []string{"time_of_day_session", "quote_volume_24h_log_bucket"},
			want:     AttributionMechanismHypothesis,
		},
		{
			name:     "session exact conjunction with profile volume",
			features: []string{"time_of_day_session", "profile_quote_volume_24h_log_bucket"},
			want:     AttributionMechanismHypothesis,
		},
		{
			name:     "cross template is ambiguous",
			features: []string{"btc_relative_return", "btc_beta_proxy", "time_of_day_session", "quote_volume_24h_log_bucket"},
			want:     AttributionAmbiguous,
		},
		{
			name:     "price return alone has no payer",
			features: []string{"symbol_return_1h"},
			want:     AttributionUnexplained,
		},
		{
			name:     "volatility and liquidity have no payer",
			features: []string{"symbol_realized_vol_4h", "quote_volume_24h_log_bucket"},
			want:     AttributionUnexplained,
		},
		{
			name:             "narrative claim cannot override unmatched features",
			features:         []string{"symbol_return_1h"},
			claimedMechanism: CrossAssetMechanism,
			want:             AttributionUnexplained,
		},
		{
			name:     "graveyard feature cannot inherit old payer",
			features: []string{"okx_bybit_funding_spread", "btc_relative_return", "btc_beta_proxy"},
			want:     AttributionUnexplained,
		},
		{
			name:             "claimed graveyard mechanism is terminal",
			features:         []string{"symbol_return_1h"},
			claimedMechanism: "funding_payer_flip_rebalance",
			want:             AttributionGraveyard,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAttribution(tc.features, tc.claimedMechanism); got != tc.want {
				t.Errorf("ClassifyAttribution(%v, %q) = %q, want %q",
					tc.features, tc.claimedMechanism, got, tc.want)
			}
		})
	}
	if got := ClassifyAttributionWithIndependentEvidence(
		[]string{"btc_relative_return", "btc_beta_proxy"}, "", true,
	); got != AttributionExplained {
		t.Errorf("future independently evidenced template = %q, want EXPLAINED", got)
	}
}

func TestNarrativeAttributionRejected(t *testing.T) {
	if got := ClassifyAttribution(
		[]string{"btc_relative_return", "btc_beta_proxy"}, CrossAssetMechanism,
	); got != AttributionMechanismHypothesis {
		t.Errorf("template membership with matching narrative = %q, want hypothesis", got)
	}
	if got := ClassifyAttribution(
		[]string{"btc_relative_return", "btc_beta_proxy"}, SessionMechanism,
	); got != AttributionUnexplained {
		t.Errorf("mismatched mechanism narrative = %q, want unexplained", got)
	}
	if AutomaticExplainedCount != 0 || FeatureMembershipAutoExplained {
		t.Fatal("G4 automatic EXPLAINED path must remain closed")
	}
}

func TestPostValidationRoutingNoDrift(t *testing.T) {
	assertPostValidationRoutingManifest(t, loadG4Manifest(t))
}

func assertPostValidationRoutingManifest(t *testing.T, m g4Manifest) {
	t.Helper()
	r := m.PostValidationRouting
	if r.ValidationPassUnexplainedStatus != UnexplainedResearchPassStatus ||
		r.ValidationPassHypothesisStatus != HypothesisResearchResultStatus ||
		r.UnexplainedPolicy != UnexplainedResearchPassPolicy ||
		r.HypothesisPolicy != HypothesisResearchResultPolicy ||
		r.AmbiguousPolicy != AmbiguousPostValidationPolicy ||
		r.GraveyardPolicy != GraveyardPostValidationPolicy ||
		r.StrictWFEligibility != StrictWFEligibility ||
		r.CurrentAutomaticStrictWFEntries != CurrentAutomaticStrictWFEntries {
		t.Errorf("post_validation_routing drifted: %+v", r)
	}
}

func TestStrictWalkForwardNoDrift(t *testing.T) {
	assertStrictWalkForwardManifest(t, loadG4Manifest(t))
}

func assertStrictWalkForwardManifest(t *testing.T, m g4Manifest) {
	t.Helper()
	wf := m.StrictWalkForward
	if wf.Partition != StrictWFPartition ||
		wf.UsesProfilePurgeEmbargo != StrictWFUsesProfilePurgeEmbargo ||
		wf.Eligibility != StrictWFEligibility ||
		wf.CurrentAutomaticEntries != CurrentAutomaticStrictWFEntries ||
		wf.MinFirings != StrictWFMinFirings ||
		wf.MinUniqueDays != StrictWFMinUniqueDays ||
		wf.MinUniqueSymbols != StrictWFMinUniqueSymbols ||
		wf.MinIndependentBlocks != StrictWFMinIndependentBlocks ||
		wf.MeanComparison != StrictWFMeanComparison ||
		wf.MaxSingleEventContributionFraction != StrictWFMaxSingleEventContributionFraction ||
		wf.MaxCapacityFailFraction != StrictWFMaxCapacityFailFraction ||
		wf.CapacityFailComparison != StrictWFCapacityFailComparison ||
		wf.PassState != StrictWFPassState ||
		wf.GateFailureState != StrictWFGateFailureState ||
		wf.InsufficientSampleState != StrictWFInsufficientSampleState ||
		wf.ResearchPassPolicy != StrictWFResearchPassPolicy ||
		wf.ExamActions != ExamActions {
		t.Errorf("strict_walk_forward scalar fields drifted: %+v", wf)
	}
	assertStrSlice(t, "strict_walk_forward.strict_positive_mean_fields", wf.StrictPositiveMeanFields, StrictWFPositiveMeanFields)
	assertStrSlice(t, "strict_walk_forward.certification_states", wf.CertificationStates, StrictWFCertificationStates)
	sign := wf.DayBlockSignTest
	if !sign.Required ||
		sign.BlockUnit != StrictWFDayBlockUnit ||
		sign.ObservationWindowHours != StrictWFObservationWindowHours ||
		sign.EmbargoGap != StrictWFEmbargoGap ||
		sign.HorizonIndependence != StrictWFHorizonIndependence ||
		sign.OverlapRule != StrictWFOverlapRule ||
		sign.PValue != StrictWFSignPValue ||
		sign.NumericMethod != StrictWFSignNumericMethod ||
		sign.IntegerCombinationCountsForbidden != StrictWFIntegerCombinationsForbidden ||
		sign.MultipleComparisonControl != StrictWFMultipleComparisonControl ||
		sign.Family != StrictWFFamily ||
		sign.FamilyAlpha != StrictWFFamilyAlpha {
		t.Errorf("strict_walk_forward.day_block_sign_test drifted: %+v", sign)
	}
}

func TestFeatureCellRoundRobinIntersection(t *testing.T) {
	features := map[string][]string{
		"okx_1h": {"f1", "f2", "f3"},
		"okx_5m": {"g1", "g2", "g3"},
	}
	var cells []DiscoveryCell
	for _, profile := range []string{"okx_1h", "okx_5m"} {
		for i, horizon := range []string{"1h", "4h"} {
			for _, direction := range []string{"long", "short"} {
				cells = append(cells, DiscoveryCell{
					ProfileID:   profile,
					Horizon:     horizon,
					Direction:   direction,
					CapitalTier: []int{30, 100}[i],
					CellID:      profile + "/" + horizon + "/" + direction,
				})
			}
		}
	}

	firstRoundSize := len(features["okx_1h"]) + len(features["okx_5m"])
	assignments := FeatureCellRoundRobin(features, cells, firstRoundSize*2)
	if len(assignments) != firstRoundSize*2 {
		t.Fatalf("assignments=%d want=%d", len(assignments), firstRoundSize*2)
	}
	firstCounts := map[string]int{}
	for _, assignment := range assignments[:firstRoundSize] {
		firstCounts[assignment.Cell.ProfileID+"/"+assignment.Feature]++
		if assignment.AllocationRound != 1 {
			t.Fatalf("first feature cycle leaked into round %d", assignment.AllocationRound)
		}
	}
	for profile, profileFeatures := range features {
		for _, feature := range profileFeatures {
			if firstCounts[profile+"/"+feature] != 1 {
				t.Errorf("%s/%s first-round allocations=%d, want 1",
					profile, feature, firstCounts[profile+"/"+feature])
			}
		}
	}

	profiles, horizons, directions, capitals, cellIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[int]bool{}, map[string]bool{}
	for _, assignment := range assignments {
		profiles[assignment.Cell.ProfileID] = true
		horizons[assignment.Cell.Horizon] = true
		directions[assignment.Cell.Direction] = true
		capitals[assignment.Cell.CapitalTier] = true
		cellIDs[assignment.Cell.CellID] = true
	}
	if len(profiles) != 2 || len(horizons) != 2 || len(directions) != 2 ||
		len(capitals) != 2 || len(cellIDs) < 6 {
		t.Fatalf("feature×cell schedule lost strata: profiles=%v horizons=%v directions=%v capitals=%v cells=%d",
			profiles, horizons, directions, capitals, len(cellIDs))
	}
}

func TestUTCAnchoredBlocksDoNotOverlap(t *testing.T) {
	start := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	for _, horizon := range []time.Duration{5 * time.Minute, time.Hour, 4 * time.Hour, 24 * time.Hour} {
		blocks := UTCAnchoredNonOverlapBlocks(start, end, horizon)
		if len(blocks) < StrictWFMinIndependentBlocks {
			t.Fatalf("horizon=%s blocks=%d want >=%d", horizon, len(blocks), StrictWFMinIndependentBlocks)
		}
		for i, block := range blocks {
			if block.Start.Hour() != 0 || block.Start.Minute() != 0 ||
				block.End.Sub(block.Start) != 24*time.Hour {
				t.Errorf("horizon=%s block[%d] is not UTC anchored 24h: %+v", horizon, i, block)
			}
			if i > 0 && block.Start.Before(blocks[i-1].End.Add(horizon)) {
				t.Errorf("horizon=%s blocks overlap embargo: prev=%+v next=%+v",
					horizon, blocks[i-1], block)
			}
		}
	}
}

func TestExactBinomialOverflowSafe(t *testing.T) {
	got, err := ExactTwoSidedBinomialSignP(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := 2.0 / 1024.0
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("p(0,10)=%g want=%g", got, want)
	}
	large, err := ExactTwoSidedBinomialSignP(4900, 5100)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(large) || math.IsInf(large, 0) || large <= 0 || large > 1 {
		t.Fatalf("overflow-unsafe large exact binomial p=%g", large)
	}
}

func TestFullMarketScopeNoDrift(t *testing.T) {
	m := loadG4Manifest(t)
	assertStrSlice(t, "full_market required fields",
		m.ProfitMap.FullMarketSemantics.RequiredSummaryFields,
		FullMarketRequiredSummaryFields)
	if err := ValidateFullMarketScope(FullMarketSummary{
		Available: 100, Selected: 100, FullMarket: true, Scope: FullMarketScope,
	}, 0, false); err != nil {
		t.Fatalf("valid full market rejected: %v", err)
	}
	if err := ValidateFullMarketScope(FullMarketSummary{
		Available: 100, Selected: 99, Truncated: true, Scope: TruncatedSmokeScope,
	}, 0, false); err == nil {
		t.Fatal("catalog_symbol_limit=0 silent truncation must fail")
	}
}

func TestTruncatedSmokeCannotEnterFormalPath(t *testing.T) {
	summary := FullMarketSummary{
		Available: 100, Selected: 20, Truncated: true, Scope: TruncatedSmokeScope,
	}
	if err := ValidateFullMarketScope(summary, 20, true); err != nil {
		t.Fatalf("explicit truncated smoke rejected: %v", err)
	}
	if FormalPathAllowed(summary.Scope) {
		t.Fatal("TRUNCATED_SMOKE entered formal candidate path")
	}
	if !FormalPathAllowed(FullMarketScope) {
		t.Fatal("FULL_MARKET should be eligible for formal path")
	}
}

func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadG4Manifest(t)
	if m.Binds.HistoryDataHash != HistoryDataHash {
		t.Errorf("binds.history_data_hash = %q, want %q", m.Binds.HistoryDataHash, HistoryDataHash)
	}
	if m.Binds.HistoryRegimeContractHash != HistoryRegimeContractHash {
		t.Errorf("binds.history_regime_contract_hash = %q, want %q", m.Binds.HistoryRegimeContractHash, HistoryRegimeContractHash)
	}
	if m.Binds.HistoryDataRequirementsHash != HistoryDataRequirementsHash {
		t.Errorf("binds.history_data_requirements_hash = %q, want %q", m.Binds.HistoryDataRequirementsHash, HistoryDataRequirementsHash)
	}
	if m.Binds.G1RouterProtocolHash != G1RouterProtocolHash {
		t.Errorf("binds.g1_router_protocol_hash = %q, want %q", m.Binds.G1RouterProtocolHash, G1RouterProtocolHash)
	}
	if m.Binds.G2RouterProtocolHash != G2RouterProtocolHash {
		t.Errorf("binds.g2_router_protocol_hash = %q, want %q", m.Binds.G2RouterProtocolHash, G2RouterProtocolHash)
	}
	if m.Binds.G3RouterProtocolHash != G3RouterProtocolHash {
		t.Errorf("binds.g3_router_protocol_hash = %q, want %q", m.Binds.G3RouterProtocolHash, G3RouterProtocolHash)
	}
	assertStrSlice(t, "closes_g3.compatible_prior_profit_map_protocol_hashes",
		m.ClosesG3.CompatiblePriorProfitMapProtocolHashes, CompatiblePriorProfitMapProtocolHashes)
	if m.ClosesG3.ProfitMapCompatibilityScope != ProfitMapCompatibilityScope {
		t.Errorf("profit map compatibility scope drifted")
	}
}

func TestBoundHashesMatchReferencedContracts(t *testing.T) {
	m := loadG4Manifest(t)

	regime, err := researchboundary.LoadRegimeContract(filepath.Clean(regimeManifestRelPath))
	if err != nil {
		t.Fatalf("load regime contract: %v", err)
	}
	if regime.ManifestHash == nil || *regime.ManifestHash != m.Binds.HistoryRegimeContractHash {
		t.Errorf("regime contract hash = %v, g4 binds = %q", regime.ManifestHash, m.Binds.HistoryRegimeContractHash)
	}

	req, err := researchboundary.LoadRequirements(filepath.Clean(reqManifestRelPath))
	if err != nil {
		t.Fatalf("load requirements: %v", err)
	}
	if req.ManifestHash == nil || *req.ManifestHash != m.Binds.HistoryDataRequirementsHash {
		t.Errorf("requirements hash = %v, g4 binds = %q", req.ManifestHash, m.Binds.HistoryDataRequirementsHash)
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{g1ManifestRelPath, G1RouterProtocolHash},
		{g2ManifestRelPath, G2RouterProtocolHash},
		{g3ManifestRelPath, G3RouterProtocolHash},
	} {
		got := reproduceProtocolHash(t, tc.path)
		if got != tc.want {
			t.Errorf("reproduced hash for %s = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestG1G2G3ResultReferencedImmutable(t *testing.T) {
	m := loadG4Manifest(t)
	if m.ClosesG1.G1Generation != G1Generation || m.ClosesG1.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.ClosesG1.G1CertifiedMoneyLeaves != G1CertifiedMoneyLeaves ||
		m.ClosesG1.G1ResearchPassLeaves != G1ResearchPassLeaves ||
		m.ClosesG1.G1SystemStatus != G1SystemStatus {
		t.Errorf("closes_g1 drift: %+v", m.ClosesG1)
	}
	if m.ClosesG2.G2Generation != G2Generation || m.ClosesG2.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.ClosesG2.G2RegisteredSearchCombinations != G2RegisteredSearchCombos ||
		m.ClosesG2.G2CandidateNominations != G2CandidateNominations ||
		m.ClosesG2.G2ResearchPassLeaves != G2ResearchPassLeaves ||
		m.ClosesG2.G2SystemStatus != G2SystemStatus {
		t.Errorf("closes_g2 drift: %+v", m.ClosesG2)
	}
	if m.ClosesG3.G3Generation != G3Generation || m.ClosesG3.G3RouterProtocolHash != G3RouterProtocolHash ||
		m.ClosesG3.G3RegisteredSearchRules != G3RegisteredSearchRules ||
		m.ClosesG3.G3CandidateNominations != G3CandidateNominations ||
		m.ClosesG3.G3ResearchPassLeaves != G3ResearchPassLeaves ||
		m.ClosesG3.G3SystemStatus != G3SystemStatus {
		t.Errorf("closes_g3 drift: %+v", m.ClosesG3)
	}

	assertG1StageSummary(t)
	assertG2StageSummary(t)
	assertG3StageSummary(t)
}

func assertG1StageSummary(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g1StageSummaryRelPath))
	if err != nil {
		t.Fatalf("read g1 stage summary: %v", err)
	}
	var summary struct {
		Generation    string `json:"generation"`
		ProtocolHash  string `json:"protocol_hash"`
		ResearchSmoke struct {
			CandidateNominations int `json:"candidate_nominations"`
		} `json:"research_smoke"`
		LeafStatus struct {
			ResearchPass []string `json:"research_pass"`
		} `json:"leaf_status"`
		SystemStatus string `json:"system_status"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("decode g1 stage summary: %v", err)
	}
	if summary.Generation != G1Generation || summary.ProtocolHash != G1RouterProtocolHash ||
		summary.ResearchSmoke.CandidateNominations != 0 ||
		len(summary.LeafStatus.ResearchPass) != G1ResearchPassLeaves ||
		summary.SystemStatus != G1SystemStatus {
		t.Errorf("g1 stage summary drifted: %+v", summary)
	}
}

func assertG2StageSummary(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g2StageSummaryRelPath))
	if err != nil {
		t.Fatalf("read g2 stage summary: %v", err)
	}
	var summary struct {
		Generation   string `json:"generation"`
		ProtocolHash string `json:"protocol_hash"`
		Search       struct {
			RegisteredCombinations int `json:"registered_combinations"`
			CandidateNominations   int `json:"candidate_nominations"`
		} `json:"search"`
		LeafStatus struct {
			ResearchPass []string `json:"research_pass"`
		} `json:"leaf_status"`
		SystemStatus string `json:"system_status"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("decode g2 stage summary: %v", err)
	}
	if summary.Generation != G2Generation || summary.ProtocolHash != G2RouterProtocolHash ||
		summary.Search.RegisteredCombinations != G2RegisteredSearchCombos ||
		summary.Search.CandidateNominations != G2CandidateNominations ||
		len(summary.LeafStatus.ResearchPass) != G2ResearchPassLeaves ||
		summary.SystemStatus != G2SystemStatus {
		t.Errorf("g2 stage summary drifted: %+v", summary)
	}
}

func assertG3StageSummary(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g3StageSummaryRelPath))
	if err != nil {
		t.Fatalf("read g3 stage summary: %v", err)
	}
	var summary struct {
		Generation   string `json:"generation"`
		ProtocolHash string `json:"protocol_hash"`
		Induction    struct {
			ActualDerivedRules   int `json:"actual_derived_rules"`
			CandidateNominations int `json:"candidate_nominations"`
		} `json:"induction"`
		Research struct {
			ResearchPassLeaves int `json:"research_pass_leaves"`
		} `json:"research"`
		SystemStatus string `json:"system_status"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("decode g3 stage summary: %v", err)
	}
	if summary.Generation != G3Generation || summary.ProtocolHash != G3RouterProtocolHash ||
		summary.Induction.ActualDerivedRules != G3RegisteredSearchRules ||
		summary.Induction.CandidateNominations != G3CandidateNominations ||
		summary.Research.ResearchPassLeaves != G3ResearchPassLeaves ||
		summary.SystemStatus != G3SystemStatus {
		t.Errorf("g3 stage summary drifted: %+v", summary)
	}
}

func reproduceProtocolHash(t *testing.T, relPath string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(relPath))
	if err != nil {
		t.Fatalf("read manifest %s: %v", relPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode manifest %s: %v", relPath, err)
	}
	audit, ok := doc["audit"].(map[string]any)
	if !ok {
		t.Fatalf("manifest %s audit missing or not an object", relPath)
	}
	audit["protocol_hash"] = nil
	hash, _, err := datatruth.HashJSON(doc)
	if err != nil {
		t.Fatalf("HashJSON %s: %v", relPath, err)
	}
	return hash
}

func TestProtocolHashMatchesManifest(t *testing.T) {
	want := reproduceProtocolHash(t, g4ManifestRelPath)
	m := loadG4Manifest(t)
	if m.Audit.ProtocolHash == nil {
		t.Fatalf("audit.protocol_hash is null; fill it with reproducible hash: %s", want)
	}
	if *m.Audit.ProtocolHash != want {
		t.Fatalf("audit.protocol_hash = %q, want %q (recompute with audit.protocol_hash=null)", *m.Audit.ProtocolHash, want)
	}
	if RouterProtocolHash != want {
		t.Fatalf("RouterProtocolHash constant = %q, want %q", RouterProtocolHash, want)
	}
}

func assertStrSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d (%v vs %v)", name, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}

func assertIntSlice(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d (%v vs %v)", name, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %d, want %d", name, i, got[i], want[i])
		}
	}
}

func assertFloatSlice(t *testing.T, name string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d (%v vs %v)", name, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %v, want %v", name, i, got[i], want[i])
		}
	}
}

func checkInt(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}
