package mechanismpanel

// 与 research/firstbattle/forced_liquidation_preregistration.json 数值保持逐字一致的统一常量。
// 这是"统一预注册与代码"的强制点:代码不得自行选择漂移的阈值,manifest 与代码任一方改动
// 都必须同步改动另一方——preregistration_test.go 会解析 manifest JSON 并断言与本文件、与
// DefaultAggregatorConfig() 的运行时取值三方相等(manifest ↔ 常量 ↔ 运行时 三向漂移检测)。
//
// 本文件只纠正"结构矛盾"(缺失的 phases 锚点、manifest 与代码引用不一致、未定义的运行时
// 钩子),不自行调整任何经济阈值(手续费、min_net_edge、触发名义、吸收比等一律照抄 manifest)。
const (
	// UnifiedMergeGapMs 对应 manifest episode.clustering.merge_gap_ms。
	UnifiedMergeGapMs int64 = 60000
	// UnifiedMaxEpisodeDurationMs 对应 manifest episode.max_episode_duration_ms。
	UnifiedMaxEpisodeDurationMs int64 = 3600000
	// UnifiedMinTriggerNotionalUSDT 对应 manifest episode.min_trigger_notional_usdt。
	UnifiedMinTriggerNotionalUSDT float64 = 50000
	// UnifiedFeatureLagMs 对应 manifest feature_time_truncation.feature_lag_ms
	// 以及 phases.shared_unified_params.feature_lag_ms(双锚共享的等待生效延迟)。
	UnifiedFeatureLagMs int64 = 250
	// UnifiedExhaustionAbsorptionMin 对应 manifest phases.shared_unified_params.exhaustion_absorption_min。
	UnifiedExhaustionAbsorptionMin float64 = 0.60
	// UnifiedExhaustionMaxRemainingRatio 对应 manifest phases.shared_unified_params.exhaustion_max_remaining_ratio。
	UnifiedExhaustionMaxRemainingRatio float64 = 0.20

	// UnifiedMaxLabelHorizonMs 对应 manifest labels.max_label_horizon_ms(标签 horizon 上界,
	// horizon_ms = min(DeadlineUnixMs - t0, UnifiedMaxLabelHorizonMs))。
	UnifiedMaxLabelHorizonMs int64 = 1800000
	// UnifiedMaxLabelStalenessMs 对应 manifest labels.max_label_staleness_ms。首个越过
	// horizon 的报价若晚于该容忍窗，horizon 必须保持 unobserved，禁止用遥远未来价格代替。
	UnifiedMaxLabelStalenessMs int64 = 10000
	// UnifiedWalkForwardEmbargoMs 对应 manifest splits.walk_forward.embargo_ms(WF 时间 embargo)。
	UnifiedWalkForwardEmbargoMs int64 = 3600000
	// UnifiedWalkForwardFolds 对应 manifest splits.walk_forward.folds。
	UnifiedWalkForwardFolds int = 5
	// UnifiedMinEpisodesPerFold 对应 manifest splits.walk_forward.min_episodes_per_fold。
	UnifiedMinEpisodesPerFold int = 20
	// UnifiedMinEpisodesPerExam 对应 manifest hypotheses[].prereg_thresholds.min_episodes_per_exam
	// (稀疏兜底:不足此数判 inconclusive 而非通过)。
	UnifiedMinEpisodesPerExam int = 30
	// UnifiedFamilyWiseAlpha 对应 manifest multiple_comparison_budget.family_wise_alpha。
	UnifiedFamilyWiseAlpha float64 = 0.01

	// UnifiedMaxOpenEpisodes/UnifiedMaxPendingLabels/UnifiedMaxContextBuffer 是 bounded-memory
	// 运行时上限,对应 manifest runtime.* 且与 DefaultAggregatorConfig() 逐字段一致。
	UnifiedMaxOpenEpisodes  int = 64
	UnifiedMaxPendingLabels int = 128
	UnifiedMaxContextBuffer int = 4096
	UnifiedMaxDedupeEntries int = 4096

	// UnifiedRedTeamRequirementCount 对应 manifest red_team.requirements 的条数(红队 12 项要求)。
	UnifiedRedTeamRequirementCount int = 12
)

// UnifiedLabelHorizonsMs 是研究侧多前瞻标签窗口(毫秒),与 DefaultAggregatorConfig().LabelHorizons、
// manifest runtime.label_horizons_ms 及 labels.label_horizons_ms 逐一一致(漂移检测三方比对)。
// 它们都 <= UnifiedMaxLabelHorizonMs(staleness 上限)。
var UnifiedLabelHorizonsMs = []int64{30000, 60000, 120000, 300000}
