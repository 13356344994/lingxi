package residualmap

import (
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/fingerprint"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

const (
	// FailClosedFingerprintCoverage documents a fail-closed zero-candidate run when no
	// catalog symbol can resolve critical-known personal execution data.
	FailClosedFingerprintCoverage = "FINGERPRINT_NO_CRITICAL_KNOWN_COVERAGE"
)

// SymbolHasCriticalKnownCoverage reports whether one symbol has at least one direction
// with trustworthy personal execution data.
func SymbolHasCriticalKnownCoverage(fp fingerprint.PersonalExecutionFingerprint, symbol string) bool {
	for _, direction := range []string{"long", "short"} {
		adj := LookupPersonalAdjustments(fp, symbol, direction)
		if !adj.CriticalUnknown {
			return true
		}
	}
	return false
}

// CriticalKnownSymbolSet returns the deterministic symbol universe that can possibly
// pass the frozen execution-fingerprint gate. Symbols outside this set are not research
// hypotheses because UNKNOWN costs force NO_TRADE before outcome evaluation.
func CriticalKnownSymbolSet(fp fingerprint.PersonalExecutionFingerprint) map[string]bool {
	out := map[string]bool{}
	for _, cell := range fp.Cells {
		symbol := normalizeSymbol(cell.Symbol)
		if symbol == "" || out[symbol] {
			continue
		}
		if SymbolHasCriticalKnownCoverage(fp, symbol) {
			out[symbol] = true
		}
	}
	return out
}

// ProfileCatalogHasCriticalKnownCoverage scans one profile's primary catalog symbols.
func ProfileCatalogHasCriticalKnownCoverage(
	fp fingerprint.PersonalExecutionFingerprint,
	bundle reverseevent.MapBundle,
	catalog historydata.HistoryCatalog,
	maxFiles int,
) (bool, error) {
	selection, err := eventmap.SelectPrimaryFiles(
		catalog, bundle.Manifest.ProfileID, maxFiles, bundle.Manifest.CatalogSymbolCount,
	)
	if err != nil {
		return false, err
	}
	for _, file := range selection.Files {
		if SymbolHasCriticalKnownCoverage(fp, file.Symbol) {
			return true, nil
		}
	}
	return false, nil
}

// ShouldFailClosedZeroCandidates reports whether every catalog symbol across target
// profiles lacks critical-known fingerprint coverage. When true and unknown blocks open,
// no triggered event can be accepted, so scanning is logically equivalent to zero output.
func ShouldFailClosedZeroCandidates(
	fp fingerprint.PersonalExecutionFingerprint,
	bundles []reverseevent.MapBundle,
	catalog historydata.HistoryCatalog,
	maxFiles int,
) (bool, error) {
	if !g10protocol.FingerprintUnknownBlocksOpen {
		return false, nil
	}
	for _, bundle := range bundles {
		ok, err := ProfileCatalogHasCriticalKnownCoverage(fp, bundle, catalog, maxFiles)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return true, nil
}
