// Package pathvalidation is the independent GPT-5.6 Sol validation owner for
// the data-driven-router-v3 discovery tournament. It never decodes
// discovery_label_bundle.jsonl.gz content (only its raw byte hash), never
// opens router_holdout, and never refits or retunes any owner model. It
// consumes only the sealed tournament manifest, the four owner policy
// artifacts, and the dataset's validation/leaf_wf shards, raw bars, and
// verified funding.
package pathvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/fundingarchive"
	"lingxi/internal/datadrivenv3/modelapi"
	"lingxi/internal/datadrivenv3/neutralrunner"
	"lingxi/internal/datatruth"
)

const (
	SealedTournamentDir = "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000"
	RequiredExamples    = 100000
)

// OwnerIDs is the frozen, ordered set of tournament model owners. It is
// intentionally distinct from the independent GPT-5.6 Sol validation owner.
var OwnerIDs = []string{"terra", "opus", "sonnet", "grok"}

// protocolChain extends datadrivenv3.ProtocolChain (g1-g5) with g6-g10 so a
// validation run confirms every protocol overlay hash the manifest depends
// on, not just the ones already checked elsewhere in the repository.
var protocolChain = []struct {
	path, field, expected string
}{
	{"research/firstbattle/data_driven_router_v3/protocol.json", "protocol_hash", neutralrunner.TournamentProtocolHashes["g1"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g2.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g2"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g3.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g3"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g4.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g4"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g5.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g5"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g6.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g6"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g7.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g7"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g8.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g8"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g9.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g9"]},
	{"research/firstbattle/data_driven_router_v3/protocol_g10.json", "overlay_hash", neutralrunner.TournamentProtocolHashes["g10"]},
}

// TournamentManifest mirrors neutralrunner.TournamentManifest's on-wire shape
// so this package can decode the sealed manifest.json without importing a
// type meant for tournament production.
type TournamentManifest struct {
	SchemaVersion      string                  `json:"schema_version"`
	Examples           int                     `json:"examples"`
	BundleHash         string                  `json:"bundle_sha256"`
	SampleHash         string                  `json:"sample_hash"`
	CodeHash           string                  `json:"code_hash"`
	ProtocolHashes     map[string]string       `json:"protocol_hashes"`
	G10ReviewerReceipt string                  `json:"g10_reviewer_receipt_hash"`
	DatasetManifest    string                  `json:"dataset_manifest_hash"`
	DatasetReviewer    string                  `json:"dataset_reviewer_receipt_hash"`
	G9ReviewerReceipt  string                  `json:"g9_reviewer_receipt_hash"`
	SubmissionHashes   map[string]string       `json:"model_submission_hashes"`
	FundingManifest    string                  `json:"funding_manifest_hash"`
	FundingReceipt     string                  `json:"funding_receipt_hash"`
	Fits               []FitArtifact           `json:"fits"`
	SharedLedger       []modelapi.AttemptEntry `json:"shared_attempt_ledger"`
	CreatedAt          string                  `json:"created_at"`
}

type FitArtifact struct {
	OwnerID    string                  `json:"owner_id"`
	DurationNS int64                   `json:"duration_ns"`
	PolicyHash string                  `json:"policy_hash"`
	LedgerHash string                  `json:"attempt_ledger_hash"`
	Attempts   []modelapi.AttemptEntry `json:"attempt_ledger"`
}

// SealedBinding is the confirmed, hash-checked view of the sealed tournament
// used by every later validation stage. Its presence is the only proof this
// run ever needs that the 100k discovery tournament, its four policies, and
// the frozen g1-g10 protocol chain are bound together and untampered.
type SealedBinding struct {
	Root             string
	Manifest         TournamentManifest
	ManifestPath     string
	Policies         map[string]modelapi.Policy
	PolicyRaw        map[string]json.RawMessage
	BundleByteHash   string
	ManifestByteHash string
	PolicyHashes     map[string]string
	LedgerHashes     map[string]string
	SharedLedgerHash string
	VerifiedChain    map[string]string
	FundingProof     fundingarchive.Provenance
	DatasetManifest  counterfactual.DatasetManifest
}

// VerifySealed recomputes every hash the sealed 100k discovery tournament
// depends on: the manifest's own bytes, each of the four owner policy
// artifacts, each attempt ledger, the discovery label bundle's raw byte
// hash (never its decoded content), the dataset manifest hash, the verified
// funding manifest/receipt hashes, the four model_submission_*.json file
// hashes, and the full g1-g10 protocol overlay chain. It fails closed on any
// mismatch and never reads discovery_label_bundle.jsonl.gz content or the
// router_holdout partition.
func VerifySealed(root string) (SealedBinding, error) {
	dir := filepath.Join(root, SealedTournamentDir)
	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return SealedBinding{}, err
	}
	var manifest TournamentManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return SealedBinding{}, err
	}
	if manifest.SchemaVersion != "lingxi.data-driven-router-v3.discovery-tournament.v1" {
		return SealedBinding{}, fmt.Errorf("unexpected sealed tournament schema %q", manifest.SchemaVersion)
	}
	if manifest.Examples != RequiredExamples {
		return SealedBinding{}, fmt.Errorf("sealed tournament examples=%d, required %d", manifest.Examples, RequiredExamples)
	}
	manifestByteHash := hashBytes(raw)
	codeHash, err := tournamentSourceCodeHash(root)
	if err != nil {
		return SealedBinding{}, err
	}
	if codeHash != manifest.CodeHash {
		return SealedBinding{}, fmt.Errorf("tournament code hash mismatch: declared=%s computed=%s", manifest.CodeHash, codeHash)
	}

	// 1) Bundle byte-level hash only: the discovery label bundle is never
	// gunzipped or decoded by this package, only checksummed as opaque bytes.
	bundlePath := filepath.Join(dir, "discovery_label_bundle.jsonl.gz")
	bundleHash, err := hashFileBytes(bundlePath)
	if err != nil {
		return SealedBinding{}, err
	}
	if bundleHash != manifest.BundleHash {
		return SealedBinding{}, fmt.Errorf("bundle_sha256 mismatch: declared=%s computed=%s", manifest.BundleHash, bundleHash)
	}

	// 2) g1-g10 protocol overlay chain.
	verifiedChain := make(map[string]string, len(protocolChain))
	for _, link := range protocolChain {
		if link.expected == "" {
			return SealedBinding{}, fmt.Errorf("protocol chain entry %s has no frozen expected hash", link.path)
		}
		got, err := verifyHashBoundDocument(filepath.Join(root, link.path), link.field, link.expected)
		if err != nil {
			return SealedBinding{}, fmt.Errorf("protocol chain %s: %w", link.path, err)
		}
		verifiedChain[link.path] = got
	}
	for _, generation := range []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7", "g8", "g9", "g10"} {
		declared, ok := manifest.ProtocolHashes[generation]
		if !ok || declared == "" {
			return SealedBinding{}, fmt.Errorf("sealed manifest missing protocol hash for %s", generation)
		}
		if declared != neutralrunner.TournamentProtocolHashes[generation] {
			return SealedBinding{}, fmt.Errorf("sealed manifest protocol hash for %s does not match frozen chain", generation)
		}
	}
	if manifest.DatasetManifest != counterfactual.DatasetManifestHash {
		return SealedBinding{}, errors.New("sealed manifest dataset_manifest_hash does not match frozen dataset binding")
	}
	if manifest.DatasetReviewer != counterfactual.DatasetReviewerHash {
		return SealedBinding{}, errors.New("sealed manifest dataset_reviewer_receipt_hash does not match frozen dataset binding")
	}
	if manifest.G10ReviewerReceipt != counterfactual.G10ReviewerHash {
		return SealedBinding{}, errors.New("sealed manifest g10_reviewer_receipt_hash does not match frozen g10 binding")
	}

	// 3) Dataset manifest hash (shard hashes are re-verified lazily by the
	// validation/leaf_wf-only dataset loader, never here).
	datasetManifest, err := verifyDatasetManifestHash(root)
	if err != nil {
		return SealedBinding{}, err
	}

	// 4) Verified funding manifest/receipt binding (raw funding, no labels).
	_, fundingProof, err := fundingarchive.LoadVerifiedSource(root)
	if err != nil {
		return SealedBinding{}, err
	}
	if fundingProof.ManifestHash != manifest.FundingManifest || fundingProof.ReceiptHash != manifest.FundingReceipt {
		return SealedBinding{}, errors.New("verified funding manifest/receipt hash does not match sealed tournament manifest")
	}

	// 5) Four model_submission_*.json file hashes.
	submissionFiles := map[string]string{
		"terra":  "model_submission_terra.json",
		"opus":   "model_submission_opus.json",
		"sonnet": "model_submission_episodic_sonnet.json",
		"grok":   "model_submission_grok.json",
	}
	for owner, name := range submissionFiles {
		hash, err := hashFileBytes(filepath.Join(root, "research/firstbattle/data_driven_router_v3", name))
		if err != nil {
			return SealedBinding{}, err
		}
		declared, ok := manifest.SubmissionHashes[owner]
		if !ok || declared != hash {
			return SealedBinding{}, fmt.Errorf("model_submission hash mismatch for owner %s", owner)
		}
	}

	// 6) Each owner's policy artifact + attempt ledger, both re-hashed from
	// the on-disk sealed files and cross-checked against manifest.fits.
	fitsByOwner := make(map[string]FitArtifact, len(manifest.Fits))
	for _, fit := range manifest.Fits {
		fitsByOwner[fit.OwnerID] = fit
	}
	policies := make(map[string]modelapi.Policy, len(OwnerIDs))
	policyRaw := make(map[string]json.RawMessage, len(OwnerIDs))
	policyHashes := make(map[string]string, len(OwnerIDs))
	ledgerHashes := make(map[string]string, len(OwnerIDs))
	for _, owner := range OwnerIDs {
		fit, ok := fitsByOwner[owner]
		if !ok {
			return SealedBinding{}, fmt.Errorf("sealed manifest missing fit artifact for owner %s", owner)
		}
		policyPath := filepath.Join(dir, "policies", owner+".json")
		policyBytes, err := os.ReadFile(policyPath)
		if err != nil {
			return SealedBinding{}, err
		}
		var policy modelapi.Policy
		if err := json.Unmarshal(policyBytes, &policy); err != nil {
			return SealedBinding{}, err
		}
		if policy.SchemaVersion != modelapi.SchemaVersion {
			return SealedBinding{}, fmt.Errorf("policy %s: unexpected schema %q", owner, policy.SchemaVersion)
		}
		// The tournament recorded policy_hash as hashBytes(json.Marshal(policy))
		// on the in-memory Policy object before writing the indented file, so a
		// compact re-marshal of the decoded struct reproduces the same bytes.
		compact, err := json.Marshal(policy)
		if err != nil {
			return SealedBinding{}, err
		}
		gotHash := hashBytes(compact)
		if gotHash != fit.PolicyHash {
			return SealedBinding{}, fmt.Errorf("policy hash mismatch for owner %s: declared=%s computed=%s", owner, fit.PolicyHash, gotHash)
		}
		policies[owner] = policy
		policyRaw[owner] = json.RawMessage(policyBytes)
		policyHashes[owner] = gotHash

		ledgerPath := filepath.Join(dir, "attempt_ledgers", owner+".json")
		ledgerBytes, err := os.ReadFile(ledgerPath)
		if err != nil {
			return SealedBinding{}, err
		}
		var ledger []modelapi.AttemptEntry
		if err := json.Unmarshal(ledgerBytes, &ledger); err != nil {
			return SealedBinding{}, err
		}
		ledgerHash, _, err := datatruth.HashJSON(ledger)
		if err != nil {
			return SealedBinding{}, err
		}
		if ledgerHash != fit.LedgerHash {
			return SealedBinding{}, fmt.Errorf("attempt ledger hash mismatch for owner %s: declared=%s computed=%s", owner, fit.LedgerHash, ledgerHash)
		}
		if err := verifyLedgerChain(ledger); err != nil {
			return SealedBinding{}, fmt.Errorf("attempt ledger chain broken for owner %s: %w", owner, err)
		}
		ledgerHashes[owner] = ledgerHash
	}

	sharedLedgerBytes, err := os.ReadFile(filepath.Join(dir, "shared_attempt_ledger.json"))
	if err != nil {
		return SealedBinding{}, err
	}
	var sharedLedger []modelapi.AttemptEntry
	if err := json.Unmarshal(sharedLedgerBytes, &sharedLedger); err != nil {
		return SealedBinding{}, err
	}
	sharedLedgerHash, _, err := datatruth.HashJSON(sharedLedger)
	if err != nil {
		return SealedBinding{}, err
	}
	manifestSharedLedgerHash, _, err := datatruth.HashJSON(manifest.SharedLedger)
	if err != nil {
		return SealedBinding{}, err
	}
	if sharedLedgerHash != manifestSharedLedgerHash {
		return SealedBinding{}, errors.New("shared attempt ledger does not match sealed manifest")
	}
	if err := verifyLedgerChain(sharedLedger); err != nil {
		return SealedBinding{}, fmt.Errorf("shared attempt ledger chain broken: %w", err)
	}

	return SealedBinding{
		Root:             root,
		Manifest:         manifest,
		ManifestPath:     manifestPath,
		Policies:         policies,
		PolicyRaw:        policyRaw,
		BundleByteHash:   bundleHash,
		ManifestByteHash: manifestByteHash,
		PolicyHashes:     policyHashes,
		LedgerHashes:     ledgerHashes,
		SharedLedgerHash: sharedLedgerHash,
		VerifiedChain:    verifiedChain,
		FundingProof:     fundingProof,
		DatasetManifest:  datasetManifest,
	}, nil
}

func tournamentSourceCodeHash(root string) (string, error) {
	var paths []string
	for _, relative := range []string{
		"internal/datadrivenv3/counterfactual",
		"internal/datadrivenv3/neutralrunner",
		"internal/datadrivenv3/accountstate",
		"internal/datadrivenv3/reward_evaluator.go",
		"cmd/data-driven-v3-tournament",
	} {
		path := filepath.Join(root, relative)
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			paths = append(paths, path)
			continue
		}
		if err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".go") {
				paths = append(paths, path)
			}
			return err
		}); err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	hasher := sha256.New()
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hasher, "%s\x00%s\x00", filepath.ToSlash(relative), hashBytes(raw))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func verifyLedgerChain(entries []modelapi.AttemptEntry) error {
	previous := ""
	for i, entry := range entries {
		if entry.Sequence != uint64(i+1) {
			return fmt.Errorf("sequence out of order at index %d", i)
		}
		if entry.PreviousHash != previous {
			return fmt.Errorf("previous_hash break at sequence %d", entry.Sequence)
		}
		previous = entry.EntryHash
	}
	return nil
}

// verifyDatasetManifestHash re-checks the same generic-object hash binding
// counterfactual.LoadDiscoveryCandidates uses, without loading any shard.
func verifyDatasetManifestHash(root string) (counterfactual.DatasetManifest, error) {
	path := filepath.Join(root, "research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3/dataset_manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return counterfactual.DatasetManifest{}, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return counterfactual.DatasetManifest{}, err
	}
	declared, _ := generic["manifest_hash"].(string)
	delete(generic, "manifest_hash")
	computed, _, err := datatruth.HashJSON(generic)
	if err != nil {
		return counterfactual.DatasetManifest{}, err
	}
	if declared != counterfactual.DatasetManifestHash || computed != declared {
		return counterfactual.DatasetManifest{}, errors.New("hash-bound V3 dataset manifest mismatch")
	}
	var manifest counterfactual.DatasetManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return counterfactual.DatasetManifest{}, err
	}
	return manifest, nil
}

// verifyHashBoundDocument implements the repository-standard "set the hash
// field to null, then compute datatruth.HashJSON" verification method used
// by every protocol/overlay artifact in this generation.
func verifyHashBoundDocument(path, field, expected string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	hashes, ok := doc["hashes"].(map[string]any)
	if !ok {
		return "", errors.New("missing hashes object")
	}
	declared, _ := hashes[field].(string)
	hashes[field] = nil
	computed, _, err := datatruth.HashJSON(doc)
	if err != nil {
		return "", err
	}
	if declared != expected || computed != expected {
		return "", fmt.Errorf("declared=%s computed=%s expected=%s", declared, computed, expected)
	}
	return computed, nil
}

func hashFileBytes(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SortedOwnerIDs returns OwnerIDs in a stable, deterministic order.
func SortedOwnerIDs() []string {
	out := append([]string(nil), OwnerIDs...)
	sort.Strings(out)
	return out
}
