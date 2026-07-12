package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/routerg5/protocol"
)

func TestCLIVerifyExamUnavailableWithoutCommitments(t *testing.T) {
	if err := run([]string{"verify"}); err != nil {
		t.Fatalf("verify without commitments should succeed with EXAM_UNAVAILABLE: %v", err)
	}
}

func TestCLIVerifySyntheticCommitments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "public_commitments.json")
	doc := map[string]any{
		"schema_version": protocol.PublicCommitmentSchemaV1,
		"generation":     protocol.Generation,
		"protocol_hash":  protocol.RouterProtocolHash,
		"commitments": []map[string]any{
			{"id": "g5-1", "queue": 1, "kind": "standard", "family": "high_volatility", "alpha_spend": 1, "data_hash": "d1", "challenge_hash": "c1", "protocol_hash": protocol.RouterProtocolHash, "status": "sealed"},
			{"id": "g5-2", "queue": 2, "kind": "standard", "family": "low_volatility_funding", "alpha_spend": 1, "data_hash": "d2", "challenge_hash": "c2", "protocol_hash": protocol.RouterProtocolHash, "status": "sealed"},
			{"id": "g5-3", "queue": 3, "kind": "standard", "family": "cross_sectional_breadth", "alpha_spend": 1, "data_hash": "d3", "challenge_hash": "c3", "protocol_hash": protocol.RouterProtocolHash, "status": "sealed"},
			{"id": "g5-4", "queue": 4, "kind": "standard", "family": "delay_stress", "alpha_spend": 1, "data_hash": "d4", "challenge_hash": "c4", "protocol_hash": protocol.RouterProtocolHash, "status": "sealed"},
			{"id": "g5-5", "queue": 5, "kind": "golden", "family": "golden", "alpha_spend": 1, "data_hash": "d5", "challenge_hash": "c5", "protocol_hash": protocol.RouterProtocolHash, "status": "sealed"},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--commitments", path}); err != nil {
		t.Fatalf("verify synthetic: %v", err)
	}
}

func TestCLIRejectsHiddenManifestPath(t *testing.T) {
	if err := run([]string{"verify", "--commitments", "/tmp/hidden-manifest.json"}); err == nil {
		t.Fatal("hidden manifest path must be rejected")
	}
}
