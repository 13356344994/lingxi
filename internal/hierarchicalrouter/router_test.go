package hierarchicalrouter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// validObs 返回一个能一路走到 PortfolioRisk 的基线 observation(叶存在且 LIVE_APPROVED 时)。
func validObs(family string, phase PhaseKind, forced Side, avail []string) Observation {
	m := map[string]bool{}
	for _, a := range avail {
		m[a] = true
	}
	return Observation{
		Instrument:      "X-USDT-SWAP",
		DecisionTimeMs:  1_000_000,
		DataTruth:       DataTruthValid,
		GlobalContext:   GlobalSupported,
		MechanismFamily: family,
		PayerObservable: true,
		Phase:           phase,
		ForcedSide:      forced,
		AvailableData:   m,
		Executability:   ExecValid,
	}
}

// testRoot 构建含一个合成家族的路由根,叶子状态由参数指定,便于覆盖认证分支。
// relay/long(需 okx_1h) 与 relay/short(需 historical_depth):证明缺数据只关相关叶。
func testRoot(t *testing.T, longState, shortState LeafState) Root {
	t.Helper()
	leaves := []Leaf{
		{
			LeafID: "test.relay.long", MechanismFamily: "test_family",
			Payer: "short_liquidations", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong,
			Generation: "test-g1", ProtocolHash: "test", RequiredData: []string{"okx_1h"},
			Invalidators: []string{"deadline_reached"}, State: longState,
		},
		{
			LeafID: "test.relay.short", MechanismFamily: "test_family",
			Payer: "long_liquidations", Phase: PhaseRelay, ForcedSide: SideShort, Direction: SideShort,
			Generation: "test-g1", ProtocolHash: "test", RequiredData: []string{"historical_depth"},
			Invalidators: []string{"deadline_reached"}, State: shortState,
		},
	}
	root, err := NewRoot(leaves, nil)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return root
}

func TestNewRootRejectsBadDirection(t *testing.T) {
	// relay 方向必须 = forced side;这里故意让 direction 与规则不符。
	_, err := NewRoot([]Leaf{{
		LeafID: "bad", MechanismFamily: "f", Phase: PhaseRelay, ForcedSide: SideLong,
		Direction: SideShort, State: LeafLiveApproved,
	}}, nil)
	if err == nil {
		t.Fatalf("expected direction-rule violation error")
	}
}

func TestGenericMechanismVariantRoutesWithoutLiquidationPhase(t *testing.T) {
	root, err := NewRoot([]Leaf{{
		LeafID: "funding.collection.long", MechanismFamily: "funding_settlement",
		Payer: "funding_payers", Variant: "collection", Direction: SideLong,
		Generation: "router-g1", ProtocolHash: "router", RequiredData: []string{"okx_funding"},
		Invalidators: []string{"settlement_complete"}, State: LeafShadowPass,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := validObs("funding_settlement", "", SideUnknown, []string{"okx_funding"})
	obs.Variant = "collection"
	trace := root.Route(obs)
	if trace.Outcome != OutcomeShadow || trace.Direction != SideLong || trace.LeafID != "funding.collection.long" {
		t.Fatalf("generic mechanism failed to route: %+v", trace)
	}
}

func TestDataTruthFailClosed(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	cases := []struct {
		status DataTruthStatus
		reason ReasonCode
	}{
		{DataTruthInvalid, ReasonDataTruthInvalid},
		{DataTruthMissing, ReasonDataTruthInvalid},
		{DataTruthLate, ReasonDataTruthInvalid},
		{DataTruthCorrection, ReasonDataTruthInvalid},
		{DataTruthDuplicate, ReasonDataTruthInvalid},
		{DataTruthOutOfOrder, ReasonDataTruthInvalid},
		{DataTruthUnknown, ReasonDataTruthUnknown},
	}
	for _, c := range cases {
		obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
		obs.DataTruth = c.status
		tr := root.Route(obs)
		if tr.Outcome != OutcomeNoTrade || tr.Reason != c.reason || tr.TerminalNode != NodeDataTruth {
			t.Errorf("DataTruth=%q -> outcome=%q reason=%q node=%q, want NO_TRADE/%q/DataTruth", c.status, tr.Outcome, tr.Reason, tr.TerminalNode, c.reason)
		}
	}
}

func TestGlobalContextOODAndUnknownNoTrade(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	for _, c := range []struct {
		gc     GlobalContextStatus
		reason ReasonCode
	}{
		{GlobalUnsupported, ReasonGlobalContextOOD},
		{GlobalUnknown, ReasonGlobalContextUnk},
	} {
		obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
		obs.GlobalContext = c.gc
		tr := root.Route(obs)
		if tr.Outcome != OutcomeNoTrade || tr.Reason != c.reason || tr.TerminalNode != NodeGlobalContext {
			t.Errorf("GlobalContext=%q -> %+v, want NO_TRADE/%q", c.gc, tr, c.reason)
		}
	}
}

func TestMechanismUnknownAndNoPayer(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)

	obs := validObs("nonexistent_family", PhaseRelay, SideLong, []string{"okx_1h"})
	tr := root.Route(obs)
	if tr.Reason != ReasonMechanismUnknown || tr.TerminalNode != NodeMechanismFamily {
		t.Errorf("unknown family -> %+v, want MECHANISM_UNKNOWN", tr)
	}

	obs2 := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
	obs2.PayerObservable = false
	tr2 := root.Route(obs2)
	if tr2.Reason != ReasonMechanismNoPayer || tr2.TerminalNode != NodeMechanismFamily {
		t.Errorf("no payer -> %+v, want MECHANISM_NO_PAYER", tr2)
	}
}

func TestPhaseSideAmbiguous(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	for _, mut := range []func(*Observation){
		func(o *Observation) { o.PhaseAmbiguous = true },
		func(o *Observation) { o.Phase = "" },
		func(o *Observation) { o.ForcedSide = SideUnknown },
		func(o *Observation) { o.Phase = PhaseExhaustion }, // 该家族无 exhaustion 叶 -> 无匹配叶
	} {
		obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
		mut(&obs)
		tr := root.Route(obs)
		if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonPhaseSideAmbiguous || tr.TerminalNode != NodePhaseSide {
			t.Errorf("ambiguous phase/side -> %+v, want PHASE_SIDE_AMBIGUOUS", tr)
		}
	}
}

// TestDataMissingClosesOnlyRelevantLeaf:同一 AvailableData(缺 historical_depth,有 okx_1h)下,
// relay/long 叶(只要 okx_1h)成功路由,relay/short 叶(要 depth)只自己 LEAF_DATA_MISSING。
func TestDataMissingClosesOnlyRelevantLeaf(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	avail := []string{"okx_1h"} // 无 historical_depth

	trLong := root.Route(validObs("test_family", PhaseRelay, SideLong, avail))
	if trLong.Outcome != OutcomeCandidate || trLong.Direction != SideLong {
		t.Errorf("relay/long should route despite missing depth: %+v", trLong)
	}

	trShort := root.Route(validObs("test_family", PhaseRelay, SideShort, avail))
	if trShort.Outcome != OutcomeNoTrade || trShort.Reason != ReasonLeafDataMissing || trShort.TerminalNode != NodePhaseSide {
		t.Errorf("relay/short should be LEAF_DATA_MISSING: %+v", trShort)
	}
	if trShort.LeafID != "test.relay.short" {
		t.Errorf("LEAF_DATA_MISSING should keep leaf id, got %q", trShort.LeafID)
	}
}

func TestExecutabilityFailClosed(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	for _, ex := range []ExecutabilityStatus{ExecInvalid, ExecUnknown} {
		obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
		obs.Executability = ex
		tr := root.Route(obs)
		if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonExecutability || tr.TerminalNode != NodeExecutability {
			t.Errorf("executability=%q -> %+v, want EXECUTABILITY_INVALID", ex, tr)
		}
	}
}

func TestCertificationStates(t *testing.T) {
	// 未认证态一律 NO_TRADE / NOT_CERTIFIED
	for _, st := range []LeafState{LeafUnresearched, LeafResearchPass, LeafExamPass} {
		root := testRoot(t, st, LeafLiveApproved)
		tr := root.Route(validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"}))
		if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonNotCertified || tr.TerminalNode != NodeCertification {
			t.Errorf("state=%q -> %+v, want NOT_CERTIFIED", st, tr)
		}
	}
	// SHADOW_PASS -> shadow 候选
	rootShadow := testRoot(t, LeafShadowPass, LeafLiveApproved)
	trS := rootShadow.Route(validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"}))
	if trS.Outcome != OutcomeShadow || trS.Reason != ReasonShadowCandidate || trS.Direction != SideLong {
		t.Errorf("SHADOW_PASS -> %+v, want SHADOW/long", trS)
	}
	// LIVE_APPROVED -> live 候选
	rootLive := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	trL := rootLive.Route(validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"}))
	if trL.Outcome != OutcomeCandidate || trL.Reason != ReasonRoutedCandidate || trL.Direction != SideLong {
		t.Errorf("LIVE_APPROVED -> %+v, want CANDIDATE/long", trL)
	}
}

func TestPortfolioRiskBlocked(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
	obs.PortfolioBlocked = true
	tr := root.Route(obs)
	if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonPortfolioBlocked || tr.TerminalNode != NodePortfolioRisk {
		t.Errorf("portfolio blocked -> %+v, want PORTFOLIO_RISK_BLOCKED", tr)
	}
}

// TestForcedLiquidationLeavesUntestableNotRevived:四个强平候选叶当前 UNTESTABLE,
// 即便数据齐全、行情支持、可执行,路由到它们也必然 LEAF_CLOSED,不可复活。
func TestForcedLiquidationLeavesUntestableNotRevived(t *testing.T) {
	root, err := NewForcedLiquidationRoot()
	if err != nil {
		t.Fatalf("NewForcedLiquidationRoot: %v", err)
	}
	full := []string{"historical_liquidation", "historical_oi", "historical_depth", "historical_xvenue", "pit_instrument", "okx_5m"}
	cases := []struct {
		phase PhaseKind
		side  Side
	}{
		{PhaseRelay, SideShort},
		{PhaseRelay, SideLong},
		{PhaseExhaustion, SideShort},
		{PhaseExhaustion, SideLong},
	}
	for _, c := range cases {
		obs := validObs("panic_liquidation", c.phase, c.side, full)
		tr := root.Route(obs)
		if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonLeafClosed || tr.TerminalNode != NodePhaseSide {
			t.Errorf("forced-liq %s/%s -> %+v, want LEAF_CLOSED (untestable, not revived)", c.phase, c.side, tr)
		}
	}
}

// TestMultiTreeSameDirectionAllowed:多树同向候选保留(账户层处理相关性)。
func TestMultiTreeSameDirectionAllowed(t *testing.T) {
	leaves := []Leaf{
		{LeafID: "a.relay.long", MechanismFamily: "fam_a", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
		{LeafID: "b.relay.long", MechanismFamily: "fam_b", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
	}
	root, err := NewRoot(leaves, nil)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	obsA := validObs("fam_a", PhaseRelay, SideLong, []string{"okx_1h"})
	obsB := validObs("fam_b", PhaseRelay, SideLong, []string{"okx_1h"})
	dec := root.RouteAll([]Observation{obsA, obsB})
	if len(dec.Approved) != 2 {
		t.Errorf("same-direction multi-tree should keep both candidates, got %d approved: %+v", len(dec.Approved), dec)
	}
	if len(dec.Conflicts) != 0 {
		t.Errorf("same-direction should have no conflict, got %+v", dec.Conflicts)
	}
}

// TestMultiTreeOpposingConflictFlat:反向冲突且无预注册仲裁规则 -> 该 instrument 空仓。
func TestMultiTreeOpposingConflictFlat(t *testing.T) {
	leaves := []Leaf{
		{LeafID: "a.relay.long", MechanismFamily: "fam_a", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
		{LeafID: "b.exh.long", MechanismFamily: "fam_b", Phase: PhaseExhaustion, ForcedSide: SideLong, Direction: SideShort, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
	}
	root, err := NewRoot(leaves, nil)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	obsA := validObs("fam_a", PhaseRelay, SideLong, []string{"okx_1h"})      // long
	obsB := validObs("fam_b", PhaseExhaustion, SideLong, []string{"okx_1h"}) // short
	dec := root.RouteAll([]Observation{obsA, obsB})
	if len(dec.Approved) != 0 {
		t.Errorf("opposing conflict should approve nothing, got %+v", dec.Approved)
	}
	if len(dec.Conflicts) != 1 || dec.Conflicts[0].Reason != ReasonConflictNoArb {
		t.Errorf("opposing conflict should record CONFLICT_NO_ARBITRATION, got %+v", dec.Conflicts)
	}
	for _, tr := range dec.Traces {
		if tr.Outcome != OutcomeNoTrade || tr.Reason != ReasonConflictNoArb {
			t.Errorf("both traces should be forced NO_TRADE/conflict, got %+v", tr)
		}
	}
}

// TestArbitrationRuleAllowsConflict:存在预注册仲裁规则时不强制空仓(交由账户层)。
func TestArbitrationRuleAllowsConflict(t *testing.T) {
	leaves := []Leaf{
		{LeafID: "a.relay.long", MechanismFamily: "fam_a", Phase: PhaseRelay, ForcedSide: SideLong, Direction: SideLong, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
		{LeafID: "b.exh.long", MechanismFamily: "fam_b", Phase: PhaseExhaustion, ForcedSide: SideLong, Direction: SideShort, Generation: "g", ProtocolHash: "h", RequiredData: []string{"okx_1h"}, Invalidators: []string{"deadline_reached"}, State: LeafLiveApproved},
	}
	root, err := NewRoot(leaves, []string{"X-USDT-SWAP"})
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	dec := root.RouteAll([]Observation{
		validObs("fam_a", PhaseRelay, SideLong, []string{"okx_1h"}),
		validObs("fam_b", PhaseExhaustion, SideLong, []string{"okx_1h"}),
	})
	if len(dec.Conflicts) != 0 {
		t.Errorf("arbitration rule present should suppress forced-flat conflict, got %+v", dec.Conflicts)
	}
	if len(dec.Approved) != 2 {
		t.Errorf("arbitration rule present should keep both candidates for account layer, got %d", len(dec.Approved))
	}
}

// TestRoutePathIsPrefixOfNodeOrder:任何 trace 的 Path 必须是冻结 NodeOrder 的前缀(节点顺序不乱)。
func TestRoutePathIsPrefixOfNodeOrder(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	obss := []Observation{
		validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"}),
		func() Observation {
			o := validObs("test_family", PhaseRelay, SideLong, nil)
			o.DataTruth = DataTruthInvalid
			return o
		}(),
		func() Observation {
			o := validObs("test_family", PhaseRelay, SideLong, nil)
			o.GlobalContext = GlobalUnknown
			return o
		}(),
		func() Observation { o := validObs("test_family", PhaseRelay, SideShort, []string{"okx_1h"}); return o }(),
	}
	for _, obs := range obss {
		tr := root.Route(obs)
		if len(tr.Path) > len(NodeOrder) {
			t.Fatalf("path longer than NodeOrder: %v", tr.Path)
		}
		for i, n := range tr.Path {
			if n != NodeOrder[i] {
				t.Errorf("path[%d]=%q not prefix of NodeOrder(%q)", i, n, NodeOrder[i])
			}
		}
	}
}

// TestCoarseTraceNoLeak:trace 序列化后不得含任何禁泄漏子串,reason 必须在冻结集合内。
func TestCoarseTraceNoLeak(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	allowed := map[ReasonCode]bool{}
	for _, r := range ReasonCodes {
		allowed[r] = true
	}
	obss := []Observation{
		validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"}),
		func() Observation { o := validObs("test_family", PhaseRelay, SideShort, nil); return o }(),
		func() Observation { o := validObs("bad", PhaseRelay, SideLong, nil); return o }(),
	}
	for _, obs := range obss {
		tr := root.Route(obs)
		if !allowed[tr.Reason] {
			t.Errorf("trace reason %q not in frozen ReasonCodes", tr.Reason)
		}
		raw, err := json.Marshal(tr)
		if err != nil {
			t.Fatalf("marshal trace: %v", err)
		}
		low := strings.ToLower(string(raw))
		for _, bad := range CoarseTraceForbiddenSubstrings {
			if strings.Contains(low, strings.ToLower(bad)) {
				t.Errorf("trace leaks forbidden substring %q: %s", bad, raw)
			}
		}
	}
}

// TestRouteIdempotent:相同输入重复路由结果确定一致(可复现)。
func TestRouteIdempotent(t *testing.T) {
	root := testRoot(t, LeafLiveApproved, LeafLiveApproved)
	obs := validObs("test_family", PhaseRelay, SideLong, []string{"okx_1h"})
	a := root.Route(obs)
	b := root.Route(obs)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("route not idempotent: %+v vs %+v", a, b)
	}
}
