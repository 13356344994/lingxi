package datadrivenv3

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
	"math"
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
	FundingArchiveDirectory = "records/history/okx_funding_archive_daily"
	fundingDiscoveryHost    = "www.okx.com"
	fundingDiscoveryPath    = "/api/v5/public/market-data-history"
	fundingStaticHost       = "static.okx.com"
	fundingZipCapBytes      = int64(1 << 20)
	fundingDiskCapBytes     = int64(200 << 20)
)

type FundingRecord struct {
	Instrument  string `json:"instrument"`
	FundingRate string `json:"funding_rate"`
	EventTime   int64  `json:"event_time"`
}

type fundingDayManifest struct {
	SchemaVersion   string `json:"schema_version"`
	Status          string `json:"status"`
	UTCDate         string `json:"utc_date"`
	Module          string `json:"module"`
	DiscoveryURL    string `json:"discovery_url"`
	DiscoverySHA256 string `json:"discovery_sha256"`
	ArchiveURL      string `json:"archive_url,omitempty"`
	ArchiveSHA256   string `json:"archive_sha256,omitempty"`
	CanonicalSHA256 string `json:"canonical_sha256,omitempty"`
	Rows            int    `json:"rows"`
	UnknownRateRows int    `json:"unknown_rate_rows,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type FundingArchiveSourceManifest struct {
	SchemaVersion string               `json:"schema_version"`
	Module        string               `json:"module"`
	StartDate     string               `json:"start_date"`
	EndDate       string               `json:"end_date"`
	DiskCapBytes  int64                `json:"disk_cap_bytes"`
	Days          []fundingDayManifest `json:"days"`
	ManifestHash  string               `json:"manifest_hash"`
}

type FundingArchiveConfig struct {
	Root       string
	Start, End time.Time
	HTTP       *http.Client
	Progress   io.Writer
}

// DownloadFundingArchive fetches only the official module=3 daily allswap
// funding archive. It is deliberately sequential: one request pair at a time.
func DownloadFundingArchive(ctx context.Context, cfg FundingArchiveConfig) (FundingArchiveSourceManifest, error) {
	if cfg.Root == "" || cfg.Start.IsZero() || cfg.End.IsZero() {
		return FundingArchiveSourceManifest{}, errors.New("funding archive requires root and inclusive dates")
	}
	start, end := cfg.Start.UTC().Truncate(24*time.Hour), cfg.End.UTC().Truncate(24*time.Hour)
	if end.Before(start) {
		return FundingArchiveSourceManifest{}, errors.New("funding archive end precedes start")
	}
	if start.Format("2006-01-02") != "2023-12-31" || end.Format("2006-01-02") != "2026-07-09" {
		return FundingArchiveSourceManifest{}, errors.New("funding archive date range must be 2023-12-31..2026-07-09")
	}
	root := filepath.Join(cfg.Root, FundingArchiveDirectory)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return FundingArchiveSourceManifest{}, err
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	out := FundingArchiveSourceManifest{
		SchemaVersion: "lingxi.data-driven-router-v3.okx-funding-archive-source-manifest.v1",
		Module:        "3", StartDate: start.Format("2006-01-02"), EndDate: end.Format("2006-01-02"),
		DiskCapBytes: fundingDiskCapBytes,
	}
	total := int(end.Sub(start)/(24*time.Hour)) + 1
	for day, n := start, 1; !day.After(end); day, n = day.AddDate(0, 0, 1), n+1 {
		manifest, err := downloadFundingDay(ctx, safeClient, root, day)
		if err != nil {
			return FundingArchiveSourceManifest{}, fmt.Errorf("%s: %w", day.Format("2006-01-02"), err)
		}
		out.Days = append(out.Days, manifest)
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "funding archive %d/%d %s %s\n", n, total, manifest.UTCDate, manifest.Status)
		}
	}
	hashable := out
	hashable.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(hashable)
	if err != nil {
		return FundingArchiveSourceManifest{}, err
	}
	out.ManifestHash = hash
	if err := writeJSONAtomic(filepath.Join(root, "source_manifest.json"), out); err != nil {
		return FundingArchiveSourceManifest{}, err
	}
	return out, nil
}

func downloadFundingDay(ctx context.Context, client http.Client, root string, day time.Time) (fundingDayManifest, error) {
	date := day.Format("2006-01-02")
	dataPath := filepath.Join(root, date+".jsonl")
	manifestPath := filepath.Join(root, date+".manifest.json")
	if _, dataErr := os.Stat(dataPath); dataErr == nil {
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			return fundingDayManifest{}, fmt.Errorf("existing data has no manifest: %w", err)
		}
		var prior fundingDayManifest
		if err := json.Unmarshal(raw, &prior); err != nil {
			return fundingDayManifest{}, err
		}
		if prior.UTCDate != date || prior.Status != "DOWNLOADED" || prior.CanonicalSHA256 == "" {
			return fundingDayManifest{}, errors.New("existing daily manifest is not a downloaded exact-date record")
		}
		got, err := SHA256File(dataPath)
		if err != nil || got != prior.CanonicalSHA256 {
			return fundingDayManifest{}, errors.New("existing canonical daily hash mismatch")
		}
		return prior, nil
	} else if !os.IsNotExist(dataErr) {
		return fundingDayManifest{}, dataErr
	} else if _, err := os.Stat(manifestPath); err == nil {
		raw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			return fundingDayManifest{}, readErr
		}
		var prior fundingDayManifest
		if readErr := json.Unmarshal(raw, &prior); readErr != nil {
			return fundingDayManifest{}, readErr
		}
		if prior.UTCDate == date && prior.Status == "ABSENT" && prior.CanonicalSHA256 == "" {
			return prior, nil
		}
		return fundingDayManifest{}, errors.New("manifest exists without canonical daily data")
	} else if !os.IsNotExist(err) {
		return fundingDayManifest{}, err
	}
	if err := checkFundingDisk(root, 0); err != nil {
		return fundingDayManifest{}, err
	}
	discoveryURL := fundingDiscoveryURL(day)
	discovery, err := fundingGETRetry(ctx, &client, discoveryURL, fundingZipCapBytes)
	if err != nil {
		return fundingDayManifest{}, err
	}
	manifest := fundingDayManifest{
		SchemaVersion: "lingxi.data-driven-router-v3.okx-funding-archive-day.v1",
		UTCDate:       date, Module: "3", DiscoveryURL: discoveryURL,
		DiscoverySHA256: hashBytes(discovery),
	}
	archiveURL, absent, err := selectFundingArchive(discovery, day)
	if err != nil {
		return fundingDayManifest{}, err
	}
	if absent {
		manifest.Status, manifest.Reason = "ABSENT", "official module=3 discovery had no exact-date allswap funding archive"
		if err := writeJSONAtomic(manifestPath, manifest); err != nil {
			return fundingDayManifest{}, err
		}
		return manifest, nil
	}
	manifest.ArchiveURL = archiveURL
	archive, err := fundingGETRetry(ctx, &client, archiveURL, fundingZipCapBytes)
	if err != nil {
		return fundingDayManifest{}, err
	}
	manifest.ArchiveSHA256 = hashBytes(archive)
	records, unknownRows, err := decodeFundingZIP(archive, day)
	if err != nil {
		return fundingDayManifest{}, err
	}
	canonical, err := canonicalFundingJSONL(records)
	if err != nil {
		return fundingDayManifest{}, err
	}
	if err := checkFundingDisk(root, int64(len(canonical))); err != nil {
		return fundingDayManifest{}, err
	}
	manifest.Status, manifest.Rows, manifest.UnknownRateRows, manifest.CanonicalSHA256 = "DOWNLOADED", len(records), unknownRows, hashBytes(canonical)
	if err := writeBytesAtomic(dataPath, canonical); err != nil {
		return fundingDayManifest{}, err
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return fundingDayManifest{}, err
	}
	return manifest, nil
}

func fundingDiscoveryURL(day time.Time) string {
	q := url.Values{"module": {"3"}, "instType": {"SWAP"}, "dateAggrType": {"daily"}, "begin": {strconv.FormatInt(day.UnixMilli(), 10)}, "end": {strconv.FormatInt(day.Add(24*time.Hour).UnixMilli()-1, 10)}}
	return "https://" + fundingDiscoveryHost + fundingDiscoveryPath + "?" + q.Encode()
}

func selectFundingArchive(raw []byte, day time.Time) (string, bool, error) {
	var envelope struct {
		Code string `json:"code"`
		Data []struct {
			DateAggrType string `json:"dateAggrType"`
			Details      []struct {
				GroupDetails []struct {
					DateTS   string `json:"dateTs"`
					Filename string `json:"filename"`
					URL      string `json:"url"`
				} `json:"groupDetails"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Code != "0" {
		return "", false, errors.New("invalid official module=3 discovery envelope")
	}
	expectedName := "allswap-fundingrates-" + day.Format("2006-01-02") + ".zip"
	expectedPath := "/cdn/okex/traderecords/swaprates/daily/" + day.Format("20060102") + "/" + expectedName
	var selected []string
	for _, group := range envelope.Data {
		if group.DateAggrType != "daily" {
			return "", false, errors.New("discovery date aggregation drift")
		}
		for _, detail := range group.Details {
			for _, file := range detail.GroupDetails {
				// OKX's dateTs is UTC+8-labelled in this archive (for example,
				// 2023-12-31 is reported as 2023-12-30T16:00Z). Filename and
				// immutable CDN path are the official daily identity.
				if file.DateTS == "" || file.Filename != expectedName {
					continue
				}
				u, err := url.Parse(file.URL)
				if err != nil || u.Scheme != "https" || u.Host != fundingStaticHost || u.Path != expectedPath ||
					u.Fragment != "" || u.User != nil || u.Query().Get("v") != "999" || len(u.Query()) != 1 {
					return "", false, errors.New("discovery selected unapproved module=3 archive URL")
				}
				selected = append(selected, u.String())
			}
		}
	}
	if len(selected) == 0 {
		return "", true, nil
	}
	if len(selected) != 1 {
		return "", false, errors.New("discovery has ambiguous exact-date funding archive")
	}
	return selected[0], false, nil
}

func fundingGET(ctx context.Context, client *http.Client, rawURL string, capBytes int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		!((u.Host == fundingDiscoveryHost && u.Path == fundingDiscoveryPath) || u.Host == fundingStaticHost) {
		return nil, errors.New("GET-only funding URL rejected")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GET status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, capBytes+1))
	if err != nil || int64(len(body)) > capBytes {
		return nil, errors.New("response exceeds daily archive cap")
	}
	return body, nil
}

func fundingGETRetry(ctx context.Context, client *http.Client, rawURL string, capBytes int64) ([]byte, error) {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second * time.Duration(attempt)):
			}
		} else {
			time.Sleep(250 * time.Millisecond) // single worker, <= four GETs/second
		}
		var body []byte
		body, err = fundingGET(ctx, client, rawURL, capBytes)
		if err == nil || !strings.Contains(err.Error(), "GET status 429") {
			return body, err
		}
	}
	return nil, err
}

func decodeFundingZIP(raw []byte, day time.Time) ([]FundingRecord, int, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) != 1 {
		return nil, 0, errors.New("funding archive ZIP must contain exactly one CSV")
	}
	file := reader.File[0]
	if file.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(file.Name), ".csv") || file.UncompressedSize64 > uint64(fundingZipCapBytes) {
		return nil, 0, errors.New("funding archive ZIP schema or size rejected")
	}
	in, err := file.Open()
	if err != nil {
		return nil, 0, err
	}
	defer in.Close()
	csvReader := csv.NewReader(io.LimitReader(in, fundingZipCapBytes+1))
	header, err := csvReader.Read()
	if err != nil || strings.Join(header, ",") != "instrument_name,funding_rate,funding_time" {
		return nil, 0, errors.New("funding CSV schema drift")
	}
	seen := map[string]FundingRecord{}
	unknownRows := 0
	for {
		row, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) != 3 {
			return nil, 0, errors.New("malformed funding CSV record")
		}
		if strings.EqualFold(strings.TrimSpace(row[1]), "none") {
			unknownRows++
			continue
		}
		rate, err := canonicalRate(row[1])
		if err != nil {
			return nil, 0, err
		}
		event, err := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
		// The daily archive is labelled in the exchange's UTC+8 calendar while
		// funding_time itself remains an epoch timestamp. Keep the raw event
		// timestamp, accepting only that labelled local day.
		localStart := day.Add(-8 * time.Hour).UnixMilli()
		localEnd := day.Add(16 * time.Hour).UnixMilli()
		if err != nil || event < localStart || event >= localEnd {
			return nil, 0, errors.New("funding event outside requested archive day")
		}
		record := FundingRecord{Instrument: strings.TrimSpace(row[0]), FundingRate: rate, EventTime: event}
		if !strings.HasSuffix(record.Instrument, "-SWAP") || record.Instrument == "" {
			return nil, 0, errors.New("funding instrument rejected")
		}
		key := record.Instrument + "/" + strconv.FormatInt(record.EventTime, 10)
		if previous, ok := seen[key]; ok && previous != record {
			return nil, 0, errors.New("conflicting duplicate funding record")
		}
		seen[key] = record
	}
	out := make([]FundingRecord, 0, len(seen))
	for _, record := range seen {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instrument != out[j].Instrument {
			return out[i].Instrument < out[j].Instrument
		}
		return out[i].EventTime < out[j].EventTime
	})
	return out, unknownRows, nil
}

func canonicalFundingJSONL(records []FundingRecord) ([]byte, error) {
	var out bytes.Buffer
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func canonicalRate(raw string) (string, error) {
	rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return "", errors.New("invalid signed funding rate")
	}
	return strconv.FormatFloat(rate, 'g', -1, 64), nil
}

func checkFundingDisk(root string, adding int64) error {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil || total+adding > fundingDiskCapBytes {
		if err != nil {
			return err
		}
		return errors.New("funding archive disk cap exceeded")
	}
	return nil
}

func writeBytesAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".staging-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, append(raw, '\n'))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
