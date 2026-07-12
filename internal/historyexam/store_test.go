package historyexam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingxi/internal/gauntlet"
)

const testProtocol = "virtual-history-protocol"

func syntheticFixture(vals ...float64) []byte {
	b, _ := json.Marshal(SyntheticFixture{Observations: vals})
	return b
}

func buildPaper(t *testing.T, examID string, queue int, kind gauntlet.Kind, nonce string, obs ...float64) HiddenPaper {
	t.Helper()
	paper, err := NewHiddenPaper(testProtocol, PaperSpec{
		ExamID:     examID,
		Queue:      queue,
		Kind:       kind,
		Family:     "liquidation",
		AlphaSpend: 1,
		Stratum:    regime("high"),
		ClusterIDs: []string{"e1"},
		Start:      day(2024, 5, 1),
		End:        day(2024, 6, 1),
		Fixture:    syntheticFixture(obs...),
		Nonce:      nonce,
	})
	if err != nil {
		t.Fatal(err)
	}
	return paper
}

func examManifest(t *testing.T) HiddenManifest {
	t.Helper()
	return HiddenManifest{
		Generation: "g1",
		Partition:  PartitionExam,
		Papers: []HiddenPaper{
			buildPaper(t, "g1-standard", 1, gauntlet.KindStandard, "n1", 1, 2, 3),
			buildPaper(t, "g1-golden", 2, gauntlet.KindGolden, "n2", 4, 5, 6),
		},
	}
}

func TestCommitmentProjectionHidesRangesAndFixture(t *testing.T) {
	manifest := examManifest(t)
	commitments := manifest.Commitments()
	if len(commitments) != 2 {
		t.Fatalf("expected two commitments, got %d", len(commitments))
	}
	// Public commitments must serialize without any hidden field.
	raw, err := json.Marshal(commitments)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fixture", "stratum", "cluster", "observations", "axes", "volatility", "funding_state", "2024"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("public commitment leaked hidden field %q: %s", forbidden, raw)
		}
	}
	if commitments[0].ChallengeHash == "" || commitments[0].DataHash == "" || commitments[0].ProtocolHash == "" {
		t.Fatal("public commitment must expose the ticket hashes")
	}
}

func TestHiddenManifestPersistsAt0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidden", "manifest.json")
	if err := SaveHiddenManifest(path, examManifest(t)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("hidden manifest must be 0600, got %o", info.Mode().Perm())
	}
	loaded, err := LoadHiddenManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Papers) != 2 {
		t.Fatalf("round trip lost papers: %+v", loaded)
	}
}

func TestLoadRefusesLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := SaveHiddenManifest(path, examManifest(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHiddenManifest(path); err == nil {
		t.Fatal("loading a group/other-readable hidden manifest must fail closed")
	}
}

func TestManifestRequiresGoldenLastAndUniqueCommitments(t *testing.T) {
	// Golden not last.
	bad := HiddenManifest{Generation: "g1", Partition: PartitionExam, Papers: []HiddenPaper{
		buildPaper(t, "g1-golden", 1, gauntlet.KindGolden, "n1", 1, 2),
		buildPaper(t, "g1-standard", 2, gauntlet.KindStandard, "n2", 3, 4),
	}}
	if err := bad.Validate(); err == nil {
		t.Fatal("golden paper must occupy the final queue")
	}
	// Duplicate data hash (same fixture + nonce -> same commitments).
	dup := HiddenManifest{Generation: "g1", Partition: PartitionExam, Papers: []HiddenPaper{
		buildPaper(t, "g1-standard", 1, gauntlet.KindStandard, "same", 1, 2),
		buildPaper(t, "g1-golden", 2, gauntlet.KindGolden, "same", 1, 2),
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate commitments within a manifest must be rejected")
	}
}

func TestManifestMustSealExamPartition(t *testing.T) {
	m := examManifest(t)
	m.Partition = PartitionResearch
	if err := m.Validate(); err == nil {
		t.Fatal("only the exam partition may be sealed into a hidden manifest")
	}
}

func TestDataHashBindsFixtureBytes(t *testing.T) {
	paper := buildPaper(t, "g1-standard", 1, gauntlet.KindStandard, "n1", 1, 2, 3)
	if HashData(paper.Fixture) != paper.DataHash {
		t.Fatal("data hash must bind the exact fixture bytes")
	}
	tampered := paper
	tampered.Fixture = syntheticFixture(9, 9, 9)
	if HashData(tampered.Fixture) == paper.DataHash {
		t.Fatal("tampering the fixture must change the data hash")
	}
}

func TestFileBackedPaperStreamsAndDetectsMutation(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "hidden-paper.json")
	if err := os.WriteFile(fixturePath, syntheticFixture(1, 2, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	paper, err := NewHiddenPaper(testProtocol, PaperSpec{
		ExamID: "g1-file", Queue: 1, Kind: gauntlet.KindStandard, Family: "liquidation",
		AlphaSpend: 1, Stratum: regime("high"), ClusterIDs: []string{"e1"},
		Start: day(2024, 5, 1), End: day(2024, 6, 1), FixturePath: fixturePath, Nonce: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if paper.FixturePath != fixturePath || len(paper.Fixture) != 0 || paper.FixtureSize == 0 {
		t.Fatalf("file-backed paper unexpectedly embedded data: %+v", paper)
	}
	if err := verifyPaperData(paper); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, syntheticFixture(9, 9, 9), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPaperData(paper); err == nil {
		t.Fatal("mutating a hidden paper file must fail its committed DataHash")
	}
}

func TestHistoryGenerationManifestHashBindsExactTickets(t *testing.T) {
	tickets := examManifest(t).Commitments()
	want, err := CommitmentManifestHash(tickets)
	if err != nil {
		t.Fatal(err)
	}
	state, err := CreateGeneration(
		gauntlet.State{}, gauntlet.FixedAccess{}, "g1", tickets,
		map[string]int{"liquidation": 2}, day(2024, 1, 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Generations) != 1 || state.Generations[0].ManifestHash != want {
		t.Fatalf("generation did not bind exact ticket commitments: %+v", state)
	}
	mutated := append([]gauntlet.Ticket(nil), tickets...)
	mutated[0].DataHash = "different"
	got, err := CommitmentManifestHash(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("changing a ticket commitment must change manifest hash")
	}
}
