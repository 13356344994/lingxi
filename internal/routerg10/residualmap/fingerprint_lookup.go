package residualmap

import (
	"strings"

	"lingxi/internal/routerg10/fingerprint"
)

// LookupPersonalAdjustments resolves conservative fee/slip/latency deltas for one event.
func LookupPersonalAdjustments(fp fingerprint.PersonalExecutionFingerprint, symbol, direction string) PersonalAdjustments {
	adj := PersonalAdjustments{}
	if fp.FingerprintHash == "" {
		adj.CriticalUnknown = true
		return adj
	}
	sym := normalizeSymbol(symbol)
	dir := strings.ToLower(direction)
	var best *fingerprint.ExecutionCell
	for i := range fp.Cells {
		c := &fp.Cells[i]
		if normalizeSymbol(c.Symbol) != sym {
			continue
		}
		if c.Direction != "" && c.Direction != dir && c.Direction != "unknown" {
			continue
		}
		if best == nil || c.SampleCount > best.SampleCount {
			best = c
		}
	}
	if fingerprint.FingerprintCriticalUnknown(fp, best) {
		adj.CriticalUnknown = true
		return adj
	}
	if best.FeeBPS.Status == fingerprint.StatusAvailable && best.FeeBPS.P90 > 0 {
		adj.FeeDeltaBPS = best.FeeBPS.P90
	}
	if best.EntrySlipBPS.Status == fingerprint.StatusAvailable && best.ExitSlipBPS.Status == fingerprint.StatusAvailable {
		adj.SlipDeltaBPS = best.EntrySlipBPS.P90 + best.ExitSlipBPS.P90
	}
	if best.LatencyMS.Status == fingerprint.StatusAvailable {
		adj.LatencyP90MS = best.LatencyMS.P90
	}
	if best.MakerWaitMS.Status == fingerprint.StatusAvailable {
		adj.MakerWaitP90MS = best.MakerWaitMS.P90
	}
	if best.FeeBPS.Status == fingerprint.StatusAvailable && best.FeeBPS.P90 > 20 {
		adj.CriticalNegative = true
	}
	return adj
}

// ActualEquityUSDT returns the fingerprint median cash tier for the actual label.
func ActualEquityUSDT(fp fingerprint.PersonalExecutionFingerprint) float64 {
	if fp.Equity.Status == fingerprint.StatusAvailable && fp.Equity.MedianCashUSDT > 0 {
		return fp.Equity.MedianCashUSDT
	}
	return 0
}

func normalizeSymbol(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	s = strings.TrimSuffix(s, "-SWAP")
	return s
}
