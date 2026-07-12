package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/datatruth"
	"lingxi/internal/researchboundary"
)

// 只读取公开的 g2 路由协议 manifest、被绑定的公开契约(regime / requirements / g1 路由协议)与 g1 阶段结论。
// 绝不读取任何真实 records/收益/考卷或大 history 目录。
const (
	g2ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_g2_preregistration.json"
	g1ManifestRelPath     = "../../../research/firstbattle/hierarchical_router_preregistration.json"
	g1StageSummaryRelPath = "../../../research/firstbattle/router_stage_summary.json"
	regimeManifestRelPath = "../../../research/firstbattle/history_regime_contract.json"
	reqManifestRelPath    = "../../../research/firstbattle/history_data_requirements.json"
)

type g2Manifest struct {
	ProtocolID string `json:"protocol_id"`
	Generation string `json:"generation"`

	ClosesG1 struct {
		G1Generation           string `json:"g1_generation"`
		G1RouterProtocolHash   string `json:"g1_router_protocol_hash"`
		G1CertifiedMoneyLeaves int    `json:"g1_certified_money_leaves"`
		G1ResearchPassLeaves   int    `json:"g1_research_pass_leaves"`
		G1SystemStatus         string `json:"g1_system_status"`
	} `json:"closes_g1"`

	Binds struct {
		HistoryDataHash             string `json:"history_data_hash"`
		HistoryRegimeContractHash   string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash string `json:"history_data_requirements_hash"`
		G1RouterProtocolHash        string `json:"g1_router_protocol_hash"`
	} `json:"binds"`

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

	DiscoveryBudget struct {
		OutcomeHorizonsHours []int   `json:"outcome_horizons_hours"`
		DirectionSymmetry    string  `json:"direction_symmetry"`
		CostBPS              float64 `json:"cost_bps"`
		NeutralBandBPS       float64 `json:"neutral_band_bps"`
		Forward              struct {
			MaxFamilies            int `json:"max_families"`
			MaxCandidatesPerFamily int `json:"max_candidates_per_family"`
		} `json:"forward"`
		Backward struct {
			MaxFiles                    int      `json:"max_files"`
			MaxLines                    int      `json:"max_lines"`
			MaxSearchCombos             int      `json:"max_search_combinations"`
			AllowedSearchSpaceID        string   `json:"allowed_search_space_id"`
			AllowedSearchDefinitionHash string   `json:"allowed_search_space_definition_hash"`
			RegisteredSearchCombos      int      `json:"registered_search_combinations"`
			PrizeMechanism              string   `json:"prize_mechanism"`
			EconomicFamily              string   `json:"economic_family"`
			NoveltyDimensions           []string `json:"novelty_dimensions"`
			PrizeFeature                string   `json:"prize_feature"`
			PrizeThreshold              float64  `json:"prize_threshold"`
			MaxCandidates               int      `json:"max_candidates"`
			DefaultProgressEvery        int      `json:"default_progress_every"`
			MatchRule                   string   `json:"match_rule"`
			VolatilityTolerance         float64  `json:"volatility_tolerance"`
			LiquidityTolerance          float64  `json:"liquidity_tolerance"`
			NeutralBandBPS              float64  `json:"neutral_band_bps"`
			DiscoveryCostBPS            float64  `json:"discovery_cost_bps"`
			PermutationDraws            int      `json:"permutation_draws"`
			ExactPermutationMaxPairs    int      `json:"exact_permutation_max_pairs"`
			BootstrapSamples            int      `json:"bootstrap_samples"`
			Seed                        int64    `json:"seed"`
			MatchWindowMs               int64    `json:"match_window_ms"`
			BoundedSmoke                struct {
				MaxSymbols      int `json:"max_symbols"`
				MaxFundingLines int `json:"max_funding_lines"`
				MaxPriceLines   int `json:"max_price_lines"`
			} `json:"bounded_smoke"`
			CheckpointRequired bool `json:"checkpoint_required"`
		} `json:"backward"`
		MaxTotalCandidates int `json:"max_total_candidates"`
	} `json:"discovery_budget"`

	TimePartitionRoles struct {
		DiscoveryPermille   int `json:"discovery_permille"`
		ForwardPermille     int `json:"forward_permille"`
		WalkForwardPermille int `json:"walk_forward_permille"`
		EmbargoHours        int `json:"embargo_hours"`
		MinForwardSamples   int `json:"min_forward_samples"`
		MinForwardClusters  int `json:"min_forward_clusters"`
		WalkForwardFolds    int `json:"walk_forward_folds"`
		WFMinSamples        int `json:"wf_min_samples"`
		WFMinClusters       int `json:"wf_min_clusters"`
		PurgeHours          int `json:"purge_hours"`
		WFEmbargoHours      int `json:"wf_embargo_hours"`
	} `json:"time_partition_roles"`

	MultipleComparisonBudget struct {
		FamilyWiseControl string  `json:"family_wise_control"`
		FamilyWiseAlpha   float64 `json:"family_wise_alpha"`
		BackwardAlpha     float64 `json:"backward_alpha"`
		ForwardAlpha      float64 `json:"forward_alpha"`
		BackwardControl   string  `json:"backward_selection_control"`
		ForwardControl    string  `json:"forward_walk_forward_control"`
		MaxSearchCombos   int     `json:"max_search_combinations"`
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
	} `json:"audit"`
}

func loadG2Manifest(t *testing.T) g2Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(g2ManifestRelPath))
	if err != nil {
		t.Fatalf("read g2 manifest: %v", err)
	}
	var m g2Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode g2 manifest: %v", err)
	}
	return m
}

// TestManifestCodeConstantsNoDrift 冻结节点顺序、叶子状态、reason 码、司天监分桶、发现预算、
// 多重检验与红队条数:manifest ↔ protocol.go 常量必须逐字段一致,任一漂移即失败。
func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadG2Manifest(t)

	if m.ProtocolID != ProtocolID {
		t.Errorf("protocol_id = %q, want %q", m.ProtocolID, ProtocolID)
	}
	if m.Generation != Generation {
		t.Errorf("generation = %q, want %q", m.Generation, Generation)
	}
	if m.Audit.ProtocolHash == nil || *m.Audit.ProtocolHash != RouterProtocolHash {
		t.Errorf("audit.protocol_hash = %v, want %q", m.Audit.ProtocolHash, RouterProtocolHash)
	}

	// ---- 节点顺序 ----
	assertStrSlice(t, "node_order", m.NodeOrder, NodeOrder)

	// ---- 叶子状态机 ----
	assertStrSlice(t, "leaf_states", m.LeafStates, LeafStates)
	assertStrSlice(t, "leaf_state_machine.order", m.LeafStateMachine.Order, LeafStateOrder)
	assertStrSlice(t, "leaf_state_machine.terminal_closed", m.LeafStateMachine.TerminalClosed, TerminalClosedStates)

	// ---- reason 码 ----
	assertStrSlice(t, "reason_codes", m.ReasonCodes, ReasonCodes)

	// ---- coarse trace 禁泄漏子串 ----
	assertStrSlice(t, "coarse_trace_rule.must_not_contain", m.CoarseTraceRule.MustNotContain, CoarseTraceForbiddenSubstrings)

	// ---- 司天监分桶 ----
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

	// ---- 发现预算 ----
	assertIntSlice(t, "discovery_budget.outcome_horizons_hours", m.DiscoveryBudget.OutcomeHorizonsHours, OutcomeHorizonsHours)
	if m.DiscoveryBudget.DirectionSymmetry != OutcomeHorizonDirectionSymmetry {
		t.Errorf("direction_symmetry = %q, want %q", m.DiscoveryBudget.DirectionSymmetry, OutcomeHorizonDirectionSymmetry)
	}
	if m.DiscoveryBudget.CostBPS != DiscoveryCostBPS || m.DiscoveryBudget.NeutralBandBPS != NeutralBandBPS {
		t.Errorf("discovery cost/neutral band drifted: cost=%v neutral=%v", m.DiscoveryBudget.CostBPS, m.DiscoveryBudget.NeutralBandBPS)
	}
	checkInt(t, "forward.max_families", m.DiscoveryBudget.Forward.MaxFamilies, ForwardMaxFamilies)
	checkInt(t, "forward.max_candidates_per_family", m.DiscoveryBudget.Forward.MaxCandidatesPerFamily, ForwardMaxCandidatesPerFamily)
	checkInt(t, "backward.max_files", m.DiscoveryBudget.Backward.MaxFiles, BackwardMaxFiles)
	checkInt(t, "backward.max_lines", m.DiscoveryBudget.Backward.MaxLines, BackwardMaxLines)
	checkInt(t, "backward.max_search_combinations", m.DiscoveryBudget.Backward.MaxSearchCombos, BackwardMaxSearchCombos)
	if m.DiscoveryBudget.Backward.AllowedSearchSpaceID != BackwardSearchSpaceID ||
		m.DiscoveryBudget.Backward.AllowedSearchDefinitionHash != BackwardSearchDefinitionHash ||
		m.DiscoveryBudget.Backward.RegisteredSearchCombos != BackwardRegisteredCombos {
		t.Errorf("g2 search-space budget drifted")
	}
	if m.DiscoveryBudget.Backward.PrizeMechanism != G2PrizeMechanism ||
		m.DiscoveryBudget.Backward.EconomicFamily != G2PrizeEconomicFamily ||
		len(m.DiscoveryBudget.Backward.NoveltyDimensions) != 3 ||
		m.DiscoveryBudget.Backward.PrizeFeature != G2PrizeFeature ||
		m.DiscoveryBudget.Backward.PrizeThreshold != G2PrizeThreshold {
		t.Errorf("g2 prize mechanism definition drifted")
	}
	checkInt(t, "backward.max_candidates", m.DiscoveryBudget.Backward.MaxCandidates, BackwardMaxCandidates)
	checkInt(t, "backward.default_progress_every", m.DiscoveryBudget.Backward.DefaultProgressEvery, BackwardDefaultProgressEvery)
	if m.DiscoveryBudget.Backward.MatchRule != BackwardMatchRule {
		t.Errorf("backward.match_rule = %q, want %q", m.DiscoveryBudget.Backward.MatchRule, BackwardMatchRule)
	}
	if !m.DiscoveryBudget.Backward.CheckpointRequired {
		t.Errorf("backward.checkpoint_required must be true")
	}
	if m.DiscoveryBudget.Backward.VolatilityTolerance != BackwardVolatilityTolerance ||
		m.DiscoveryBudget.Backward.LiquidityTolerance != BackwardLiquidityTolerance ||
		m.DiscoveryBudget.Backward.NeutralBandBPS != NeutralBandBPS ||
		m.DiscoveryBudget.Backward.DiscoveryCostBPS != DiscoveryCostBPS ||
		m.DiscoveryBudget.Backward.PermutationDraws != BackwardPermutationDraws ||
		m.DiscoveryBudget.Backward.ExactPermutationMaxPairs != BackwardExactPermutationMaxPairs ||
		m.DiscoveryBudget.Backward.BootstrapSamples != BackwardBootstrapSamples ||
		m.DiscoveryBudget.Backward.Seed != BackwardSeed {
		t.Errorf("backward frozen statistical parameters drifted")
	}
	if m.DiscoveryBudget.Backward.MatchWindowMs != BackwardMatchWindowMs ||
		m.DiscoveryBudget.Backward.BoundedSmoke.MaxSymbols != G2SmokeMaxSymbols ||
		m.DiscoveryBudget.Backward.BoundedSmoke.MaxFundingLines != G2SmokeMaxFundingLines ||
		m.DiscoveryBudget.Backward.BoundedSmoke.MaxPriceLines != G2SmokeMaxPriceLines {
		t.Errorf("backward match/smoke bounds drifted")
	}
	if m.TimePartitionRoles.DiscoveryPermille != DiscoveryPermille ||
		m.TimePartitionRoles.ForwardPermille != ForwardPermille ||
		m.TimePartitionRoles.WalkForwardPermille != WalkForwardPermille ||
		m.TimePartitionRoles.EmbargoHours != PartitionEmbargoHours {
		t.Errorf("time partition roles drifted")
	}
	if m.TimePartitionRoles.MinForwardSamples != G2MinForwardSamples ||
		m.TimePartitionRoles.MinForwardClusters != G2MinForwardClusters ||
		m.TimePartitionRoles.WalkForwardFolds != G2WalkForwardFolds ||
		m.TimePartitionRoles.WFMinSamples != G2WFMinSamples ||
		m.TimePartitionRoles.WFMinClusters != G2WFMinClusters ||
		m.TimePartitionRoles.PurgeHours != G2PurgeHours ||
		m.TimePartitionRoles.WFEmbargoHours != G2WFEmbargoHours {
		t.Errorf("forward/WF frozen gates drifted")
	}
	if m.NodeInputs.Sorter.MinQuoteVolume != SorterMinQuoteVolume ||
		m.NodeInputs.Sorter.MinVolatilityCostRatio != SorterMinVolatilityCostRatio {
		t.Errorf("sorter frozen thresholds drifted")
	}
	checkInt(t, "discovery_budget.max_total_candidates", m.DiscoveryBudget.MaxTotalCandidates, MaxTotalCandidates)

	// ---- 多重检验 ----
	if m.MultipleComparisonBudget.FamilyWiseControl != FamilyWiseControl {
		t.Errorf("family_wise_control = %q, want %q", m.MultipleComparisonBudget.FamilyWiseControl, FamilyWiseControl)
	}
	if m.MultipleComparisonBudget.FamilyWiseAlpha != FamilyWiseAlpha {
		t.Errorf("family_wise_alpha = %v, want %v", m.MultipleComparisonBudget.FamilyWiseAlpha, FamilyWiseAlpha)
	}
	if m.MultipleComparisonBudget.BackwardAlpha != BackwardFamilyAlpha ||
		m.MultipleComparisonBudget.ForwardAlpha != ForwardFamilyAlpha ||
		m.MultipleComparisonBudget.BackwardControl != "Bonferroni" ||
		m.MultipleComparisonBudget.ForwardControl != FamilyWiseControl ||
		BackwardFamilyAlpha+ForwardFamilyAlpha > FamilyWiseAlpha {
		t.Errorf("g2 alpha allocation drifted")
	}
	checkInt(t, "multiple_comparison_budget.max_search_combinations", m.MultipleComparisonBudget.MaxSearchCombos, MaxSearchCombosTest)

	// ---- 红队 14 项 ----
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

// TestBoundHashesMatchCodeConstants 断言 manifest.binds 与 protocol.go 常量一致。
func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadG2Manifest(t)
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
}

// TestBoundHashesMatchReferencedContracts 从被引用的公开契约实际复现 hash,断言 manifest 绑定正确:
// - regime / requirements 通过 researchboundary 加载并校验各自 manifest_hash;
// - g1 路由协议 hash 通过 datatruth.HashJSON(audit.protocol_hash=null) 复现。
// 这防止 g2 协议绑定到与真实契约不符的 hash。
func TestBoundHashesMatchReferencedContracts(t *testing.T) {
	m := loadG2Manifest(t)

	regime, err := researchboundary.LoadRegimeContract(filepath.Clean(regimeManifestRelPath))
	if err != nil {
		t.Fatalf("load regime contract: %v", err)
	}
	if regime.ManifestHash == nil || *regime.ManifestHash != m.Binds.HistoryRegimeContractHash {
		t.Errorf("regime contract hash = %v, g2 binds = %q", regime.ManifestHash, m.Binds.HistoryRegimeContractHash)
	}

	req, err := researchboundary.LoadRequirements(filepath.Clean(reqManifestRelPath))
	if err != nil {
		t.Fatalf("load requirements: %v", err)
	}
	if req.ManifestHash == nil || *req.ManifestHash != m.Binds.HistoryDataRequirementsHash {
		t.Errorf("requirements hash = %v, g2 binds = %q", req.ManifestHash, m.Binds.HistoryDataRequirementsHash)
	}

	g1Hash := reproduceProtocolHash(t, g1ManifestRelPath)
	if g1Hash != m.Binds.G1RouterProtocolHash {
		t.Errorf("g1 router protocol hash reproduced = %q, g2 binds = %q", g1Hash, m.Binds.G1RouterProtocolHash)
	}
	if g1Hash != G1RouterProtocolHash {
		t.Errorf("g1 router protocol hash reproduced = %q, want frozen %q", g1Hash, G1RouterProtocolHash)
	}
}

// TestG1ResultReferencedImmutable 断言 g2 只引用不修改 g1 零候选结论:
// manifest.closes_g1 与 protocol.go 常量一致,且被引用的 router_stage_summary.json 仍是 0 认证叶 / NO_GO。
func TestG1ResultReferencedImmutable(t *testing.T) {
	m := loadG2Manifest(t)
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

// reproduceProtocolHash 复现协议哈希规范:读取 manifest -> 解析为通用 JSON -> 把
// audit.protocol_hash 置 null -> datatruth.HashJSON。
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

// TestProtocolHashMatchesManifest 校验 g2 audit.protocol_hash 已按规范填入且可复现。
func TestProtocolHashMatchesManifest(t *testing.T) {
	want := reproduceProtocolHash(t, g2ManifestRelPath)
	m := loadG2Manifest(t)
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
