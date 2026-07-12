// Command router-g2-discovery runs the research-only G2 historical prize
// map (internal/routerdiscovery/g2prize) against a small, explicit fixture
// directory. It never reads the repository's real records/ history, never
// touches sealed exam material, and refuses to run with any unbounded scan
// configuration: --max-files and --max-lines must both be positive
// integers, and a --checkpoint path is mandatory.
//
// The only possible output is a Report of CANDIDATE_NOMINATION entries and
// accounted-for rejections. This command never promotes anything to
// RESEARCH_PASS — that transition belongs to independent forward
// falsification elsewhere and is out of scope for a backward-mining CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerg2/protocol"
	"lingxi/internal/routerg2/sky"
)

type familyFlags []string

func (f *familyFlags) String() string { return strings.Join(*f, ",") }

func (f *familyFlags) Set(value string) error {
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		*f = append(*f, token)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	return runWithCatalogValidator(args, stdout, stderr, historydata.ValidateCatalog)
}

func runWithCatalogValidator(
	args []string,
	stdout, stderr io.Writer,
	validateCatalog func(historydata.HistoryCatalog) error,
) error {
	fs := flag.NewFlagSet("router-g2-discovery", flag.ContinueOnError)
	root := fs.String("input-dir", "", "research-only fixture root directory, one subdirectory per family (must not point at real records/ history)")
	historyRoot := fs.String("history-root", "", "canonical history root used to deterministically regenerate events")
	historyCatalogPath := fs.String("history-catalog", "", "canonical HistoryCatalog path")
	searchSpacePath := fs.String("search-space", "", "path to the preregistered g2 search space JSON")
	maxFiles := fs.Int("max-files", 0, "maximum event files to scan; must be a positive integer, unbounded scanning is forbidden")
	maxLines := fs.Int("max-lines", 0, "maximum event lines to scan across all files; must be a positive integer, unbounded scanning is forbidden")
	progressEvery := fs.Int("progress-every", 100, "progress reporting interval by lines scanned")
	checkpoint := fs.String("checkpoint", "", "required checkpoint path binding input/config identity")
	output := fs.String("output", "", "optional report output path (default: stdout)")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G2_DISCOVERY_HMAC_KEY", "signed event manifest verification key environment variable")
	var families familyFlags
	fs.Var(&families, "family", "family to scan (repeat or comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*root) == "" {
		return errors.New("router-g2-discovery: --input-dir is required")
	}
	if strings.TrimSpace(*historyRoot) == "" || strings.TrimSpace(*historyCatalogPath) == "" {
		return errors.New("router-g2-discovery: --history-root and --history-catalog are required")
	}
	if strings.TrimSpace(*searchSpacePath) == "" {
		return errors.New("router-g2-discovery: --search-space is required")
	}
	if *maxFiles <= 0 || *maxFiles > protocol.BackwardMaxFiles {
		return errors.New("router-g2-discovery: --max-files must be a positive integer; unbounded scanning is forbidden")
	}
	if *maxLines <= 0 || *maxLines > protocol.BackwardMaxLines {
		return errors.New("router-g2-discovery: --max-lines must be a positive integer; unbounded scanning is forbidden")
	}
	if strings.TrimSpace(*checkpoint) == "" {
		return errors.New("router-g2-discovery: --checkpoint is required")
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	eventManifestPath := filepath.Join(absRoot, "event_manifest.json")
	eventManifestRaw, err := os.ReadFile(eventManifestPath)
	if err != nil {
		return fmt.Errorf("router-g2-discovery: frozen event manifest is required: %w", err)
	}
	var eventManifest g2prize.Manifest
	if err := json.Unmarshal(eventManifestRaw, &eventManifest); err != nil {
		return err
	}
	if err := eventManifest.Validate(); err != nil {
		return err
	}
	if eventManifest.Generation != protocol.Generation ||
		eventManifest.ProtocolHash != protocol.RouterProtocolHash ||
		eventManifest.SourceDataHash != protocol.HistoryDataHash ||
		eventManifest.MaxSymbols != protocol.G2SmokeMaxSymbols ||
		eventManifest.MaxFundingLines != protocol.G2SmokeMaxFundingLines ||
		eventManifest.MaxPriceLines != protocol.G2SmokeMaxPriceLines {
		return errors.New("router-g2-discovery: event manifest is not bound to frozen G2 protocol")
	}
	if err := g2prize.VerifyManifestSignature(eventManifest, []byte(os.Getenv(*keyEnv))); err != nil {
		return err
	}
	cutpointsRaw, err := os.ReadFile(filepath.Join(absRoot, "sky_cutpoints.json"))
	if err != nil {
		return fmt.Errorf("router-g2-discovery: frozen sky cutpoints are required: %w", err)
	}
	var cutpoints sky.Cutpoints
	if err := json.Unmarshal(cutpointsRaw, &cutpoints); err != nil {
		return err
	}
	cutpointsHash, err := cutpoints.Hash()
	if err != nil || !cutpoints.Frozen || cutpointsHash != eventManifest.SkyCutpointsHash {
		return errors.New("router-g2-discovery: sky cutpoints do not match frozen event manifest")
	}
	catalogRaw, err := os.ReadFile(*historyCatalogPath)
	if err != nil {
		return fmt.Errorf("router-g2-discovery: source HistoryCatalog is required: %w", err)
	}
	var sourceCatalog historydata.HistoryCatalog
	if err := json.Unmarshal(catalogRaw, &sourceCatalog); err != nil {
		return err
	}
	if validateCatalog == nil {
		return errors.New("router-g2-discovery: catalog validator is required")
	}
	if err := validateCatalog(sourceCatalog); err != nil {
		return err
	}
	derivationHash, err := g2prize.SelectedFileHash(sourceCatalog, eventManifest.MaxSymbols)
	if err != nil {
		return err
	}
	if sourceCatalog.DataHash != eventManifest.SourceDataHash ||
		sourceCatalog.SchemaHash != eventManifest.SourceSchemaHash ||
		sourceCatalog.DatasetID != eventManifest.SourceDatasetID ||
		derivationHash != eventManifest.DerivationHash {
		return errors.New("router-g2-discovery: event provenance does not match source HistoryCatalog")
	}
	boundariesRaw, err := os.ReadFile(filepath.Join(absRoot, "partition_boundaries.json"))
	if err != nil {
		return err
	}
	var boundaries g2prize.Boundaries
	if err := json.Unmarshal(boundariesRaw, &boundaries); err != nil {
		return err
	}
	expectedEvents, err := g2prize.BuildEvents(context.Background(), g2prize.Config{
		HistoryRoot: *historyRoot, Catalog: &sourceCatalog,
		SkyCutpoints: cutpoints,
		MaxSymbols:   eventManifest.MaxSymbols, MaxFundingLines: eventManifest.MaxFundingLines,
		MaxPriceLines: eventManifest.MaxPriceLines, End: boundaries.DiscoveryEnd,
	})
	if err != nil {
		return err
	}
	expectedDiscoveryHash, err := g2prize.HashEvents(expectedEvents)
	if err != nil {
		return err
	}
	if expectedDiscoveryHash != eventManifest.DiscoveryDataHash ||
		len(expectedEvents) != eventManifest.EventCount {
		return errors.New("router-g2-discovery: event bundle is not the deterministic frozen generator output")
	}
	eventManifestHash := eventManifest.ManifestHash
	space, err := loadSearchSpace(*searchSpacePath)
	if err != nil {
		return err
	}
	if err := space.Validate(); err != nil {
		return err
	}
	if space.Generation != eventManifest.Generation || space.ProtocolHash != eventManifest.ProtocolHash ||
		space.SourceDataHash != eventManifest.SourceDataHash {
		return errors.New("router-g2-discovery: search space is not bound to the frozen event manifest's generation/protocol/history")
	}
	if space.ID != protocol.BackwardSearchSpaceID ||
		space.DefinitionHash != protocol.BackwardSearchDefinitionHash ||
		len(space.Combinations) != protocol.BackwardRegisteredCombos {
		return errors.New("router-g2-discovery: search space is not the single frozen G2 definition")
	}
	spaceHash, _, err := datatruth.HashJSON(space)
	if err != nil {
		return err
	}

	cfg := runConfig{
		RootDir: absRoot, Families: append([]string(nil), families...),
		MaxFiles: *maxFiles, MaxLines: *maxLines,
		SearchSpacePath: *searchSpacePath, SearchSpaceHash: spaceHash,
		EventManifestHash: eventManifestHash,
		MatchWindowMS:     protocol.BackwardMatchWindowMs,
		NeutralBandBPS:    protocol.NeutralBandBPS,
		PermutationDraws:  protocol.BackwardPermutationDraws,
		BootstrapSamples:  protocol.BackwardBootstrapSamples,
		Seed:              protocol.BackwardSeed,
		FamilyWiseAlpha:   protocol.BackwardFamilyAlpha,
		Generation:        space.Generation, ProtocolHash: space.ProtocolHash,
	}
	configHash, err := configFingerprint(cfg)
	if err != nil {
		return err
	}
	files, err := discoverEventFiles(cfg.RootDir, cfg.Families, cfg.MaxFiles)
	if err != nil {
		return err
	}
	inputHash, err := inputFingerprint(files)
	if err != nil {
		return err
	}

	cp, hasCheckpoint, err := loadCheckpoint(*checkpoint)
	if err != nil {
		return err
	}
	if hasCheckpoint {
		if !cp.Done {
			return errors.New("router-g2-discovery: checkpoint exists but is not marked done; bounded fixture scans do not support partial resume, delete the checkpoint and rerun")
		}
		if cp.ConfigHash != configHash || cp.InputHash != inputHash {
			return errors.New("router-g2-discovery: checkpoint input/config identity drifted; refusing to reuse a stale checkpoint")
		}
		if cp.Report.DiscoveryDataHash != eventManifest.DiscoveryDataHash ||
			cp.Report.EventsInput != eventManifest.EventCount {
			return errors.New("router-g2-discovery: checkpoint report does not match frozen event manifest")
		}
		// A completed checkpoint is a bound run receipt, not an authority for
		// the report payload. Recompute from frozen inputs so even a party
		// able to rewrite and rehash the checkpoint cannot inject nominations.
	}

	events, _, err := loadEvents(scanConfig{
		Files: files, MaxLines: cfg.MaxLines, ProgressEvery: *progressEvery, Progress: stderr,
	})
	if err != nil {
		return err
	}
	runTime := time.Unix(0, 0).UTC()
	for _, event := range events {
		if event.DecisionTime.After(runTime) {
			runTime = event.DecisionTime.UTC()
		}
	}

	report, err := g2prize.Run(g2prize.Options{
		Events:           events,
		SearchSpace:      space,
		NeutralBandBPS:   cfg.NeutralBandBPS,
		MatchWindow:      time.Duration(cfg.MatchWindowMS) * time.Millisecond,
		PermutationDraws: cfg.PermutationDraws,
		BootstrapSamples: cfg.BootstrapSamples,
		Seed:             cfg.Seed,
		FamilyWiseAlpha:  cfg.FamilyWiseAlpha,
		Generation:       cfg.Generation,
		ProtocolHash:     cfg.ProtocolHash,
		Now:              runTime,
		Progress:         stderr,
		ProgressEvery:    *progressEvery,
	})
	if err != nil {
		return err
	}
	if report.DiscoveryDataHash != eventManifest.DiscoveryDataHash ||
		report.EventsInput != eventManifest.EventCount {
		return errors.New("router-g2-discovery: selected events do not match frozen event manifest")
	}

	if err := saveCheckpoint(*checkpoint, checkpointFile{
		ConfigHash: configHash, InputHash: inputHash, Done: true, Report: report,
	}); err != nil {
		return err
	}
	return writeReport(stdout, *output, report)
}

func loadSearchSpace(path string) (g2prize.SearchSpace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return g2prize.SearchSpace{}, err
	}
	var space g2prize.SearchSpace
	if err := json.Unmarshal(raw, &space); err != nil {
		return g2prize.SearchSpace{}, err
	}
	return space, nil
}

func writeReport(stdout io.Writer, path string, report g2prize.Report) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if strings.TrimSpace(path) == "" {
		_, err := stdout.Write(raw)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
