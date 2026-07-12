package eventmap

import (
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
)

// ProfileSpecFor returns the frozen profile specification via G4 profitmap.
func ProfileSpecFor(profileID string) (ProfileSpec, error) {
	return profitmap.ProfileSpecFor(profileID)
}

// ComputeBoundaries builds per-profile 60/20/20 partitions via G4 profitmap.
func ComputeBoundaries(spec ProfileSpec, btcBars []Bar) (Boundaries, error) {
	return profitmap.ComputeBoundaries(spec, btcBars)
}

// HashBoundaries returns a tamper-evident hash of partition boundaries.
func HashBoundaries(b Boundaries) (string, error) {
	return profitmap.HashBoundaries(b)
}

// SelectPrimaryFiles deterministically selects catalog files.
func SelectPrimaryFiles(catalog historydata.HistoryCatalog, profileID string, maxFiles, symbolLimit int) (profitmap.Selection, error) {
	return profitmap.SelectPrimaryFiles(catalog, profileID, maxFiles, symbolLimit)
}

// ValidateSelectionScope validates full-market scope.
func ValidateSelectionScope(selection profitmap.Selection, catalogSymbolLimit int, allowTruncatedSmoke bool) (MapScope, error) {
	return profitmap.ValidateSelectionScope(selection, catalogSymbolLimit, allowTruncatedSmoke)
}

// ScopeForSelection builds map scope metadata.
func ScopeForSelection(selection profitmap.Selection) MapScope {
	return profitmap.ScopeForSelection(selection)
}

// VerifyCatalogFile checks one catalog file exists and matches catalog stats.
func VerifyCatalogFile(catalog historydata.HistoryCatalog, rootDir, relPath string) error {
	return profitmap.VerifyCatalogFile(catalog, rootDir, relPath)
}

// LoadBars streams confirmed bars from one jsonl file.
func LoadBars(path string, maxLines int) ([]Bar, error) {
	return profitmap.LoadBars(path, maxLines)
}

// LoadSymbolFunding loads optional okx funding for one instrument.
func LoadSymbolFunding(historyRoot, instrument string, maxLines int) ([]profitmap.FundingPoint, error) {
	return profitmap.LoadSymbolFunding(historyRoot, instrument, maxLines)
}

// CatalogFilePath resolves a catalog-relative path.
func CatalogFilePath(rootDir, relPath string) string {
	return profitmap.CatalogFilePath(rootDir, relPath)
}

// CatalogFile returns one catalog file stats entry.
func CatalogFile(catalog historydata.HistoryCatalog, relPath string) (historydata.FileStats, bool) {
	return profitmap.CatalogFile(catalog, relPath)
}

// LiquidityBucket returns floor(log10(avgQuoteVolume)) label.
func LiquidityBucket(avgQuote float64) string {
	return profitmap.LiquidityBucket(avgQuote)
}

// RollingWindowBars converts rolling quote volume window to bar count.
func RollingWindowBars(spec ProfileSpec) int {
	return profitmap.RollingWindowBars(spec)
}
