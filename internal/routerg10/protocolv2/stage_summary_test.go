package protocolv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	g10constitution "lingxi/internal/routerg10/constitution"
	"lingxi/internal/datatruth"
)

const g10pStageSummaryRelPath = "../../../research/firstbattle/g10_personal_exclusion_stage_summary.json"

// Frozen formal-replay fingerprints for g10_personal_exclusion_stage_summary.json.
const (
	StageSummaryConstitutionHash            = "815b7055fd167bc091e8d5c4c2e344b3b4f27167e1842d1fca0c4d16fe8fa4b3"
	StageSummaryAnnouncementManifestHash    = "ec83be1d6a2b97c28c50685a4af019c38ba9fd9c4b0f07e29b3c11d142d2e4c0"
	StageSummaryExclusionManifestHash       = "865733c7a84468632b40e13c3177f6bd3ec4d572489b9c07b49468069c5ea864"
	StageSummaryEntryProofBundleHash        = "ce48b3923282b60dc2fafb36d8e9a5d32d43f7afcf4388a40379c78c16e900df"
	StageSummaryExecutionPolicyBundleHash   = "49896adcba31ffdb5d56059b8758dd9fef3c8f77228e920f4f6abb7ce2bfe6e3"
	StageSummaryPersonalBrainTraceHash      = "2fd1b2d0d7a26238f2cd3216312fbff60a217a3e931bd4f4ef9a4d0524e5c587"
	StageSummaryG6MapManifestHashOKX1H      = "3dd4437ae829ed00931ea61b37b874cb3e8b03aa685a1a36681b16ecc531c852"
	StageSummaryG6MapManifestHashOKX5M      = "2c09a3dd722415d2278fbb2ed9446280871827420db613bb7f08431c97eea84a"
	StageSummaryUpstreamFailClosedStatus    = "NOT_RUN_UPSTREAM_FAIL_CLOSED"
	StageSummaryExclusionFailClosedReason   = "INCOMPLETE_ANNOUNCEMENT_PIT"
	StageSummaryHash                        = "d2dd0ce87c190d07350659c1cb5222d40af519c01b67ddc03e970179ff9a41d1"
)

type g10pStageSummary struct {
	SchemaVersion     string `json:"schema_version"`
	Generation        string `json:"generation"`
	ProtocolHash      string `json:"protocol_hash"`
	ConstitutionHash  string `json:"constitution_hash"`
	HistoryDataHash   string `json:"history_data_hash"`
	G6FullMarketMaps  map[string]struct {
		Coverage         string `json:"coverage"`
		MapManifestHash  string `json:"map_manifest_hash"`
	} `json:"g6_full_market_maps"`
	Announcements struct {
		FormalReplay       bool     `json:"formal_replay"`
		Incomplete         bool     `json:"incomplete"`
		IncompleteReasons  []string `json:"incomplete_reasons"`
		TotalAnnouncements int      `json:"total_announcements"`
		TapeHash           string   `json:"tape_hash"`
		ManifestHash       string   `json:"manifest_hash"`
	} `json:"announcements"`
	ExclusionMap struct {
		FormalReplay      bool   `json:"formal_replay"`
		FailClosed        bool   `json:"fail_closed"`
		FailClosedReason  string `json:"fail_closed_reason"`
		AttemptCount      int    `json:"attempt_count"`
		AcceptedCount     int    `json:"accepted_count"`
		RejectedCount     int    `json:"rejected_count"`
		ManifestHash      string `json:"manifest_hash"`
	} `json:"exclusion_map"`
	ReverseTapes struct {
		Status              string `json:"status"`
		UpstreamBlockReason string `json:"upstream_block_reason"`
	} `json:"reverse_tapes"`
	Playbooks struct {
		Status              string `json:"status"`
		UpstreamBlockReason string `json:"upstream_block_reason"`
		ResearchPassPlaybooks int  `json:"research_pass_playbooks"`
	} `json:"playbooks"`
	Validation struct {
		Status              string `json:"status"`
		UpstreamBlockReason string `json:"upstream_block_reason"`
		Pass                int    `json:"pass"`
		Fail                int    `json:"fail"`
		Inconclusive        int    `json:"inconclusive"`
	} `json:"validation"`
	WalkForward struct {
		Status              string `json:"status"`
		UpstreamBlockReason string `json:"upstream_block_reason"`
		Pass                int    `json:"pass"`
	} `json:"walk_forward"`
	PathFixtures struct {
		EntryProofBundleHash      string `json:"entry_proof_bundle_hash"`
		ExecutionPolicyBundleHash string `json:"execution_policy_bundle_hash"`
		PersonalBrainTraceHash    string `json:"personal_brain_trace_hash"`
		PersonalBrainReplayMatch  bool   `json:"personal_brain_replay_match"`
		PersonalBrainDegeneracyStatus string `json:"personal_brain_degeneracy_status"`
	} `json:"path_fixtures"`
	Verification struct {
		UnitTests          string `json:"unit_tests"`
		RaceDetector       string `json:"race_detector"`
		GoVet              string `json:"go_vet"`
		RedTeamProtocolV2  string `json:"red_team_protocolv2"`
	} `json:"verification"`
	ExamStatus    string `json:"exam_status"`
	TradeAllowed  bool   `json:"trade_allowed"`
	SystemStatus  string `json:"system_status"`
	Conclusion    string `json:"conclusion"`
	ExamActions   struct {
		Created   int `json:"created"`
		Unsealed  int `json:"unsealed"`
		Consumed  int `json:"consumed"`
	} `json:"exam_actions"`
	StageSummaryHash *string `json:"stage_summary_hash"`
}

func loadG10pStageSummary(t *testing.T) g10pStageSummary {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g10pStageSummaryRelPath))
	if err != nil {
		t.Fatalf("read g10p stage summary: %v", err)
	}
	var s g10pStageSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode g10p stage summary: %v", err)
	}
	return s
}

func TestStageSummaryHashMatchesCanonical(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(g10pStageSummaryRelPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["stage_summary_hash"] = nil
	hash, _, err := datatruth.HashJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := loadG10pStageSummary(t)
	if s.StageSummaryHash == nil || *s.StageSummaryHash != hash || hash != StageSummaryHash {
		t.Fatalf("stage_summary_hash = %v, recomputed = %q want %q", s.StageSummaryHash, hash, StageSummaryHash)
	}
}

func TestStageSummaryFormalReplayFactsNoDrift(t *testing.T) {
	s := loadG10pStageSummary(t)
	if s.Generation != Generation {
		t.Fatal("generation drift")
	}
	if s.ProtocolHash != RouterProtocolHash {
		t.Fatalf("protocol_hash = %q want %q", s.ProtocolHash, RouterProtocolHash)
	}
	if s.ConstitutionHash != StageSummaryConstitutionHash {
		t.Fatalf("constitution_hash = %q want %q", s.ConstitutionHash, StageSummaryConstitutionHash)
	}
	constHash, err := g10constitution.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if s.ConstitutionHash != constHash {
		t.Fatalf("constitution_hash != constitution.Hash(): %q vs %q", s.ConstitutionHash, constHash)
	}
	if s.HistoryDataHash != HistoryDataHash {
		t.Fatal("history_data_hash drift")
	}

	okx1h := s.G6FullMarketMaps["okx_1h"]
	okx5m := s.G6FullMarketMaps["okx_5m"]
	if okx1h.Coverage != G4FullMarketCoverageOKX1H || okx1h.MapManifestHash != StageSummaryG6MapManifestHashOKX1H {
		t.Fatal("okx_1h g6 map drift")
	}
	if okx5m.Coverage != G4FullMarketCoverageOKX5M || okx5m.MapManifestHash != StageSummaryG6MapManifestHashOKX5M {
		t.Fatal("okx_5m g6 map drift")
	}

	if !s.Announcements.FormalReplay || !s.Announcements.Incomplete ||
		s.Announcements.TotalAnnouncements != 0 ||
		s.Announcements.ManifestHash != StageSummaryAnnouncementManifestHash {
		t.Fatal("announcements formal replay drift")
	}
	if len(s.Announcements.IncompleteReasons) != 4 {
		t.Fatalf("incomplete_reasons = %d want 4", len(s.Announcements.IncompleteReasons))
	}

	if !s.ExclusionMap.FormalReplay || !s.ExclusionMap.FailClosed ||
		s.ExclusionMap.FailClosedReason != StageSummaryExclusionFailClosedReason ||
		s.ExclusionMap.AttemptCount != 0 || s.ExclusionMap.AcceptedCount != 0 ||
		s.ExclusionMap.ManifestHash != StageSummaryExclusionManifestHash {
		t.Fatal("exclusion_map formal replay drift")
	}

	for _, block := range []struct {
		name   string
		status string
		reason string
	}{
		{"reverse_tapes", s.ReverseTapes.Status, s.ReverseTapes.UpstreamBlockReason},
		{"playbooks", s.Playbooks.Status, s.Playbooks.UpstreamBlockReason},
		{"validation", s.Validation.Status, s.Validation.UpstreamBlockReason},
		{"walk_forward", s.WalkForward.Status, s.WalkForward.UpstreamBlockReason},
	} {
		if block.status != StageSummaryUpstreamFailClosedStatus ||
			block.reason != StageSummaryExclusionFailClosedReason {
			t.Fatalf("%s upstream fail-closed drift: status=%q reason=%q", block.name, block.status, block.reason)
		}
	}
	if s.Playbooks.ResearchPassPlaybooks != 0 ||
		s.Validation.Pass != 0 || s.Validation.Fail != 0 || s.Validation.Inconclusive != 0 ||
		s.WalkForward.Pass != 0 {
		t.Fatal("downstream counts must remain zero when upstream fail-closed")
	}

	pf := s.PathFixtures
	if pf.EntryProofBundleHash != StageSummaryEntryProofBundleHash ||
		pf.ExecutionPolicyBundleHash != StageSummaryExecutionPolicyBundleHash ||
		pf.PersonalBrainTraceHash != StageSummaryPersonalBrainTraceHash ||
		!pf.PersonalBrainReplayMatch || pf.PersonalBrainDegeneracyStatus != "OK" {
		t.Fatal("path_fixtures drift")
	}

	if s.ExamStatus != ExamUnavailableStatus || s.TradeAllowed != TradeAllowedDefault ||
		s.SystemStatus != "NO_GO" || s.ExamActions.Created != 0 ||
		s.ExamActions.Unsealed != 0 || s.ExamActions.Consumed != 0 {
		t.Fatal("exam/trade boundary drift")
	}
	if s.Verification.UnitTests != "PASS" || s.Verification.RaceDetector != "PASS" ||
		s.Verification.GoVet != "PASS" || s.Verification.RedTeamProtocolV2 != "PASS" {
		t.Fatal("verification status drift")
	}
}
