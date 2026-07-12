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
	g6ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g6_preregistration.json"
	g5ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g5_preregistration.json"
	g4ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g4_preregistration.json"
	g3ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g3_preregistration.json"
	g2ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	g2StageSummaryRelPath = "../../../research/firstbattle/g2_stage_summary.json"
	g3StageSummaryRelPath = "../../../research/firstbattle/g3_stage_summary.json"
	g4StageSummaryRelPath = "../../../research/firstbattle/g4_stage_summary.json"
	g5StageSummaryRelPath = "../../../research/firstbattle/g5_stage_summary.json"
)

type g6Manifest struct {
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

	ClosesG5 struct {
		G5Generation           string `json:"g5_generation"`
		G5RouterProtocolHash   string `json:"g5_router_protocol_hash"`
		G5FrozenPolicyCount    int    `json:"g5_frozen_policy_count"`
		G5TrainingStatus       string `json:"g5_training_status"`
		G5ResearchPassPolicies int    `json:"g5_research_pass_policies"`
		G5SystemStatus         string `json:"g5_system_status"`
	} `json:"closes_g5"`

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
		G2RouterProtocolHash        string `json:"g2_router_protocol_hash"`
		G3RouterProtocolHash        string `json:"g3_router_protocol_hash"`
		G4RouterProtocolHash        string `json:"g4_router_protocol_hash"`
		G5RouterProtocolHash        string `json:"g5_router_protocol_hash"`
	} `json:"binds"`

	DataProfiles struct {
		OptionalNonblockingFamilies []string `json:"optional_nonblocking_families"`
		UntestableFamilies          []string `json:"untestable_families"`
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

	EventUnit struct {
		EventIDFormula                        string `json:"event_id_formula"`
		WindowNonoverlapRequired              bool   `json:"window_nonoverlap_required"`
		NonOverlapCooldownFormula             string `json:"non_overlap_cooldown_formula"`
		ShockClusterRequired                  bool   `json:"shock_cluster_required"`
		ShockClusterRule                      string `json:"shock_cluster_rule"`
		IndependentSampleUnit                 string `json:"independent_sample_unit"`
		DiscoveryDedupPerOutcomeHorizonDelay  bool   `json:"discovery_dedup_per_outcome_horizon_delay"`
		DiscoveryDedupTrackerKey              string `json:"discovery_dedup_tracker_key"`
	} `json:"event_unit"`

	EventFamilies struct {
		PrimaryOHLCVFamilies      []string `json:"primary_ohlcv_families"`
		OptionalContextFamilies   []string `json:"optional_context_families"`
		UntestableFamilies        []string `json:"untestable_families"`
		Directions                []string `json:"directions"`
		DelayBars                 []int    `json:"delay_bars"`
		CapitalTiersUSDT          []float64 `json:"capital_tiers_usdt"`
	} `json:"event_families"`

	EventAccount struct {
		MaxConcurrentByCapital       map[string]int `json:"max_concurrent_by_capital"`
		MaxEventStressLossFraction   float64        `json:"max_event_stress_loss_fraction"`
		MaxAccountStressLossFraction float64        `json:"max_account_stress_loss_fraction"`
		MaxAccountLeverage           int            `json:"max_account_leverage"`
		EffectiveLeverageCheckRule   string         `json:"effective_leverage_check_rule"`
		QueueRankingRule             string         `json:"queue_ranking_rule"`
		OnePositionPerEvent          bool           `json:"one_position_per_event"`
		PositionNotionalRule         string         `json:"position_notional_rule"`
		MinNotionalLotFloorRule      string         `json:"min_notional_lot_floor_rule"`
		PendingFlushRule             string         `json:"pending_flush_rule"`
		PendingFlushExecuteAt        string         `json:"pending_flush_execute_at"`
		ExecuteIndexFormula          string         `json:"execute_index_formula"`
		StaleRevalidateAtExecution   bool           `json:"stale_revalidate_at_execution"`
		RiskGateAfterBreach          string         `json:"risk_gate_after_breach"`
	} `json:"event_account"`

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

	ReverseEventCards struct {
		DiscoveryMode               string   `json:"discovery_mode"`
		InstitutionalModelForbidden bool     `json:"institutional_model_forbidden"`
		EventCardRequiredFields     []string `json:"event_card_required_fields"`
		UnexplainedCandidateStatus  string   `json:"unexplained_candidate_status"`
		HashChain                   []string `json:"hash_chain"`
		GroupingUnit                string   `json:"grouping_unit"`
	} `json:"reverse_event_cards"`

	DynamicExit struct {
		StopErrorRequired              bool     `json:"stop_error_required"`
		ExitTriggers                   []string `json:"exit_triggers"`
		DeadlineIsFallbackOnly         bool     `json:"deadline_is_fallback_only"`
		MechanicalPriceStopSecondaryOnly bool   `json:"mechanical_price_stop_secondary_only"`
	} `json:"dynamic_exit"`

	ValidationGates struct {
		ApplyOn                               string    `json:"apply_on"`
		NoExam                                bool      `json:"no_exam"`
		FreezeBeforeValidation                bool      `json:"freeze_before_validation"`
		InitialEquityUSDT                     []float64 `json:"initial_equity_usdt"`
		AllEquityTiersMustPass                bool      `json:"all_equity_tiers_must_pass"`
		DelayBars                             []int     `json:"delay_bars"`
		CostStressMultiplier                  float64   `json:"cost_stress_multiplier"`
		MinEventCount                         int       `json:"min_event_count"`
		MinIndependentDays                    int       `json:"min_independent_days"`
		MinNonOverlapShockBlocks              int       `json:"min_non_overlap_shock_blocks"`
		BaseNetReturnMustBePositive           bool      `json:"base_net_return_must_be_positive"`
		AllDelayNetReturnMustBePositive       bool      `json:"all_delay_net_return_must_be_positive"`
		CostStressNetReturnMustBePositive     bool      `json:"cost_stress_net_return_must_be_positive"`
		NoLeverageSolvencyBreach              bool      `json:"no_leverage_solvency_breach"`
		EffectiveLeverageCheckRule            string    `json:"effective_leverage_check_rule"`
		MaxSingleDayPositiveContributionFraction  float64 `json:"max_single_day_positive_contribution_fraction"`
		MaxSingleEventPositiveContributionFraction float64 `json:"max_single_event_positive_contribution_fraction"`
		DiscoveryScreenFDRRequired            bool      `json:"discovery_screen_fdr_required"`
		DiscoveryScreenFamily                 string    `json:"discovery_screen_family"`
		PassStatus                            string    `json:"pass_status"`
		FailStatus                            string    `json:"fail_status"`
		InconclusiveStatus                    string    `json:"inconclusive_status"`
		SparseSampleAction                    string    `json:"sparse_sample_action"`
		MinEventCountInconclusiveNote         string    `json:"min_event_count_inconclusive_note"`
		SolvencyFailReason                    string    `json:"solvency_fail_reason"`
	} `json:"validation_gates"`

	WalkForwardGates struct {
		Partition                         string  `json:"partition"`
		FoldCount                         int     `json:"fold_count"`
		UsesProfilePurgeEmbargo           bool    `json:"uses_profile_purge_embargo"`
		Eligibility                       string  `json:"eligibility"`
		SkippedValidationFailStatus       string  `json:"skipped_validation_fail_status"`
		FrozenCardNoRetuning              bool    `json:"frozen_card_no_retuning"`
		ScanIncludesAllTriggers           bool    `json:"scan_includes_all_triggers"`
		SizingUsesSurvivorFrozenEdge      bool    `json:"sizing_uses_survivor_frozen_edge"`
		PnLUsesRealizedTrigger            bool    `json:"pnl_uses_realized_trigger"`
		SizingEdgeFloorBPS                float64 `json:"sizing_edge_floor_bps"`
		SizingEdgeSource                  string  `json:"sizing_edge_source"`
		FoldNetReturnMustBePositive       bool    `json:"fold_net_return_must_be_positive"`
		WorstFoldNetReturnMustBePositive  bool    `json:"worst_fold_net_return_must_be_positive"`
		LOFONetReturnMustBePositive       bool    `json:"lofo_net_return_must_be_positive"`
		AllDelayNetReturnMustBePositive   bool    `json:"all_delay_net_return_must_be_positive"`
		CostStressNetReturnMustBePositive bool    `json:"cost_stress_net_return_must_be_positive"`
		SignTest                          struct {
			PValue                                  string  `json:"p_value"`
			NumericMethod                           string  `json:"numeric_method"`
			MultipleComparisonControl               string  `json:"multiple_comparison_control"`
			Family                                  string  `json:"family"`
			FamilyAlpha                             float64 `json:"family_alpha"`
			DailyReplayMatchesScenarioFundingStress bool    `json:"daily_replay_matches_scenario_funding_stress"`
		} `json:"sign_test"`
		PassStatus         string `json:"pass_status"`
		FailStatus         string `json:"fail_status"`
		InconclusiveStatus string `json:"inconclusive_status"`
		ExamActions        int    `json:"exam_actions"`
	} `json:"walk_forward_gates"`

	EventMechanismReview struct {
		UnexplainedEventPassStatus         string `json:"unexplained_event_pass_status"`
		ForcedFlowEvidenceRequired        bool   `json:"forced_flow_evidence_required"`
		CounterpartyEvidenceRequired      bool   `json:"counterparty_evidence_required"`
		AutoMechanismFromFeaturesForbidden bool  `json:"auto_mechanism_from_features_forbidden"`
		TradeAllowed                      bool   `json:"trade_allowed"`
		ExamEligible                      bool   `json:"exam_eligible"`
		Certified                         bool   `json:"certified"`
		ExamActions                       int    `json:"exam_actions"`
	} `json:"event_mechanism_review"`

	ExamCommitments struct {
		SchemaVersion             string `json:"schema_version"`
		RequiredPaperCount        int    `json:"required_paper_count"`
		StandardPaperCount        int    `json:"standard_paper_count"`
		GoldenPaperCount          int    `json:"golden_paper_count"`
		PublicFields              []string `json:"public_fields"`
		ForbiddenPublicFields     []string `json:"forbidden_public_fields"`
		AllowedStatuses           []string `json:"allowed_statuses"`
		NoValidReservationsStatus string `json:"no_valid_reservations_status"`
		ScheduleTemplate          []struct {
			Queue      int    `json:"queue"`
			Kind       string `json:"kind"`
			Family     string `json:"family"`
			AlphaSpend int    `json:"alpha_spend"`
		} `json:"schedule_template"`
	} `json:"exam_commitments"`

	ExamEvaluator struct {
		FixtureSchemaVersion              string `json:"fixture_schema_version"`
		MinBlocks                         int    `json:"min_blocks"`
		ExpectedCommitmentHashFormula     string `json:"expected_commitment_hash_formula"`
		RegistrationRequiresReplayCallback bool  `json:"registration_requires_replay_callback"`
		ReplayRequired                    bool   `json:"replay_required"`
		ReplayGradesComputedOnly          bool   `json:"replay_grades_computed_only"`
		Gates                             struct {
			BaseNetReturnMustBePositive       bool    `json:"base_net_return_must_be_positive"`
			AllDelayNetReturnMustBePositive   bool    `json:"all_delay_net_return_must_be_positive"`
			MinIndependentDays                int     `json:"min_independent_days"`
			NoLeverageSolvencyBreach          bool    `json:"no_leverage_solvency_breach"`
			CostStressMultiplier              float64 `json:"cost_stress_multiplier"`
			CostStressNetReturnMustBePositive bool    `json:"cost_stress_net_return_must_be_positive"`
			ShockBlockLOORequired             bool    `json:"shock_block_loo_required"`
			MaxConcurrentByCapitalRequired    bool    `json:"max_concurrent_by_capital_required"`
		} `json:"gates"`
		BoundaryStatuses struct {
			Unavailable string `json:"unavailable"`
			NoGo        string `json:"no_go"`
			Available   string `json:"available"`
		} `json:"boundary_statuses"`
	} `json:"exam_evaluator"`

	RuntimeResource struct {
		GOMAXPROCS               int    `json:"gomaxprocs"`
		NicePriority             int    `json:"nice_priority"`
		CheckpointRequired       bool   `json:"checkpoint_required"`
		CheckpointShardRule      string `json:"checkpoint_shard_rule"`
		ForbiddenFullServerScan  bool   `json:"forbidden_full_server_scan"`
	} `json:"runtime_resource"`

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

func loadG6Manifest(t *testing.T) g6Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g6ManifestRelPath))
	if err != nil {
		t.Fatalf("read g6 manifest: %v", err)
	}
	var m g6Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g6 manifest: %v", err)
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
	raw, err := os.ReadFile(filepath.Clean(g6ManifestRelPath))
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
	m := loadG6Manifest(t)
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != hash {
		t.Fatalf("manifest audit.protocol_hash = %v, recomputed = %q", m.Audit.ProtocolHash, hash)
	}
	if hash != RouterProtocolHash {
		t.Fatalf("RouterProtocolHash = %q, recomputed = %q", RouterProtocolHash, hash)
	}
}

func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
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
	assertStrSlice(t, "untestable_families", m.DataProfiles.UntestableFamilies, UntestableFamilies)
	assertStrSlice(t, "public_fields", m.ExamCommitments.PublicFields, PublicCommitmentFields)
	assertStrSlice(t, "forbidden_public_fields", m.ExamCommitments.ForbiddenPublicFields, ForbiddenPublicCommitmentFields)
	assertStrSlice(t, "allowed_statuses", m.ExamCommitments.AllowedStatuses, AllowedCommitmentStatuses)
	assertStrSlice(t, "coarse_trace", m.CoarseTraceRule.MustNotContain, CoarseTraceMustNotContain)
	if len(m.RedTeam.Requirements) != 19 {
		t.Fatalf("red_team requirements = %d, want 19", len(m.RedTeam.Requirements))
	}
}

func TestG1G2G3G4G5ResultReferencedImmutable(t *testing.T) {
	m := loadG6Manifest(t)
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
	if m.ClosesG5.G5RouterProtocolHash != G5RouterProtocolHash ||
		m.ClosesG5.G5FrozenPolicyCount != G5FrozenPolicyCount ||
		m.ClosesG5.G5TrainingStatus != G5TrainingStatus ||
		m.ClosesG5.G5ResearchPassPolicies != G5ResearchPassPolicies ||
		m.ClosesG5.G5SystemStatus != G5SystemStatus {
		t.Fatal("closes_g5 drift")
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
		{g5StageSummaryRelPath, G5SystemStatus},
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
		{g5ManifestRelPath, G5RouterProtocolHash},
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
	m := loadG6Manifest(t)
	if m.Binds.HistoryDataHash != HistoryDataHash ||
		m.Binds.HistoryRegimeContractHash != HistoryRegimeContractHash ||
		m.Binds.HistoryDataRequirementsHash != HistoryDataRequirementsHash ||
		m.Binds.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.Binds.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.Binds.G3RouterProtocolHash != G3RouterProtocolHash ||
		m.Binds.G4RouterProtocolHash != G4RouterProtocolHash ||
		m.Binds.G5RouterProtocolHash != G5RouterProtocolHash {
		t.Fatal("binds drift")
	}
}

func TestEventFamiliesNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	assertStrSlice(t, "primary_ohlcv_families", m.EventFamilies.PrimaryOHLCVFamilies, PrimaryOHLCVEventFamilies)
	assertStrSlice(t, "optional_context_families", m.EventFamilies.OptionalContextFamilies, OptionalNonblockingFamilies)
	assertStrSlice(t, "untestable_families", m.EventFamilies.UntestableFamilies, UntestableFamilies)
	assertStrSlice(t, "directions", m.EventFamilies.Directions, EventDirections)
	assertIntSlice(t, "delay_bars", m.EventFamilies.DelayBars, DelayBars)
	assertFloatSlice(t, "capital_tiers", m.EventFamilies.CapitalTiersUSDT, CapitalTiersUSDT)
	p1 := m.DataProfiles.Profiles[0]
	p2 := m.DataProfiles.Profiles[1]
	if p1.OutcomeHorizonsHours[0] != OKX1HOutcomeHorizonsHours[0] ||
		p2.OutcomeHorizonsMinutes[0] != OKX5MOutcomeHorizonsMinutes[0] {
		t.Fatal("profile horizons drift")
	}
}

func TestCostModelInheritsG4(t *testing.T) {
	m := loadG6Manifest(t)
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
	m := loadG6Manifest(t)
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

func TestEventAccountCapsNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	ea := m.EventAccount
	if ea.MaxConcurrentByCapital["30"] != MaxConcurrent30U ||
		ea.MaxConcurrentByCapital["100"] != MaxConcurrent100U ||
		ea.MaxConcurrentByCapital["1000"] != MaxConcurrent1000U {
		t.Fatal("max concurrent by capital drift")
	}
	if ea.MaxEventStressLossFraction != MaxEventStressLossFraction ||
		ea.MaxAccountStressLossFraction != MaxAccountStressLossFraction ||
		ea.MaxAccountLeverage != MaxAccountLeverage {
		t.Fatal("stress/leverage caps drift")
	}
	if ea.QueueRankingRule != QueueRankingRule ||
		!ea.OnePositionPerEvent || !OnePositionPerEvent ||
		ea.PositionNotionalRule != PositionNotionalRule ||
		ea.MinNotionalLotFloorRule != MinNotionalLotFloorRule ||
		ea.PendingFlushRule != PendingFlushRule ||
		ea.PendingFlushExecuteAt != PendingFlushExecuteAt ||
		ea.ExecuteIndexFormula != PendingExecuteIndexFormula ||
		ea.EffectiveLeverageCheckRule != EffectiveLeverageCheckRule ||
		ea.RiskGateAfterBreach != RiskGateAfterBreachRule {
		t.Fatalf("event_account drift: %+v", ea)
	}
}

func TestEventUnitNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	eu := m.EventUnit
	if eu.EventIDFormula != EventIDFormula ||
		eu.WindowNonoverlapRequired != WindowNonoverlapRequired ||
		eu.NonOverlapCooldownFormula != NonOverlapCooldownFormula ||
		eu.ShockClusterRequired != ShockClusterRequired ||
		eu.ShockClusterRule != ShockClusterRule ||
		eu.IndependentSampleUnit != IndependentSampleUnit ||
		!eu.DiscoveryDedupPerOutcomeHorizonDelay || !DiscoveryDedupPerOutcomeHorizonDelay ||
		eu.DiscoveryDedupTrackerKey != DiscoveryDedupTrackerKey {
		t.Fatalf("event_unit drift: %+v", eu)
	}
}

func TestDynamicExitNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	de := m.DynamicExit
	if !de.StopErrorRequired || !StopErrorRequired ||
		!de.DeadlineIsFallbackOnly || !DeadlineIsFallbackOnly ||
		!de.MechanicalPriceStopSecondaryOnly || !MechanicalPriceStopSecondaryOnly {
		t.Fatal("dynamic_exit flags drift")
	}
	assertStrSlice(t, "exit_triggers", de.ExitTriggers, StopErrorExitTriggers)
}

func TestExamCommitmentsNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	if m.ExamCommitments.SchemaVersion != PublicCommitmentSchemaV1 ||
		m.ExamCommitments.RequiredPaperCount != ExamRequiredPaperCount ||
		m.ExamCommitments.StandardPaperCount != ExamStandardPaperCount ||
		m.ExamCommitments.GoldenPaperCount != ExamGoldenPaperCount {
		t.Fatal("exam commitment counts drift")
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

func TestExamUnavailableNoWait(t *testing.T) {
	m := loadG6Manifest(t)
	if m.ExamCommitments.NoValidReservationsStatus != ExamUnavailableStatus {
		t.Fatal("exam unavailable status drift")
	}
}

func TestValidationGatesNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	vg := m.ValidationGates
	if vg.ApplyOn != ValidationApplyOn ||
		!vg.NoExam || ValidationNoExam != vg.NoExam ||
		!vg.FreezeBeforeValidation || ValidationFrozenBeforeReadingResults != vg.FreezeBeforeValidation ||
		vg.CostStressMultiplier != CostStressMultiplier ||
		vg.MinEventCount != ValidationMinEventCount ||
		vg.MinIndependentDays != ValidationMinIndependentDays ||
		vg.MinNonOverlapShockBlocks != ValidationMinNonOverlapShockBlocks ||
		!vg.BaseNetReturnMustBePositive || ValidationBaseNetReturnMustBePositive != vg.BaseNetReturnMustBePositive ||
		!vg.AllDelayNetReturnMustBePositive || ValidationAllDelayNetReturnMustBePositive != vg.AllDelayNetReturnMustBePositive ||
		!vg.CostStressNetReturnMustBePositive || ValidationCostStressNetReturnMustBePositive != vg.CostStressNetReturnMustBePositive ||
		!vg.AllEquityTiersMustPass || ValidationAllEquityTiersMustPass != vg.AllEquityTiersMustPass ||
		!vg.NoLeverageSolvencyBreach || ValidationNoLeverageSolvencyBreach != vg.NoLeverageSolvencyBreach ||
		!vg.DiscoveryScreenFDRRequired || ValidationDiscoveryScreenFDRRequired != vg.DiscoveryScreenFDRRequired ||
		vg.DiscoveryScreenFamily != ValidationDiscoveryScreenFamily ||
		vg.EffectiveLeverageCheckRule != EffectiveLeverageCheckRule ||
		vg.PassStatus != ValidationPassStatus ||
		vg.FailStatus != ValidationFailStatus ||
		vg.InconclusiveStatus != ValidationInconclusiveStatus ||
		vg.SparseSampleAction != ValidationSparseSampleAction ||
		vg.MinEventCountInconclusiveNote != ValidationMinEventCountInconclusiveNote ||
		vg.SolvencyFailReason != ValidationSolvencyFailReason ||
		vg.MaxSingleDayPositiveContributionFraction != ValidationMaxSingleDayPositiveContributionFraction ||
		vg.MaxSingleEventPositiveContributionFraction != ValidationMaxSingleEventPositiveContributionFraction {
		t.Fatalf("validation_gates drift: %+v", vg)
	}
	assertFloatSlice(t, "validation_initial_equities", vg.InitialEquityUSDT, ValidationInitialEquities)
	assertIntSlice(t, "validation_delay_bars", vg.DelayBars, ValidationDelayBars)
}

func TestWalkForwardGatesNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	wf := m.WalkForwardGates
	if wf.Partition != WFPartition ||
		wf.FoldCount != WFFoldCount ||
		wf.UsesProfilePurgeEmbargo != WFUsesProfilePurgeEmbargo ||
		wf.Eligibility != WFEligibility ||
		wf.SkippedValidationFailStatus != WFSkippedValidationFailStatus ||
		!wf.FrozenCardNoRetuning || !WFFrozenCardNoRetuning ||
		!wf.ScanIncludesAllTriggers || !WFScanIncludesAllTriggers ||
		!wf.SizingUsesSurvivorFrozenEdge || !WFSizingUsesSurvivorFrozenEdge ||
		!wf.PnLUsesRealizedTrigger || !WFSizingPnLUsesRealizedTrigger ||
		wf.SizingEdgeFloorBPS != WFSizingEdgeFloorBPS ||
		wf.SizingEdgeSource != WFSizingEdgeSource ||
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
		wf.SignTest.Family != WFEventCardAttemptFamily ||
		wf.SignTest.FamilyAlpha != WFFamilyAlpha ||
		!wf.SignTest.DailyReplayMatchesScenarioFundingStress || !WFDailyReplayMatchesScenarioFunding {
		t.Fatalf("walk_forward_gates.sign_test drift: %+v", wf.SignTest)
	}
}

func TestEventMechanismReviewNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	em := m.EventMechanismReview
	if em.UnexplainedEventPassStatus != MechanismUnexplainedEventPassStatus ||
		!em.ForcedFlowEvidenceRequired || !MechanismForcedFlowEvidenceRequired ||
		!em.AutoMechanismFromFeaturesForbidden || !MechanismAutoFromFeaturesForbidden ||
		em.TradeAllowed != MechanismTradeAllowed ||
		em.ExamEligible != MechanismExamEligible ||
		em.Certified != MechanismCertified ||
		em.ExamActions != ExamActions {
		t.Fatalf("event_mechanism_review drift: %+v", em)
	}
}

func TestExamEvaluatorGatesNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	ee := m.ExamEvaluator
	if ee.FixtureSchemaVersion != ExamFixtureSchemaV1 ||
		ee.MinBlocks != ExamFixtureMinBlocks ||
		ee.ExpectedCommitmentHashFormula != ExamExpectedCommitmentHashFormula ||
		!ee.RegistrationRequiresReplayCallback ||
		!ee.ReplayRequired || !ExamReplayRequired ||
		!ee.ReplayGradesComputedOnly || !ExamReplayGradesComputedOnly ||
		!ee.Gates.BaseNetReturnMustBePositive || ExamBaseNetReturnMustBePositive != ee.Gates.BaseNetReturnMustBePositive ||
		!ee.Gates.AllDelayNetReturnMustBePositive || ExamAllDelayNetReturnMustBePositive != ee.Gates.AllDelayNetReturnMustBePositive ||
		ee.Gates.MinIndependentDays != ExamMinIndependentDays ||
		!ee.Gates.NoLeverageSolvencyBreach || ExamNoLeverageSolvencyBreach != ee.Gates.NoLeverageSolvencyBreach ||
		ee.Gates.CostStressMultiplier != ExamCostStressMultiplier ||
		!ee.Gates.CostStressNetReturnMustBePositive || ExamCostStressNetReturnMustBePositive != ee.Gates.CostStressNetReturnMustBePositive ||
		!ee.Gates.ShockBlockLOORequired || ExamShockBlockLOORequired != ee.Gates.ShockBlockLOORequired ||
		!ee.Gates.MaxConcurrentByCapitalRequired || ExamMaxConcurrentByCapitalRequired != ee.Gates.MaxConcurrentByCapitalRequired {
		t.Fatalf("exam_evaluator drift: %+v", ee)
	}
	if ee.BoundaryStatuses.Unavailable != ExamUnavailableStatus ||
		ee.BoundaryStatuses.NoGo != ExamBoundaryNoGoStatus ||
		ee.BoundaryStatuses.Available != ExamBoundaryAvailableStatus {
		t.Fatalf("exam boundary statuses drift: %+v", ee.BoundaryStatuses)
	}
}

func TestRuntimeResourceNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	rr := m.RuntimeResource
	if rr.GOMAXPROCS != RuntimeGOMAXPROCS ||
		rr.NicePriority != RuntimeNicePriority ||
		rr.CheckpointRequired != RuntimeCheckpointRequired ||
		rr.CheckpointShardRule != RuntimeCheckpointShardRule ||
		rr.ForbiddenFullServerScan != RuntimeForbiddenFullServerScan {
		t.Fatalf("runtime_resource drift: %+v", rr)
	}
}

func TestReverseEventCardsNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	rec := m.ReverseEventCards
	if rec.DiscoveryMode != DiscoveryMode ||
		!rec.InstitutionalModelForbidden || !InstitutionalModelForbidden ||
		rec.UnexplainedCandidateStatus != UnexplainedCandidateStatus ||
		rec.GroupingUnit != ReverseGroupingUnit {
		t.Fatal("reverse_event_cards drift")
	}
	assertStrSlice(t, "event_card_required_fields", rec.EventCardRequiredFields, EventCardRequiredFields)
	assertStrSlice(t, "hash_chain", rec.HashChain, EventCardHashChain)
}

func TestEventAccountPendingNoDrift(t *testing.T) {
	m := loadG6Manifest(t)
	ea := m.EventAccount
	if ea.PendingFlushRule != PendingFlushRule ||
		ea.PendingFlushExecuteAt != PendingFlushExecuteAt ||
		ea.ExecuteIndexFormula != PendingExecuteIndexFormula ||
		ea.StaleRevalidateAtExecution != PendingStaleRevalidate {
		t.Fatalf("event_account pending drift: %+v", ea)
	}
}
