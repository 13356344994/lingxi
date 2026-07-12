package fingerprint_test

import (
	"testing"

	"lingxi/internal/routerg10/fingerprint"
)

func TestCellCriticalUnknownSparseSample(t *testing.T) {
	cell := fingerprint.ExecutionCell{
		SampleCount: 1,
		FeeBPS:      fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 1, P50: 1, P90: 2},
		EntrySlipBPS: fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 1},
		ExitSlipBPS:  fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 1},
		LatencyMS:    fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 1, P90: 100},
		MakerFillRate: fingerprint.RateMetric{Status: fingerprint.StatusAvailable, N: 1, Rate: 0.5},
		Instrument: fingerprint.InstrumentConstraints{
			Status: fingerprint.StatusAvailable, LotSize: "1", ContractValue: "1", TickSize: "0.1",
			ContractType: "linear", DynamicMinNotionalUSDT: 5,
		},
	}
	if !fingerprint.CellHasCriticalUnknown(cell) {
		t.Fatal("sparse cell must be critical unknown")
	}
}

func TestCellCriticalUnknownMissingDynamicMinNotional(t *testing.T) {
	cell := fingerprint.ExecutionCell{
		SampleCount: 5,
		FeeBPS:      fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5, P50: 1, P90: 2},
		EntrySlipBPS: fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5},
		ExitSlipBPS:  fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5},
		LatencyMS:    fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5, P90: 100},
		MakerFillRate: fingerprint.RateMetric{Status: fingerprint.StatusAvailable, N: 5, Rate: 0.5},
		Instrument:   fingerprint.InstrumentConstraints{Status: fingerprint.StatusUnknown},
	}
	if !fingerprint.CellHasCriticalUnknown(cell) {
		t.Fatal("missing dynamic min-notional must be critical unknown")
	}
}

func TestFingerprintCriticalUnknownRequiresSignalAckFill(t *testing.T) {
	fp := fingerprint.PersonalExecutionFingerprint{
		FingerprintHash: "abc",
		SourceStatus: map[string]fingerprint.SourceStatus{
			"maker_attempts":    {Status: fingerprint.StatusAvailable, Decoded: 3},
			"executable_shadow": {Status: fingerprint.StatusUnknown},
		},
		Cells: []fingerprint.ExecutionCell{{
			SampleCount: 5,
			FeeBPS:      fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5, P90: 2},
			EntrySlipBPS: fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5},
			ExitSlipBPS:  fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5},
			LatencyMS:    fingerprint.QuantileMetric{Status: fingerprint.StatusAvailable, N: 5, P90: 100},
			MakerFillRate: fingerprint.RateMetric{Status: fingerprint.StatusAvailable, N: 5, Rate: 0.5},
			Instrument: fingerprint.InstrumentConstraints{
				Status: fingerprint.StatusAvailable, LotSize: "1", ContractValue: "1", TickSize: "0.1",
				ContractType: "linear", DynamicMinNotionalUSDT: 5,
			},
		}},
	}
	if !fingerprint.FingerprintCriticalUnknown(fp, &fp.Cells[0]) {
		t.Fatal("missing executable_shadow must fail closed")
	}
}
