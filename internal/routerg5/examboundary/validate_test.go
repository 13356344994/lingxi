package examboundary

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
	"lingxi/internal/routerg5/protocol"
)

func syntheticCommitments(prefix string) []PublicCommitment {
	out := make([]PublicCommitment, len(protocol.ExamScheduleTemplate))
	for i, slot := range protocol.ExamScheduleTemplate {
		out[i] = PublicCommitment{
			ID:            prefix + "-paper-" + slot.Family,
			Queue:         slot.Queue,
			Kind:          slot.Kind,
			Family:        slot.Family,
			AlphaSpend:    slot.AlphaSpend,
			DataHash:      prefix + "-data-" + slot.Family,
			ChallengeHash: prefix + "-challenge-" + slot.Family,
			ProtocolHash:  protocol.RouterProtocolHash,
			Status:        "sealed",
		}
	}
	return out
}

func TestPublicCommitmentSchemaLeak(t *testing.T) {
	leaky, err := json.Marshal(map[string]any{
		"schema_version": protocol.PublicCommitmentSchemaV1,
		"generation":     protocol.Generation,
		"protocol_hash":  protocol.RouterProtocolHash,
		"commitments": []map[string]any{{
			"id": "x", "queue": 1, "kind": "standard", "family": "high_volatility",
			"alpha_spend": 1, "data_hash": "d", "challenge_hash": "c",
			"protocol_hash": protocol.RouterProtocolHash, "status": "sealed",
			"start": "2026-01-01T00:00:00Z",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AssertNoSchemaLeak(leaky); err == nil {
		t.Fatal("hidden start time must be rejected")
	}
}

func TestSyntheticCommitmentOrderDuplicateConsumption(t *testing.T) {
	commitments := syntheticCommitments("syn")
	if err := ValidateSchedule(commitments); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	dup := append([]PublicCommitment(nil), commitments...)
	dup[1].ChallengeHash = dup[0].ChallengeHash
	if err := ValidateSchedule(dup); err == nil {
		t.Fatal("duplicate challenge hash must be rejected")
	}
	reordered := append([]PublicCommitment(nil), commitments[1], commitments[0], commitments[2], commitments[3], commitments[4])
	if err := ValidateSchedule(reordered); err == nil {
		t.Fatal("template order drift must be rejected")
	}

	now := time.Now().UTC()
	budget := map[string]int{"high_volatility": 1, "low_volatility_funding": 1, "cross_sectional_breadth": 1, "delay_stress": 1, "golden": 1}
	tickets := ToGauntletTickets(commitments)
	state, err := gauntlet.CreateGeneration(gauntlet.State{}, gauntlet.FixedAccess{}, gauntlet.RoleCustodian, protocol.Generation, tickets, budget, "virtual-manifest", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = gauntlet.UnsealNext(state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, protocol.Generation, now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = gauntlet.Crash(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, protocol.Generation, now, exam.HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	if state.Generations[0].Exams[0].Status != exam.StatusConsumedCrashed {
		t.Fatalf("crash must consume first paper, got %s", state.Generations[0].Exams[0].Status)
	}
	if err := AssertConsumptionOrder(state, protocol.Generation); err != nil {
		t.Fatalf("consumption order: %v", err)
	}
}

func TestVerifyExamUnavailableWithoutDocument(t *testing.T) {
	report, err := Verify("", "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != protocol.ExamUnavailableStatus || report.ExamActions != 0 || report.Available {
		t.Fatalf("expected EXAM_UNAVAILABLE exam_actions=0: %+v", report)
	}
}

func TestVerifyAvailableSyntheticDocument(t *testing.T) {
	doc := Document{
		SchemaVersion: protocol.PublicCommitmentSchemaV1,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Commitments:   syntheticCommitments("ok"),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDocument(doc); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		t.Fatal(err)
	}
}

func TestHiddenManifestPathRejected(t *testing.T) {
	if err := rejectHiddenManifestPath("/tmp/hidden-manifest.json"); err == nil {
		t.Fatal("hidden manifest path must be rejected")
	}
}
