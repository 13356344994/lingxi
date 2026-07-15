package pathvalidation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// WFConfirmationEntry is the WF-confirmation-only projection of one path
// verdict, kept as its own atomic artifact so a reviewer can audit the "one
// leaf_wf confirmation, no threshold retuning" step in isolation.
type WFConfirmationEntry struct {
	Owner             string     `json:"owner"`
	LeafID            string     `json:"leaf_id"`
	ValidationVerdict Verdict    `json:"validation_verdict"`
	WFAttempted       bool       `json:"wf_attempted"`
	WFPassed          bool       `json:"wf_passed"`
	WFReport          GateReport `json:"wf_report"`
	InactivityFailure bool       `json:"inactivity_failure"`
}

// HashManifest binds every atomic output file to a single, independently
// recomputable hash tree.
type HashManifest struct {
	SchemaVersion       string            `json:"schema_version"`
	ReceiptHash         string            `json:"receipt_json_sha256"`
	OwnerPathsHash      string            `json:"owner_path_verdicts_json_sha256"`
	WFConfirmationHash  string            `json:"wf_confirmation_json_sha256"`
	SealedBundleHash    string            `json:"sealed_bundle_byte_hash"`
	SealedManifestHash  string            `json:"sealed_manifest_byte_hash"`
	TournamentCodeHash  string            `json:"tournament_code_hash"`
	PolicyHashes        map[string]string `json:"policy_hashes"`
	LedgerHashes        map[string]string `json:"attempt_ledger_hashes"`
	SharedLedgerHash    string            `json:"shared_attempt_ledger_hash"`
	ProtocolHashes      map[string]string `json:"protocol_hashes"`
	SealedManifestPath  string            `json:"sealed_manifest_path"`
	DatasetManifestHash string            `json:"dataset_manifest_hash"`
}

// WriteAtomicReport writes the four required artifacts (validation receipt,
// per-owner path verdicts, WF confirmation, hashes) under a fresh, atomically
// published output directory. It refuses to overwrite an existing directory.
func WriteAtomicReport(outputDir string, report Report) error {
	if _, err := os.Stat(outputDir); err == nil {
		return errors.New("atomic output destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(outputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(outputDir)+".partial-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	receiptView := report
	receiptView.Owners = nil
	receiptRaw, err := writeJSON(filepath.Join(staging, "validation_receipt.json"), receiptView)
	if err != nil {
		return err
	}

	ownerPathsRaw, err := writeJSON(filepath.Join(staging, "owner_path_verdicts.json"), report.Owners)
	if err != nil {
		return err
	}

	wf := make([]WFConfirmationEntry, 0)
	for _, owner := range report.Owners {
		for _, p := range owner.Paths {
			if !p.WFAttempted {
				continue
			}
			wf = append(wf, WFConfirmationEntry{
				Owner: p.Owner, LeafID: p.LeafID, ValidationVerdict: p.ValidationVerdict,
				WFAttempted: p.WFAttempted, WFPassed: p.WFPassed, WFReport: p.WFReport,
				InactivityFailure: p.InactivityFailure,
			})
		}
	}
	wfRaw, err := writeJSON(filepath.Join(staging, "wf_confirmation.json"), wf)
	if err != nil {
		return err
	}

	hashes := HashManifest{
		SchemaVersion:       "lingxi.data-driven-router-v3.path-validation-hashes.v1",
		ReceiptHash:         hashBytes(receiptRaw),
		OwnerPathsHash:      hashBytes(ownerPathsRaw),
		WFConfirmationHash:  hashBytes(wfRaw),
		SealedBundleHash:    report.SealedBundleByteHash,
		SealedManifestHash:  report.SealedManifestByteHash,
		TournamentCodeHash:  report.TournamentCodeHash,
		PolicyHashes:        report.PolicyHashes,
		LedgerHashes:        report.LedgerHashes,
		SharedLedgerHash:    report.SharedLedgerHash,
		ProtocolHashes:      report.ProtocolHashes,
		SealedManifestPath:  report.SealedManifestPath,
		DatasetManifestHash: report.DatasetManifestHash,
	}
	if _, err := writeJSON(filepath.Join(staging, "hashes.json"), hashes); err != nil {
		return err
	}

	if err := syncDir(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, outputDir); err != nil {
		return err
	}
	return syncDir(filepath.Dir(outputDir))
}

// writeJSON returns exactly the bytes written to disk (including the
// trailing newline) so callers hashing the returned value always agree with
// hashing the file afterward.
func writeJSON(path string, value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	return raw, nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
