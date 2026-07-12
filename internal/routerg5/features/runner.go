package features

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const configSchemaVersion = "lingxi.routerg5.features.config.v1"

// RunConfig bounds one profile feature-context materialization run.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	ProfileID           string `json:"profile_id"`
	HistoryRoot         string `json:"history_root"`
	OutputDir           string `json:"output_dir"`
	MaxFiles            int    `json:"max_files"`
	MaxLines            int    `json:"max_lines"`
	CatalogSymbolLimit  int    `json:"catalog_symbol_limit"`
	ShardDuration       string `json:"shard_duration"`
	CapitalUSDT         float64
	ProgressEvery       int64  `json:"progress_every"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
	AllowTruncatedSmoke bool   `json:"allow_truncated_smoke"`
}

func (c RunConfig) ConfigHash() string {
	hash, err := ConfigFingerprint(c)
	if err != nil {
		panic(err)
	}
	return hash
}

// ConfigFingerprint hashes the run config for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = configSchemaVersion
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.ProfileID == "" || c.HistoryRoot == "" || c.OutputDir == "" {
		return errors.New("features: profile, history-root, output-dir required")
	}
	if !ProtocolProfileID(c.ProfileID) {
		return fmt.Errorf("features: unsupported profile %q", c.ProfileID)
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("features: max-files and max-lines must be positive")
	}
	if c.CatalogSymbolLimit < 0 {
		return errors.New("features: catalog-symbol-limit must be non-negative")
	}
	if c.CapitalUSDT <= 0 {
		c.CapitalUSDT = DefaultCapitalUSDT
	}
	return nil
}

func (c RunConfig) shardDuration() time.Duration {
	if c.ShardDuration == "" {
		return 7 * 24 * time.Hour
	}
	d, err := time.ParseDuration(c.ShardDuration)
	if err != nil || d <= 0 {
		return 7 * 24 * time.Hour
	}
	return d
}

// Runner orchestrates bounded PIT feature context generation.
type Runner struct {
	Config         RunConfig
	Catalog        historydata.HistoryCatalog
	CheckpointPath string
	OnProgress     func(lines int64, symbol string)
}

// RunResult summarizes one completed or resumed run.
type RunResult struct {
	ProfileID string
	Manifest  ContextManifest
	Rows      int
}

// Run executes bounded feature context generation with checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g5protocol.HistoryDataHash {
		return RunResult{}, errors.New("features: catalog data hash mismatch")
	}
	if err := historydata.ValidateCatalog(r.Catalog); err != nil {
		return RunResult{}, err
	}
	profile, err := ProfileFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	schema, err := FrozenFeatureSchema()
	if err != nil {
		return RunResult{}, err
	}
	selection, err := profitmap.SelectPrimaryFiles(r.Catalog, r.Config.ProfileID, r.Config.MaxFiles, r.Config.CatalogSymbolLimit)
	if err != nil {
		return RunResult{}, err
	}
	if _, err := profitmap.ValidateSelectionScope(selection, r.Config.CatalogSymbolLimit, r.Config.AllowTruncatedSmoke); err != nil {
		return RunResult{}, fmt.Errorf("features: selection scope: %w", err)
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	if len(symbols) == 0 {
		return RunResult{}, errors.New("features: no symbols selected")
	}
	btcRel := profile.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, btcRel); err != nil {
		return RunResult{}, err
	}
	btcBars, err := profitmap.LoadBars(filepath.Join(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	boundaries, err := profitmap.ComputeBoundaries(profile.ProfileSpec, btcBars)
	if err != nil {
		return RunResult{}, err
	}
	bHash, err := profitmap.HashBoundaries(boundaries)
	if err != nil {
		return RunResult{}, err
	}
	funding, _ := loadOptionalFunding(r.Catalog, r.Config.HistoryRoot, "BTC-USDT-SWAP", r.Config.MaxLines)
	market := MarketSnapshot{BTCBars: btcBars, Funding: funding}
	cfgHash := r.Config.ConfigHash()
	selectedFilesHash, _, err := datatruth.HashJSON(selection.Files)
	if err != nil {
		return RunResult{}, err
	}
	catalogHash := r.Catalog.DataHash
	if err := os.MkdirAll(r.Config.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}

	cp := Checkpoint{
		ProfileID: r.Config.ProfileID, Generation: r.Config.Generation,
		ProtocolHash: r.Config.ProtocolHash, HistoryDataHash: r.Config.HistoryDataHash,
		CatalogHash: catalogHash, ConfigHash: cfgHash, BoundariesHash: bHash,
		SelectedFilesHash: selectedFilesHash, FeatureHash: schema.FeatureHash,
		OutputDir: r.Config.OutputDir,
	}
	if loaded, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(loaded, r.Config, catalogHash, bHash, selectedFilesHash, schema.FeatureHash); err != nil {
			return RunResult{}, err
		}
		cp = loaded
	}

	shardDur := r.Config.shardDuration()
	shardStarts := shardBoundaries(boundaries.Start, boundaries.End, shardDur)
	if cp.NextShardIndex >= len(shardStarts) {
		cp.Done = true
	}

	var linesProcessed int64 = cp.LinesProcessed

	for shardIdx := cp.NextShardIndex; shardIdx < len(shardStarts) && !cp.Done; shardIdx++ {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}
		shardStart := shardStarts[shardIdx]
		shardEnd := shardStart.Add(shardDur)
		if shardEnd.After(boundaries.End) {
			shardEnd = boundaries.End
		}
		times := DecisionTimesFromBTC(btcBars, profile, shardStart, shardEnd)
		if len(times) == 0 {
			cp.CompletedShards = append(cp.CompletedShards, shardIdx)
			cp.NextShardIndex = shardIdx + 1
			continue
		}

		accum := map[time.Time]map[string]SymbolLocal{}
		btcByTime := map[time.Time]BTCFeatures{}
		for _, symbol := range symbols {
			if err := ctx.Err(); err != nil {
				return RunResult{}, err
			}
			rel := profile.PrimaryFamily + "/" + symbol + ".jsonl"
			if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
				return RunResult{}, err
			}
			bars, err := profitmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
			if err != nil {
				return RunResult{}, err
			}
			linesProcessed += int64(len(bars))
			listing := ListingRule{}
			for _, t := range times {
				if !EligibleAt(PITUniverseInput{Symbol: symbol, Bars: bars, Listing: listing}, profile, t) {
					continue
				}
				if _, ok := btcByTime[t]; !ok {
					btcByTime[t] = ComputeBTCFeatures(market.BTCBars, market.Funding, profile, t)
				}
				loc, ok := ComputeSymbolLocal(symbol, bars, market.BTCBars, profile, t, r.Config.CapitalUSDT)
				if !ok {
					continue
				}
				if accum[t] == nil {
					accum[t] = map[string]SymbolLocal{}
				}
				accum[t][symbol] = loc
			}
			cp.LinesProcessed = linesProcessed
			if r.OnProgress != nil {
				r.OnProgress(linesProcessed, symbol)
			}
		}

		var shardRows []DecisionContext
		for _, t := range times {
			locals := accum[t]
			if len(locals) == 0 {
				continue
			}
			btc := btcByTime[t]
			rankBySymbol := ApplyCrossSectionRanks(locals)
			partition := boundaries.PartitionAt(t)
			syms := make([]string, 0, len(locals))
			for sym := range locals {
				syms = append(syms, sym)
			}
			sortStrings(syms)
			for _, sym := range syms {
				row, err := BuildDecisionContext(schema, profile.ID, sym, t, partition, btc, locals[sym], rankBySymbol[sym])
				if err != nil {
					return RunResult{}, err
				}
				shardRows = append(shardRows, row)
			}
		}

		shardName := fmt.Sprintf("feature_context_%s_%04d.jsonl", profile.ID, shardIdx)
		shardMeta, err := WriteContextShard(r.Config.OutputDir, ShardManifest{
			ProfileID: profile.ID, ShardIndex: shardIdx,
			StartTime: shardStart, EndTime: shardEnd, FileName: shardName,
		}, shardRows)
		if err != nil {
			return RunResult{}, err
		}
		cp.ShardManifests = append(cp.ShardManifests, shardMeta)
		cp.TotalRows += shardMeta.RowCount
		cp.CompletedShards = append(cp.CompletedShards, shardIdx)
		cp.NextShardIndex = shardIdx + 1
		if cp.NextShardIndex >= len(shardStarts) {
			cp.Done = true
		}
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
	}

	manifest := ContextManifest{
		Generation:        r.Config.Generation,
		ProtocolHash:      r.Config.ProtocolHash,
		HistoryDataHash:   r.Config.HistoryDataHash,
		ProfileID:         profile.ID,
		FeatureHash:       schema.FeatureHash,
		ConfigHash:        cfgHash,
		CatalogHash:       catalogHash,
		BoundariesHash:    bHash,
		SelectedFilesHash: selectedFilesHash,
		ShardCount:        len(cp.ShardManifests),
		TotalRows:         cp.TotalRows,
		Shards:            cp.ShardManifests,
		ExamActions:       g5protocol.ExamActions,
		CreatedAt:         time.Now().UTC(),
	}
	if err := WriteContextManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	sealed, err := sealManifest(manifest)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{ProfileID: profile.ID, Manifest: sealed, Rows: cp.TotalRows}, nil
}

func shardBoundaries(start, end time.Time, dur time.Duration) []time.Time {
	var out []time.Time
	for t := start; t.Before(end); t = t.Add(dur) {
		out = append(out, t)
	}
	return out
}

func loadOptionalFunding(catalog historydata.HistoryCatalog, root, symbol string, maxLines int) ([]profitmap.FundingPoint, error) {
	rel := "okx_funding/" + symbol + ".jsonl"
	stats, declared := profitmap.CatalogFile(catalog, rel)
	if !declared || stats.Empty {
		return nil, nil
	}
	if err := profitmap.VerifyCatalogFile(catalog, root, rel); err != nil {
		return nil, err
	}
	return profitmap.LoadSymbolFunding(root, symbol, maxLines)
}
