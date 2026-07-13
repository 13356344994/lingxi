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
	resolved, ok := fingerprint.ResolveCriticalCell(fp, sym, dir)
	if !ok || fingerprint.FingerprintCriticalUnknown(fp, &resolved) {
		adj.CriticalUnknown = true
		return adj
	}
	if resolved.FeeBPS.Status == fingerprint.StatusAvailable && resolved.FeeBPS.P90 > 0 {
		adj.FeeDeltaBPS = resolved.FeeBPS.P90
	}
	if resolved.EntrySlipBPS.Status == fingerprint.StatusAvailable && resolved.ExitSlipBPS.Status == fingerprint.StatusAvailable {
		adj.SlipDeltaBPS = resolved.EntrySlipBPS.P90 + resolved.ExitSlipBPS.P90
	}
	if resolved.LatencyMS.Status == fingerprint.StatusAvailable {
		adj.LatencyP90MS = resolved.LatencyMS.P90
	}
	if resolved.MakerWaitMS.Status == fingerprint.StatusAvailable {
		adj.MakerWaitP90MS = resolved.MakerWaitMS.P90
	}
	if resolved.FeeBPS.Status == fingerprint.StatusAvailable && resolved.FeeBPS.P90 > 20 {
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
