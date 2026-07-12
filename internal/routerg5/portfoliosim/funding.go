package portfoliosim

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
)

// FundingBundle holds optional per-symbol funding points.
type FundingBundle struct {
	BySymbol map[string][]profitmap.FundingPoint
	Missing  map[string]bool
}

// LoadFundingBundle loads optional okx_funding points for referenced symbols.
func LoadFundingBundle(catalog historydata.HistoryCatalog, historyRoot string, symbols map[string]bool, maxLines int) (FundingBundle, error) {
	bySymbol := map[string][]profitmap.FundingPoint{}
	missing := map[string]bool{}
	for symbol := range symbols {
		rel := "okx_funding/" + symbol + ".jsonl"
		declared := false
		for _, fam := range catalog.Families {
			if fam.Name != "okx_funding" {
				continue
			}
			for _, file := range fam.Files {
				if file.Path == rel {
					declared = true
					break
				}
			}
		}
		points, err := profitmap.LoadSymbolFunding(historyRoot, symbol, maxLines)
		if err != nil {
			if strings.Contains(err.Error(), "no such file") {
				missing[symbol] = true
				continue
			}
			return FundingBundle{}, fmt.Errorf("portfoliosim: funding %s: %w", symbol, err)
		}
		if !declared && len(points) == 0 {
			missing[symbol] = true
			continue
		}
		if len(points) == 0 {
			missing[symbol] = true
			continue
		}
		bySymbol[symbol] = points
	}
	return FundingBundle{BySymbol: bySymbol, Missing: missing}, nil
}

// FundingUSDTBetween deducts abs settled funding over [start, end) for one position.
func FundingUSDTBetween(bundle FundingBundle, symbol string, state actionspace.PositionState, start, end time.Time, stress float64) float64 {
	if state.Side == actionspace.SideFlat || state.SizeFraction <= 0 {
		return 0
	}
	points, ok := bundle.BySymbol[symbol]
	if !ok || len(points) == 0 {
		return 0
	}
	notional := actionspace.NotionalUSDT(state)
	bps := profitmap.FundingBPSOverHold(points, start, end)
	if bps <= 0 {
		return 0
	}
	cost := notional * bps / 10000
	if stress > 1.0+1e-9 {
		cost *= stress
	}
	return cost
}

// ApplyHoldFunding deducts funding paid while positions are open between two decision times.
func (a *Account) ApplyHoldFunding(bundle FundingBundle, symbols []string, start, end time.Time, stress float64) {
	if !end.After(start) {
		return
	}
	for _, symbol := range symbols {
		pos, ok := a.Positions[symbol]
		if !ok || pos.Side == actionspace.SideFlat {
			continue
		}
		if bundle.Missing[symbol] {
			a.FundingMissing = true
			continue
		}
		funding := FundingUSDTBetween(bundle, symbol, pos, start, end, stress)
		if funding <= 0 {
			continue
		}
		a.CashUSDT -= funding
		a.FundingPaidUSDT += funding
		a.SymbolPnL[symbol] -= funding
	}
	if a.CashUSDT <= 0 && len(a.Positions) == 0 {
		a.SolvencyBreach = true
	}
}

// FundingCatalogPath returns the canonical funding path for one symbol.
func FundingCatalogPath(symbol string) string {
	return filepath.Join("okx_funding", symbol+".jsonl")
}

// HasFundingData reports whether bundle contains funding points for a symbol.
func HasFundingData(bundle FundingBundle, symbol string) bool {
	points, ok := bundle.BySymbol[symbol]
	return ok && len(points) > 0
}

// MaxFundingStress returns the stress multiplier applied to funding under cost stress.
func MaxFundingStress(stress float64) float64 {
	return math.Max(1.0, stress)
}
