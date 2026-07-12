package mechanismpanel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/datatruth"
)

// manifestRelPath 指向研究侧预注册 manifest。测试只读取本 manifest（研究区可反复读取的公开
// 协议文件），绝不读取任何真实 records/收益/考卷或运行生产。
const manifestRelPath = "../../research/firstbattle/forced_liquidation_preregistration.json"

// prereg 只声明漂移检测需要断言的字段;其余 manifest 字段(公开协议描述)不在此结构里,不影响。
type prereg struct {
	Episode struct {
		Clustering struct {
			MergeGapMs int64 `json:"merge_gap_ms"`
		} `json:"clustering"`
		MaxEpisodeDurationMs   int64   `json:"max_episode_duration_ms"`
		MinTriggerNotionalUSDT float64 `json:"min_trigger_notional_usdt"`
	} `json:"episode"`

	FeatureTimeTruncation struct {
		FeatureLagMs int64 `json:"feature_lag_ms"`
	} `json:"feature_time_truncation"`

	Phases struct {
		SharedUnifiedParams struct {
			FeatureLagMs                int64   `json:"feature_lag_ms"`
			ExhaustionAbsorptionMin     float64 `json:"exhaustion_absorption_min"`
			ExhaustionMaxRemainingRatio float64 `json:"exhaustion_max_remaining_ratio"`
			SymmetricBudget             bool    `json:"symmetric_budget"`
			NoPresupposedWinner         bool    `json:"no_presupposed_winner"`
		} `json:"shared_unified_params"`
		Relay struct {
			Phase string `json:"phase"`
		} `json:"relay"`
		Exhaustion struct {
			Phase string `json:"phase"`
		} `json:"exhaustion"`
	} `json:"phases"`

	Labels struct {
		MaxLabelHorizonMs   int64   `json:"max_label_horizon_ms"`
		MaxLabelStalenessMs int64   `json:"max_label_staleness_ms"`
		LabelHorizonsMs     []int64 `json:"label_horizons_ms"`
	} `json:"labels"`

	Runtime struct {
		MaxOpenEpisodes        int     `json:"max_open_episodes"`
		MaxPendingLabels       int     `json:"max_pending_labels"`
		MaxContextBuffer       int     `json:"max_context_buffer"`
		MaxDedupeEntries       int     `json:"max_dedupe_entries"`
		LabelHorizonsMs        []int64 `json:"label_horizons_ms"`
		MergeGapMs             int64   `json:"merge_gap_ms"`
		MaxEpisodeDurationMs   int64   `json:"max_episode_duration_ms"`
		MinTriggerNotionalUSDT float64 `json:"min_trigger_notional_usdt"`
		FeatureLagMs           int64   `json:"feature_lag_ms"`
		MaxLabelStalenessMs    int64   `json:"max_label_staleness_ms"`
	} `json:"runtime"`

	Splits struct {
		WalkForward struct {
			EmbargoMs          int64 `json:"embargo_ms"`
			Folds              int   `json:"folds"`
			MinEpisodesPerFold int   `json:"min_episodes_per_fold"`
		} `json:"walk_forward"`
	} `json:"splits"`

	Hypotheses []struct {
		ID               string `json:"id"`
		PreregThresholds struct {
			MinEpisodesPerExam int     `json:"min_episodes_per_exam"`
			MaxFamilyWiseAlpha float64 `json:"max_family_wise_alpha"`
		} `json:"prereg_thresholds"`
	} `json:"hypotheses"`

	MultipleComparisonBudget struct {
		FamilyWiseAlpha float64 `json:"family_wise_alpha"`
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

func loadManifest(t *testing.T) prereg {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(manifestRelPath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m prereg
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

// TestManifestCodeConstantsNoDrift 是"统一预注册与代码"的强制点:manifest 数值、preregistration.go
// 的 Unified* 常量、以及 DefaultAggregatorConfig() 的运行时取值必须三方逐字段一致。任一方漂移
// (包括双锚 phases 的 feature_lag / exhaustion 阈值、label horizons/staleness、walk-forward embargo、
// bounded-memory 运行时上限)都会在此失败,迫使 manifest 与代码同步修改。
func TestManifestCodeConstantsNoDrift(t *testing.T) {
	m := loadManifest(t)

	// ---- manifest ↔ preregistration.go 常量 ----
	checks := []struct {
		name     string
		manifest any
		constant any
	}{
		{"episode.clustering.merge_gap_ms", m.Episode.Clustering.MergeGapMs, UnifiedMergeGapMs},
		{"episode.max_episode_duration_ms", m.Episode.MaxEpisodeDurationMs, UnifiedMaxEpisodeDurationMs},
		{"episode.min_trigger_notional_usdt", m.Episode.MinTriggerNotionalUSDT, UnifiedMinTriggerNotionalUSDT},
		{"feature_time_truncation.feature_lag_ms", m.FeatureTimeTruncation.FeatureLagMs, UnifiedFeatureLagMs},
		{"phases.shared_unified_params.feature_lag_ms", m.Phases.SharedUnifiedParams.FeatureLagMs, UnifiedFeatureLagMs},
		{"phases.shared_unified_params.exhaustion_absorption_min", m.Phases.SharedUnifiedParams.ExhaustionAbsorptionMin, UnifiedExhaustionAbsorptionMin},
		{"phases.shared_unified_params.exhaustion_max_remaining_ratio", m.Phases.SharedUnifiedParams.ExhaustionMaxRemainingRatio, UnifiedExhaustionMaxRemainingRatio},
		{"labels.max_label_horizon_ms", m.Labels.MaxLabelHorizonMs, UnifiedMaxLabelHorizonMs},
		{"labels.max_label_staleness_ms", m.Labels.MaxLabelStalenessMs, UnifiedMaxLabelStalenessMs},
		{"splits.walk_forward.embargo_ms", m.Splits.WalkForward.EmbargoMs, UnifiedWalkForwardEmbargoMs},
		{"splits.walk_forward.folds", m.Splits.WalkForward.Folds, UnifiedWalkForwardFolds},
		{"splits.walk_forward.min_episodes_per_fold", m.Splits.WalkForward.MinEpisodesPerFold, UnifiedMinEpisodesPerFold},
		{"multiple_comparison_budget.family_wise_alpha", m.MultipleComparisonBudget.FamilyWiseAlpha, UnifiedFamilyWiseAlpha},
		{"runtime.max_open_episodes", m.Runtime.MaxOpenEpisodes, UnifiedMaxOpenEpisodes},
		{"runtime.max_pending_labels", m.Runtime.MaxPendingLabels, UnifiedMaxPendingLabels},
		{"runtime.max_context_buffer", m.Runtime.MaxContextBuffer, UnifiedMaxContextBuffer},
		{"runtime.max_dedupe_entries", m.Runtime.MaxDedupeEntries, UnifiedMaxDedupeEntries},
		{"runtime.merge_gap_ms", m.Runtime.MergeGapMs, UnifiedMergeGapMs},
		{"runtime.max_episode_duration_ms", m.Runtime.MaxEpisodeDurationMs, UnifiedMaxEpisodeDurationMs},
		{"runtime.min_trigger_notional_usdt", m.Runtime.MinTriggerNotionalUSDT, UnifiedMinTriggerNotionalUSDT},
		{"runtime.feature_lag_ms", m.Runtime.FeatureLagMs, UnifiedFeatureLagMs},
		{"runtime.max_label_staleness_ms", m.Runtime.MaxLabelStalenessMs, UnifiedMaxLabelStalenessMs},
	}
	for _, c := range checks {
		if !valuesEqual(c.manifest, c.constant) {
			t.Errorf("drift @ %s: manifest=%v constant=%v", c.name, c.manifest, c.constant)
		}
	}

	// ---- 双锚 phase 名称 ↔ 代码 PhaseKind ----
	if m.Phases.Relay.Phase != string(PhaseRelay) {
		t.Errorf("phases.relay.phase = %q, want %q", m.Phases.Relay.Phase, PhaseRelay)
	}
	if m.Phases.Exhaustion.Phase != string(PhaseExhaustion) {
		t.Errorf("phases.exhaustion.phase = %q, want %q", m.Phases.Exhaustion.Phase, PhaseExhaustion)
	}
	if !m.Phases.SharedUnifiedParams.SymmetricBudget || !m.Phases.SharedUnifiedParams.NoPresupposedWinner {
		t.Errorf("phases.shared_unified_params must assert symmetric_budget && no_presupposed_winner")
	}

	// ---- label horizons/staleness:manifest labels ↔ runtime ↔ 常量 ↔ 运行时 ----
	assertInt64Slice(t, "labels.label_horizons_ms", m.Labels.LabelHorizonsMs, UnifiedLabelHorizonsMs)
	assertInt64Slice(t, "runtime.label_horizons_ms", m.Runtime.LabelHorizonsMs, UnifiedLabelHorizonsMs)
	for _, h := range m.Labels.LabelHorizonsMs {
		if h > m.Labels.MaxLabelHorizonMs {
			t.Errorf("label horizon %d exceeds max_label_horizon_ms %d (staleness bound violated)", h, m.Labels.MaxLabelHorizonMs)
		}
	}

	// ---- 每假设 min_episodes_per_exam / alpha ↔ 常量 ----
	if len(m.Hypotheses) == 0 {
		t.Fatalf("manifest has no hypotheses")
	}
	for _, h := range m.Hypotheses {
		if h.PreregThresholds.MinEpisodesPerExam != UnifiedMinEpisodesPerExam {
			t.Errorf("hypothesis %s min_episodes_per_exam = %d, want %d", h.ID, h.PreregThresholds.MinEpisodesPerExam, UnifiedMinEpisodesPerExam)
		}
		if h.PreregThresholds.MaxFamilyWiseAlpha != UnifiedFamilyWiseAlpha {
			t.Errorf("hypothesis %s max_family_wise_alpha = %v, want %v", h.ID, h.PreregThresholds.MaxFamilyWiseAlpha, UnifiedFamilyWiseAlpha)
		}
	}

	// ---- 红队 12 项 ----
	if len(m.RedTeam.Requirements) != UnifiedRedTeamRequirementCount {
		t.Errorf("red_team.requirements has %d items, want %d", len(m.RedTeam.Requirements), UnifiedRedTeamRequirementCount)
	}
	seen := map[string]bool{}
	for i, r := range m.RedTeam.Requirements {
		if r.ID == "" || r.Requirement == "" || r.EnforcedBy == "" {
			t.Errorf("red_team.requirements[%d] must have non-empty id/requirement/enforced_by: %+v", i, r)
		}
		if seen[r.ID] {
			t.Errorf("red_team.requirements has duplicate id %q", r.ID)
		}
		seen[r.ID] = true
	}
}

// TestManifestMatchesDefaultAggregatorConfig 断言 manifest 的运行时参数与 DefaultAggregatorConfig()
// 真实构造出的运行时配置逐字段一致(第三方漂移检测:manifest ↔ 运行时)。
func TestManifestMatchesDefaultAggregatorConfig(t *testing.T) {
	m := loadManifest(t)
	cfg := DefaultAggregatorConfig()

	if got := cfg.GapThreshold.Milliseconds(); got != m.Runtime.MergeGapMs {
		t.Errorf("GapThreshold = %dms, manifest runtime.merge_gap_ms = %d", got, m.Runtime.MergeGapMs)
	}
	if got := cfg.MaxEpisodeDuration.Milliseconds(); got != m.Runtime.MaxEpisodeDurationMs {
		t.Errorf("MaxEpisodeDuration = %dms, manifest runtime.max_episode_duration_ms = %d", got, m.Runtime.MaxEpisodeDurationMs)
	}
	if cfg.MinTriggerNotionalUSDT != m.Runtime.MinTriggerNotionalUSDT {
		t.Errorf("MinTriggerNotionalUSDT = %v, manifest = %v", cfg.MinTriggerNotionalUSDT, m.Runtime.MinTriggerNotionalUSDT)
	}
	if cfg.FeatureLagMs != m.Runtime.FeatureLagMs {
		t.Errorf("FeatureLagMs = %v, manifest = %v", cfg.FeatureLagMs, m.Runtime.FeatureLagMs)
	}
	if cfg.ExhaustionAbsorptionMin != m.Phases.SharedUnifiedParams.ExhaustionAbsorptionMin {
		t.Errorf("ExhaustionAbsorptionMin = %v, manifest = %v", cfg.ExhaustionAbsorptionMin, m.Phases.SharedUnifiedParams.ExhaustionAbsorptionMin)
	}
	if cfg.ExhaustionMaxRemainingRatio != m.Phases.SharedUnifiedParams.ExhaustionMaxRemainingRatio {
		t.Errorf("ExhaustionMaxRemainingRatio = %v, manifest = %v", cfg.ExhaustionMaxRemainingRatio, m.Phases.SharedUnifiedParams.ExhaustionMaxRemainingRatio)
	}
	if cfg.MaxOpenEpisodes != m.Runtime.MaxOpenEpisodes {
		t.Errorf("MaxOpenEpisodes = %v, manifest = %v", cfg.MaxOpenEpisodes, m.Runtime.MaxOpenEpisodes)
	}
	if cfg.MaxPendingLabels != m.Runtime.MaxPendingLabels {
		t.Errorf("MaxPendingLabels = %v, manifest = %v", cfg.MaxPendingLabels, m.Runtime.MaxPendingLabels)
	}
	if cfg.MaxContextBuffer != m.Runtime.MaxContextBuffer {
		t.Errorf("MaxContextBuffer = %v, manifest = %v", cfg.MaxContextBuffer, m.Runtime.MaxContextBuffer)
	}
	if got := cfg.MaxLabelStaleness.Milliseconds(); got != m.Runtime.MaxLabelStalenessMs {
		t.Errorf("MaxLabelStaleness = %dms, manifest = %dms", got, m.Runtime.MaxLabelStalenessMs)
	}
	gotHorizons := make([]int64, 0, len(cfg.LabelHorizons))
	for _, h := range cfg.LabelHorizons {
		gotHorizons = append(gotHorizons, h.Milliseconds())
	}
	assertInt64Slice(t, "DefaultAggregatorConfig.LabelHorizons vs manifest runtime.label_horizons_ms", gotHorizons, m.Runtime.LabelHorizonsMs)
}

// computeProtocolHash 复现协议哈希规范:读取 manifest -> 解析为通用 JSON -> 把 audit.protocol_hash
// 置为 null -> datatruth.HashJSON。HashJSON 对对象键序与空白不敏感,故结果可逐字节复现。
func computeProtocolHash(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(manifestRelPath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode manifest generic: %v", err)
	}
	if audit, ok := doc["audit"].(map[string]any); ok {
		audit["protocol_hash"] = nil
	} else {
		t.Fatalf("manifest.audit missing or not an object")
	}
	hash, _, err := datatruth.HashJSON(doc)
	if err != nil {
		t.Fatalf("HashJSON: %v", err)
	}
	return hash
}

// TestProtocolHashMatchesManifest 校验 audit.protocol_hash 已按规范填入且可复现。
// 若失败会打印期望值,方便把哈希写回 manifest(填入时 audit.protocol_hash 置 null 计算)。
func TestProtocolHashMatchesManifest(t *testing.T) {
	want := computeProtocolHash(t)
	m := loadManifest(t)
	if m.Audit.ProtocolHash == nil {
		t.Fatalf("audit.protocol_hash is null; fill it with the reproducible hash: %s", want)
	}
	if *m.Audit.ProtocolHash != want {
		t.Fatalf("audit.protocol_hash = %q, want %q (recompute with audit.protocol_hash=null)", *m.Audit.ProtocolHash, want)
	}
}

func valuesEqual(a, b any) bool {
	switch av := a.(type) {
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	default:
		return false
	}
}

func assertInt64Slice(t *testing.T, name string, got, want []int64) {
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
