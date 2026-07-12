package personalplaybook

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/reversetapes"
)

// LoadInputBundle reads and verifies reverse-tapes outputs.
func LoadInputBundle(cfg RunConfig) (InputBundle, error) {
	manifestPath := filepath.Join(cfg.ReverseTapesDir, reversetapes.ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return InputBundle{}, fmt.Errorf("personalplaybook: manifest: %w", err)
	}
	var manifest reversetapes.TapeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return InputBundle{}, err
	}
	if err := reversetapes.ValidateManifest(manifest); err != nil {
		return InputBundle{}, fmt.Errorf("personalplaybook: manifest invalid: %w", err)
	}
	if manifest.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return InputBundle{}, errors.New("personalplaybook: reverse tapes protocol mismatch")
	}
	if cfg.ReverseTapesManifestHash != "" && manifest.ManifestHash != cfg.ReverseTapesManifestHash {
		return InputBundle{}, errors.New(RejectManifestMismatch)
	}

	ledgerPath := filepath.Join(cfg.ReverseTapesDir, reversetapes.LedgerFileName)
	raw, err = os.ReadFile(ledgerPath)
	if err != nil {
		return InputBundle{}, fmt.Errorf("personalplaybook: ledger: %w", err)
	}
	var ledger reversetapes.ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return InputBundle{}, err
	}
	if err := reversetapes.ValidateLedger(ledger); err != nil {
		return InputBundle{}, fmt.Errorf("personalplaybook: ledger invalid: %w", err)
	}
	if ledger.LedgerHash != manifest.LedgerHash {
		return InputBundle{}, errors.New("personalplaybook: reverse tapes ledger/manifest mismatch")
	}

	constitutionHash, err := exclusionmap.ConstitutionHash()
	if err != nil {
		return InputBundle{}, err
	}
	if manifest.ConstitutionHash != constitutionHash {
		return InputBundle{}, errors.New("personalplaybook: constitution hash mismatch")
	}

	certified, err := loadTapes(filepath.Join(cfg.ReverseTapesDir, reversetapes.OutputTapesFile))
	if err != nil {
		return InputBundle{}, fmt.Errorf("personalplaybook: certified tapes: %w", err)
	}
	discovery, err := loadTapes(filepath.Join(cfg.ReverseTapesDir, reversetapes.OutputDiscoveryFile))
	if err != nil {
		if !os.IsNotExist(err) {
			return InputBundle{}, fmt.Errorf("personalplaybook: discovery tapes: %w", err)
		}
		discovery = nil
	}

	for i, tape := range certified {
		if err := validateCertifiedTape(tape); err != nil {
			return InputBundle{}, fmt.Errorf("personalplaybook: certified tape %d: %w", i, err)
		}
	}
	for i, tape := range discovery {
		if err := reversetapes.ValidateTape(tape); err != nil {
			return InputBundle{}, fmt.Errorf("personalplaybook: discovery tape %d: %w", i, err)
		}
		if tape.Status != reversetapes.TapeStatusDiscoveryOnly {
			return InputBundle{}, fmt.Errorf("personalplaybook: discovery tape %d status %q", i, tape.Status)
		}
	}

	return InputBundle{
		Manifest:         manifest,
		Ledger:           ledger,
		CertifiedTapes:   certified,
		DiscoveryTapes:   discovery,
		ConstitutionHash: constitutionHash,
	}, nil
}

func loadTapes(path string) ([]reversetapes.ExecutableProfitTape, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []reversetapes.ExecutableProfitTape
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var tape reversetapes.ExecutableProfitTape
		if err := json.Unmarshal(sc.Bytes(), &tape); err != nil {
			return nil, err
		}
		out = append(out, tape)
	}
	return out, sc.Err()
}

func validateCertifiedTape(tape reversetapes.ExecutableProfitTape) error {
	if err := reversetapes.ValidateTape(tape); err != nil {
		return err
	}
	if tape.Status != reversetapes.TapeStatusExecutableCertified {
		return fmt.Errorf("%s: status %q", RejectNotExecutableCertified, tape.Status)
	}
	if tape.ProofBinding.ConstitutionHash == "" ||
		tape.ProofBinding.ExclusionProofHash == "" ||
		tape.ProofBinding.EntryProofHash == "" ||
		tape.ProofBinding.ExecutionPolicyHash == "" ||
		tape.ProofBinding.ExitProofHash == "" ||
		tape.ProofBinding.AnnouncementTapeHash == "" {
		return errors.New(RejectProofBindingIncomplete)
	}
	if tape.DecisionBefore.FrozenExecutionAction == "SKIP" {
		return errors.New(RejectNotExecutableCertified + ": skip frozen action")
	}
	return nil
}

// CollectCertifiedSeeds schedules certified tapes with fair family round-robin.
func CollectCertifiedSeeds(tapes []reversetapes.ExecutableProfitTape) []TapeSeed {
	if len(tapes) == 0 {
		return nil
	}
	byFamily := map[string][]reversetapes.ExecutableProfitTape{}
	for _, tape := range tapes {
		family := tape.PersonalTapeFamily
		if family == "" {
			family = tape.EventFamily
		}
		byFamily[family] = append(byFamily[family], tape)
	}
	families := sortedKeys(byFamily)
	maxLen := 0
	for _, bucket := range byFamily {
		if len(bucket) > maxLen {
			maxLen = len(bucket)
		}
	}
	var seeds []TapeSeed
	for round := 0; round < maxLen; round++ {
		for fi, family := range families {
			bucket := byFamily[family]
			if round >= len(bucket) {
				continue
			}
			seeds = append(seeds, TapeSeed{Tape: bucket[round], Priority: fi})
		}
	}
	return seeds
}

func (c RunConfig) Validate() error {
	if c.ReverseTapesDir == "" || c.OutputDir == "" {
		return errors.New("personalplaybook: reverse-tapes-dir and output-dir required")
	}
	if c.PlaybookLimit <= 0 || c.PlaybookLimit > MaxPlaybookCandidates {
		return fmt.Errorf("personalplaybook: playbook_limit must be in 1..%d", MaxPlaybookCandidates)
	}
	if c.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("personalplaybook: protocol hash mismatch")
	}
	return nil
}

// ConfigFingerprint hashes run config for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}
