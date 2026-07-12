package portfoliosim

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/features"
)

// BarsBundle holds canonical OHLCV bars keyed by symbol.
type BarsBundle struct {
	BySymbol map[string][]profitmap.Bar
	Catalog  historydata.HistoryCatalog
	Funding  FundingBundle
}

// LoadCanonicalBars loads confirmed bars for symbols referenced in validation features.
func LoadCanonicalBars(catalog historydata.HistoryCatalog, historyRoot, profileID string, rows map[string][]features.DecisionContext, maxLines int) (BarsBundle, error) {
	if catalog.DataHash == "" {
		return BarsBundle{}, errors.New("portfoliosim: catalog hash missing")
	}
	symbols := map[string]bool{}
	for _, shardRows := range rows {
		for _, row := range shardRows {
			symbols[row.Symbol] = true
		}
	}
	bySymbol := map[string][]profitmap.Bar{}
	for symbol := range symbols {
		rel, err := catalogRelPath(catalog, profileID, symbol)
		if err != nil {
			return BarsBundle{}, fmt.Errorf("portfoliosim: catalog lookup %s: %w", symbol, err)
		}
		bars, err := profitmap.LoadBars(filepath.Join(historyRoot, rel), maxLines)
		if err != nil {
			return BarsBundle{}, fmt.Errorf("portfoliosim: load bars %s: %w", symbol, err)
		}
		if len(bars) == 0 {
			return BarsBundle{}, fmt.Errorf("portfoliosim: no confirmed bars for %s", symbol)
		}
		bySymbol[symbol] = bars
	}
	funding, err := LoadFundingBundle(catalog, historyRoot, symbols, maxLines)
	if err != nil {
		return BarsBundle{}, err
	}
	return BarsBundle{BySymbol: bySymbol, Catalog: catalog, Funding: funding}, nil
}

func catalogRelPath(catalog historydata.HistoryCatalog, profileID, symbol string) (string, error) {
	family := "okx_1h"
	if profileID == "okx_5m" {
		family = "okx_5m"
	}
	suffix := symbol + ".jsonl"
	for _, fam := range catalog.Families {
		if fam.Name != family {
			continue
		}
		for _, file := range fam.Files {
			if strings.HasSuffix(file.Path, suffix) {
				return file.Path, nil
			}
		}
	}
	return "", fmt.Errorf("symbol %s not in catalog", symbol)
}

// DecisionBarIndex returns the decision bar index for one symbol/time.
func DecisionBarIndex(bundle BarsBundle, profile features.Profile, symbol string, decisionTime time.Time) int {
	bars, ok := bundle.BySymbol[symbol]
	if !ok {
		return -1
	}
	idx := profitmap.BarIndexAt(bars, decisionTime, profile.BarDuration)
	if idx < 0 || profitmap.RejectCurrentBarOpen(bars, idx, profile.BarDuration) {
		return -1
	}
	return idx
}

// EntryOpenPrice returns the open price at executeIndex for one symbol.
func EntryOpenPrice(bundle BarsBundle, symbol string, executeIndex int) (float64, bool) {
	bars, ok := bundle.BySymbol[symbol]
	if !ok || executeIndex < 0 || executeIndex >= len(bars) {
		return 0, false
	}
	price := bars[executeIndex].Open
	return price, price > 0
}

// ClosePricesAtDecision builds decision-time close prices from canonical bars.
func ClosePricesAtDecision(bundle BarsBundle, profile features.Profile, symbols []string, decisionTime time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, symbol := range symbols {
		bars, ok := bundle.BySymbol[symbol]
		if !ok {
			continue
		}
		idx := profitmap.BarIndexAt(bars, decisionTime, profile.BarDuration)
		if idx < 0 || profitmap.RejectCurrentBarOpen(bars, idx, profile.BarDuration) {
			continue
		}
		if bars[idx].Close > 0 {
			out[symbol] = bars[idx].Close
		}
	}
	return out
}

// PricesFromBars builds decision-time close prices from canonical bars.
func PricesFromBars(bundle BarsBundle, profile features.Profile, rows []features.DecisionContext) map[string]float64 {
	out := map[string]float64{}
	for _, row := range rows {
		bars, ok := bundle.BySymbol[row.Symbol]
		if !ok {
			continue
		}
		idx := profitmap.BarIndexAt(bars, row.DecisionTime, profile.BarDuration)
		if idx < 0 || profitmap.RejectCurrentBarOpen(bars, idx, profile.BarDuration) {
			continue
		}
		if bars[idx].Close > 0 {
			out[row.Symbol] = bars[idx].Close
		}
	}
	return out
}

// MergePrices prefers canonical bar prices, falling back to feature-derived proxies.
func MergePrices(canonical, fallback map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range fallback {
		out[k] = v
	}
	for k, v := range canonical {
		if v > 0 {
			out[k] = v
		}
	}
	return out
}

// ExecuteIndexForDelay returns decisionIndex+1+delay per frozen next-open rule.
func ExecuteIndexForDelay(decisionIndex, delayBars int) int {
	return decisionIndex + 1 + delayBars
}
