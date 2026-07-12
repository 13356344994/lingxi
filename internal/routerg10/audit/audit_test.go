package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/protocol"
)

func sampleEntry(action string) BrainTraceEntry {
	return BrainTraceEntry{
		SchemaVersion:      SchemaVersion,
		Generation:         protocol.Generation,
		ProtocolHash:       protocol.RouterProtocolHash,
		BarIndex:           1,
		DecisionTime:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Symbol:             "BTC-USDT-SWAP",
		EventFamily:        "ohlcv_extreme_continuation",
		FingerprintVersion: "fp-hash",
		CounterfactualTable: counterfactual.CounterfactualTable{
			TableHash: "table-hash",
		},
		FinalAction:    action,
		StopErrorState: "holding",
		SolvencyState:  "ok",
	}
}

func TestAppendOnlyTraceHashStable(t *testing.T) {
	log := NewTraceLog()
	e1, err := log.Append(sampleEntry("LONG"))
	if err != nil {
		t.Fatal(err)
	}
	e2, err := log.Append(sampleEntry("HOLD"))
	if err != nil {
		t.Fatal(err)
	}
	if e1.TraceHash == e2.TraceHash {
		t.Fatal("distinct entries must have distinct trace hashes")
	}
	log2 := NewTraceLog()
	a, _ := log2.Append(sampleEntry("LONG"))
	b, _ := log2.Append(sampleEntry("HOLD"))
	if a.TraceHash != log.Entries[0].TraceHash || b.TraceHash != log.Entries[1].TraceHash {
		t.Fatal("replay trace hash drift")
	}
}

func TestVerifyReplayMatch(t *testing.T) {
	log1 := NewTraceLog()
	log2 := NewTraceLog()
	for _, action := range []string{"LONG", "HOLD", "CLOSE"} {
		_, _ = log1.Append(sampleEntry(action))
		_, _ = log2.Append(sampleEntry(action))
	}
	res := VerifyReplay(*log1, *log2)
	if !res.Match {
		t.Fatalf("replay mismatch: %+v", res)
	}
}

func TestDegeneracySingleActionDominance(t *testing.T) {
	log := NewTraceLog()
	for i := 0; i < 100; i++ {
		_, _ = log.Append(sampleEntry("LONG"))
	}
	report, err := DetectDegeneracy(*log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Degenerate || report.Status != DegeneracyStatus {
		t.Fatalf("expected degenerate report: %+v", report)
	}
}

func TestDegeneracyNeverExit(t *testing.T) {
	log := NewTraceLog()
	for i := 0; i < NeverExitBarsThreshold+10; i++ {
		action := "LONG"
		if i%3 == 0 {
			action = "HOLD"
		}
		_, _ = log.Append(sampleEntry(action))
	}
	report, err := DetectDegeneracy(*log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Degenerate {
		t.Fatal("expected never_exit degeneracy")
	}
}

func TestDegeneracyHealthyMix(t *testing.T) {
	log := NewTraceLog()
	actions := []string{"NO_TRADE", "LONG", "HOLD", "CLOSE", "TRIM", "SHORT", "NO_TRADE", "HOLD"}
	for _, a := range actions {
		_, _ = log.Append(sampleEntry(a))
	}
	report, err := DetectDegeneracy(*log, map[string]bool{
		"continuation_long": true, "event_complete_close": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Degenerate {
		t.Fatalf("healthy mix flagged degenerate: %+v", report)
	}
}

func TestStateHashUsesTraceMemoryHitsOnly(t *testing.T) {
	log := NewTraceLog()
	entry, err := log.Append(BrainTraceEntry{
		SchemaVersion: SchemaVersion, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, BarIndex: 1,
		DecisionTime: sampleEntry("LONG").DecisionTime,
		Symbol: "BTC-USDT-SWAP", FingerprintVersion: "fp",
		MemoryHits: []episodic.TraceMemoryHit{{EpisodeHash: "ep1", EventID: "e1", Similarity: 0.9}},
		CounterfactualTable: counterfactual.CounterfactualTable{TableHash: "t"},
		FinalAction: "LONG", StopErrorState: "holding", SolvencyState: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(entry)
	if strings.Contains(string(raw), "outcome_win") || strings.Contains(string(raw), "conservative_net_bps") {
		t.Fatal("trace memory hits must not serialize outcome labels")
	}
}

func TestRequiredFieldsValidated(t *testing.T) {
	log := NewTraceLog()
	entry, err := log.Append(sampleEntry("NO_TRADE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEntry(entry); err != nil {
		t.Fatal(err)
	}
}

func TestPathMatrixRequiredList(t *testing.T) {
	if len(PathMatrixRequired) < 8 {
		t.Fatalf("path matrix count=%d want >=8", len(PathMatrixRequired))
	}
}
