package fingerprint

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/replay"
)

type streamStats struct {
	Lines     int
	Decoded   int
	Malformed int
	Skipped   int
	Truncated bool
}

type ingestContext struct {
	store        *store
	catalog      datatruth.InstrumentCatalog
	hasCatalog   bool
	defaultVenue string
}

type settlementRow struct {
	Time          time.Time `json:"time"`
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	NotionalUSDT  float64   `json:"notional"`
	FeeUSDT       *float64  `json:"fee_usdt"`
	EntrySlipBps  *float64  `json:"entry_slip_bps"`
	ExitSlipBps   *float64  `json:"exit_slip_bps"`
	OpenViaTaker  bool      `json:"open_via_taker"`
	MakerEntry    bool      `json:"maker_entry"`
	FundingAtOpen float64   `json:"funding_at_open"`
}

type openDecisionRow struct {
	RecordedAt     time.Time `json:"recorded_at"`
	Venue          string    `json:"venue"`
	Symbol         string    `json:"symbol"`
	Side           string    `json:"side"`
	DecisionResult string    `json:"decision_result"`
	FundingRate    float64   `json:"funding_rate"`
	OpenViaTaker   bool      `json:"open_via_taker"`
	MakerRejected  bool      `json:"maker_rejected"`
}

type shadowRow struct {
	RecordedAt        time.Time `json:"recorded_at"`
	Venue             string    `json:"venue"`
	Symbol            string    `json:"symbol"`
	FastCandidateSide string    `json:"fast_candidate_side"`
	EvalRestMs        int64     `json:"eval_rest_ms"`
	SpreadBps         float64   `json:"spread_bps"`
}

type equityRow struct {
	TS       time.Time `json:"ts"`
	Venue    string    `json:"venue"`
	CashUSDT float64   `json:"cash_usdt"`
}

func streamJSONL(path string, maxLines, skipLines int, handle func([]byte) error) (streamStats, error) {
	var stats streamStats
	f, err := os.Open(path)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		stats.Lines++
		if stats.Lines <= skipLines {
			stats.Skipped++
			continue
		}
		if maxLines > 0 && stats.Decoded >= maxLines {
			stats.Truncated = true
			return stats, nil
		}
		if err := handle(line); err != nil {
			if err == errStopStream {
				stats.Truncated = true
				return stats, nil
			}
			stats.Malformed++
			continue
		}
		stats.Decoded++
	}
	return stats, sc.Err()
}

var errStopStream = io.EOF

func ingestSettlements(path string, maxLines, skipLines int, ctx ingestContext) (streamStats, error) {
	return streamJSONL(path, maxLines, skipLines, func(line []byte) error {
		var row settlementRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		venue := ctx.defaultVenue
		key := CellKey{
			Venue:          venue,
			Symbol:         strings.TrimSpace(row.Symbol),
			HourUTC:        hourUTC(row.Time),
			Direction:      normalizeDirection(row.Side),
			OrderType:      orderTypeFromFlags(row.OpenViaTaker, row.MakerEntry),
			NotionalBucket: notionalBucket(row.NotionalUSDT),
		}
		acc := ctx.store.cell(key)
		acc.samples++
		if row.FeeUSDT != nil && *row.FeeUSDT >= 0 && row.NotionalUSDT > 0 {
			acc.feeBPS.add(feeBPS(*row.FeeUSDT, row.NotionalUSDT))
		}
		if row.EntrySlipBps != nil {
			acc.entrySlip.add(*row.EntrySlipBps)
		}
		if row.ExitSlipBps != nil {
			acc.exitSlip.add(*row.ExitSlipBps)
		}
		acc.funding.add(row.FundingAtOpen)
		if ctx.hasCatalog {
			attachInstrument(acc, instrumentForSymbol(ctx.catalog, key.Symbol, venue, row.Time, 0))
		}
		return nil
	})
}

func ingestMakerAttempts(path string, maxLines, skipLines int, ctx ingestContext) (streamStats, error) {
	return streamJSONL(path, maxLines, skipLines, func(line []byte) error {
		var row replay.MakerAttemptRecord
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		venue := strings.TrimSpace(row.Venue)
		if venue == "" {
			venue = ctx.defaultVenue
		}
		key := CellKey{
			Venue:          venue,
			Symbol:         strings.TrimSpace(row.Symbol),
			HourUTC:        hourUTC(row.RecordedAt),
			Direction:      normalizeDirection(row.Side),
			OrderType:      orderTypeFromMakerAttempt(row.MakerFilled),
			NotionalBucket: "unknown",
		}
		acc := ctx.store.cell(key)
		acc.samples++
		acc.makerFill.add(row.MakerFilled)
		acc.cancelRisk.add(!row.MakerFilled)
		if row.MakerWaitMs > 0 {
			acc.makerWait.add(float64(row.MakerWaitMs))
			acc.latencyMS.add(float64(row.MakerWaitMs))
		}
		if ctx.hasCatalog {
			attachInstrument(acc, instrumentForSymbol(ctx.catalog, key.Symbol, venue, row.RecordedAt, row.FillMid))
		}
		return nil
	})
}

func ingestOpenDecisions(path string, maxLines, skipLines int, ctx ingestContext) (streamStats, error) {
	return streamJSONL(path, maxLines, skipLines, func(line []byte) error {
		var row openDecisionRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		venue := strings.TrimSpace(row.Venue)
		if venue == "" {
			venue = ctx.defaultVenue
		}
		key := CellKey{
			Venue:          venue,
			Symbol:         strings.TrimSpace(row.Symbol),
			HourUTC:        hourUTC(row.RecordedAt),
			Direction:      normalizeDirection(row.Side),
			OrderType:      orderTypeFromDecision(row.OpenViaTaker, row.DecisionResult),
			NotionalBucket: "unknown",
		}
		acc := ctx.store.cell(key)
		acc.samples++
		acc.funding.add(row.FundingRate)
		if strings.EqualFold(row.DecisionResult, "maker_not_filled") || row.MakerRejected {
			acc.cancelRisk.add(true)
		} else if strings.EqualFold(row.DecisionResult, "opened") ||
			strings.EqualFold(row.DecisionResult, "taker_filled") {
			acc.cancelRisk.add(false)
		}
		if ctx.hasCatalog {
			attachInstrument(acc, instrumentForSymbol(ctx.catalog, key.Symbol, venue, row.RecordedAt, 0))
		}
		return nil
	})
}

func ingestExecutableShadow(path string, maxLines, skipLines int, ctx ingestContext) (streamStats, error) {
	return streamJSONL(path, maxLines, skipLines, func(line []byte) error {
		var row shadowRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		venue := strings.TrimSpace(row.Venue)
		if venue == "" {
			venue = ctx.defaultVenue
		}
		side := row.FastCandidateSide
		if strings.EqualFold(side, "flat") {
			side = "unknown"
		}
		key := CellKey{
			Venue:          venue,
			Symbol:         strings.TrimSpace(row.Symbol),
			HourUTC:        hourUTC(row.RecordedAt),
			Direction:      normalizeDirection(side),
			OrderType:      "unknown",
			NotionalBucket: "unknown",
		}
		acc := ctx.store.cell(key)
		acc.samples++
		if row.EvalRestMs > 0 {
			acc.latencyMS.add(float64(row.EvalRestMs))
		}
		if ctx.hasCatalog {
			attachInstrument(acc, instrumentForSymbol(ctx.catalog, key.Symbol, venue, row.RecordedAt, 0))
		}
		return nil
	})
}

func ingestEquitySnapshots(path string, maxLines, skipLines int) (EquitySummary, streamStats, error) {
	var cash []float64
	venues := map[string]struct{}{}
	stats, err := streamJSONL(path, maxLines, skipLines, func(line []byte) error {
		var row equityRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		if row.CashUSDT > 0 {
			cash = append(cash, row.CashUSDT)
		}
		if strings.TrimSpace(row.Venue) != "" {
			venues[row.Venue] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return EquitySummary{Status: StatusUnknown}, stats, err
	}
	if len(cash) == 0 {
		return EquitySummary{Status: StatusUnknown}, stats, nil
	}
	q := finalizeQuantile(cash)
	return EquitySummary{
		MedianCashUSDT: q.P50,
		P90CashUSDT:    q.P90,
		Venues:         len(venues),
		Status:         StatusAvailable,
	}, stats, nil
}

func defaultVenueFromRoot(root string) string {
	lower := strings.ToLower(root)
	switch {
	case strings.Contains(lower, "bybit"):
		return "bybit"
	case strings.Contains(lower, "binance"):
		return "binance"
	case strings.Contains(lower, "gate"):
		return "gate"
	default:
		return "okx"
	}
}
