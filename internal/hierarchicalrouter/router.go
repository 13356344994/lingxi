package hierarchicalrouter

import (
	"fmt"
	"sort"
	"strings"
)

// NewRoot 用一组冻结叶子构建路由根。叶子按 mechanism_family 分树,按 (phase, forced_side) 索引。
// direction 必须与 phase 规则一致(relay=ForcedSide, exhaustion=ForcedSide.Opposite),否则报错;
// 这防止运行时静默改写方向。arbitration 是可选的预注册仲裁规则键集合。
func NewRoot(leaves []Leaf, arbitration []string) (Root, error) {
	root := Root{
		families:    map[string]MechanismTree{},
		arbitration: map[string]bool{},
	}
	seen := map[string]bool{}
	for _, leaf := range leaves {
		if leaf.LeafID == "" || leaf.MechanismFamily == "" {
			return Root{}, fmt.Errorf("hierarchicalrouter: leaf missing id or family: %+v", leaf)
		}
		if seen[leaf.LeafID] {
			return Root{}, fmt.Errorf("hierarchicalrouter: duplicate leaf_id %q", leaf.LeafID)
		}
		seen[leaf.LeafID] = true
		key := ""
		if leaf.Phase != "" {
			if strings.TrimSpace(leaf.Variant) != "" {
				return Root{}, fmt.Errorf("hierarchicalrouter: leaf %q cannot define both phase and variant", leaf.LeafID)
			}
			if want := expectedDirection(leaf.Phase, leaf.ForcedSide); want != leaf.Direction {
				return Root{}, fmt.Errorf("hierarchicalrouter: leaf %q direction=%q violates phase rule (want %q)", leaf.LeafID, leaf.Direction, want)
			}
			key = leafKey(leaf.Phase, leaf.ForcedSide)
		} else {
			if strings.TrimSpace(leaf.Variant) == "" || leaf.Direction == SideUnknown {
				return Root{}, fmt.Errorf("hierarchicalrouter: generic leaf %q requires variant and direction", leaf.LeafID)
			}
			key = variantKey(strings.TrimSpace(leaf.Variant))
		}
		tree, ok := root.families[leaf.MechanismFamily]
		if !ok {
			tree = MechanismTree{Family: leaf.MechanismFamily, leaves: map[string]Leaf{}}
			root.families[leaf.MechanismFamily] = tree
		}
		if _, dup := tree.leaves[key]; dup {
			return Root{}, fmt.Errorf("hierarchicalrouter: family %q has duplicate selector=%s", leaf.MechanismFamily, key)
		}
		tree.leaves[key] = leaf
	}
	for _, key := range arbitration {
		if key != "" {
			root.arbitration[key] = true
		}
	}
	return root, nil
}

// expectedDirection 冻结方向规则:relay 顺被迫流,exhaustion 逆被迫流。
func expectedDirection(phase PhaseKind, forced Side) Side {
	switch phase {
	case PhaseRelay:
		return forced
	case PhaseExhaustion:
		return forced.Opposite()
	default:
		return SideUnknown
	}
}

// Route 对单个机制家族的 observation 执行 fail-closed 分层路由,返回不泄漏考卷的 trace。
// 节点严格按 NodeOrder 逐层通过;任何一层未知/歧义/冲突/未认证/缺数据均按规则短路。
func (r Root) Route(obs Observation) DecisionTrace {
	tr := DecisionTrace{Instrument: obs.Instrument, MechanismFamily: obs.MechanismFamily}

	// 1. DataTruth(全局路由上下文的三时钟/PIT/完整性/去重)
	tr.Path = append(tr.Path, NodeDataTruth)
	switch obs.DataTruth {
	case DataTruthValid:
		// 通过
	case DataTruthInvalid, DataTruthMissing, DataTruthLate, DataTruthCorrection, DataTruthDuplicate, DataTruthOutOfOrder:
		return tr.noTrade(NodeDataTruth, ReasonDataTruthInvalid)
	default:
		return tr.noTrade(NodeDataTruth, ReasonDataTruthUnknown)
	}

	// 2. GlobalContext(历史行情契约分类 / OOD)
	tr.Path = append(tr.Path, NodeGlobalContext)
	switch obs.GlobalContext {
	case GlobalSupported:
		// 通过
	case GlobalUnsupported:
		return tr.noTrade(NodeGlobalContext, ReasonGlobalContextOOD)
	default:
		return tr.noTrade(NodeGlobalContext, ReasonGlobalContextUnk)
	}

	// 3. MechanismFamily(机制家族 + 可观测 payer)
	tr.Path = append(tr.Path, NodeMechanismFamily)
	tree, ok := r.Family(obs.MechanismFamily)
	if !ok || obs.MechanismFamily == "" {
		return tr.noTrade(NodeMechanismFamily, ReasonMechanismUnknown)
	}
	if !obs.PayerObservable {
		return tr.noTrade(NodeMechanismFamily, ReasonMechanismNoPayer)
	}

	// 4. PhaseSide(phase + forced side 选叶;叶级数据缺失/关闭只关此叶)
	tr.Path = append(tr.Path, NodePhaseSide)
	var leaf Leaf
	if strings.TrimSpace(obs.Variant) != "" {
		leaf, ok = tree.LeafVariant(strings.TrimSpace(obs.Variant))
	} else {
		if obs.PhaseAmbiguous || obs.Phase == "" || obs.ForcedSide == SideUnknown {
			return tr.noTrade(NodePhaseSide, ReasonPhaseSideAmbiguous)
		}
		leaf, ok = tree.Leaf(obs.Phase, obs.ForcedSide)
	}
	if !ok {
		return tr.noTrade(NodePhaseSide, ReasonPhaseSideAmbiguous)
	}
	tr.LeafID = leaf.LeafID
	// 关闭态叶子不可复活(CLOSED/UNTESTABLE)
	if leaf.State == LeafClosed || leaf.State == LeafUntestable {
		return tr.noTrade(NodePhaseSide, ReasonLeafClosed)
	}
	// 叶级数据缺失只关此叶,不停整棵树
	if missing := leaf.MissingData(obs.AvailableData); len(missing) > 0 {
		return tr.noTrade(NodePhaseSide, ReasonLeafDataMissing)
	}

	// 5. Executability(成本/深度/延迟)
	tr.Path = append(tr.Path, NodeExecutability)
	if obs.Executability != ExecValid {
		return tr.noTrade(NodeExecutability, ReasonExecutability)
	}

	// 6. Certification(叶子认证状态机)
	tr.Path = append(tr.Path, NodeCertification)
	switch leaf.State {
	case LeafLiveApproved:
		// 进入 PortfolioRisk
	case LeafShadowPass:
		// shadow-only 候选:仍走 PortfolioRisk 检查,但结果为 SHADOW
	default:
		// UNRESEARCHED/RESEARCH_PASS/EXAM_PASS 未达发候选门
		return tr.noTrade(NodeCertification, ReasonNotCertified)
	}

	// 7. PortfolioRisk(相关簇/风险闸;反向冲突在 RouteAll 的账户层解析)
	tr.Path = append(tr.Path, NodePortfolioRisk)
	if obs.PortfolioBlocked {
		return tr.noTrade(NodePortfolioRisk, ReasonPortfolioBlocked)
	}

	tr.TerminalNode = NodePortfolioRisk
	tr.Direction = leaf.Direction
	if leaf.State == LeafShadowPass {
		tr.Outcome = OutcomeShadow
		tr.Reason = ReasonShadowCandidate
	} else {
		tr.Outcome = OutcomeCandidate
		tr.Reason = ReasonRoutedCandidate
	}
	return tr
}

// noTrade 把 trace 定型为 fail-closed 结果。
func (tr DecisionTrace) noTrade(node NodeKind, reason ReasonCode) DecisionTrace {
	tr.TerminalNode = node
	tr.Outcome = OutcomeNoTrade
	tr.Reason = reason
	tr.Direction = SideUnknown
	// LeafID 若已在 PhaseSide 识别则保留,方便 coarse 审计。
	return tr
}

// RouteAll 并行路由多个机制家族 observation,再在账户层解析反向冲突。
// 同向多树候选保留(账户层处理相关性与资金竞争);反向冲突且无预注册仲裁规则 -> 该 instrument 空仓。
func (r Root) RouteAll(obss []Observation) PortfolioDecision {
	dec := PortfolioDecision{}
	for _, obs := range obss {
		dec.Traces = append(dec.Traces, r.Route(obs))
	}

	// 按 instrument 收集通过路由的候选(live/shadow)
	byInstrument := map[string][]int{}
	for i, tr := range dec.Traces {
		if tr.Outcome == OutcomeCandidate || tr.Outcome == OutcomeShadow {
			byInstrument[tr.Instrument] = append(byInstrument[tr.Instrument], i)
		}
	}

	blocked := map[int]bool{}
	instruments := make([]string, 0, len(byInstrument))
	for inst := range byInstrument {
		instruments = append(instruments, inst)
	}
	sort.Strings(instruments)
	for _, inst := range instruments {
		idxs := byInstrument[inst]
		dirs := map[Side]bool{}
		for _, i := range idxs {
			dirs[dec.Traces[i].Direction] = true
		}
		// 反向冲突:同一 instrument 同时出现 long 与 short 候选
		if dirs[SideLong] && dirs[SideShort] && !r.arbitration[inst] {
			families := make([]string, 0, len(idxs))
			for _, i := range idxs {
				blocked[i] = true
				families = append(families, dec.Traces[i].MechanismFamily)
			}
			sort.Strings(families)
			dec.Conflicts = append(dec.Conflicts, ConflictNote{
				Instrument: inst,
				Reason:     ReasonConflictNoArb,
				Families:   families,
			})
		}
	}

	for i, tr := range dec.Traces {
		if blocked[i] {
			// 账户层强制空仓:改写为 NO_TRADE(conflict)
			forced := tr
			forced.Outcome = OutcomeNoTrade
			forced.Reason = ReasonConflictNoArb
			forced.TerminalNode = NodePortfolioRisk
			forced.Direction = SideUnknown
			dec.Traces[i] = forced
			continue
		}
		if tr.Outcome == OutcomeCandidate || tr.Outcome == OutcomeShadow {
			dec.Approved = append(dec.Approved, tr)
		}
	}
	return dec
}
