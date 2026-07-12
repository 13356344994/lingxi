package announcements

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// Manifest seals announcement tape and event chain bindings.
type Manifest struct {
	SchemaVersion    string              `json:"schema_version"`
	Generation       string              `json:"generation"`
	ProtocolHash     string              `json:"protocol_hash"`
	HistoryDataHash  string              `json:"history_data_hash"`
	ConfigHash       string              `json:"config_hash"`
	TapeHash         string              `json:"tape_hash"`
	ChainHash        string              `json:"chain_hash"`
	CoverageHash     string              `json:"coverage_hash"`
	Coverage         CoverageSummary     `json:"coverage"`
	ExecutionVenue   string              `json:"execution_venue"`
	Incomplete       bool                `json:"incomplete"`
	SourceStatus     map[string]SourceStatus `json:"source_status"`
	CreatedAt        time.Time           `json:"created_at"`
	ManifestHash     string              `json:"manifest_hash"`
}

// BuildManifest seals outputs before downstream G10 consumption.
func BuildManifest(cfg RunConfig, tape AnnouncementTape, chain EventChain) (Manifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return Manifest{}, err
	}
	covHash, err := hashCoverage(tape.Coverage)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		SchemaVersion:   ManifestSchemaVersion,
		Generation:      g10protocol.Generation,
		ProtocolHash:    g10protocol.RouterProtocolHash,
		HistoryDataHash: g10protocol.HistoryDataHash,
		ConfigHash:      cfgHash,
		TapeHash:        tape.TapeHash,
		ChainHash:       chain.ChainHash,
		CoverageHash:    covHash,
		Coverage:        tape.Coverage,
		ExecutionVenue:  ExecutionVenue,
		Incomplete:      tape.Incomplete,
		SourceStatus:    cloneSourceStatus(tape.SourceStatus),
		CreatedAt:       time.Now().UTC(),
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

func hashCoverage(c CoverageSummary) (string, error) {
	hash, _, err := datatruth.HashJSON(c)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return errors.New("announcements: manifest schema mismatch")
	}
	if m.Generation != g10protocol.Generation || m.ProtocolHash != g10protocol.RouterProtocolHash {
		return errors.New("announcements: manifest protocol mismatch")
	}
	if m.ExecutionVenue != ExecutionVenue {
		return errors.New("announcements: execution_venue must be OKX")
	}
	wantCov, err := hashCoverage(m.Coverage)
	if err != nil || wantCov != m.CoverageHash {
		return errors.New("announcements: manifest coverage hash mismatch")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errors.New("announcements: manifest hash mismatch (tamper/drift)")
	}
	return nil
}

// LoadManifest reads and verifies g10_announcement_manifest.json.
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func cloneSourceStatus(in map[string]SourceStatus) map[string]SourceStatus {
	out := make(map[string]SourceStatus, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteOutputs atomically writes tape, chain, and manifest artifacts.
func WriteOutputs(outputDir string, tape AnnouncementTape, chain EventChain, manifest Manifest) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	tapePath := filepath.Join(outputDir, OutputAnnouncementsFile)
	if err := writeTapeJSONL(tapePath, tape); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputChainFile), chain); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputManifestFile), manifest); err != nil {
		return err
	}
	return nil
}

func writeTapeJSONL(path string, tape AnnouncementTape) error {
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	items := append([]Announcement(nil), tape.Announcements...)
	sortAnnouncements(items)
	for _, a := range items {
		if err := enc.Encode(a); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// HashTape seals announcement rows.
func HashTape(items []Announcement) (string, error) {
	copyItems := append([]Announcement(nil), items...)
	sortAnnouncements(copyItems)
	hash, _, err := datatruth.HashJSON(copyItems)
	return hash, err
}

// ConfigFingerprint hashes bounded CLI knobs.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

// ShouldFailClosed reports incomplete archives that must not backfill history.
func ShouldFailClosed(sourceStatus map[string]SourceStatus) bool {
	for _, st := range sourceStatus {
		if st.FetchFailed || st.Status == StatusIncomplete {
			return true
		}
		if st.Truncated {
			return true
		}
		if st.PagesExpected > 0 && st.PagesComplete < st.PagesExpected {
			return true
		}
	}
	return false
}

// VerifyOutputArtifacts checks on-disk hashes against manifest.
func VerifyOutputArtifacts(outputDir string, manifest Manifest) error {
	chainPath := filepath.Join(outputDir, OutputChainFile)
	raw, err := os.ReadFile(chainPath)
	if err != nil {
		return fmt.Errorf("chain artifact: %w", err)
	}
	var chain EventChain
	if err := json.Unmarshal(raw, &chain); err != nil {
		return err
	}
	if chain.ChainHash != manifest.ChainHash {
		return errors.New("announcements: chain artifact hash mismatch")
	}
	if chain.ExecutionVenue != ExecutionVenue {
		return errors.New("announcements: chain execution venue mismatch")
	}
	return nil
}
