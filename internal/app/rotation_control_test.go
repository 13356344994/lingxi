package app

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRotationControlStateRoundTripActiveOnly(t *testing.T) {
	dir := t.TempDir()
	if err := saveRotationControlState(dir, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(rotationControlFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["live"]; ok {
		t.Fatalf("control file must not persist live: %s", string(data))
	}

	st, ok, err := loadRotationControlState(dir)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !st.Active {
		t.Fatal("active should round-trip true")
	}
}

func TestRotationControlStateIgnoresLegacyLive(t *testing.T) {
	dir := t.TempDir()
	path := rotationControlFilePath(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"active":false,"live":true,"updated_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	r := testUnifiedRotationRunnerWithFakes(t)
	r.recordDir = dir
	r.InitState(true)

	if r.active.Load() {
		t.Fatal("legacy live=true must not override active=false from file")
	}
}
