package hierarchicalrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/datatruth"
	"lingxi/internal/researchboundary"
)

// 只读取公开的路由协议 manifest 与被绑定的公开契约(regime / requirements / 强平 g1)。
// 绝不读取任何真实 records/收益/考卷或大 history 目录。
const (
	routerManifestRelPath = "../../research/firstbattle/hierarchical_router_preregistration.json"
	regimeManifestRelPath = "../../research/firstbattle/history_regime_contract.json"
	reqManifestRelPath    = "../../research/firstbattle/history_data_requirements.json"
	forcedLiqManifestPath = "../../research/firstbattle/forced_liquidation_preregistration.json"
)

type routerManifest struct {
	ProtocolID string `json:"protocol_id"`
	Generation string `json:"generation"`

	Binds struct {
		HistoryDataHash               string `json:"history_data_hash"`
		HistoryRegimeContractHash     string `json:"history_regime_contract_hash"`
		HistoryDataRequirementsHash   string `json:"history_data_requirements_hash"`
		ForcedLiquidationProtocolHash string `json:"forced_liquidation_protocol_hash"`
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

	Leaves []struct {
		LeafID          string   `json:"leaf_id"`
		MechanismFamily string   `json:"mechanism_family"`
		Payer           string   `json:"payer"`
		Phase           string   `json:"phase"`
		ForcedSide      string   `json:"forced_side"`
		Direction       string   `json:"direction"`
		Generation      string   `json:"generation"`
		ProtocolHash    string   `json:"protocol_hash"`
		RequiredData    []string `json:"required_data"`
		Invalidators    []string `json:"invalidators"`
		State           string   `json:"state"`
	} `json:"leaves"`

	DiscoveryBudget struct {
		Forward struct {
			MaxFamilies            int `json:"max_families"`
			MaxCandidatesPerFamily int `json:"max_candidates_per_family"`
		} `json:"forward"`
		Backward struct {
			MaxFiles                    int     `json:"max_files"`
			MaxLines                    int     `json:"max_lines"`
			MaxSearchCombos             int     `json:"max_search_combinations"`
			AllowedSearchSpaceID        string  `json:"allowed_search_space_id"`
			AllowedSearchDefinitionHash string  `json:"allowed_search_space_definition_hash"`
			RegisteredSearchCombos      int     `json:"registered_search_combinations"`
			MaxCandidates               int     `json:"max_candidates"`
			DefaultProgressEvery        int     `json:"default_progress_every"`
			CheckpointRequired          bool    `json:"checkpoint_required"`
			SignedEventManifestRequired bool    `json:"signed_event_manifest_required"`
			SelectionTaxControl         string  `json:"selection_tax_control"`
			MatchWindowMs               int64   `json:"match_window_ms"`
			VolatilityTolerance         float64 `json:"volatility_tolerance"`
			LiquidityTolerance          float64 `json:"liquidity_tolerance"`
			NeutralBandBPS              float64 `json:"neutral_band_bps"`
			DiscoveryCostBPS            float64 `json:"discovery_cost_bps"`
			PermutationDraws            int     `json:"permutation_draws"`
			BootstrapSamples            int     `json:"bootstrap_samples"`
			Seed                        int64   `json:"seed"`
			FundingPostSettlement       struct {
				OutcomeHorizonHours int       `json:"outcome_horizon_hours"`
				DiscoveryCostBPS    float64   `json:"discovery_cost_bps"`
				VolatilityBucketBPS []float64 `json:"volatility_bucket_bps"`
				LiquidityBucket     string    `json:"liquidity_bucket"`
				BoundedSmoke        struct {
					MaxSymbols      int `json:"max_symbols"`
					MaxFundingLines int `json:"max_funding_lines"`
					MaxPriceLines   int `json:"max_price_lines"`
				} `json:"bounded_smoke"`
			} `json:"funding_post_settlement_event_definition"`
		} `json:"backward"`
		MaxTotalCandidates int `json:"max_total_candidates"`
	} `json:"discovery_budget"`

	MultipleComparisonBudget struct {
		FamilyWiseControl string  `json:"family_wise_control"`
		FamilyWiseAlpha   float64 `json:"family_wise_alpha"`
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

func loadRouterManifest(t *testing.T) routerManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(routerManifestRelPath))
	if err != nil {
		t.Fatalf("read router manifest: %v", err)
	}
	var m routerManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode router manifest: %v", err)
	}
	return m
}

// TestManifestCodeConstantsNoDrift 冻结节点顺序、叶子状态、reason 码、发现预算、多重检验与失败标准:
// manifest ↔ preregistration.go 常量必须逐字段一致,任一漂移即失败。
func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadRouterManifest(t)

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
	if len(m.NodeOrder) != len(NodeOrder) {
		t.Fatalf("node_order length = %d, want %d", len(m.NodeOrder), len(NodeOrder))
	}
	for i := range NodeOrder {
		if m.NodeOrder[i] != string(NodeOrder[i]) {
			t.Errorf("node_order[%d] = %q, want %q", i, m.NodeOrder[i], NodeOrder[i])
		}
	}

	// ---- 叶子状态机 ----
	assertStrSlice(t, "leaf_states", m.LeafStates, leafStatesToStrings(LeafStates))
	assertStrSlice(t, "leaf_state_machine.order", m.LeafStateMachine.Order, leafStatesToStrings(LeafStateOrder))
	assertStrSlice(t, "leaf_state_machine.terminal_closed", m.LeafStateMachine.TerminalClosed, leafStatesToStrings(TerminalClosedStates))

	// ---- reason 码 ----
	assertStrSlice(t, "reason_codes", m.ReasonCodes, reasonCodesToStrings(ReasonCodes))

	// ---- coarse trace 禁泄漏子串 ----
	assertStrSlice(t, "coarse_trace_rule.must_not_contain", m.CoarseTraceRule.MustNotContain, CoarseTraceForbiddenSubstrings)

	// ---- 发现预算 ----
	checkInt(t, "forward.max_families", m.DiscoveryBudget.Forward.MaxFamilies, ForwardMaxFamilies)
	checkInt(t, "forward.max_candidates_per_family", m.DiscoveryBudget.Forward.MaxCandidatesPerFamily, ForwardMaxCandidatesPerFamily)
	checkInt(t, "backward.max_files", m.DiscoveryBudget.Backward.MaxFiles, BackwardMaxFiles)
	checkInt(t, "backward.max_lines", m.DiscoveryBudget.Backward.MaxLines, BackwardMaxLines)
	checkInt(t, "backward.max_search_combinations", m.DiscoveryBudget.Backward.MaxSearchCombos, BackwardMaxSearchCombos)
	if m.DiscoveryBudget.Backward.AllowedSearchSpaceID != BackwardSearchSpaceID ||
		m.DiscoveryBudget.Backward.AllowedSearchDefinitionHash != BackwardSearchDefinitionHash ||
		m.DiscoveryBudget.Backward.RegisteredSearchCombos != BackwardRegisteredCombos {
		t.Errorf("backward search-space family budget drifted")
	}
	checkInt(t, "backward.max_candidates", m.DiscoveryBudget.Backward.MaxCandidates, BackwardMaxCandidates)
	checkInt(t, "backward.default_progress_every", m.DiscoveryBudget.Backward.DefaultProgressEvery, BackwardDefaultProgressEvery)
	if !m.DiscoveryBudget.Backward.CheckpointRequired {
		t.Errorf("backward.checkpoint_required must be true")
	}
	if m.DiscoveryBudget.Backward.SignedEventManifestRequired != BackwardSignedEventManifestRequired {
		t.Errorf("backward signed event manifest requirement drifted")
	}
	if m.DiscoveryBudget.Backward.SelectionTaxControl != BackwardSelectionTaxControl {
		t.Errorf("backward selection tax control drifted")
	}
	if m.DiscoveryBudget.Backward.MatchWindowMs != BackwardMatchWindowMs ||
		m.DiscoveryBudget.Backward.VolatilityTolerance != BackwardVolatilityTolerance ||
		m.DiscoveryBudget.Backward.LiquidityTolerance != BackwardLiquidityTolerance ||
		m.DiscoveryBudget.Backward.NeutralBandBPS != BackwardNeutralBandBPS ||
		m.DiscoveryBudget.Backward.DiscoveryCostBPS != BackwardDiscoveryCostBPS ||
		m.DiscoveryBudget.Backward.PermutationDraws != BackwardPermutationDraws ||
		m.DiscoveryBudget.Backward.BootstrapSamples != BackwardBootstrapSamples ||
		m.DiscoveryBudget.Backward.Seed != BackwardSeed {
		t.Errorf("backward frozen statistical parameters drifted")
	}
	eventDef := m.DiscoveryBudget.Backward.FundingPostSettlement
	if eventDef.OutcomeHorizonHours != BackwardOutcomeHorizonHours ||
		eventDef.DiscoveryCostBPS != BackwardDiscoveryCostBPS ||
		len(eventDef.VolatilityBucketBPS) != 2 ||
		eventDef.VolatilityBucketBPS[0] != BackwardVolatilityLowBPS ||
		eventDef.VolatilityBucketBPS[1] != BackwardVolatilityHighBPS ||
		eventDef.LiquidityBucket != BackwardLiquidityBucketRule {
		t.Errorf("funding post-settlement event definition drifted")
	}
	if eventDef.BoundedSmoke.MaxSymbols != BackwardSmokeMaxSymbols ||
		eventDef.BoundedSmoke.MaxFundingLines != BackwardSmokeMaxFundingLines ||
		eventDef.BoundedSmoke.MaxPriceLines != BackwardSmokeMaxPriceLines {
		t.Errorf("funding post-settlement bounded smoke limits drifted")
	}
	checkInt(t, "discovery_budget.max_total_candidates", m.DiscoveryBudget.MaxTotalCandidates, MaxTotalCandidates)

	// ---- 多重检验 ----
	if m.MultipleComparisonBudget.FamilyWiseControl != FamilyWiseControl {
		t.Errorf("family_wise_control = %q, want %q", m.MultipleComparisonBudget.FamilyWiseControl, FamilyWiseControl)
	}
	if m.MultipleComparisonBudget.FamilyWiseAlpha != FamilyWiseAlpha {
		t.Errorf("family_wise_alpha = %v, want %v", m.MultipleComparisonBudget.FamilyWiseAlpha, FamilyWiseAlpha)
	}
	checkInt(t, "multiple_comparison_budget.max_search_combinations", m.MultipleComparisonBudget.MaxSearchCombos, MaxSearchCombosTest)

	// ---- 红队 12 项 ----
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

// TestManifestLeavesMatchCode 断言 manifest.leaves 与 ForcedLiquidationLeaves() 逐叶一致,
// 且方向遵守 phase 规则、绑定强平 g1 generation/hash、当前均 UNTESTABLE。
func TestManifestLeavesMatchCode(t *testing.T) {
	m := loadRouterManifest(t)
	code := ForcedLiquidationLeaves()
	if len(m.Leaves) != len(code) {
		t.Fatalf("manifest leaves = %d, code leaves = %d", len(m.Leaves), len(code))
	}
	byID := map[string]Leaf{}
	for _, l := range code {
		byID[l.LeafID] = l
	}
	for _, ml := range m.Leaves {
		cl, ok := byID[ml.LeafID]
		if !ok {
			t.Errorf("manifest leaf %q missing in code", ml.LeafID)
			continue
		}
		if ml.MechanismFamily != cl.MechanismFamily || ml.Payer != cl.Payer ||
			ml.Phase != string(cl.Phase) || ml.ForcedSide != string(cl.ForcedSide) ||
			ml.Direction != string(cl.Direction) || ml.Generation != cl.Generation ||
			ml.ProtocolHash != cl.ProtocolHash || ml.State != string(cl.State) {
			t.Errorf("leaf %q drift: manifest=%+v code=%+v", ml.LeafID, ml, cl)
		}
		if ml.Generation != ForcedLiquidationGeneration || ml.ProtocolHash != ForcedLiquidationProtocolHash {
			t.Errorf("leaf %q must bind forced-liq g1 generation/hash", ml.LeafID)
		}
		if ml.State != string(LeafUntestable) {
			t.Errorf("leaf %q state = %q, want UNTESTABLE", ml.LeafID, ml.State)
		}
		// direction 必须遵守 phase 规则
		want := expectedDirection(cl.Phase, cl.ForcedSide)
		if string(want) != ml.Direction {
			t.Errorf("leaf %q direction=%q violates phase rule (want %q)", ml.LeafID, ml.Direction, want)
		}
		assertStrSlice(t, "leaf "+ml.LeafID+" required_data", ml.RequiredData, cl.RequiredData)
		assertStrSlice(t, "leaf "+ml.LeafID+" invalidators", ml.Invalidators, cl.Invalidators)
	}
}

// TestBoundHashesMatchCodeConstants 断言 manifest.binds 与 preregistration.go 常量一致。
func TestBoundHashesMatchCodeConstants(t *testing.T) {
	m := loadRouterManifest(t)
	if m.Binds.HistoryDataHash != HistoryDataHash {
		t.Errorf("binds.history_data_hash = %q, want %q", m.Binds.HistoryDataHash, HistoryDataHash)
	}
	if m.Binds.HistoryRegimeContractHash != HistoryRegimeContractHash {
		t.Errorf("binds.history_regime_contract_hash = %q, want %q", m.Binds.HistoryRegimeContractHash, HistoryRegimeContractHash)
	}
	if m.Binds.HistoryDataRequirementsHash != HistoryDataRequirementsHash {
		t.Errorf("binds.history_data_requirements_hash = %q, want %q", m.Binds.HistoryDataRequirementsHash, HistoryDataRequirementsHash)
	}
	if m.Binds.ForcedLiquidationProtocolHash != ForcedLiquidationProtocolHash {
		t.Errorf("binds.forced_liquidation_protocol_hash = %q, want %q", m.Binds.ForcedLiquidationProtocolHash, ForcedLiquidationProtocolHash)
	}
}

// TestBoundHashesMatchReferencedContracts 从被引用的公开契约实际复现 hash,断言 manifest 绑定正确:
// - regime / requirements 通过 researchboundary 加载并校验各自 manifest_hash;
// - 强平 g1 protocol hash 通过 datatruth.HashJSON(audit.protocol_hash=null) 复现。
// 这防止路由协议绑定到与真实契约不符的 hash。
func TestBoundHashesMatchReferencedContracts(t *testing.T) {
	m := loadRouterManifest(t)

	regime, err := researchboundary.LoadRegimeContract(filepath.Clean(regimeManifestRelPath))
	if err != nil {
		t.Fatalf("load regime contract: %v", err)
	}
	if regime.ManifestHash == nil || *regime.ManifestHash != m.Binds.HistoryRegimeContractHash {
		t.Errorf("regime contract hash = %v, router binds = %q", regime.ManifestHash, m.Binds.HistoryRegimeContractHash)
	}

	req, err := researchboundary.LoadRequirements(filepath.Clean(reqManifestRelPath))
	if err != nil {
		t.Fatalf("load requirements: %v", err)
	}
	if req.ManifestHash == nil || *req.ManifestHash != m.Binds.HistoryDataRequirementsHash {
		t.Errorf("requirements hash = %v, router binds = %q", req.ManifestHash, m.Binds.HistoryDataRequirementsHash)
	}

	forcedHash := reproduceProtocolHash(t, forcedLiqManifestPath)
	if forcedHash != m.Binds.ForcedLiquidationProtocolHash {
		t.Errorf("forced-liq protocol hash reproduced = %q, router binds = %q", forcedHash, m.Binds.ForcedLiquidationProtocolHash)
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

// TestProtocolHashMatchesManifest 校验 audit.protocol_hash 已按规范填入且可复现。
func TestProtocolHashMatchesManifest(t *testing.T) {
	want := reproduceProtocolHash(t, routerManifestRelPath)
	m := loadRouterManifest(t)
	if m.Audit.ProtocolHash == nil {
		t.Fatalf("audit.protocol_hash is null; fill it with reproducible hash: %s", want)
	}
	if *m.Audit.ProtocolHash != want {
		t.Fatalf("audit.protocol_hash = %q, want %q (recompute with audit.protocol_hash=null)", *m.Audit.ProtocolHash, want)
	}
}

func leafStatesToStrings(in []LeafState) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func reasonCodesToStrings(in []ReasonCode) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
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

func checkInt(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}
