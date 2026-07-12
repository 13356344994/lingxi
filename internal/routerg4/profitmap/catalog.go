package profitmap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/protocol"
)

type SelectedFile struct {
	Symbol   string `json:"symbol"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	FileHash string `json:"file_hash"`
}

type Selection struct {
	Files     []SelectedFile `json:"files"`
	Available int            `json:"available"`
	Truncated bool           `json:"truncated"`
}

type MapScope struct {
	AvailablePrimaryFiles int    `json:"available_primary_files"`
	SelectedPrimaryFiles  int    `json:"selected_primary_files"`
	SelectionTruncated    bool   `json:"selection_truncated"`
	FullMarket            bool   `json:"full_market"`
	MapScope              string `json:"map_scope"`
}

func ScopeForSelection(selection Selection) MapScope {
	scope := MapScope{
		AvailablePrimaryFiles: selection.Available,
		SelectedPrimaryFiles:  len(selection.Files),
		SelectionTruncated:    selection.Truncated,
		FullMarket:            !selection.Truncated,
		MapScope:              protocol.FullMarketScope,
	}
	if selection.Truncated {
		scope.MapScope = protocol.TruncatedSmokeScope
	}
	return scope
}

func ValidateSelectionScope(selection Selection, catalogSymbolLimit int, allowTruncatedSmoke bool) (MapScope, error) {
	scope := ScopeForSelection(selection)
	err := protocol.ValidateFullMarketScope(protocol.FullMarketSummary{
		Available: scope.AvailablePrimaryFiles, Selected: scope.SelectedPrimaryFiles,
		Truncated: scope.SelectionTruncated, FullMarket: scope.FullMarket, Scope: scope.MapScope,
	}, catalogSymbolLimit, allowTruncatedSmoke)
	return scope, err
}

const (
	timeHour   = time.Hour
	timeMinute = time.Minute
)

const (
	familyOKX1H = "okx_1h"
	familyOKX5M = "okx_5m"
)

// ProfileSpecFor returns the frozen profile specification.
func ProfileSpecFor(profileID string) (ProfileSpec, error) {
	switch profileID {
	case ProfileOKX1H:
		return ProfileSpec{
			ID:                    ProfileOKX1H,
			PrimaryFamily:         protocol.OKX1HPrimaryFamily,
			BarDuration:           timeHour,
			MaxFeatureLookback:    time.Duration(protocol.OKX1HMaxFeatureLookbackHours) * timeHour,
			MaxOutcomeHorizon:     24 * timeHour,
			RollingQuoteVolumeWin: 24 * timeHour,
			Horizons: []HorizonSpec{
				{Label: "1h", BarCount: 1, Duration: timeHour},
				{Label: "4h", BarCount: 4, Duration: 4 * timeHour},
				{Label: "24h", BarCount: 24, Duration: 24 * timeHour},
			},
		}, nil
	case ProfileOKX5M:
		return ProfileSpec{
			ID:                    ProfileOKX5M,
			PrimaryFamily:         protocol.OKX5MPrimaryFamily,
			BarDuration:           5 * timeMinute,
			MaxFeatureLookback:    time.Duration(protocol.OKX5MMaxFeatureLookbackHours) * timeHour,
			MaxOutcomeHorizon:     time.Duration(protocol.OKX5MMaxOutcomeHorizonHours) * timeHour,
			RollingQuoteVolumeWin: 24 * timeHour,
			Horizons: []HorizonSpec{
				{Label: "5m", BarCount: 1, Duration: 5 * timeMinute},
				{Label: "15m", BarCount: 3, Duration: 15 * timeMinute},
				{Label: "60m", BarCount: 12, Duration: timeHour},
				{Label: "240m", BarCount: 48, Duration: 4 * timeHour},
			},
		}, nil
	default:
		return ProfileSpec{}, fmt.Errorf("profitmap: unknown profile %q", profileID)
	}
}

// SelectedSymbols returns catalog-qualified symbols for one profile primary family.
func SelectedSymbols(catalog historydata.HistoryCatalog, profileID string, limit int) ([]string, error) {
	selection, err := SelectPrimaryFiles(catalog, profileID, int(^uint(0)>>1), limit)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	return symbols, nil
}

// SelectPrimaryFiles deterministically applies both the requested symbol limit
// and the mandatory max-files safety bound. A zero symbol limit means full
// catalog subject to maxFiles.
func SelectPrimaryFiles(catalog historydata.HistoryCatalog, profileID string, maxFiles, symbolLimit int) (Selection, error) {
	spec, err := ProfileSpecFor(profileID)
	if err != nil {
		return Selection{}, err
	}
	if maxFiles <= 0 || symbolLimit < 0 {
		return Selection{}, fmt.Errorf("profitmap: max-files must be positive and catalog-symbol-limit non-negative")
	}
	var files []SelectedFile
	for _, family := range catalog.Families {
		if family.Name != spec.PrimaryFamily {
			continue
		}
		for _, file := range family.Files {
			if file.Empty || !strings.HasSuffix(file.Path, ".jsonl") {
				continue
			}
			instrument := strings.TrimSuffix(filepath.Base(file.Path), ".jsonl")
			if instrument != "" {
				files = append(files, SelectedFile{
					Symbol: instrument, Path: filepath.ToSlash(filepath.Clean(file.Path)),
					Size: file.Size, FileHash: file.FileHash,
				})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	available := len(files)
	cap := maxFiles
	if symbolLimit > 0 && symbolLimit < cap {
		cap = symbolLimit
	}
	if len(files) > cap {
		files = files[:cap]
	}
	return Selection{Files: files, Available: available, Truncated: len(files) < available}, nil
}

// CatalogFilePath resolves a catalog-relative path to an absolute path.
func CatalogFilePath(rootDir, relPath string) string {
	return filepath.Join(rootDir, filepath.FromSlash(relPath))
}

// VerifyCatalogFile checks one catalog file exists and matches catalog stats.
func VerifyCatalogFile(catalog historydata.HistoryCatalog, rootDir, relPath string) error {
	familyName := strings.Split(relPath, "/")[0]
	var stats *historydata.FileStats
	for _, family := range catalog.Families {
		if family.Name != familyName {
			continue
		}
		for i := range family.Files {
			if family.Files[i].Path == relPath {
				stats = &family.Files[i]
				break
			}
		}
	}
	if stats == nil {
		return fmt.Errorf("profitmap: catalog missing file %q", relPath)
	}
	abs := CatalogFilePath(rootDir, relPath)
	fi, err := osStat(abs)
	if err != nil {
		return fmt.Errorf("profitmap: catalog file %q: %w", relPath, err)
	}
	if fi.Size() != stats.Size {
		return fmt.Errorf("profitmap: catalog size drift for %q", relPath)
	}
	file, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("profitmap: open catalog file %q: %w", relPath, err)
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, file, make([]byte, 256*1024)); err != nil {
		return fmt.Errorf("profitmap: hash catalog file %q: %w", relPath, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); stats.FileHash == "" || got != stats.FileHash {
		return fmt.Errorf("profitmap: catalog hash drift for %q", relPath)
	}
	return nil
}

func CatalogFile(catalog historydata.HistoryCatalog, relPath string) (historydata.FileStats, bool) {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	for _, family := range catalog.Families {
		for _, file := range family.Files {
			if filepath.ToSlash(filepath.Clean(file.Path)) == clean {
				return file, true
			}
		}
	}
	return historydata.FileStats{}, false
}

// LiquidityBucket returns floor(log10(avgQuoteVolume)) clamped to [0,9].
func LiquidityBucket(avgQuoteVolume float64) string {
	value := liquidityBucketValue(avgQuoteVolume)
	return fmt.Sprintf("liq_%d", int(value))
}

func liquidityBucketValue(avgQuoteVolume float64) float64 {
	if avgQuoteVolume <= 0 {
		return 0
	}
	value := math.Floor(math.Log10(avgQuoteVolume))
	if value < 0 {
		value = 0
	}
	if value > 9 {
		value = 9
	}
	return value
}

// RollingWindowBars converts a time window to bar count for a profile.
func RollingWindowBars(spec ProfileSpec) int {
	if spec.BarDuration <= 0 {
		return 24
	}
	bars := int(spec.RollingQuoteVolumeWin / spec.BarDuration)
	if bars < 1 {
		return 1
	}
	return bars
}

// PurgeEmbargoForHorizon returns purge/embargo duration for one horizon.
func PurgeEmbargoForHorizon(spec ProfileSpec, horizon HorizonSpec) time.Duration {
	lookback := spec.MaxFeatureLookback
	hold := horizon.Duration
	if hold > lookback {
		return hold
	}
	return lookback
}

// MaxPurgeEmbargo returns the largest purge/embargo across profile horizons.
func MaxPurgeEmbargo(spec ProfileSpec) time.Duration {
	max := spec.MaxFeatureLookback
	for _, h := range spec.Horizons {
		if pe := PurgeEmbargoForHorizon(spec, h); pe > max {
			max = pe
		}
	}
	return max
}

// AllProfileIDs returns frozen profile ids in protocol order.
func AllProfileIDs() []string {
	return append([]string(nil), protocol.DataProfileIDs...)
}

// osStat is overridden in tests.
var osStat = func(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
