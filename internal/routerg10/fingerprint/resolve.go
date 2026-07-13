package fingerprint

import "strings"

// ResolveCriticalCell composes the conservative symbol/direction execution view used by
// G10 opens. Raw cells remain partitioned by the frozen fingerprint dimensions; this
// resolver only joins complementary aggregates from heterogeneous source streams.
func ResolveCriticalCell(fp PersonalExecutionFingerprint, symbol, direction string) (ExecutionCell, bool) {
	symbol = normalizeLookupSymbol(symbol)
	direction = strings.ToLower(strings.TrimSpace(direction))
	var matched []ExecutionCell
	for _, cell := range fp.Cells {
		if normalizeLookupSymbol(cell.Symbol) != symbol {
			continue
		}
		if cell.Direction != "" && cell.Direction != direction && cell.Direction != "unknown" {
			continue
		}
		matched = append(matched, cell)
	}
	if len(matched) == 0 {
		return ExecutionCell{}, false
	}

	resolved := ExecutionCell{
		CellKey: CellKey{
			Venue:          commonString(matched, func(c ExecutionCell) string { return c.Venue }),
			Symbol:         symbol,
			HourUTC:        "all",
			Direction:      direction,
			OrderType:      "composite",
			NotionalBucket: "all",
		},
		FeeBPS:         mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.FeeBPS }),
		EntrySlipBPS:   mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.EntrySlipBPS }),
		ExitSlipBPS:    mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.ExitSlipBPS }),
		MakerFillRate:  mergeRates(matched, func(c ExecutionCell) RateMetric { return c.MakerFillRate }),
		MakerWaitMS:    mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.MakerWaitMS }),
		LatencyMS:      mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.LatencyMS }),
		CancelRiskRate: mergeRates(matched, func(c ExecutionCell) RateMetric { return c.CancelRiskRate }),
		FundingRate:    mergeQuantiles(matched, func(c ExecutionCell) QuantileMetric { return c.FundingRate }),
		Instrument:     conservativeInstrument(matched),
	}
	resolved.SampleCount = minimumCriticalSamples(resolved)
	return resolved, true
}

func normalizeLookupSymbol(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	return strings.TrimSuffix(s, "-SWAP")
}

func mergeQuantiles(cells []ExecutionCell, metric func(ExecutionCell) QuantileMetric) QuantileMetric {
	out := QuantileMetric{Status: StatusUnknown}
	for _, cell := range cells {
		m := metric(cell)
		if m.Status != StatusAvailable || m.N <= 0 {
			continue
		}
		if out.Status != StatusAvailable || m.P50 > out.P50 {
			out.P50 = m.P50
		}
		if out.Status != StatusAvailable || m.P90 > out.P90 {
			out.P90 = m.P90
		}
		out.N += m.N
		out.Status = StatusAvailable
	}
	return out
}

func mergeRates(cells []ExecutionCell, metric func(ExecutionCell) RateMetric) RateMetric {
	out := RateMetric{Status: StatusUnknown}
	var weighted float64
	for _, cell := range cells {
		m := metric(cell)
		if m.Status != StatusAvailable || m.N <= 0 {
			continue
		}
		weighted += m.Rate * float64(m.N)
		out.N += m.N
		out.Status = StatusAvailable
	}
	if out.N > 0 {
		out.Rate = weighted / float64(out.N)
	}
	return out
}

func conservativeInstrument(cells []ExecutionCell) InstrumentConstraints {
	out := InstrumentConstraints{Status: StatusUnknown}
	for _, cell := range cells {
		inst := cell.Instrument
		if inst.Status != StatusAvailable || inst.DynamicMinNotionalUSDT <= 0 {
			continue
		}
		if out.Status != StatusAvailable || inst.DynamicMinNotionalUSDT > out.DynamicMinNotionalUSDT {
			out = inst
		}
	}
	return out
}

func minimumCriticalSamples(cell ExecutionCell) int {
	values := []int{
		cell.FeeBPS.N,
		cell.EntrySlipBPS.N,
		cell.ExitSlipBPS.N,
		cell.LatencyMS.N,
		cell.MakerFillRate.N,
	}
	minimum := 0
	for _, value := range values {
		if value <= 0 {
			return 0
		}
		if minimum == 0 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func commonString(cells []ExecutionCell, value func(ExecutionCell) string) string {
	var common string
	for _, cell := range cells {
		current := strings.TrimSpace(value(cell))
		if current == "" {
			continue
		}
		if common == "" {
			common = current
			continue
		}
		if !strings.EqualFold(common, current) {
			return "mixed"
		}
	}
	return common
}
