package g3reverse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg2/sorter"
	"lingxi/internal/routerg3/protocol"
)

var allHorizons = []Horizon{Horizon1h, Horizon4h, Horizon24h}

func partitionTime(start time.Time, span time.Duration, permille int) time.Time {
	// Divide before multiply to avoid int64 overflow on multi-year spans.
	offset := span / time.Duration(1000) * time.Duration(permille)
	return start.Add(offset)
}

// ComputeBoundaries anchors four 40/20/20/20 partitions on the okx_funding
// available span with 168h embargoes between adjacent slices.
func ComputeBoundaries(funding []fundingPoint) (Boundaries, error) {
	if len(funding) < 3 {
		return Boundaries{}, errors.New("g3reverse: insufficient BTC funding history for partitions")
	}
	start := funding[0].Time
	end := funding[len(funding)-1].Time.Add(time.Millisecond)
	span := end.Sub(start)
	embargo := time.Duration(protocol.PartitionEmbargoHours) * time.Hour
	b := Boundaries{
		Start:           start,
		InductionEnd:    partitionTime(start, span, protocol.ReverseInductionPermille),
		RecheckEnd:      partitionTime(start, span, protocol.ReverseInductionPermille+protocol.RecheckPermille),
		ForwardEnd:      partitionTime(start, span, protocol.ReverseInductionPermille+protocol.RecheckPermille+protocol.ForwardPermille),
		End:             end,
		EmbargoHours:    protocol.PartitionEmbargoHours,
		InductionPerm:   protocol.ReverseInductionPermille,
		RecheckPerm:     protocol.RecheckPermille,
		ForwardPerm:     protocol.ForwardPermille,
		WalkForwardPerm: protocol.WalkForwardPermille,
	}
	b.RecheckStart = b.InductionEnd.Add(embargo)
	b.ForwardStart = b.RecheckEnd.Add(embargo)
	b.WFStart = b.ForwardEnd.Add(embargo)
	if !b.RecheckStart.Before(b.RecheckEnd) {
		return Boundaries{}, errors.New("g3reverse: history span cannot satisfy recheck embargo")
	}
	if !b.ForwardStart.Before(b.ForwardEnd) {
		return Boundaries{}, errors.New("g3reverse: history span cannot satisfy forward embargo")
	}
	if !b.WFStart.Before(b.End) {
		return Boundaries{}, errors.New("g3reverse: history span cannot satisfy walk-forward embargo")
	}
	return b, nil
}

// FitSkyCutpoints fits frozen sky cutpoints using induction-partition hourly
// decision points only; never reads outcomes.
func FitSkyCutpoints(ctx context.Context, cfg Config, boundaries Boundaries) (sky.Cutpoints, error) {
	if cfg.Catalog == nil || cfg.HistoryRoot == "" {
		return sky.Cutpoints{}, errors.New("g3reverse: catalog/history root required for sky fit")
	}
	absoluteRoot, err := filepath.Abs(cfg.HistoryRoot)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	for _, rel := range []string{"okx_1h/BTC-USDT-SWAP.jsonl", "okx_funding/BTC-USDT-SWAP.jsonl"} {
		if err := verifyCatalogFile(cfg.Catalog, absoluteRoot, rel); err != nil {
			return sky.Cutpoints{}, err
		}
	}
	prices, err := loadPrices(filepath.Join(absoluteRoot, "okx_1h", "BTC-USDT-SWAP.jsonl"), cfg.MaxPriceLines)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	funding, err := loadOKXFunding(filepath.Join(absoluteRoot, "okx_funding", "BTC-USDT-SWAP.jsonl"), cfg.MaxFundingLines)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	series := sky.Series{}
	for _, bar := range prices {
		series.Bars = append(series.Bars, sky.Bar{OpenTime: bar.OpenTime, Close: bar.Close})
	}
	for _, point := range funding {
		series.Funding = append(series.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	var samples []sky.FeatureSet
	for _, bar := range prices {
		select {
		case <-ctx.Done():
			return sky.Cutpoints{}, ctx.Err()
		default:
		}
		decision := bar.OpenTime.Add(time.Hour)
		if decision.Before(boundaries.Start) || !decision.Before(boundaries.InductionEnd) {
			continue
		}
		features := sky.ComputeFeatures(series, decision)
		if !features.OOD {
			samples = append(samples, features)
		}
	}
	cutpoints, err := sky.Fit(sky.FitPartition, samples, protocol.SkyQuantiles)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	return cutpoints.Freeze(), nil
}

// BuildEvents deterministically regenerates the G3 event pool from catalog-bound
// history files for the configured [Start, End) window.
func BuildEvents(ctx context.Context, cfg Config) ([]Event, error) {
	if cfg.HistoryRoot == "" || cfg.MaxSymbols <= 0 || cfg.MaxFundingLines <= 0 || cfg.MaxPriceLines <= 0 {
		return nil, errors.New("g3reverse: bounded root/max-symbols/max-lines are required")
	}
	if cfg.Catalog == nil || cfg.Catalog.DataHash == "" {
		return nil, errors.New("g3reverse: verified HistoryCatalog is required")
	}
	if !cfg.SkyCutpoints.Frozen {
		return nil, errors.New("g3reverse: frozen Sky cutpoints are required")
	}
	absoluteRoot, err := filepath.Abs(cfg.HistoryRoot)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absoluteRoot) != filepath.Clean(cfg.Catalog.RootDir) {
		return nil, errors.New("g3reverse: history root does not match catalog root")
	}
	if cfg.ProgressEvery <= 0 {
		cfg.ProgressEvery = 1
	}
	maxFamily := cfg.MaxFamilyLines
	if maxFamily <= 0 {
		maxFamily = cfg.MaxPriceLines
	}
	for _, rel := range []string{
		filepath.Join(familyOKX1h, "BTC-USDT-SWAP.jsonl"),
		filepath.Join(familyOKXFunding, "BTC-USDT-SWAP.jsonl"),
	} {
		if err := verifyCatalogFile(cfg.Catalog, absoluteRoot, rel); err != nil {
			return nil, err
		}
	}
	btcPrices, err := loadPrices(filepath.Join(cfg.HistoryRoot, "okx_1h", "BTC-USDT-SWAP.jsonl"), cfg.MaxPriceLines)
	if err != nil {
		return nil, err
	}
	btcFunding, err := loadOKXFunding(filepath.Join(cfg.HistoryRoot, "okx_funding", "BTC-USDT-SWAP.jsonl"), cfg.MaxFundingLines)
	if err != nil {
		return nil, err
	}
	btcSeries := sky.Series{}
	for _, bar := range btcPrices {
		btcSeries.Bars = append(btcSeries.Bars, sky.Bar{OpenTime: bar.OpenTime, Close: bar.Close})
	}
	for _, point := range btcFunding {
		btcSeries.Funding = append(btcSeries.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	symbols := selectedInstruments(*cfg.Catalog, cfg.MaxSymbols)
	var events []Event
	for index, instrument := range symbols {
		select {
		case <-ctx.Done():
			return events, ctx.Err()
		default:
		}
		symbol := CanonicalSymbol(instrument)
		symbolEvents, err := buildInstrumentEvents(cfg, absoluteRoot, instrument, symbol, btcSeries, maxFamily)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		events = append(events, symbolEvents...)
		if cfg.OnProgress != nil && (index+1)%cfg.ProgressEvery == 0 {
			cfg.OnProgress(Progress{SymbolsDone: index + 1, SymbolsTotal: len(symbols), Events: len(events), Symbol: symbol})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].DecisionTime.Equal(events[j].DecisionTime) {
			return events[i].ID < events[j].ID
		}
		return events[i].DecisionTime.Before(events[j].DecisionTime)
	})
	return events, nil
}

type symbolSeries struct {
	prices       []priceBar
	okxFunding   []fundingPoint
	bybitFunding []fundingPoint
	rubikTaker   []rubikTakerPoint
	rubikRatio   []rubikRatioPoint
}

func buildInstrumentEvents(
	cfg Config,
	root, okxInstrument, symbol string,
	btcSeries sky.Series,
	maxFamilyLines int,
) ([]Event, error) {
	priceRel := filepath.Join("okx_1h", okxInstrument+".jsonl")
	fundingRel := filepath.Join("okx_funding", okxInstrument+".jsonl")
	if err := verifyCatalogFile(cfg.Catalog, root, priceRel); err != nil {
		return nil, err
	}
	if err := verifyCatalogFile(cfg.Catalog, root, fundingRel); err != nil {
		return nil, err
	}
	prices, err := loadPrices(filepath.Join(root, priceRel), cfg.MaxPriceLines)
	if err != nil {
		return nil, err
	}
	okxFunding, err := loadOKXFunding(filepath.Join(root, fundingRel), cfg.MaxFundingLines)
	if err != nil {
		return nil, err
	}
	series := symbolSeries{prices: prices, okxFunding: okxFunding}
	bybitRel := filepath.Join(familyBybitFunding, BybitFileName(symbol)+".jsonl")
	bybitPath := filepath.Join(root, bybitRel)
	if read, err := verifyOptionalCatalogFile(cfg.Catalog, root, bybitRel); err != nil {
		return nil, err
	} else if read {
		if bybit, err := loadBybitFunding(bybitPath, cfg.MaxFundingLines); err == nil {
			series.bybitFunding = bybit
		} else {
			return nil, fmt.Errorf("g3reverse: read optional %s: %w", bybitRel, err)
		}
	}
	rubikTakerRel := filepath.Join(familyRubikTaker1h, RubikFileName(symbol)+".jsonl")
	rubikTakerPath := filepath.Join(root, rubikTakerRel)
	if read, err := verifyOptionalCatalogFile(cfg.Catalog, root, rubikTakerRel); err != nil {
		return nil, err
	} else if read {
		if rubik, err := loadRubikTaker(rubikTakerPath, maxFamilyLines); err == nil {
			series.rubikTaker = rubik
		} else {
			return nil, fmt.Errorf("g3reverse: read optional %s: %w", rubikTakerRel, err)
		}
	}
	rubikRatioRel := filepath.Join(familyRubikLSRatio1, RubikFileName(symbol)+".jsonl")
	rubikRatioPath := filepath.Join(root, rubikRatioRel)
	if read, err := verifyOptionalCatalogFile(cfg.Catalog, root, rubikRatioRel); err != nil {
		return nil, err
	} else if read {
		if ratio, err := loadRubikLSRatio(rubikRatioPath, maxFamilyLines); err == nil {
			series.rubikRatio = ratio
		} else {
			return nil, fmt.Errorf("g3reverse: read optional %s: %w", rubikRatioRel, err)
		}
	}
	return buildSymbolEvents(symbol, series, btcSeries, cfg.SkyCutpoints, cfg.Start, cfg.End, cfg.OnReject), nil
}

func buildSymbolEvents(
	symbol string,
	data symbolSeries,
	btcSeries sky.Series,
	cutpoints sky.Cutpoints,
	start, end time.Time,
	onReject func(string),
) []Event {
	prices := data.prices
	if len(prices) < 49 {
		return nil
	}
	var events []Event
	for i := 24; i < len(prices); i++ {
		decisionTime := prices[i].OpenTime.Add(time.Hour)
		if (!start.IsZero() && decisionTime.Before(start)) || (!end.IsZero() && !decisionTime.Before(end)) {
			continue
		}
		// Features may use bar i's confirmed close. Execution starts at the
		// next bar's open; a 24h event exits at bar i+25's open.
		if i+25 >= len(prices) ||
			!prices[i+1].OpenTime.Equal(decisionTime) ||
			!prices[i+25].OpenTime.Equal(decisionTime.Add(24*time.Hour)) {
			continue
		}
		featureAsOf := decisionTime
		skyState := sky.Classify(sky.ComputeFeatures(btcSeries, decisionTime), cutpoints)
		if skyState.OOD || skyState.Label == "" {
			if onReject != nil {
				onReject("SKY_OOD")
			}
			continue
		}
		features, missing := computeFeatureCatalog(data, i, decisionTime)
		returns24 := logReturns(prices, i-23, i)
		volBPS := realizedVolatilityBPS(returns24)
		var quoteVolume float64
		for j := i - 23; j <= i; j++ {
			quoteVolume += prices[j].QuoteVolume
		}
		avgQuoteVolume := quoteVolume / 24
		available := map[string]bool{"ohlcv": true}
		if len(data.okxFunding) > 0 {
			available["funding"] = true
			available["okx_funding"] = true
		}
		if len(data.bybitFunding) > 0 {
			available["bybit_funding"] = true
		}
		if len(data.rubikTaker) > 0 {
			available["rubik_taker_1h"] = true
		}
		if len(data.rubikRatio) > 0 {
			available["rubik_lsratio_1h"] = true
		}
		sortResult := sorter.EvaluateLeaf(
			sorter.GlobalSignals{
				DataTruth: sorter.DataTruthValid, Executability: sorter.Executable,
				AvailableData: available,
			},
			sorter.LeafInput{
				LeafID: "g3-reverse-provisional", SorterDomain: "liquid",
				HistoryCoverage:            sorter.GatePass,
				PreDecisionLiquidity:       gateFor(avgQuoteVolume >= protocol.SorterMinQuoteVolume),
				VolatilityCostRatio:        volBPS / protocol.DiscoveryCostBPS,
				VolatilityCostRatioKnown:   true,
				MinVolatilityCostRatio:     protocol.SorterMinVolatilityCost,
				SingleSymbolCapacity:       sorter.GatePass,
				CorrelatedClusterCapacity:  sorter.GatePass,
				MechanismRequiredDataNames: []string{"ohlcv"},
			},
		)
		if !sortResult.Eligible {
			if onReject != nil {
				onReject(string(sortResult.CoarseReason))
			}
			continue
		}
		entryIndex := i + 1
		entry := prices[entryIndex].Open
		liquidityBucket := liquidityBucketOf(avgQuoteVolume)
		cluster := symbol
		for _, horizon := range allHorizons {
			hours := horizon.Hours()
			exitIndex := entryIndex + hours
			if exitIndex >= len(prices) {
				continue
			}
			if !prices[exitIndex].OpenTime.Equal(decisionTime.Add(time.Duration(hours) * time.Hour)) {
				continue
			}
			exit := prices[exitIndex].Open
			rawBPS := (exit/entry - 1) * 10000
			for _, direction := range []mechanism.Direction{mechanism.DirectionLong, mechanism.DirectionShort} {
				signedBPS := rawBPS
				if direction == mechanism.DirectionShort {
					signedBPS = -rawBPS
				}
				net := signedBPS - protocol.DiscoveryCostBPS
				featureCopy := copyFeatures(features)
				events = append(events, Event{
					ID:              eventID(symbol, decisionTime.UnixMilli(), horizon, direction),
					Symbol:          symbol,
					Cluster:         cluster,
					SkyState:        skyState.Label,
					SorterDomain:    sortResult.SorterDomain,
					LiquidityBucket: liquidityBucket,
					Horizon:         horizon,
					DecisionTime:    decisionTime,
					FeatureAsOf:     featureAsOf,
					EntryTime:       prices[entryIndex].OpenTime,
					ExitTime:        prices[exitIndex].OpenTime,
					EntryPrice:      entry,
					ExitPrice:       exit,
					Direction:       direction,
					Features:        featureCopy,
					MissingFeatures: append([]string(nil), missing...),
					NetOutcomeBPS:   net,
					OutcomeClass:    classifyOutcome(net, protocol.NeutralBandBPS),
				})
			}
		}
	}
	return events
}

func computeFeatureCatalog(data symbolSeries, barIndex int, decision time.Time) (map[string]float64, []string) {
	prices := data.prices
	features := map[string]float64{}
	add := func(name string, value float64, ok bool) {
		if ok && isFinite(value) {
			features[name] = value
		}
	}
	if barIndex >= 1 {
		add("symbol_return_1h", (prices[barIndex].Close/prices[barIndex-1].Close-1)*10000, true)
	}
	if barIndex >= 4 {
		add("symbol_return_4h", (prices[barIndex].Close/prices[barIndex-4].Close-1)*10000, prices[barIndex-4].Close > 0)
	}
	if barIndex >= 24 {
		add("symbol_return_24h", (prices[barIndex].Close/prices[barIndex-24].Close-1)*10000, prices[barIndex-24].Close > 0)
	}
	if barIndex >= 1 {
		add("symbol_realized_vol_1h", realizedVolatilityBPS(logReturns(prices, barIndex, barIndex)), true)
	}
	if barIndex >= 4 {
		add("symbol_realized_vol_4h", realizedVolatilityBPS(logReturns(prices, barIndex-3, barIndex)), true)
	}
	if barIndex >= 24 {
		add("symbol_realized_vol_24h", realizedVolatilityBPS(logReturns(prices, barIndex-23, barIndex)), true)
	}
	okxFund, okxOK := latestFundingBefore(data.okxFunding, decision)
	bybitFund, bybitOK := latestFundingBefore(data.bybitFunding, decision)
	if okxOK && bybitOK {
		add("okx_bybit_funding_spread", okxFund.Rate-bybitFund.Rate, true)
		divergence := 0.0
		if (okxFund.Rate >= 0) != (bybitFund.Rate >= 0) && (okxFund.Rate != 0 || bybitFund.Rate != 0) {
			divergence = 1
		}
		add("okx_bybit_funding_sign_divergence", divergence, true)
	}
	if taker, ok := latestRubikTakerBefore(data.rubikTaker, decision); ok {
		sum := taker.Buy + taker.Sell
		if sum > 0 {
			add("rubik_taker_imbalance_1h", (taker.Buy-taker.Sell)/sum, true)
		}
	}
	if ratio, ok := latestRubikRatioBefore(data.rubikRatio, decision); ok {
		add("rubik_lsratio_1h", ratio.Ratio, true)
	}
	var quoteVolume float64
	for j := barIndex - 23; j <= barIndex; j++ {
		if j >= 0 {
			quoteVolume += prices[j].QuoteVolume
		}
	}
	if quoteVolume > 0 {
		features["quote_volume_24h_log_bucket"] = liquidityBucketValue(quoteVolume / 24)
	}
	if vol, ok := features["symbol_realized_vol_24h"]; ok && protocol.DiscoveryCostBPS > 0 {
		features["volatility_cost_ratio"] = vol / protocol.DiscoveryCostBPS
	}
	missing := missingCatalogFeatures(features, protocol.FeatureCatalog)
	return features, missing
}

func logReturns(prices []priceBar, from, to int) []float64 {
	var returns []float64
	for i := from; i <= to; i++ {
		if i <= 0 || prices[i-1].Close <= 0 || prices[i].Close <= 0 {
			continue
		}
		returns = append(returns, (prices[i].Close/prices[i-1].Close-1)*10000)
	}
	return returns
}

func copyFeatures(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func missingCatalogFeatures(present map[string]float64, catalog []string) []string {
	var missing []string
	for _, name := range catalog {
		v, ok := present[name]
		if !ok || !isFinite(v) {
			missing = append(missing, name)
		}
	}
	return missing
}

func realizedVolatilityBPS(returns []float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	var sumSquare float64
	for _, value := range returns {
		sumSquare += value * value
	}
	return math.Sqrt(sumSquare / float64(len(returns)))
}

func gateFor(pass bool) sorter.GateStatus {
	if pass {
		return sorter.GatePass
	}
	return sorter.GateFail
}

func liquidityBucketOf(avgQuoteVolume float64) string {
	return fmt.Sprintf("liq_%d", int(liquidityBucketValue(avgQuoteVolume)))
}

func liquidityBucketValue(avgQuoteVolume float64) float64 {
	if avgQuoteVolume <= 0 {
		return 0
	}
	value := math.Floor(math.Log10(avgQuoteVolume))
	if value < 0 {
		value = 0
	}
	if value > 9 {
		value = 9
	}
	return value
}

func selectedInstruments(catalog historydata.HistoryCatalog, maxSymbols int) []string {
	var instruments []string
	available := map[string]bool{}
	for _, family := range catalog.Families {
		for _, file := range family.Files {
			if !file.Empty {
				available[filepath.Clean(file.Path)] = true
			}
		}
	}
	for _, family := range catalog.Families {
		if family.Name != familyOKXFunding {
			continue
		}
		for _, file := range family.Files {
			if !file.Empty && strings.HasSuffix(file.Path, ".jsonl") {
				instrument := strings.TrimSuffix(filepath.Base(file.Path), ".jsonl")
				pricePath := filepath.Join(familyOKX1h, instrument+".jsonl")
				if available[filepath.Clean(pricePath)] {
					instruments = append(instruments, instrument)
				}
			}
		}
	}
	sort.Strings(instruments)
	if len(instruments) > maxSymbols {
		instruments = instruments[:maxSymbols]
	}
	return instruments
}

func catalogFile(catalog *historydata.HistoryCatalog, relPath string) *historydata.FileStats {
	for familyIndex := range catalog.Families {
		for fileIndex := range catalog.Families[familyIndex].Files {
			file := &catalog.Families[familyIndex].Files[fileIndex]
			if filepath.Clean(file.Path) == filepath.Clean(relPath) {
				return file
			}
		}
	}
	return nil
}

// verifyOptionalCatalogFile returns whether a verified optional file should be
// parsed. A catalog Empty entry is a valid missing feature: if its physical
// file exists it is still size/hash verified, but it is never parsed.
func verifyOptionalCatalogFile(catalog *historydata.HistoryCatalog, root, relPath string) (bool, error) {
	expected := catalogFile(catalog, relPath)
	path := filepath.Join(root, relPath)
	_, statErr := os.Stat(path)
	if expected == nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		if statErr != nil {
			return false, fmt.Errorf("g3reverse: stat optional %s: %w", relPath, statErr)
		}
		return false, fmt.Errorf("g3reverse: optional %s exists without catalog record", relPath)
	}
	if os.IsNotExist(statErr) {
		if expected.Empty {
			return false, nil
		}
		return false, fmt.Errorf("g3reverse: optional %s declared non-empty but file is missing: %w", relPath, os.ErrNotExist)
	}
	if statErr != nil {
		return false, fmt.Errorf("g3reverse: stat optional %s: %w", relPath, statErr)
	}
	if err := verifyFileStats(root, relPath, expected); err != nil {
		return false, err
	}
	return !expected.Empty, nil
}

func verifyCatalogFile(catalog *historydata.HistoryCatalog, root, relPath string) error {
	expected := catalogFile(catalog, relPath)
	if expected == nil || expected.Empty {
		return fmt.Errorf("g3reverse: required %s missing or catalog-empty: %w", relPath, os.ErrNotExist)
	}
	return verifyFileStats(root, relPath, expected)
}

func verifyFileStats(root, relPath string, expected *historydata.FileStats) error {
	path := filepath.Join(root, relPath)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("g3reverse: stat %s: %w", relPath, err)
	}
	if info.Size() != expected.Size {
		return fmt.Errorf("g3reverse: catalog-bound input %s size changed", relPath)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("g3reverse: open %s: %w", relPath, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, file, make([]byte, 4*1024*1024)); err != nil {
		return fmt.Errorf("g3reverse: hash %s: %w", relPath, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.FileHash {
		return fmt.Errorf("g3reverse: catalog-bound input %s hash changed", relPath)
	}
	return nil
}

// VerifySelectedFiles re-reads and verifies every file selected by generation.
// It is used before accepting checkpointed partition outputs.
func VerifySelectedFiles(catalog *historydata.HistoryCatalog, root string, maxSymbols int) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, rel := range []string{
		filepath.Join(familyOKX1h, "BTC-USDT-SWAP.jsonl"),
		filepath.Join(familyOKXFunding, "BTC-USDT-SWAP.jsonl"),
	} {
		if err := verifyCatalogFile(catalog, absoluteRoot, rel); err != nil {
			return err
		}
	}
	for _, instrument := range selectedInstruments(*catalog, maxSymbols) {
		symbol := CanonicalSymbol(instrument)
		required := []string{
			filepath.Join(familyOKXFunding, instrument+".jsonl"),
			filepath.Join(familyOKX1h, instrument+".jsonl"),
		}
		for _, rel := range required {
			if err := verifyCatalogFile(catalog, absoluteRoot, rel); err != nil {
				return err
			}
		}
		optional := []string{
			filepath.Join(familyBybitFunding, BybitFileName(symbol)+".jsonl"),
			filepath.Join(familyRubikTaker1h, RubikFileName(symbol)+".jsonl"),
			filepath.Join(familyRubikLSRatio1, RubikFileName(symbol)+".jsonl"),
		}
		for _, rel := range optional {
			if _, err := verifyOptionalCatalogFile(catalog, absoluteRoot, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// HashEvents produces a stable commitment over an event pool.
func HashEvents(events []Event) (string, error) {
	sorted := append([]Event(nil), events...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	hash, _, err := datatruth.HashJSON(sorted)
	return hash, err
}

// HashBoundaries commits the frozen partition windows.
func HashBoundaries(b Boundaries) (string, error) {
	hash, _, err := datatruth.HashJSON(b)
	return hash, err
}

// BuildWinnerIndex extracts induction winners for hypothesis generation only.
func BuildWinnerIndex(events []Event, generation, protocolHash string) (WinnerIndex, error) {
	var entries []WinnerIndexEntry
	for _, event := range events {
		if event.OutcomeClass != OutcomeWinner {
			continue
		}
		entries = append(entries, WinnerIndexEntry{
			EventID: event.ID, Symbol: event.Symbol, SkyState: event.SkyState,
			SorterDomain: event.SorterDomain, Horizon: event.Horizon,
			Direction: string(event.Direction), DecisionTime: event.DecisionTime,
			NetOutcomeBPS: event.NetOutcomeBPS, OutcomeClass: event.OutcomeClass,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].DecisionTime.Equal(entries[j].DecisionTime) {
			return entries[i].EventID < entries[j].EventID
		}
		return entries[i].DecisionTime.Before(entries[j].DecisionTime)
	})
	indexHash, _, err := datatruth.HashJSON(entries)
	if err != nil {
		return WinnerIndex{}, err
	}
	return WinnerIndex{
		Generation: generation, ProtocolHash: protocolHash,
		Entries: entries, IndexHash: indexHash,
	}, nil
}

// BuildGenerationStats aggregates partition-level counters.
func BuildGenerationStats(
	induction, recheck, forward, wf []Event,
	rejections map[string]map[string]int,
) GenerationStats {
	stats := GenerationStats{
		ExamActions:               protocol.ExamActions,
		EventsByPartition:         map[string]int{},
		WinnersByPartition:        map[string]int{},
		RejectionsByPartition:     rejections,
		EligibleByPartitionAndSky: map[string]map[string]int{},
		OutcomeByPartition:        map[string]map[string]int{},
	}
	for name, events := range map[string][]Event{
		string(PartitionInduction): induction,
		string(PartitionRecheck):   recheck,
		string(PartitionForward):   forward,
		string(PartitionWalkFwd):   wf,
	} {
		stats.EventsByPartition[name] = len(events)
		stats.EligibleByPartitionAndSky[name] = map[string]int{}
		stats.OutcomeByPartition[name] = map[string]int{}
		for _, event := range events {
			stats.EligibleByPartitionAndSky[name][event.SkyState]++
			stats.OutcomeByPartition[name][string(event.OutcomeClass)]++
			if event.OutcomeClass == OutcomeWinner {
				stats.WinnersByPartition[name]++
			}
		}
	}
	return stats
}

// AnchorFundingSpan loads BTC okx_funding to anchor partition boundaries.
func AnchorFundingSpan(root string, maxLines int) ([]fundingPoint, Boundaries, error) {
	points, err := loadFamilyFundingSpan(filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl"), maxLines)
	if err != nil {
		return nil, Boundaries{}, err
	}
	b, err := ComputeBoundaries(points)
	if err != nil {
		return nil, Boundaries{}, err
	}
	return points, b, nil
}
