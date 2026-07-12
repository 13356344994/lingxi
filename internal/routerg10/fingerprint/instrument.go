package fingerprint

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	contextdata "lingxi/internal/datatruth/context"
	"lingxi/internal/datatruth"
	"lingxi/internal/mechanismpanel"
)

const (
	ctTypeLinear  = "linear"
	ctTypeInverse = "inverse"
	ctTypeUnknown = "unknown"
)

func loadInstrumentCatalog(path string) (datatruth.InstrumentCatalog, error) {
	if strings.TrimSpace(path) == "" {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("instrument meta path empty")
	}
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

func instrumentForSymbol(cat datatruth.InstrumentCatalog, symbol, venue string, at time.Time, price float64) InstrumentConstraints {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return InstrumentConstraints{Status: StatusUnknown}
	}
	ids := []string{symbol, symbol + "-SWAP"}
	if strings.HasSuffix(symbol, "-SWAP") {
		ids = append([]string{strings.TrimSuffix(symbol, "-SWAP"), symbol}, ids...)
	}
	knownAt := at.UTC()
	if knownAt.IsZero() {
		knownAt = time.Now().UTC()
	}
	for _, id := range ids {
		v, ok := cat.InstrumentAt(id, knownAt, knownAt)
		if !ok {
			continue
		}
		if venue != "" && !strings.EqualFold(strings.TrimSpace(v.Venue), venue) && strings.TrimSpace(v.Venue) != "" {
			continue
		}
		return buildInstrumentConstraints(v, price)
	}
	return InstrumentConstraints{Status: StatusUnknown}
}

func buildInstrumentConstraints(v datatruth.InstrumentVersion, price float64) InstrumentConstraints {
	lot := strings.TrimSpace(v.LotSize)
	ctVal := strings.TrimSpace(v.ContractValue)
	ctType := inferContractType(v)
	c := InstrumentConstraints{
		TickSize:      strings.TrimSpace(v.TickSize),
		LotSize:       lot,
		MinSize:       lot,
		ContractValue: ctVal,
		ContractType:  ctType,
		MinNotional:   strings.TrimSpace(v.MinNotional),
		Status:        StatusUnknown,
	}
	if strings.TrimSpace(v.TickSize) == "" || lot == "" || ctVal == "" {
		return c
	}
	if ctType == ctTypeInverse || ctType == ctTypeUnknown {
		return c
	}
	return finalizeLinearInstrument(c, lot, ctVal, price)
}

// KnownLinearInstrument returns explicit OKX linear USDT instrument constraints for fixtures.
// refPrice is the reference mark used to freeze dynamic min-notional (min_sz * ct_val * price).
func KnownLinearInstrument(minSz, ctVal, tickSize string, refPrice float64) InstrumentConstraints {
	lot := strings.TrimSpace(minSz)
	if lot == "" {
		lot = "0.01"
	}
	cv := strings.TrimSpace(ctVal)
	if cv == "" {
		cv = "1"
	}
	c := InstrumentConstraints{
		TickSize:      strings.TrimSpace(tickSize),
		LotSize:       lot,
		MinSize:       lot,
		ContractValue: cv,
		ContractType:  ctTypeLinear,
		Status:        StatusUnknown,
	}
	return finalizeLinearInstrument(c, lot, cv, refPrice)
}

func finalizeLinearInstrument(c InstrumentConstraints, lot, ctVal string, price float64) InstrumentConstraints {
	if price <= 0 {
		return c
	}
	minSz, err := parsePositive(lot)
	if err != nil {
		return c
	}
	cv, err := parsePositive(ctVal)
	if err != nil {
		return c
	}
	c.DynamicMinNotionalUSDT = minSz * cv * price
	if c.DynamicMinNotionalUSDT <= 0 {
		return c
	}
	c.Status = StatusAvailable
	return c
}

func inferContractType(v datatruth.InstrumentVersion) string {
	symbol := strings.ToUpper(strings.TrimSpace(v.Symbol))
	instrumentID := strings.ToUpper(strings.TrimSpace(v.InstrumentID))
	venue := strings.ToLower(strings.TrimSpace(v.Venue))

	for _, id := range []string{symbol, instrumentID} {
		if id == "" {
			continue
		}
		if strings.Contains(id, "USD-SWAP") && !strings.Contains(id, "USDT") {
			return ctTypeInverse
		}
	}
	if okxLinearUSDTEvidence(symbol, instrumentID, venue) {
		return ctTypeLinear
	}
	return ctTypeUnknown
}

// okxLinearUSDTEvidence maps lot/ctVal-only OKX meta to linear USDT swap semantics when
// symbol identity carries USDT quote evidence. Inverse and ambiguous ids remain fail-closed.
func okxLinearUSDTEvidence(symbol, instrumentID, venue string) bool {
	if venue != "" && venue != "okx" {
		return false
	}
	for _, id := range []string{symbol, instrumentID} {
		if id == "" {
			continue
		}
		if strings.Contains(id, "USDT") {
			return true
		}
	}
	return false
}

func statusFromInstrument(v datatruth.InstrumentVersion) string {
	return buildInstrumentConstraints(v, 0).Status
}

func parsePositive(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("non-positive")
	}
	return v, nil
}
