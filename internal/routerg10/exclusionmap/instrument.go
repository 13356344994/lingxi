package exclusionmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	contextdata "lingxi/internal/datatruth/context"
	"lingxi/internal/datatruth"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	"lingxi/internal/mechanismpanel"
)

// pitDynamicMinNotionalUSDT resolves PIT instrument min notional at decision time.
// Uses min_sz * ct_val * price for linear contracts; inverse/unknown fail closed.
func pitDynamicMinNotionalUSDT(
	cat datatruth.InstrumentCatalog,
	symbol string,
	decisionTime time.Time,
	price float64,
) (float64, error) {
	if price <= 0 {
		return 0, errMinNotionalUnavailable
	}
	ids := instrumentLookupIDs(symbol)
	at := decisionTime.UTC()
	for _, id := range ids {
		v, ok := cat.InstrumentAt(id, at, at)
		if !ok {
			continue
		}
		minSz := strings.TrimSpace(v.LotSize)
		ctVal := strings.TrimSpace(v.ContractValue)
		if minSz == "" || ctVal == "" {
			continue
		}
		ctType := inferContractType(v)
		got, _, err := g10entryproof.DynamicMinNotionalUSDT(minSz, ctVal, ctType, price)
		if err != nil {
			return 0, err
		}
		if got > 0 {
			return got, nil
		}
	}
	return 0, errMinNotionalUnavailable
}

func instrumentLookupIDs(symbol string) []string {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil
	}
	if strings.HasSuffix(symbol, "-SWAP") {
		base := strings.TrimSuffix(symbol, "-SWAP")
		return []string{symbol, base}
	}
	return []string{symbol, symbol + "-SWAP"}
}

func inferContractType(v datatruth.InstrumentVersion) string {
	symbol := strings.ToUpper(strings.TrimSpace(v.Symbol))
	instrumentID := strings.ToUpper(strings.TrimSpace(v.InstrumentID))
	for _, id := range []string{symbol, instrumentID} {
		if id == "" {
			continue
		}
		if strings.Contains(id, "USD-SWAP") && !strings.Contains(id, "USDT") {
			return g10entryproof.CtTypeInverse
		}
	}
	if strings.Contains(symbol, "USDT") || strings.Contains(instrumentID, "USDT") {
		return g10entryproof.CtTypeLinear
	}
	return g10entryproof.CtTypeUnknown
}

func loadInstrumentCatalog(historyRoot string) (datatruth.InstrumentCatalog, bool) {
	if strings.TrimSpace(historyRoot) == "" {
		return datatruth.InstrumentCatalog{}, false
	}
	candidates := []string{
		filepath.Join(historyRoot, "okx_directional", "instrument_meta.json"),
		filepath.Join(historyRoot, "okx", "instrument_meta.json"),
		filepath.Join(historyRoot, "instrument_meta.json"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		cat, err := loadInstrumentCatalogFile(path)
		if err == nil {
			return cat, true
		}
	}
	return datatruth.InstrumentCatalog{}, false
}

func loadInstrumentCatalogFile(path string) (datatruth.InstrumentCatalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return datatruth.InstrumentCatalog{}, err
	}
	var probe struct {
		Versions []any `json:"versions"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && len(probe.Versions) > 0 {
		return mechanismpanel.LoadInstrumentCatalog(path)
	}
	return contextdata.ConvertInstrumentSnapshot(raw)
}

type minNotionalUnavailableError struct{}

func (minNotionalUnavailableError) Error() string { return "pit dynamic min notional unavailable" }

var errMinNotionalUnavailable = minNotionalUnavailableError{}
