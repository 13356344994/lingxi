package g2prize

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/protocol"
	"lingxi/internal/routerg2/sky"
)

// SchemaVersion identifies this package's event manifest schema. It is a
// distinct string from g1's "lingxi.router-history-events.v1": g2 is a new
// generation with its own manifest identity, never a silent reuse of g1's.
const SchemaVersion = "lingxi.router-g2-prize-events.v1"

// GeneratorID identifies the deterministic event generator that produced a
// manifest's events.
const GeneratorID = "g2_prize_funding_flip_v2"

// Manifest is the signed, tamper-evident receipt binding one deterministic
// g2prize.BuildEvents output to the exact canonical HistoryCatalog and
// selected-file identities it was derived from. Its structure and
// verification contract deliberately mirror g1's
// routerdiscovery/historyevents.Manifest — canonical catalog binding,
// selected-file derivation hash, sealed manifest hash, HMAC signature — so
// the same tamper-detection guarantees apply to g2, without this package
// importing or modifying the frozen g1 implementation.
type Manifest struct {
	SchemaVersion     string `json:"schema_version"`
	GeneratorID       string `json:"generator_id"`
	Generation        string `json:"generation"`
	ProtocolHash      string `json:"protocol_hash"`
	SourceDataHash    string `json:"source_data_hash"`
	SourceSchemaHash  string `json:"source_schema_hash"`
	SourceDatasetID   string `json:"source_dataset_id"`
	DerivationHash    string `json:"derivation_inputs_hash"`
	DiscoveryDataHash string `json:"discovery_data_hash"`
	EventCount        int    `json:"event_count"`
	MaxSymbols        int    `json:"max_symbols"`
	MaxFundingLines   int    `json:"max_funding_lines"`
	MaxPriceLines     int    `json:"max_price_lines"`
	ConfigHash        string `json:"config_hash"`
	SkyCutpointsHash  string `json:"sky_cutpoints_hash"`
	ManifestHash      string `json:"manifest_hash"`
	KeyID             string `json:"key_id"`
	Signature         string `json:"signature"`
}

// NewManifest seals a manifest binding events to catalog and the frozen
// generator configuration. generation/protocolHash are opaque caller-owned
// identities (g2's protocol freeze is tracked outside this package); both
// must be non-empty so a manifest can never silently claim an unbound
// generation.
func NewManifest(events []Event, catalog historydata.HistoryCatalog, cutpoints sky.Cutpoints, generation, protocolHash string, maxSymbols, maxFundingLines, maxPriceLines int) (Manifest, error) {
	if maxSymbols <= 0 || maxFundingLines <= 0 || maxPriceLines <= 0 {
		return Manifest{}, errors.New("g2prize: bounded manifest limits are required")
	}
	if strings.TrimSpace(generation) == "" || strings.TrimSpace(protocolHash) == "" {
		return Manifest{}, errors.New("g2prize: manifest generation and protocol hash are required")
	}
	discoveryHash, err := HashEvents(events)
	if err != nil {
		return Manifest{}, err
	}
	if catalog.DataHash == "" || catalog.SchemaHash == "" || catalog.DatasetID == "" {
		return Manifest{}, errors.New("g2prize: verified history catalog is required")
	}
	derivationHash, err := SelectedFileHash(catalog, maxSymbols)
	if err != nil {
		return Manifest{}, err
	}
	if !cutpoints.Frozen {
		return Manifest{}, errors.New("g2prize: frozen sky cutpoints are required")
	}
	cutpointsHash, err := cutpoints.Hash()
	if err != nil {
		return Manifest{}, err
	}
	configHash, err := frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines, cutpointsHash)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, GeneratorID: GeneratorID,
		Generation: generation, ProtocolHash: protocolHash,
		SourceDataHash: catalog.DataHash, SourceSchemaHash: catalog.SchemaHash,
		SourceDatasetID: catalog.DatasetID, DerivationHash: derivationHash,
		DiscoveryDataHash: discoveryHash,
		EventCount:        len(events), MaxSymbols: maxSymbols, MaxFundingLines: maxFundingLines,
		MaxPriceLines: maxPriceLines, ConfigHash: configHash, SkyCutpointsHash: cutpointsHash,
	}
	return SealManifest(manifest)
}

func frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines int, cutpointsHash string) (string, error) {
	config := struct {
		GeneratorID            string   `json:"generator_id"`
		MaxSymbols             int      `json:"max_symbols"`
		MaxFundingLines        int      `json:"max_funding_lines"`
		MaxPriceLines          int      `json:"max_price_lines"`
		CostBPS                float64  `json:"cost_bps"`
		SkyCutpointsHash       string   `json:"sky_cutpoints_hash"`
		MinQuoteVolume         float64  `json:"min_quote_volume"`
		MinVolatilityCostRatio float64  `json:"min_volatility_cost_ratio"`
		Horizons               []string `json:"horizons"`
	}{
		GeneratorID: GeneratorID, MaxSymbols: maxSymbols,
		MaxFundingLines: maxFundingLines, MaxPriceLines: maxPriceLines,
		CostBPS:                protocol.DiscoveryCostBPS,
		SkyCutpointsHash:       cutpointsHash,
		MinQuoteVolume:         protocol.SorterMinQuoteVolume,
		MinVolatilityCostRatio: protocol.SorterMinVolatilityCostRatio,
		Horizons:               []string{string(Horizon1h), string(Horizon4h), string(Horizon24h)},
	}
	hash, _, err := datatruth.HashJSON(config)
	return hash, err
}

// SealManifest stamps manifest with its content hash, clearing any prior
// key/signature/hash so the sealed value only ever reflects manifest's
// substantive fields.
func SealManifest(manifest Manifest) (Manifest, error) {
	manifest.KeyID = ""
	manifest.Signature = ""
	manifest.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.ManifestHash = hash
	return manifest, nil
}

// SignManifest HMAC-signs a sealed manifest's hash with key, identified by
// keyID. The key must be at least 32 bytes: short keys are refused rather
// than silently accepted.
func SignManifest(manifest Manifest, key []byte, keyID string) (Manifest, error) {
	if len(key) < 32 || strings.TrimSpace(keyID) == "" {
		return Manifest{}, errors.New("g2prize: signing key >=32 bytes and key id are required")
	}
	sealed, err := SealManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(sealed.ManifestHash))
	sealed.KeyID = strings.TrimSpace(keyID)
	sealed.Signature = hex.EncodeToString(mac.Sum(nil))
	return sealed, nil
}

// VerifyManifestSignature fails closed unless manifest is internally
// consistent (Validate) and carries a signature that verifies against key.
func VerifyManifestSignature(manifest Manifest, key []byte) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if len(key) < 32 || manifest.KeyID == "" || manifest.Signature == "" {
		return errors.New("g2prize: signed event manifest is required")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(manifest.ManifestHash))
	expected, err := hex.DecodeString(manifest.Signature)
	if err != nil || !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("g2prize: event manifest signature mismatch")
	}
	return nil
}

// Validate checks internal consistency only: schema/generator identity,
// required commitments present, bounded limits match the frozen generator
// config, and the manifest hash is self-consistent. It does not check the
// manifest's generation/protocol hash against any global registry — g2's
// protocol freeze is tracked and enforced by callers (e.g. the CLI), not by
// this package.
func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion || manifest.GeneratorID != GeneratorID ||
		strings.TrimSpace(manifest.Generation) == "" || strings.TrimSpace(manifest.ProtocolHash) == "" ||
		manifest.SourceDataHash == "" || manifest.SourceSchemaHash == "" || manifest.SourceDatasetID == "" ||
		manifest.DerivationHash == "" || manifest.DiscoveryDataHash == "" ||
		manifest.EventCount <= 0 || manifest.ConfigHash == "" || manifest.SkyCutpointsHash == "" {
		return errors.New("g2prize: event manifest is missing required commitments")
	}
	if manifest.MaxSymbols <= 0 || manifest.MaxFundingLines <= 0 || manifest.MaxPriceLines <= 0 {
		return errors.New("g2prize: event manifest bounded limits must be positive")
	}
	configHash, err := frozenConfigHash(manifest.MaxSymbols, manifest.MaxFundingLines, manifest.MaxPriceLines, manifest.SkyCutpointsHash)
	if err != nil || configHash != manifest.ConfigHash {
		return errors.New("g2prize: event generator config hash mismatch")
	}
	want := manifest.ManifestHash
	sealed, err := SealManifest(manifest)
	if err != nil {
		return err
	}
	if want == "" || sealed.ManifestHash != want {
		return errors.New("g2prize: event manifest hash mismatch")
	}
	return nil
}

// SelectedFileHash is the canonical "selected file hash" commitment: it
// hashes the exact set of catalog-pinned input files (path, content hash,
// size) that the deterministic generator selects for the first maxSymbols
// alphabetically sorted symbols. Any change to those files' content, or to
// which files are selected, changes this hash — which is exactly what lets
// the CLI detect drift between a frozen manifest and the catalog it claims
// to have been derived from.
func SelectedFileHash(catalog historydata.HistoryCatalog, maxSymbols int) (string, error) {
	if maxSymbols <= 0 {
		return "", errors.New("g2prize: positive max symbols required")
	}
	files := map[string]historydata.FileStats{}
	var fundingNames []string
	for _, family := range catalog.Families {
		for _, file := range family.Files {
			files[filepath.Clean(file.Path)] = file
			if family.Name == "okx_funding" && !file.Empty && strings.HasSuffix(file.Path, ".jsonl") {
				fundingNames = append(fundingNames, strings.TrimSuffix(filepath.Base(file.Path), ".jsonl"))
			}
		}
	}
	sort.Strings(fundingNames)
	if len(fundingNames) > maxSymbols {
		fundingNames = fundingNames[:maxSymbols]
	}
	type input struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	var selected []input
	for _, instrument := range fundingNames {
		for _, rel := range []string{
			filepath.Join("okx_funding", instrument+".jsonl"),
			filepath.Join("okx_1h", instrument+".jsonl"),
		} {
			file, ok := files[filepath.Clean(rel)]
			if !ok || file.Empty {
				continue
			}
			selected = append(selected, input{Path: rel, Hash: file.FileHash, Size: file.Size})
		}
	}
	if len(selected) == 0 {
		return "", errors.New("g2prize: no catalog inputs selected")
	}
	hash, _, err := datatruth.HashJSON(selected)
	return hash, err
}
