// Package fundingarchive downloads and verifies the official OKX daily
// all-swap funding-rate archive without touching labels or outcomes.
package fundingarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

const (
	DiscoveryURL = "https://www.okx.com/api/v5/public/market-data-history"
	SourceID     = "okx-official-allswap-fundingrates-v3"
	DailyCap     = 1 << 20
	TotalCap     = 100 << 20
	CSVHeader    = "instrument_name,funding_rate,funding_time"
)

type Config struct {
	Root       string
	Start      time.Time
	End        time.Time
	RateLimit  time.Duration
	HTTPClient *http.Client
	Now        func() time.Time
	Progress   io.Writer
	MaxDays    int
}

type Plan struct {
	SchemaVersion string   `json:"schema_version"`
	SourceID      string   `json:"source_id"`
	Module        string   `json:"module"`
	InstType      string   `json:"inst_type"`
	DateAggrType  string   `json:"date_aggr_type"`
	Dates         []string `json:"dates"`
	QueryHash     string   `json:"source_query_hash"`
	PlanHash      string   `json:"plan_hash"`
}

type DailyResult struct {
	Date          string `json:"date"`
	Status        string `json:"status"`
	Filename      string `json:"filename,omitempty"`
	SourceURLHash string `json:"source_url_hash,omitempty"`
	RawHash       string `json:"daily_raw_hash,omitempty"`
	OutputHash    string `json:"output_hash,omitempty"`
	Rows          int    `json:"rows,omitempty"`
	UnknownRows   int    `json:"unknown_signed_rate_rows,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	GapReason     string `json:"gap_reason,omitempty"`
}

type Checkpoint struct {
	SchemaVersion string                 `json:"schema_version"`
	PlanHash      string                 `json:"plan_hash"`
	Completed     map[string]DailyResult `json:"completed"`
}

type Manifest struct {
	SchemaVersion string        `json:"schema_version"`
	LabelsRead    bool          `json:"labels_or_outcomes_read"`
	PlanHash      string        `json:"plan_hash"`
	SourceID      string        `json:"source_id"`
	Header        string        `json:"csv_header"`
	DateCount     int           `json:"date_count"`
	CompleteDays  int           `json:"complete_days"`
	GapDays       int           `json:"gap_days"`
	Rows          int           `json:"rows"`
	Bytes         int64         `json:"downloaded_bytes"`
	Days          []DailyResult `json:"days"`
	ManifestHash  string        `json:"manifest_hash"`
}

// Provenance is the immutable identity of the verified funding source.
// ReceiptHash is the canonical hash of receipt.json, not a hash inferred from
// a caller supplied summary.
type Provenance struct {
	ManifestHash string          `json:"manifest_hash"`
	ReceiptHash  string          `json:"receipt_hash"`
	CompleteDays map[string]bool `json:"-"`
	StartDate    string          `json:"start_date"`
	EndDate      string          `json:"end_date"`
	GapDays      int             `json:"gap_days"`
}

type Event struct {
	EventID       string `json:"event_id"`
	InstID        string `json:"inst_id"`
	FundingRate   string `json:"funding_rate"`
	FundingTime   int64  `json:"funding_time"`
	EventTime     string `json:"event_time"`
	ReceiveTime   string `json:"receive_time"`
	ProcessTime   string `json:"process_time"`
	SourceID      string `json:"source_id"`
	SourceURLHash string `json:"source_url_hash"`
	DailyRawHash  string `json:"daily_raw_hash"`
}

type discoveryFile struct {
	Filename string
	URL      string
}

func BuildPlan(start, end time.Time) (Plan, error) {
	start, end = day(start), day(end)
	if start.IsZero() || end.Before(start) {
		return Plan{}, errors.New("invalid date range")
	}
	var dates []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format("2006-01-02"))
	}
	queryHash, _, err := datatruth.HashJSON(map[string]any{
		"endpoint": DiscoveryURL, "module": "3", "instType": "SWAP",
		"instIdList": "BTC-USDT-SWAP", "dateAggrType": "daily", "dates": dates,
	})
	if err != nil {
		return Plan{}, err
	}
	p := Plan{SchemaVersion: "lingxi.okx-funding-archive-plan.v1", SourceID: SourceID, Module: "3", InstType: "SWAP", DateAggrType: "daily", Dates: dates, QueryHash: queryHash}
	copy := p
	copy.PlanHash = ""
	p.PlanHash, _, err = datatruth.HashJSON(copy)
	return p, err
}

func Run(ctx context.Context, cfg Config) (Manifest, error) {
	if cfg.Root == "" {
		return Manifest{}, errors.New("root required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 100 * time.Millisecond
	}
	plan, err := BuildPlan(cfg.Start, cfg.End)
	if err != nil {
		return Manifest{}, err
	}
	outDir := filepath.Join(cfg.Root, "records/history/okx_funding_v3")
	if err := os.MkdirAll(filepath.Join(outDir, "daily"), 0o755); err != nil {
		return Manifest{}, err
	}
	if err := freezePlan(filepath.Join(outDir, "source_plan.json"), plan); err != nil {
		return Manifest{}, err
	}
	symbols, err := catalogSymbols(filepath.Join(cfg.Root, "research/firstbattle/history_catalog.json"))
	if err != nil {
		return Manifest{}, err
	}
	cpPath := filepath.Join(outDir, "checkpoint.json")
	cp, err := loadCheckpoint(cpPath, plan.PlanHash)
	if err != nil {
		return Manifest{}, err
	}
	client := safeClient(cfg.HTTPClient)
	var downloaded int64
	runDates := plan.Dates
	if cfg.MaxDays > 0 && cfg.MaxDays < len(runDates) {
		runDates = runDates[:cfg.MaxDays]
	}
	for index, dateText := range runDates {
		if previous, ok := cp.Completed[dateText]; ok {
			if previous.Status == "COMPLETE" {
				path := filepath.Join(outDir, "daily", dateText+".jsonl")
				hash, err := fileHash(path)
				if err != nil || hash != previous.OutputHash {
					return Manifest{}, fmt.Errorf("checkpoint tamper %s", dateText)
				}
			}
			continue
		}
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "funding archive %d/%d %s\n", index+1, len(runDates), dateText)
		}
		d, _ := time.Parse("2006-01-02", dateText)
		found, receiveTime, err := discover(ctx, client, d)
		if err != nil {
			return Manifest{}, fmt.Errorf("discover %s: %w", dateText, err)
		}
		if found == nil {
			cp.Completed[dateText] = DailyResult{Date: dateText, Status: "GAP", GapReason: "OFFICIAL_PACKAGE_NOT_LISTED"}
		} else {
			raw, processTime, err := download(ctx, client, found.URL)
			if err != nil {
				var statusErr *httpStatusError
				if errors.As(err, &statusErr) && statusErr.Status == http.StatusNotFound {
					cp.Completed[dateText] = DailyResult{Date: dateText, Status: "GAP", Filename: found.Filename, GapReason: "OFFICIAL_PACKAGE_404"}
				} else {
					return Manifest{}, fmt.Errorf("download %s: %w", dateText, err)
				}
			} else {
				downloaded += int64(len(raw))
				if downloaded > TotalCap {
					return Manifest{}, errors.New("total download cap exceeded")
				}
				result, body, err := verifyDay(dateText, found, raw, receiveTime, processTime, symbols)
				if err != nil {
					return Manifest{}, fmt.Errorf("verify %s: %w", dateText, err)
				}
				path := filepath.Join(outDir, "daily", dateText+".jsonl")
				if err := writeAtomic(path, body, 0o644); err != nil {
					return Manifest{}, err
				}
				cp.Completed[dateText] = result
			}
		}
		if err := writeAtomicJSON(cpPath, cp); err != nil {
			return Manifest{}, err
		}
		if index+1 < len(runDates) {
			select {
			case <-ctx.Done():
				return Manifest{}, ctx.Err()
			case <-time.After(cfg.RateLimit):
			}
		}
	}
	if len(runDates) < len(plan.Dates) {
		smoke := Manifest{SchemaVersion: "lingxi.okx-funding-archive-smoke.v1", LabelsRead: false, PlanHash: plan.PlanHash, SourceID: SourceID, Header: CSVHeader, DateCount: len(runDates)}
		for _, date := range runDates {
			result := cp.Completed[date]
			smoke.Days = append(smoke.Days, result)
			if result.Status != "COMPLETE" {
				return Manifest{}, fmt.Errorf("smoke day %s did not produce an official package: %s", date, result.GapReason)
			}
			smoke.CompleteDays++
			smoke.Rows += result.Rows
			smoke.Bytes += result.Bytes
		}
		copy := smoke
		copy.ManifestHash = ""
		smoke.ManifestHash, _, err = datatruth.HashJSON(copy)
		if err != nil {
			return Manifest{}, err
		}
		if err := writeAtomicJSON(filepath.Join(outDir, "smoke_receipt.json"), smoke); err != nil {
			return Manifest{}, err
		}
		return smoke, nil
	}
	manifest, err := buildManifest(plan, cp)
	if err != nil {
		return Manifest{}, err
	}
	if err := writeAtomicJSON(filepath.Join(outDir, "manifest.json"), manifest); err != nil {
		return Manifest{}, err
	}
	receipt := map[string]any{"schema_version": "lingxi.okx-funding-archive-receipt.v1", "labels_or_outcomes_read": false, "plan_hash": plan.PlanHash, "manifest_hash": manifest.ManifestHash, "complete_days": manifest.CompleteDays, "gap_days": manifest.GapDays, "rows": manifest.Rows}
	if err := writeAtomicJSON(filepath.Join(outDir, "receipt.json"), receipt); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// LoadVerified revalidates the atomic manifest and every complete daily
// partition before exposing signed rates to the outcome-blind preflight.
func LoadVerified(root string) (map[string]map[int64]string, error) {
	rates, _, err := LoadVerifiedSource(root)
	return rates, err
}

// LoadVerifiedSource validates the manifest, its independent receipt, every
// complete daily shard, and returns only official signed funding events.
func LoadVerifiedSource(root string) (map[string]map[int64]string, Provenance, error) {
	base := filepath.Join(root, "records/history/okx_funding_v3")
	raw, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if os.IsNotExist(err) {
		return map[string]map[int64]string{}, Provenance{}, nil
	}
	if err != nil {
		return nil, Provenance{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, Provenance{}, err
	}
	declared := manifest.ManifestHash
	manifest.ManifestHash = ""
	computed, _, err := datatruth.HashJSON(manifest)
	if err != nil || declared == "" || computed != declared {
		return nil, Provenance{}, errors.New("funding v3 manifest hash mismatch")
	}
	receiptRaw, err := os.ReadFile(filepath.Join(base, "receipt.json"))
	if err != nil {
		return nil, Provenance{}, fmt.Errorf("funding v3 receipt: %w", err)
	}
	var receipt struct {
		SchemaVersion string `json:"schema_version"`
		ManifestHash  string `json:"manifest_hash"`
		PlanHash      string `json:"plan_hash"`
		CompleteDays  int    `json:"complete_days"`
		GapDays       int    `json:"gap_days"`
		Rows          int    `json:"rows"`
	}
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		return nil, Provenance{}, err
	}
	receiptHash := hashBytes(receiptRaw)
	if receipt.SchemaVersion != "lingxi.okx-funding-archive-receipt.v1" ||
		receipt.ManifestHash != declared || receipt.PlanHash != manifest.PlanHash ||
		receipt.CompleteDays != manifest.CompleteDays || receipt.GapDays != manifest.GapDays || receipt.Rows != manifest.Rows {
		return nil, Provenance{}, errors.New("funding v3 receipt does not bind manifest")
	}
	out := map[string]map[int64]string{}
	completeDays := make(map[string]bool, manifest.CompleteDays)
	for _, result := range manifest.Days {
		if result.Status != "COMPLETE" {
			continue
		}
		completeDays[result.Date] = true
		path := filepath.Join(base, "daily", result.Date+".jsonl")
		hash, err := fileHash(path)
		if err != nil || hash != result.OutputHash {
			return nil, Provenance{}, fmt.Errorf("funding v3 daily hash mismatch %s", result.Date)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, Provenance{}, err
		}
		decoder := json.NewDecoder(f)
		for {
			var event Event
			if err := decoder.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				f.Close()
				return nil, Provenance{}, err
			}
			if event.SourceID != SourceID || event.EventID == "" || event.SourceURLHash == "" || event.DailyRawHash != result.RawHash {
				f.Close()
				return nil, Provenance{}, fmt.Errorf("funding v3 provenance invalid %s", result.Date)
			}
			byTime := out[event.InstID]
			if byTime == nil {
				byTime = map[int64]string{}
				out[event.InstID] = byTime
			}
			if prior, ok := byTime[event.FundingTime]; ok && prior != event.FundingRate {
				f.Close()
				return nil, Provenance{}, fmt.Errorf("funding v3 conflict %s/%d", event.InstID, event.FundingTime)
			}
			byTime[event.FundingTime] = event.FundingRate
		}
		if err := f.Close(); err != nil {
			return nil, Provenance{}, err
		}
	}
	startDate, endDate := "", ""
	if len(manifest.Days) > 0 {
		startDate = manifest.Days[0].Date
		endDate = manifest.Days[len(manifest.Days)-1].Date
	}
	return out, Provenance{
		ManifestHash: declared,
		ReceiptHash:  receiptHash,
		CompleteDays: completeDays,
		StartDate:    startDate,
		EndDate:      endDate,
		GapDays:      manifest.GapDays,
	}, nil
}

func discoveryQuery(d time.Time) url.Values {
	begin := day(d).UnixMilli()
	q := url.Values{}
	q.Set("module", "3")
	q.Set("instType", "SWAP")
	q.Set("instIdList", "BTC-USDT-SWAP")
	q.Set("dateAggrType", "daily")
	q.Set("begin", strconv.FormatInt(begin, 10))
	q.Set("end", strconv.FormatInt(begin+int64(24*time.Hour/time.Millisecond)-1, 10))
	return q
}

func discover(ctx context.Context, client *http.Client, d time.Time) (*discoveryFile, time.Time, error) {
	u, _ := url.Parse(DiscoveryURL)
	u.RawQuery = discoveryQuery(d).Encode()
	body, received, status, err := get(ctx, client, u, DailyCap)
	if err != nil {
		return nil, time.Time{}, err
	}
	if status != http.StatusOK {
		return nil, time.Time{}, &httpStatusError{Status: status}
	}
	file, err := ParseDiscovery(body, d)
	return file, received, err
}

func ParseDiscovery(body []byte, requested time.Time) (*discoveryFile, error) {
	var envelope struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				GroupDetails []struct {
					Filename string `json:"filename"`
					URL      string `json:"url"`
				} `json:"groupDetails"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != "0" {
		return nil, fmt.Errorf("OKX code %q", envelope.Code)
	}
	expected := "allswap-fundingrates-" + day(requested).Format("2006-01-02") + ".zip"
	var matches []discoveryFile
	for _, data := range envelope.Data {
		for _, detail := range data.Details {
			for _, group := range detail.GroupDetails {
				if group.Filename == expected {
					matches = append(matches, discoveryFile{Filename: group.Filename, URL: group.URL})
				}
			}
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) != 1 {
		return nil, errors.New("discovery returned duplicate requested date")
	}
	if err := validateStaticURL(matches[0].URL, requested); err != nil {
		return nil, err
	}
	return &matches[0], nil
}

func download(ctx context.Context, client *http.Client, rawURL string) ([]byte, time.Time, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, time.Time{}, err
	}
	body, received, status, err := get(ctx, client, u, DailyCap)
	if err != nil {
		return nil, time.Time{}, err
	}
	if status != http.StatusOK {
		return nil, time.Time{}, &httpStatusError{Status: status}
	}
	return body, received, nil
}

func get(ctx context.Context, client *http.Client, u *url.URL, cap int64) ([]byte, time.Time, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, time.Time{}, 0, err
	}
	resp, err := client.Do(req)
	received := time.Now().UTC()
	if err != nil {
		return nil, received, 0, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > cap {
		return nil, received, resp.StatusCode, errors.New("response exceeds size cap")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cap+1))
	if err != nil {
		return nil, received, resp.StatusCode, err
	}
	if int64(len(body)) > cap {
		return nil, received, resp.StatusCode, errors.New("response exceeds size cap")
	}
	return body, received, resp.StatusCode, nil
}

type httpStatusError struct{ Status int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

func safeClient(in *http.Client) *http.Client {
	var c http.Client
	if in != nil {
		c = *in
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func validateStaticURL(raw string, d time.Time) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "static.okx.com") || u.User != nil {
		return errors.New("static URL rejected")
	}
	expected := "/cdn/okex/traderecords/swaprates/daily/" + d.Format("20060102") + "/allswap-fundingrates-" + d.Format("2006-01-02") + ".zip"
	if u.Path != expected {
		return errors.New("static path/date rejected")
	}
	return nil
}

func verifyDay(dateText string, file *discoveryFile, raw []byte, receive, process time.Time, symbols map[string]struct{}) (DailyResult, []byte, error) {
	rawHash := hashBytes(raw)
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return DailyResult{}, nil, err
	}
	if len(reader.File) != 1 || reader.File[0].FileInfo().IsDir() {
		return DailyResult{}, nil, errors.New("zip must contain one CSV")
	}
	zf := reader.File[0]
	expectedCSV := strings.TrimSuffix(file.Filename, ".zip") + ".csv"
	if zf.Name != expectedCSV || filepath.Base(zf.Name) != zf.Name || strings.Contains(zf.Name, `\`) {
		return DailyResult{}, nil, errors.New("zip-slip or unexpected CSV name")
	}
	rc, err := zf.Open()
	if err != nil {
		return DailyResult{}, nil, err
	}
	csvRaw, err := io.ReadAll(io.LimitReader(rc, DailyCap+1))
	closeErr := rc.Close()
	if err != nil {
		return DailyResult{}, nil, err
	}
	if closeErr != nil {
		return DailyResult{}, nil, closeErr
	}
	if len(csvRaw) > DailyCap {
		return DailyResult{}, nil, errors.New("CSV exceeds cap")
	}
	records, err := csv.NewReader(bytes.NewReader(csvRaw)).ReadAll()
	if err != nil {
		return DailyResult{}, nil, err
	}
	if len(records) == 0 || strings.Join(records[0], ",") != CSVHeader {
		return DailyResult{}, nil, errors.New("CSV header mismatch")
	}
	sourceURLHash := hashBytes([]byte(file.URL))
	events := map[string]Event{}
	unknownRows := 0
	for row, fields := range records[1:] {
		if len(fields) != 3 {
			return DailyResult{}, nil, fmt.Errorf("row %d field count", row+2)
		}
		instID, rate := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		if _, ok := symbols[instID]; !ok {
			continue
		}
		if rate == "" || strings.EqualFold(rate, "none") {
			unknownRows++
			continue
		}
		ts, err := parseFundingTime(strings.TrimSpace(fields[2]))
		if err != nil {
			return DailyResult{}, nil, fmt.Errorf("row %d: %w", row+2, err)
		}
		if _, err := strconv.ParseFloat(rate, 64); err != nil {
			return DailyResult{}, nil, fmt.Errorf("row %d rate: %w", row+2, err)
		}
		key := instID + "\x00" + strconv.FormatInt(ts, 10)
		eventID, _, _ := datatruth.HashJSON(map[string]any{"instId": instID, "fundingTime": ts})
		event := Event{EventID: eventID, InstID: instID, FundingRate: rate, FundingTime: ts, EventTime: time.UnixMilli(ts).UTC().Format(time.RFC3339Nano), ReceiveTime: receive.UTC().Format(time.RFC3339Nano), ProcessTime: process.UTC().Format(time.RFC3339Nano), SourceID: SourceID, SourceURLHash: sourceURLHash, DailyRawHash: rawHash}
		if prior, ok := events[key]; ok {
			if prior.FundingRate != rate {
				return DailyResult{}, nil, fmt.Errorf("conflicting duplicate %s", key)
			}
			continue
		}
		events[key] = event
	}
	keys := make([]string, 0, len(events))
	for key := range events {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	for _, key := range keys {
		if err := encoder.Encode(events[key]); err != nil {
			return DailyResult{}, nil, err
		}
	}
	outputHash := hashBytes(out.Bytes())
	return DailyResult{Date: dateText, Status: "COMPLETE", Filename: file.Filename, SourceURLHash: sourceURLHash, RawHash: rawHash, OutputHash: outputHash, Rows: len(keys), UnknownRows: unknownRows, Bytes: int64(len(raw))}, out.Bytes(), nil
}

func parseFundingTime(value string) (int64, error) {
	if ts, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ts < 1_000_000_000_000 {
			ts *= 1000
		}
		return ts, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, fmt.Errorf("invalid funding_time %q", value)
	}
	return t.UnixMilli(), nil
}

func catalogSymbols(path string) (map[string]struct{}, error) {
	var catalog struct {
		Families []struct {
			Name  string `json:"name"`
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"families"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, family := range catalog.Families {
		if family.Name != "okx_1h" {
			continue
		}
		for _, file := range family.Files {
			out[strings.TrimSuffix(filepath.Base(file.Path), ".jsonl")] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("catalog has no okx_1h symbols")
	}
	return out, nil
}

func freezePlan(path string, plan Plan) error {
	if raw, err := os.ReadFile(path); err == nil {
		var prior Plan
		if json.Unmarshal(raw, &prior) != nil || prior.PlanHash != plan.PlanHash {
			return errors.New("frozen source plan drift")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeAtomicJSON(path, plan)
}

func loadCheckpoint(path, planHash string) (Checkpoint, error) {
	cp := Checkpoint{SchemaVersion: "lingxi.okx-funding-archive-checkpoint.v1", PlanHash: planHash, Completed: map[string]DailyResult{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cp, nil
	}
	if err != nil {
		return cp, err
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		return cp, err
	}
	if cp.PlanHash != planHash || cp.Completed == nil {
		return cp, errors.New("checkpoint plan drift")
	}
	return cp, nil
}

func buildManifest(plan Plan, cp Checkpoint) (Manifest, error) {
	m := Manifest{SchemaVersion: "lingxi.okx-funding-archive-manifest.v1", LabelsRead: false, PlanHash: plan.PlanHash, SourceID: SourceID, Header: CSVHeader, DateCount: len(plan.Dates)}
	for _, date := range plan.Dates {
		result, ok := cp.Completed[date]
		if !ok {
			return Manifest{}, fmt.Errorf("date incomplete %s", date)
		}
		m.Days = append(m.Days, result)
		if result.Status == "COMPLETE" {
			m.CompleteDays++
			m.Rows += result.Rows
			m.Bytes += result.Bytes
		} else {
			m.GapDays++
		}
	}
	copy := m
	copy.ManifestHash = ""
	var err error
	m.ManifestHash, _, err = datatruth.HashJSON(copy)
	return m, err
}

func writeAtomicJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'), 0o644)
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileHash(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}
func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func day(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
