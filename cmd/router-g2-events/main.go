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
	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerg2/protocol"
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
	fs := flag.NewFlagSet("router-g2-events", flag.ContinueOnError)
	historyRoot := fs.String("history-root", "", "canonical research-only history root")
	catalogPath := fs.String("history-catalog", "", "canonical HistoryCatalog")
	outputDir := fs.String("output-dir", "", "explicit G2 research bundle directory")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G2_DISCOVERY_HMAC_KEY", "event manifest signing key env")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputDir == "" {
		return fmt.Errorf("--output-dir is required")
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
	baseConfig := g2prize.Config{
		HistoryRoot: *historyRoot, Catalog: &catalog,
		MaxSymbols:      protocol.G2SmokeMaxSymbols,
		MaxFundingLines: protocol.G2SmokeMaxFundingLines,
		MaxPriceLines:   protocol.G2SmokeMaxPriceLines,
		ProgressEvery:   5,
		OnProgress: func(progress g2prize.Progress) {
			encoded, _ := json.Marshal(progress)
			fmt.Fprintln(os.Stderr, string(encoded))
		},
	}
	rejections := map[string]map[string]int{
		"discovery": {}, "forward": {}, "walk_forward": {},
	}
	cutpoints, boundaries, err := g2prize.FitSkyCutpoints(ctx, baseConfig)
	if err != nil {
		return err
	}
	baseConfig.SkyCutpoints = cutpoints
	baseConfig.End = boundaries.DiscoveryEnd
	baseConfig.OnReject = func(reason string) { rejections["discovery"][reason]++ }
	events, err := g2prize.BuildEvents(ctx, baseConfig)
	if err != nil {
		return err
	}
	forwardConfig := baseConfig
	forwardConfig.Start = boundaries.ForwardStart
	forwardConfig.End = boundaries.WFStart
	forwardConfig.OnReject = func(reason string) { rejections["forward"][reason]++ }
	forwardEvents, err := g2prize.BuildEvents(ctx, forwardConfig)
	if err != nil {
		return err
	}
	wfConfig := baseConfig
	wfConfig.Start = boundaries.WFStart
	wfConfig.End = boundaries.End
	wfConfig.OnReject = func(reason string) { rejections["walk_forward"][reason]++ }
	wfEvents, err := g2prize.BuildEvents(ctx, wfConfig)
	if err != nil {
		return err
	}
	manifest, err := g2prize.NewManifest(
		events, catalog, cutpoints, protocol.Generation, protocol.RouterProtocolHash,
		protocol.G2SmokeMaxSymbols, protocol.G2SmokeMaxFundingLines, protocol.G2SmokeMaxPriceLines,
	)
	if err != nil {
		return err
	}
	manifest, err = g2prize.SignManifest(manifest, []byte(os.Getenv(*keyEnv)), "router-g2-events")
	if err != nil {
		return err
	}
	searchSpace, err := g2prize.DefaultSearchSpace()
	if err != nil {
		return err
	}
	familyDir := filepath.Join(*outputDir, "g2_prize")
	if err := os.MkdirAll(familyDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(familyDir, "events.jsonl"), events); err != nil {
		return err
	}
	forwardDir := filepath.Join(*outputDir, "g2_forward")
	wfDir := filepath.Join(*outputDir, "g2_wf")
	if err := os.MkdirAll(forwardDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(forwardDir, "events.jsonl"), forwardEvents); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(wfDir, "events.jsonl"), wfEvents); err != nil {
		return err
	}
	for path, value := range map[string]any{
		filepath.Join(*outputDir, "event_manifest.json"):       manifest,
		filepath.Join(*outputDir, "history_catalog.json"):      catalog,
		filepath.Join(*outputDir, "sky_cutpoints.json"):        cutpoints,
		filepath.Join(*outputDir, "partition_boundaries.json"): boundaries,
		filepath.Join(*outputDir, "search_space.json"):         searchSpace,
		filepath.Join(*outputDir, "generation_stats.json"): map[string]any{
			"eligible_by_partition_and_sky": map[string]map[string]int{
				"discovery": coverage(events), "forward": coverage(forwardEvents), "walk_forward": coverage(wfEvents),
			},
			"rejections_by_partition": rejections,
		},
	} {
		if err := writeJSON(path, value); err != nil {
			return err
		}
	}
	fmt.Printf(
		"{\"discovery_events\":%d,\"forward_events\":%d,\"wf_events\":%d,\"search_combinations\":%d}\n",
		len(events), len(forwardEvents), len(wfEvents), len(searchSpace.Combinations),
	)
	return nil
}

func coverage(events []g2prize.Event) map[string]int {
	out := map[string]int{}
	for _, event := range events {
		out[event.SkyState]++
	}
	return out
}

func writeJSONL(path string, events []g2prize.Event) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 256*1024)
	for _, event := range events {
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
