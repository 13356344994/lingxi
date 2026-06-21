package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const equitySnapshotsFileName = "equity_snapshots.jsonl"
const equitySampleInterval = 3 * time.Minute

var equityStableCoins = map[string]struct{}{
	"USDT": {}, "USDC": {}, "FDUSD": {}, "DAI": {}, "TUSD": {}, "USDE": {},
}

type equitySnapshotRecord struct {
	TS       string  `json:"ts"`
	Venue    string  `json:"venue"`
	CashUSDT float64 `json:"cash_usdt"`
	Complete *bool   `json:"complete,omitempty"`
}

func (e *CrossVenueExecutor) equitySnapshotsPath() string {
	return filepath.Join(crossVenueRecordDir(e.recordDir), equitySnapshotsFileName)
}

type equityReporter interface {
	FetchEquityUSDT(ctx context.Context) (float64, error)
}

func (e *CrossVenueExecutor) cashUSDTFromBalances(b venue.Balances) float64 {
	var total float64
	for asset, bal := range b.ByAsset {
		if _, ok := equityStableCoins[strings.ToUpper(asset)]; ok {
			total += bal.Free + bal.Locked
		}
	}
	return total
}

func (e *CrossVenueExecutor) sampleEquityOnce(ctx context.Context) {
	if e == nil {
		return
	}
	now := e.nowFn().UTC()
	ts := now.Format(time.RFC3339)
	var records []equitySnapshotRecord
	for name, v := range e.venues {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var cash float64
		if er, ok := v.(equityReporter); ok {
			eq, err := er.FetchEquityUSDT(cctx)
			if err == nil {
				cash = eq
			} else {
				e.logger.Printf("[权益采样] %s 用现金口径回退(无总权益接口/拉取失败): %v", name, err)
				bal, balErr := v.FetchBalances(cctx)
				if balErr != nil {
					cancel()
					e.logger.Printf("[权益采样] %s 拉余额失败(跳过本轮): %v", name, balErr)
					continue
				}
				cash = e.cashUSDTFromBalances(bal)
			}
		} else {
			bal, err := v.FetchBalances(cctx)
			if err != nil {
				cancel()
				e.logger.Printf("[权益采样] %s 拉余额失败(跳过本轮): %v", name, err)
				continue
			}
			cash = e.cashUSDTFromBalances(bal)
		}
		cancel()
		records = append(records, equitySnapshotRecord{
			TS:       ts,
			Venue:    name,
			CashUSDT: cash,
		})
	}
	if len(records) == 0 {
		return
	}
	expectedVenues := len(e.venues)
	complete := len(records) >= expectedVenues
	if !complete {
		var missing []string
		got := make(map[string]struct{}, len(records))
		for _, rec := range records {
			got[rec.Venue] = struct{}{}
		}
		for name := range e.venues {
			if _, ok := got[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		e.logger.Printf("[权益采样] 本轮残缺: 成功 %d/期望 %d, 缺 [%s]", len(records), expectedVenues, strings.Join(missing, ","))
	}
	completeFlag := complete
	for i := range records {
		records[i].Complete = &completeFlag
	}
	e.equitySnapMu.Lock()
	defer e.equitySnapMu.Unlock()
	path := e.equitySnapshotsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.logger.Printf("[权益采样] 创建目录失败: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		e.logger.Printf("[权益采样] 打开文件失败: %v", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			e.logger.Printf("[权益采样] 写入记录失败: %v", err)
			return
		}
	}
}

func (e *CrossVenueExecutor) RunEquitySampler(ctx context.Context) {
	if e == nil {
		return
	}
	e.logger.Printf("[权益采样] 已启动: interval=%s 口径=各所USDT现金余额真实变化", equitySampleInterval)
	e.sampleEquityOnce(ctx)
	ticker := time.NewTicker(equitySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.sampleEquityOnce(ctx)
		}
	}
}

// TodayCSTEquityDeltas 按北京时间自然日界,用 equity_snapshots 计算各所账户总值相对日初的真实变化。
// 返回值: deltas(今日变化)、currents(当前快照)、baselines(日界后首条快照)、dayStart、earliestBaselineTS。
func (e *CrossVenueExecutor) TodayCSTEquityDeltas(now time.Time) (deltas, currents, baselines map[string]float64, dayStart, earliestBaseline time.Time) {
	dayStart = beijingCSTDayStart(now)
	deltas, currents, baselines, earliestBaseline = e.equityEarnings(dayStart)
	return deltas, currents, baselines, dayStart, earliestBaseline
}

func (e *CrossVenueExecutor) equityEarnings(windowStart time.Time) (deltas, currents, baselines map[string]float64, earliest time.Time) {
	deltas = make(map[string]float64)
	currents = make(map[string]float64)
	baselines = make(map[string]float64)
	if e == nil {
		return deltas, currents, baselines, earliest
	}
	e.equitySnapMu.Lock()
	defer e.equitySnapMu.Unlock()
	path := e.equitySnapshotsPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return deltas, currents, baselines, earliest
		}
		return deltas, currents, baselines, earliest
	}
	defer f.Close()

	latestTS := make(map[string]time.Time)
	baselineTS := make(map[string]time.Time)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			continue
		}
		venueName := rec.Venue
		if prev, ok := latestTS[venueName]; !ok || !ts.Before(prev) {
			currents[venueName] = rec.CashUSDT
			latestTS[venueName] = ts
		}
		if !ts.Before(windowStart) {
			if prev, ok := baselineTS[venueName]; !ok || ts.Before(prev) {
				baselines[venueName] = rec.CashUSDT
				baselineTS[venueName] = ts
			}
		}
	}

	for venueName, current := range currents {
		baseline, ok := baselines[venueName]
		if !ok {
			baseline = current
			baselines[venueName] = current
		}
		deltas[venueName] = current - baseline
	}

	for _, ts := range baselineTS {
		if earliest.IsZero() || ts.Before(earliest) {
			earliest = ts
		}
	}
	return deltas, currents, baselines, earliest
}

func (e *CrossVenueExecutor) pruneEquitySnapshots(before time.Time) (removed int, err error) {
	if e == nil {
		return 0, nil
	}
	e.equitySnapMu.Lock()
	defer e.equitySnapMu.Unlock()
	path := e.equitySnapshotsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			kept = append(kept, line)
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			kept = append(kept, line)
			continue
		}
		if ts.Before(before) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	tmpPath := filepath.Join(filepath.Dir(path), ".equity_snapshots.jsonl.tmp")
	out := strings.Join(kept, "\n")
	if len(kept) > 0 {
		out += "\n"
	}
	if err := os.WriteFile(tmpPath, []byte(out), 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, err
	}
	return removed, nil
}
