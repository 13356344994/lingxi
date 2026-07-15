// Package datafitness measures published, outcome-blind execution projections.
package datafitness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"lingxi/internal/historydownload"
)

const (
	SchemaVersion     = "lingxi.p0-swap-data-fitness-receipt.v3"
	CalculatorVersion = "p0-swap-data-fitness.v3"
	ProjectionStatus  = "PROJECTION_BYTES_FROZEN_AT_MEASUREMENT"
)

var forbiddenCanonical = map[string]bool{
	"futurereturn": true, "return": true, "pnl": true, "fillpnl": true,
	"realizedpnl": true, "realizedusdt": true, "profit": true, "winner": true,
	"futureoutcome": true, "mfe": true, "mae": true, "upnlafterdecision": true,
	"outcome": true,
}

type Config struct{ OrdersRoot, FillsRoot, PublicDataRoot string }

type Receipt struct {
	SchemaVersion      string             `json:"schema_version"`
	CalculatorVersion  string             `json:"calculator_version"`
	Inputs             Inputs             `json:"inputs"`
	ProjectionEvidence ProjectionEvidence `json:"projection_evidence"`
	Counts             Counts             `json:"counts"`
	IdentityConflicts  IdentityConflicts  `json:"identity_conflicts"`
	NoOutcomes         bool               `json:"no_outcomes"`
	ReceiptHash        string             `json:"receipt_hash"`
}

type Inputs struct {
	OrdersSourceManifestHash   string `json:"orders_source_manifest_hash"`
	OrdersQueryHash            string `json:"orders_query_hash"`
	OrdersProjectionSchemaHash string `json:"orders_projection_schema_hash"`
	FillsSourceManifestHash    string `json:"fills_source_manifest_hash"`
	FillsQueryHash             string `json:"fills_query_hash"`
	FillsProjectionSchemaHash  string `json:"fills_projection_schema_hash"`
	InstrumentsCurrentHash     string `json:"instruments_current_hash"`
	InstrumentsManifestHash    string `json:"instruments_manifest_hash"`
}

type ProjectionEvidence struct {
	Status    string              `json:"status"`
	Algorithm string              `json:"algorithm"`
	Orders    ProjectionAggregate `json:"orders"`
	Fills     ProjectionAggregate `json:"fills"`
}
type ProjectionAggregate struct {
	SourceManifestHash string `json:"source_manifest_hash"`
	AggregateHash      string `json:"aggregate_hash"`
	PageCount          int    `json:"page_count"`
	TotalBytes         int64  `json:"total_bytes"`
	TotalRows          int    `json:"total_rows"`
}

type Counts struct {
	OrdersRecords                    int            `json:"orders_records"`
	OrdersDistinctTokens             int            `json:"orders_distinct_order_tokens"`
	OrdersDuplicateTokens            int            `json:"orders_duplicate_tokens"`
	OrdersClOrdIDMissing             int            `json:"orders_cl_ord_id_missing"`
	OrdersSourceNonempty             int            `json:"orders_source_nonempty"`
	FillsRecords                     int            `json:"fills_records"`
	FillsDistinctOrderTokens         int            `json:"fills_distinct_order_tokens"`
	FillsDistinctTradeTokens         int            `json:"fills_distinct_trade_tokens"`
	FillsDistinctBillTokens          int            `json:"fills_distinct_bill_tokens"`
	FillsClOrdIDMissing              int            `json:"fills_cl_ord_id_missing"`
	MatchedOrderTokens               int            `json:"matched_order_tokens"`
	UnmatchedFillOrderTokens         int            `json:"unmatched_fill_order_tokens"`
	OrdersTradedInstruments          int            `json:"orders_traded_instruments"`
	FillsTradedInstruments           int            `json:"fills_traded_instruments"`
	UnionTradedInstruments           int            `json:"union_traded_instruments"`
	OrdersSnapshotMissing            int            `json:"orders_snapshot_missing"`
	FillsSnapshotMissing             int            `json:"fills_snapshot_missing"`
	UnionSnapshotMissing             int            `json:"union_snapshot_missing"`
	OrdersSnapshotMissingInstruments []string       `json:"orders_snapshot_missing_instruments"`
	FillsSnapshotMissingInstruments  []string       `json:"fills_snapshot_missing_instruments"`
	UnionSnapshotMissingInstruments  []string       `json:"union_snapshot_missing_instruments"`
	OrderStates                      map[string]int `json:"order_states"`
	NoFillOrders                     int            `json:"no_fill_orders"`
	OrdersAccFillPositive            int            `json:"orders_acc_fill_positive"`
	CanceledAccFillPositive          int            `json:"canceled_acc_fill_positive"`
	CanceledAccFillZero              int            `json:"canceled_acc_fill_zero"`
	OrderTypes                       map[string]int `json:"order_types"`
	FillExecTypes                    map[string]int `json:"fill_exec_types"`
	FillsPerOrder                    map[string]int `json:"fills_per_order"`
	FillsSingleOrderCount            int            `json:"fills_single_order_count"`
	FillsMultiOrderCount             int            `json:"fills_multi_order_count"`
	FillsMaxPerOrder                 int            `json:"fills_max_per_order"`
	CurrentInstrumentRecords         int            `json:"current_instrument_records"`
	CurrentCTTypePresent             int            `json:"current_ct_type_present"`
	CurrentMinSzPresent              int            `json:"current_min_sz_present"`
	CurrentCTValPresent              int            `json:"current_ct_val_present"`
	UTCOrderDays                     int            `json:"utc_order_days"`
	TopTwoOrderDaySharePct           float64        `json:"top_two_order_day_share_pct"`
	OrdersEventRange                 TimeRange      `json:"orders_event_range"`
	FillsEventRange                  TimeRange      `json:"fills_event_range"`
}
type TimeRange struct{ Min, Max string }

func (r TimeRange) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Min string `json:"min"`
		Max string `json:"max"`
	}{r.Min, r.Max})
}

type IdentityConflicts struct {
	OrderCount   int          `json:"order_count"`
	FillRowCount int          `json:"fill_row_count"`
	SymbolPairs  []SymbolPair `json:"symbol_pairs"`
}
type SymbolPair struct {
	OrdersInstrument string `json:"orders_instrument"`
	OrdersInstType   string `json:"orders_inst_type"`
	FillsInstrument  string `json:"fills_instrument"`
	FillsInstType    string `json:"fills_inst_type"`
	Orders           int    `json:"orders"`
	FillRows         int    `json:"fill_rows"`
}
type order struct{ instID, instType string }
type timeBounds struct{ min, max time.Time }

// Measure has no raw-page path and no network dependency. Its projection hash
// freezes exactly what was measured; it does not prove equivalence to raw data.
func Measure(cfg Config) (Receipt, error) {
	om, err := readManifest(filepath.Join(cfg.OrdersRoot, "manifest.json"), historydownload.OrdersEndpoint)
	if err != nil {
		return Receipt{}, err
	}
	fm, err := readManifest(filepath.Join(cfg.FillsRoot, "manifest.json"), historydownload.FillsEndpoint)
	if err != nil {
		return Receipt{}, err
	}
	orderSchema, err := validateManifestSchema(om, historydownload.OrdersEndpoint)
	if err != nil {
		return Receipt{}, err
	}
	fillSchema, err := validateManifestSchema(fm, historydownload.FillsEndpoint)
	if err != nil {
		return Receipt{}, err
	}
	current, im, err := historydownload.VerifyInstrumentCurrent(cfg.PublicDataRoot)
	if err != nil {
		return Receipt{}, fmt.Errorf("datafitness: public instruments authority: %w", err)
	}
	instrumentSchema, err := validateManifestSchema(im, historydownload.PublicInstruments)
	if err != nil {
		return Receipt{}, err
	}
	receiptRaw, err := os.ReadFile(filepath.Join(cfg.PublicDataRoot, "instruments", current.Generation, "snapshot-receipt.json"))
	if err != nil {
		return Receipt{}, err
	}
	var ir historydownload.InstrumentSnapshotReceipt
	if decodeOne(receiptRaw, &ir) != nil || ir.Endpoint != historydownload.PublicInstruments ||
		ir.Projection.SchemaHash != im.ProjectionSchemaHash || !equalStrings(ir.Projection.Fields, im.ProjectionFields) {
		return Receipt{}, errors.New("datafitness: invalid public instruments receipt")
	}

	result := Receipt{
		SchemaVersion: SchemaVersion, CalculatorVersion: CalculatorVersion, NoOutcomes: true,
		Inputs: Inputs{
			OrdersSourceManifestHash: *om.ManifestHash, OrdersQueryHash: om.QueryHash, OrdersProjectionSchemaHash: om.ProjectionSchemaHash,
			FillsSourceManifestHash: *fm.ManifestHash, FillsQueryHash: fm.QueryHash, FillsProjectionSchemaHash: fm.ProjectionSchemaHash,
			InstrumentsCurrentHash: deref(current.Hash), InstrumentsManifestHash: *im.ManifestHash,
		},
		ProjectionEvidence: ProjectionEvidence{Status: ProjectionStatus, Algorithm: "sha256(concat(relative_filename+NUL+page_sha256+NUL+decimal_bytes+NUL+decimal_rows+LF))"},
		Counts:             Counts{OrderStates: map[string]int{}, OrderTypes: map[string]int{}, FillExecTypes: map[string]int{}, FillsPerOrder: map[string]int{}},
	}
	currentInst := map[string]bool{}
	for _, row := range ir.Projection.Records {
		if err := validateRow(row, instrumentSchema, nil); err != nil {
			return Receipt{}, err
		}
		if err := requirePositiveDecimals(row, "minSz", "ctVal", "lotSz", "tickSz"); err != nil {
			return Receipt{}, err
		}
		result.Counts.CurrentInstrumentRecords++
		if stringField(row, "ctType") != "" {
			result.Counts.CurrentCTTypePresent++
		}
		if stringField(row, "minSz") != "" {
			result.Counts.CurrentMinSzPresent++
		}
		if stringField(row, "ctVal") != "" {
			result.Counts.CurrentCTValPresent++
		}
		if id := stringField(row, "instId"); id != "" {
			currentInst[id] = true
		}
	}

	orders := map[string]order{}
	orderInst, fillInst, unionInst := map[string]bool{}, map[string]bool{}, map[string]bool{}
	orderDays := map[string]int{}
	var orderTimes timeBounds
	result.ProjectionEvidence.Orders, err = streamPages(filepath.Join(cfg.OrdersRoot, "projection"), len(om.PageRawHashes), func(row map[string]any) error {
		if err := validateRow(row, orderSchema, map[string]bool{"clOrdIdToken": true}); err != nil {
			return err
		}
		token, instID, instType := stringField(row, "ordIdToken"), stringField(row, "instId"), stringField(row, "instType")
		if token == "" || instID == "" || instType == "" {
			return errors.New("datafitness: order projection identity missing")
		}
		result.Counts.OrdersRecords++
		if _, exists := orders[token]; exists {
			result.Counts.OrdersDuplicateTokens++
			return errors.New("datafitness: duplicate order token")
		}
		orders[token] = order{instID, instType}
		orderInst[instID], unionInst[instID] = true, true
		state := stringField(row, "state")
		result.Counts.OrderStates[state]++
		result.Counts.OrderTypes[stringField(row, "ordType")]++
		if stringField(row, "clOrdIdToken") == "" {
			result.Counts.OrdersClOrdIDMissing++
		}
		if stringField(row, "source") != "" {
			result.Counts.OrdersSourceNonempty++
		}
		accFill, err := decimal(row, "accFillSz", false)
		if err != nil {
			return err
		}
		if _, err := decimal(row, "sz", true); err != nil {
			return err
		}
		px := stringField(row, "px")
		if px == "" {
			if stringField(row, "ordType") != "market" {
				return errors.New("datafitness: px missing outside market order")
			}
		} else if _, err := parseDecimal("px", px, stringField(row, "ordType") != "market"); err != nil {
			return err
		}
		if accFill.Sign() > 0 {
			result.Counts.OrdersAccFillPositive++
			if state == "canceled" {
				result.Counts.CanceledAccFillPositive++
			}
		} else {
			result.Counts.NoFillOrders++
			if state == "canceled" {
				result.Counts.CanceledAccFillZero++
			}
		}
		c, err := millis(row["cTime"])
		if err != nil {
			return fmt.Errorf("datafitness: cTime: %w", err)
		}
		u, err := millis(row["uTime"])
		if err != nil {
			return fmt.Errorf("datafitness: uTime: %w", err)
		}
		orderTimes.add(c)
		orderTimes.add(u)
		orderDays[c.Format("2006-01-02")]++
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	if result.Counts.OrdersRecords != om.RecordCount {
		return Receipt{}, errors.New("datafitness: orders row count differs from manifest")
	}
	result.ProjectionEvidence.Orders.SourceManifestHash = *om.ManifestHash
	result.Counts.OrdersDistinctTokens = len(orders)

	fillsByOrder, trades, bills := map[string]int{}, map[string]bool{}, map[string]bool{}
	pairs, conflictTokens := map[string]*SymbolPair{}, map[string]map[string]bool{}
	var fillTimes timeBounds
	result.ProjectionEvidence.Fills, err = streamPages(filepath.Join(cfg.FillsRoot, "projection"), len(fm.PageRawHashes), func(row map[string]any) error {
		if err := validateRow(row, fillSchema, map[string]bool{"clOrdIdToken": true}); err != nil {
			return err
		}
		if err := requirePositiveDecimals(row, "fillPx", "fillSz"); err != nil {
			return err
		}
		token, instID, instType := stringField(row, "ordIdToken"), stringField(row, "instId"), stringField(row, "instType")
		trade, bill := stringField(row, "tradeIdToken"), stringField(row, "billIdToken")
		if token == "" || instID == "" || instType == "" || trade == "" || bill == "" {
			return errors.New("datafitness: fill projection required identity missing")
		}
		if trades[trade] {
			return errors.New("datafitness: duplicate fill trade token")
		}
		trades[trade] = true
		if bills[bill] {
			return errors.New("datafitness: duplicate fill bill token")
		}
		bills[bill] = true
		result.Counts.FillsRecords++
		fillsByOrder[token]++
		fillInst[instID], unionInst[instID] = true, true
		if stringField(row, "clOrdIdToken") == "" {
			result.Counts.FillsClOrdIDMissing++
		}
		result.Counts.FillExecTypes[stringField(row, "execType")]++
		ft, err := millis(row["fillTime"])
		if err != nil {
			return fmt.Errorf("datafitness: fillTime: %w", err)
		}
		ts, err := millis(row["ts"])
		if err != nil {
			return fmt.Errorf("datafitness: ts: %w", err)
		}
		fillTimes.add(ft)
		fillTimes.add(ts)
		if got, ok := orders[token]; ok && (got.instID != instID || got.instType != instType) {
			key := got.instID + "\x00" + got.instType + "\x00" + instID + "\x00" + instType
			if pairs[key] == nil {
				pairs[key] = &SymbolPair{OrdersInstrument: got.instID, OrdersInstType: got.instType, FillsInstrument: instID, FillsInstType: instType}
				conflictTokens[key] = map[string]bool{}
			}
			pairs[key].FillRows++
			conflictTokens[key][token] = true
		}
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	if result.Counts.FillsRecords != fm.RecordCount {
		return Receipt{}, errors.New("datafitness: fills row count differs from manifest")
	}
	result.ProjectionEvidence.Fills.SourceManifestHash = *fm.ManifestHash
	for token, n := range fillsByOrder {
		if _, ok := orders[token]; ok {
			result.Counts.MatchedOrderTokens++
		} else {
			result.Counts.UnmatchedFillOrderTokens++
		}
		result.Counts.FillsPerOrder[strconv.Itoa(n)]++
		if n == 1 {
			result.Counts.FillsSingleOrderCount++
		} else {
			result.Counts.FillsMultiOrderCount++
		}
		if n > result.Counts.FillsMaxPerOrder {
			result.Counts.FillsMaxPerOrder = n
		}
	}
	for key, pair := range pairs {
		pair.Orders = len(conflictTokens[key])
		result.IdentityConflicts.OrderCount += pair.Orders
		result.IdentityConflicts.FillRowCount += pair.FillRows
		result.IdentityConflicts.SymbolPairs = append(result.IdentityConflicts.SymbolPairs, *pair)
	}
	sort.Slice(result.IdentityConflicts.SymbolPairs, func(i, j int) bool {
		a, b := result.IdentityConflicts.SymbolPairs[i], result.IdentityConflicts.SymbolPairs[j]
		return a.OrdersInstrument+a.OrdersInstType+a.FillsInstrument+a.FillsInstType < b.OrdersInstrument+b.OrdersInstType+b.FillsInstrument+b.FillsInstType
	})
	result.Counts.FillsDistinctOrderTokens, result.Counts.FillsDistinctTradeTokens, result.Counts.FillsDistinctBillTokens = len(fillsByOrder), len(trades), len(bills)
	result.Counts.OrdersTradedInstruments, result.Counts.FillsTradedInstruments, result.Counts.UnionTradedInstruments = len(orderInst), len(fillInst), len(unionInst)
	result.Counts.OrdersSnapshotMissingInstruments = missingSet(orderInst, currentInst)
	result.Counts.FillsSnapshotMissingInstruments = missingSet(fillInst, currentInst)
	result.Counts.UnionSnapshotMissingInstruments = missingSet(unionInst, currentInst)
	result.Counts.OrdersSnapshotMissing, result.Counts.FillsSnapshotMissing, result.Counts.UnionSnapshotMissing = len(result.Counts.OrdersSnapshotMissingInstruments), len(result.Counts.FillsSnapshotMissingInstruments), len(result.Counts.UnionSnapshotMissingInstruments)
	result.Counts.UTCOrderDays, result.Counts.TopTwoOrderDaySharePct = len(orderDays), topTwoShare(orderDays, result.Counts.OrdersRecords)
	result.Counts.OrdersEventRange, result.Counts.FillsEventRange = orderTimes.format(), fillTimes.format()
	hash, err := ReceiptFingerprint(result)
	if err != nil {
		return Receipt{}, err
	}
	result.ReceiptHash = hash
	return result, nil
}

// ReceiptFingerprint recomputes the self-excluding canonical receipt hash.
func ReceiptFingerprint(r Receipt) (string, error) {
	r.ReceiptHash = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func readManifest(path, endpoint string) (historydownload.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return historydownload.Manifest{}, err
	}
	var m historydownload.Manifest
	if decodeOne(raw, &m) != nil || historydownload.VerifyManifest(m) != nil || m.Endpoint != endpoint || !m.Private || m.ManifestHash == nil {
		return historydownload.Manifest{}, errors.New("datafitness: invalid private projection manifest")
	}
	return m, nil
}
func streamPages(dir string, want int, fn func(map[string]any) error) (ProjectionAggregate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ProjectionAggregate{}, err
	}
	pages := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			pages = append(pages, e.Name())
		}
	}
	sort.Strings(pages)
	if len(pages) != want {
		return ProjectionAggregate{}, fmt.Errorf("datafitness: projection page count %d != manifest %d", len(pages), want)
	}
	h := sha256.New()
	result := ProjectionAggregate{PageCount: len(pages)}
	for i, name := range pages {
		expected := fmt.Sprintf("page-%06d.json", i+1)
		if name != expected {
			return ProjectionAggregate{}, fmt.Errorf("datafitness: non-contiguous projection page %q", name)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ProjectionAggregate{}, err
		}
		if len(raw) > 16<<20 {
			return ProjectionAggregate{}, errors.New("datafitness: projection page exceeds bound")
		}
		var rows []map[string]any
		if err := decodeOne(raw, &rows); err != nil {
			return ProjectionAggregate{}, fmt.Errorf("datafitness: projection page %s: %w", name, err)
		}
		pageSum := sha256.Sum256(raw)
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\n", name, hex.EncodeToString(pageSum[:]), len(raw), len(rows))
		result.TotalBytes += int64(len(raw))
		result.TotalRows += len(rows)
		for _, row := range rows {
			if err := fn(row); err != nil {
				return ProjectionAggregate{}, err
			}
		}
	}
	result.AggregateHash = hex.EncodeToString(h.Sum(nil))
	return result, nil
}
func decodeOne(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("trailing JSON value")
	}
	if dec.InputOffset() < int64(len(raw)) && len(bytes.TrimSpace(raw[dec.InputOffset():])) != 0 {
		return errors.New("trailing bytes")
	}
	return nil
}
func validateManifestSchema(m historydownload.Manifest, endpoint string) (map[string]bool, error) {
	expectedFields, expectedHash, err := historydownload.ProjectionSchema(endpoint)
	if err != nil {
		return nil, err
	}
	if m.ProjectionSchemaHash != expectedHash || !equalStrings(m.ProjectionFields, expectedFields) {
		return nil, errors.New("datafitness: manifest projection schema differs from calculator")
	}
	allowed := make(map[string]bool, len(expectedFields))
	for _, field := range expectedFields {
		if forbiddenKey(field) {
			return nil, fmt.Errorf("datafitness: forbidden projection schema field %q", field)
		}
		allowed[field] = true
	}
	return allowed, nil
}
func validateRow(row map[string]any, allowed, optional map[string]bool) error {
	if err := scanOutcomeKeys(row); err != nil {
		return err
	}
	for key, value := range row {
		if !allowed[key] {
			return fmt.Errorf("datafitness: projection field %q outside endpoint schema", key)
		}
		if _, ok := value.(string); !ok {
			return fmt.Errorf("datafitness: projection field %q is not a scalar string", key)
		}
	}
	for key := range allowed {
		if _, ok := row[key]; !ok && !optional[key] {
			return fmt.Errorf("datafitness: required projection field %q missing", key)
		}
	}
	return nil
}
func scanOutcomeKeys(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenKey(key) {
				return fmt.Errorf("datafitness: outcome field %q in projection", key)
			}
			if err := scanOutcomeKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := scanOutcomeKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}
func forbiddenKey(key string) bool {
	var b strings.Builder
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return forbiddenCanonical[b.String()]
}
func stringField(row map[string]any, key string) string { value, _ := row[key].(string); return value }
func decimal(row map[string]any, field string, positive bool) (*big.Rat, error) {
	value, ok := row[field].(string)
	if !ok || value == "" {
		return nil, fmt.Errorf("datafitness: required decimal %s missing", field)
	}
	return parseDecimal(field, value, positive)
}
func requirePositiveDecimals(row map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, err := decimal(row, field, true); err != nil {
			return err
		}
	}
	return nil
}
func parseDecimal(field, value string, positive bool) (*big.Rat, error) {
	if !decimalSyntax(value) {
		return nil, fmt.Errorf("datafitness: %s is not a strict decimal", field)
	}
	number, ok := new(big.Rat).SetString(value)
	if !ok || number.Sign() < 0 || positive && number.Sign() == 0 {
		return nil, fmt.Errorf("datafitness: %s has invalid sign", field)
	}
	return number, nil
}
func decimalSyntax(value string) bool {
	if value == "" {
		return false
	}
	start := 0
	if value[0] == '-' {
		start = 1
		if len(value) == 1 {
			return false
		}
	}
	digits, dot := 0, false
	for i := start; i < len(value); i++ {
		if value[i] == '.' && !dot {
			dot = true
			continue
		}
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		digits++
	}
	return digits > 0 && value[len(value)-1] != '.'
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func millis(v any) (time.Time, error) {
	text, ok := v.(string)
	if !ok || text == "" {
		return time.Time{}, errors.New("missing decimal milliseconds")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, errors.New("invalid positive decimal milliseconds")
	}
	return time.UnixMilli(n).UTC(), nil
}
func (b *timeBounds) add(t time.Time) {
	if b.min.IsZero() || t.Before(b.min) {
		b.min = t
	}
	if b.max.IsZero() || t.After(b.max) {
		b.max = t
	}
}
func (b timeBounds) format() TimeRange {
	if b.min.IsZero() {
		return TimeRange{}
	}
	return TimeRange{b.min.Format(time.RFC3339Nano), b.max.Format(time.RFC3339Nano)}
}
func missingSet(a, b map[string]bool) []string {
	out := []string{}
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
func topTwoShare(days map[string]int, total int) float64 {
	if total == 0 {
		return 0
	}
	values := []int{}
	for _, n := range days {
		values = append(values, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(values)))
	sum := 0
	for i := 0; i < len(values) && i < 2; i++ {
		sum += values[i]
	}
	return float64(sum) * 100 / float64(total)
}
func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
