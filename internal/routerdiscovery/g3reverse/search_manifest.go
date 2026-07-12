package g3reverse

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg3/protocol"
)

const (
	SearchManifestSchemaVersion = "lingxi.router-g3-search-manifest.v1"
	SearchManifestGeneratorID   = "g3_reverse_induction_search_v1"
)

// SearchManifest is the sealed binding of actual derived rules to protocol,
// derivation algorithm config, event-manifest commitments, and alpha ledger.
// ActualDerivedRuleCount is the true count in 0..192 and is never padded.
type SearchManifest struct {
	SchemaVersion                 string `json:"schema_version"`
	GeneratorID                   string `json:"generator_id"`
	SearchSpaceID                 string `json:"search_space_id"`
	Generation                    string `json:"generation"`
	ProtocolHash                  string `json:"protocol_hash"`
	DerivationAlgorithmVersion    string `json:"derivation_algorithm_version"`
	DerivationAlgorithmConfigHash string `json:"derivation_algorithm_config_hash"`
	SourceDataHash                string `json:"source_data_hash"`
	CatalogDerivationHash         string `json:"catalog_derivation_hash"`
	EventManifestHash             string `json:"event_manifest_hash"`
	InductionEventHash            string `json:"induction_event_hash"`
	WinnerIndexHash               string `json:"winner_index_hash"`
	ActualDerivedRuleCount        int    `json:"actual_derived_rule_count"`
	CanonicalDerivedRulesHash     string `json:"canonical_derived_rules_hash"`
	AlphaLedgerHash               string `json:"alpha_ledger_hash"`
	CoverageHash                  string `json:"coverage_hash"`
	ManifestHash                  string `json:"manifest_hash"`
	KeyID                         string `json:"key_id"`
	Signature                     string `json:"signature"`
}

// SearchManifestBindings carries event-manifest commitments into the seal.
type SearchManifestBindings struct {
	SourceDataHash        string
	CatalogDerivationHash string
	EventManifestHash     string
	InductionEventHash    string
	WinnerIndexHash       string
	CoverageHash          string
}

// NewSearchManifest seals a search manifest over the actual derived rules.
func NewSearchManifest(rules []Rule, ledger AlphaLedger, bindings SearchManifestBindings) (SearchManifest, error) {
	if bindings.SourceDataHash != protocol.HistoryDataHash {
		return SearchManifest{}, errors.New("g3reverse: search manifest source data hash mismatch")
	}
	if bindings.CatalogDerivationHash == "" ||
		bindings.EventManifestHash == "" || bindings.InductionEventHash == "" ||
		bindings.WinnerIndexHash == "" {
		return SearchManifest{}, errors.New("g3reverse: search manifest bindings are incomplete")
	}
	if len(rules) > protocol.MaxDerivedSearchRules {
		return SearchManifest{}, errors.New("g3reverse: actual derived rule count exceeds total cap")
	}
	rulesHash, err := HashRules(rules)
	if err != nil {
		return SearchManifest{}, err
	}
	if err := ledger.Validate(); err != nil {
		return SearchManifest{}, err
	}
	registered := 0
	for _, entry := range ledger.Entries {
		if entry.Status == LedgerStatusRegistered {
			registered++
		}
	}
	if registered != len(rules) {
		return SearchManifest{}, errors.New("g3reverse: registered ledger count must equal actual rules")
	}
	if bindings.CoverageHash == "" {
		rulesPerGroup := map[string]int{}
		for _, rule := range rules {
			key := fmt.Sprintf("%s|%s|%s|%s", rule.SkyState, rule.SorterDomain, rule.Horizon, rule.Direction)
			rulesPerGroup[key]++
		}
		bindings.CoverageHash, err = hashCoverage(len(rulesPerGroup), len(rulesPerGroup), rulesPerGroup)
		if err != nil {
			return SearchManifest{}, err
		}
	}
	manifest := SearchManifest{
		SchemaVersion:                 SearchManifestSchemaVersion,
		GeneratorID:                   SearchManifestGeneratorID,
		SearchSpaceID:                 protocol.ReverseInductionSearchSpaceID,
		Generation:                    protocol.Generation,
		ProtocolHash:                  protocol.RouterProtocolHash,
		DerivationAlgorithmVersion:    protocol.DerivationAlgorithmVersion,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		SourceDataHash:                bindings.SourceDataHash,
		CatalogDerivationHash:         bindings.CatalogDerivationHash,
		EventManifestHash:             bindings.EventManifestHash,
		InductionEventHash:            bindings.InductionEventHash,
		WinnerIndexHash:               bindings.WinnerIndexHash,
		ActualDerivedRuleCount:        len(rules),
		CanonicalDerivedRulesHash:     rulesHash,
		AlphaLedgerHash:               ledger.LedgerHash,
		CoverageHash:                  bindings.CoverageHash,
	}
	return SealSearchManifest(manifest)
}

// SealSearchManifest stamps ManifestHash over unsigned content.
func SealSearchManifest(manifest SearchManifest) (SearchManifest, error) {
	manifest.KeyID = ""
	manifest.Signature = ""
	manifest.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(manifest)
	if err != nil {
		return SearchManifest{}, err
	}
	manifest.ManifestHash = hash
	return manifest, nil
}

// SignSearchManifest HMAC-signs a sealed search manifest hash.
func SignSearchManifest(manifest SearchManifest, key []byte, keyID string) (SearchManifest, error) {
	if len(key) < 32 || strings.TrimSpace(keyID) == "" {
		return SearchManifest{}, errors.New("g3reverse: search signing key >=32 bytes and key id are required")
	}
	sealed, err := SealSearchManifest(manifest)
	if err != nil {
		return SearchManifest{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(sealed.ManifestHash))
	sealed.KeyID = strings.TrimSpace(keyID)
	sealed.Signature = hex.EncodeToString(mac.Sum(nil))
	return sealed, nil
}

// VerifySearchManifestSignature fails closed on tamper or binding drift.
func VerifySearchManifestSignature(manifest SearchManifest, key []byte) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if len(key) < 32 || manifest.KeyID == "" || manifest.Signature == "" {
		return errors.New("g3reverse: signed search manifest is required")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(manifest.ManifestHash))
	expected, err := hex.DecodeString(manifest.Signature)
	if err != nil || !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("g3reverse: search manifest signature mismatch")
	}
	return nil
}

// Validate checks internal search-manifest consistency (no padding to 192).
func (manifest SearchManifest) Validate() error {
	if manifest.SchemaVersion != SearchManifestSchemaVersion ||
		manifest.GeneratorID != SearchManifestGeneratorID ||
		manifest.SearchSpaceID != protocol.ReverseInductionSearchSpaceID ||
		manifest.Generation != protocol.Generation ||
		manifest.ProtocolHash != protocol.RouterProtocolHash ||
		manifest.DerivationAlgorithmVersion != protocol.DerivationAlgorithmVersion ||
		manifest.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
		return errors.New("g3reverse: search manifest protocol/algorithm binding mismatch")
	}
	if manifest.SourceDataHash != protocol.HistoryDataHash {
		return errors.New("g3reverse: search manifest source data hash mismatch")
	}
	if manifest.CatalogDerivationHash == "" ||
		manifest.EventManifestHash == "" || manifest.InductionEventHash == "" ||
		manifest.WinnerIndexHash == "" || manifest.CanonicalDerivedRulesHash == "" ||
		manifest.AlphaLedgerHash == "" || manifest.CoverageHash == "" {
		return errors.New("g3reverse: search manifest missing required commitments")
	}
	if manifest.ActualDerivedRuleCount < 0 || manifest.ActualDerivedRuleCount > protocol.MaxDerivedSearchRules {
		return errors.New("g3reverse: actual derived rule count out of range")
	}
	want := manifest.ManifestHash
	sealed, err := SealSearchManifest(manifest)
	if err != nil {
		return err
	}
	if want == "" || sealed.ManifestHash != want {
		return errors.New("g3reverse: search manifest hash mismatch")
	}
	return nil
}
