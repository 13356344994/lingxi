package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("router-g3-events", flag.ContinueOnError)
	historyRoot := fs.String("history-root", "", "canonical research-only history root")
	catalogPath := fs.String("history-catalog", "", "canonical HistoryCatalog")
	outputDir := fs.String("output-dir", "", "explicit G3 research bundle directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path for resumable generation")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", "event manifest signing key env")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputDir == "" || *checkpointPath == "" {
		return fmt.Errorf("--output-dir and --checkpoint are required")
	}
	signingKey := []byte(os.Getenv(*keyEnv))
	if len(signingKey) == 0 {
		return fmt.Errorf("HMAC key environment variable %s is empty", *keyEnv)
	}
	raw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	if err := historydata.ValidateCatalog(catalog); err != nil {
		return err
	}
	maxSymbols := protocol.G3SmokeMaxSymbols
	maxFunding := protocol.G3SmokeMaxFundingLines
	maxPrice := protocol.G3SmokeMaxPriceLines
	maxFamily := protocol.G3SmokeMaxPriceLines
	baseConfig := g3reverse.Config{
		HistoryRoot: *historyRoot, Catalog: &catalog,
		MaxSymbols: maxSymbols, MaxFundingLines: maxFunding,
		MaxPriceLines: maxPrice, MaxFamilyLines: maxFamily,
		ProgressEvery: 5,
		OnProgress: func(progress g3reverse.Progress) {
			encoded, _ := json.Marshal(progress)
			fmt.Fprintln(os.Stderr, string(encoded))
		},
	}
	_, boundaries, err := g3reverse.AnchorFundingSpan(*historyRoot, maxFunding)
	if err != nil {
		return err
	}
	if err := g3reverse.VerifySelectedFiles(&catalog, *historyRoot, maxSymbols); err != nil {
		return err
	}
	rejections := map[string]map[string]int{
		string(g3reverse.PartitionInduction): {},
		string(g3reverse.PartitionRecheck):   {},
		string(g3reverse.PartitionForward):   {},
		string(g3reverse.PartitionWalkFwd):   {},
	}
	cutpoints, err := g3reverse.FitSkyCutpoints(ctx, baseConfig, boundaries)
	if err != nil {
		return err
	}
	baseConfig.SkyCutpoints = cutpoints
	cutpointsHash, err := cutpoints.Hash()
	if err != nil {
		return err
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		return err
	}
	selectedFileHash, err := g3reverse.SelectedFileHash(catalog, maxSymbols)
	if err != nil {
		return err
	}
	configHash, err := g3reverse.ConfigHash(maxSymbols, maxFunding, maxPrice, maxFamily, cutpointsHash, boundariesHash)
	if err != nil {
		return err
	}
	absoluteOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	binding := checkpointBinding{
		CatalogDataHash: catalog.DataHash, CatalogSchemaHash: catalog.SchemaHash,
		DatasetID: catalog.DatasetID, SelectedFileHash: selectedFileHash,
		ConfigHash: configHash, CutpointsHash: cutpointsHash,
		BoundariesHash: boundariesHash, OutputDir: absoluteOutput,
	}
	cp, exists, err := loadCheckpoint(*checkpointPath)
	if err != nil {
		return err
	}
	if exists {
		if err := cp.validateBinding(binding); err != nil {
			return err
		}
	} else {
		cp = newCheckpoint(binding)
	}
	if err := os.MkdirAll(absoluteOutput, 0o755); err != nil {
		return err
	}
	type partSpec struct {
		name   g3reverse.Partition
		reject map[string]int
	}
	specs := []partSpec{
		{g3reverse.PartitionInduction, rejections[string(g3reverse.PartitionInduction)]},
		{g3reverse.PartitionRecheck, rejections[string(g3reverse.PartitionRecheck)]},
		{g3reverse.PartitionForward, rejections[string(g3reverse.PartitionForward)]},
		{g3reverse.PartitionWalkFwd, rejections[string(g3reverse.PartitionWalkFwd)]},
	}
	dirs := map[g3reverse.Partition]string{
		g3reverse.PartitionInduction: "g3_induction",
		g3reverse.PartitionRecheck:   "g3_recheck",
		g3reverse.PartitionForward:   "g3_forward",
		g3reverse.PartitionWalkFwd:   "g3_wf",
	}
	eventsByPartition := map[g3reverse.Partition][]g3reverse.Event{}
	for _, spec := range specs {
		start, end := boundaries.Window(spec.name)
		name := string(spec.name)
		if record, ok := cp.Partitions[name]; ok {
			if !record.Start.Equal(start) || !record.End.Equal(end) {
				return fmt.Errorf("checkpoint partition %s boundary drift", name)
			}
			events, err := validatePartitionRecord(absoluteOutput, name, record, selectedFileHash, configHash)
			if err != nil {
				return err
			}
			eventsByPartition[spec.name] = events
			rejections[name] = record.Rejections
			continue
		}
		cfg := baseConfig
		cfg.Start = start
		cfg.End = end
		cfg.OnReject = func(reason string) { spec.reject[reason]++ }
		cfg.OnProgress = func(p g3reverse.Progress) {
			p.Partition = string(spec.name)
			encoded, _ := json.Marshal(p)
			fmt.Fprintln(os.Stderr, string(encoded))
		}
		events, err := g3reverse.BuildEvents(ctx, cfg)
		if err != nil {
			return err
		}
		eventsByPartition[spec.name] = events
		familyDir := filepath.Join(absoluteOutput, dirs[spec.name])
		if err := os.MkdirAll(familyDir, 0o755); err != nil {
			return err
		}
		eventsPath := filepath.Join(dirs[spec.name], "events.jsonl")
		if err := writeJSONL(filepath.Join(absoluteOutput, eventsPath), events); err != nil {
			return err
		}
		eventHash, err := g3reverse.HashEvents(events)
		if err != nil {
			return err
		}
		inputHash, err := partitionInputHash(name, start, end, selectedFileHash, configHash)
		if err != nil {
			return err
		}
		cp.Partitions[name] = partitionCheckpoint{
			Start: start, End: end, InputHash: inputHash, EventHash: eventHash,
			EventCount: len(events), EventsPath: eventsPath, Rejections: spec.reject,
		}
		if err := saveCheckpoint(*checkpointPath, cp); err != nil {
			return err
		}
	}
	induction := eventsByPartition[g3reverse.PartitionInduction]
	recheck := eventsByPartition[g3reverse.PartitionRecheck]
	forward := eventsByPartition[g3reverse.PartitionForward]
	wf := eventsByPartition[g3reverse.PartitionWalkFwd]
	winner, err := g3reverse.BuildWinnerIndex(induction, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		return err
	}
	manifest, err := g3reverse.NewManifest(
		induction, recheck, forward, wf, winner, catalog, cutpoints, boundaries,
		protocol.Generation, protocol.RouterProtocolHash,
		maxSymbols, maxFunding, maxPrice, maxFamily,
	)
	if err != nil {
		return err
	}
	manifest, err = g3reverse.SignManifest(manifest, signingKey, "router-g3-events")
	if err != nil {
		return err
	}
	if cp.Done && cp.ManifestHash != manifest.ManifestHash {
		return fmt.Errorf("router-g3-events: completed checkpoint manifest drift")
	}
	stats := g3reverse.BuildGenerationStats(induction, recheck, forward, wf, rejections)
	if err := writeJSONL(filepath.Join(absoluteOutput, "g3_induction", "winner_index.jsonl"), winner.Entries); err != nil {
		return err
	}
	for path, value := range map[string]any{
		filepath.Join(absoluteOutput, "event_manifest.json"):       manifest,
		filepath.Join(absoluteOutput, "history_catalog.json"):      catalog,
		filepath.Join(absoluteOutput, "sky_cutpoints.json"):        cutpoints,
		filepath.Join(absoluteOutput, "partition_boundaries.json"): boundaries,
		filepath.Join(absoluteOutput, "g3_generation_stats.json"):  stats,
	} {
		if err := writeJSON(path, value); err != nil {
			return err
		}
	}
	cp.Done = true
	cp.ManifestHash = manifest.ManifestHash
	if err := saveCheckpoint(*checkpointPath, cp); err != nil {
		return err
	}
	fmt.Printf(
		"{\"induction_events\":%d,\"recheck_events\":%d,\"forward_events\":%d,\"wf_events\":%d,\"winners\":%d,\"exam_actions\":%d}\n",
		len(induction), len(recheck), len(forward), len(wf), len(winner.Entries), protocol.ExamActions,
	)
	return nil
}

func writeJSONL(path string, events any) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	switch rows := events.(type) {
	case []g3reverse.Event:
		for _, event := range rows {
			raw, err := json.Marshal(event)
			if err != nil {
				file.Close()
				return err
			}
			if _, err := writer.Write(append(raw, '\n')); err != nil {
				file.Close()
				return err
			}
		}
	case []g3reverse.WinnerIndexEntry:
		for _, entry := range rows {
			raw, err := json.Marshal(entry)
			if err != nil {
				file.Close()
				return err
			}
			if _, err := writer.Write(append(raw, '\n')); err != nil {
				file.Close()
				return err
			}
		}
	default:
		file.Close()
		return fmt.Errorf("unsupported jsonl payload")
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
