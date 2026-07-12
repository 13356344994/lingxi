package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	sourceSettlements      = "settlements"
	sourceMakerAttempts    = "maker_attempts"
	sourceOpenDecisions    = "open_decisions"
	sourceExecutableShadow = "executable_shadow"
	sourceEquitySnapshots  = "equity_snapshots"
	sourceInstrumentMeta   = "instrument_meta"
)

// DefaultSourcePaths resolves standard record locations under recordRoot.
func DefaultSourcePaths(recordRoot string) SourcePaths {
	okxDir := filepath.Join(recordRoot, "okx_directional")
	return SourcePaths{
		Settlements:      filepath.Join(okxDir, "settlements.jsonl"),
		MakerAttempts:    filepath.Join(okxDir, "fast_maker_attempts.jsonl"),
		OpenDecisions:    filepath.Join(okxDir, "open_decision_events.jsonl"),
		ExecutableShadow: filepath.Join(okxDir, "fast_executable_shadow_events.jsonl"),
		EquitySnapshots:  filepath.Join(recordRoot, "crossvenue", "equity_snapshots.jsonl"),
		InstrumentMeta:   filepath.Join(okxDir, "instrument_meta.json"),
	}
}

func activeSources(paths SourcePaths) map[string]string {
	out := map[string]string{}
	add := func(key, path string) {
		if strings.TrimSpace(path) != "" {
			out[key] = path
		}
	}
	add(sourceSettlements, paths.Settlements)
	add(sourceMakerAttempts, paths.MakerAttempts)
	add(sourceOpenDecisions, paths.OpenDecisions)
	add(sourceExecutableShadow, paths.ExecutableShadow)
	add(sourceEquitySnapshots, paths.EquitySnapshots)
	add(sourceInstrumentMeta, paths.InstrumentMeta)
	return out
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func unknownStatus(path string) SourceStatus {
	return SourceStatus{Path: path, Status: StatusUnknown}
}
