// Package protocol 是 g2 分层路由预注册协议的"统一预注册与代码"强制点:与
// research/firstbattle/hierarchical_router_g2_preregistration.json 逐字段一致的冻结常量。
// manifest 与代码任一方改动都必须同步改另一方——protocol_test.go 解析 manifest 并断言与本文件
// 三方一致(节点顺序、叶子状态机、reason 码、司天监分桶、发现预算、多重检验、失败标准、红队条数)。
//
// g2 新 generation、新 protocol hash;绑定但绝不修改 g1 hash/result 与历史契约。
// 冻结协议后修改节点、司天监特征、成本或候选定义都必须新 generation(新 protocol hash)。
package protocol

const (
	// ProtocolID / Generation 对应 manifest.protocol_id / manifest.generation。
	ProtocolID         = "firstbattle.hierarchical_router.g2"
	Generation         = "firstbattle-router-g2"
	RouterProtocolHash = "395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec"

	// 绑定但不篡改的被引用契约 hash(见 manifest.binds)。
	HistoryDataHash             = "f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c"
	HistoryRegimeContractHash   = "a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127"
	HistoryDataRequirementsHash = "07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7"

	// 绑定但不篡改的 g1 路由协议 hash 与零候选结论(见 manifest.closes_g1 / binds)。
	G1RouterProtocolHash   = "0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31"
	G1Generation           = "firstbattle-router-g1"
	G1CertifiedMoneyLeaves = 0
	G1ResearchPassLeaves   = 0
	G1SystemStatus         = "NO_GO"
)

// NodeOrder 是冻结的 g2 运行时节点顺序,与 manifest.node_order 逐字一致。
var NodeOrder = []string{
	"DataTruth",
	"SkyBureau",
	"Sorter",
	"MechanismFamily",
	"ConditionalLeaf",
}

// LeafStates 是冻结的叶子状态机取值集合,与 manifest.leaf_states 逐字一致。
var LeafStates = []string{
	"UNRESEARCHED",
	"RESEARCH_PASS",
	"EXAM_PASS",
	"SHADOW_PASS",
	"LIVE_APPROVED",
	"CLOSED",
	"UNTESTABLE",
}

// LeafStateOrder 是可晋级序,与 manifest.leaf_state_machine.order 一致。
var LeafStateOrder = []string{
	"UNRESEARCHED",
	"RESEARCH_PASS",
	"EXAM_PASS",
	"SHADOW_PASS",
	"LIVE_APPROVED",
}

// TerminalClosedStates 是不可复活的终止态,与 manifest.leaf_state_machine.terminal_closed 一致。
var TerminalClosedStates = []string{"CLOSED", "UNTESTABLE"}

// ReasonCodes 是冻结的 coarse reason 码全集,与 manifest.reason_codes 逐字一致。
var ReasonCodes = []string{
	"ROUTED_CANDIDATE",
	"SHADOW_CANDIDATE",
	"DATA_TRUTH_INVALID",
	"DATA_TRUTH_UNKNOWN",
	"SKY_OOD",
	"SKY_UNKNOWN",
	"SORTER_REJECTED",
	"MECHANISM_UNKNOWN",
	"MECHANISM_NO_PAYER",
	"CONDITIONAL_LEAF_AMBIGUOUS",
	"LEAF_DATA_MISSING",
	"LEAF_CLOSED",
	"NOT_CERTIFIED",
	"CONFLICT_NO_ARBITRATION",
	"PORTFOLIO_RISK_BLOCKED",
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

// 司天监分桶(与 manifest.sky)——只用已收盘 bar、按 discovery research 分布拟合、不读收益。
const (
	SkyBinning       = "research_quantiles"
	SkyFitOn         = "discovery_research_partition_only"
	SkyMissingAction = "OOD_NO_TRADE"
)

// SkyTimescalesHours 对应 manifest.sky.timescales_hours。
var SkyTimescalesHours = []int{1, 4, 24}

// SkyQuantiles 对应 manifest.sky.quantiles(两切点 -> 三桶)。
var SkyQuantiles = []float64{0.3333333333, 0.6666666667}

// SkyDimensions 是冻结的司天监维度名,与 manifest.sky.dimensions[].name 逐字一致。
var SkyDimensions = []string{
	"btc_return_24h",
	"btc_trend_efficiency_24h",
	"btc_realized_volatility_24h",
	"btc_drawdown_168h",
	"okx_btc_funding_state",
}

// 发现预算(与 manifest.discovery_budget)。
const (
	OutcomeHorizonDirectionSymmetry = "long_short_symmetric"
	DiscoveryCostBPS                = 12.0
	NeutralBandBPS                  = 3.0

	ForwardMaxFamilies            = 8
	ForwardMaxCandidatesPerFamily = 4

	BackwardMaxFiles                 = 512
	BackwardMaxLines                 = 5000000
	BackwardMaxSearchCombos          = 256
	BackwardSearchSpaceID            = "firstbattle.g2.sky_conditioned_prize.v1"
	BackwardSearchDefinitionHash     = "796bcfc675d3f64ea59b5617058002556ab93c074dd679318bedbc2d5550dd84"
	BackwardRegisteredCombos         = 162
	G2PrizeMechanism                 = "funding_payer_flip_rebalance"
	G2PrizeEconomicFamily            = "funding_settlement"
	G2PrizeFeature                   = "funding_sign_flip"
	G2PrizeThreshold                 = 1.0
	BackwardMaxCandidates            = 32
	BackwardDefaultProgressEvery     = 100000
	BackwardMatchRule                = "same_sky_state_symbol_liquidity_direction"
	BackwardVolatilityTolerance      = 0.0
	BackwardLiquidityTolerance       = 0.0
	BackwardPermutationDraws         = 50000
	BackwardBootstrapSamples         = 1000
	BackwardSeed                     = int64(20260712)
	BackwardExactPermutationMaxPairs = 20
	BackwardMatchWindowMs            = int64(604800000)
	BackwardNeutralBandBPS           = 3.0
	G2SmokeMaxSymbols                = 20
	G2SmokeMaxFundingLines           = 500
	G2SmokeMaxPriceLines             = 25000
	DiscoveryPermille                = 600
	ForwardPermille                  = 200
	WalkForwardPermille              = 200
	PartitionEmbargoHours            = 168
	SorterMinQuoteVolume             = 500000.0
	SorterMinVolatilityCostRatio     = 2.0
	G2MinForwardSamples              = 30
	G2MinForwardClusters             = 10
	G2WalkForwardFolds               = 5
	G2WFMinSamples                   = 30
	G2WFMinClusters                  = 10
	G2PurgeHours                     = 168
	G2WFEmbargoHours                 = 168

	MaxTotalCandidates = 64
)

// OutcomeHorizonsHours 对应 manifest.discovery_budget.outcome_horizons_hours(1h/4h/24h 双向)。
var OutcomeHorizonsHours = []int{1, 4, 24}

// 多重检验预算——与 manifest.multiple_comparison_budget 一致。
const (
	FamilyWiseControl   = "Holm-Bonferroni"
	FamilyWiseAlpha     = 0.01
	BackwardFamilyAlpha = 0.005
	ForwardFamilyAlpha  = 0.005
	MaxSearchCombosTest = 256 // == BackwardMaxSearchCombos;每个搜索组合都是一个检验
)

// RedTeamRequirementCount 对应 manifest.red_team.requirements 条数(g2 路由红队 14 项)。
const RedTeamRequirementCount = 14
