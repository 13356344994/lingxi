package fingerprint

// MinCellSamples is the minimum per-cell sample count before aggregates are trusted.
const MinCellSamples = 3

// CellHasCriticalUnknown reports whether one execution cell lacks trustworthy cost data.
func CellHasCriticalUnknown(c ExecutionCell) bool {
	if c.SampleCount < MinCellSamples {
		return true
	}
	if c.FeeBPS.Status == StatusUnknown ||
		c.EntrySlipBPS.Status == StatusUnknown ||
		c.ExitSlipBPS.Status == StatusUnknown ||
		c.LatencyMS.Status == StatusUnknown ||
		c.MakerFillRate.Status == StatusUnknown {
		return true
	}
	if c.Instrument.Status == StatusUnknown {
		return true
	}
	if c.Instrument.LotSize == "" || c.Instrument.ContractValue == "" {
		return true
	}
	if c.Instrument.ContractType == ctTypeInverse || c.Instrument.ContractType == ctTypeUnknown {
		return true
	}
	if c.Instrument.DynamicMinNotionalUSDT <= 0 {
		return true
	}
	return false
}

// FingerprintCriticalUnknown reports whether personal execution data is too sparse or
// missing min-notional/lot/maker-fill/signal-ack-fill coverage for one symbol lookup.
func FingerprintCriticalUnknown(fp PersonalExecutionFingerprint, cell *ExecutionCell) bool {
	if fp.FingerprintHash == "" {
		return true
	}
	if cell == nil {
		return true
	}
	if CellHasCriticalUnknown(*cell) {
		return true
	}
	if !sourceAvailable(fp, "maker_attempts") {
		return true
	}
	if !signalAckFillAvailable(fp, *cell) {
		return true
	}
	return false
}

func sourceAvailable(fp PersonalExecutionFingerprint, key string) bool {
	st, ok := fp.SourceStatus[key]
	return ok && st.Status == StatusAvailable && st.Decoded > 0
}

func signalAckFillAvailable(fp PersonalExecutionFingerprint, cell ExecutionCell) bool {
	if !sourceAvailable(fp, "executable_shadow") {
		return false
	}
	return cell.LatencyMS.Status == StatusAvailable && cell.LatencyMS.N >= MinCellSamples
}
