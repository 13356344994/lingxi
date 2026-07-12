package hierarchicalrouter

// 本文件是"统一预注册与代码"的强制点:与
// research/firstbattle/hierarchical_router_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——preregistration_test.go 解析 manifest 并断言
// 与本文件三方一致(节点顺序、叶子状态机、reason 码、发现预算、多重检验、失败标准)。
//
// 冻结协议后修改节点、特征、成本或候选定义必须新 generation(新 protocol hash)。

const (
	// ProtocolID / Generation 对应 manifest.protocol_id / manifest.generation。
	ProtocolID         = "firstbattle.hierarchical_router"
	Generation         = "firstbattle-router-g1"
	RouterProtocolHash = "0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31"

	// 绑定但不篡改的被引用契约 hash(见 manifest.binds)。
	HistoryDataHash               = "f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c"
	HistoryRegimeContractHash     = "a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127"
	HistoryDataRequirementsHash   = "07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7"
	ForcedLiquidationProtocolHash = "6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb"
	ForcedLiquidationGeneration   = "firstbattle-forced-liquidation-g1"
)

// NodeOrder 是冻结的节点顺序,与 manifest.node_order 逐字一致;Route 严格按此顺序推进。
var NodeOrder = []NodeKind{
	NodeDataTruth,
	NodeGlobalContext,
	NodeMechanismFamily,
	NodePhaseSide,
	NodeExecutability,
	NodeCertification,
	NodePortfolioRisk,
}

// LeafStates 是冻结的叶子状态机取值集合,与 manifest.leaf_states 逐字一致。
var LeafStates = []LeafState{
	LeafUnresearched,
	LeafResearchPass,
	LeafExamPass,
	LeafShadowPass,
	LeafLiveApproved,
	LeafClosed,
	LeafUntestable,
}

// LeafStateOrder 是可晋级序(不含终止关闭态),与 manifest.leaf_state_machine.order 一致。
var LeafStateOrder = []LeafState{
	LeafUnresearched,
	LeafResearchPass,
	LeafExamPass,
	LeafShadowPass,
	LeafLiveApproved,
}

// TerminalClosedStates 是不可复活的终止态,与 manifest.leaf_state_machine.terminal_closed 一致。
var TerminalClosedStates = []LeafState{LeafClosed, LeafUntestable}

// ReasonCodes 是冻结的 coarse reason 码全集,与 manifest.reason_codes 逐字一致。
var ReasonCodes = []ReasonCode{
	ReasonRoutedCandidate,
	ReasonShadowCandidate,
	ReasonDataTruthInvalid,
	ReasonDataTruthUnknown,
	ReasonGlobalContextOOD,
	ReasonGlobalContextUnk,
	ReasonMechanismUnknown,
	ReasonMechanismNoPayer,
	ReasonPhaseSideAmbiguous,
	ReasonLeafDataMissing,
	ReasonLeafClosed,
	ReasonExecutability,
	ReasonNotCertified,
	ReasonConflictNoArb,
	ReasonPortfolioBlocked,
}

// CoarseTraceForbiddenSubstrings 是 trace 中绝不允许出现的泄漏子串(与 manifest.coarse_trace_rule)。
var CoarseTraceForbiddenSubstrings = []string{
	"net_edge",
	"pnl",
	"realized_usdt",
	"exam_id",
	"hidden_time_range",
	"threshold_values",
	"near_threshold_margins",
	"per_trade_returns",
}

// 发现预算(正向)——与 manifest.discovery_budget.forward 逐字段一致。
const (
	ForwardMaxFamilies            = 8
	ForwardMaxCandidatesPerFamily = 4
)

// 发现预算(逆向)——与 manifest.discovery_budget.backward 逐字段一致。
const (
	BackwardMaxFiles                    = 512
	BackwardMaxLines                    = 5000000
	BackwardMaxSearchCombos             = 256
	BackwardSearchSpaceID               = "firstbattle.backward.funding_post_settlement.g1"
	BackwardSearchDefinitionHash        = "b86ca14e10d0f6e7c5528182ae4b7b50c333f21a706cf266577aed14822732f4"
	BackwardRegisteredCombos            = 2
	BackwardMaxCandidates               = 32
	BackwardDefaultProgressEvery        = 100000
	BackwardCheckpointRequired          = true
	BackwardSignedEventManifestRequired = true
	BackwardSelectionTaxControl         = "Bonferroni"
	BackwardMatchWindowMs               = int64(604800000)
	BackwardVolatilityTolerance         = 0.0
	BackwardLiquidityTolerance          = 0.0
	BackwardNeutralBandBPS              = 3.0
	BackwardDiscoveryCostBPS            = 12.0
	BackwardPermutationDraws            = 2000
	BackwardBootstrapSamples            = 1000
	BackwardSeed                        = int64(20260711)
	BackwardOutcomeHorizonHours         = 4
	BackwardVolatilityLowBPS            = 50.0
	BackwardVolatilityHighBPS           = 150.0
	BackwardLiquidityBucketRule         = "floor_log10_average_quote_volume_24h_clamp_0_9"
	BackwardSmokeMaxSymbols             = 20
	BackwardSmokeMaxFundingLines        = 500
	BackwardSmokeMaxPriceLines          = 25000
)

// MaxTotalCandidates 对应 manifest.discovery_budget.max_total_candidates。
const MaxTotalCandidates = 64

// 多重检验预算——与 manifest.multiple_comparison_budget 一致。
const (
	FamilyWiseControl   = "Holm-Bonferroni"
	FamilyWiseAlpha     = 0.01
	MaxSearchCombosTest = 256 // == BackwardMaxSearchCombos;每个搜索组合都是一个检验
)

// RedTeamRequirementCount 对应 manifest.red_team.requirements 条数(路由红队 12 项)。
const RedTeamRequirementCount = 12

// ForcedLiquidationLeaves 返回四个冻结的强平候选叶(当前均 UNTESTABLE/NO_GO)。
// 这些叶子与 manifest.leaves 及 history_data_requirements.json / history_testability.json 一致;
// 路由到它们必然 LEAF_CLOSED,发现器不可复活。
func ForcedLiquidationLeaves() []Leaf {
	req := []string{"historical_liquidation", "historical_oi", "historical_depth", "historical_xvenue", "pit_instrument", "okx_5m"}
	relayInval := []string{"remaining_forced_notional_drains", "absorption_ratio_exceeds", "deadline_reached", "out_of_distribution"}
	exhInval := []string{"remaining_forced_notional_rises", "absorption_ratio_collapses", "deadline_reached", "out_of_distribution"}
	return []Leaf{
		{
			LeafID: "panic_liquidation.relay.long_squeezed.short", MechanismFamily: "panic_liquidation",
			Payer: "long_liquidations", Phase: PhaseRelay, ForcedSide: SideShort, Direction: SideShort,
			Generation: ForcedLiquidationGeneration, ProtocolHash: ForcedLiquidationProtocolHash,
			RequiredData: req, Invalidators: relayInval, State: LeafUntestable,
		},
		{
			LeafID: "panic_liquidation.relay.short_squeezed.long", MechanismFamily: "panic_liquidation",
			Payer: "short_liquidations", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong,
			Generation: ForcedLiquidationGeneration, ProtocolHash: ForcedLiquidationProtocolHash,
			RequiredData: req, Invalidators: relayInval, State: LeafUntestable,
		},
		{
			LeafID: "panic_liquidation.exhaustion.long_squeezed.long", MechanismFamily: "panic_liquidation",
			Payer: "long_liquidations", Phase: PhaseExhaustion, ForcedSide: SideShort, Direction: SideLong,
			Generation: ForcedLiquidationGeneration, ProtocolHash: ForcedLiquidationProtocolHash,
			RequiredData: req, Invalidators: exhInval, State: LeafUntestable,
		},
		{
			LeafID: "panic_liquidation.exhaustion.short_squeezed.short", MechanismFamily: "panic_liquidation",
			Payer: "short_liquidations", Phase: PhaseExhaustion, ForcedSide: SideLong, Direction: SideShort,
			Generation: ForcedLiquidationGeneration, ProtocolHash: ForcedLiquidationProtocolHash,
			RequiredData: req, Invalidators: exhInval, State: LeafUntestable,
		},
	}
}

// NewForcedLiquidationRoot 构建仅含四个强平候选叶、无预注册仲裁规则的路由根。
// 这是第一版 research/shadow 路由根:四叶全部 UNTESTABLE,无认证赚钱叶。
func NewForcedLiquidationRoot() (Root, error) {
	return NewRoot(ForcedLiquidationLeaves(), nil)
}
