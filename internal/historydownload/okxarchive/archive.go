// Package okxarchive handles official, public OKX historical archive files.
package okxarchive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	MaxArchiveBytes = int64(4 << 30)
	MaxFiles        = 128
)

var officialHosts = map[string]bool{
	"www.okx.com":    true, // official discovery pages only
	"static.okx.com": true, // controlled archive CDN only
}

// ValidateURL accepts only HTTPS official discovery or static archive URLs.
func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !officialHosts[strings.ToLower(u.Host)] {
		return nil, errors.New("okxarchive: host rejected")
	}
	if u.Host == "static.okx.com" && !strings.HasSuffix(strings.ToLower(u.Path), ".zip") {
		return nil, errors.New("okxarchive: CDN asset must be ZIP")
	}
	return u, nil
}

// ExtractCSV safely extracts CSV files after zip-slip, compression, and CRC checks.
func ExtractCSV(zipPath, destination string, maxBytes int64) ([]string, error) {
	return ExtractCSVWithCapacity(zipPath, destination, maxBytes, nil)
}

type CapacityCheck func(adding int64) error

func ExtractCSVWithCapacity(zipPath, destination string, maxBytes int64, capacity CapacityCheck) ([]string, error) {
	if maxBytes <= 0 || maxBytes > MaxArchiveBytes {
		maxBytes = MaxArchiveBytes
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if len(r.File) == 0 || len(r.File) > MaxFiles {
		return nil, errors.New("okxarchive: invalid archive file count")
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(destination); err == nil {
		return nil, errors.New("okxarchive: extraction destination already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, filepath.Base(destination)+".staging-*")
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	root, err := filepath.Abs(stage)
	if err != nil {
		return nil, err
	}
	var total int64
	var relativeOutput []string
	for _, file := range r.File {
		if file.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(file.Name), ".csv") {
			return nil, errors.New("okxarchive: only regular CSV files allowed")
		}
		if file.UncompressedSize64 > uint64(maxBytes-total) {
			return nil, errors.New("okxarchive: decompression size cap exceeded")
		}
		target := filepath.Join(root, filepath.Clean(file.Name))
		rel, err := filepath.Rel(root, target)
		if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			return nil, errors.New("okxarchive: zip-slip path rejected")
		}
		if err := extractOne(file, target, maxBytes-total, capacity); err != nil {
			return nil, err
		}
		total += int64(file.UncompressedSize64)
		relativeOutput = append(relativeOutput, rel)
	}
	if err := os.Rename(stage, destination); err != nil {
		return nil, err
	}
	published = true
	parentDir, err := os.Open(parent)
	if err != nil {
		return nil, err
	}
	if err := parentDir.Sync(); err != nil {
		_ = parentDir.Close()
		return nil, err
	}
	if err := parentDir.Close(); err != nil {
		return nil, err
	}
	output := make([]string, 0, len(relativeOutput))
	for _, relative := range relativeOutput {
		output = append(output, filepath.Join(destination, relative))
	}
	return output, nil
}

func extractOne(file *zip.File, target string, max int64, capacity CapacityCheck) error {
	in, err := file.Open() // zip reader validates CRC at EOF.
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := io.Writer(out)
	if capacity != nil {
		writer = capacityWriter{writer: out, check: capacity}
	}
	written, copyErr := io.Copy(writer, io.LimitReader(in, max+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target + ".tmp")
		return copyErr
	}
	if syncErr != nil || closeErr != nil || written > max {
		_ = os.Remove(target + ".tmp")
		return errors.New("okxarchive: CSV exceeds size cap or CRC invalid")
	}
	return os.Rename(target+".tmp", target)
}

// SmokeReceipt proves a one-day, one-instrument archive decode before bulk use.
type SmokeReceipt struct {
	SchemaVersion    string    `json:"schema_version"`
	Module           string    `json:"module"`
	InstType         string    `json:"inst_type"`
	Instrument       string    `json:"instrument"`
	UTCDate          string    `json:"utc_date"`
	Filename         string    `json:"filename"`
	DiscoveryURLHash string    `json:"discovery_url_hash"`
	DeclaredBytes    int64     `json:"declared_bytes"`
	ArchiveCapBytes  int64     `json:"archive_cap_bytes"`
	RequestedBeginMS int64     `json:"requested_begin_ms"`
	RequestedEndMS   int64     `json:"requested_end_ms"`
	Rows             int       `json:"rows"`
	CSVHash          string    `json:"csv_hash"`
	ZIPHash          string    `json:"zip_hash"`
	Columns          []string  `json:"columns"`
	EventClock       string    `json:"event_clock"`
	EventTimeMin     time.Time `json:"event_time_min"`
	EventTimeMax     time.Time `json:"event_time_max"`
	PITStatus        string    `json:"pit_status"`
	VerifiedAt       time.Time `json:"verified_at"`
}

func VerifySmokeReceipt(receipt SmokeReceipt, request DiscoveryRequest, selected SelectedArchive) error {
	if receipt.SchemaVersion != "okxarchive.smoke.v2" ||
		receipt.Module != request.Module || receipt.InstType != request.InstType ||
		receipt.Instrument != request.Instrument || receipt.UTCDate != request.UTCDate ||
		receipt.Filename != selected.Filename || receipt.DiscoveryURLHash != selected.URLHash ||
		receipt.DeclaredBytes != selected.DeclaredBytes || receipt.ArchiveCapBytes != request.ArchiveCap ||
		receipt.RequestedBeginMS != request.BeginMS || receipt.RequestedEndMS != request.EndMS ||
		receipt.DeclaredBytes > receipt.ArchiveCapBytes || receipt.Rows < 1 ||
		receipt.CSVHash == "" || receipt.ZIPHash == "" || len(receipt.Columns) == 0 || receipt.EventClock == "" ||
		receipt.EventTimeMin.IsZero() || receipt.EventTimeMax.IsZero() ||
		receipt.EventTimeMin.After(receipt.EventTimeMax) ||
		receipt.EventTimeMin.UnixMilli() < request.BeginMS || receipt.EventTimeMax.UnixMilli() > request.EndMS {
		return errors.New("okxarchive: valid one-day one-instrument smoke receipt required before bulk")
	}
	return nil
}

func BuildSmokeReceipt(csvPath, zipHash string, request DiscoveryRequest, selected SelectedArchive) (SmokeReceipt, error) {
	if selected.Module != request.Module || selected.InstType != request.InstType ||
		selected.Instrument != request.Instrument || selected.UTCDate != request.UTCDate {
		return SmokeReceipt{}, errors.New("okxarchive: selected archive provenance mismatch")
	}
	f, err := os.Open(csvPath)
	if err != nil {
		return SmokeReceipt{}, err
	}
	defer f.Close()
	h := sha256.New()
	tee := io.TeeReader(f, h)
	reader := csv.NewReader(tee)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil || len(header) == 0 {
		return SmokeReceipt{}, errors.New("okxarchive: CSV header absent")
	}
	columns := make([]string, len(header))
	eventIndex := -1
	required := map[string]bool{}
	for index, column := range header {
		column = strings.TrimSpace(column)
		columns[index] = column
		lower := strings.ToLower(column)
		required[lower] = true
		switch lower {
		case "ts", "timestamp", "time":
			eventIndex = index
		}
	}
	if eventIndex < 0 {
		return SmokeReceipt{}, errors.New("okxarchive: CSV event clock column absent")
	}
	if request.Module == "1" && (!required["px"] || !required["sz"] || !required["side"]) {
		return SmokeReceipt{}, errors.New("okxarchive: trades CSV required columns absent")
	}
	requestedDay, err := time.Parse("2006-01-02", request.UTCDate)
	if err != nil {
		return SmokeReceipt{}, err
	}
	rows := 0
	var eventMin, eventMax time.Time
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || eventIndex >= len(record) {
			return SmokeReceipt{}, errors.New("okxarchive: malformed CSV row")
		}
		eventMS, err := strconv.ParseInt(strings.TrimSpace(record[eventIndex]), 10, 64)
		if err != nil || !time.UnixMilli(eventMS).UTC().Truncate(24*time.Hour).Equal(requestedDay) {
			return SmokeReceipt{}, errors.New("okxarchive: CSV event outside requested UTC day")
		}
		eventTime := time.UnixMilli(eventMS).UTC()
		if eventMin.IsZero() || eventTime.Before(eventMin) {
			eventMin = eventTime
		}
		if eventMax.IsZero() || eventTime.After(eventMax) {
			eventMax = eventTime
		}
		rows++
	}
	if rows < 1 {
		return SmokeReceipt{}, errors.New("okxarchive: CSV has no data rows")
	}
	receipt := SmokeReceipt{
		SchemaVersion: "okxarchive.smoke.v2", Module: request.Module, InstType: request.InstType,
		Instrument: request.Instrument, UTCDate: request.UTCDate, Filename: selected.Filename,
		DiscoveryURLHash: selected.URLHash, DeclaredBytes: selected.DeclaredBytes,
		ArchiveCapBytes: request.ArchiveCap, RequestedBeginMS: request.BeginMS, RequestedEndMS: request.EndMS,
		Rows:    rows,
		CSVHash: hex.EncodeToString(h.Sum(nil)), ZIPHash: zipHash, Columns: columns, EventClock: columns[eventIndex],
		EventTimeMin: eventMin, EventTimeMax: eventMax,
		PITStatus: "PUBLIC_ARCHIVE_SMOKE_ONLY/PIT_UNPROVEN", VerifiedAt: time.Now().UTC(),
	}
	if err := VerifySmokeReceipt(receipt, request, selected); err != nil {
		return SmokeReceipt{}, err
	}
	return receipt, nil
}

// DownloadZIP downloads exactly one validated static OKX ZIP with a hard cap.
func DownloadZIP(ctx context.Context, client *http.Client, rawURL, path string, capBytes int64) (string, error) {
	return DownloadZIPWithCapacity(ctx, client, rawURL, path, capBytes, nil)
}

func DownloadZIPWithCapacity(
	ctx context.Context,
	client *http.Client,
	rawURL, path string,
	capBytes int64,
	capacity CapacityCheck,
) (string, error) {
	if capBytes <= 0 || capBytes > MaxArchiveBytes {
		return "", errors.New("okxarchive: explicit valid disk cap required")
	}
	u, err := ValidateURL(rawURL)
	if err != nil || u.Host != "static.okx.com" {
		return "", errors.New("okxarchive: controlled static ZIP required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := safeClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("okxarchive: ZIP status %d", response.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return "", err
	}
	hasher := sha256.New()
	fileWriter := io.Writer(temp)
	if capacity != nil {
		fileWriter = capacityWriter{writer: temp, check: capacity}
	}
	written, copyErr := io.Copy(io.MultiWriter(fileWriter, hasher), io.LimitReader(response.Body, capBytes+1))
	if copyErr != nil || written > capBytes {
		cleanup()
		if copyErr != nil {
			return "", copyErr
		}
		return "", errors.New("okxarchive: ZIP disk cap exceeded")
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type capacityWriter struct {
	writer io.Writer
	check  CapacityCheck
}

func (w capacityWriter) Write(value []byte) (int, error) {
	if err := w.check(int64(len(value))); err != nil {
		return 0, err
	}
	return w.writer.Write(value)
}
