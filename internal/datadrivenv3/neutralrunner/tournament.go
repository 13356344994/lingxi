package neutralrunner

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
	"lingxi/internal/datadrivenv3/models/changepoint"
	"lingxi/internal/datadrivenv3/models/episodic"
	"lingxi/internal/datadrivenv3/models/moe"
	"lingxi/internal/datadrivenv3/models/tree"
	"lingxi/internal/datatruth"
)

var TournamentProtocolHashes = map[string]string{
	"g1":  "5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd",
	"g2":  "7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226",
	"g3":  "63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57",
	"g4":  "2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9",
	"g5":  "518d0003c829da641f1bcc955c4382023e158ba9938c0ac6f0ff19b5bf729385",
	"g6":  "08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b",
	"g7":  "4aacc43e928d25b6b83cdf8fe10d5b576e2b1009ea10b7bfad3e750388e47747",
	"g8":  "c492cd191e869073b330221835fcec4b4437b27ec67e6e9d18252119fc7da682",
	"g9":  "fe3ce29ef3b8b07da7ef81e0ea3ccc732722f8a184e7ba91ebb8edacd7b2802c",
	"g10": "0d09d595947f09aeb6258b9af023e5c87e2d334bc52649761f91cb2ae263e7cb",
}

type TournamentConfig struct {
	Root       string
	Output     string
	Limit      int
	Production bool
	Progress   io.Writer
}

type FitArtifact struct {
	OwnerID    string                  `json:"owner_id"`
	DurationNS int64                   `json:"duration_ns"`
	PolicyHash string                  `json:"policy_hash"`
	LedgerHash string                  `json:"attempt_ledger_hash"`
	Attempts   []modelapi.AttemptEntry `json:"attempt_ledger"`
	Policy     modelapi.Policy         `json:"-"`
}

type TournamentManifest struct {
	SchemaVersion       string                  `json:"schema_version"`
	Examples            int                     `json:"examples"`
	BundleHash          string                  `json:"bundle_sha256"`
	SampleHash          string                  `json:"sample_hash"`
	CodeHash            string                  `json:"code_hash"`
	ProtocolHashes      map[string]string       `json:"protocol_hashes"`
	G10ReviewerReceipt  string                  `json:"g10_reviewer_receipt_hash"`
	DatasetManifest     string                  `json:"dataset_manifest_hash"`
	DatasetReviewer     string                  `json:"dataset_reviewer_receipt_hash"`
	G9ReviewerReceipt   string                  `json:"g9_reviewer_receipt_hash"`
	SubmissionHashes    map[string]string       `json:"model_submission_hashes"`
	FundingManifest     string                  `json:"funding_manifest_hash"`
	FundingReceipt      string                  `json:"funding_receipt_hash"`
	Fits                []FitArtifact           `json:"fits"`
	SharedAttemptLedger []modelapi.AttemptEntry `json:"shared_attempt_ledger"`
	CreatedAt           string                  `json:"created_at"`
}

func RunTournament(ctx context.Context, cfg TournamentConfig) (TournamentManifest, error) {
	if err := validateTournamentConfig(cfg); err != nil {
		return TournamentManifest{}, err
	}
	start := time.Now()
	progress(cfg.Progress, "stage=manifest_and_discovery status=started")
	candidates, _, err := counterfactual.LoadDiscoveryCandidates(cfg.Root, cfg.Progress)
	if err != nil {
		return TournamentManifest{}, err
	}
	funding, fundingProvenance, err := counterfactual.LoadFunding(cfg.Root)
	if err != nil {
		return TournamentManifest{}, err
	}
	progress(cfg.Progress, "stage=manifest_and_discovery status=complete candidates=%d elapsed=%s", len(candidates), time.Since(start))

	barCache := map[string]map[int64]counterfactual.Bar{}
	selected := make([]counterfactual.Prepared, 0, cfg.Limit)
	for index, candidate := range candidates {
		if len(selected) == cfg.Limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return TournamentManifest{}, err
		}
		instrument := candidate.Row.Instrument
		bars := barCache[instrument]
		if bars == nil {
			bars, err = counterfactual.LoadBars(cfg.Root, instrument, candidate.Row.SourceVersion)
			if err != nil {
				return TournamentManifest{}, err
			}
			barCache[instrument] = bars
		}
		prepared, eligible, err := counterfactual.Prepare(candidate, bars, funding)
		if err != nil {
			return TournamentManifest{}, err
		}
		if eligible {
			selected = append(selected, prepared)
		}
		if (index+1)%10000 == 0 {
			progress(cfg.Progress, "stage=eligibility scanned=%d eligible_selected=%d elapsed=%s", index+1, len(selected), time.Since(start))
		}
	}
	if len(selected) != cfg.Limit {
		return TournamentManifest{}, fmt.Errorf("only %d eligible discovery examples available for limit %d", len(selected), cfg.Limit)
	}
	progress(cfg.Progress, "stage=eligibility status=complete selected=%d elapsed=%s", len(selected), time.Since(start))

	labels := make([]counterfactual.Label, len(selected))
	examples := make([]modelapi.TrainingExample, len(selected))
	for index, prepared := range selected {
		label, err := counterfactual.Evaluate(prepared)
		if err != nil {
			return TournamentManifest{}, err
		}
		labels[index], examples[index] = label, label.Example
		if (index+1)%100 == 0 {
			progress(cfg.Progress, "stage=counterfactual evaluated=%d/%d elapsed=%s", index+1, len(selected), time.Since(start))
		}
	}
	sampleHash, _, err := datatruth.HashJSON(examples)
	if err != nil {
		return TournamentManifest{}, err
	}

	parent := filepath.Dir(cfg.Output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return TournamentManifest{}, err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(cfg.Output)+".partial-*")
	if err != nil {
		return TournamentManifest{}, err
	}
	defer os.RemoveAll(staging)
	bundlePath := filepath.Join(staging, "discovery_label_bundle.jsonl.gz")
	if err := writeBundle(bundlePath, labels); err != nil {
		return TournamentManifest{}, err
	}
	bundleHash, err := hashFile(bundlePath)
	if err != nil {
		return TournamentManifest{}, err
	}
	progress(cfg.Progress, "stage=bundle status=staged hash=%s elapsed=%s", bundleHash, time.Since(start))

	sharedLedger := &modelapi.AttemptLedger{}
	fits, err := fitModels(ctx, examples, sampleHash, staging, sharedLedger, cfg.Progress)
	if err != nil {
		return TournamentManifest{}, err
	}
	submissionHashes, err := submissionHashes(cfg.Root)
	if err != nil {
		return TournamentManifest{}, err
	}
	codeHash, err := sourceCodeHash(cfg.Root)
	if err != nil {
		return TournamentManifest{}, err
	}
	manifest := TournamentManifest{
		SchemaVersion:       tournamentSchema(cfg.Production),
		Examples:            len(examples),
		BundleHash:          bundleHash,
		SampleHash:          sampleHash,
		CodeHash:            codeHash,
		ProtocolHashes:      TournamentProtocolHashes,
		G10ReviewerReceipt:  counterfactual.G10ReviewerHash,
		DatasetManifest:     counterfactual.DatasetManifestHash,
		DatasetReviewer:     counterfactual.DatasetReviewerHash,
		G9ReviewerReceipt:   "e77d1511d022fb511165f3412535ff81a6149cb5a0da7729044c5afb9069c953",
		SubmissionHashes:    submissionHashes,
		FundingManifest:     fundingProvenance.ManifestHash,
		FundingReceipt:      fundingProvenance.ReceiptHash,
		Fits:                fits,
		SharedAttemptLedger: sharedLedger.Entries(),
		CreatedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSON(filepath.Join(staging, "manifest.json"), manifest); err != nil {
		return TournamentManifest{}, err
	}
	if err := syncDir(staging); err != nil {
		return TournamentManifest{}, err
	}
	if err := publishAtomic(staging, cfg.Output); err != nil {
		return TournamentManifest{}, err
	}
	progress(cfg.Progress, "stage=publish status=complete output=%s elapsed=%s", cfg.Output, time.Since(start))
	return manifest, nil
}

func tournamentSchema(production bool) string {
	if production {
		return "lingxi.data-driven-router-v3.discovery-tournament.v1"
	}
	return "lingxi.data-driven-router-v3.canary-tournament.v1"
}

func validateTournamentConfig(cfg TournamentConfig) error {
	if cfg.Root == "" || cfg.Output == "" || cfg.Limit <= 0 {
		return errors.New("root, output, and positive limit required")
	}
	sealedRoot := filepath.Join(cfg.Root, "research/firstbattle/data_driven_router_v3/sealed")
	insideSealed, err := pathWithin(sealedRoot, cfg.Output)
	if err != nil {
		return err
	}
	if cfg.Production {
		if cfg.Limit != 100000 {
			return errors.New("production tournament requires limit 100000")
		}
		if !insideSealed {
			return errors.New("production output must be under the V3 sealed directory")
		}
		return nil
	}
	if insideSealed {
		return errors.New("canary refuses sealed production path")
	}
	return nil
}

func pathWithin(parent, child string) (bool, error) {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false, err
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false, err
	}
	return relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func publishAtomic(staging, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return errors.New("atomic output destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return syncDir(filepath.Dir(destination))
}

func fitModels(ctx context.Context, examples []modelapi.TrainingExample, sampleHash, staging string, shared *modelapi.AttemptLedger, out io.Writer) ([]FitArtifact, error) {
	type fitSpec struct {
		id     string
		model  modelapi.Model
		ledger func() []modelapi.AttemptEntry
	}
	treeModel := tree.New()
	moeModel := moe.New()
	episodicModel := episodic.New()
	changeModel := changepoint.New()
	specs := []fitSpec{
		{tree.OwnerID, treeModel, treeModel.AttemptLedger},
		{moe.OwnerID, moeModel, moeModel.Ledger},
		{episodic.OwnerID, episodicModel, episodicModel.AttemptLedger},
		{changepoint.OwnerID, changeModel, changeModel.Ledger},
	}
	fits := make([]FitArtifact, 0, len(specs))
	for _, spec := range specs {
		start := time.Now()
		if _, err := shared.Append("fit:"+spec.id, spec.id, "FIT_DISCOVERY", sampleHash); err != nil {
			return nil, err
		}
		policy, err := spec.model.Fit(ctx, examples)
		if err != nil {
			return nil, fmt.Errorf("fit %s: %w", spec.id, err)
		}
		duration := time.Since(start)
		policyRaw, err := json.Marshal(policy)
		if err != nil {
			return nil, err
		}
		attempts := spec.ledger()
		ledgerHash, _, err := datatruth.HashJSON(attempts)
		if err != nil {
			return nil, err
		}
		artifact := FitArtifact{OwnerID: spec.id, DurationNS: duration.Nanoseconds(), PolicyHash: hashBytes(policyRaw), LedgerHash: ledgerHash, Attempts: attempts, Policy: policy}
		if err := writeJSON(filepath.Join(staging, "policies", spec.id+".json"), policy); err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(staging, "attempt_ledgers", spec.id+".json"), attempts); err != nil {
			return nil, err
		}
		fits = append(fits, artifact)
		progress(out, "stage=fit owner=%s duration=%s policy_hash=%s attempts=%d", spec.id, duration, artifact.PolicyHash, len(attempts))
	}
	if err := writeJSON(filepath.Join(staging, "shared_attempt_ledger.json"), shared.Entries()); err != nil {
		return nil, err
	}
	return fits, nil
}

func writeBundle(path string, labels []counterfactual.Label) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zr := gzip.NewWriter(file)
	encoder := json.NewEncoder(zr)
	for _, label := range labels {
		row := struct {
			SchemaVersion       string                `json:"schema_version"`
			ExampleID           string                `json:"example_id"`
			MarketStateHash     string                `json:"market_state_hash"`
			AccountContextHash  string                `json:"account_context_hash"`
			SelectedTarget      modelapi.TargetIntent `json:"selected_target_intent"`
			TargetSelectionHash string                `json:"target_selection_hash"`
		}{
			"lingxi.data-driven-router-v3.discovery-label-bundle.v1",
			label.Example.ExampleID,
			label.MarketStateHash,
			label.AccountContextHash,
			label.Example.SyntheticTarget,
			label.TargetSelectionHash,
		}
		if err := encoder.Encode(row); err != nil {
			zr.Close()
			file.Close()
			return err
		}
	}
	if err := zr.Close(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func submissionHashes(root string) (map[string]string, error) {
	paths := map[string]string{
		"terra":  "model_submission_terra.json",
		"opus":   "model_submission_opus.json",
		"sonnet": "model_submission_episodic_sonnet.json",
		"grok":   "model_submission_grok.json",
	}
	out := make(map[string]string, len(paths))
	for owner, name := range paths {
		hash, err := hashFile(filepath.Join(root, "research/firstbattle/data_driven_router_v3", name))
		if err != nil {
			return nil, err
		}
		out[owner] = hash
	}
	return out, nil
}

func sourceCodeHash(root string) (string, error) {
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
		err = filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".go") {
				paths = append(paths, path)
			}
			return err
		})
		if err != nil {
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

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func hashFile(path string) (string, error) {
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

func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func progress(out io.Writer, format string, args ...any) {
	if out != nil {
		fmt.Fprintf(out, format+"\n", args...)
	}
}
