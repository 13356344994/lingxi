package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	if pos.HighLegFundingUSDT != 0 || pos.LowLegFundingUSDT != 0 {
		writeRecord(pos.HighVenue, pos.HighLegFundingUSDT)
		writeRecord(pos.LowVenue, pos.LowLegFundingUSDT)
		return
	}
	if pos.EstFundingUSDT != 0 {
		writeRecord(pos.HighVenue, pos.EstFundingUSDT)
	}
}

func (e *CrossVenueExecutor) realizedEarningsByVenue(since time.Time) map[string]float64 {
	out := make(map[string]float64)
	if e == nil {
		return out
	}
	e.earningsMu.Lock()
	defer e.earningsMu.Unlock()

	path := e.earningsLedgerPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec crossVenueEarningRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			continue
		}
		if ts.Before(since) {
			continue
		}
		out[rec.Venue] += rec.UsdtPnL
	}
	return out
}

func (e *CrossVenueExecutor) openAccruedByVenue(windowStart time.Time) map[string]float64 {
	out := make(map[string]float64)
	if e == nil {
		return out
	}
	e.mu.RLock()
	defer e.mu.RUnlock()

	now := e.nowFn()
	for _, pos := range e.positions {
		ageHours := now.Sub(pos.OpenedAt).Hours()
		if ageHours <= 0 {
			continue
		}
		overlapHours := now.Sub(windowStart).Hours()
		ratio := overlapHours / ageHours
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		if pos.HighLegFundingUSDT != 0 || pos.LowLegFundingUSDT != 0 {
			out[pos.HighVenue] += pos.HighLegFundingUSDT * ratio
			out[pos.LowVenue] += pos.LowLegFundingUSDT * ratio
		} else {
			out[pos.HighVenue] += pos.EstFundingUSDT * ratio
		}
	}
	return out
}

// earliestActivitySince 返回跨所侧最早的「有效数据时刻」(在场持仓开仓时刻、窗口内已实现收益记录时刻),
// 全部下夹到 windowStart(即不早于 24h 窗口起点)。用于仪表盘把「近 N 小时」标签按真实数据覆盖时长展示,
// 跨进程重启稳定(数据落盘,不随重启把窗口缩成几分钟)。无任何数据时返回零值。
func (e *CrossVenueExecutor) earliestActivitySince(windowStart time.Time) time.Time {
	if e == nil {
		return time.Time{}
	}
	var earliest time.Time
	note := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if t.Before(windowStart) {
			t = windowStart
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}

	e.mu.RLock()
	for _, pos := range e.positions {
		note(pos.OpenedAt)
	}
	e.mu.RUnlock()

	e.earningsMu.Lock()
	if f, err := os.Open(e.earningsLedgerPath()); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var rec crossVenueEarningRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue
			}
			if ts, err := time.Parse(time.RFC3339, rec.TS); err == nil {
				note(ts)
			}
		}
		_ = f.Close()
	}
	e.earningsMu.Unlock()

	return earliest
}

func (e *CrossVenueExecutor) pruneEarnings(before time.Time) (removed int, err error) {
	if e == nil {
		return 0, nil
	}
	e.earningsMu.Lock()
	defer e.earningsMu.Unlock()

	path := e.earningsLedgerPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var rec crossVenueEarningRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			kept = append(kept, trimmed)
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			kept = append(kept, trimmed)
			continue
		}
		if ts.Before(before) {
			removed++
			continue
		}
		kept = append(kept, trimmed)
	}

	if removed == 0 {
		return 0, nil
	}

	dir := filepath.Dir(path)
	tmpPath := filepath.Join(dir, fmt.Sprintf(".%s.tmp", crossVenueEarningsFileName))
	content := strings.Join(kept, "\n")
	if len(kept) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	return removed, nil
}
