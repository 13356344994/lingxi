package protocolv2

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/datatruth"
)

const (
	g10pManifestRelPath = "../../../research/firstbattle/hierarchical_router_g10_personal_exclusion_preregistration.json"
	g10ManifestRelPath  = "../../../research/firstbattle/hierarchical_router_g10_preregistration.json"
	g6ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_g6_preregistration.json"
	g5ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_g5_preregistration.json"
	g4ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_g4_preregistration.json"
	g3ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_g3_preregistration.json"
	g2ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath   = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	g2StageSummaryRelPath = "../../../research/firstbattle/g2_stage_summary.json"
	g3StageSummaryRelPath = "../../../research/firstbattle/g3_stage_summary.json"
	g4StageSummaryRelPath = "../../../research/firstbattle/g4_stage_summary.json"
	g5StageSummaryRelPath = "../../../research/firstbattle/g5_stage_summary.json"
	g6StageSummaryRelPath = "../../../research/firstbattle/g6_stage_summary.json"
)

type g10pManifest struct {
	ProtocolID string `json:"protocol_id"`
	Generation string `json:"generation"`

	ClosesG1 struct {
		G1RouterProtocolHash string `json:"g1_router_protocol_hash"`
		G1SystemStatus       string `json:"g1_system_status"`
	} `json:"closes_g1"`
	ClosesG2 struct {
		G2RouterProtocolHash           string `json:"g2_router_protocol_hash"`
		G2RegisteredSearchCombinations int    `json:"g2_registered_search_combinations"`
		G2SystemStatus                 string `json:"g2_system_status"`
	} `json:"closes_g2"`
	ClosesG3 struct {
		G3RouterProtocolHash    string `json:"g3_router_protocol_hash"`
		G3RegisteredSearchRules int    `json:"g3_registered_search_rules"`
		G3SystemStatus          string `json:"g3_system_status"`
	} `json:"closes_g3"`
	ClosesG4 struct {
		G4RouterProtocolHash       string            `json:"g4_router_protocol_hash"`
		G4FullMarketCoverage       map[string]string `json:"g4_full_market_coverage"`
		G4FrozenCandidates         int               `json:"g4_frozen_candidates"`
		G4ValidationPassCandidates int               `json:"g4_validation_pass_candidates"`
		G4SystemStatus             string            `json:"g4_system_status"`
	} `json:"closes_g4"`
	ClosesG5 struct {
		G5RouterProtocolHash string `json:"g5_router_protocol_hash"`
		G5FrozenPolicyCount  int    `json:"g5_frozen_policy_count"`
		G5TrainingStatus     string `json:"g5_training_status"`
		G5SystemStatus       string `json:"g5_system_status"`
	} `json:"closes_g5"`
	ClosesG6 struct {
		G6RouterProtocolHash  string `json:"g6_router_protocol_hash"`
		G6EventCards          int    `json:"g6_event_cards"`
		G6ValidationPassCards int    `json:"g6_validation_pass_cards"`
		G6SystemStatus        string `json:"g6_system_status"`
		G6ExamStatus          string `json:"g6_exam_status"`
	} `json:"closes_g6"`
	ClosesG10 struct {
		G10RouterProtocolHash           string `json:"g10_router_protocol_hash"`
		G10ZeroCandidatesInterpretation string `json:"g10_zero_candidates_interpretation"`
		G10LegacyStressCapsPreserved    struct {
			MaxEventStressLossFraction   float64 `json:"max_event_stress_loss_fraction"`
			MaxAccountStressLossFraction float64 `json:"max_account_stress_loss_fraction"`
		} `json:"g10_legacy_stress_caps_preserved"`
	} `json:"closes_g10"`

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
		G2RouterProtocolHash        string `json:"g2_router_protocol_hash"`
		G3RouterProtocolHash        string `json:"g3_router_protocol_hash"`
		G4RouterProtocolHash        string `json:"g4_router_protocol_hash"`
		G5RouterProtocolHash        string `json:"g5_router_protocol_hash"`
		G6RouterProtocolHash        string `json:"g6_router_protocol_hash"`
		G10RouterProtocolHash       string `json:"g10_router_protocol_hash"`
		ConstitutionSchemaVersion   string `json:"constitution_schema_version"`
	} `json:"binds"`

	PersonalConstitution struct {
		SchemaVersion                    string   `json:"schema_version"`
		CapitalTiersUSDT                 []float64 `json:"capital_tiers_usdt"`
		MaxConcurrentByCapital           map[string]int `json:"max_concurrent_by_capital"`
		MaxEventTerminalLossFraction     float64  `json:"max_event_terminal_loss_fraction"`
		MaxAccountTerminalRiskFraction   float64  `json:"max_account_terminal_risk_fraction"`
		RiskCeilingIsNotSizingTarget     bool     `json:"risk_ceiling_is_not_sizing_target"`
		SizingDirection                  string   `json:"sizing_direction"`
		ExecutionVenueOKXOnly            string   `json:"execution_venue_okx_only"`
		CrossVenuePITInfoVenues          []string `json:"cross_venue_pit_info_venues"`
		CrossVenueExecutionForbidden     bool     `json:"cross_venue_execution_forbidden"`
		AllowLargeFloatingLoss           bool     `json:"allow_large_floating_loss"`
		AllowOvernightHold               bool     `json:"allow_overnight_hold"`
		AllowCrossFundingSettlement      bool     `json:"allow_cross_funding_settlement"`
		MultiVenuePresetCapitalFuture    bool     `json:"multi_venue_preset_capital_future"`
		NoManualConfirmationRequired     bool     `json:"no_manual_confirmation_required"`
		LongTermIdleCapacityAllowed      bool     `json:"long_term_idle_capacity_allowed"`
		SolvencyIndependentRequired      bool     `json:"solvency_independent_required"`
		ProhibitLossChasing              bool     `json:"prohibit_loss_chasing"`
		ProhibitUpwardRounding           bool     `json:"prohibit_upward_rounding"`
		ProhibitTopUpAfterToxicFill      bool     `json:"prohibit_top_up_after_toxic_fill"`
		ActualEquityTierLabel            string   `json:"actual_equity_tier_label"`
	} `json:"personal_constitution"`

	TripleExclusion struct {
		SchemaVersion                string   `json:"schema_version"`
		RequiredAxes                 []string `json:"required_axes"`
		InstitutionExclusionEvidence []string `json:"institution_exclusion_evidence"`
		RetailExclusionEvidence      []string `json:"retail_exclusion_evidence"`
		BotExclusionEvidence         []string `json:"bot_exclusion_evidence"`
		ForbiddenWildEvidence        []string `json:"forbidden_wild_evidence"`
	} `json:"triple_exclusion"`

	CrossVenuePITAnnouncements struct {
		SchemaVersion       string   `json:"schema_version"`
		ExecutionVenue      string   `json:"execution_venue"`
		InfoVenues          []string `json:"info_venues"`
		EventTypes          []string `json:"event_types"`
		RequiredFields      []string `json:"required_fields"`
		VisibilityRule      string   `json:"visibility_rule"`
		ForbiddenBackfill   []string `json:"forbidden_backfill"`
		FailOpenForbidden   bool     `json:"fail_open_forbidden"`
	} `json:"cross_venue_pit_announcements"`

	EntryProof struct {
		SchemaVersion                  string   `json:"schema_version"`
		RequiredFields                 []string `json:"required_fields"`
		DiscoveryAllowedGrades         []string `json:"discovery_allowed_grades"`
		CertificationAllowedGrades     []string `json:"certification_allowed_grades"`
		EntryUnprovenStatus            string   `json:"entry_unproven_status"`
		MakerTouchNotEqualFill         bool     `json:"maker_touch_not_equal_fill"`
		DynamicMinNotionalFromPIT      bool     `json:"dynamic_min_notional_from_pit"`
		StaticMinNotionalForbidden     bool     `json:"static_min_notional_forbidden"`
	} `json:"entry_proof"`

	ExecutionPolicy struct {
		SchemaVersion         string   `json:"schema_version"`
		ActionsCompared       []string `json:"actions_compared"`
		FreezeBeforeOutcomeKnown bool  `json:"freeze_before_outcome_known"`
		MakerQuadrants        []string `json:"maker_quadrants"`
		MissedProfitableMoveStatus string `json:"missed_profitable_move_status"`
		MissedProfitablePnLIsZero  bool   `json:"missed_profitable_pnl_is_zero"`
		NoLongLivedMaker      bool     `json:"no_long_lived_maker"`
		NoChaseAfterMiss      bool     `json:"no_chase_after_miss"`
		TakerIsNotDefault     bool     `json:"taker_is_not_default"`
		ConditionalMarketableLimitRequires []string `json:"conditional_marketable_limit_requires"`
		ToxicFillRevalidateStatus string `json:"toxic_fill_revalidate_status"`
		ToxicFillExitStatus       string `json:"toxic_fill_exit_status"`
		ForbiddenTopUpAfterToxicFill bool `json:"forbidden_top_up_after_toxic_fill"`
	} `json:"execution_policy"`

	ExitProof struct {
		SchemaVersion             string   `json:"schema_version"`
		RequiredFields            []string `json:"required_fields"`
		ExitRequiredForCertification bool  `json:"exit_required_for_certification"`
	} `json:"exit_proof"`

	BrainActions struct {
		PositionActions          []string `json:"position_actions"`
		ExecutionActionsCompared []string `json:"execution_actions_compared"`
	} `json:"brain_actions"`

	DynamicExit struct {
		StopErrorRequired           bool     `json:"stop_error_required"`
		SolvencyIndependentRequired bool     `json:"solvency_independent_required"`
		ExitTriggers                []string `json:"exit_triggers"`
		PlaybookCannotDisableSolvency bool   `json:"playbook_cannot_disable_solvency"`
	} `json:"dynamic_exit"`

	BrainTrace struct {
		SchemaVersion            string   `json:"schema_version"`
		RequiredFields           []string `json:"required_fields"`
		ReplayTraceHashMustMatch bool     `json:"replay_trace_hash_must_match"`
	} `json:"brain_trace"`

	Playbook struct {
		HashChain      []string `json:"hash_chain"`
		DiscoveryMode  string   `json:"discovery_mode"`
		NotPersonalEnoughStatus string `json:"not_personal_enough_status"`
	} `json:"playbook"`

	BrainMechanismReview struct {
		TradeAllowedDefault bool `json:"trade_allowed_default"`
		ExamEligible        bool `json:"exam_eligible"`
		Certified           bool `json:"certified"`
		ExamActions         int  `json:"exam_actions"`
	} `json:"brain_mechanism_review"`

	ExamCommitments struct {
		NoValidReservationsStatus string `json:"no_valid_reservations_status"`
		ResearchStageExamActions  int    `json:"research_stage_exam_actions"`
	} `json:"exam_commitments"`

	TimePartitionRoles struct {
		FullHistoryRequired      bool `json:"full_history_required"`
		SmokeSubstituteForbidden bool `json:"smoke_substitute_forbidden"`
		ExamActions              int  `json:"exam_actions"`
		ResearchPermille         int  `json:"research_permille"`
		ValidationPermille       int  `json:"validation_permille"`
		WalkForwardPermille      int  `json:"walk_forward_permille"`
		PartitionOrder           string `json:"partition_order"`
	} `json:"time_partition_roles"`

	RuntimeResource struct {
		GOMAXPROCS              int    `json:"gomaxprocs"`
		NicePriority            int    `json:"nice_priority"`
		CheckpointRequired      bool   `json:"checkpoint_required"`
		CheckpointShardRule     string `json:"checkpoint_shard_rule"`
		ForbiddenFullServerScan bool   `json:"forbidden_full_server_scan"`
	} `json:"runtime_resource"`

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

func loadG10pManifest(t *testing.T) g10pManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g10pManifestRelPath))
	if err != nil {
		t.Fatalf("read g10p manifest: %v", err)
	}
	var m g10pManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g10p manifest: %v", err)
	}
	return m
}

func assertStrSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d, want %d\ngot=%v\nwant=%v", name, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
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
	raw, err := os.ReadFile(filepath.Clean(g10pManifestRelPath))
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
	m := loadG10pManifest(t)
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != hash {
		t.Fatalf("manifest audit.protocol_hash = %v, recomputed = %q", m.Audit.ProtocolHash, hash)
	}
	if hash != RouterProtocolHash {
		t.Fatalf("RouterProtocolHash = %q, recomputed = %q", RouterProtocolHash, hash)
	}
}

func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	if m.ProtocolID != ProtocolID || m.Generation != Generation {
		t.Fatal("protocol_id/generation drift")
	}
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != RouterProtocolHash {
		t.Fatal("audit.protocol_hash drift")
	}
	if m.Audit.ExamActions != ExamActions || m.TimePartitionRoles.ExamActions != ExamActions {
		t.Fatal("exam_actions drift")
	}
	if len(m.RedTeam.Requirements) != 16 {
		t.Fatalf("red_team requirements = %d, want 16", len(m.RedTeam.Requirements))
	}
	if m.Binds.ConstitutionSchemaVersion != ConstitutionSchemaVersion {
		t.Fatal("constitution schema drift")
	}
}

func TestG1G2G3G4G5G6ResultReferencedImmutable(t *testing.T) {
	m := loadG10pManifest(t)
	if m.ClosesG1.G1RouterProtocolHash != G1RouterProtocolHash || m.ClosesG1.G1SystemStatus != G1SystemStatus {
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
		m.ClosesG5.G5SystemStatus != G5SystemStatus {
		t.Fatal("closes_g5 drift")
	}
	if m.ClosesG6.G6RouterProtocolHash != G6RouterProtocolHash ||
		m.ClosesG6.G6EventCards != G6EventCards ||
		m.ClosesG6.G6ValidationPassCards != G6ValidationPassCards ||
		m.ClosesG6.G6SystemStatus != G6SystemStatus ||
		m.ClosesG6.G6ExamStatus != G6ExamStatus {
		t.Fatal("closes_g6 drift")
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
		{g6ManifestRelPath, G6RouterProtocolHash},
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

func TestG10V1ResultReferencedImmutable(t *testing.T) {
	m := loadG10pManifest(t)
	if m.ClosesG10.G10RouterProtocolHash != G10RouterProtocolHash {
		t.Fatal("closes_g10 hash drift")
	}
	if m.ClosesG10.G10ZeroCandidatesInterpretation != G10ZeroCandidatesInterpretation {
		t.Fatal("closes_g10 zero candidates interpretation drift")
	}
	if m.ClosesG10.G10LegacyStressCapsPreserved.MaxEventStressLossFraction != G10LegacyMaxEventStressLossFraction ||
		m.ClosesG10.G10LegacyStressCapsPreserved.MaxAccountStressLossFraction != G10LegacyMaxAccountStressLossFraction {
		t.Fatal("closes_g10 legacy stress caps drift")
	}
	if m.Binds.G10RouterProtocolHash != G10RouterProtocolHash {
		t.Fatal("binds g10 v1 hash drift")
	}

	raw, err := os.ReadFile(filepath.Clean(g10ManifestRelPath))
	if err != nil {
		t.Fatal(err)
	}
	var g10Doc map[string]any
	if err := json.Unmarshal(raw, &g10Doc); err != nil {
		t.Fatal(err)
	}
	audit := g10Doc["audit"].(map[string]any)
	if audit["protocol_hash"] != G10RouterProtocolHash {
		t.Fatalf("g10 v1 manifest hash changed: %v", audit["protocol_hash"])
	}
	if G10RouterProtocolHash != g10protocol.RouterProtocolHash {
		t.Fatal("g10 v1 protocol.go hash drift")
	}
}

func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadG10pManifest(t)
	if m.Binds.HistoryDataHash != HistoryDataHash ||
		m.Binds.HistoryRegimeContractHash != HistoryRegimeContractHash ||
		m.Binds.HistoryDataRequirementsHash != HistoryDataRequirementsHash ||
		m.Binds.G1RouterProtocolHash != G1RouterProtocolHash ||
		m.Binds.G2RouterProtocolHash != G2RouterProtocolHash ||
		m.Binds.G3RouterProtocolHash != G3RouterProtocolHash ||
		m.Binds.G4RouterProtocolHash != G4RouterProtocolHash ||
		m.Binds.G5RouterProtocolHash != G5RouterProtocolHash ||
		m.Binds.G6RouterProtocolHash != G6RouterProtocolHash ||
		m.Binds.G10RouterProtocolHash != G10RouterProtocolHash {
		t.Fatal("binds drift")
	}
}

func TestPersonalConstitutionNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	pc := m.PersonalConstitution
	if pc.SchemaVersion != ConstitutionSchemaVersion {
		t.Fatal("constitution schema drift")
	}
	if ConstitutionSchemaVersion != g10constitution.SchemaVersion {
		t.Fatal("constitution package schema drift")
	}
	assertFloatSlice(t, "capital_tiers", pc.CapitalTiersUSDT, CapitalTiersUSDT)
	if pc.MaxEventTerminalLossFraction != MaxEventTerminalLossFraction ||
		pc.MaxAccountTerminalRiskFraction != MaxAccountTerminalRiskFraction ||
		!pc.RiskCeilingIsNotSizingTarget || !RiskCeilingIsNotSizingTarget ||
		pc.SizingDirection != SizingDirection {
		t.Fatalf("terminal risk ceiling drift: %+v", pc)
	}
	if pc.ExecutionVenueOKXOnly != ExecutionVenueOKXOnly ||
		!pc.CrossVenueExecutionForbidden || !CrossVenueExecutionForbidden {
		t.Fatal("execution venue drift")
	}
	assertStrSlice(t, "cross_venue_pit_info_venues", pc.CrossVenuePITInfoVenues, CrossVenuePITVenues)
	if pc.MaxConcurrentByCapital["30"] != MaxConcurrent30U ||
		pc.MaxConcurrentByCapital["100"] != MaxConcurrent100U ||
		pc.MaxConcurrentByCapital["1000"] != MaxConcurrent1000U {
		t.Fatal("max concurrent drift")
	}
}

func TestOKXOnlyCrossVenuePITNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	cv := m.CrossVenuePITAnnouncements
	if cv.SchemaVersion != CrossVenuePITAnnouncementsSchemaV1 ||
		cv.ExecutionVenue != ExecutionVenueOKXOnly ||
		cv.VisibilityRule != CrossVenueVisibilityRule ||
		!cv.FailOpenForbidden || !CrossVenueFailOpenForbidden {
		t.Fatalf("cross venue pit drift: %+v", cv)
	}
	assertStrSlice(t, "info_venues", cv.InfoVenues, CrossVenuePITInfoVenues)
	assertStrSlice(t, "event_types", cv.EventTypes, CrossVenueAnnouncementEventTypes)
	assertStrSlice(t, "required_fields", cv.RequiredFields, CrossVenueAnnouncementRequiredFields)
	assertStrSlice(t, "forbidden_backfill", cv.ForbiddenBackfill, CrossVenueForbiddenBackfill)
}

func TestTripleExclusionNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	te := m.TripleExclusion
	if te.SchemaVersion != TripleExclusionSchemaVersion {
		t.Fatal("triple exclusion schema drift")
	}
	assertStrSlice(t, "required_axes", te.RequiredAxes, TripleExclusionAxes)
	assertStrSlice(t, "institution_evidence", te.InstitutionExclusionEvidence, InstitutionExclusionEvidence)
	assertStrSlice(t, "retail_evidence", te.RetailExclusionEvidence, RetailExclusionEvidence)
	assertStrSlice(t, "bot_evidence", te.BotExclusionEvidence, BotExclusionEvidence)
	assertStrSlice(t, "forbidden_wild_evidence", te.ForbiddenWildEvidence, ForbiddenWildEvidence)
}

func TestEntryProofNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	ep := m.EntryProof
	if ep.SchemaVersion != EntryProofSchemaV1 ||
		ep.EntryUnprovenStatus != EntryUnprovenStatus ||
		!ep.MakerTouchNotEqualFill ||
		!ep.DynamicMinNotionalFromPIT ||
		!ep.StaticMinNotionalForbidden {
		t.Fatalf("entry proof drift: %+v", ep)
	}
	assertStrSlice(t, "entry_proof_fields", ep.RequiredFields, EntryProofRequiredFields)
	assertStrSlice(t, "discovery_grades", ep.DiscoveryAllowedGrades, EntryProofDiscoveryAllowedGrades)
	assertStrSlice(t, "certification_grades", ep.CertificationAllowedGrades, EntryProofCertificationAllowedGrades)
}

func TestExecutionPolicyNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	ep := m.ExecutionPolicy
	if ep.SchemaVersion != ExecutionPolicySchemaV1 ||
		!ep.FreezeBeforeOutcomeKnown ||
		ep.MissedProfitableMoveStatus != MissedProfitableMoveStatus ||
		!ep.MissedProfitablePnLIsZero ||
		!ep.NoLongLivedMaker ||
		!ep.NoChaseAfterMiss ||
		!ep.TakerIsNotDefault ||
		ep.ToxicFillRevalidateStatus != ToxicFillRevalidateStatus ||
		ep.ToxicFillExitStatus != ToxicFillExitStatus ||
		!ep.ForbiddenTopUpAfterToxicFill {
		t.Fatalf("execution policy drift: %+v", ep)
	}
	assertStrSlice(t, "actions_compared", ep.ActionsCompared, ExecutionActionsCompared)
	assertStrSlice(t, "maker_quadrants", ep.MakerQuadrants, MakerFillQuadrants)
	assertStrSlice(t, "conditional_marketable_limit_requires", ep.ConditionalMarketableLimitRequires, ConditionalMarketableLimitRequires)
}

func TestExitProofNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	xp := m.ExitProof
	if xp.SchemaVersion != ExitProofSchemaV1 || !xp.ExitRequiredForCertification {
		t.Fatalf("exit proof drift: %+v", xp)
	}
	assertStrSlice(t, "exit_proof_fields", xp.RequiredFields, ExitProofRequiredFields)
}

func TestSolvencyAndProhibitionsNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	pc := m.PersonalConstitution
	de := m.DynamicExit
	if !pc.SolvencyIndependentRequired || !SolvencyIndependentRequired ||
		!pc.ProhibitLossChasing || !ProhibitLossChasing ||
		!pc.ProhibitUpwardRounding || !ProhibitUpwardRounding ||
		!pc.ProhibitTopUpAfterToxicFill || !ProhibitTopUpAfterToxicFill ||
		!pc.AllowLargeFloatingLoss || !AllowLargeFloatingLoss ||
		!pc.AllowOvernightHold || !AllowOvernightHold ||
		!pc.AllowCrossFundingSettlement || !AllowCrossFundingSettlement {
		t.Fatal("personal tolerance/prohibition drift")
	}
	if !de.StopErrorRequired || !de.SolvencyIndependentRequired || !de.PlaybookCannotDisableSolvency {
		t.Fatal("dynamic_exit solvency drift")
	}
	assertStrSlice(t, "exit_triggers", de.ExitTriggers, StopErrorExitTriggers)
}

func TestTradeFalseExamUnavailableNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	bm := m.BrainMechanismReview
	if bm.TradeAllowedDefault != TradeAllowedDefault ||
		bm.ExamEligible != ExamEligible ||
		bm.Certified != Certified ||
		bm.ExamActions != ExamActions {
		t.Fatalf("brain_mechanism_review drift: %+v", bm)
	}
	if m.ExamCommitments.NoValidReservationsStatus != ExamUnavailableStatus ||
		m.ExamCommitments.ResearchStageExamActions != ExamActions {
		t.Fatal("exam unavailable drift")
	}
}

func TestPlaybookHashChainNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	if m.Playbook.DiscoveryMode != DiscoveryMode ||
		m.Playbook.NotPersonalEnoughStatus != NotPersonalEnoughStatus {
		t.Fatal("playbook mode drift")
	}
	assertStrSlice(t, "playbook_hash_chain", m.Playbook.HashChain, PlaybookHashChain)
}

func TestBrainTraceV2NoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	bt := m.BrainTrace
	if bt.SchemaVersion != BrainTraceSchemaV2 || !bt.ReplayTraceHashMustMatch {
		t.Fatalf("brain_trace v2 drift: %+v", bt)
	}
	assertStrSlice(t, "brain_trace_required_fields", bt.RequiredFields, BrainTraceV2RequiredFields)
	assertStrSlice(t, "brain_position_actions", m.BrainActions.PositionActions, BrainPositionActions)
	assertStrSlice(t, "execution_actions_compared", m.BrainActions.ExecutionActionsCompared, ExecutionActionsCompared)
}

func TestFullHistoryValidationNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	tp := m.TimePartitionRoles
	if !tp.FullHistoryRequired || !FullHistoryRequired ||
		!tp.SmokeSubstituteForbidden || !SmokeSubstituteForbidden ||
		tp.ResearchPermille != ResearchPermille ||
		tp.ValidationPermille != ValidationPermille ||
		tp.WalkForwardPermille != WalkForwardPermille ||
		tp.PartitionOrder != PartitionOrder {
		t.Fatalf("time partition drift: %+v", tp)
	}
}

func TestRuntimeResourceNoDrift(t *testing.T) {
	m := loadG10pManifest(t)
	rr := m.RuntimeResource
	if rr.GOMAXPROCS != RuntimeGOMAXPROCS ||
		rr.NicePriority != RuntimeNicePriority ||
		rr.CheckpointRequired != RuntimeCheckpointRequired ||
		rr.CheckpointShardRule != RuntimeCheckpointShardRule ||
		rr.ForbiddenFullServerScan != RuntimeForbiddenFullServerScan {
		t.Fatalf("runtime_resource drift: %+v", rr)
	}
}

func TestG10V1ProtocolHashUntouched(t *testing.T) {
	if g10protocol.RouterProtocolHash != "9f86ce54c24b00de41d1a6735a4f8190b00d49dce2a66371242646ffecd10750" {
		t.Fatalf("g10 v1 protocol hash was modified: %q", g10protocol.RouterProtocolHash)
	}
	if RouterProtocolHash == g10protocol.RouterProtocolHash {
		t.Fatal("g10p hash must differ from g10 v1 hash")
	}
}
