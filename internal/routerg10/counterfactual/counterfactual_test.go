package counterfactual

import (
	"encoding/json"
	"testing"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

func TestLookaheadNotInQueryFeatures(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	err := episodic.ValidateQuery(ctx.Query, map[string]float64{"post_event_labels": 1})
	if err == nil {
		t.Fatal("post_event_labels must be forbidden in query")
	}
}

func TestK8MemoryIntegration(t *testing.T) {
	episodes := episodicSyntheticForCF()
	store := episodic.NewStore(episodes)
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	hits, err := store.Retrieve(ctx.Query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != episodic.MaxRetrievalK {
		t.Fatalf("want K8, got %d", len(hits))
	}
	ctx.Memory = episodic.Advise(hits, episodic.DefaultResidual())
	dec, err := Decide(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction == "" {
		t.Fatal("expected decision action")
	}
}

func TestBreakevenGate(t *testing.T) {
	sc := findScenario("breakeven_fail_no_trade")
	dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE, got %s", dec.FinalAction)
	}
	if !containsReject(dec.RejectReasons, RejectBreakevenFail) {
		t.Fatalf("want breakeven reject, got %+v", dec.RejectReasons)
	}
}

func TestStressInsolventGate(t *testing.T) {
	sc := findScenario("stress_insolvent_no_trade")
	dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE, got %s", dec.FinalAction)
	}
	if !containsReject(dec.RejectReasons, RejectStressInsolvent) {
		t.Fatalf("want stress reject, got %+v", dec.RejectReasons)
	}
}

func TestMinLotReject(t *testing.T) {
	sc := findScenario("min_lot_skip")
	dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE, got %s", dec.FinalAction)
	}
	found := containsReject(dec.RejectReasons, RejectMinLotInfeasible) ||
		containsReject(dec.RejectReasons, RejectBelowMinNotional)
	if !found {
		t.Fatalf("want min lot reject, got %+v", dec.RejectReasons)
	}
}

func TestDelayBarsPreserved(t *testing.T) {
	sc := findScenario("delay_stress")
	dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.DelayBars != 2 {
		t.Fatalf("delay bars = %d want 2", dec.DelayBars)
	}
}

func TestMemoryVetoBlocksOpen(t *testing.T) {
	sc := findScenario("negative_memory_no_trade")
	dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE, got %s", dec.FinalAction)
	}
	if !containsReject(dec.RejectReasons, RejectMemoryVeto) {
		t.Fatalf("want memory veto, got %+v", dec.RejectReasons)
	}
}

func TestNoEventOnlyHoldTrimClose(t *testing.T) {
	sc := findScenario("no_event_hold_only")
	table, err := Compare(sc.Context, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range table.Actions {
		if row.Action == "LONG" || row.Action == "SHORT" || row.Action == "ADD" || row.Action == "REVERSE" {
			if row.Allowed {
				t.Fatalf("open action %s must be disallowed without event", row.Action)
			}
		}
	}
	dec, _ := Decide(sc.Context, protocol.RouterProtocolHash)
	if dec.FinalAction != "HOLD" {
		t.Fatalf("want HOLD, got %s", dec.FinalAction)
	}
}

func TestEightActionsCompared(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	table, err := Compare(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Actions) != len(ActionsCompared) {
		t.Fatalf("action count = %d want %d", len(table.Actions), len(ActionsCompared))
	}
}

func TestCanonicalHashStable(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	table1, _ := Compare(ctx, protocol.RouterProtocolHash)
	table2, _ := Compare(ctx, protocol.RouterProtocolHash)
	if table1.TableHash != table2.TableHash {
		t.Fatal("table hash drift")
	}
	raw, _ := json.Marshal(table1)
	var decoded CounterfactualTable
	_ = json.Unmarshal(raw, &decoded)
	h, _, _ := datatruth.HashJSON(decoded)
	_ = h
	dec1, _ := Decide(ctx, protocol.RouterProtocolHash)
	dec2, _ := Decide(ctx, protocol.RouterProtocolHash)
	if dec1.DecisionHash != dec2.DecisionHash {
		t.Fatal("decision hash drift")
	}
}

func TestPositiveEdgeStressNonZero(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	table, err := Compare(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range table.Actions {
		if row.Action != "LONG" && row.Action != "SHORT" {
			continue
		}
		if row.PersonalConservativeNetBPS > 0 && row.StressLossBPS <= 0 {
			t.Fatalf("%s positive edge must have adverse stress, got %+v", row.Action, row)
		}
	}
}

func TestPersonalAdjUnknownFailClosed(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	ctx.Fingerprint.FingerprintHash = ""
	ctx.PersonalAdj = residualmap.PersonalAdjustments{}
	dec, err := Decide(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("unknown fingerprint must fail closed, got %s", dec.FinalAction)
	}
	if !containsReject(dec.RejectReasons, RejectFingerprintUnknown) {
		t.Fatalf("want fingerprint reject, got %+v", dec.RejectReasons)
	}
}

func TestNearZeroPrefersNoTrade(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	ctx.Event.GrossLongBPS = 0.5
	ctx.Event.GrossShortBPS = -0.5
	dec, err := Decide(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("near-zero edge should prefer NO_TRADE, got %s", dec.FinalAction)
	}
}

func TestAdverseStressPassesSolvencyGate(t *testing.T) {
	ctx := BuildDefaultFixtures().Scenarios[0].Context
	row, ok := func() (ActionMetric, bool) {
		table, err := Compare(ctx, protocol.RouterProtocolHash)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range table.Actions {
			if r.Action == "LONG" {
				return r, true
			}
		}
		return ActionMetric{}, false
	}()
	if !ok {
		t.Fatal("missing LONG row")
	}
	if row.PersonalConservativeNetBPS-row.StressLossBPS < 0 {
		t.Fatalf("continuation fixture must pass stress solvency, net=%v stress=%v", row.PersonalConservativeNetBPS, row.StressLossBPS)
	}
}

func TestFixturePathMatrix(t *testing.T) {
	bundle := BuildDefaultFixtures()
	for _, sc := range bundle.Scenarios {
		dec, err := Decide(sc.Context, protocol.RouterProtocolHash)
		if err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		if dec.FinalAction != sc.WantAction {
			t.Fatalf("%s: want %s got %s rejects=%v", sc.Name, sc.WantAction, dec.FinalAction, dec.RejectReasons)
		}
		for _, want := range sc.WantReject {
			if !containsReject(dec.RejectReasons, want) {
				t.Fatalf("%s: want reject %s in %+v", sc.Name, want, dec.RejectReasons)
			}
		}
	}
}

func TestFixtureBundleDeterministic(t *testing.T) {
	b1 := BuildDefaultFixtures()
	b2 := BuildDefaultFixtures()
	if b1.BundleHash != b2.BundleHash {
		t.Fatal("fixture bundle hash drift")
	}
}

func findScenario(name string) FixtureScenario {
	for _, sc := range BuildDefaultFixtures().Scenarios {
		if sc.Name == name {
			return sc
		}
	}
	panic("scenario not found: " + name)
}

func containsReject(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func episodicSyntheticForCF() []episodic.FrozenEpisode {
	bundle := BuildDefaultFixtures()
	var out []episodic.FrozenEpisode
	for i, sc := range bundle.Scenarios {
		out = append(out, episodic.FrozenEpisode{
			SchemaVersion: episodic.SchemaVersionEpisode,
			Generation:    episodic.Generation,
			EpisodeHash:   "ep-" + sc.Name,
			EventID:       "fixture-" + sc.Name,
			Symbol:        sc.Context.Symbol,
			ProfileID:     sc.Context.ProfileID,
			EventFamily:   sc.Context.Query.EventFamily,
			EventPhase:    sc.Context.Query.EventPhase,
			State:         sc.Context.Query.State,
			PersonalExec:  sc.Context.Query.PersonalExec,
			Outcome: episodic.ConservativeOutcome{
				NetBPS: float64(i%3-1) * 5,
				Win:    i%3 != 0,
			},
		})
	}
	return out
}
