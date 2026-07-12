package episodic

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg6/eventmap"
)

func syntheticEpisodes() []FrozenEpisode {
	now := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	fp := fingerprint.PersonalExecutionFingerprint{FingerprintHash: "fp-test"}
	baseQuery := func(i int, ret, outcome float64, win bool) FrozenEpisode {
		ev := eventmap.DetectedEvent{
			ID: fmtID(i), Symbol: "BTC-USDT-SWAP", Family: eventmap.FamilyExtremeContinuation,
			DecisionTime: now.Add(time.Duration(i) * time.Hour),
			ReturnBPS: ret, PriorReturnBPS: ret * 0.2, BTCRelativeBPS: 3,
		}
		out := eventmap.EventOutcome{
			EventID: ev.ID, Direction: "long", ConservativeBPS: outcome,
		}
		ep, _ := BuildEpisodeFromEvent(ev, out, eventmap.ProfileOKX1H, 0.5, ActionPath{"LONG"}, fp, protocol.RouterProtocolHash)
		ep.Outcome.Win = win
		ep.Outcome.NetBPS = outcome
		return ep
	}
	return []FrozenEpisode{
		baseQuery(1, 50, 12, true),
		baseQuery(2, 48, 8, true),
		baseQuery(3, 52, -5, false),
		baseQuery(4, 49, -3, false),
		baseQuery(5, 51, -4, false),
		baseQuery(6, 47, -6, false),
		baseQuery(7, 46, -7, false),
		baseQuery(8, 45, -8, false),
		baseQuery(9, 44, -2, false),
	}
}

func fmtID(i int) string { return fmt.Sprintf("evt-%d", i) }

func TestValidateQueryRejectsOutcomeLabelField(t *testing.T) {
	q := QueryVector{EventFamily: "ohlcv_extreme_continuation"}
	err := ValidateQuery(q, map[string]float64{"label": 1})
	if err == nil {
		t.Fatal("label query field must be rejected")
	}
}

func TestLookaheadRejectionForbiddenQueryField(t *testing.T) {
	q := QueryVector{EventFamily: eventmap.FamilyExtremeContinuation}
	err := ValidateQuery(q, map[string]float64{"future_outcome": 1})
	if err == nil {
		t.Fatal("future_outcome query must be rejected")
	}
	err = ValidateQuery(q, map[string]float64{"realized_pnl": 5})
	if err == nil {
		t.Fatal("realized_pnl query must be rejected")
	}
}

func TestK8MaxRetrieval(t *testing.T) {
	store := NewStore(syntheticEpisodes())
	q := QueryVector{
		Symbol: "BTC-USDT-SWAP", EventFamily: eventmap.FamilyExtremeContinuation, EventPhase: 0.5,
		State: StateVector{ReturnBPS: 50, PriorReturnBPS: 10, RollingVolumeUSDT: 5e5},
		PersonalExec: PersonalExecSnapshot{FingerprintKnown: true},
	}
	hits, err := store.Retrieve(q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != MaxRetrievalK {
		t.Fatalf("want K=%d hits, got %d", MaxRetrievalK, len(hits))
	}
}

func TestNegativeMajorityVeto(t *testing.T) {
	store := NewStore(syntheticEpisodes())
	q := QueryVector{
		Symbol: "BTC-USDT-SWAP", EventFamily: eventmap.FamilyExtremeContinuation, EventPhase: 0.5,
		State: StateVector{ReturnBPS: 50, PriorReturnBPS: 10},
	}
	hits, _ := store.Retrieve(q, nil)
	advice := Advise(hits, DefaultResidual())
	if !advice.VetoOpen {
		t.Fatalf("expected veto with loss majority, got %+v", advice)
	}
	if !contains(advice.AdviceCodes, AdviceVetoOpen) {
		t.Fatalf("want %s in advice codes", AdviceVetoOpen)
	}
}

func TestInsufficientSampleReduceOrNoTrade(t *testing.T) {
	hits := []RetrievalHit{{OutcomeWin: true, Similarity: 0.9}}
	advice := Advise(hits, DefaultResidual())
	if advice.SizeScale >= 1 {
		t.Fatalf("insufficient sample must shrink size, got %v", advice.SizeScale)
	}
	if !contains(advice.AdviceCodes, AdviceInsufficientSample) {
		t.Fatal("expected insufficient sample code")
	}
}

func TestCanonicalHashStableAcrossMarshal(t *testing.T) {
	ep := syntheticEpisodes()[0]
	h1, err := HashEpisode(ep)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ep)
	var decoded FrozenEpisode
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	h2, err := HashEpisode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("episode hash must be stable across marshal")
	}
	idx, err := SealIndex(syntheticEpisodes(), protocol.RouterProtocolHash, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw1, _ := json.Marshal(idx)
	raw2, _ := json.Marshal(idx)
	ha, _, _ := datatruth.HashJSON(json.RawMessage(raw1))
	hb, _, _ := datatruth.HashJSON(json.RawMessage(raw2))
	if ha != hb {
		t.Fatal("index hash drift")
	}
}

func TestResidualDisabledByDefault(t *testing.T) {
	r := DefaultResidual()
	if r.Enabled() {
		t.Fatal("residual must be disabled by default")
	}
	if r.Status() != ResidualDefaultStatus {
		t.Fatalf("status = %q", r.Status())
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResidualEnableRequiresWFGain(t *testing.T) {
	r := ResidualModel{StatusFlag: "ENABLED", Weights: []float64{0.1}, WFGain: false}
	if err := r.Validate(); err == nil {
		t.Fatal("enable without wf gain must fail")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
