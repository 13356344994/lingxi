package executionpolicy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

// CanonicalPolicyHash hashes policy with hash field cleared.
func CanonicalPolicyHash(fp FrozenPolicy) (string, error) {
	payload := fp
	payload.ExecutionPolicyHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateFrozenPolicy checks sealed policy integrity.
func ValidateFrozenPolicy(fp FrozenPolicy) error {
	if fp.SchemaVersion != SchemaVersion {
		return fmt.Errorf("executionpolicy: schema drift")
	}
	if !fp.FreezeBeforeOutcomeKnown {
		return fmt.Errorf("executionpolicy: must freeze before outcome known")
	}
	if err := ValidateActionsCompared(fp.ActionsCompared); err != nil {
		return err
	}
	if !fp.NoLongLivedMaker || !fp.NoChaseAfterMiss {
		return fmt.Errorf("executionpolicy: no_long_lived_maker/no_chase_after_miss required")
	}
	want, err := CanonicalPolicyHash(fp)
	if err != nil {
		return err
	}
	if want != fp.ExecutionPolicyHash {
		return fmt.Errorf("executionpolicy: hash mismatch")
	}
	return nil
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

// Runner executes bounded execution-policy generation.
type Runner struct {
	Config         RunConfig
	CheckpointPath string
	OnProgress     func(lines int)
}

// Run ingests triggers and writes frozen policies plus manifest.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	cfg := r.Config
	if cfg.SchemaVersion == "" {
		cfg.SchemaVersion = ConfigSchema
	}
	if cfg.Generation == "" {
		cfg.Generation = Generation
	}
	if cfg.MaxLines <= 0 {
		return RunResult{}, errors.New("executionpolicy: max_lines must be positive")
	}
	if cfg.Sources.Triggers == "" {
		return RunResult{}, errors.New("executionpolicy: triggers source required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}

	configHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return RunResult{}, err
	}

	triggers, err := loadTriggers(cfg.Sources.Triggers, cfg.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	if r.OnProgress != nil {
		r.OnProgress(len(triggers))
	}

	policies := make([]FrozenPolicy, 0, len(triggers))
	for _, tr := range triggers {
		fp, err := FreezePolicy(tr, triggers)
		if err != nil {
			return RunResult{}, err
		}
		policies = append(policies, fp)
	}

	manifest, err := BuildManifest(cfg, policies)
	if err != nil {
		return RunResult{}, err
	}

	outPath := filepath.Join(cfg.OutputDir, OutputPoliciesFile)
	if err := writePolicies(outPath, policies); err != nil {
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

	if r.CheckpointPath != "" {
		cp := Checkpoint{
			SchemaVersion: CheckpointSchemaVersion,
			ConfigHash:    configHash,
			OutputDir:     cfg.OutputDir,
			PolicyCount:   len(policies),
			ManifestHash:  manifest.ManifestHash,
			UpdatedAt:     time.Now().UTC(),
		}
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
	}

	return RunResult{
		Policies:   policies,
		Manifest:   manifest,
		OutputPath: outPath,
	}, nil
}

func loadTriggers(path string, maxLines int) ([]TriggerRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []TriggerRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if maxLines > 0 && len(out) >= maxLines {
			break
		}
		var tr TriggerRecord
		if err := json.Unmarshal(sc.Bytes(), &tr); err != nil {
			return nil, fmt.Errorf("executionpolicy: decode trigger: %w", err)
		}
		out = append(out, tr)
	}
	return out, sc.Err()
}

func writePolicies(path string, policies []FrozenPolicy) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, fp := range policies {
		if err := enc.Encode(fp); err != nil {
			return err
		}
	}
	return nil
}

// Checkpoint records resume state.
type Checkpoint struct {
	SchemaVersion string    `json:"schema_version"`
	ConfigHash    string    `json:"config_hash"`
	OutputDir     string    `json:"output_dir"`
	PolicyCount   int       `json:"policy_count"`
	ManifestHash  string    `json:"manifest_hash"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SaveCheckpoint writes checkpoint.
func SaveCheckpoint(path string, cp Checkpoint) error {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// BuildManifest seals policy outputs.
func BuildManifest(cfg RunConfig, policies []FrozenPolicy) (Manifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return Manifest{}, err
	}
	policiesHash, err := HashPolicies(policies)
	if err != nil {
		return Manifest{}, err
	}
	forbid := 0
	for _, fp := range policies {
		if fp.MakerGate.ForbidMaker {
			forbid++
		}
	}
	m := Manifest{
		SchemaVersion:          ManifestSchemaVersion,
		Generation:             cfg.Generation,
		ConfigHash:             cfgHash,
		PolicyCount:            len(policies),
		AllTriggersDenominator: len(policies),
		MakerGateForbidCount:   forbid,
		PoliciesHash:           policiesHash,
		CreatedAt:              time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return Manifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m Manifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashPolicies canonicalizes policy list hash.
func HashPolicies(policies []FrozenPolicy) (string, error) {
	payload := make([]FrozenPolicy, len(policies))
	for i, fp := range policies {
		payload[i] = fp
		payload[i].ExecutionPolicyHash = ""
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("executionpolicy: manifest schema drift")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if want != m.ManifestHash {
		return fmt.Errorf("executionpolicy: manifest_hash mismatch")
	}
	if m.AllTriggersDenominator != m.PolicyCount {
		return fmt.Errorf("executionpolicy: all-triggers denominator drift")
	}
	return nil
}
