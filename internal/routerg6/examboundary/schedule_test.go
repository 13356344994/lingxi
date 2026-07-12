package examboundary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
	"lingxi/internal/routerg6/protocol"
)

func TestGoldenMustBeLastInSchedule(t *testing.T) {
	commitments := syntheticCommitments("golden-last")
	if err := ValidateSchedule(commitments); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	if commitments[len(commitments)-1].Kind != "golden" {
		t.Fatal("golden must be final slot")
	}
}

func TestOneTimeConsumptionAfterGrade(t *testing.T) {
	commitments := syntheticCommitments("once")
	now := time.Now().UTC()
	budget := map[string]int{
		"high_volatility": 1, "low_volatility_funding": 1, "cross_sectional_breadth": 1,
		"delay_stress": 1, "golden": 1,
	}
	state, err := gauntlet.CreateGeneration(gauntlet.State{}, gauntlet.FixedAccess{}, gauntlet.RoleCustodian, protocol.Generation, ToGauntletTickets(commitments), budget, "virtual-manifest", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = gauntlet.UnsealNext(state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, protocol.Generation, now)
	if err != nil {
		t.Fatal(err)
	}
	signer := exam.HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")}
	state, _, err = gauntlet.Grade(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, protocol.Generation, true, "PASS", now, signer)
	if err != nil {
		t.Fatal(err)
	}
	if state.Generations[0].Exams[0].Status != exam.StatusConsumedPassed {
		t.Fatalf("first paper must be consumed: %s", state.Generations[0].Exams[0].Status)
	}
	if _, _, err := gauntlet.Grade(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, protocol.Generation, true, "PASS", now, signer); err == nil {
		t.Fatal("second grade without unseal must fail")
	}
}

func TestPublicCommitmentJSONHiddenLeak(t *testing.T) {
	doc := Document{
		SchemaVersion: protocol.PublicCommitmentSchemaV1,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Commitments:   syntheticCommitments("leak"),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"start", "cluster_ids", "fixture_path", "observations", "2024"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(forbidden)) {
			t.Fatalf("public doc leaked %q: %s", forbidden, raw)
		}
	}
}
