package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/gauntlet"
	"lingxi/internal/historyexam"
)

func writePlan(t *testing.T, dir string) string {
	t.Helper()
	d := func(m, day int) time.Time { return time.Date(2024, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	plan := planInput{
		Generation:   "g1",
		ProtocolHash: historyexam.SyntheticTestProtocolHash,
		EmbargoHours: 24,
		Clusters: []clusterInput{
			{ClusterID: "r1", Axes: map[string]string{"volatility": "high", "funding_state": "positive"}, Start: d(1, 1), End: d(2, 1), Partition: "research"},
			{ClusterID: "w1", Axes: map[string]string{"volatility": "low", "funding_state": "positive"}, Start: d(3, 1), End: d(4, 1), Partition: "walk_forward"},
			{ClusterID: "e1", Axes: map[string]string{"volatility": "high", "funding_state": "positive"}, Start: d(5, 1), End: d(6, 1), Partition: "exam"},
			{ClusterID: "e2", Axes: map[string]string{"volatility": "low", "funding_state": "positive"}, Start: d(7, 1), End: d(8, 1), Partition: "exam"},
		},
		Papers: []paperInput{
			{ExamID: "g1-standard", Queue: 1, Kind: "standard", Family: "liquidation", AlphaSpend: 1, ClusterIDs: []string{"e1"}, Nonce: "n1", Observations: []float64{2, 2, 2}},
			{ExamID: "g1-golden", Queue: 2, Kind: "golden", Family: "liquidation", AlphaSpend: 1, ClusterIDs: []string{"e2"}, Nonce: "n2", Observations: []float64{5, 5, 5}},
		},
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureRun(t *testing.T, args ...string) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := run(args)
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("run %v failed: %v", args, runErr)
	}
	return string(out)
}

func TestCLISealPrintsOnlyPublicCommitments(t *testing.T) {
	dir := t.TempDir()
	planPath := writePlan(t, dir)
	hiddenPath := filepath.Join(dir, "hidden", "manifest.json")
	statePath := filepath.Join(dir, "state.json")

	out := captureRun(t, "seal", "--plan", planPath, "--hidden-manifest", hiddenPath, "--state", statePath)

	assertNoHiddenLeak(t, out, dir, hiddenPath)
	var tickets []gauntlet.Ticket
	if err := json.Unmarshal([]byte(out), &tickets); err != nil {
		t.Fatalf("seal must print public tickets json: %v (%s)", err, out)
	}
	if len(tickets) != 2 || tickets[0].ChallengeHash == "" || tickets[0].DataHash == "" {
		t.Fatalf("expected two public commitments with hashes: %+v", tickets)
	}

	info, err := os.Stat(hiddenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("hidden manifest must be 0600, got %o", info.Mode().Perm())
	}
}

func TestCLIFullGradingWorkflow(t *testing.T) {
	dir := t.TempDir()
	planPath := writePlan(t, dir)
	hiddenPath := filepath.Join(dir, "hidden", "manifest.json")
	statePath := filepath.Join(dir, "state.json")
	t.Setenv("LINGXI_GAUNTLET_TEST_HMAC_KEY", "0123456789abcdef0123456789abcdef")

	_ = captureRun(t, "seal", "--plan", planPath, "--hidden-manifest", hiddenPath, "--state", statePath)

	// Unseal + grade the standard paper (mean 2 >= 1.5 -> pass).
	_ = captureRun(t, "unseal-next", "--state", statePath, "--generation", "g1")
	gradeOut := captureRun(t, "grade", "--state", statePath, "--hidden-manifest", hiddenPath, "--generation", "g1")
	if !strings.Contains(gradeOut, `"pass":true`) || !strings.Contains(gradeOut, `"reason":"PASS"`) {
		t.Fatalf("standard paper should pass with a signed result: %s", gradeOut)
	}
	assertNoHiddenLeak(t, gradeOut, dir, hiddenPath)

	// Unseal + grade the golden paper.
	_ = captureRun(t, "unseal-next", "--state", statePath, "--generation", "g1")
	goldenOut := captureRun(t, "grade", "--state", statePath, "--hidden-manifest", hiddenPath, "--generation", "g1")
	if !strings.Contains(goldenOut, `"pass":true`) {
		t.Fatalf("golden paper should pass: %s", goldenOut)
	}

	// Researcher-facing result command prints only the signed result.
	resultOut := captureRun(t, "result", "--state", statePath, "--generation", "g1")
	if !strings.Contains(resultOut, `"signature"`) {
		t.Fatalf("result must expose the signed receipt: %s", resultOut)
	}
	assertNoHiddenLeak(t, resultOut, dir, hiddenPath)
}

func TestCLIGradeDoesNotAcceptRuntimeThresholds(t *testing.T) {
	err := run([]string{"grade", "--min-mean", "0"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("runtime grading thresholds must be impossible, got %v", err)
	}
}

func TestCLICrashConsumesRunningPaper(t *testing.T) {
	dir := t.TempDir()
	planPath := writePlan(t, dir)
	hiddenPath := filepath.Join(dir, "hidden", "manifest.json")
	statePath := filepath.Join(dir, "state.json")
	t.Setenv("LINGXI_GAUNTLET_TEST_HMAC_KEY", "0123456789abcdef0123456789abcdef")

	_ = captureRun(t, "seal", "--plan", planPath, "--hidden-manifest", hiddenPath, "--state", statePath)
	_ = captureRun(t, "unseal-next", "--state", statePath, "--generation", "g1")
	crashOut := captureRun(t, "crash", "--state", statePath, "--generation", "g1")
	if !strings.Contains(crashOut, `"reason":"CRASH"`) {
		t.Fatalf("crash must return coarse CRASH reason: %s", crashOut)
	}
	assertNoHiddenLeak(t, crashOut, dir, hiddenPath)

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "consumed_crashed") {
		t.Fatalf("state must record consumed_crashed: %s", raw)
	}
}

func assertNoHiddenLeak(t *testing.T, out, dir, hiddenPath string) {
	t.Helper()
	for _, forbidden := range []string{"fixture", "observations", "stratum", "axes", "cluster", "volatility", "funding_state", hiddenPath, dir, "2024"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(forbidden)) {
			t.Fatalf("output leaked hidden detail %q: %s", forbidden, out)
		}
	}
}
