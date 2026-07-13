package fingerprint

import (
	"errors"
	"sort"

	"lingxi/internal/datatruth"
)

const (
	CoverageSchemaVersion = "lingxi.router-g10-fingerprint.coverage.v1"
	CoverageFileName      = "personal_execution_fingerprint_coverage.json"
)

// CoverageReport explains why real execution evidence is or is not critical-known.
// It contains only public symbol names and aggregate counts.
type CoverageReport struct {
	SchemaVersion           string         `json:"schema_version"`
	Generation              string         `json:"generation"`
	FingerprintHash         string         `json:"fingerprint_hash"`
	ReportHash              string         `json:"report_hash"`
	RawCellCount            int            `json:"raw_cell_count"`
	SymbolCount             int            `json:"symbol_count"`
	SymbolDirectionCount    int            `json:"symbol_direction_count"`
	CriticalKnownCount      int            `json:"critical_known_count"`
	CriticalUnknownCount    int            `json:"critical_unknown_count"`
	CriticalKnownSymbols    []string       `json:"critical_known_symbols,omitempty"`
	CriticalUnknownByReason map[string]int `json:"critical_unknown_by_reason"`
}

// BuildCoverageReport resolves each observed symbol in both directions and counts every
// fail-closed reason without reading outcomes or producing research candidates.
func BuildCoverageReport(fp PersonalExecutionFingerprint) (CoverageReport, error) {
	symbolSet := map[string]struct{}{}
	for _, cell := range fp.Cells {
		if symbol := normalizeLookupSymbol(cell.Symbol); symbol != "" {
			symbolSet[symbol] = struct{}{}
		}
	}
	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	report := CoverageReport{
		SchemaVersion:           CoverageSchemaVersion,
		Generation:              fp.Generation,
		FingerprintHash:         fp.FingerprintHash,
		RawCellCount:            len(fp.Cells),
		SymbolCount:             len(symbols),
		CriticalUnknownByReason: map[string]int{},
	}
	knownSymbols := map[string]struct{}{}
	for _, symbol := range symbols {
		for _, direction := range []string{"long", "short"} {
			report.SymbolDirectionCount++
			cell, ok := ResolveCriticalCell(fp, symbol, direction)
			reasons := CriticalUnknownReasons(fp, cell, ok)
			if len(reasons) == 0 {
				report.CriticalKnownCount++
				knownSymbols[symbol] = struct{}{}
				continue
			}
			report.CriticalUnknownCount++
			for _, reason := range reasons {
				report.CriticalUnknownByReason[reason]++
			}
		}
	}
	for symbol := range knownSymbols {
		report.CriticalKnownSymbols = append(report.CriticalKnownSymbols, symbol)
	}
	sort.Strings(report.CriticalKnownSymbols)
	hashPayload := report
	hashPayload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(hashPayload)
	if err != nil {
		return CoverageReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

// ValidateCoverageReport verifies the report is bound to its fingerprint and content.
func ValidateCoverageReport(report CoverageReport, fingerprintHash string) error {
	if report.SchemaVersion != CoverageSchemaVersion {
		return errors.New("fingerprint coverage: schema mismatch")
	}
	if report.FingerprintHash == "" || report.FingerprintHash != fingerprintHash {
		return errors.New("fingerprint coverage: fingerprint hash mismatch")
	}
	payload := report
	payload.ReportHash = ""
	want, _, err := datatruth.HashJSON(payload)
	if err != nil {
		return err
	}
	if report.ReportHash == "" || report.ReportHash != want {
		return errors.New("fingerprint coverage: report hash mismatch")
	}
	return nil
}

// CriticalUnknownReasons returns all fail-closed reasons for a resolved cell.
func CriticalUnknownReasons(fp PersonalExecutionFingerprint, cell ExecutionCell, found bool) []string {
	var reasons []string
	add := func(condition bool, reason string) {
		if condition {
			reasons = append(reasons, reason)
		}
	}
	add(fp.FingerprintHash == "", "FINGERPRINT_HASH_MISSING")
	add(!found, "CELL_MISSING")
	if !found {
		return reasons
	}
	add(cell.SampleCount < MinCellSamples, "CRITICAL_SAMPLE_COUNT_LT_3")
	add(cell.FeeBPS.Status != StatusAvailable || cell.FeeBPS.N < MinCellSamples, "FEE_UNKNOWN_OR_SPARSE")
	add(cell.EntrySlipBPS.Status != StatusAvailable || cell.EntrySlipBPS.N < MinCellSamples, "ENTRY_SLIP_UNKNOWN_OR_SPARSE")
	add(cell.ExitSlipBPS.Status != StatusAvailable || cell.ExitSlipBPS.N < MinCellSamples, "EXIT_SLIP_UNKNOWN_OR_SPARSE")
	add(cell.LatencyMS.Status != StatusAvailable || cell.LatencyMS.N < MinCellSamples, "LATENCY_UNKNOWN_OR_SPARSE")
	add(cell.MakerFillRate.Status != StatusAvailable || cell.MakerFillRate.N < MinCellSamples, "MAKER_FILL_UNKNOWN_OR_SPARSE")
	add(cell.Instrument.Status != StatusAvailable, "INSTRUMENT_UNKNOWN")
	add(cell.Instrument.LotSize == "", "LOT_SIZE_UNKNOWN")
	add(cell.Instrument.ContractValue == "", "CONTRACT_VALUE_UNKNOWN")
	add(cell.Instrument.ContractType == ctTypeInverse || cell.Instrument.ContractType == ctTypeUnknown, "CONTRACT_TYPE_UNSUPPORTED")
	add(cell.Instrument.DynamicMinNotionalUSDT <= 0, "DYNAMIC_MIN_NOTIONAL_UNKNOWN")
	add(!sourceAvailable(fp, sourceMakerAttempts), "MAKER_ATTEMPTS_SOURCE_UNAVAILABLE")
	add(!sourceAvailable(fp, sourceExecutableShadow), "EXECUTABLE_SHADOW_SOURCE_UNAVAILABLE")
	return reasons
}
