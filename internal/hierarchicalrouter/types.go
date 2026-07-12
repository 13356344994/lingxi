// Package hierarchicalrouter 实现首战分层路由的 fail-closed 决策核心。
//
// 单一目标:不同行情/特殊情况下从根节点逐层匹配到对应的、已认证的赚钱方向;未知、歧义、
// 反向冲突、未认证一律 NO_TRADE;缺数据只关闭相关叶,不停整棵树。本包只驱动 research/shadow
// 路由,不接生产下单路径(不 import app/下单模块)。
//
// 节点顺序、叶子状态机、reason 码、发现预算与失败标准均以
// research/firstbattle/hierarchical_router_preregistration.json 为唯一真相,并由
// preregistration.go 常量与 preregistration_test.go 三方漂移检测冻结。
package hierarchicalrouter

// NodeKind 是路由漏斗的节点标识;顺序见 NodeOrder(与 manifest.node_order 逐字一致)。
type NodeKind string

const (
	NodeDataTruth       NodeKind = "DataTruth"
	NodeGlobalContext   NodeKind = "GlobalContext"
	NodeMechanismFamily NodeKind = "MechanismFamily"
	NodePhaseSide       NodeKind = "PhaseSide"
	NodeExecutability   NodeKind = "Executability"
	NodeCertification   NodeKind = "Certification"
	NodePortfolioRisk   NodeKind = "PortfolioRisk"
)

// LeafState 是候选叶子的认证状态机取值;顺序/终止见 preregistration.go。
type LeafState string

const (
	LeafUnresearched LeafState = "UNRESEARCHED"
	LeafResearchPass LeafState = "RESEARCH_PASS"
	LeafExamPass     LeafState = "EXAM_PASS"
	LeafShadowPass   LeafState = "SHADOW_PASS"
	LeafLiveApproved LeafState = "LIVE_APPROVED"
	LeafClosed       LeafState = "CLOSED"
	LeafUntestable   LeafState = "UNTESTABLE"
)

// PhaseKind 与强平 g1 协议 phases 命名逐字一致(relay/exhaustion)。
type PhaseKind string

const (
	PhaseRelay      PhaseKind = "relay"
	PhaseExhaustion PhaseKind = "exhaustion"
)

// Side 是交易/被迫流方向。
type Side string

const (
	SideLong    Side = "long"
	SideShort   Side = "short"
	SideUnknown Side = ""
)

// Opposite 返回相反方向;未知保持未知。
func (s Side) Opposite() Side {
	switch s {
	case SideLong:
		return SideShort
	case SideShort:
		return SideLong
	default:
		return SideUnknown
	}
}

// Outcome 是一次路由的粗粒度结果类别。
type Outcome string

const (
	OutcomeCandidate Outcome = "CANDIDATE" // live 候选(叶已 LIVE_APPROVED 且通过所有闸)
	OutcomeShadow    Outcome = "SHADOW"    // shadow-only 候选(叶 SHADOW_PASS)
	OutcomeNoTrade   Outcome = "NO_TRADE"  // fail-closed
)

// ReasonCode 是稳定的 coarse 原因码;绝不含收益/隐藏范围/阈值附近数值(见 coarse_trace_rule)。
type ReasonCode string

const (
	ReasonRoutedCandidate    ReasonCode = "ROUTED_CANDIDATE"
	ReasonShadowCandidate    ReasonCode = "SHADOW_CANDIDATE"
	ReasonDataTruthInvalid   ReasonCode = "DATA_TRUTH_INVALID"
	ReasonDataTruthUnknown   ReasonCode = "DATA_TRUTH_UNKNOWN"
	ReasonGlobalContextOOD   ReasonCode = "GLOBAL_CONTEXT_OOD"
	ReasonGlobalContextUnk   ReasonCode = "GLOBAL_CONTEXT_UNKNOWN"
	ReasonMechanismUnknown   ReasonCode = "MECHANISM_UNKNOWN"
	ReasonMechanismNoPayer   ReasonCode = "MECHANISM_NO_PAYER"
	ReasonPhaseSideAmbiguous ReasonCode = "PHASE_SIDE_AMBIGUOUS"
	ReasonLeafDataMissing    ReasonCode = "LEAF_DATA_MISSING"
	ReasonLeafClosed         ReasonCode = "LEAF_CLOSED"
	ReasonExecutability      ReasonCode = "EXECUTABILITY_INVALID"
	ReasonNotCertified       ReasonCode = "NOT_CERTIFIED"
	ReasonConflictNoArb      ReasonCode = "CONFLICT_NO_ARBITRATION"
	ReasonPortfolioBlocked   ReasonCode = "PORTFOLIO_RISK_BLOCKED"
)

// DataTruthStatus 是 DataTruth 节点观测到的三时钟/PIT/完整性/去重状态。
type DataTruthStatus string

const (
	DataTruthValid      DataTruthStatus = "valid"
	DataTruthInvalid    DataTruthStatus = "invalid"
	DataTruthMissing    DataTruthStatus = "missing"
	DataTruthLate       DataTruthStatus = "late"
	DataTruthCorrection DataTruthStatus = "correction"
	DataTruthDuplicate  DataTruthStatus = "duplicate"
	DataTruthOutOfOrder DataTruthStatus = "out_of_order"
	DataTruthUnknown    DataTruthStatus = "" // 缺省=未知 -> NO_TRADE
)

// GlobalContextStatus 是 GlobalContext 节点的行情契约分类结果。
type GlobalContextStatus string

const (
	GlobalSupported   GlobalContextStatus = "supported"
	GlobalUnsupported GlobalContextStatus = "unsupported" // OOD
	GlobalUnknown     GlobalContextStatus = ""            // 未分类 -> NO_TRADE
)

// ExecutabilityStatus 是 Executability 节点的成本/深度/延迟状态。
type ExecutabilityStatus string

const (
	ExecValid   ExecutabilityStatus = "valid"
	ExecInvalid ExecutabilityStatus = "invalid"
	ExecUnknown ExecutabilityStatus = "" // 未知 -> NO_TRADE
)

// Leaf 是一个冻结的候选叶子;身份与规则来自 manifest.leaves,不在运行时篡改。
type Leaf struct {
	LeafID          string
	MechanismFamily string
	Payer           string
	Variant         string // 非 phase 机制的冻结叶选择键；与 Phase 二选一
	Phase           PhaseKind
	ForcedSide      Side
	Direction       Side // 由 phase 规则冻结:relay=ForcedSide, exhaustion=ForcedSide.Opposite
	Generation      string
	ProtocolHash    string
	RequiredData    []string
	Invalidators    []string
	State           LeafState
}

// MissingData 返回该叶所需但在 available 中缺失的数据族(叶级,缺失只关此叶)。
func (l Leaf) MissingData(available map[string]bool) []string {
	var missing []string
	for _, family := range l.RequiredData {
		if !available[family] {
			missing = append(missing, family)
		}
	}
	return missing
}

// MechanismTree 是单个机制家族的路由子树,按 (phase, forced_side) 索引叶子。
type MechanismTree struct {
	Family string
	leaves map[string]Leaf
}

func leafKey(phase PhaseKind, side Side) string {
	return string(phase) + "|" + string(side)
}

func variantKey(variant string) string { return "variant|" + variant }

// Leaf 按 phase 与 forced side 查找叶子。
func (t MechanismTree) Leaf(phase PhaseKind, side Side) (Leaf, bool) {
	l, ok := t.leaves[leafKey(phase, side)]
	return l, ok
}

func (t MechanismTree) LeafVariant(variant string) (Leaf, bool) {
	l, ok := t.leaves[variantKey(variant)]
	return l, ok
}

// Leaves 返回该树全部叶子(顺序不保证)。
func (t MechanismTree) Leaves() []Leaf {
	out := make([]Leaf, 0, len(t.leaves))
	for _, l := range t.leaves {
		out = append(out, l)
	}
	return out
}

// Root 是分层路由根;持有各机制家族树与预注册仲裁规则集合。
type Root struct {
	families map[string]MechanismTree
	// arbitration 是"允许在反向冲突时不空仓"的预注册仲裁规则键集合(instrument 或 family-pair)。
	// 默认空:任何反向冲突且无规则 -> 空仓。
	arbitration map[string]bool
}

// Family 按家族 id 返回树。
func (r Root) Family(id string) (MechanismTree, bool) {
	t, ok := r.families[id]
	return t, ok
}

// Families 返回全部机制家族树(顺序不保证)。
func (r Root) Families() []MechanismTree {
	out := make([]MechanismTree, 0, len(r.families))
	for _, t := range r.families {
		out = append(out, t)
	}
	return out
}

// Observation 是单个机制家族在决策时刻可见字段的路由输入。
// 只含决策时刻正向可计算的信号;不含任何收益/未来数据。
type Observation struct {
	Instrument       string
	DecisionTimeMs   int64
	DataTruth        DataTruthStatus
	GlobalContext    GlobalContextStatus
	MechanismFamily  string
	PayerObservable  bool
	Variant          string
	Phase            PhaseKind
	PhaseAmbiguous   bool
	ForcedSide       Side
	AvailableData    map[string]bool
	Executability    ExecutabilityStatus
	PortfolioBlocked bool
}

// DecisionTrace 是一次路由的完整但不泄漏考卷的审计输出。
// 字段刻意只含 coarse 信息(见 manifest.coarse_trace_rule)。
type DecisionTrace struct {
	Instrument      string
	MechanismFamily string
	Path            []NodeKind
	TerminalNode    NodeKind
	Outcome         Outcome
	Reason          ReasonCode
	LeafID          string
	Direction       Side
}

// PortfolioDecision 是多机制家族并行路由后账户层的汇总结果。
type PortfolioDecision struct {
	Traces    []DecisionTrace // 每个 observation 一条(输入顺序)
	Approved  []DecisionTrace // 通过冲突解析、可下发的候选(live/shadow)
	Conflicts []ConflictNote  // 反向冲突且无仲裁 -> 空仓
}

// ConflictNote 记录一处 instrument 上的反向冲突(coarse)。
type ConflictNote struct {
	Instrument string
	Reason     ReasonCode
	Families   []string
}
