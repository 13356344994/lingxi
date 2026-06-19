package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// LargeTradePnLThresholdUSDT 单笔 |trade_pnl| 超过此值视为异常(需隔离,不参与可信 replay)。
	LargeTradePnLThresholdUSDT = 50.0
	// DailyReconcileToleranceBps 日级四所合计权益变动 vs settlements Σnet_pnl 容差(相对日初权益, bps)。
	// 100 bps = 1%: 允许转账/手续费/资金费口径差,但不吞没 HEI 级幻觉(-801U)。
	DailyReconcileToleranceBps = 100.0
)

// LedgerReconReport 账本对账只读分析报告。
type LedgerReconReport struct {
	GeneratedAt              time.Time                   `json:"generated_at"`
	SettlementsPath          string                      `json:"settlements_path"`
	EquitySnapshotsPath      string                      `json:"equity_snapshots_path"`
	TotalSettlements         int                         `json:"total_settlements"`
	TotalNetPnL              float64                     `json:"total_net_pnl"`
	TotalNetPnLExclAbnormal  float64                     `json:"total_net_pnl_excl_abnormal"`
	EquityTotalStart         float64                     `json:"equity_total_start"`
	EquityTotalEnd           float64                     `json:"equity_total_end"`
	EquityTotalDelta         float64                     `json:"equity_total_delta"`
	Daily                    []LedgerReconDailyRow       `json:"daily"`
	AbnormalPnL              []LedgerReconAbnormalRecord `json:"abnormal_pnl"`
	FundingTruthStatusCounts map[string]int              `json:"funding_truth_status_counts"`
	Coverage                 LedgerReconCoverage         `json:"coverage"`
	ToleranceBps             float64                     `json:"tolerance_bps"`
	LargePnLThresholdUSDT    float64                     `json:"large_pnl_threshold_usdt"`
}

type LedgerReconDailyRow struct {
	DateUTC              string  `json:"date_utc"`
	EquityStartUSDT      float64 `json:"equity_start_usdt"`
	EquityEndUSDT        float64 `json:"equity_end_usdt"`
	EquityDeltaUSDT      float64 `json:"equity_delta_usdt"`
	SettlementsNetPnL    float64 `json:"settlements_net_pnl"`
	SettlementsNetExclAb float64 `json:"settlements_net_excl_abnormal"`
	DiffUSDT             float64 `json:"diff_usdt"`
	DiffExclAbnormalUSDT float64 `json:"diff_excl_abnormal_usdt"`
	ToleranceUSDT        float64 `json:"tolerance_usdt"`
	WithinTolerance      bool    `json:"within_tolerance"`
	WithinTolExclAbnorm  bool    `json:"within_tolerance_excl_abnormal"`
	TradeCount           int     `json:"trade_count"`
}

type LedgerReconAbnormalRecord struct {
	SettlementID string    `json:"settlement_id"`
	Symbol       string    `json:"symbol"`
	CloseTime    time.Time `json:"close_time"`
	HighVenue    string    `json:"high_venue"`
	LowVenue     string    `json:"low_venue"`
	TradePnL     float64   `json:"trade_pnl"`
	NetPnL       float64   `json:"net_pnl"`
	NotionalUSDT float64   `json:"notional_usdt"`
	Reason       string    `json:"reason"`
}

type LedgerReconCoverage struct {
	TrustedCount      int     `json:"trusted_count"`
	UnreconciledCount int     `json:"unreconciled_count"`
	AbnormalCount     int     `json:"abnormal_count"`
	TrustedPct        float64 `json:"trusted_pct"`
	UnreconciledPct   float64 `json:"unreconciled_pct"`
	AbnormalPct       float64 `json:"abnormal_pct"`
}

type equitySnapshotPoint struct {
	ts    time.Time
	total float64
}

// RunLedgerRecon 读取 settlements + equity_snapshots,输出对账报告(只读,不改实盘)。
func RunLedgerRecon(settlementsPath, equityPath string, now time.Time) (LedgerReconReport, error) {
	settlements, err := loadSettlementsJSONL(settlementsPath)
	if err != nil {
		return LedgerReconReport{}, err
	}
	equitySeries, err := loadEquitySnapshotSeries(equityPath)
	if err != nil {
		return LedgerReconReport{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	report := LedgerReconReport{
		GeneratedAt:              now.UTC(),
		SettlementsPath:          settlementsPath,
		EquitySnapshotsPath:      equityPath,
		TotalSettlements:         len(settlements),
		FundingTruthStatusCounts: map[string]int{},
		ToleranceBps:             DailyReconcileToleranceBps,
		LargePnLThresholdUSDT:    LargeTradePnLThresholdUSDT,
	}

	dayTrusted := map[string]bool{}
	dayTrustedExclAb := map[string]bool{}
	byDay := map[string][]CrossVenueSettlement{}
	dayEquityStart := map[string]float64{}
	dayEquityEnd := map[string]float64{}

	for _, rec := range settlements {
		report.TotalNetPnL += rec.NetPnL
		status := strings.TrimSpace(rec.FundingTruthStatus)
		if status == "" {
			status = "unknown"
		}
		report.FundingTruthStatusCounts[status]++

		if math.Abs(rec.TradePnL) > LargeTradePnLThresholdUSDT {
			report.AbnormalPnL = append(report.AbnormalPnL, LedgerReconAbnormalRecord{
				SettlementID: rec.SettlementID,
				Symbol:       rec.Symbol,
				CloseTime:    rec.CloseTime,
				HighVenue:    rec.HighVenue,
				LowVenue:     rec.LowVenue,
				TradePnL:     rec.TradePnL,
				NetPnL:       rec.NetPnL,
				NotionalUSDT: rec.NotionalUSDT,
				Reason:       fmt.Sprintf("|trade_pnl|=%.2f > threshold %.0f", rec.TradePnL, LargeTradePnLThresholdUSDT),
			})
		} else {
			report.TotalNetPnLExclAbnormal += rec.NetPnL
		}

		day := rec.CloseTime.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], rec)
	}

	sort.Slice(report.AbnormalPnL, func(i, j int) bool {
		return report.AbnormalPnL[i].CloseTime.Before(report.AbnormalPnL[j].CloseTime)
	})

	if len(equitySeries) > 0 {
		report.EquityTotalStart = equitySeries[0].total
		report.EquityTotalEnd = equitySeries[len(equitySeries)-1].total
		report.EquityTotalDelta = report.EquityTotalEnd - report.EquityTotalStart
	}

	for _, pt := range equitySeries {
		day := pt.ts.UTC().Format("2006-01-02")
		if _, ok := dayEquityStart[day]; !ok {
			dayEquityStart[day] = pt.total
		}
		dayEquityEnd[day] = pt.total
	}

	days := sortedDayKeys(byDay, dayEquityStart)
	for _, day := range days {
		recs := byDay[day]
		var netAll, netExclAb float64
		for _, rec := range recs {
			netAll += rec.NetPnL
			if math.Abs(rec.TradePnL) <= LargeTradePnLThresholdUSDT {
				netExclAb += rec.NetPnL
			}
		}
		start := dayEquityStart[day]
		end := dayEquityEnd[day]
		delta := end - start
		tol := start * DailyReconcileToleranceBps / 10000
		if tol < 0.01 {
			tol = 0.01
		}
		diffAll := delta - netAll
		diffExcl := delta - netExclAb
		row := LedgerReconDailyRow{
			DateUTC:              day,
			EquityStartUSDT:      start,
			EquityEndUSDT:        end,
			EquityDeltaUSDT:      delta,
			SettlementsNetPnL:    netAll,
			SettlementsNetExclAb: netExclAb,
			DiffUSDT:             diffAll,
			DiffExclAbnormalUSDT: diffExcl,
			ToleranceUSDT:        tol,
			WithinTolerance:      math.Abs(diffAll) <= tol,
			WithinTolExclAbnorm:  math.Abs(diffExcl) <= tol,
			TradeCount:           len(recs),
		}
		report.Daily = append(report.Daily, row)
		dayTrusted[day] = row.WithinTolerance
		dayTrustedExclAb[day] = row.WithinTolExclAbnorm
	}

	var trusted, unreconciled, abnormal int
	for _, rec := range settlements {
		day := rec.CloseTime.UTC().Format("2006-01-02")
		if math.Abs(rec.TradePnL) > LargeTradePnLThresholdUSDT {
			abnormal++
			continue
		}
		if dayTrustedExclAb[day] {
			trusted++
		} else {
			unreconciled++
		}
	}
	total := len(settlements)
	report.Coverage = LedgerReconCoverage{
		TrustedCount:      trusted,
		UnreconciledCount: unreconciled,
		AbnormalCount:     abnormal,
	}
	if total > 0 {
		report.Coverage.TrustedPct = float64(trusted) / float64(total) * 100
		report.Coverage.UnreconciledPct = float64(unreconciled) / float64(total) * 100
		report.Coverage.AbnormalPct = float64(abnormal) / float64(total) * 100
	}

	return report, nil
}

func loadSettlementsJSONL(path string) ([]CrossVenueSettlement, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []CrossVenueSettlement
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec CrossVenueSettlement
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, scanner.Err()
}

func loadEquitySnapshotSeries(path string) ([]equitySnapshotPoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type raw struct {
		TS       string  `json:"ts"`
		Venue    string  `json:"venue"`
		CashUSDT float64 `json:"cash_usdt"`
	}
	byTS := map[string]float64{}
	var tsOrder []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec raw
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if _, ok := byTS[rec.TS]; !ok {
			tsOrder = append(tsOrder, rec.TS)
		}
		byTS[rec.TS] += rec.CashUSDT
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(tsOrder)
	out := make([]equitySnapshotPoint, 0, len(tsOrder))
	for _, tsStr := range tsOrder {
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			ts, err = time.Parse(time.RFC3339Nano, tsStr)
			if err != nil {
				continue
			}
		}
		out = append(out, equitySnapshotPoint{ts: ts.UTC(), total: byTS[tsStr]})
	}
	return out, nil
}

func sortedDayKeys(byDay map[string][]CrossVenueSettlement, equityDays map[string]float64) []string {
	seen := map[string]struct{}{}
	for d := range byDay {
		seen[d] = struct{}{}
	}
	for d := range equityDays {
		seen[d] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// FormatLedgerReconReport 人类可读摘要(供 cmd/ledgerrecon 打印)。
func FormatLedgerReconReport(r LedgerReconReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Ledger Recon ===\n")
	fmt.Fprintf(&b, "Generated: %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Settlements: %s (%d records)\n", r.SettlementsPath, r.TotalSettlements)
	fmt.Fprintf(&b, "Equity: %s (Δ %.4f U, %.4f → %.4f)\n",
		r.EquitySnapshotsPath, r.EquityTotalDelta, r.EquityTotalStart, r.EquityTotalEnd)
	fmt.Fprintf(&b, "Σ net_pnl: %.4f U (excl abnormal: %.4f U)\n", r.TotalNetPnL, r.TotalNetPnLExclAbnormal)
	fmt.Fprintf(&b, "Tolerance: %.0f bps/day | Large |trade_pnl| threshold: %.0f U\n\n",
		r.ToleranceBps, r.LargePnLThresholdUSDT)

	fmt.Fprintf(&b, "--- Daily (UTC) ---\n")
	for _, row := range r.Daily {
		flag := "OK"
		if !row.WithinTolExclAbnorm {
			flag = "GAP"
		}
		fmt.Fprintf(&b, "%s  equity_Δ=%+.4f  settlements=%+.4f (excl_ab=%+.4f)  diff=%+.4f (excl_ab=%+.4f)  tol=±%.4f  [%s]  n=%d\n",
			row.DateUTC, row.EquityDeltaUSDT, row.SettlementsNetPnL, row.SettlementsNetExclAb,
			row.DiffUSDT, row.DiffExclAbnormalUSDT, row.ToleranceUSDT, flag, row.TradeCount)
	}

	fmt.Fprintf(&b, "\n--- Abnormal |trade_pnl| > %.0f ---\n", r.LargePnLThresholdUSDT)
	if len(r.AbnormalPnL) == 0 {
		fmt.Fprintf(&b, "(none)\n")
	} else {
		for _, ab := range r.AbnormalPnL {
			fmt.Fprintf(&b, "  %s %s %s/%s close=%s trade_pnl=%+.2f net_pnl=%+.2f notional=%.2f\n",
				ab.SettlementID, ab.Symbol, ab.HighVenue, ab.LowVenue,
				ab.CloseTime.UTC().Format(time.RFC3339), ab.TradePnL, ab.NetPnL, ab.NotionalUSDT)
		}
	}

	fmt.Fprintf(&b, "\n--- funding_truth_status ---\n")
	statuses := make([]string, 0, len(r.FundingTruthStatusCounts))
	for s := range r.FundingTruthStatusCounts {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	for _, s := range statuses {
		fmt.Fprintf(&b, "  %s: %d\n", s, r.FundingTruthStatusCounts[s])
	}

	fmt.Fprintf(&b, "\n--- Coverage ---\n")
	fmt.Fprintf(&b, "  trusted: %.1f%% (%d)  unreconciled: %.1f%% (%d)  abnormal: %.1f%% (%d)\n",
		r.Coverage.TrustedPct, r.Coverage.TrustedCount,
		r.Coverage.UnreconciledPct, r.Coverage.UnreconciledCount,
		r.Coverage.AbnormalPct, r.Coverage.AbnormalCount)
	return b.String()
}

// DefaultLedgerReconPaths 返回默认 records 路径(相对 repo 根)。
func DefaultLedgerReconPaths(recordDir string) (settlements, equity string) {
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	cv := crossVenueRecordDir(base)
	return filepath.Join(cv, crossVenueSettlementsFileName), filepath.Join(cv, equitySnapshotsFileName)
}
