package app

import "time"

func (e *FundingRateSniperEngine) noteDustResidual(phase string, symbol string, pair string, asset string, quantity float64, notionalUSDT float64, reason string) error {
	if e == nil || e.reportStore == nil || quantity <= executionQtyTolerance {
		return nil
	}
	record := DustLedgerRecord{
		TraceRecordMetadata: e.recordMetadata(),
		RunID:               e.runID,
		CycleID:             e.cycleIDSnapshot(),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Phase:               phase,
		Symbol:              symbol,
		Pair:                pair,
		Asset:               asset,
		Quantity:            formatFloat(quantity),
		NotionalUSDT:        formatFloat(maxFloat(notionalUSDT, 0)),
		Reason:              reason,
	}
	return e.reportStore.AppendDustLedger(record)
}
