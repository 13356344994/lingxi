package historyevents

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
	"lingxi/internal/hierarchicalrouter"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/backward"
)

type Config struct {
	HistoryRoot     string
	MaxSymbols      int
	MaxFundingLines int
	MaxPriceLines   int
	ProgressEvery   int
	OnProgress      func(Progress)
	Catalog         *historydata.HistoryCatalog
}

type Progress struct {
	SymbolsDone  int    `json:"symbols_done"`
	SymbolsTotal int    `json:"symbols_total"`
	Events       int    `json:"events"`
	Symbol       string `json:"symbol"`
}

func BuildFundingPostSettlement(ctx context.Context, cfg Config) ([]backward.Event, error) {
	if cfg.HistoryRoot == "" || cfg.MaxSymbols <= 0 || cfg.MaxFundingLines <= 0 || cfg.MaxPriceLines <= 0 {
		return nil, errors.New("historyevents: bounded root/max-symbols/max-lines are required")
	}
	if cfg.Catalog == nil || cfg.Catalog.DataHash != hierarchicalrouter.HistoryDataHash {
		return nil, errors.New("historyevents: verified HistoryCatalog is required")
	}
	absoluteRoot, err := filepath.Abs(cfg.HistoryRoot)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absoluteRoot) != filepath.Clean(cfg.Catalog.RootDir) {
		return nil, errors.New("historyevents: history root does not match catalog root")
	}
	if cfg.ProgressEvery <= 0 {
		cfg.ProgressEvery = 1
	}
	fundingDir := filepath.Join(cfg.HistoryRoot, "okx_funding")
	priceDir := filepath.Join(cfg.HistoryRoot, "okx_1h")
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
	var events []backward.Event
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
		events = append(events, buildEvents(symbol, prices, funding)...)
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
		return errors.New("historyevents: catalog-bound input size changed")
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
		return errors.New("historyevents: catalog-bound input hash changed")
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

func buildEvents(symbol string, prices []priceBar, funding []fundingPoint) []backward.Event {
	if len(prices) < 29 {
		return nil
	}
	indexByTime := map[int64]int{}
	for i, bar := range prices {
		indexByTime[bar.Time.UnixMilli()] = i
	}
	var events []backward.Event
	for _, point := range funding {
		index, ok := indexByTime[point.Time.UnixMilli()]
		if !ok || index < 25 || index+hierarchicalrouter.BackwardOutcomeHorizonHours >= len(prices) {
			continue
		}
		direction := mechanism.DirectionLong
		if point.Rate < 0 {
			direction = mechanism.DirectionShort
		}
		// OKX candle ts is bar open time. At funding_time the current bar has only
		// just opened, so its open is observable but its close/volume are future.
		entry := prices[index].Open
		exit := prices[index+hierarchicalrouter.BackwardOutcomeHorizonHours].Open
		rawBPS := (exit/entry - 1) * 10000
		if direction == mechanism.DirectionShort {
			rawBPS = -rawBPS
		}
		returns := make([]float64, 0, 24)
		var quoteVolume float64
		for i := index - 24; i <= index-1; i++ {
			if i > 0 {
				returns = append(returns, (prices[i].Close/prices[i-1].Close-1)*10000)
			}
			quoteVolume += prices[i].QuoteVolume
		}
		volatility := volatilityBucket(returns)
		liquidity := liquidityBucket(quoteVolume / 24)
		priorReturn := (entry/prices[index-24].Open - 1) * 10000
		events = append(events, backward.Event{
			ID:     fmt.Sprintf("%s-%d-%s", symbol, point.Time.UnixMilli(), direction),
			Symbol: symbol, Cluster: symbol,
			Stratum:      fmt.Sprintf("vol=%.0f|liq=%.0f", volatility, liquidity),
			DecisionTime: point.Time.Add(time.Millisecond), FeatureAsOf: point.Time,
			Payer: mechanism.PayerFundingReceivers, Direction: direction,
			Volatility: volatility, Liquidity: liquidity,
			Features: map[string]float64{
				"funding_rate":         point.Rate,
				"funding_abs":          math.Abs(point.Rate),
				"prior_return_24h_bps": priorReturn,
			},
			NetOutcomeBPS: rawBPS - hierarchicalrouter.BackwardDiscoveryCostBPS,
		})
	}
	return events
}

func volatilityBucket(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum, sumSquare float64
	for _, value := range values {
		sum += value
		sumSquare += value * value
	}
	mean := sum / float64(len(values))
	std := math.Sqrt(math.Max(0, sumSquare/float64(len(values))-mean*mean))
	switch {
	case std < hierarchicalrouter.BackwardVolatilityLowBPS:
		return 0
	case std < hierarchicalrouter.BackwardVolatilityHighBPS:
		return 1
	default:
		return 2
	}
}

func liquidityBucket(averageQuoteVolume float64) float64 {
	if averageQuoteVolume <= 0 {
		return 0
	}
	value := math.Floor(math.Log10(averageQuoteVolume))
	if value < 0 {
		return 0
	}
	if value > 9 {
		return 9
	}
	return value
}
