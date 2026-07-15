// Package historydownload provides a fail-closed, read-only historical data
// downloader. It deliberately contains no venue adapter or execution imports.
package historydownload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	datatruthhistory "lingxi/internal/datatruth/history"
)

const (
	ContractHash          = "4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588"
	DefaultPrivateRawRoot = "/root/.lingxi-private/historydownload"
	DefaultPublicRawRoot  = "/root/.lingxi-data/historydownload"
)

const (
	InstrumentPITStatus = "SNAPSHOT_ONLY/PIT_UNPROVEN"
	ReportCenterStatus  = "MANUAL_EXPORT_UNVERIFIED"
	DownloaderVersion   = "historydownload.v3"
)

// Clock records the three distinct clocks required by the route data contract.
type Clock struct {
	EventTime   time.Time `json:"event_time,omitempty"`
	ReceiveTime time.Time `json:"receive_time"`
	ProcessTime time.Time `json:"process_time"`
}

type ClockRange struct {
	Min time.Time `json:"min,omitempty"`
	Max time.Time `json:"max,omitempty"`
}

// Request is intentionally narrow: callers cannot supply a URL, method, or body.
type Request struct {
	Endpoint string
	Query    map[string]string
	Private  bool
}

// Checkpoint is an integrity-bound, resumable cursor state.
type Checkpoint struct {
	SchemaVersion  string            `json:"schema_version"`
	Downloader     string            `json:"downloader_version"`
	ContractHash   string            `json:"contract_hash"`
	Endpoint       string            `json:"endpoint"`
	QueryHash      string            `json:"query_hash"`
	SchemaHash     string            `json:"schema_hash"`
	RawDirectory   string            `json:"raw_directory"`
	OutputDir      string            `json:"output_dir"`
	ManifestPath   string            `json:"manifest_path"`
	TokenSaltID    string            `json:"token_salt_id"`
	SemanticsHash  string            `json:"semantics_hash"`
	Private        bool              `json:"private"`
	DiskCapBytes   int64             `json:"disk_cap_bytes"`
	RootDirectory  string            `json:"root_directory"`
	RootCapBytes   int64             `json:"root_cap_bytes"`
	CursorToken    string            `json:"cursor_token,omitempty"`
	CursorTokens   []string          `json:"cursor_tokens,omitempty"`
	PageRawHashes  []string          `json:"page_raw_hashes,omitempty"`
	RecordKeys     []string          `json:"record_keys,omitempty"`
	Page           int               `json:"page"`
	RecordCount    int               `json:"record_count"`
	DuplicateCount int               `json:"duplicate_count"`
	MissingIDCount int               `json:"missing_identity_count"`
	BytesWritten   int64             `json:"bytes_written"`
	Retries        int               `json:"retries"`
	PresenceCounts map[string]int    `json:"presence_counts,omitempty"`
	OmittedCounts  map[string]int    `json:"omitted_counts,omitempty"`
	PublishState   string            `json:"publish_state"`
	ManifestHash   string            `json:"manifest_hash,omitempty"`
	EventTimeRange ClockRange        `json:"event_time_range"`
	ReceiveRange   ClockRange        `json:"receive_time_range"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Hash           *string           `json:"hash"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// Manifest contains provenance metadata only; it never has a raw identifier.
type Manifest struct {
	SchemaVersion        string            `json:"schema_version"`
	Downloader           string            `json:"downloader_version"`
	ContractHash         string            `json:"contract_hash"`
	Endpoint             string            `json:"endpoint"`
	Private              bool              `json:"private"`
	QueryHash            string            `json:"query_hash"`
	PageRawHashes        []string          `json:"page_raw_hashes"`
	ArtifactHashes       map[string]string `json:"artifact_hashes,omitempty"`
	CursorTokens         []string          `json:"cursor_tokens"`
	RecordCount          int               `json:"record_count"`
	DuplicateCount       int               `json:"duplicate_count"`
	MissingIDCount       int               `json:"missing_identity_count"`
	BytesWritten         int64             `json:"bytes_written"`
	ProjectionFields     []string          `json:"projection_fields"`
	ProjectionSchemaHash string            `json:"projection_schema_hash"`
	PresenceCounts       map[string]int    `json:"presence_counts"`
	OmittedCounts        map[string]int    `json:"omitted_counts,omitempty"`
	Clocks               Clock             `json:"clocks"`
	EventTimeRange       ClockRange        `json:"event_time_range"`
	ReceiveTimeRange     ClockRange        `json:"receive_time_range"`
	ClockFitness         string            `json:"clock_fitness"`
	DenominatorFitness   string            `json:"denominator_fitness"`
	Retries              int               `json:"retries"`
	Warnings             []string          `json:"warnings,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	CompletedAt          time.Time         `json:"completed_at"`
	PITStatus            string            `json:"pit_status"`
	ManifestHash         *string           `json:"manifest_hash"`
}

func hash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

type bindingSpec struct {
	Key       string
	Path      string
	SelfField []string
	Recompute bool
}

var frozenBindingSpecs = []bindingSpec{
	{"history_data_hash", "research/firstbattle/history_catalog.json", []string{"data_hash"}, false},
	{"forced_liquidation_g1_protocol_hash", "research/firstbattle/forced_liquidation_preregistration.json", []string{"audit", "protocol_hash"}, true},
	{"hierarchical_router_g1_manifest_hash", "research/firstbattle/hierarchical_router_preregistration.json", []string{"audit", "protocol_hash"}, true},
	{"g10_protocol_hash", "research/firstbattle/hierarchical_router_g10_preregistration.json", []string{"audit", "protocol_hash"}, true},
	{"g10_execution_evidence_audit_protocol_hash", "research/firstbattle/g10_execution_evidence_audit_preregistration.json", []string{"audit", "protocol_hash"}, true},
	{"g10_execution_evidence_audit_source_manifest_hash", "research/firstbattle/g10_execution_evidence_audit/source_manifest.json", []string{"manifest_hash"}, true},
	{"g10_execution_evidence_audit_summary_hash", "research/firstbattle/g10_execution_evidence_audit/audit_summary.json", []string{"artifact_hash"}, true},
	{"g10_execution_evidence_audit_stage_summary_hash", "research/firstbattle/g10_execution_evidence_audit_stage_summary.json", []string{"hashes", "stage_summary_hash"}, true},
	{"g10_wild_route_portfolio_json_hash", "research/firstbattle/g10_wild_route_portfolio.json", []string{"hashes", "portfolio_hash"}, true},
	{"g10_reopen_condition_inventory_json_hash", "research/firstbattle/g10_reopen_condition_inventory.json", []string{"hashes", "inventory_hash"}, true},
	{"g10_reopen_condition_inventory_source_manifest_hash", "research/firstbattle/g10_reopen_condition_inventory_source_manifest.json", []string{"hashes", "manifest_hash"}, true},
}

// ValidateContract verifies the frozen contract and every uniquely mapped
// upstream binding without modifying any artifact.
func ValidateContract(repoRoot, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(b, &document); err != nil {
		return fmt.Errorf("historydownload: invalid contract: %w", err)
	}
	hashes, ok := document["hashes"].(map[string]any)
	if !ok || hashes["protocol_hash"] != ContractHash {
		return errors.New("historydownload: frozen route-data contract hash mismatch")
	}
	hashes["protocol_hash"] = nil
	got, _, err := datatruth.HashJSON(document)
	if err != nil || got != ContractHash {
		return errors.New("historydownload: frozen route-data contract content drift")
	}
	common, ok := document["common"].(map[string]any)
	if !ok {
		return errors.New("historydownload: contract common section missing")
	}
	forbidden, ok := common["forbidden_outcome_fields"].([]any)
	if !ok || len(forbidden) == 0 {
		return errors.New("historydownload: contract forbidden outcome fields missing")
	}
	for _, rawField := range forbidden {
		field, ok := rawField.(string)
		if !ok || field == "" {
			return errors.New("historydownload: invalid forbidden outcome field")
		}
		for endpoint, schema := range projectionSchemas {
			for projected := range schema {
				if strings.EqualFold(field, projected) {
					return fmt.Errorf("historydownload: forbidden outcome field %q admitted by %s projection", field, endpoint)
				}
			}
		}
	}
	bindings, ok := document["bindings"].(map[string]any)
	if !ok {
		return errors.New("historydownload: contract bindings missing")
	}
	return validateFrozenBindings(repoRoot, bindings)
}

func validateFrozenBindings(repoRoot string, bindings map[string]any) error {
	if len(bindings)-1 != len(frozenBindingSpecs) {
		return errors.New("historydownload: binding coverage table mismatch")
	}
	for _, spec := range frozenBindingSpecs {
		expected, ok := bindings[spec.Key].(string)
		if !ok || expected == "" {
			return fmt.Errorf("historydownload: binding %s missing", spec.Key)
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(spec.Path)))
		if err != nil {
			return fmt.Errorf("historydownload: binding %s artifact unavailable: %w", spec.Key, err)
		}
		var artifact map[string]any
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return fmt.Errorf("historydownload: binding %s artifact invalid", spec.Key)
		}
		container := artifact
		for _, segment := range spec.SelfField[:len(spec.SelfField)-1] {
			next, ok := container[segment].(map[string]any)
			if !ok {
				return fmt.Errorf("historydownload: binding %s self-exclusion path missing", spec.Key)
			}
			container = next
		}
		field := spec.SelfField[len(spec.SelfField)-1]
		declared, ok := container[field].(string)
		if !ok || declared != expected {
			return fmt.Errorf("historydownload: binding %s declared hash mismatch", spec.Key)
		}
		if !spec.Recompute {
			var catalog datatruthhistory.HistoryCatalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				return fmt.Errorf("historydownload: binding %s catalog invalid", spec.Key)
			}
			if catalog.DataHash != expected {
				return fmt.Errorf("historydownload: binding %s catalog hash mismatch", spec.Key)
			}
			if err := datatruthhistory.ValidateCatalog(catalog); err != nil {
				return fmt.Errorf("historydownload: binding %s catalog consistency: %w", spec.Key, err)
			}
			continue
		}
		container[field] = nil
		computed, _, err := datatruth.HashJSON(artifact)
		if err != nil || computed != expected {
			return fmt.Errorf("historydownload: binding %s content hash mismatch", spec.Key)
		}
	}
	return nil
}

func ResolveRepository(contractPath string) (string, string, error) {
	canonicalContract, err := filepath.EvalSymlinks(contractPath)
	if err != nil {
		return "", "", err
	}
	canonicalContract, err = filepath.Abs(canonicalContract)
	if err != nil {
		return "", "", err
	}
	for candidate := filepath.Dir(canonicalContract); ; candidate = filepath.Dir(candidate) {
		goMod, readErr := os.ReadFile(filepath.Join(candidate, "go.mod"))
		_, gitErr := os.Stat(filepath.Join(candidate, ".git"))
		firstLine := strings.TrimSpace(strings.SplitN(string(goMod), "\n", 2)[0])
		if readErr == nil && gitErr == nil && firstLine == "module lingxi" {
			root, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", "", err
			}
			expected := filepath.Join(root, "research", "firstbattle", "route_data_contracts.json")
			expected, err = filepath.EvalSymlinks(expected)
			if err != nil || expected != canonicalContract {
				return "", "", errors.New("historydownload: contract is not canonical repository contract")
			}
			return root, canonicalContract, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return "", "", errors.New("historydownload: repository anchor not found")
}

func ValidateOutputRoot(candidate, repoRoot string) (string, error) {
	canonical, err := canonicalPotentialPath(candidate)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil {
		return "", err
	}
	if relative == "." || relative == "records" || strings.HasPrefix(relative, "records"+string(os.PathSeparator)) ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))) {
		return "", errors.New("historydownload: output root must be outside repository and records")
	}
	return canonical, nil
}

func canonicalPotentialPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	current := absolute
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("historydownload: no existing output-root ancestor")
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

// SaveCheckpoint atomically seals a resume state. Checkpoints are private.
func SaveCheckpoint(path string, cp Checkpoint) error {
	if cp.Endpoint == "" {
		return errors.New("historydownload: checkpoint endpoint required")
	}
	cp.SchemaVersion = "historydownload.checkpoint.v1"
	cp.Downloader = DownloaderVersion
	cp.ContractHash = ContractHash
	cp.UpdatedAt = time.Now().UTC()
	cp.Hash = nil
	h, err := hash(cp)
	if err != nil {
		return err
	}
	cp.Hash = &h
	return writeAtomic(path, cp, 0o600)
}

func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	actual := cp.Hash
	cp.Hash = nil
	want, err := hash(cp)
	if err != nil || actual == nil || *actual != want || cp.ContractHash != ContractHash || cp.Downloader != DownloaderVersion {
		return Checkpoint{}, false, errors.New("historydownload: checkpoint integrity or contract mismatch")
	}
	return cp, true, nil
}

// PublishManifest atomically writes a research-facing metadata receipt.
func PublishManifest(path string, m Manifest) error {
	sealed, err := SealManifest(m)
	if err != nil {
		return err
	}
	return writeAtomic(path, sealed, 0o644)
}

func SealManifest(m Manifest) (Manifest, error) {
	m.SchemaVersion = "historydownload.manifest.v1"
	m.Downloader = DownloaderVersion
	m.ContractHash = ContractHash
	m.ManifestHash = nil
	h, err := hash(m)
	if err != nil {
		return Manifest{}, err
	}
	m.ManifestHash = &h
	return m, nil
}

func VerifyManifest(m Manifest) error {
	actual := m.ManifestHash
	m.ManifestHash = nil
	want, err := hash(m)
	if err != nil || actual == nil || *actual != want || m.ContractHash != ContractHash || m.Downloader != DownloaderVersion {
		return errors.New("historydownload: manifest integrity or contract mismatch")
	}
	return nil
}

func writeAtomic(path string, v any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeAtomicBytes(path, b, mode)
}

func WriteJSONAtomic(path string, v any, mode os.FileMode) error {
	return writeAtomic(path, v, mode)
}

func writeAtomicBytes(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(b); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	published = true
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
