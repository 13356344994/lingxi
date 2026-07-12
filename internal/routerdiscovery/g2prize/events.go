package g2prize

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/protocol"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg2/sorter"
)

// Config bounds one deterministic event-regeneration run against a small,
// research-only history root. Every knob is mandatory and positive: there
// is no "unbounded" path through BuildEvents.
type Config struct {
	HistoryRoot     string
	MaxSymbols      int
	MaxFundingLines int
	MaxPriceLines   int
	ProgressEvery   int
	OnProgress      func(Progress)
	OnReject        func(reason string)
	Catalog         *historydata.HistoryCatalog
	SkyCutpoints    sky.Cutpoints
	Start           time.Time
	End             time.Time
}

type Progress struct {
	SymbolsDone  int    `json:"symbols_done"`
	SymbolsTotal int    `json:"symbols_total"`
	Events       int    `json:"events"`
	Symbol       string `json:"symbol"`
}

type Boundaries struct {
	DiscoveryEnd time.Time `json:"discovery_end"`
	ForwardStart time.Time `json:"forward_start"`
	WFStart      time.Time `json:"wf_start"`
	End          time.Time `json:"end"`
}

func FitSkyCutpoints(ctx context.Context, cfg Config) (sky.Cutpoints, Boundaries, error) {
	if cfg.Catalog == nil || cfg.HistoryRoot == "" {
		return sky.Cutpoints{}, Boundaries{}, errors.New("g2prize: catalog/history root required for sky fit")
	}
	absoluteRoot, err := filepath.Abs(cfg.HistoryRoot)
	if err != nil {
		return sky.Cutpoints{}, Boundaries{}, err
	}
	for _, rel := range []string{"okx_1h/BTC-USDT-SWAP.jsonl", "okx_funding/BTC-USDT-SWAP.jsonl"} {
		if err := verifyCatalogFile(cfg.Catalog, absoluteRoot, rel); err != nil {
			return sky.Cutpoints{}, Boundaries{}, err
		}
	}
	prices, err := loadPrices(filepath.Join(absoluteRoot, "okx_1h", "BTC-USDT-SWAP.jsonl"), cfg.MaxPriceLines)
	if err != nil {
		return sky.Cutpoints{}, Boundaries{}, err
	}
	funding, err := loadFunding(filepath.Join(absoluteRoot, "okx_funding", "BTC-USDT-SWAP.jsonl"), cfg.MaxFundingLines)
	if err != nil {
		return sky.Cutpoints{}, Boundaries{}, err
	}
	if len(funding) < 3 {
		return sky.Cutpoints{}, Boundaries{}, errors.New("g2prize: insufficient BTC funding history for partitions")
	}
	start := funding[0].Time
	end := funding[len(funding)-1].Time.Add(time.Millisecond)
	span := end.Sub(start)
	discoveryEnd := start.Add(span * time.Duration(protocol.DiscoveryPermille) / 1000)
	wfStart := start.Add(span * time.Duration(protocol.DiscoveryPermille+protocol.ForwardPermille) / 1000)
	forwardStart := discoveryEnd.Add(time.Duration(protocol.PartitionEmbargoHours) * time.Hour)
	if !forwardStart.Before(wfStart) {
		return sky.Cutpoints{}, Boundaries{}, errors.New("g2prize: history span cannot satisfy frozen discovery embargo")
	}
	series := sky.Series{}
	for _, bar := range prices {
		series.Bars = append(series.Bars, sky.Bar{OpenTime: bar.Time, Close: bar.Close})
	}
	for _, point := range funding {
		series.Funding = append(series.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	var samples []sky.FeatureSet
	for _, point := range funding {
		select {
		case <-ctx.Done():
			return sky.Cutpoints{}, Boundaries{}, ctx.Err()
		default:
		}
		decision := point.Time.Add(time.Millisecond)
		if decision.After(discoveryEnd) {
			continue
		}
		features := sky.ComputeFeatures(series, decision)
		if !features.OOD {
			samples = append(samples, features)
		}
	}
	cutpoints, err := sky.Fit(sky.FitPartition, samples, protocol.SkyQuantiles)
	if err != nil {
		return sky.Cutpoints{}, Boundaries{}, err
	}
	return cutpoints.Freeze(), Boundaries{
		DiscoveryEnd: discoveryEnd, ForwardStart: forwardStart, WFStart: wfStart, End: end,
	}, nil
}

// BuildEvents deterministically regenerates the g2 prize-map event pool
// from HistoryRoot, verifying every selected input file against cfg.Catalog
// by exact size and content hash before reading it. Given the same catalog
// and root, it always produces byte-identical events: this is the
// "canonical catalog + selected file hash + deterministic regeneration"
// contract the CLI relies on to detect drift.
//
// For every funding-settlement decision point it emits candidate events for
// both hypothetical directions (long and short) and for all three frozen
// horizons (1h/4h/24h), so long/short and horizon symmetry is structural at
// the data layer, not something the search space has to compensate for.
func BuildEvents(ctx context.Context, cfg Config) ([]Event, error) {
	if cfg.HistoryRoot == "" || cfg.MaxSymbols <= 0 || cfg.MaxFundingLines <= 0 || cfg.MaxPriceLines <= 0 {
		return nil, errors.New("g2prize: bounded root/max-symbols/max-lines are required")
	}
	if cfg.Catalog == nil || cfg.Catalog.DataHash == "" {
		return nil, errors.New("g2prize: verified HistoryCatalog is required")
	}
	if !cfg.SkyCutpoints.Frozen {
		return nil, errors.New("g2prize: frozen Sky cutpoints are required")
	}
	absoluteRoot, err := filepath.Abs(cfg.HistoryRoot)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absoluteRoot) != filepath.Clean(cfg.Catalog.RootDir) {
		return nil, errors.New("g2prize: history root does not match catalog root")
	}
	if cfg.ProgressEvery <= 0 {
		cfg.ProgressEvery = 1
	}
	fundingDir := filepath.Join(cfg.HistoryRoot, "okx_funding")
	priceDir := filepath.Join(cfg.HistoryRoot, "okx_1h")
	btcPrices, err := loadPrices(filepath.Join(priceDir, "BTC-USDT-SWAP.jsonl"), cfg.MaxPriceLines)
	if err != nil {
		return nil, err
	}
	btcFunding, err := loadFunding(filepath.Join(fundingDir, "BTC-USDT-SWAP.jsonl"), cfg.MaxFundingLines)
	if err != nil {
		return nil, err
	}
	btcSeries := sky.Series{}
	for _, bar := range btcPrices {
		btcSeries.Bars = append(btcSeries.Bars, sky.Bar{OpenTime: bar.Time, Close: bar.Close})
	}
	for _, point := range btcFunding {
		btcSeries.Funding = append(btcSeries.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	entries, err := os.ReadDir(fundingDir)
	if err != nil {
		return nil, err
	}
	var symbols []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		symbols = append(symbols, strings.TrimSuffix(entry.Name(), ".jsonl"))
	}
	sort.Strings(symbols)
	if len(symbols) > cfg.MaxSymbols {
		symbols = symbols[:cfg.MaxSymbols]
	}
	var events []Event
	for index, instrument := range symbols {
		select {
		case <-ctx.Done():
			return events, ctx.Err()
		default:
		}
		pricePath := filepath.Join(priceDir, instrument+".jsonl")
		fundingPath := filepath.Join(fundingDir, instrument+".jsonl")
		if err := verifyCatalogFile(cfg.Catalog, absoluteRoot, filepath.Join("okx_1h", instrument+".jsonl")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := verifyCatalogFile(cfg.Catalog, absoluteRoot, filepath.Join("okx_funding", instrument+".jsonl")); err != nil {
			return nil, err
		}
		prices, err := loadPrices(pricePath, cfg.MaxPriceLines)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		funding, err := loadFunding(fundingPath, cfg.MaxFundingLines)
		if err != nil {
			return nil, err
		}
		symbol := strings.TrimSuffix(instrument, "-SWAP")
		events = append(events, buildSymbolEvents(
			symbol, prices, funding, btcSeries, cfg.SkyCutpoints, cfg.Start, cfg.End, cfg.OnReject,
		)...)
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

func verifyCatalogFile(catalog *historydata.HistoryCatalog, root, relPath string) error {
	var expected *historydata.FileStats
	for familyIndex := range catalog.Families {
		for fileIndex := range catalog.Families[familyIndex].Files {
			file := &catalog.Families[familyIndex].Files[fileIndex]
			if filepath.Clean(file.Path) == filepath.Clean(relPath) {
				expected = file
				break
			}
		}
	}
	if expected == nil || expected.Empty {
		return os.ErrNotExist
	}
	path := filepath.Join(root, relPath)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != expected.Size {
		return errors.New("g2prize: catalog-bound input size changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, file, make([]byte, 4*1024*1024)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.FileHash {
		return errors.New("g2prize: catalog-bound input hash changed")
	}
	return nil
}

type priceBar struct {
	Time        time.Time
	Open        float64
	Close       float64
	QuoteVolume float64
}

type fundingPoint struct {
	Time time.Time
	Rate float64
}

func loadPrices(path string, maxLines int) ([]priceBar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var bars []priceBar
	for scanner.Scan() && len(bars) < maxLines {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if len(row.Data) < 8 {
			continue
		}
		ms, _ := strconv.ParseInt(row.Data[0], 10, 64)
		openPrice, _ := strconv.ParseFloat(row.Data[1], 64)
		closePrice, _ := strconv.ParseFloat(row.Data[4], 64)
		quoteVolume, _ := strconv.ParseFloat(row.Data[7], 64)
		if ms > 0 && openPrice > 0 && closePrice > 0 {
			bars = append(bars, priceBar{Time: time.UnixMilli(ms).UTC(), Open: openPrice, Close: closePrice, QuoteVolume: quoteVolume})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })
	return bars, nil
}

func loadFunding(path string, maxLines int) ([]fundingPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []fundingPoint
	for scanner.Scan() && len(points) < maxLines {
		var row struct {
			Data struct {
				FundingRate string `json:"fundingRate"`
				FundingTime string `json:"fundingTime"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		ms, _ := strconv.ParseInt(row.Data.FundingTime, 10, 64)
		rate, _ := strconv.ParseFloat(row.Data.FundingRate, 64)
		if ms > 0 && rate != 0 {
			points = append(points, fundingPoint{Time: time.UnixMilli(ms).UTC(), Rate: rate})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

var allHorizons = []Horizon{Horizon1h, Horizon4h, Horizon24h}

// buildSymbolEvents turns one symbol's price/funding series into the g2
// prize-map event pool: for every funding-settlement point with enough
// trailing/leading bars, it computes the SkyState/SorterDomain/liquidity-
// bucket stratification labels once (from pre-decision data only) and then
// emits one event per (horizon, hypothetical direction) pair sharing that
// same stratification and feature snapshot.
func buildSymbolEvents(
	symbol string,
	prices []priceBar,
	funding []fundingPoint,
	btcSeries sky.Series,
	cutpoints sky.Cutpoints,
	start, end time.Time,
	onReject func(string),
) []Event {
	if len(prices) < 49 {
		return nil
	}
	indexByTime := map[int64]int{}
	for i, bar := range prices {
		indexByTime[bar.Time.UnixMilli()] = i
	}
	var events []Event
	for fundingIndex, point := range funding {
		decisionTime := point.Time.Add(time.Millisecond)
		if (!start.IsZero() && decisionTime.Before(start)) ||
			(!end.IsZero() && !decisionTime.Before(end)) {
			continue
		}
		index, ok := indexByTime[point.Time.UnixMilli()]
		if !ok || index < 25 || index+24 >= len(prices) {
			continue
		}
		// OKX candle ts is bar open time. At funding_time the current bar
		// has only just opened, so its open is observable but its
		// close/volume are future.
		entry := prices[index].Open
		returns := make([]float64, 0, 24)
		var quoteVolume float64
		for i := index - 24; i <= index-1; i++ {
			if i > 0 {
				returns = append(returns, (prices[i].Close/prices[i-1].Close-1)*10000)
			}
			quoteVolume += prices[i].QuoteVolume
		}
		avgQuoteVolume := quoteVolume / 24
		skyState := sky.Classify(sky.ComputeFeatures(btcSeries, decisionTime), cutpoints)
		if skyState.OOD || skyState.Label == "" {
			if onReject != nil {
				onReject("SKY_OOD")
			}
			continue
		}
		volatilityBPS := realizedVolatilityBPS(returns)
		sortResult := sorter.EvaluateLeaf(
			sorter.GlobalSignals{
				DataTruth: sorter.DataTruthValid, Executability: sorter.Executable,
				AvailableData: map[string]bool{"ohlcv": true, "funding": true},
			},
			sorter.LeafInput{
				LeafID: "g2-prize-provisional", SorterDomain: "liquid",
				HistoryCoverage:          sorter.GatePass,
				PreDecisionLiquidity:     gateFor(avgQuoteVolume >= protocol.SorterMinQuoteVolume),
				VolatilityCostRatio:      volatilityBPS / protocol.DiscoveryCostBPS,
				VolatilityCostRatioKnown: true,
				MinVolatilityCostRatio:   protocol.SorterMinVolatilityCostRatio,
				SingleSymbolCapacity:     sorter.GatePass, CorrelatedClusterCapacity: sorter.GatePass,
				MechanismRequiredDataNames: []string{"ohlcv", "funding"},
			},
		)
		if !sortResult.Eligible {
			if onReject != nil {
				onReject(string(sortResult.CoarseReason))
			}
			continue
		}
		sorterDomain := sortResult.SorterDomain
		liquidityBucket := liquidityBucketOf(avgQuoteVolume)
		priorReturn := (entry/prices[index-24].Open - 1) * 10000
		cluster := symbol
		features := map[string]float64{
			"funding_rate":         point.Rate,
			"funding_abs":          math.Abs(point.Rate),
			"prior_return_24h_bps": priorReturn,
			"funding_sign_flip":    0,
		}
		if fundingIndex > 0 && funding[fundingIndex-1].Rate*point.Rate < 0 {
			features["funding_sign_flip"] = 1
		}
		for _, horizon := range allHorizons {
			hours := horizon.Hours()
			if index+hours >= len(prices) {
				continue
			}
			exit := prices[index+hours].Open
			rawBPS := (exit/entry - 1) * 10000
			for _, direction := range []mechanism.Direction{mechanism.DirectionLong, mechanism.DirectionShort} {
				signedBPS := rawBPS
				if direction == mechanism.DirectionShort {
					signedBPS = -rawBPS
				}
				events = append(events, Event{
					ID:              fmt.Sprintf("%s-%d-%s-%s", symbol, point.Time.UnixMilli(), horizon, direction),
					Symbol:          symbol,
					Cluster:         cluster,
					SkyState:        skyState.Label,
					SorterDomain:    sorterDomain,
					LiquidityBucket: liquidityBucket,
					Horizon:         horizon,
					DecisionTime:    decisionTime,
					FeatureAsOf:     point.Time,
					Payer:           mechanism.PayerFundingReceivers,
					Direction:       direction,
					Features:        features,
					NetOutcomeBPS:   signedBPS - protocol.DiscoveryCostBPS,
				})
			}
		}
	}
	return events
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
	if avgQuoteVolume <= 0 {
		return "liq_0"
	}
	value := math.Floor(math.Log10(avgQuoteVolume))
	if value < 0 {
		value = 0
	}
	if value > 9 {
		value = 9
	}
	return fmt.Sprintf("liq_%d", int(value))
}
