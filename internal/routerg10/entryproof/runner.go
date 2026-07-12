package entryproof

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Runner executes bounded entry-proof generation with optional checkpoint resume.
type Runner struct {
	Config         RunConfig
	CheckpointPath string
	OnProgress     func(lines int)
}

// Run ingests trigger fixtures and writes proofs plus manifest.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	cfg := r.Config
	if cfg.SchemaVersion == "" {
		cfg.SchemaVersion = ConfigSchema
	}
	if cfg.Generation == "" {
		cfg.Generation = Generation
	}
	if cfg.MaxLines <= 0 {
		return RunResult{}, errors.New("entryproof: max_lines must be positive")
	}
	if stringsTrim(cfg.Sources.Triggers) == "" {
		return RunResult{}, errors.New("entryproof: triggers source required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}

	configHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return RunResult{}, err
	}

	skip := 0
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if cp.ConfigHash != configHash || cp.OutputDir != cfg.OutputDir {
			return RunResult{}, errors.New("entryproof: checkpoint binding mismatch")
		}
		skip = cp.LinesProcessed
	}

	proofs, lines, err := ingestTriggers(cfg.Sources.Triggers, cfg.MaxLines, skip)
	if err != nil {
		return RunResult{}, err
	}
	if r.OnProgress != nil {
		r.OnProgress(lines)
	}

	manifest, err := BuildManifest(cfg, proofs, map[string]string{
		"triggers": "AVAILABLE",
	})
	if err != nil {
		return RunResult{}, err
	}

	outPath := filepath.Join(cfg.OutputDir, OutputProofsFile)
	if err := writeProofs(outPath, proofs); err != nil {
		return RunResult{}, err
	}
	manifestPath := filepath.Join(cfg.OutputDir, OutputManifestFile)
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return RunResult{}, err
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return RunResult{}, err
	}

	cp := Checkpoint{
		SchemaVersion:   CheckpointSchemaVersion,
		ConfigHash:      configHash,
		OutputDir:       cfg.OutputDir,
		LinesProcessed:  skip + lines,
		ProofCount:      len(proofs),
		ManifestHash:    manifest.ManifestHash,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}

	return RunResult{
		Proofs:     proofs,
		Manifest:   manifest,
		OutputPath: outPath,
	}, nil
}

func ingestTriggers(path string, maxLines, skip int) ([]EntryProof, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var proofs []EntryProof
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lines := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		lines++
		if lines <= skip {
			continue
		}
		if maxLines > 0 && len(proofs) >= maxLines {
			break
		}
		var tr TriggerRecord
		if err := json.Unmarshal(line, &tr); err != nil {
			return nil, lines, fmt.Errorf("entryproof: decode trigger: %w", err)
		}
		ep, err := BuildEntryProof(tr)
		if err != nil {
			return nil, lines, err
		}
		proofs = append(proofs, ep)
	}
	if err := sc.Err(); err != nil {
		return nil, lines, err
	}
	return proofs, lines, nil
}

func writeProofs(path string, proofs []EntryProof) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ep := range proofs {
		if err := enc.Encode(ep); err != nil {
			return err
		}
	}
	return nil
}

func stringsTrim(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' {
			return s
		}
	}
	return ""
}
