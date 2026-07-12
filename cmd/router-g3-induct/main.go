package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg3/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g3-induct.checkpoint.v4"

type checkpointFile struct {
	SchemaVersion                 string    `json:"schema_version"`
	Generation                    string    `json:"generation"`
	ProtocolHash                  string    `json:"protocol_hash"`
	DerivationAlgorithmConfigHash string    `json:"derivation_algorithm_config_hash"`
	EventManifestHash             string    `json:"event_manifest_hash"`
	InductionEventHash            string    `json:"induction_event_hash"`
	WinnerIndexHash               string    `json:"winner_index_hash"`
	SourceDataHash                string    `json:"source_data_hash"`
	CatalogDerivationHash         string    `json:"catalog_derivation_hash"`
	CatalogPath                   string    `json:"catalog_path"`
	HistoryRoot                   string    `json:"history_root"`
	CutpointsHash                 string    `json:"cutpoints_hash"`
	BoundariesHash                string    `json:"boundaries_hash"`
	RecomputedEventHash           string    `json:"recomputed_event_hash"`
	RecomputedWinnerHash          string    `json:"recomputed_winner_hash"`
	OutputDir                     string    `json:"output_dir"`
	Done                          bool      `json:"done"`
	SearchManifestHash            string    `json:"search_manifest_hash,omitempty"`
	RulesHash                     string    `json:"rules_hash,omitempty"`
	LedgerHash                    string    `json:"ledger_hash,omitempty"`
	SavedAt                       time.Time `json:"saved_at"`
	CheckpointHash                string    `json:"checkpoint_hash"`
}

type checkpointBinding struct {
	EventManifestHash     string
	InductionEventHash    string
	WinnerIndexHash       string
	SourceDataHash        string
	CatalogDerivationHash string
	CatalogPath           string
	HistoryRoot           string
	CutpointsHash         string
	BoundariesHash        string
	RecomputedEventHash   string
	RecomputedWinnerHash  string
	OutputDir             string
}

type canonicalProvenance struct {
	Events              []g3reverse.Event
	Winners             g3reverse.WinnerIndex
	Catalog             historydata.HistoryCatalog
	CatalogPath         string
	HistoryRoot         string
	CutpointsHash       string
	BoundariesHash      string
	RecomputedEventHash string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g3-induct", flag.ContinueOnError)
	bundleDir := fs.String("bundle-dir", "", "G3 events research bundle directory")
	historyRoot := fs.String("history-root", "", "canonical research-only history root")
	catalogPath := fs.String("history-catalog", "", "canonical HistoryCatalog")
	outputDir := fs.String("output-dir", "", "explicit induction output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", "HMAC key env for event+search manifests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bundleDir == "" || *historyRoot == "" || *catalogPath == "" ||
		*outputDir == "" || *checkpointPath == "" {
		return errors.New("--bundle-dir, --history-root, --history-catalog, --output-dir and --checkpoint are required")
	}
	signingKey := []byte(os.Getenv(*keyEnv))
	if len(signingKey) < 32 {
		return fmt.Errorf("HMAC key environment variable %s must be >=32 bytes", *keyEnv)
	}

	absoluteBundle, err := filepath.Abs(*bundleDir)
	if err != nil {
		return err
	}
	absoluteOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}

	var eventManifest g3reverse.Manifest
	if err := readJSON(filepath.Join(absoluteBundle, "event_manifest.json"), &eventManifest); err != nil {
		return err
	}
	if err := g3reverse.VerifyManifestSignature(eventManifest, signingKey); err != nil {
		return err
	}
	if eventManifest.Generation != protocol.Generation || eventManifest.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("router-g3-induct: event manifest protocol mismatch")
	}

	provenance, err := verifyCanonicalProvenance(
		context.Background(), *historyRoot, *catalogPath, absoluteBundle, eventManifest,
	)
	if err != nil {
		return err
	}
	events := provenance.Events
	winners := provenance.Winners

	bindings := g3reverse.SearchManifestBindings{
		SourceDataHash:        eventManifest.SourceDataHash,
		CatalogDerivationHash: eventManifest.DerivationHash,
		EventManifestHash:     eventManifest.ManifestHash,
		InductionEventHash:    eventManifest.InductionEventHash,
		WinnerIndexHash:       eventManifest.WinnerIndexHash,
	}
	cpBinding := checkpointBinding{
		EventManifestHash: eventManifest.ManifestHash, InductionEventHash: eventManifest.InductionEventHash,
		WinnerIndexHash: eventManifest.WinnerIndexHash, SourceDataHash: eventManifest.SourceDataHash,
		CatalogDerivationHash: eventManifest.DerivationHash,
		CatalogPath:           provenance.CatalogPath, HistoryRoot: provenance.HistoryRoot,
		CutpointsHash: provenance.CutpointsHash, BoundariesHash: provenance.BoundariesHash,
		RecomputedEventHash:  provenance.RecomputedEventHash,
		RecomputedWinnerHash: provenance.Winners.IndexHash,
		OutputDir:            absoluteOutput,
	}

	cp, exists, err := loadCheckpoint(*checkpointPath)
	if err != nil {
		return err
	}
	if exists {
		if err := cp.validateBinding(cpBinding); err != nil {
			return err
		}
		if cp.Done {
			if err := verifyCompletedOutputs(absoluteOutput, cp); err != nil {
				return err
			}
			fmt.Printf("{\"resumed\":true,\"actual_derived_rule_count\":%d,\"exam_actions\":%d}\n",
				countRulesFile(filepath.Join(absoluteOutput, "rules.json")), protocol.ExamActions)
			return nil
		}
	} else {
		cp = newCheckpoint(cpBinding)
	}

	result, err := g3reverse.DeriveWithOptions(
		events, winners, bindings, signingKey, "router-g3-induct",
		g3reverse.DeriveOptions{
			ProgressEvery: protocol.ReverseInductionProgressEvery,
			OnProgress: func(progress g3reverse.DerivationProgress) {
				encoded, _ := json.Marshal(progress)
				fmt.Fprintln(os.Stderr, string(encoded))
			},
		},
	)
	if err != nil {
		return err
	}
	if err := g3reverse.VerifySearchManifestSignature(result.Manifest, signingKey); err != nil {
		return err
	}

	if err := os.MkdirAll(absoluteOutput, 0o755); err != nil {
		return err
	}
	artifacts := map[string]any{
		"search_manifest.json":   result.Manifest,
		"rules.json":             result.Rules,
		"alpha_ledger.json":      result.Ledger,
		"induction_summary.json": result.Summary,
	}
	for name, value := range artifacts {
		if err := writeJSONAtomic(filepath.Join(absoluteOutput, name), value); err != nil {
			return err
		}
	}

	cp.Done = true
	cp.SearchManifestHash = result.Manifest.ManifestHash
	cp.RulesHash = result.Manifest.CanonicalDerivedRulesHash
	cp.LedgerHash = result.Ledger.LedgerHash
	if err := saveCheckpoint(*checkpointPath, cp); err != nil {
		return err
	}
	fmt.Printf("{\"actual_derived_rule_count\":%d,\"single\":%d,\"dual\":%d,\"exam_actions\":%d}\n",
		result.Summary.ActualDerivedRuleCount, result.Summary.SingleRegistered,
		result.Summary.DualRegistered, protocol.ExamActions)
	return nil
}

func newCheckpoint(binding checkpointBinding) checkpointFile {
	return checkpointFile{
		SchemaVersion: checkpointSchemaVersion,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		EventManifestHash:             binding.EventManifestHash,
		InductionEventHash:            binding.InductionEventHash,
		WinnerIndexHash:               binding.WinnerIndexHash,
		SourceDataHash:                binding.SourceDataHash,
		CatalogDerivationHash:         binding.CatalogDerivationHash,
		CatalogPath:                   binding.CatalogPath,
		HistoryRoot:                   binding.HistoryRoot,
		CutpointsHash:                 binding.CutpointsHash,
		BoundariesHash:                binding.BoundariesHash,
		RecomputedEventHash:           binding.RecomputedEventHash,
		RecomputedWinnerHash:          binding.RecomputedWinnerHash,
		OutputDir:                     binding.OutputDir,
	}
}

func (cp checkpointFile) validateBinding(binding checkpointBinding) error {
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash ||
		cp.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash ||
		cp.EventManifestHash != binding.EventManifestHash ||
		cp.InductionEventHash != binding.InductionEventHash ||
		cp.WinnerIndexHash != binding.WinnerIndexHash ||
		cp.SourceDataHash != binding.SourceDataHash ||
		cp.CatalogDerivationHash != binding.CatalogDerivationHash ||
		filepath.Clean(cp.CatalogPath) != filepath.Clean(binding.CatalogPath) ||
		filepath.Clean(cp.HistoryRoot) != filepath.Clean(binding.HistoryRoot) ||
		cp.CutpointsHash != binding.CutpointsHash ||
		cp.BoundariesHash != binding.BoundariesHash ||
		cp.RecomputedEventHash != binding.RecomputedEventHash ||
		cp.RecomputedWinnerHash != binding.RecomputedWinnerHash ||
		filepath.Clean(cp.OutputDir) != filepath.Clean(binding.OutputDir) {
		return errors.New("router-g3-induct: checkpoint input or config drift")
	}
	return nil
}

func verifyCanonicalProvenance(
	ctx context.Context,
	historyRoot, catalogPath, bundleDir string,
	eventManifest g3reverse.Manifest,
) (canonicalProvenance, error) {
	absoluteRoot, err := filepath.Abs(historyRoot)
	if err != nil {
		return canonicalProvenance{}, err
	}
	absoluteCatalog, err := filepath.Abs(catalogPath)
	if err != nil {
		return canonicalProvenance{}, err
	}
	var catalog historydata.HistoryCatalog
	if err := readJSON(absoluteCatalog, &catalog); err != nil {
		return canonicalProvenance{}, err
	}
	if err := historydata.ValidateCatalog(catalog); err != nil {
		return canonicalProvenance{}, err
	}
	catalogRoot, err := filepath.Abs(catalog.RootDir)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if filepath.Clean(catalogRoot) != filepath.Clean(absoluteRoot) {
		return canonicalProvenance{}, errors.New("router-g3-induct: history root does not match catalog root")
	}
	if catalog.DataHash != eventManifest.SourceDataHash ||
		catalog.SchemaHash != eventManifest.SourceSchemaHash ||
		catalog.DatasetID != eventManifest.SourceDatasetID {
		return canonicalProvenance{}, errors.New("router-g3-induct: catalog identity does not match event manifest")
	}
	if err := g3reverse.VerifySelectedFiles(&catalog, absoluteRoot, eventManifest.MaxSymbols); err != nil {
		return canonicalProvenance{}, err
	}
	selectedHash, err := g3reverse.SelectedFileHash(catalog, eventManifest.MaxSymbols)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if selectedHash != eventManifest.DerivationHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: catalog derivation hash mismatch")
	}

	var cutpoints sky.Cutpoints
	if err := readJSON(filepath.Join(bundleDir, "sky_cutpoints.json"), &cutpoints); err != nil {
		return canonicalProvenance{}, err
	}
	cutpointsHash, err := cutpoints.Hash()
	if err != nil {
		return canonicalProvenance{}, err
	}
	if cutpointsHash != eventManifest.SkyCutpointsHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: sky cutpoints hash mismatch")
	}
	var boundaries g3reverse.Boundaries
	if err := readJSON(filepath.Join(bundleDir, "partition_boundaries.json"), &boundaries); err != nil {
		return canonicalProvenance{}, err
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if boundariesHash != eventManifest.PartitionBoundariesHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: partition boundaries hash mismatch")
	}
	configHash, err := g3reverse.ConfigHash(
		eventManifest.MaxSymbols, eventManifest.MaxFundingLines,
		eventManifest.MaxPriceLines, eventManifest.MaxFamilyLines,
		cutpointsHash, boundariesHash,
	)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if configHash != eventManifest.ConfigHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: frozen event config hash mismatch")
	}

	_, canonicalBoundaries, err := g3reverse.AnchorFundingSpan(absoluteRoot, eventManifest.MaxFundingLines)
	if err != nil {
		return canonicalProvenance{}, err
	}
	canonicalBoundariesHash, err := g3reverse.HashBoundaries(canonicalBoundaries)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if canonicalBoundariesHash != boundariesHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: bundle boundaries do not match canonical history")
	}
	baseConfig := g3reverse.Config{
		HistoryRoot: absoluteRoot, Catalog: &catalog,
		MaxSymbols: eventManifest.MaxSymbols, MaxFundingLines: eventManifest.MaxFundingLines,
		MaxPriceLines: eventManifest.MaxPriceLines, MaxFamilyLines: eventManifest.MaxFamilyLines,
		ProgressEvery: protocol.ReverseInductionProgressEvery,
	}
	canonicalCutpoints, err := g3reverse.FitSkyCutpoints(ctx, baseConfig, canonicalBoundaries)
	if err != nil {
		return canonicalProvenance{}, err
	}
	canonicalCutpointsHash, err := canonicalCutpoints.Hash()
	if err != nil {
		return canonicalProvenance{}, err
	}
	if canonicalCutpointsHash != cutpointsHash {
		return canonicalProvenance{}, errors.New("router-g3-induct: bundle cutpoints do not match canonical history")
	}

	start, end := boundaries.Window(g3reverse.PartitionInduction)
	baseConfig.SkyCutpoints = cutpoints
	baseConfig.Start = start
	baseConfig.End = end
	recomputedEvents, err := g3reverse.BuildEvents(ctx, baseConfig)
	if err != nil {
		return canonicalProvenance{}, err
	}
	recomputedEventHash, err := g3reverse.HashEvents(recomputedEvents)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if recomputedEventHash != eventManifest.InductionEventHash ||
		len(recomputedEvents) != eventManifest.InductionEventCount {
		return canonicalProvenance{}, errors.New("router-g3-induct: canonical induction events do not match event manifest")
	}
	bundleEvents, err := readEventJSONL(filepath.Join(bundleDir, "g3_induction", "events.jsonl"))
	if err != nil {
		return canonicalProvenance{}, err
	}
	bundleEventHash, err := g3reverse.HashEvents(bundleEvents)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if bundleEventHash != recomputedEventHash || len(bundleEvents) != len(recomputedEvents) {
		return canonicalProvenance{}, errors.New("router-g3-induct: induction event bundle differs from canonical regeneration")
	}

	recomputedWinners, err := g3reverse.BuildWinnerIndex(
		recomputedEvents, protocol.Generation, protocol.RouterProtocolHash,
	)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if recomputedWinners.IndexHash != eventManifest.WinnerIndexHash ||
		len(recomputedWinners.Entries) != eventManifest.WinnerCount {
		return canonicalProvenance{}, errors.New("router-g3-induct: canonical winner index does not match event manifest")
	}
	bundleWinnerEntries, err := readWinnerJSONL(filepath.Join(bundleDir, "g3_induction", "winner_index.jsonl"))
	if err != nil {
		return canonicalProvenance{}, err
	}
	bundleWinnerHash, err := g3reverse.HashWinnerEntries(bundleWinnerEntries)
	if err != nil {
		return canonicalProvenance{}, err
	}
	if bundleWinnerHash != recomputedWinners.IndexHash ||
		len(bundleWinnerEntries) != len(recomputedWinners.Entries) {
		return canonicalProvenance{}, errors.New("router-g3-induct: winner index bundle differs from canonical regeneration")
	}

	return canonicalProvenance{
		Events: recomputedEvents, Winners: recomputedWinners, Catalog: catalog,
		CatalogPath: absoluteCatalog, HistoryRoot: absoluteRoot,
		CutpointsHash: cutpointsHash, BoundariesHash: boundariesHash,
		RecomputedEventHash: recomputedEventHash,
	}, nil
}

func verifyCompletedOutputs(outputDir string, cp checkpointFile) error {
	var manifest g3reverse.SearchManifest
	if err := readJSON(filepath.Join(outputDir, "search_manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest.ManifestHash != cp.SearchManifestHash {
		return errors.New("router-g3-induct: completed checkpoint search manifest drift")
	}
	var ledger g3reverse.AlphaLedger
	if err := readJSON(filepath.Join(outputDir, "alpha_ledger.json"), &ledger); err != nil {
		return err
	}
	if ledger.LedgerHash != cp.LedgerHash {
		return errors.New("router-g3-induct: completed checkpoint ledger drift")
	}
	var rules []g3reverse.Rule
	if err := readJSON(filepath.Join(outputDir, "rules.json"), &rules); err != nil {
		return err
	}
	rulesHash, err := g3reverse.HashRules(rules)
	if err != nil {
		return err
	}
	if rulesHash != cp.RulesHash {
		return errors.New("router-g3-induct: completed checkpoint rules drift")
	}
	return nil
}

func loadCheckpoint(path string) (checkpointFile, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkpointFile{}, false, nil
		}
		return checkpointFile{}, false, err
	}
	var cp checkpointFile
	if err := json.Unmarshal(raw, &cp); err != nil {
		return checkpointFile{}, false, err
	}
	if cp.SchemaVersion != checkpointSchemaVersion {
		return checkpointFile{}, false, errors.New("router-g3-induct: checkpoint schema mismatch")
	}
	want := cp.CheckpointHash
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return checkpointFile{}, false, err
	}
	if want == "" || hash != want {
		return checkpointFile{}, false, errors.New("router-g3-induct: checkpoint hash mismatch")
	}
	cp.CheckpointHash = want
	return cp, true, nil
}

func saveCheckpoint(path string, cp checkpointFile) error {
	if path == "" {
		return errors.New("router-g3-induct: checkpoint path is required")
	}
	cp.SchemaVersion = checkpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
	return writeJSONAtomic(path, cp)
}

func readJSON(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readEventJSONL(path string) ([]g3reverse.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var events []g3reverse.Event
	for scanner.Scan() {
		var event g3reverse.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func readWinnerJSONL(path string) ([]g3reverse.WinnerIndexEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var entries []g3reverse.WinnerIndexEntry
	for scanner.Scan() {
		var entry g3reverse.WinnerIndexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func countRulesFile(path string) int {
	var rules []g3reverse.Rule
	if err := readJSON(path, &rules); err != nil {
		return 0
	}
	return len(rules)
}
