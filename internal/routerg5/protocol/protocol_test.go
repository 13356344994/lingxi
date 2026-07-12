package protocol

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/datatruth"
)

const (
	g5ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g5_preregistration.json"
	g4ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g4_preregistration.json"
	g3ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g3_preregistration.json"
	g2ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	g2StageSummaryRelPath = "../../../research/firstbattle/g2_stage_summary.json"
	g3StageSummaryRelPath = "../../../research/firstbattle/g3_stage_summary.json"
	g4StageSummaryRelPath = "../../../research/firstbattle/g4_stage_summary.json"
)

type g5Manifest struct {
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
		G3Generation            string `json:"g3_generation"`
		G3RouterProtocolHash    string `json:"g3_router_protocol_hash"`
		G3RegisteredSearchRules int    `json:"g3_registered_search_rules"`
		G3CandidateNominations  int    `json:"g3_candidate_nominations"`
		G3ResearchPassLeaves    int    `json:"g3_research_pass_leaves"`
		G3SystemStatus          string `json:"g3_system_status"`
	} `json:"closes_g3"`

	ClosesG4 struct {
		G4Generation               string            `json:"g4_generation"`
		G4RouterProtocolHash       string            `json:"g4_router_protocol_hash"`
		G4FullMarketCoverage       map[string]string `json:"g4_full_market_coverage"`
		G4FrozenCandidates         int               `json:"g4_frozen_candidates"`
		G4ValidationPassCandidates int               `json:"g4_validation_pass_candidates"`
		G4SystemStatus             string            `json:"g4_system_status"`
	} `json:"closes_g4"`

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
		G2RouterProtocolHash        string `json:"g2_router_protocol_hash"`
		G3RouterProtocolHash        string `json:"g3_router_protocol_hash"`
		G4RouterProtocolHash        string `json:"g4_router_protocol_hash"`
	} `json:"binds"`

	DataProfiles struct {
		OptionalNonblockingFamilies []string `json:"optional_nonblocking_families"`
		Profiles                    []struct {
			ID                      string   `json:"id"`
			PrimaryFamily           string   `json:"primary_family"`
			OutcomeHorizonsHours    []int    `json:"outcome_horizons_hours"`
			OutcomeHorizonsMinutes  []int    `json:"outcome_horizons_minutes"`
			MaxFeatureLookbackHours int      `json:"max_feature_lookback_hours"`
			PurgeEmbargoRule        string   `json:"purge_embargo_rule"`
			EntryPriceRule          string   `json:"entry_price_rule"`
			ExitPriceRule           string   `json:"exit_price_rule"`
		} `json:"profiles"`
	} `json:"data_profiles"`

	TimePartitionRoles struct {
		PartitionOrder      string `json:"partition_order"`
		ResearchPermille    int    `json:"research_permille"`
		ValidationPermille  int    `json:"validation_permille"`
		WalkForwardPermille int    `json:"walk_forward_permille"`
		PurgeEmbargoFormula string `json:"purge_embargo_formula"`
		ExamActions         int    `json:"exam_actions"`
	} `json:"time_partition_roles"`

	ActionSpace struct {
		Verbs                    []string  `json:"verbs"`
		DelayBars                []int     `json:"delay_bars"`
		OracleSizeFractions      []float64 `json:"oracle_size_fractions"`
		PolicySizeRange          []float64 `json:"policy_size_range"`
		OracleReferenceCapitalUSDT float64 `json:"oracle_reference_capital_usdt"`
		OracleReferenceCapitalNote string  `json:"oracle_reference_capital_note"`
		MaxAccountLeverage       int       `json:"max_account_leverage"`
		TopK                     int       `json:"top_k"`
		PositionNotionalRule     string    `json:"position_notional_rule"`
	} `json:"action_space"`

	CostModel struct {
		Inherits                      string  `json:"inherits"`
		G4ProtocolHash                string  `json:"g4_protocol_hash"`
		RoundtripTakerFeeBPS          float64 `json:"roundtrip_taker_fee_bps"`
		BaseSpreadSlippageFloorBPS    float64 `json:"base_spread_slippage_floor_bps"`
		CostStressMultiplier          float64 `json:"cost_stress_multiplier"`
		DelayPlusOneBarEntry          bool    `json:"delay_plus_one_bar_entry"`
		CapacityImpactFormula         string  `json:"capacity_impact_formula"`
		BarRangeToxicityFormula       string  `json:"bar_range_toxicity_formula"`
		ConservativeNetFormula        string  `json:"conservative_net_formula"`
	} `json:"cost_model"`

	FeaturePolicy struct {
		Mode                  string   `json:"mode"`
		SkyOutputs            []string `json:"sky_outputs"`
		SorterOutputs         []string `json:"sorter_outputs"`
		CrossSectionalRanks   []string `json:"cross_sectional_ranks"`
		ForbiddenFeatureInputs []string `json:"forbidden_feature_inputs"`
	} `json:"feature_policy"`

	ReverseLearnPolicy struct {
		ModelFamily       string    `json:"model_family"`
		DeepRLForbidden   bool      `json:"deep_rl_forbidden"`
		LambdaGrid        []float64 `json:"lambda_grid"`
		TrainingSeed      int       `json:"training_seed"`
		MaxModelAttempts  int       `json:"max_model_attempts"`
		DiscoveryTrainingLabelReservoir int `json:"discovery_training_label_reservoir"`
		TrainingLabelSelection          string `json:"training_label_selection"`
		DiscoveryLambdaHoldoutPermille  int    `json:"discovery_lambda_holdout_permille"`
		SGDEpochs                       int    `json:"sgd_epochs"`
		SGDLearningRate                 float64 `json:"sgd_learning_rate"`
		ActionEncoding struct {
			Verbs                []string `json:"verbs"`
			Directions           []string `json:"directions"`
			StartStates          []string `json:"start_states"`
			NumericFields        []string `json:"numeric_fields"`
			InteractionRule      string   `json:"interaction_rule"`
			FixedActionDetection string   `json:"fixed_action_detection"`
			RejectDegenerate     []string `json:"reject_degenerate"`
		} `json:"action_encoding"`
		HashChain         []string  `json:"hash_chain"`
	} `json:"reverse_learn_policy"`

	ValidationGates struct {
		ApplyOn                               string    `json:"apply_on"`
		NoExam                                bool      `json:"no_exam"`
		FreezeBeforeValidation                bool      `json:"freeze_before_validation"`
		InitialEquityUSDT                     []float64 `json:"initial_equity_usdt"`
		AllEquityTiersMustPass                bool      `json:"all_equity_tiers_must_pass"`
		DelayBars                             []int     `json:"delay_bars"`
		CostStressMultiplier                  float64   `json:"cost_stress_multiplier"`
		MinActiveDays                         int       `json:"min_active_days"`
		MaxDrawdownFraction                   float64   `json:"max_drawdown_fraction"`
		MaxSymbolPositiveContributionFraction float64   `json:"max_symbol_positive_contribution_fraction"`
		BaseNetReturnMustBePositive           bool      `json:"base_net_return_must_be_positive"`
		AllDelayNetReturnMustBePositive       bool      `json:"all_delay_net_return_must_be_positive"`
		NoLeverageSolvencyBreach              bool      `json:"no_leverage_solvency_breach"`
		EffectiveLeverageCheckRule            string    `json:"effective_leverage_check_rule"`
		PassStatus                            string    `json:"pass_status"`
		FailStatus                            string    `json:"fail_status"`
		InconclusiveStatus                    string    `json:"inconclusive_status"`
		SparseSampleAction                    string    `json:"sparse_sample_action"`
		MinActiveDaysInconclusiveNote         string    `json:"min_active_days_inconclusive_note"`
		SolvencyFailReason                    string    `json:"solvency_fail_reason"`
	} `json:"validation_gates"`

	PortfolioValidation struct {
		PendingFlushRule              string   `json:"pending_flush_rule"`
		ExecuteIndexFormula           string   `json:"execute_index_formula"`
		StaleRevalidateAtExecution    bool     `json:"stale_revalidate_at_execution"`
		ProposalCostFields            []string `json:"proposal_cost_fields"`
		ExecutionCostFormula          string   `json:"execution_cost_formula"`
		FundingOptionalNonblocking    bool     `json:"funding_optional_nonblocking"`
		FundingIncludedInCostEstimate string   `json:"funding_included_in_cost_estimate"`
		FundingStressIncluded         bool     `json:"funding_stress_included"`
		EffectiveLeverageCheckRule    string   `json:"effective_leverage_check_rule"`
		PendingNotionalRescaleRule    string   `json:"pending_notional_rescale_rule"`
		RiskGateAfterBreach           string   `json:"risk_gate_after_breach"`
		ClusterPnLIncrementalRule     string   `json:"cluster_pnl_incremental_rule"`
		DailyReplayEOFTailRule        string   `json:"daily_replay_eof_tail_rule"`
		DailyReplayNoFinalDecisionRule string  `json:"daily_replay_no_final_decision_rule"`
	} `json:"portfolio_validation"`

	WalkForwardGates struct {
		Partition                         string  `json:"partition"`
		FoldCount                         int     `json:"fold_count"`
		UsesProfilePurgeEmbargo           bool    `json:"uses_profile_purge_embargo"`
		Eligibility                       string  `json:"eligibility"`
		SkippedValidationFailStatus       string  `json:"skipped_validation_fail_status"`
		FrozenPolicyNoRetuning            bool    `json:"frozen_policy_no_retuning"`
		FoldNetReturnMustBePositive       bool    `json:"fold_net_return_must_be_positive"`
		WorstFoldNetReturnMustBePositive  bool    `json:"worst_fold_net_return_must_be_positive"`
		LOFONetReturnMustBePositive       bool    `json:"lofo_net_return_must_be_positive"`
		AllDelayNetReturnMustBePositive   bool    `json:"all_delay_net_return_must_be_positive"`
		CostStressNetReturnMustBePositive bool    `json:"cost_stress_net_return_must_be_positive"`
		SignTest                          struct {
			PValue                                    string  `json:"p_value"`
			NumericMethod                             string  `json:"numeric_method"`
			MultipleComparisonControl                 string  `json:"multiple_comparison_control"`
			Family                                    string  `json:"family"`
			FamilyAlpha                               float64 `json:"family_alpha"`
			DailyReplayMatchesScenarioFundingStress   bool    `json:"daily_replay_matches_scenario_funding_stress"`
		} `json:"sign_test"`
		PassStatus       string `json:"pass_status"`
		FailStatus       string `json:"fail_status"`
		InconclusiveStatus string `json:"inconclusive_status"`
		ExamActions      int    `json:"exam_actions"`
	} `json:"walk_forward_gates"`

	PolicyMechanismReview struct {
		UnexplainedPolicyPassStatus       string `json:"unexplained_policy_pass_status"`
		ForcedFlowEvidenceRequired        bool   `json:"forced_flow_evidence_required"`
		CounterpartyEvidenceRequired      bool   `json:"counterparty_evidence_required"`
		AutoMechanismFromFeaturesForbidden bool  `json:"auto_mechanism_from_features_forbidden"`
		TradeAllowed                      bool   `json:"trade_allowed"`
		ExamEligible                      bool   `json:"exam_eligible"`
		Certified                         bool   `json:"certified"`
		ExamActions                       int    `json:"exam_actions"`
	} `json:"policy_mechanism_review"`

	ExamCommitments struct {
		SchemaVersion       string `json:"schema_version"`
		RequiredPaperCount  int    `json:"required_paper_count"`
		StandardPaperCount  int    `json:"standard_paper_count"`
		GoldenPaperCount    int    `json:"golden_paper_count"`
		PublicFields        []string `json:"public_fields"`
		ForbiddenPublicFields []string `json:"forbidden_public_fields"`
		AllowedStatuses     []string `json:"allowed_statuses"`
		NoValidReservationsStatus string `json:"no_valid_reservations_status"`
		ScheduleTemplate    []struct {
			Queue      int    `json:"queue"`
			Kind       string `json:"kind"`
			Family     string `json:"family"`
			AlphaSpend int    `json:"alpha_spend"`
		} `json:"schedule_template"`
	} `json:"exam_commitments"`

	ExamEvaluator struct {
		FixtureSchemaVersion string `json:"fixture_schema_version"`
		MinBlocks            int    `json:"min_blocks"`
		ForbiddenFixtureFields []string `json:"forbidden_fixture_fields"`
		ExpectedCommitmentHashFormula string `json:"expected_commitment_hash_formula"`
		RegistrationRequiresReplayCallback bool `json:"registration_requires_replay_callback"`
		ReplayRequired               bool   `json:"replay_required"`
		ReplayGradesComputedOnly     bool   `json:"replay_grades_computed_only"`
		Gates                struct {
			BaseNetReturnMustBePositive             bool    `json:"base_net_return_must_be_positive"`
			AllDelayNetReturnMustBePositive         bool    `json:"all_delay_net_return_must_be_positive"`
			MinActiveDays                           int     `json:"min_active_days"`
			MaxDrawdownFraction                     float64 `json:"max_drawdown_fraction"`
			MaxSymbolPositiveContributionFraction   float64 `json:"max_symbol_positive_contribution_fraction"`
			NoLeverageSolvencyBreach                bool    `json:"no_leverage_solvency_breach"`
			CostStressMultiplier                    float64 `json:"cost_stress_multiplier"`
			CostStressNetReturnMustBePositive       bool    `json:"cost_stress_net_return_must_be_positive"`
			ClusterLOORequired                      bool    `json:"cluster_loo_required"`
			TopKRequired                            bool    `json:"top_k_required"`
		} `json:"gates"`
		BoundaryStatuses struct {
			Unavailable string `json:"unavailable"`
			NoGo        string `json:"no_go"`
			Available   string `json:"available"`
		} `json:"boundary_statuses"`
	} `json:"exam_evaluator"`

	CoarseTraceRule struct {
		MustNotContain []string `json:"must_not_contain"`
	} `json:"coarse_trace_rule"`

	RedTeam struct {
		Requirements []struct {
			ID string `json:"id"`
		} `json:"requirements"`
	} `json:"red_team"`

	Audit struct {
		ProtocolHash *string `json:"protocol_hash"`
		ExamActions  int     `json:"exam_actions"`
	} `json:"audit"`
}

func loadG5Manifest(t *testing.T) g5Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g5ManifestRelPath))
	if err != nil {
		t.Fatalf("read g5 manifest: %v", err)
	}
	var m g5Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g5 manifest: %v", err)
	}
	return m
}

func assertStrSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d, want %d", name, len(got), len(want))
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
		t.Fatalf("%s length = %d, want %d", name, len(got), len(want))
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
		t.Fatalf("%s length = %d, want %d", name, len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("%s[%d] = %v, want %v", name, i, got[i], want[i])
		}
	}
}

func TestProtocolHashMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(g5ManifestRelPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	audit := doc["audit"].(map[string]any)
	audit["protocol_hash"] = nil
	hash, _, err := datatruth.HashJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	m := loadG5Manifest(t)
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != hash {
		t.Fatalf("manifest audit.protocol_hash = %v, recomputed = %q", m.Audit.ProtocolHash, hash)
	}
	if hash != RouterProtocolHash {
		t.Fatalf("RouterProtocolHash = %q, recomputed = %q", RouterProtocolHash, hash)
	}
}

func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	if m.ProtocolID != ProtocolID {
		t.Errorf("protocol_id drift")
	}
	if m.Generation != Generation {
		t.Errorf("generation drift")
	}
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != RouterProtocolHash {
		t.Errorf("audit.protocol_hash drift")
	}
	if m.Audit.ExamActions != ExamActions || m.TimePartitionRoles.ExamActions != ExamActions {
		t.Errorf("exam_actions drift")
	}
	assertStrSlice(t, "optional_nonblocking", m.DataProfiles.OptionalNonblockingFamilies, OptionalNonblockingFamilies)
	assertStrSlice(t, "action_verbs", m.ActionSpace.Verbs, ActionVerbs)
	assertIntSlice(t, "delay_bars", m.ActionSpace.DelayBars, DelayBars)
	assertFloatSlice(t, "oracle_size_fractions", m.ActionSpace.OracleSizeFractions, OracleSizeFractions)
	assertFloatSlice(t, "policy_size_range", m.ActionSpace.PolicySizeRange, PolicySizeRange)
	if m.ActionSpace.MaxAccountLeverage != MaxAccountLeverage || m.ActionSpace.TopK != TopK {
		t.Errorf("leverage/topk drift")
	}
	if m.ActionSpace.PositionNotionalRule != PositionNotionalRule {
		t.Fatal("position notional rule drift")
	}
	if m.ActionSpace.OracleReferenceCapitalUSDT != OracleReferenceCapitalUSDT ||
		m.ActionSpace.OracleReferenceCapitalNote != OracleReferenceCapitalNote {
		t.Fatal("oracle reference capital drift")
	}
	assertStrSlice(t, "public_fields", m.ExamCommitments.PublicFields, PublicCommitmentFields)
	assertStrSlice(t, "forbidden_public_fields", m.ExamCommitments.ForbiddenPublicFields, ForbiddenPublicCommitmentFields)
	assertStrSlice(t, "allowed_statuses", m.ExamCommitments.AllowedStatuses, AllowedCommitmentStatuses)
	assertStrSlice(t, "coarse_trace", m.CoarseTraceRule.MustNotContain, CoarseTraceMustNotContain)
	if len(m.RedTeam.Requirements) != 15 {
		t.Fatalf("red_team requirements = %d, want 15", len(m.RedTeam.Requirements))
	}
}

func TestG1G2G3G4ResultReferencedImmutable(t *testing.T) {
	m := loadG5Manifest(t)
	if m.ClosesG1.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.ClosesG1.G1SystemStatus != G1SystemStatus ||
		m.ClosesG1.G1CertifiedMoneyLeaves != G1CertifiedMoneyLeaves ||
		m.ClosesG1.G1ResearchPassLeaves != G1ResearchPassLeaves {
		t.Fatal("closes_g1 drift")
	}
	if m.ClosesG2.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.ClosesG2.G2RegisteredSearchCombinations != G2RegisteredSearchCombos ||
		m.ClosesG2.G2SystemStatus != G2SystemStatus {
		t.Fatal("closes_g2 drift")
	}
	if m.ClosesG3.G3RouterProtocolHash != G3RouterProtocolHash ||
		m.ClosesG3.G3RegisteredSearchRules != G3RegisteredSearchRules ||
		m.ClosesG3.G3SystemStatus != G3SystemStatus {
		t.Fatal("closes_g3 drift")
	}
	if m.ClosesG4.G4RouterProtocolHash != G4RouterProtocolHash ||
		m.ClosesG4.G4FrozenCandidates != G4FrozenCandidates ||
		m.ClosesG4.G4ValidationPassCandidates != G4ValidationPassCandidates ||
		m.ClosesG4.G4SystemStatus != G4SystemStatus ||
		m.ClosesG4.G4FullMarketCoverage["okx_1h"] != G4FullMarketCoverageOKX1H ||
		m.ClosesG4.G4FullMarketCoverage["okx_5m"] != G4FullMarketCoverageOKX5M {
		t.Fatal("closes_g4 drift")
	}

	type stageSummary struct {
		SystemStatus string `json:"system_status"`
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{g1StageSummaryRelPath, G1SystemStatus},
		{g2StageSummaryRelPath, G2SystemStatus},
		{g3StageSummaryRelPath, G3SystemStatus},
		{g4StageSummaryRelPath, G4SystemStatus},
	} {
		raw, err := os.ReadFile(filepath.Clean(tc.path))
		if err != nil {
			t.Fatal(err)
		}
		var s stageSummary
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.SystemStatus != tc.want {
			t.Fatalf("%s system_status = %q, want %q", tc.path, s.SystemStatus, tc.want)
		}
	}

	type priorManifest struct {
		Audit struct {
			ProtocolHash *string `json:"protocol_hash"`
		} `json:"audit"`
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{g1ManifestRelPath, G1RouterProtocolHash},
		{g2ManifestRelPath, G2RouterProtocolHash},
		{g3ManifestRelPath, G3RouterProtocolHash},
		{g4ManifestRelPath, G4RouterProtocolHash},
	} {
		raw, err := os.ReadFile(filepath.Clean(tc.path))
		if err != nil {
			t.Fatal(err)
		}
		var pm priorManifest
		if err := json.Unmarshal(raw, &pm); err != nil {
			t.Fatal(err)
		}
		if pm.Audit.ProtocolHash == nil || *pm.Audit.ProtocolHash != tc.want {
			t.Fatalf("%s protocol hash drift", tc.path)
		}
	}
}

func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadG5Manifest(t)
	if m.Binds.HistoryDataHash != HistoryDataHash ||
		m.Binds.HistoryRegimeContractHash != HistoryRegimeContractHash ||
		m.Binds.HistoryDataRequirementsHash != HistoryDataRequirementsHash ||
		m.Binds.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.Binds.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.Binds.G3RouterProtocolHash != G3RouterProtocolHash ||
		m.Binds.G4RouterProtocolHash != G4RouterProtocolHash {
		t.Fatal("binds drift")
	}
}

func TestActionSpaceNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	p1 := m.DataProfiles.Profiles[0]
	p2 := m.DataProfiles.Profiles[1]
	if p1.OutcomeHorizonsHours[0] != OKX1HOutcomeHorizonsHours[0] ||
		p2.OutcomeHorizonsMinutes[0] != OKX5MOutcomeHorizonsMinutes[0] {
		t.Fatal("profile horizons drift")
	}
	if len(m.ExamCommitments.ScheduleTemplate) != len(ExamScheduleTemplate) {
		t.Fatal("exam schedule template length drift")
	}
	for i, slot := range ExamScheduleTemplate {
		got := m.ExamCommitments.ScheduleTemplate[i]
		if got.Queue != slot.Queue || got.Kind != slot.Kind || got.Family != slot.Family || got.AlphaSpend != slot.AlphaSpend {
			t.Fatalf("schedule template[%d] drift", i)
		}
	}
}

func TestCostModelInheritsG4(t *testing.T) {
	m := loadG5Manifest(t)
	if m.CostModel.Inherits != CostModelInherits ||
		m.CostModel.G4ProtocolHash != G4RouterProtocolHash ||
		m.CostModel.RoundtripTakerFeeBPS != RoundtripTakerFeeBPS ||
		m.CostModel.BaseSpreadSlippageFloorBPS != BaseSpreadSlippageFloorBPS ||
		m.CostModel.CostStressMultiplier != CostStressMultiplier ||
		m.CostModel.DelayPlusOneBarEntry != DelayPlusOneBarEntry ||
		m.CostModel.CapacityImpactFormula != CapacityImpactFormula ||
		m.CostModel.BarRangeToxicityFormula != BarRangeToxicityFormula ||
		m.CostModel.ConservativeNetFormula != ConservativeNetFormula {
		t.Fatal("cost model drift from g4 inheritance")
	}
}

func TestProfilePartitionNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	if m.TimePartitionRoles.ResearchPermille != ResearchPermille ||
		m.TimePartitionRoles.ValidationPermille != ValidationPermille ||
		m.TimePartitionRoles.WalkForwardPermille != WalkForwardPermille ||
		m.TimePartitionRoles.PartitionOrder != PartitionOrder ||
		m.TimePartitionRoles.PurgeEmbargoFormula != PurgeEmbargoFormula {
		t.Fatal("time partition drift")
	}
	if m.DataProfiles.Profiles[0].PurgeEmbargoRule != PurgeEmbargoRule {
		t.Fatal("purge/embargo rule drift")
	}
}

func TestNoDeepRL(t *testing.T) {
	m := loadG5Manifest(t)
	if m.ReverseLearnPolicy.ModelFamily != ModelFamily ||
		!m.ReverseLearnPolicy.DeepRLForbidden ||
		m.ReverseLearnPolicy.TrainingSeed != TrainingSeed ||
		m.ReverseLearnPolicy.MaxModelAttempts != MaxModelAttempts ||
		m.ReverseLearnPolicy.DiscoveryTrainingLabelReservoir != DiscoveryTrainingLabelReservoir ||
		m.ReverseLearnPolicy.TrainingLabelSelection != TrainingLabelSelection ||
		m.ReverseLearnPolicy.DiscoveryLambdaHoldoutPermille != DiscoveryLambdaHoldoutPermille ||
		m.ReverseLearnPolicy.SGDEpochs != SGDEpochs ||
		m.ReverseLearnPolicy.SGDLearningRate != SGDLearningRate {
		t.Fatal("reverse learn policy drift")
	}
	assertFloatSlice(t, "lambda_grid", m.ReverseLearnPolicy.LambdaGrid, LambdaGrid)
	assertStrSlice(t, "hash_chain", m.ReverseLearnPolicy.HashChain, PolicyHashChain)
	assertStrSlice(t, "action_verbs", m.ReverseLearnPolicy.ActionEncoding.Verbs, ActionVerbs)
	assertStrSlice(t, "action_directions", m.ReverseLearnPolicy.ActionEncoding.Directions, ActionEncodingDirections)
	assertStrSlice(t, "action_states", m.ReverseLearnPolicy.ActionEncoding.StartStates, ActionEncodingStates)
	assertStrSlice(t, "action_numeric", m.ReverseLearnPolicy.ActionEncoding.NumericFields, ActionEncodingNumericFields)
	if m.ReverseLearnPolicy.ActionEncoding.InteractionRule != ActionEncodingInteractionRule {
		t.Fatal("action encoding interaction rule drift")
	}
	if m.ReverseLearnPolicy.ActionEncoding.FixedActionDetection != FixedActionDetectionRule {
		t.Fatal("fixed action detection rule drift")
	}
	assertStrSlice(t, "reject_degenerate", m.ReverseLearnPolicy.ActionEncoding.RejectDegenerate, DegeneratePolicyRejections)
	assertStrSlice(t, "sky_outputs", m.FeaturePolicy.SkyOutputs, SkyOutputs)
	assertStrSlice(t, "sorter_outputs", m.FeaturePolicy.SorterOutputs, SorterOutputs)
	assertStrSlice(t, "cross_sectional_ranks", m.FeaturePolicy.CrossSectionalRanks, CrossSectionalRanks)
	assertStrSlice(t, "forbidden_feature_inputs", m.FeaturePolicy.ForbiddenFeatureInputs, ForbiddenFeatureInputs)
	if m.FeaturePolicy.Mode != FeaturePolicyMode {
		t.Fatal("feature policy mode drift")
	}
	if m.ExamCommitments.NoValidReservationsStatus != ExamUnavailableStatus {
		t.Fatal("exam unavailable status drift")
	}
}

func TestValidationGatesNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	vg := m.ValidationGates
	if vg.ApplyOn != ValidationApplyOn ||
		!vg.NoExam || ValidationNoExam != vg.NoExam ||
		!vg.FreezeBeforeValidation || ValidationFrozenBeforeReadingResults != vg.FreezeBeforeValidation ||
		vg.CostStressMultiplier != CostStressMultiplier ||
		vg.MinActiveDays != ValidationMinActiveDays ||
		vg.MaxDrawdownFraction != ValidationMaxDrawdownFraction ||
		vg.MaxSymbolPositiveContributionFraction != ValidationMaxSymbolPositiveContributionFraction ||
		!vg.BaseNetReturnMustBePositive || ValidationBaseNetReturnMustBePositive != vg.BaseNetReturnMustBePositive ||
		!vg.AllDelayNetReturnMustBePositive || ValidationAllDelayNetReturnMustBePositive != vg.AllDelayNetReturnMustBePositive ||
		!vg.AllEquityTiersMustPass || ValidationAllEquityTiersMustPass != vg.AllEquityTiersMustPass ||
		!vg.NoLeverageSolvencyBreach || ValidationNoLeverageSolvencyBreach != vg.NoLeverageSolvencyBreach ||
		vg.EffectiveLeverageCheckRule != EffectiveLeverageCheckRule ||
		vg.PassStatus != ValidationPassStatus ||
		vg.FailStatus != ValidationFailStatus ||
		vg.InconclusiveStatus != ValidationInconclusiveStatus ||
		vg.SparseSampleAction != ValidationSparseSampleAction ||
		vg.MinActiveDaysInconclusiveNote != ValidationMinActiveDaysInconclusiveNote ||
		vg.SolvencyFailReason != ValidationSolvencyFailReason {
		t.Fatalf("validation_gates drift: %+v", vg)
	}
	assertFloatSlice(t, "validation_initial_equities", vg.InitialEquityUSDT, ValidationInitialEquities)
	assertIntSlice(t, "validation_delay_bars", vg.DelayBars, ValidationDelayBars)
}

func TestPortfolioValidationNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	pv := m.PortfolioValidation
	if pv.PendingFlushRule != PendingFlushRule ||
		pv.ExecuteIndexFormula != PendingExecuteIndexFormula ||
		pv.StaleRevalidateAtExecution != PendingStaleRevalidate ||
		pv.ExecutionCostFormula != ExecutionCostFormula ||
		!pv.FundingOptionalNonblocking || FundingOptionalNonblocking != pv.FundingOptionalNonblocking ||
		pv.FundingIncludedInCostEstimate != FundingIncludedInCostEstimate ||
		!pv.FundingStressIncluded || FundingStressIncluded != pv.FundingStressIncluded ||
		pv.EffectiveLeverageCheckRule != EffectiveLeverageCheckRule ||
		pv.PendingNotionalRescaleRule != PendingNotionalRescaleRule ||
		pv.RiskGateAfterBreach != RiskGateAfterBreachRule ||
		pv.ClusterPnLIncrementalRule != ClusterPnLIncrementalRule ||
		pv.DailyReplayEOFTailRule != DailyReplayEOFTailRule ||
		pv.DailyReplayNoFinalDecisionRule != DailyReplayNoFinalDecisionRule {
		t.Fatalf("portfolio_validation drift: %+v", pv)
	}
	assertStrSlice(t, "proposal_cost_fields", pv.ProposalCostFields, ProposalCostFieldList)
}

func TestWalkForwardGatesNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	wf := m.WalkForwardGates
	if wf.Partition != WFPartition ||
		wf.FoldCount != WFFoldCount ||
		wf.UsesProfilePurgeEmbargo != WFUsesProfilePurgeEmbargo ||
		wf.Eligibility != WFEligibility ||
		wf.SkippedValidationFailStatus != WFSkippedValidationFailStatus ||
		!wf.FrozenPolicyNoRetuning || !WFFrozenPolicyNoRetuning ||
		!wf.FoldNetReturnMustBePositive || !WFFoldNetReturnMustBePositive ||
		!wf.WorstFoldNetReturnMustBePositive || !WFWorstFoldNetReturnMustBePositive ||
		!wf.LOFONetReturnMustBePositive || !WFLOFONetReturnMustBePositive ||
		!wf.AllDelayNetReturnMustBePositive || !WFAllDelayNetReturnMustBePositive ||
		!wf.CostStressNetReturnMustBePositive || !WFCostStressNetReturnMustBePositive ||
		wf.PassStatus != WFPassStatus ||
		wf.FailStatus != WFFailStatus ||
		wf.InconclusiveStatus != WFInconclusiveStatus ||
		wf.ExamActions != ExamActions {
		t.Fatalf("walk_forward_gates drift: %+v", wf)
	}
	if wf.SignTest.PValue != WFSignPValue ||
		wf.SignTest.NumericMethod != WFSignNumericMethod ||
		wf.SignTest.MultipleComparisonControl != WFMultipleComparisonControl ||
		wf.SignTest.Family != WFPolicyAttemptFamily ||
		wf.SignTest.FamilyAlpha != WFFamilyAlpha ||
		!wf.SignTest.DailyReplayMatchesScenarioFundingStress || !WFDailyReplayMatchesScenarioFunding {
		t.Fatalf("walk_forward_gates.sign_test drift: %+v", wf.SignTest)
	}
}

func TestPolicyMechanismReviewNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	pm := m.PolicyMechanismReview
	if pm.UnexplainedPolicyPassStatus != MechanismUnexplainedPolicyPassStatus ||
		!pm.ForcedFlowEvidenceRequired || !MechanismForcedFlowEvidenceRequired ||
		!pm.CounterpartyEvidenceRequired ||
		!pm.AutoMechanismFromFeaturesForbidden || !MechanismAutoFromFeaturesForbidden ||
		pm.TradeAllowed != MechanismTradeAllowed ||
		pm.ExamEligible != MechanismExamEligible ||
		pm.Certified != MechanismCertified ||
		pm.ExamActions != ExamActions {
		t.Fatalf("policy_mechanism_review drift: %+v", pm)
	}
}

func TestExamEvaluatorGatesNoDrift(t *testing.T) {
	m := loadG5Manifest(t)
	ee := m.ExamEvaluator
	if ee.FixtureSchemaVersion != ExamFixtureSchemaV1 ||
		ee.MinBlocks != ExamFixtureMinBlocks ||
		ee.ExpectedCommitmentHashFormula != ExamExpectedCommitmentHashFormula ||
		!ee.RegistrationRequiresReplayCallback ||
		!ee.ReplayRequired || !ExamReplayRequired ||
		!ee.ReplayGradesComputedOnly || !ExamReplayGradesComputedOnly ||
		!ee.Gates.BaseNetReturnMustBePositive || ExamBaseNetReturnMustBePositive != ee.Gates.BaseNetReturnMustBePositive ||
		!ee.Gates.AllDelayNetReturnMustBePositive || ExamAllDelayNetReturnMustBePositive != ee.Gates.AllDelayNetReturnMustBePositive ||
		ee.Gates.MinActiveDays != ExamMinActiveDays ||
		ee.Gates.MaxDrawdownFraction != ExamMaxDrawdownFraction ||
		ee.Gates.MaxSymbolPositiveContributionFraction != ExamMaxSymbolPositiveContributionFraction ||
		!ee.Gates.NoLeverageSolvencyBreach || ExamNoLeverageSolvencyBreach != ee.Gates.NoLeverageSolvencyBreach ||
		ee.Gates.CostStressMultiplier != ExamCostStressMultiplier ||
		!ee.Gates.CostStressNetReturnMustBePositive || ExamCostStressNetReturnMustBePositive != ee.Gates.CostStressNetReturnMustBePositive ||
		!ee.Gates.ClusterLOORequired || ExamClusterLOORequired != ee.Gates.ClusterLOORequired ||
		!ee.Gates.TopKRequired || ExamTopKRequired != ee.Gates.TopKRequired {
		t.Fatalf("exam_evaluator drift: %+v", ee)
	}
	if ee.BoundaryStatuses.Unavailable != ExamUnavailableStatus ||
		ee.BoundaryStatuses.NoGo != ExamBoundaryNoGoStatus ||
		ee.BoundaryStatuses.Available != ExamBoundaryAvailableStatus {
		t.Fatalf("exam boundary statuses drift: %+v", ee.BoundaryStatuses)
	}
}
