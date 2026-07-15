package neutralrunner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicOutputPublishesCompleteDirectoryAndRefusesReplacement(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, ".partial")
	destination := filepath.Join(root, "canary")
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := publishAtomic(staging, destination); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(destination, "manifest.json")); err != nil || string(raw) != "complete" {
		t.Fatalf("published output mismatch: %q %v", raw, err)
	}
	second := filepath.Join(root, ".partial-2")
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishAtomic(second, destination); err == nil {
		t.Fatal("existing atomic output was replaced")
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("failed publication removed staging evidence")
	}
}

func TestProductionPathAndLimitGuards(t *testing.T) {
	root := t.TempDir()
	sealedOutput := filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000")
	canaryOutput := filepath.Join(root, "research/firstbattle/data_driven_router_v3/canary/tournament-1000")
	tests := []struct {
		name    string
		config  TournamentConfig
		wantErr bool
	}{
		{"production_valid", TournamentConfig{Root: root, Output: sealedOutput, Limit: 100000, Production: true}, false},
		{"production_wrong_limit", TournamentConfig{Root: root, Output: sealedOutput, Limit: 1000, Production: true}, true},
		{"production_outside_sealed", TournamentConfig{Root: root, Output: canaryOutput, Limit: 100000, Production: true}, true},
		{"production_sealed_root_itself", TournamentConfig{Root: root, Output: filepath.Dir(sealedOutput), Limit: 100000, Production: true}, true},
		{"canary_inside_sealed", TournamentConfig{Root: root, Output: sealedOutput, Limit: 1000}, true},
		{"canary_valid", TournamentConfig{Root: root, Output: canaryOutput, Limit: 1000}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTournamentConfig(test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
	if got := tournamentSchema(false); got != "lingxi.data-driven-router-v3.canary-tournament.v1" {
		t.Fatalf("canary schema=%q", got)
	}
	if got := tournamentSchema(true); got != "lingxi.data-driven-router-v3.discovery-tournament.v1" {
		t.Fatalf("production schema=%q", got)
	}
}
