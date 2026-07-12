package personalplaybook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg6/eventmap"
)

// Runner orchestrates personal wild playbook derivation from reverse tapes.
type Runner struct {
	Config         RunConfig
	CheckpointPath string
	OnProgress     func(attempts, playbooks int)
}

// RunResult holds sealed personal playbook outputs.
type RunResult struct {
	Playbooks []PersonalWildPlaybook
	Discovery []PersonalWildPlaybook
	Ledger    ExperimentLedger
	Manifest  PlaybookManifest
	Summary   RunSummary
}

// Run executes bounded derivation with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}

	bundle, err := LoadInputBundle(r.Config)
	if err != nil {
		return RunResult{}, err
	}

	var resume *Checkpoint
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, bundle); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted()
		}
		resume = &cp
	}

	seeds := CollectCertifiedSeeds(bundle.CertifiedTapes)
	playbooks, discovery, ledger, err := Derive(seeds, bundle.DiscoveryTapes, r.Config.PlaybookLimit, resume)
	if err != nil {
		return RunResult{}, err
	}
	ledger.ConstitutionHash = bundle.ConstitutionHash
	ledger.ReverseTapesManifestHash = bundle.Manifest.ManifestHash
	ledger.ReverseTapesLedgerHash = bundle.Ledger.LedgerHash
	if sealed, err := SealLedger(ledger); err != nil {
		return RunResult{}, err
	} else {
		ledger = sealed
	}
	for i := range playbooks {
		playbooks[i].ExperimentHash = ledger.LedgerHash
	}
	for i := range discovery {
		discovery[i].ExperimentHash = ledger.LedgerHash
	}
	if r.OnProgress != nil {
		r.OnProgress(len(ledger.Attempts), certifiedCount(playbooks))
	}

	artifactHashes := map[string]string{}
	if err := WriteOutputs(r.Config.OutputDir, playbooks, discovery, ledger, PlaybookManifest{}, RunSummary{}); err != nil {
		return RunResult{}, err
	}
	for _, name := range []string{OutputPlaybooksFile, OutputDiscoveryFile, LedgerFileName} {
		if h, err := eventmap.HashFile(filepath.Join(r.Config.OutputDir, name)); err == nil {
			artifactHashes[name] = h
		}
	}

	manifest, err := BuildManifest(r.Config, bundle, playbooks, ledger, artifactHashes)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(bundle, playbooks, discovery, ledger, manifest, r.Config.PlaybookLimit)
	if err := WriteOutputs(r.Config.OutputDir, playbooks, discovery, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}

	playbooksHash, err := HashPlaybooks(playbooks)
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		HistoryDataHash:          r.Config.HistoryDataHash,
		ConfigHash:               manifest.ConfigHash,
		ReverseTapesManifestHash: bundle.Manifest.ManifestHash,
		ReverseTapesLedgerHash:   bundle.Ledger.LedgerHash,
		ConstitutionHash:         bundle.ConstitutionHash,
		OutputDir:                r.Config.OutputDir,
		NextWorkIndex:            len(seeds),
		PlaybookCount:            certifiedCount(playbooks),
		Playbooks:                playbooks,
		DiscoveryPlaybooks:       discovery,
		LedgerAttempts:           ledger.Attempts,
		PlaybookHash:             playbooksHash,
		LedgerHash:               ledger.LedgerHash,
		Done:                     true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		Playbooks: playbooks,
		Discovery: discovery,
		Ledger:    ledger,
		Manifest:  manifest,
		Summary:   summary,
	}, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	manifestPath := filepath.Join(r.Config.OutputDir, ManifestFileName)
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyOutputArtifacts(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, OutputPlaybooksFile))
	if err != nil {
		return RunResult{}, err
	}
	var payload struct {
		Playbooks []PersonalWildPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, LedgerFileName))
	if err != nil {
		return RunResult{}, err
	}
	var ledger ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, SummaryFileName))
	if err != nil {
		return RunResult{}, err
	}
	var summary RunSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		Playbooks: payload.Playbooks,
		Ledger:    ledger,
		Manifest:  manifest,
		Summary:   summary,
	}, nil
}
