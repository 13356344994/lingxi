package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingxi/internal/datatruth"
	"lingxi/internal/researchboundary"
)

// 只读取公开的 g3 路由协议 manifest、被绑定的公开契约(regime / requirements / g1/g2 路由协议)与 g1/g2 阶段结论。
// 绝不读取任何真实 records/收益/考卷或大 history 目录。
const (
	g3ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g3_preregistration.json"
	g2ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	g2StageSummaryRelPath = "../../../research/firstbattle/g2_stage_summary.json"
	regimeManifestRelPath = "../../../research/firstbattle/history_regime_contract.json"
	reqManifestRelPath    = "../../../research/firstbattle/history_data_requirements.json"
)

type mechanismFamilyManifest struct {
	Family              string   `json:"family"`
	Features            []string `json:"features"`
	Mechanism           string   `json:"mechanism"`
	Payer               string   `json:"payer"`
	ForcedBehavior      string   `json:"forced_behavior"`
	CannotWaitReason    string   `json:"cannot_wait_reason"`
	CompetitionDefense  string   `json:"competition_defense"`
	DeadlineTemplate    string   `json:"deadline_template"`
	InvalidatorTemplate []string `json:"invalidator_template"`
	DistinctFromG1G2    string   `json:"distinct_from_g1_g2"`
}

type mechanismAttributionManifest struct {
	Version             string                    `json:"version"`
	MechanismFamilies   []mechanismFamilyManifest `json:"mechanism_families"`
	ConditionerFeatures []string                  `json:"conditioner_features"`
	ConditionerRule     string                    `json:"conditioner_rule"`
	RuleResolution      struct {
		ZeroMechanismFeatures             string `json:"zero_mechanism_features"`
		OneMechanismFamily                string `json:"one_mechanism_family"`
		MultipleFeaturesSameFamily        string `json:"multiple_features_same_mechanism_family"`
		MultipleDifferentFamilies         string `json:"multiple_different_mechanism_families"`
		ConditionerPlusOneMechanismFamily string `json:"conditioner_plus_one_mechanism_family"`
		ClosedRulesStillChargeAlpha       bool   `json:"closed_rules_still_charge_alpha"`
	} `json:"rule_resolution"`
	G1G2MechanismGraveyard []string `json:"g1_g2_mechanism_graveyard"`
	GraveyardRule          string   `json:"graveyard_rule"`
}

type g3Manifest struct {
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

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
		G2RouterProtocolHash        string `json:"g2_router_protocol_hash"`
	} `json:"binds"`

	DataFamilies struct {
		Families                     []string `json:"families"`
		OptionalNonblocking          []string `json:"optional_nonblocking"`
		ExcludedFromG3RuleGeneration []string `json:"excluded_from_g3_rule_generation"`
	} `json:"data_families"`

	NodeOrder   []string `json:"node_order"`
	LeafStates  []string `json:"leaf_states"`
	ReasonCodes []string `json:"reason_codes"`

	LeafStateMachine struct {
		Order          []string `json:"order"`
		TerminalClosed []string `json:"terminal_closed"`
	} `json:"leaf_state_machine"`

	CoarseTraceRule struct {
		MustNotContain []string `json:"must_not_contain"`
	} `json:"coarse_trace_rule"`

	NodeInputs struct {
		Sorter struct {
			MinQuoteVolume         float64 `json:"min_quote_volume"`
			MinVolatilityCostRatio float64 `json:"min_volatility_cost_ratio"`
		} `json:"Sorter"`
	} `json:"node_inputs"`

	Sky struct {
		TimescalesHours []int     `json:"timescales_hours"`
		Binning         string    `json:"binning"`
		Quantiles       []float64 `json:"quantiles"`
		FitOn           string    `json:"fit_on"`
		MissingAction   string    `json:"missing_action"`
		BindsRegimeHash string    `json:"binds_regime_contract_hash"`
		Dimensions      []struct {
			Name string `json:"name"`
		} `json:"dimensions"`
	} `json:"sky"`

	FeatureCatalog struct {
		MaxFeaturesPerRule     int       `json:"max_features_per_rule"`
		MinFeaturesPerRule     int       `json:"min_features_per_rule"`
		ThresholdSource        string    `json:"threshold_source"`
		ThresholdQuantiles     []float64 `json:"threshold_quantiles"`
		ForbiddenFeatureInputs []string  `json:"forbidden_feature_inputs"`
		Categories             []struct {
			Features []struct {
				Name              string   `json:"name"`
				Inputs            []string `json:"inputs"`
				MaxStalenessHours *int     `json:"max_staleness_hours"`
				StaleAction       string   `json:"stale_action"`
			} `json:"features"`
		} `json:"categories"`
	} `json:"feature_catalog"`

	FeatureMechanismAttribution mechanismAttributionManifest `json:"feature_to_economic_mechanism_attribution"`

	DiscoveryBudget struct {
		OutcomeHorizonsHours []int   `json:"outcome_horizons_hours"`
		DirectionSymmetry    string  `json:"direction_symmetry"`
		CostBPS              float64 `json:"cost_bps"`
		NeutralBandBPS       float64 `json:"neutral_band_bps"`
		Forward              struct {
			MaxFamilies            int `json:"max_families"`
			MaxCandidatesPerFamily int `json:"max_candidates_per_family"`
		} `json:"forward"`
		ReverseInduction struct {
			MaxFiles                       int            `json:"max_files"`
			MaxLines                       int            `json:"max_lines"`
			MaxSingleFeatureRules          int            `json:"max_single_feature_rules"`
			MaxDualFeatureRules            int            `json:"max_dual_feature_rules"`
			MaxDerivedSearchRules          int            `json:"max_derived_search_rules"`
			AllowedSearchSpaceID           string         `json:"allowed_search_space_id"`
			DerivationAlgorithmVersion     string         `json:"derivation_algorithm_version"`
			DerivationAlgorithm            map[string]any `json:"derivation_algorithm"`
			AllowedSearchDefinitionHash    string         `json:"allowed_search_space_definition_hash"`
			ActualDerivedRuleCount         *int           `json:"actual_derived_rule_count"`
			ActualGeneratedAttemptCount    *int           `json:"actual_generated_attempt_count"`
			ActualDerivedRulesBinding      string         `json:"actual_derived_rules_binding"`
			SealedSearchManifestRequired   bool           `json:"sealed_search_manifest_required"`
			MaxCandidates                  int            `json:"max_candidates"`
			DefaultProgressEvery           int            `json:"default_progress_every"`
			MatchRule                      string         `json:"match_rule"`
			MatchedControlsRequired        bool           `json:"matched_controls_required"`
			ControlsNontriggerOutcomeBlind bool           `json:"controls_nontrigger_outcome_blind"`
			ControlsLossOrNeutralOnly      *bool          `json:"controls_loss_or_neutral_only"`
			ControlsOneToOneSameCluster    bool           `json:"controls_one_to_one_same_cluster"`
			NoCrossClusterControlReuse     bool           `json:"no_cross_cluster_control_reuse"`
			TreatmentIncludesAllTriggered  bool           `json:"treatment_includes_all_triggered_events"`
			WinnerOnlyRequiresNontrigger   bool           `json:"winner_only_rule_requires_nontrigger_controls"`
			VolatilityTolerance            float64        `json:"volatility_tolerance"`
			LiquidityTolerance             float64        `json:"liquidity_tolerance"`
			NeutralBandBPS                 float64        `json:"neutral_band_bps"`
			DiscoveryCostBPS               float64        `json:"discovery_cost_bps"`
			PermutationDraws               int            `json:"permutation_draws"`
			ExactPermutationMaxPairs       int            `json:"exact_permutation_max_pairs"`
			BootstrapSamples               int            `json:"bootstrap_samples"`
			Seed                           int64          `json:"seed"`
			MatchWindowMs                  int64          `json:"match_window_ms"`
			BoundedSmoke                   struct {
				MaxSymbols      int `json:"max_symbols"`
				MaxFundingLines int `json:"max_funding_lines"`
				MaxPriceLines   int `json:"max_price_lines"`
			} `json:"bounded_smoke"`
			CheckpointRequired bool `json:"checkpoint_required"`
		} `json:"reverse_induction"`
		MaxTotalCandidates int `json:"max_total_candidates"`
	} `json:"discovery_budget"`

	TimePartitionRoles struct {
		ReverseInductionPermille int `json:"reverse_induction_permille"`
		RecheckPermille          int `json:"recheck_permille"`
		ForwardPermille          int `json:"forward_permille"`
		WalkForwardPermille      int `json:"walk_forward_permille"`
		EmbargoHours             int `json:"embargo_hours"`
		MinInductionClusters     int `json:"min_induction_clusters"`
		MinForwardSamples        int `json:"min_forward_samples"`
		MinForwardClusters       int `json:"min_forward_clusters"`
		MinRecheckSamples        int `json:"min_recheck_samples"`
		MinRecheckClusters       int `json:"min_recheck_clusters"`
		WalkForwardFolds         int `json:"walk_forward_folds"`
		WFMinSamples             int `json:"wf_min_samples"`
		WFMinClusters            int `json:"wf_min_clusters"`
		PurgeHours               int `json:"purge_hours"`
		WFEmbargoHours           int `json:"wf_embargo_hours"`
		ExamActions              int `json:"exam_actions"`
	} `json:"time_partition_roles"`

	MultipleComparisonBudget struct {
		FamilyWiseControl       string  `json:"family_wise_control"`
		FamilyWiseAlpha         float64 `json:"family_wise_alpha"`
		ReverseInductionAlpha   float64 `json:"reverse_induction_alpha"`
		RecheckAlpha            float64 `json:"recheck_alpha"`
		ForwardAlpha            float64 `json:"forward_alpha"`
		WalkForwardAlpha        float64 `json:"walk_forward_alpha"`
		ReverseInductionControl string  `json:"reverse_induction_selection_control"`
		RecheckControl          string  `json:"recheck_control"`
		ForwardControl          string  `json:"forward_walk_forward_control"`
		MaxDerivedSearchRules   int     `json:"max_derived_search_rules"`
		AlphaLedger             struct {
			Required                           bool     `json:"required"`
			AppendOnly                         bool     `json:"append_only"`
			OneEntryPerGeneratedCheckedAttempt bool     `json:"one_entry_per_generated_checked_attempt"`
			EntryOrder                         string   `json:"entry_order"`
			Statuses                           []string `json:"statuses"`
			MustRecord                         []string `json:"must_record"`
			AlphaChargeRule                    string   `json:"alpha_charge_rule"`
			ActualCountSource                  string   `json:"actual_count_source"`
			CannotDeleteGeneratedAttempts      bool     `json:"cannot_delete_generated_attempts"`
			ProvenanceRule                     string   `json:"provenance_rule"`
		} `json:"alpha_ledger"`
	} `json:"multiple_comparison_budget"`

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

func loadG3Manifest(t *testing.T) g3Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g3ManifestRelPath))
	if err != nil {
		t.Fatalf("read g3 manifest: %v", err)
	}
	var m g3Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g3 manifest: %v", err)
	}
	return m
}

func flattenFeatureCatalog(m g3Manifest) []string {
	var names []string
	for _, cat := range m.FeatureCatalog.Categories {
		for _, f := range cat.Features {
			names = append(names, f.Name)
		}
	}
	return names
}

// TestFeatureMechanismAttributionNoDrift 逐字段冻结 feature→economic mechanism 归因、
// conditioner 限制、双特征裁决、deadline/invalidator 模板和 G1/G2 机制墓地。
func TestFeatureMechanismAttributionNoDrift(t *testing.T) {
	m := loadG3Manifest(t)
	a := m.FeatureMechanismAttribution
	if a.Version != FeatureMechanismAttributionVersion {
		t.Errorf("attribution.version = %q, want %q", a.Version, FeatureMechanismAttributionVersion)
	}
	if len(a.MechanismFamilies) != len(MechanismAttributions) {
		t.Fatalf("mechanism_families length = %d, want %d", len(a.MechanismFamilies), len(MechanismAttributions))
	}
	mechanismFeatures := map[string]string{}
	for i, want := range MechanismAttributions {
		got := a.MechanismFamilies[i]
		if got.Family != want.Family ||
			got.Mechanism != want.Mechanism ||
			got.Payer != want.Payer ||
			got.ForcedBehavior != want.ForcedBehavior ||
			got.CannotWaitReason != want.CannotWaitReason ||
			got.CompetitionDefense != want.CompetitionDefense ||
			got.DeadlineTemplate != want.DeadlineTemplate ||
			got.DistinctFromG1G2 != want.DistinctFromG1G2 {
			t.Errorf("mechanism_families[%d] scalar fields drifted: got %+v want %+v", i, got, want)
		}
		assertStrSlice(t, "mechanism family features", got.Features, want.Features)
		assertStrSlice(t, "mechanism family invalidator_template", got.InvalidatorTemplate, want.InvalidatorTemplate)
		if got.Payer == "" || got.ForcedBehavior == "" || got.CannotWaitReason == "" ||
			got.CompetitionDefense == "" || got.DeadlineTemplate == "" || len(got.InvalidatorTemplate) == 0 {
			t.Errorf("mechanism_families[%d] has incomplete economic attribution", i)
		}
		for _, feature := range got.Features {
			if previous, duplicate := mechanismFeatures[feature]; duplicate {
				t.Errorf("mechanism feature %q belongs to both %q and %q", feature, previous, got.Family)
			}
			mechanismFeatures[feature] = got.Family
		}
	}

	assertStrSlice(t, "conditioner_features", a.ConditionerFeatures, ConditionerFeatures)
	if a.ConditionerRule != ConditionerRule {
		t.Errorf("conditioner_rule = %q, want %q", a.ConditionerRule, ConditionerRule)
	}
	for _, feature := range ConditionerFeatures {
		if family := mechanismFeatures[feature]; family != "" {
			t.Errorf("conditioner %q must not provide mechanism family %q", feature, family)
		}
	}
	if a.RuleResolution.ZeroMechanismFeatures != ZeroMechanismFeaturesResolution ||
		a.RuleResolution.OneMechanismFamily != OneMechanismFamilyResolution ||
		a.RuleResolution.MultipleFeaturesSameFamily != SameMechanismFamilyResolution ||
		a.RuleResolution.MultipleDifferentFamilies != DifferentMechanismFamiliesResolution ||
		a.RuleResolution.ConditionerPlusOneMechanismFamily != ConditionerPlusMechanismResolution ||
		a.RuleResolution.ClosedRulesStillChargeAlpha != ClosedRulesStillChargeAlpha {
		t.Errorf("rule_resolution drifted: %+v", a.RuleResolution)
	}
	assertStrSlice(t, "g1_g2_mechanism_graveyard", a.G1G2MechanismGraveyard, G1G2MechanismGraveyard)
	if a.GraveyardRule != MechanismGraveyardRule {
		t.Errorf("graveyard_rule = %q, want %q", a.GraveyardRule, MechanismGraveyardRule)
	}

	classified := map[string]bool{}
	for feature := range mechanismFeatures {
		classified[feature] = true
	}
	for _, feature := range ConditionerFeatures {
		classified[feature] = true
	}
	for _, feature := range FeatureCatalog {
		if !classified[feature] {
			t.Errorf("feature_catalog feature %q has no frozen mechanism/conditioner role", feature)
		}
	}
	if len(classified) != len(FeatureCatalog) {
		t.Errorf("attribution classifies %d features, feature catalog has %d", len(classified), len(FeatureCatalog))
	}
	for _, family := range MechanismAttributions {
		for _, dead := range G1G2MechanismGraveyard {
			if family.Mechanism == dead {
				t.Errorf("G3 mechanism %q is in G1/G2 graveyard", family.Mechanism)
			}
		}
	}
}

// TestManifestCodeConstantsNoDrift 冻结节点顺序、叶子状态、reason 码、司天监分桶、特征目录、发现预算、
// 多重检验与红队条数:manifest ↔ protocol.go 常量必须逐字段一致,任一漂移即失败。
func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG3Manifest(t)

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

	assertStrSlice(t, "node_order", m.NodeOrder, NodeOrder)
	assertStrSlice(t, "leaf_states", m.LeafStates, LeafStates)
	assertStrSlice(t, "leaf_state_machine.order", m.LeafStateMachine.Order, LeafStateOrder)
	assertStrSlice(t, "leaf_state_machine.terminal_closed", m.LeafStateMachine.TerminalClosed, TerminalClosedStates)
	assertStrSlice(t, "reason_codes", m.ReasonCodes, ReasonCodes)
	assertStrSlice(t, "coarse_trace_rule.must_not_contain", m.CoarseTraceRule.MustNotContain, CoarseTraceForbiddenSubstrings)
	assertStrSlice(t, "data_families.families", m.DataFamilies.Families, DataFamilies)
	assertStrSlice(t, "data_families.optional_nonblocking", m.DataFamilies.OptionalNonblocking, OptionalNonblockingDataFamilies)
	assertStrSlice(t, "data_families.excluded_from_g3_rule_generation", m.DataFamilies.ExcludedFromG3RuleGeneration, RuleGenerationExcludedDataFamilies)
	assertStrSlice(t, "feature_catalog names", flattenFeatureCatalog(m), FeatureCatalog)
	assertStrSlice(t, "feature_catalog.forbidden_feature_inputs", m.FeatureCatalog.ForbiddenFeatureInputs, ForbiddenFeatureInputs)

	if m.FeatureCatalog.MaxFeaturesPerRule != MaxFeaturesPerRule ||
		m.FeatureCatalog.MinFeaturesPerRule != MinFeaturesPerRule ||
		m.FeatureCatalog.ThresholdSource != ThresholdSource {
		t.Errorf("feature_catalog rule bounds drifted")
	}
	assertFloatSlice(t, "feature_catalog.threshold_quantiles", m.FeatureCatalog.ThresholdQuantiles, ThresholdQuantiles)
	for _, cat := range m.FeatureCatalog.Categories {
		for _, f := range cat.Features {
			for _, input := range f.Inputs {
				if input == "rubik_taker_5m" {
					t.Errorf("feature_catalog feature %q must not use optional_nonblocking rubik_taker_5m", f.Name)
				}
			}
			if strings.Contains(f.Name, "taker_imbalance_5m") {
				t.Errorf("feature_catalog must not contain G3 rule feature %q", f.Name)
			}
			if f.Name == "rubik_taker_imbalance_1h" || f.Name == "rubik_lsratio_1h" {
				if f.MaxStalenessHours == nil || *f.MaxStalenessHours != Rubik1hMaxStalenessHours ||
					f.StaleAction != Rubik1hStaleAction {
					t.Errorf("Rubik 1h feature %q staleness = %v/%q, want %dh/%q",
						f.Name, f.MaxStalenessHours, f.StaleAction, Rubik1hMaxStalenessHours, Rubik1hStaleAction)
				}
			}
		}
	}

	if m.Sky.Binning != SkyBinning {
		t.Errorf("sky.binning = %q, want %q", m.Sky.Binning, SkyBinning)
	}
	if m.Sky.FitOn != SkyFitOn {
		t.Errorf("sky.fit_on = %q, want %q", m.Sky.FitOn, SkyFitOn)
	}
	if m.Sky.MissingAction != SkyMissingAction {
		t.Errorf("sky.missing_action = %q, want %q", m.Sky.MissingAction, SkyMissingAction)
	}
	if m.Sky.BindsRegimeHash != HistoryRegimeContractHash {
		t.Errorf("sky.binds_regime_contract_hash = %q, want %q", m.Sky.BindsRegimeHash, HistoryRegimeContractHash)
	}
	assertIntSlice(t, "sky.timescales_hours", m.Sky.TimescalesHours, SkyTimescalesHours)
	assertFloatSlice(t, "sky.quantiles", m.Sky.Quantiles, SkyQuantiles)
	skyDims := make([]string, len(m.Sky.Dimensions))
	for i, d := range m.Sky.Dimensions {
		skyDims[i] = d.Name
	}
	assertStrSlice(t, "sky.dimensions[].name", skyDims, SkyDimensions)

	assertIntSlice(t, "discovery_budget.outcome_horizons_hours", m.DiscoveryBudget.OutcomeHorizonsHours, OutcomeHorizonsHours)
	if m.DiscoveryBudget.DirectionSymmetry != OutcomeHorizonDirectionSymmetry {
		t.Errorf("direction_symmetry = %q, want %q", m.DiscoveryBudget.DirectionSymmetry, OutcomeHorizonDirectionSymmetry)
	}
	if m.DiscoveryBudget.CostBPS != DiscoveryCostBPS || m.DiscoveryBudget.NeutralBandBPS != NeutralBandBPS {
		t.Errorf("discovery cost/neutral band drifted: cost=%v neutral=%v", m.DiscoveryBudget.CostBPS, m.DiscoveryBudget.NeutralBandBPS)
	}
	checkInt(t, "forward.max_families", m.DiscoveryBudget.Forward.MaxFamilies, ForwardMaxFamilies)
	checkInt(t, "forward.max_candidates_per_family", m.DiscoveryBudget.Forward.MaxCandidatesPerFamily, ForwardMaxCandidatesPerFamily)
	checkInt(t, "reverse_induction.max_files", m.DiscoveryBudget.ReverseInduction.MaxFiles, ReverseInductionMaxFiles)
	checkInt(t, "reverse_induction.max_lines", m.DiscoveryBudget.ReverseInduction.MaxLines, ReverseInductionMaxLines)
	checkInt(t, "reverse_induction.max_single_feature_rules", m.DiscoveryBudget.ReverseInduction.MaxSingleFeatureRules, MaxSingleFeatureRules)
	checkInt(t, "reverse_induction.max_dual_feature_rules", m.DiscoveryBudget.ReverseInduction.MaxDualFeatureRules, MaxDualFeatureRules)
	checkInt(t, "reverse_induction.max_derived_search_rules", m.DiscoveryBudget.ReverseInduction.MaxDerivedSearchRules, MaxDerivedSearchRules)
	if m.DiscoveryBudget.ReverseInduction.AllowedSearchSpaceID != ReverseInductionSearchSpaceID ||
		m.DiscoveryBudget.ReverseInduction.DerivationAlgorithmVersion != DerivationAlgorithmVersion ||
		m.DiscoveryBudget.ReverseInduction.AllowedSearchDefinitionHash != DerivationAlgorithmConfigHash {
		t.Errorf("g3 search-space budget drifted")
	}
	if m.DiscoveryBudget.ReverseInduction.ActualDerivedRuleCount != nil {
		t.Errorf("actual_derived_rule_count must remain null until later sealed search manifest")
	}
	if m.DiscoveryBudget.ReverseInduction.ActualGeneratedAttemptCount != nil {
		t.Errorf("actual_generated_attempt_count must remain null until later sealed search manifest")
	}
	if m.DiscoveryBudget.ReverseInduction.ActualDerivedRulesBinding != "set_only_by_later_sealed_search_manifest" ||
		!m.DiscoveryBudget.ReverseInduction.SealedSearchManifestRequired {
		t.Errorf("actual derived rules must bind only through a later sealed search manifest")
	}
	checkInt(t, "reverse_induction.max_candidates", m.DiscoveryBudget.ReverseInduction.MaxCandidates, ReverseInductionMaxCandidates)
	checkInt(t, "reverse_induction.default_progress_every", m.DiscoveryBudget.ReverseInduction.DefaultProgressEvery, ReverseInductionProgressEvery)
	if m.DiscoveryBudget.ReverseInduction.MatchRule != ReverseInductionMatchRule {
		t.Errorf("reverse_induction.match_rule = %q, want %q", m.DiscoveryBudget.ReverseInduction.MatchRule, ReverseInductionMatchRule)
	}
	if !m.DiscoveryBudget.ReverseInduction.MatchedControlsRequired ||
		!m.DiscoveryBudget.ReverseInduction.ControlsNontriggerOutcomeBlind ||
		!m.DiscoveryBudget.ReverseInduction.ControlsOneToOneSameCluster ||
		!m.DiscoveryBudget.ReverseInduction.NoCrossClusterControlReuse ||
		!m.DiscoveryBudget.ReverseInduction.TreatmentIncludesAllTriggered ||
		!m.DiscoveryBudget.ReverseInduction.WinnerOnlyRequiresNontrigger {
		t.Errorf("reverse_induction outcome-blind matched control contract drifted")
	}
	if m.DiscoveryBudget.ReverseInduction.ControlsLossOrNeutralOnly != nil {
		t.Errorf("controls_loss_or_neutral_only must be absent; control selection cannot inspect outcome")
	}
	if !m.DiscoveryBudget.ReverseInduction.CheckpointRequired {
		t.Errorf("reverse_induction.checkpoint_required must be true")
	}
	if m.DiscoveryBudget.ReverseInduction.VolatilityTolerance != ReverseInductionVolatilityTol ||
		m.DiscoveryBudget.ReverseInduction.LiquidityTolerance != ReverseInductionLiquidityTol ||
		m.DiscoveryBudget.ReverseInduction.NeutralBandBPS != NeutralBandBPS ||
		m.DiscoveryBudget.ReverseInduction.DiscoveryCostBPS != DiscoveryCostBPS ||
		m.DiscoveryBudget.ReverseInduction.PermutationDraws != ReverseInductionPermutationDraws ||
		m.DiscoveryBudget.ReverseInduction.ExactPermutationMaxPairs != ReverseInductionExactPermMax ||
		m.DiscoveryBudget.ReverseInduction.BootstrapSamples != ReverseInductionBootstrapSamples ||
		m.DiscoveryBudget.ReverseInduction.Seed != ReverseInductionSeed {
		t.Errorf("reverse_induction frozen statistical parameters drifted")
	}
	if m.DiscoveryBudget.ReverseInduction.MatchWindowMs != ReverseInductionMatchWindowMs ||
		m.DiscoveryBudget.ReverseInduction.BoundedSmoke.MaxSymbols != G3SmokeMaxSymbols ||
		m.DiscoveryBudget.ReverseInduction.BoundedSmoke.MaxFundingLines != G3SmokeMaxFundingLines ||
		m.DiscoveryBudget.ReverseInduction.BoundedSmoke.MaxPriceLines != G3SmokeMaxPriceLines {
		t.Errorf("reverse_induction match/smoke bounds drifted")
	}
	if m.TimePartitionRoles.ReverseInductionPermille != ReverseInductionPermille ||
		m.TimePartitionRoles.RecheckPermille != RecheckPermille ||
		m.TimePartitionRoles.ForwardPermille != ForwardPermille ||
		m.TimePartitionRoles.WalkForwardPermille != WalkForwardPermille ||
		m.TimePartitionRoles.EmbargoHours != PartitionEmbargoHours {
		t.Errorf("time partition roles drifted")
	}
	if m.TimePartitionRoles.MinInductionClusters != MinInductionClusters {
		t.Errorf("min_induction_clusters = %d, want %d", m.TimePartitionRoles.MinInductionClusters, MinInductionClusters)
	}
	if m.TimePartitionRoles.MinForwardSamples != G3MinForwardSamples ||
		m.TimePartitionRoles.MinForwardClusters != G3MinForwardClusters ||
		m.TimePartitionRoles.MinRecheckSamples != G3MinRecheckSamples ||
		m.TimePartitionRoles.MinRecheckClusters != G3MinRecheckClusters ||
		m.TimePartitionRoles.WalkForwardFolds != G3WalkForwardFolds ||
		m.TimePartitionRoles.WFMinSamples != G3WFMinSamples ||
		m.TimePartitionRoles.WFMinClusters != G3WFMinClusters ||
		m.TimePartitionRoles.PurgeHours != G3PurgeHours ||
		m.TimePartitionRoles.WFEmbargoHours != G3WFEmbargoHours {
		t.Errorf("recheck/forward/WF frozen gates drifted")
	}
	if m.NodeInputs.Sorter.MinQuoteVolume != SorterMinQuoteVolume ||
		m.NodeInputs.Sorter.MinVolatilityCostRatio != SorterMinVolatilityCost {
		t.Errorf("sorter frozen thresholds drifted")
	}
	checkInt(t, "discovery_budget.max_total_candidates", m.DiscoveryBudget.MaxTotalCandidates, MaxTotalCandidates)

	if m.MultipleComparisonBudget.FamilyWiseControl != FamilyWiseControl {
		t.Errorf("family_wise_control = %q, want %q", m.MultipleComparisonBudget.FamilyWiseControl, FamilyWiseControl)
	}
	if m.MultipleComparisonBudget.FamilyWiseAlpha != FamilyWiseAlpha {
		t.Errorf("family_wise_alpha = %v, want %v", m.MultipleComparisonBudget.FamilyWiseAlpha, FamilyWiseAlpha)
	}
	if m.MultipleComparisonBudget.ReverseInductionAlpha != ReverseInductionAlpha ||
		m.MultipleComparisonBudget.RecheckAlpha != RecheckAlpha ||
		m.MultipleComparisonBudget.ForwardAlpha != ForwardAlpha ||
		m.MultipleComparisonBudget.WalkForwardAlpha != WalkForwardAlpha ||
		m.MultipleComparisonBudget.ReverseInductionControl != "Bonferroni" ||
		m.MultipleComparisonBudget.RecheckControl != FamilyWiseControl ||
		m.MultipleComparisonBudget.ForwardControl != FamilyWiseControl ||
		ReverseInductionAlpha+RecheckAlpha+ForwardAlpha+WalkForwardAlpha > FamilyWiseAlpha {
		t.Errorf("g3 alpha allocation drifted")
	}
	checkInt(t, "multiple_comparison_budget.max_derived_search_rules", m.MultipleComparisonBudget.MaxDerivedSearchRules, MaxDerivedSearchRulesTest)
	if !m.MultipleComparisonBudget.AlphaLedger.Required ||
		!m.MultipleComparisonBudget.AlphaLedger.AppendOnly ||
		!m.MultipleComparisonBudget.AlphaLedger.OneEntryPerGeneratedCheckedAttempt ||
		!m.MultipleComparisonBudget.AlphaLedger.CannotDeleteGeneratedAttempts ||
		m.MultipleComparisonBudget.AlphaLedger.EntryOrder != AlphaLedgerEntryOrder ||
		m.MultipleComparisonBudget.AlphaLedger.AlphaChargeRule != AlphaLedgerChargeRule ||
		m.MultipleComparisonBudget.AlphaLedger.ActualCountSource != AlphaLedgerCountSource ||
		m.MultipleComparisonBudget.AlphaLedger.ProvenanceRule != AlphaLedgerProvenanceRule {
		t.Errorf("alpha ledger binding drifted")
	}
	assertStrSlice(t, "alpha_ledger.statuses", m.MultipleComparisonBudget.AlphaLedger.Statuses, AlphaLedgerStatuses)
	assertStrSlice(t, "alpha_ledger.must_record", m.MultipleComparisonBudget.AlphaLedger.MustRecord, AlphaLedgerMustRecord)

	if len(m.RedTeam.Requirements) != RedTeamRequirementCount {
		t.Errorf("red_team.requirements = %d items, want %d", len(m.RedTeam.Requirements), RedTeamRequirementCount)
	}
	seen := map[string]bool{}
	for i, r := range m.RedTeam.Requirements {
		if r.ID == "" || r.Requirement == "" || r.EnforcedBy == "" {
			t.Errorf("red_team.requirements[%d] must have non-empty id/requirement/enforced_by: %+v", i, r)
		}
		if seen[r.ID] {
			t.Errorf("red_team.requirements duplicate id %q", r.ID)
		}
		seen[r.ID] = true
	}
}

// TestOutcomeBlindControlAndReachabilityNoDrift 冻结 outcome-blind control 语义，并证明
// 100,000 次 Monte Carlo 在 192 规则最坏 Bonferroni 门槛下仍有足够 p 值分辨率。
func TestOutcomeBlindControlAndReachabilityNoDrift(t *testing.T) {
	m := loadG3Manifest(t)
	gotSelection, ok := m.DiscoveryBudget.ReverseInduction.DerivationAlgorithm["control_selection"].(string)
	if !ok || gotSelection != ReverseInductionControlSelection {
		t.Errorf("derivation control_selection = %#v, want %q", gotSelection, ReverseInductionControlSelection)
	}
	if !m.DiscoveryBudget.ReverseInduction.ControlsNontriggerOutcomeBlind {
		t.Errorf("controls_nontrigger_outcome_blind must be true")
	}
	if m.DiscoveryBudget.ReverseInduction.ControlsLossOrNeutralOnly != nil {
		t.Errorf("outcome-conditioned controls_loss_or_neutral_only must not exist")
	}
	if m.DiscoveryBudget.ReverseInduction.PermutationDraws != ReverseInductionPermutationDraws {
		t.Errorf("permutation_draws = %d, want %d",
			m.DiscoveryBudget.ReverseInduction.PermutationDraws, ReverseInductionPermutationDraws)
	}
	minMonteCarloP := 1.0 / float64(ReverseInductionPermutationDraws+1)
	worstCaseAlpha := ReverseInductionAlpha / float64(MaxDerivedSearchRules)
	if minMonteCarloP > worstCaseAlpha {
		t.Fatalf("Monte Carlo resolution %g cannot reach 192-rule Bonferroni threshold %g",
			minMonteCarloP, worstCaseAlpha)
	}
	if ReverseInductionExactPermMax != 20 {
		t.Errorf("exact permutation max pairs = %d, want 20", ReverseInductionExactPermMax)
	}
	requiredRedTeam := map[string]bool{
		"rt18_noise_outcome_blind_controls":  false,
		"rt19_192_rule_production_reachable": false,
	}
	for _, requirement := range m.RedTeam.Requirements {
		if _, required := requiredRedTeam[requirement.ID]; required {
			requiredRedTeam[requirement.ID] = true
		}
	}
	for id, found := range requiredRedTeam {
		if !found {
			t.Errorf("red_team missing required %q", id)
		}
	}
}

// TestBoundedStreamingDerivationNoDrift 冻结逐条生成、注册 cap 即停、attempt ledger、
// not_considered 后缀、O(max rules + current group) 内存、progress/checkpoint 语义。
func TestBoundedStreamingDerivationNoDrift(t *testing.T) {
	m := loadG3Manifest(t)
	algorithm := m.DiscoveryBudget.ReverseInduction.DerivationAlgorithm
	wantStrings := map[string]string{
		"execution_model":            DerivationExecutionModel,
		"attempt_pipeline":           DerivationAttemptPipeline,
		"dedupe":                     DerivationDedupeRule,
		"truncation":                 DerivationTruncationRule,
		"single_stop_rule":           DerivationSingleStopRule,
		"dual_stop_rule":             DerivationDualStopRule,
		"not_considered_rule":        DerivationNotConsideredRule,
		"candidate_memory_bound":     DerivationCandidateMemoryBound,
		"materialization_prohibited": DerivationMaterializationRule,
		"streaming_state":            DerivationStreamingStateRule,
		"progress_rule":              DerivationProgressRule,
		"checkpoint_rule":            DerivationCheckpointRule,
	}
	for field, want := range wantStrings {
		got, ok := algorithm[field].(string)
		if !ok || got != want {
			t.Errorf("derivation_algorithm.%s = %#v, want %q", field, algorithm[field], want)
		}
	}
	if m.DiscoveryBudget.ReverseInduction.MaxSingleFeatureRules != MaxSingleFeatureRules ||
		m.DiscoveryBudget.ReverseInduction.MaxDualFeatureRules != MaxDualFeatureRules ||
		m.DiscoveryBudget.ReverseInduction.MaxDerivedSearchRules != 192 {
		t.Errorf("bounded streaming registration caps drifted")
	}
	if m.DiscoveryBudget.ReverseInduction.DefaultProgressEvery <= 0 ||
		!m.DiscoveryBudget.ReverseInduction.CheckpointRequired {
		t.Errorf("bounded streaming must support positive progress interval and checkpoints")
	}
	if !m.MultipleComparisonBudget.AlphaLedger.OneEntryPerGeneratedCheckedAttempt {
		t.Errorf("alpha ledger must record one entry per actually generated checked attempt")
	}
	assertStrSlice(t, "bounded streaming ledger statuses",
		m.MultipleComparisonBudget.AlphaLedger.Statuses, AlphaLedgerStatuses)
	if m.MultipleComparisonBudget.AlphaLedger.AlphaChargeRule != AlphaLedgerChargeRule ||
		m.MultipleComparisonBudget.AlphaLedger.ProvenanceRule != AlphaLedgerProvenanceRule {
		t.Errorf("bounded streaming ledger alpha/provenance semantics drifted")
	}
	foundRedTeam := false
	for _, requirement := range m.RedTeam.Requirements {
		if requirement.ID == "rt20_bounded_streaming_derivation_memory" {
			foundRedTeam = true
			break
		}
	}
	if !foundRedTeam {
		t.Errorf("red_team missing bounded streaming memory requirement")
	}
}

// TestV3CoarseRoundRobinCoverageNoDrift 冻结 coarse Sky pattern、机制优先 feature 顺序、
// group-innermost round-robin 和覆盖报告，防止预算再次耗尽在 exact-Sky 字典序前缀。
func TestV3CoarseRoundRobinCoverageNoDrift(t *testing.T) {
	m := loadG3Manifest(t)
	algorithm := m.DiscoveryBudget.ReverseInduction.DerivationAlgorithm
	wantStrings := map[string]string{
		"induction_grouping":        DerivationInductionGrouping,
		"max_coarse_groups_formula": DerivationMaxGroupsFormula,
		"observed_group_rule":       DerivationObservedGroupRule,
		"group_order":               DerivationGroupOrder,
		"rule_fires":                DerivationRuleFires,
		"control_sky_match":         DerivationControlSkyMatch,
		"feature_order":             DerivationFeatureOrderRule,
		"single_rule_order":         DerivationSingleRuleOrder,
		"single_coverage_rule":      DerivationSingleCoverageRule,
		"single_stage_exit":         DerivationSingleStageExit,
		"dual_entry_rule":           DerivationDualEntryRule,
		"dual_rule_order":           DerivationDualRuleOrder,
		"dual_coverage_rule":        DerivationDualCoverageRule,
		"selection_order":           DerivationSelectionOrder,
	}
	for field, want := range wantStrings {
		got, ok := algorithm[field].(string)
		if !ok || got != want {
			t.Errorf("derivation_algorithm.%s = %#v, want %q", field, algorithm[field], want)
		}
	}

	assertAnyStringSlice(t, "feature_derivation_order", algorithm["feature_derivation_order"], FeatureDerivationOrder)
	assertAnyStringSlice(t, "sorter_domains", algorithm["sorter_domains"], G3SorterDomains)
	assertAnyStringSlice(t, "stage_reporting", algorithm["stage_reporting"], DerivationStageReportingFields)
	axes, ok := algorithm["coarse_sky_axes"].(map[string]any)
	if !ok {
		t.Fatalf("coarse_sky_axes missing or invalid: %#v", algorithm["coarse_sky_axes"])
	}
	for _, axis := range []string{"ret", "rv", "fund"} {
		assertAnyStringSlice(t, "coarse_sky_axes."+axis, axes[axis], CoarseSkyAxisLabels)
	}
	wildcards, ok := algorithm["wildcard_sky_axes"].(map[string]any)
	if !ok || wildcards["te"] != "*" || wildcards["dd"] != "*" {
		t.Errorf("wildcard_sky_axes = %#v, want te/dd='*'", algorithm["wildcard_sky_axes"])
	}
	gotMaxGroups, ok := algorithm["max_coarse_groups"].(float64)
	if !ok || int(gotMaxGroups) != MaxCoarseObservedGroups {
		t.Errorf("max_coarse_groups = %#v, want %d", algorithm["max_coarse_groups"], MaxCoarseObservedGroups)
	}
	calculatedMax := len(CoarseSkyAxisLabels) * len(CoarseSkyAxisLabels) * len(CoarseSkyAxisLabels) *
		len(G3SorterDomains) * len(OutcomeHorizonsHours) * 2
	if calculatedMax != MaxCoarseObservedGroups {
		t.Fatalf("coarse group formula computes %d, want %d", calculatedMax, MaxCoarseObservedGroups)
	}

	mechanismFeatures := map[string]bool{}
	for _, attribution := range MechanismAttributions {
		for _, feature := range attribution.Features {
			mechanismFeatures[feature] = true
		}
	}
	for i := 0; i < 4; i++ {
		if !mechanismFeatures[FeatureDerivationOrder[i]] {
			t.Errorf("feature_derivation_order[%d]=%q is not a mechanism feature", i, FeatureDerivationOrder[i])
		}
	}
	for _, feature := range FeatureDerivationOrder[4:] {
		if mechanismFeatures[feature] {
			t.Errorf("conditioner suffix unexpectedly contains mechanism feature %q", feature)
		}
	}
	assertSameStringSet(t, "feature derivation/catalog set", FeatureDerivationOrder, FeatureCatalog)

	if MaxSingleFeatureRules != MaxCoarseObservedGroups || MaxDualFeatureRules != 30 ||
		MaxSingleFeatureRules+MaxDualFeatureRules != MaxDerivedSearchRules {
		t.Errorf("v3 single/dual/total budget drifted: %d/%d/%d",
			MaxSingleFeatureRules, MaxDualFeatureRules, MaxDerivedSearchRules)
	}
	foundRedTeam := false
	for _, requirement := range m.RedTeam.Requirements {
		if requirement.ID == "rt21_coarse_group_round_robin_coverage" {
			foundRedTeam = true
			break
		}
	}
	if !foundRedTeam {
		t.Errorf("red_team missing coarse-group round-robin coverage requirement")
	}
}

// TestBoundHashesMatchCodeConstants 断言 manifest.binds 与 protocol.go 常量一致。
func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadG3Manifest(t)
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
}

// TestBoundHashesMatchReferencedContracts 从被引用的公开契约实际复现 hash,断言 manifest 绑定正确。
func TestBoundHashesMatchReferencedContracts(t *testing.T) {
	m := loadG3Manifest(t)

	regime, err := researchboundary.LoadRegimeContract(filepath.Clean(regimeManifestRelPath))
	if err != nil {
		t.Fatalf("load regime contract: %v", err)
	}
	if regime.ManifestHash == nil || *regime.ManifestHash != m.Binds.HistoryRegimeContractHash {
		t.Errorf("regime contract hash = %v, g3 binds = %q", regime.ManifestHash, m.Binds.HistoryRegimeContractHash)
	}

	req, err := researchboundary.LoadRequirements(filepath.Clean(reqManifestRelPath))
	if err != nil {
		t.Fatalf("load requirements: %v", err)
	}
	if req.ManifestHash == nil || *req.ManifestHash != m.Binds.HistoryDataRequirementsHash {
		t.Errorf("requirements hash = %v, g3 binds = %q", req.ManifestHash, m.Binds.HistoryDataRequirementsHash)
	}

	g1Hash := reproduceProtocolHash(t, g1ManifestRelPath)
	if g1Hash != m.Binds.G1RouterProtocolHash {
		t.Errorf("g1 router protocol hash reproduced = %q, g3 binds = %q", g1Hash, m.Binds.G1RouterProtocolHash)
	}
	if g1Hash != G1RouterProtocolHash {
		t.Errorf("g1 router protocol hash reproduced = %q, want frozen %q", g1Hash, G1RouterProtocolHash)
	}

	g2Hash := reproduceProtocolHash(t, g2ManifestRelPath)
	if g2Hash != m.Binds.G2RouterProtocolHash {
		t.Errorf("g2 router protocol hash reproduced = %q, g3 binds = %q", g2Hash, m.Binds.G2RouterProtocolHash)
	}
	if g2Hash != G2RouterProtocolHash {
		t.Errorf("g2 router protocol hash reproduced = %q, want frozen %q", g2Hash, G2RouterProtocolHash)
	}
}

// TestG1ResultReferencedImmutable 断言 g3 只引用不修改 g1 零候选结论。
func TestG1ResultReferencedImmutable(t *testing.T) {
	m := loadG3Manifest(t)
	if m.ClosesG1.G1Generation != G1Generation || m.ClosesG1.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.ClosesG1.G1CertifiedMoneyLeaves != G1CertifiedMoneyLeaves ||
		m.ClosesG1.G1ResearchPassLeaves != G1ResearchPassLeaves ||
		m.ClosesG1.G1SystemStatus != G1SystemStatus {
		t.Errorf("closes_g1 drift from code constants: %+v", m.ClosesG1)
	}

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
	if summary.Generation != G1Generation {
		t.Errorf("g1 stage summary generation = %q, want %q", summary.Generation, G1Generation)
	}
	if summary.ProtocolHash != G1RouterProtocolHash {
		t.Errorf("g1 stage summary protocol_hash = %q, want %q", summary.ProtocolHash, G1RouterProtocolHash)
	}
	if summary.ResearchSmoke.CandidateNominations != 0 {
		t.Errorf("g1 zero-candidate result mutated: candidate_nominations = %d, want 0", summary.ResearchSmoke.CandidateNominations)
	}
	if len(summary.LeafStatus.ResearchPass) != G1ResearchPassLeaves {
		t.Errorf("g1 research_pass leaves = %d, want %d", len(summary.LeafStatus.ResearchPass), G1ResearchPassLeaves)
	}
	if summary.SystemStatus != G1SystemStatus {
		t.Errorf("g1 system_status = %q, want %q", summary.SystemStatus, G1SystemStatus)
	}
}

// TestG2ResultReferencedImmutable 断言 g3 只引用不修改 g2 162 组合 0 提名 NO_GO 结论。
func TestG2ResultReferencedImmutable(t *testing.T) {
	m := loadG3Manifest(t)
	if m.ClosesG2.G2Generation != G2Generation || m.ClosesG2.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.ClosesG2.G2RegisteredSearchCombinations != G2RegisteredSearchCombos ||
		m.ClosesG2.G2CandidateNominations != G2CandidateNominations ||
		m.ClosesG2.G2ResearchPassLeaves != G2ResearchPassLeaves ||
		m.ClosesG2.G2SystemStatus != G2SystemStatus {
		t.Errorf("closes_g2 drift from code constants: %+v", m.ClosesG2)
	}

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
		ExamActions struct {
			Created  int `json:"created"`
			Unsealed int `json:"unsealed"`
			Consumed int `json:"consumed"`
		} `json:"exam_actions"`
		SystemStatus string `json:"system_status"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("decode g2 stage summary: %v", err)
	}
	if summary.Generation != G2Generation {
		t.Errorf("g2 stage summary generation = %q, want %q", summary.Generation, G2Generation)
	}
	if summary.ProtocolHash != G2RouterProtocolHash {
		t.Errorf("g2 stage summary protocol_hash = %q, want %q", summary.ProtocolHash, G2RouterProtocolHash)
	}
	if summary.Search.RegisteredCombinations != G2RegisteredSearchCombos {
		t.Errorf("g2 registered_combinations = %d, want %d", summary.Search.RegisteredCombinations, G2RegisteredSearchCombos)
	}
	if summary.Search.CandidateNominations != G2CandidateNominations {
		t.Errorf("g2 candidate_nominations = %d, want %d", summary.Search.CandidateNominations, G2CandidateNominations)
	}
	if len(summary.LeafStatus.ResearchPass) != G2ResearchPassLeaves {
		t.Errorf("g2 research_pass leaves = %d, want %d", len(summary.LeafStatus.ResearchPass), G2ResearchPassLeaves)
	}
	if summary.SystemStatus != G2SystemStatus {
		t.Errorf("g2 system_status = %q, want %q", summary.SystemStatus, G2SystemStatus)
	}
}

// TestSearchDefinitionHashMatchesCanonical 断言冻结的是派生算法/config hash，而非 induction 后才知道的具体规则答案。
func TestSearchDefinitionHashMatchesCanonical(t *testing.T) {
	m := loadG3Manifest(t)
	searchDef := map[string]any{
		"id":                            ReverseInductionSearchSpaceID,
		"generation":                    Generation,
		"derivation_algorithm_version":  DerivationAlgorithmVersion,
		"derivation_algorithm":          m.DiscoveryBudget.ReverseInduction.DerivationAlgorithm,
		"alpha_ledger_config":           m.MultipleComparisonBudget.AlphaLedger,
		"feature_mechanism_attribution": m.FeatureMechanismAttribution,
		"feature_catalog":               FeatureCatalog,
		"feature_derivation_order":      FeatureDerivationOrder,
		"coarse_group_config": map[string]any{
			"sky_axis_labels": CoarseSkyAxisLabels,
			"wildcard_axes":   CoarseSkyWildcardAxes,
			"sorter_domains":  G3SorterDomains,
			"max_groups":      MaxCoarseObservedGroups,
		},
		"feature_freshness": map[string]any{
			"rubik_taker_imbalance_1h_max_staleness_hours": Rubik1hMaxStalenessHours,
			"rubik_lsratio_1h_max_staleness_hours":         Rubik1hMaxStalenessHours,
			"stale_action":                                 Rubik1hStaleAction,
		},
		"matched_control_config": map[string]any{
			"match_rule":                                    ReverseInductionMatchRule,
			"controls_nontrigger_outcome_blind":             ControlsNontriggerOutcomeBlind,
			"winner_only_rule_requires_nontrigger_controls": WinnerOnlyRequiresNontrigger,
			"one_to_one_same_cluster":                       true,
			"no_cross_cluster_control_reuse":                true,
		},
		"induction_stat_config": map[string]any{
			"permutation_draws":           ReverseInductionPermutationDraws,
			"exact_permutation_max_pairs": ReverseInductionExactPermMax,
			"min_induction_clusters":      MinInductionClusters,
		},
		"max_features_per_rule":    MaxFeaturesPerRule,
		"min_features_per_rule":    MinFeaturesPerRule,
		"threshold_quantiles":      ThresholdQuantiles,
		"outcome_horizons_hours":   OutcomeHorizonsHours,
		"direction_symmetry":       OutcomeHorizonDirectionSymmetry,
		"max_single_feature_rules": MaxSingleFeatureRules,
		"max_dual_feature_rules":   MaxDualFeatureRules,
		"max_derived_search_rules": MaxDerivedSearchRules,
	}
	got, _, err := datatruth.HashJSON(searchDef)
	if err != nil {
		t.Fatalf("HashJSON search def: %v", err)
	}
	if got != DerivationAlgorithmConfigHash {
		t.Fatalf("derivation algorithm/config hash = %q, want %q", got, DerivationAlgorithmConfigHash)
	}
	if m.DiscoveryBudget.ReverseInduction.AllowedSearchDefinitionHash != got {
		t.Fatalf("manifest derivation algorithm/config hash = %q, want %q",
			m.DiscoveryBudget.ReverseInduction.AllowedSearchDefinitionHash, got)
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

// TestProtocolHashMatchesManifest 校验 g3 audit.protocol_hash 已按规范填入且可复现。
func TestProtocolHashMatchesManifest(t *testing.T) {
	want := reproduceProtocolHash(t, g3ManifestRelPath)
	m := loadG3Manifest(t)
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

func assertAnyStringSlice(t *testing.T, name string, got any, want []string) {
	t.Helper()
	raw, ok := got.([]any)
	if !ok {
		t.Errorf("%s = %#v, want string array", name, got)
		return
	}
	converted := make([]string, len(raw))
	for i, value := range raw {
		s, ok := value.(string)
		if !ok {
			t.Errorf("%s[%d] = %#v, want string", name, i, value)
			return
		}
		converted[i] = s
	}
	assertStrSlice(t, name, converted, want)
}

func assertSameStringSet(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s lengths differ: %d vs %d", name, len(got), len(want))
		return
	}
	counts := map[string]int{}
	for _, value := range want {
		counts[value]++
	}
	for _, value := range got {
		counts[value]--
	}
	for value, count := range counts {
		if count != 0 {
			t.Errorf("%s mismatch for %q: delta=%d", name, value, count)
		}
	}
}

func checkInt(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}
