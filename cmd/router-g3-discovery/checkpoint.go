package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg3/protocol"
)

func configFingerprint(cfg runConfig) (string, error) {
	payload := struct {
		SchemaVersion         string  `json:"schema_version"`
		MaxLines              int     `json:"max_lines"`
		MatchWindowMS         int64   `json:"match_window_ms"`
		NeutralBandBPS        float64 `json:"neutral_band_bps"`
		PermutationDraws      int     `json:"permutation_draws"`
		BootstrapSamples      int     `json:"bootstrap_samples"`
		ExactPermutationMax   int     `json:"exact_permutation_max"`
		Seed                  int64   `json:"seed"`
		ReverseInductionAlpha float64 `json:"reverse_induction_alpha"`
		RecheckAlpha          float64 `json:"recheck_alpha"`
		Generation            string  `json:"generation"`
		ProtocolHash          string  `json:"protocol_hash"`
	}{
		SchemaVersion: checkpointSchemaVersion, MaxLines: cfg.MaxLines,
		MatchWindowMS: cfg.MatchWindowMS, NeutralBandBPS: cfg.NeutralBandBPS,
		PermutationDraws: cfg.PermutationDraws, BootstrapSamples: cfg.BootstrapSamples,
		ExactPermutationMax: cfg.ExactPermutationMax, Seed: cfg.Seed,
		ReverseInductionAlpha: cfg.ReverseInductionAlpha, RecheckAlpha: cfg.RecheckAlpha,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func inputFingerprint(inductionHash, recheckHash, rulesHash, ledgerHash, searchManifestHash string) (string, error) {
	payload := struct {
		InductionEventHash string `json:"induction_event_hash"`
		RecheckEventHash   string `json:"recheck_event_hash"`
		RulesHash          string `json:"rules_hash"`
		LedgerHash         string `json:"ledger_hash"`
		SearchManifestHash string `json:"search_manifest_hash"`
	}{
		InductionEventHash: inductionHash, RecheckEventHash: recheckHash,
		RulesHash: rulesHash, LedgerHash: ledgerHash, SearchManifestHash: searchManifestHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func loadCheckpoint(path string) (checkpointFile, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkpointFile{}, false, nil
		}
		return checkpointFile{}, false, err
	}
	var cp checkpointFile
	if err := json.Unmarshal(raw, &cp); err != nil {
		return checkpointFile{}, false, err
	}
	if cp.SchemaVersion != checkpointSchemaVersion {
		return checkpointFile{}, false, errors.New("router-g3-discovery: checkpoint schema mismatch")
	}
	want := cp.CheckpointHash
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return checkpointFile{}, false, err
	}
	if want == "" || hash != want {
		return checkpointFile{}, false, errors.New("router-g3-discovery: checkpoint hash mismatch")
	}
	cp.CheckpointHash = want
	return cp, true, nil
}

func saveCheckpoint(path string, cp checkpointFile) error {
	if path == "" {
		return errors.New("router-g3-discovery: checkpoint path is required")
	}
	cp.SchemaVersion = checkpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
