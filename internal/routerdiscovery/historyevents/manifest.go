package historyevents

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
	"lingxi/internal/hierarchicalrouter"
	"lingxi/internal/routerdiscovery/backward"
)

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
	ManifestHash      string `json:"manifest_hash"`
	KeyID             string `json:"key_id"`
	Signature         string `json:"signature"`
}

func NewManifest(events []backward.Event, catalog historydata.HistoryCatalog, maxSymbols, maxFundingLines, maxPriceLines int) (Manifest, error) {
	if maxSymbols <= 0 || maxFundingLines <= 0 || maxPriceLines <= 0 {
		return Manifest{}, errors.New("historyevents: bounded manifest limits are required")
	}
	discoveryHash, err := backward.HashEvents(events)
	if err != nil {
		return Manifest{}, err
	}
	if catalog.DataHash != hierarchicalrouter.HistoryDataHash || catalog.SchemaHash == "" || catalog.DatasetID == "" {
		return Manifest{}, errors.New("historyevents: verified history catalog is required")
	}
	derivationHash, err := DerivationHash(catalog, maxSymbols)
	if err != nil {
		return Manifest{}, err
	}
	configHash, err := frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		SchemaVersion: "lingxi.router-history-events.v1", GeneratorID: "funding_post_settlement_v1",
		Generation: hierarchicalrouter.Generation, ProtocolHash: hierarchicalrouter.RouterProtocolHash,
		SourceDataHash: catalog.DataHash, SourceSchemaHash: catalog.SchemaHash,
		SourceDatasetID: catalog.DatasetID, DerivationHash: derivationHash,
		DiscoveryDataHash: discoveryHash,
		EventCount:        len(events), MaxSymbols: maxSymbols, MaxFundingLines: maxFundingLines,
		MaxPriceLines: maxPriceLines, ConfigHash: configHash,
	}
	return SealManifest(manifest)
}

func frozenConfigHash(maxSymbols, maxFundingLines, maxPriceLines int) (string, error) {
	config := struct {
		GeneratorID       string  `json:"generator_id"`
		MaxSymbols        int     `json:"max_symbols"`
		MaxFundingLines   int     `json:"max_funding_lines"`
		MaxPriceLines     int     `json:"max_price_lines"`
		CostBPS           float64 `json:"cost_bps"`
		OutcomeHorizon    int     `json:"outcome_horizon_hours"`
		VolatilityLowBPS  float64 `json:"volatility_low_bps"`
		VolatilityHighBPS float64 `json:"volatility_high_bps"`
		LiquidityRule     string  `json:"liquidity_rule"`
	}{
		GeneratorID: "funding_post_settlement_v1", MaxSymbols: maxSymbols,
		MaxFundingLines: maxFundingLines, MaxPriceLines: maxPriceLines,
		CostBPS:           hierarchicalrouter.BackwardDiscoveryCostBPS,
		OutcomeHorizon:    hierarchicalrouter.BackwardOutcomeHorizonHours,
		VolatilityLowBPS:  hierarchicalrouter.BackwardVolatilityLowBPS,
		VolatilityHighBPS: hierarchicalrouter.BackwardVolatilityHighBPS,
		LiquidityRule:     hierarchicalrouter.BackwardLiquidityBucketRule,
	}
	hash, _, err := datatruth.HashJSON(config)
	return hash, err
}

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

func SignManifest(manifest Manifest, key []byte, keyID string) (Manifest, error) {
	if len(key) < 32 || strings.TrimSpace(keyID) == "" {
		return Manifest{}, errors.New("historyevents: signing key >=32 bytes and key id are required")
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

func VerifyManifestSignature(manifest Manifest, key []byte) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if len(key) < 32 || manifest.KeyID == "" || manifest.Signature == "" {
		return errors.New("historyevents: signed event manifest is required")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(manifest.ManifestHash))
	expected, err := hex.DecodeString(manifest.Signature)
	if err != nil || !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("historyevents: event manifest signature mismatch")
	}
	return nil
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != "lingxi.router-history-events.v1" ||
		manifest.GeneratorID != "funding_post_settlement_v1" ||
		manifest.Generation != hierarchicalrouter.Generation ||
		manifest.ProtocolHash != hierarchicalrouter.RouterProtocolHash ||
		manifest.SourceDataHash != hierarchicalrouter.HistoryDataHash ||
		manifest.SourceSchemaHash == "" || manifest.SourceDatasetID == "" ||
		manifest.DerivationHash == "" || manifest.DiscoveryDataHash == "" ||
		manifest.EventCount <= 0 || manifest.ConfigHash == "" {
		return errors.New("historyevents: event manifest is not bound to frozen history/router")
	}
	if manifest.MaxSymbols != hierarchicalrouter.BackwardSmokeMaxSymbols ||
		manifest.MaxFundingLines != hierarchicalrouter.BackwardSmokeMaxFundingLines ||
		manifest.MaxPriceLines != hierarchicalrouter.BackwardSmokeMaxPriceLines {
		return errors.New("historyevents: event manifest bounded limits drifted")
	}
	configHash, err := frozenConfigHash(manifest.MaxSymbols, manifest.MaxFundingLines, manifest.MaxPriceLines)
	if err != nil || configHash != manifest.ConfigHash {
		return errors.New("historyevents: event generator config hash mismatch")
	}
	want := manifest.ManifestHash
	sealed, err := SealManifest(manifest)
	if err != nil {
		return err
	}
	if want == "" || sealed.ManifestHash != want {
		return errors.New("historyevents: event manifest hash mismatch")
	}
	return nil
}

func DerivationHash(catalog historydata.HistoryCatalog, maxSymbols int) (string, error) {
	if maxSymbols <= 0 {
		return "", errors.New("historyevents: positive max symbols required")
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
		return "", errors.New("historyevents: no catalog inputs selected")
	}
	hash, _, err := datatruth.HashJSON(selected)
	return hash, err
}
