package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const crossVenueEarningsFileName = "earnings.jsonl"

type crossVenueEarningRecord struct {
	TS      string  `json:"ts"`
	Venue   string  `json:"venue"`
	Symbol  string  `json:"symbol"`
	UsdtPnL float64 `json:"usdt_pnl"`
	Source  string  `json:"source"`
}

func (e *CrossVenueExecutor) earningsLedgerPath() string {
	return filepath.Join(crossVenueRecordDir(e.recordDir), crossVenueEarningsFileName)
}

func (e *CrossVenueExecutor) appendRealizedEarning(pos CrossVenuePosition) {
	if e == nil {
		return
	}
	e.earningsMu.Lock()
	defer e.earningsMu.Unlock()

	path := e.earningsLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		if e.logger != nil {
			e.logger.Printf("[跨所收益] 创建目录失败 path=%s: %v", path, err)
		}
		return
	}

	ts := e.nowFn().UTC().Format(time.RFC3339)
	writeRecord := func(venue string, pnl float64) {
		if pnl == 0 {
			return
		}
		rec := crossVenueEarningRecord{
			TS:      ts,
			Venue:   venue,
			Symbol:  pos.Symbol,
			UsdtPnL: pnl,
			Source:  "crossvenue",
		}
		data, err := json.Marshal(rec)
		if err != nil {
			if e.logger != nil {
				e.logger.Printf("[跨所收益] 序列化失败: %v", err)
			}
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			if e.logger != nil {
				e.logger.Printf("[跨所收益] 打开台账失败 path=%s: %v", path, err)
			}
			return
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			if e.logger != nil {
				e.logger.Printf("[跨所收益] 写入台账失败 path=%s: %v", path, err)
			}
		}
		_ = f.Close()
	}

	writeRecord(pos.HighVenue, pos.HighLegFundingUSDT)
	writeRecord(pos.LowVenue, pos.LowLegFundingUSDT)
}
