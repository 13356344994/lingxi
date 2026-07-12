package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/features"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g5-feature-context", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	profileID := fs.String("profile", "", "profile id: okx_1h or okx_5m")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	catalogSymbolLimit := fs.Int("catalog-symbol-limit", -1, "symbol limit; 0 means full catalog")
	shardDuration := fs.String("shard-duration", "168h", "time shard duration")
	capitalUSDT := fs.Float64("capital-usdt", features.DefaultCapitalUSDT, "reference capital for cost/capacity scores")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow TRUNCATED_SMOKE subset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *profileID == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g5-feature-context: --catalog, --history-root, --profile, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g5-feature-context: --max-files and --max-lines must be positive")
	}
	if *catalogSymbolLimit < 0 {
		return errors.New("router-g5-feature-context: --catalog-symbol-limit is required (0 for full catalog)")
	}
	if *profileID != profitmap.ProfileOKX1H && *profileID != profitmap.ProfileOKX5M {
		return fmt.Errorf("router-g5-feature-context: unsupported profile %q", *profileID)
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	cfg := features.RunConfig{
		SchemaVersion:       "lingxi.routerg5.features.config.v1",
		ProfileID:           *profileID,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		CatalogSymbolLimit:  *catalogSymbolLimit,
		ShardDuration:       *shardDuration,
		CapitalUSDT:         *capitalUSDT,
		ProgressEvery:       1000,
		Generation:          g5protocol.Generation,
		ProtocolHash:        g5protocol.RouterProtocolHash,
		HistoryDataHash:     g5protocol.HistoryDataHash,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
	}
	runner := features.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(lines int64, symbol string) {
			fmt.Fprintf(os.Stderr, "progress profile=%s symbol=%s lines=%d\n", *profileID, symbol, lines)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"profile\":%q,\"rows\":%d,\"feature_hash\":%q,\"shards\":%d,\"exam_actions\":%d}\n",
		result.ProfileID, result.Rows, result.Manifest.FeatureHash, result.Manifest.ShardCount, g5protocol.ExamActions)
	return nil
}
