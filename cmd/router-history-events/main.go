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
	"lingxi/internal/hierarchicalrouter"
	"lingxi/internal/routerdiscovery/historyevents"
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
	fs := flag.NewFlagSet("router-history-events", flag.ContinueOnError)
	root := fs.String("history-root", "", "research-only history root")
	catalogPath := fs.String("history-catalog", "", "verified HistoryCatalog for the exact history root")
	outputDir := fs.String("output-dir", "", "explicit research event output directory")
	maxSymbols := fs.Int("max-symbols", 0, "positive symbol cap")
	maxFundingLines := fs.Int("max-funding-lines", 0, "positive per-symbol funding line cap")
	maxPriceLines := fs.Int("max-price-lines", 0, "positive per-symbol price line cap")
	progressEvery := fs.Int("progress-every", 10, "symbol progress interval")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_DISCOVERY_HMAC_KEY", "event manifest signing key environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputDir == "" {
		return fmt.Errorf("--output-dir is required")
	}
	catalogRaw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return fmt.Errorf("--history-catalog is required: %w", err)
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		return err
	}
	if err := historydata.ValidateCatalog(catalog); err != nil {
		return err
	}
	if *maxSymbols != hierarchicalrouter.BackwardSmokeMaxSymbols ||
		*maxFundingLines != hierarchicalrouter.BackwardSmokeMaxFundingLines ||
		*maxPriceLines != hierarchicalrouter.BackwardSmokeMaxPriceLines {
		return fmt.Errorf("bounded smoke limits must match the frozen router protocol")
	}
	events, err := historyevents.BuildFundingPostSettlement(ctx, historyevents.Config{
		HistoryRoot: *root, MaxSymbols: *maxSymbols,
		MaxFundingLines: *maxFundingLines, MaxPriceLines: *maxPriceLines,
		ProgressEvery: *progressEvery,
		Catalog:       &catalog,
		OnProgress: func(progress historyevents.Progress) {
			raw, _ := json.Marshal(progress)
			fmt.Fprintln(os.Stderr, string(raw))
		},
	})
	if err != nil {
		return err
	}
	familyDir := filepath.Join(*outputDir, "funding_post_settlement")
	if err := os.MkdirAll(familyDir, 0o755); err != nil {
		return err
	}
	finalPath := filepath.Join(familyDir, "events.jsonl")
	tmp := finalPath + ".tmp"
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
	if err := os.Rename(tmp, finalPath); err != nil {
		return err
	}
	manifest, err := historyevents.NewManifest(events, catalog, *maxSymbols, *maxFundingLines, *maxPriceLines)
	if err != nil {
		return err
	}
	key := []byte(os.Getenv(*keyEnv))
	manifest, err = historyevents.SignManifest(manifest, key, "router-history-events")
	if err != nil {
		return err
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestTmp := filepath.Join(*outputDir, "event_manifest.json.tmp")
	manifestPath := filepath.Join(*outputDir, "event_manifest.json")
	if err := os.WriteFile(manifestTmp, append(manifestRaw, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(manifestTmp, manifestPath); err != nil {
		return err
	}
	catalogCopy, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	catalogTmp := filepath.Join(*outputDir, "history_catalog.json.tmp")
	catalogOut := filepath.Join(*outputDir, "history_catalog.json")
	if err := os.WriteFile(catalogTmp, append(catalogCopy, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(catalogTmp, catalogOut); err != nil {
		return err
	}
	fmt.Printf("{\"events\":%d,\"family\":\"funding_post_settlement\"}\n", len(events))
	return nil
}
