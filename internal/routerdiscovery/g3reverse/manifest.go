package g3reverse

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
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg3/protocol"
)

const (
	SchemaVersion = "lingxi.router-g3-reverse-events.v1"
	GeneratorID   = "g3_reverse_hourly_closed_bar_v1"
)

// Manifest is the signed receipt binding four partition bundles to catalog,
// selected files, cutpoints, partition windows, and config hashes.
type Manifest struct {
	SchemaVersion           string `json:"schema_version"`
	GeneratorID             string `json:"generator_id"`
	Generation              string `json:"generation"`
	ProtocolHash            string `json:"protocol_hash"`
	SourceDataHash          string `json:"source_data_hash"`
	SourceSchemaHash        string `json:"source_schema_hash"`
	SourceDatasetID         string `json:"source_dataset_id"`
	DerivationHash          string `json:"derivation_inputs_hash"`
	PartitionBoundariesHash string `json:"partition_boundaries_hash"`
	InductionEventHash      string `json:"induction_event_hash"`
	RecheckEventHash        string `json:"recheck_event_hash"`
	ForwardEventHash        string `json:"forward_event_hash"`
	WalkForwardEventHash    string `json:"walk_forward_event_hash"`
	WinnerIndexHash         string `json:"winner_index_hash"`
	EventCount              int    `json:"event_count"`
	InductionEventCount     int    `json:"induction_event_count"`
	RecheckEventCount       int    `json:"recheck_event_count"`
	ForwardEventCount       int    `json:"forward_event_count"`
	WalkForwardEventCount   int    `json:"walk_forward_event_count"`
	WinnerCount             int    `json:"winner_count"`
	MaxSymbols              int    `json:"max_symbols"`
	MaxFundingLines         int    `json:"max_funding_lines"`
	MaxPriceLines           int    `json:"max_price_lines"`
	MaxFamilyLines          int    `json:"max_family_lines"`
	ConfigHash              string `json:"config_hash"`
	SkyCutpointsHash        string `json:"sky_cutpoints_hash"`
	ManifestHash            string `json:"manifest_hash"`
	KeyID                   string `json:"key_id"`
	Signature               string `json:"signature"`
}

// NewManifest seals a manifest over four partition event pools.
func NewManifest(
	induction, recheck, forward, wf []Event,
	winner WinnerIndex,
	catalog historydata.HistoryCatalog,
	cutpoints sky.Cutpoints,
	boundaries Boundaries,
	generation, protocolHash string,
	maxSymbols, maxFundingLines, maxPriceLines, maxFamilyLines int,
) (Manifest, error) {
	if maxSymbols <= 0 || maxFundingLines <= 0 || maxPriceLines <= 0 || maxFamilyLines <= 0 {
		return Manifest{}, errors.New("g3reverse: bounded manifest limits are required")
	}
	if generation != protocol.Generation || protocolHash != protocol.RouterProtocolHash {
		return Manifest{}, errors.New("g3reverse: manifest generation/protocol hash must match frozen G3 protocol")
	}
	if catalog.DataHash != protocol.HistoryDataHash || catalog.SchemaHash == "" || catalog.DatasetID == "" {
		return Manifest{}, errors.New("g3reverse: history catalog must match frozen G3 history data hash")
	}
	if !cutpoints.Frozen {
		return Manifest{}, errors.New("g3reverse: frozen sky cutpoints are required")
	}
	inductionHash, err := HashEvents(induction)
	if err != nil {
		return Manifest{}, err
	}
	recheckHash, err := HashEvents(recheck)
	if err != nil {
		return Manifest{}, err
	}
	forwardHash, err := HashEvents(forward)
	if err != nil {
		return Manifest{}, err
	}
	wfHash, err := HashEvents(wf)
	if err != nil {
		return Manifest{}, err
	}
	boundaryHash, err := HashBoundaries(boundaries)
	if err != nil {
		return Manifest{}, err
	}
	derivationHash, err := SelectedFileHash(catalog, maxSymbols)
	if err != nil {
		return Manifest{}, err
	}
	cutpointsHash, err := cutpoints.Hash()
	if err != nil {
		return Manifest{}, err
	}
	configHash, err := frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines, maxFamilyLines, cutpointsHash, boundaryHash)
	if err != nil {
		return Manifest{}, err
	}
	total := len(induction) + len(recheck) + len(forward) + len(wf)
	manifest := Manifest{
		SchemaVersion: SchemaVersion, GeneratorID: GeneratorID,
		Generation: generation, ProtocolHash: protocolHash,
		SourceDataHash: catalog.DataHash, SourceSchemaHash: catalog.SchemaHash,
		SourceDatasetID: catalog.DatasetID, DerivationHash: derivationHash,
		PartitionBoundariesHash: boundaryHash,
		InductionEventHash:      inductionHash,
		RecheckEventHash:        recheckHash,
		ForwardEventHash:        forwardHash,
		WalkForwardEventHash:    wfHash,
		WinnerIndexHash:         winner.IndexHash,
		EventCount:              total,
		InductionEventCount:     len(induction),
		RecheckEventCount:       len(recheck),
		ForwardEventCount:       len(forward),
		WalkForwardEventCount:   len(wf),
		WinnerCount:             len(winner.Entries),
		MaxSymbols:              maxSymbols,
		MaxFundingLines:         maxFundingLines,
		MaxPriceLines:           maxPriceLines,
		MaxFamilyLines:          maxFamilyLines,
		ConfigHash:              configHash,
		SkyCutpointsHash:        cutpointsHash,
	}
	return SealManifest(manifest)
}

func frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines, maxFamilyLines int, cutpointsHash, boundaryHash string) (string, error) {
	config := struct {
		GeneratorID             string   `json:"generator_id"`
		MaxSymbols              int      `json:"max_symbols"`
		MaxFundingLines         int      `json:"max_funding_lines"`
		MaxPriceLines           int      `json:"max_price_lines"`
		MaxFamilyLines          int      `json:"max_family_lines"`
		CostBPS                 float64  `json:"cost_bps"`
		NeutralBandBPS          float64  `json:"neutral_band_bps"`
		SkyCutpointsHash        string   `json:"sky_cutpoints_hash"`
		PartitionBoundariesHash string   `json:"partition_boundaries_hash"`
		MinQuoteVolume          float64  `json:"min_quote_volume"`
		MinVolatilityCostRatio  float64  `json:"min_volatility_cost_ratio"`
		Horizons                []string `json:"horizons"`
		FeatureCatalog          []string `json:"feature_catalog"`
	}{
		GeneratorID: GeneratorID, MaxSymbols: maxSymbols,
		MaxFundingLines: maxFundingLines, MaxPriceLines: maxPriceLines,
		MaxFamilyLines:          maxFamilyLines,
		CostBPS:                 protocol.DiscoveryCostBPS,
		NeutralBandBPS:          protocol.NeutralBandBPS,
		SkyCutpointsHash:        cutpointsHash,
		PartitionBoundariesHash: boundaryHash,
		MinQuoteVolume:          protocol.SorterMinQuoteVolume,
		MinVolatilityCostRatio:  protocol.SorterMinVolatilityCost,
		Horizons:                []string{string(Horizon1h), string(Horizon4h), string(Horizon24h)},
		FeatureCatalog:          append([]string(nil), protocol.FeatureCatalog...),
	}
	hash, _, err := datatruth.HashJSON(config)
	return hash, err
}

// ConfigHash exposes the canonical generator configuration commitment for
// checkpoint binding.
func ConfigHash(maxSymbols, maxFundingLines, maxPriceLines, maxFamilyLines int, cutpointsHash, boundaryHash string) (string, error) {
	return frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines, maxFamilyLines, cutpointsHash, boundaryHash)
}

// SealManifest stamps manifest with its content hash.
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

// SignManifest HMAC-signs a sealed manifest hash.
func SignManifest(manifest Manifest, key []byte, keyID string) (Manifest, error) {
	if len(key) < 32 || strings.TrimSpace(keyID) == "" {
		return Manifest{}, errors.New("g3reverse: signing key >=32 bytes and key id are required")
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

// VerifyManifestSignature fails closed unless manifest validates and signature matches.
func VerifyManifestSignature(manifest Manifest, key []byte) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if len(key) < 32 || manifest.KeyID == "" || manifest.Signature == "" {
		return errors.New("g3reverse: signed event manifest is required")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(manifest.ManifestHash))
	expected, err := hex.DecodeString(manifest.Signature)
	if err != nil || !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("g3reverse: event manifest signature mismatch")
	}
	return nil
}

// Validate checks internal manifest consistency.
func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion || manifest.GeneratorID != GeneratorID ||
		manifest.Generation != protocol.Generation || manifest.ProtocolHash != protocol.RouterProtocolHash ||
		manifest.SourceDataHash != protocol.HistoryDataHash ||
		manifest.SourceSchemaHash == "" || manifest.SourceDatasetID == "" ||
		manifest.DerivationHash == "" || manifest.PartitionBoundariesHash == "" ||
		manifest.InductionEventHash == "" || manifest.RecheckEventHash == "" ||
		manifest.ForwardEventHash == "" || manifest.WalkForwardEventHash == "" ||
		manifest.ConfigHash == "" || manifest.SkyCutpointsHash == "" {
		return errors.New("g3reverse: event manifest is missing required commitments")
	}
	if manifest.MaxSymbols <= 0 || manifest.MaxFundingLines <= 0 || manifest.MaxPriceLines <= 0 || manifest.MaxFamilyLines <= 0 {
		return errors.New("g3reverse: event manifest bounded limits must be positive")
	}
	if manifest.EventCount != manifest.InductionEventCount+manifest.RecheckEventCount+manifest.ForwardEventCount+manifest.WalkForwardEventCount {
		return errors.New("g3reverse: event count mismatch across partitions")
	}
	configHash, err := frozenConfigHash(
		manifest.MaxSymbols, manifest.MaxFundingLines, manifest.MaxPriceLines, manifest.MaxFamilyLines,
		manifest.SkyCutpointsHash, manifest.PartitionBoundariesHash,
	)
	if err != nil || configHash != manifest.ConfigHash {
		return errors.New("g3reverse: event generator config hash mismatch")
	}
	want := manifest.ManifestHash
	sealed, err := SealManifest(manifest)
	if err != nil {
		return err
	}
	if want == "" || sealed.ManifestHash != want {
		return errors.New("g3reverse: event manifest hash mismatch")
	}
	return nil
}

// SelectedFileHash hashes catalog-pinned inputs for the first maxSymbols symbols.
func SelectedFileHash(catalog historydata.HistoryCatalog, maxSymbols int) (string, error) {
	if maxSymbols <= 0 {
		return "", errors.New("g3reverse: positive max symbols required")
	}
	files := map[string]historydata.FileStats{}
	for _, family := range catalog.Families {
		for _, file := range family.Files {
			files[filepath.Clean(file.Path)] = file
		}
	}
	fundingNames := selectedInstruments(catalog, maxSymbols)
	type input struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	var selected []input
	seen := map[string]bool{}
	addSelected := func(rel string) {
		clean := filepath.Clean(rel)
		if seen[clean] {
			return
		}
		file, ok := files[clean]
		if !ok || file.Empty {
			return
		}
		seen[clean] = true
		selected = append(selected, input{Path: rel, Hash: file.FileHash, Size: file.Size})
	}
	addSelected(filepath.Join(familyOKXFunding, "BTC-USDT-SWAP.jsonl"))
	addSelected(filepath.Join(familyOKX1h, "BTC-USDT-SWAP.jsonl"))
	for _, instrument := range fundingNames {
		symbol := CanonicalSymbol(instrument)
		candidates := []string{
			filepath.Join(familyOKXFunding, instrument+".jsonl"),
			filepath.Join(familyOKX1h, instrument+".jsonl"),
			filepath.Join(familyBybitFunding, BybitFileName(symbol)+".jsonl"),
			filepath.Join(familyRubikTaker1h, RubikFileName(symbol)+".jsonl"),
			filepath.Join(familyRubikLSRatio1, RubikFileName(symbol)+".jsonl"),
		}
		for _, rel := range candidates {
			addSelected(rel)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	if len(selected) == 0 {
		return "", errors.New("g3reverse: no catalog inputs selected")
	}
	hash, _, err := datatruth.HashJSON(selected)
	return hash, err
}
