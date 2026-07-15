// okx-history-download is an opt-in, GET-only OKX historical downloader.
// Without --connect it validates the frozen contract and performs no network I/O.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/historydownload"
	"lingxi/internal/historydownload/okxarchive"
	"lingxi/internal/historydownload/okxreadonly"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "okx-history-download:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("okx-history-download", flag.ContinueOnError)
	mode := fs.String("mode", "", "private-pages, instruments-snapshot, or archive-smoke")
	endpoint := fs.String("endpoint", "", "approved endpoint for private-pages or instruments-snapshot")
	connect := fs.Bool("connect", false, "permit explicit GET-only connection (default is offline)")
	secret := fs.String("secret", "", "0600 local private credential file")
	rawRoot := fs.String("raw-root", "", "raw storage outside repository")
	rootCap := fs.Int64("root-cap", 4<<30, "historydownload root hard cap in bytes (maximum 4 GiB)")
	contract := fs.String("contract", "research/firstbattle/route_data_contracts.json", "frozen contract path")
	rate := fs.Duration("min-interval", time.Second, "minimum interval between requests")
	instType := fs.String("inst-type", "", "OKX instrument type")
	instID := fs.String("inst-id", "", "single instrument ID")
	archiveModule := fs.String("module", "1", "archive module: 1 trades only")
	utcDate := fs.String("utc-date", "", "single archive UTC date YYYY-MM-DD")
	archiveCap := fs.Int64("archive-cap", 0, "explicit archive ZIP byte cap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repoRoot, canonicalContract, err := historydownload.ResolveRepository(*contract)
	if err != nil {
		return err
	}
	if err := historydownload.ValidateContract(repoRoot, canonicalContract); err != nil {
		return err
	}
	if *mode != "private-pages" && *mode != "instruments-snapshot" && *mode != "archive-smoke" {
		return fmt.Errorf("--mode must be private-pages, instruments-snapshot, or archive-smoke")
	}
	if *mode != "archive-smoke" && *endpoint == "" {
		return fmt.Errorf("--endpoint is required")
	}
	if *rootCap <= 0 || *rootCap > 4<<30 {
		return fmt.Errorf("--root-cap must be between 1 and 4 GiB")
	}
	if *mode == "private-pages" && *endpoint != historydownload.OrdersEndpoint && *endpoint != historydownload.FillsEndpoint {
		return fmt.Errorf("private-pages supports only orders-history-archive or fills-history")
	}
	if *mode == "instruments-snapshot" && *endpoint != historydownload.PublicInstruments && *endpoint != historydownload.AccountInstruments {
		return fmt.Errorf("instruments-snapshot supports only instrument endpoints")
	}
	private := *mode == "private-pages" || (*mode == "instruments-snapshot" && *endpoint == historydownload.AccountInstruments)
	expectedRoot := historydownload.DefaultPublicRawRoot
	if private {
		expectedRoot = historydownload.DefaultPrivateRawRoot
	}
	customRoot := *rawRoot != ""
	if *connect && customRoot && *rawRoot != expectedRoot {
		return fmt.Errorf("--connect requires the canonical mode-specific default raw root")
	}
	if *rawRoot == "" {
		*rawRoot = expectedRoot
	}
	canonicalRoot, err := historydownload.ValidateOutputRoot(*rawRoot, repoRoot)
	if err != nil {
		return err
	}
	*rawRoot = canonicalRoot
	if !*connect {
		fmt.Fprintf(os.Stdout, "{\"offline\":true,\"mode\":%q,\"endpoint\":%q,\"raw_root\":%q,\"contract_hash\":%q}\n", *mode, *endpoint, *rawRoot, historydownload.ContractHash)
		return nil
	}
	if filepath.Clean(*rawRoot) != expectedRoot {
		return fmt.Errorf("--connect requires the canonical mode-specific default raw root")
	}
	if *mode == "private-pages" && *instType == "" {
		return fmt.Errorf("private-pages requires explicit --inst-type")
	}
	var credentials historydownload.Credentials
	var credentialWarnings []string
	if private {
		if *secret == "" {
			return fmt.Errorf("--secret required for private GET")
		}
		credentials, credentialWarnings, err = historydownload.LoadCredentials(*secret)
		if err != nil {
			return err
		}
		for _, warning := range credentialWarnings {
			fmt.Fprintln(os.Stderr, "warning:", warning)
		}
	}
	limiter, err := historydownload.NewLimiter(*rate)
	if err != nil {
		return err
	}
	client := okxreadonly.Client{Credentials: credentials, Limiter: limiter}
	query := map[string]string{}
	if *instType != "" {
		query["instType"] = *instType
	}
	if *instID != "" {
		query["instId"] = *instID
	}
	ctx := context.Background()
	switch *mode {
	case "private-pages":
		return downloadPrivatePages(ctx, &client, *endpoint, query, *rawRoot, *rootCap, credentialWarnings)
	case "instruments-snapshot":
		return downloadInstrumentSnapshot(ctx, &client, *endpoint, private, query, *rawRoot, *rootCap, credentialWarnings)
	case "archive-smoke":
		request, err := archiveRequest(*archiveModule, *instType, *instID, *utcDate, *archiveCap, time.Now().UTC())
		if err != nil {
			return err
		}
		return downloadArchiveSmoke(ctx, &client, request, *rawRoot, *rootCap, nil)
	}
	return fmt.Errorf("unreachable mode")
}

func archiveRequest(module, instType, instID, utcDate string, capBytes int64, now time.Time) (okxarchive.DiscoveryRequest, error) {
	if module != "1" {
		return okxarchive.DiscoveryRequest{}, fmt.Errorf("archive-smoke --module must be 1 (trades)")
	}
	if instType == "" || instID == "" || strings.Contains(instID, ",") || utcDate == "" || capBytes <= 0 ||
		capBytes > okxarchive.MaxArchiveBytes {
		return okxarchive.DiscoveryRequest{}, fmt.Errorf("archive-smoke requires one --inst-type, --inst-id, --utc-date, and valid --archive-cap")
	}
	date, err := time.Parse("2006-01-02", utcDate)
	if err != nil {
		return okxarchive.DiscoveryRequest{}, fmt.Errorf("archive-smoke invalid UTC date")
	}
	today := now.UTC().Truncate(24 * time.Hour)
	if date.After(today.AddDate(0, 0, -2)) {
		return okxarchive.DiscoveryRequest{}, fmt.Errorf("archive-smoke date must be at least T+2 complete")
	}
	begin := date.UnixMilli()
	return okxarchive.DiscoveryRequest{
		Module: module, InstType: instType, Instrument: instID, UTCDate: utcDate,
		BeginMS: begin, EndMS: begin + int64(24*time.Hour/time.Millisecond) - 1, ArchiveCap: capBytes,
	}, nil
}

func downloadArchiveSmoke(
	ctx context.Context,
	client *okxreadonly.Client,
	request okxarchive.DiscoveryRequest,
	root string,
	rootCap int64,
	fault okxarchive.ArchiveFault,
) error {
	rootLock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		return err
	}
	defer rootLock.Close()
	checkCapacity := func(adding int64) error {
		return historydownload.CheckRootCapacity(root, rootCap, adding)
	}
	if err := checkCapacity(request.ArchiveCap); err != nil {
		return err
	}
	query := map[string]string{
		"module": request.Module, "instType": request.InstType, "instIdList": request.Instrument,
		"dateAggrType": "daily", "begin": strconv.FormatInt(request.BeginMS, 10), "end": strconv.FormatInt(request.EndMS, 10),
	}
	discovery, err := client.FetchEnvelope(ctx, historydownload.Request{
		Endpoint: historydownload.MarketDataDiscovery, Query: query, Private: false,
	})
	if err != nil {
		return err
	}
	selected, err := okxarchive.SelectArchive(discovery.Raw, request)
	if err != nil {
		return err
	}
	if fault != nil {
		if err := fault("after_discovery"); err != nil {
			return err
		}
	}
	archiveRoot := filepath.Join(root, "archive-smoke")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp(archiveRoot, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	if err := checkCapacity(int64(len(discovery.Raw))); err != nil {
		return err
	}
	if err := historydownload.WriteBytesAtomic(filepath.Join(workspace, "discovery.json"), discovery.Raw, 0o600); err != nil {
		return err
	}
	zipPath := filepath.Join(workspace, "archive.zip")
	zipHash, err := okxarchive.DownloadZIPWithCapacity(
		ctx, client.HTTP, selected.URL, zipPath, request.ArchiveCap, checkCapacity,
	)
	if err != nil {
		return err
	}
	if fault != nil {
		if err := fault("after_zip"); err != nil {
			return err
		}
	}
	csvFiles, err := okxarchive.ExtractCSVWithCapacity(
		zipPath, filepath.Join(workspace, "extracted"), request.ArchiveCap, checkCapacity,
	)
	if err != nil || len(csvFiles) != 1 {
		if err == nil {
			err = fmt.Errorf("archive-smoke ZIP must contain exactly one CSV")
		}
		return err
	}
	dataPath := filepath.Join(workspace, "data.csv")
	if err := os.Rename(csvFiles[0], dataPath); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(workspace, "extracted")); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("after_extract"); err != nil {
			return err
		}
	}
	receipt, err := okxarchive.BuildSmokeReceipt(dataPath, zipHash, request, selected)
	if err != nil {
		return err
	}
	queryHash, err := historydownload.Fingerprint(okxreadonly.SortedQuery(query))
	if err != nil {
		return err
	}
	zipInfo, err := os.Stat(zipPath)
	if err != nil {
		return err
	}
	csvInfo, err := os.Stat(dataPath)
	if err != nil {
		return err
	}
	started := discovery.ReceiveTime
	completed := time.Now().UTC()
	manifest := historydownload.Manifest{
		Endpoint: historydownload.MarketDataDiscovery, QueryHash: queryHash,
		PageRawHashes: []string{historydownload.HashBytes(discovery.Raw)}, RecordCount: receipt.Rows,
		BytesWritten: zipInfo.Size() + csvInfo.Size(), ProjectionFields: receipt.Columns,
		PresenceCounts: map[string]int{}, Clocks: historydownload.Clock{EventTime: receipt.EventTimeMax, ProcessTime: completed},
		EventTimeRange: historydownload.ClockRange{Min: receipt.EventTimeMin, Max: receipt.EventTimeMax},
		ClockFitness:   "ARCHIVE_ORIGINAL_RECEIVE_TIME_ABSENT",
		Warnings: []string{
			"public archive has no original collector receive time",
			"one-day one-instrument smoke only; not rule PIT",
		},
		StartedAt: started, CompletedAt: completed, PITStatus: "PUBLIC_ARCHIVE_SMOKE_ONLY/PIT_UNPROVEN",
	}
	_, err = okxarchive.PublishArchiveGeneration(root, rootCap, rootLock, workspace, receipt, manifest, fault)
	return err
}

func downloadPrivatePages(ctx context.Context, client *okxreadonly.Client, endpoint string, query map[string]string, root string, rootCap int64, warnings []string) error {
	rootLock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		return err
	}
	defer rootLock.Close()
	fields, schemaHash, err := historydownload.ProjectionSchema(endpoint)
	if err != nil {
		return err
	}
	warnings = append([]string(nil), warnings...)
	warnings = append(warnings,
		"REST archive has no original collector receive time; receive_time is local retrieval time only",
		"rolling private API slice does not prove a complete attempts denominator",
	)
	if endpoint == historydownload.OrdersEndpoint {
		warnings = append(warnings, "orders-history-archive omits rejected/expired/live-open attempts; denominator incomplete")
	}
	query["limit"] = "100"
	queryHash, err := historydownload.Fingerprint(okxreadonly.SortedQuery(query))
	if err != nil {
		return err
	}
	instType := query["instType"]
	if instType == "" {
		return fmt.Errorf("private-pages requires explicit instType")
	}
	dir := filepath.Join(root, strings.TrimPrefix(endpoint, "/"), instType)
	config := historydownload.DownloadConfig{
		Endpoint: endpoint, RawDirectory: filepath.Join(dir, "raw"), ProjectionDirectory: filepath.Join(dir, "projection"),
		CheckpointPath: filepath.Join(dir, "checkpoint.json"), ManifestPath: filepath.Join(dir, "manifest.json"),
		QueryHash: queryHash, SchemaHash: schemaHash, DiskCapBytes: rootCap, Private: true, PITStatus: "PRIVATE_HISTORY_UNVERIFIED",
		RootDirectory: root, RootCapBytes: rootCap,
		RootLock:     rootLock,
		ClockFitness: "ARCHIVE_ORIGINAL_RECEIVE_TIME_ABSENT", DenominatorFitness: "INCOMPLETE_API_SLICE",
		TokenStore: historydownload.TokenStore{SaltPath: filepath.Join(root, "token.salt")}, Warnings: warnings,
	}
	_, err = historydownload.Download(ctx, config, func(ctx context.Context, cursor string) (historydownload.Page, error) {
		requestQuery := copyQuery(query)
		if cursor != "" {
			requestQuery["after"] = cursor
		}
		response, err := client.FetchEnvelope(ctx, historydownload.Request{Endpoint: endpoint, Query: requestQuery, Private: true})
		if err != nil {
			return historydownload.Page{}, err
		}
		next, err := paginationCursor(endpoint, response.Data)
		if err != nil {
			return historydownload.Page{}, err
		}
		eventMin, eventMax, err := eventClockRange(endpoint, response.Data)
		if err != nil {
			return historydownload.Page{}, err
		}
		return historydownload.Page{
			Raw: response.Raw, Records: response.Data, NextCursor: next, ReceiveTime: response.ReceiveTime,
			EventTimeMin: eventMin, EventTimeMax: eventMax,
		}, nil
	})
	_ = fields // schema receipt carries its canonical fields.
	return err
}

func downloadInstrumentSnapshot(ctx context.Context, client *okxreadonly.Client, endpoint string, private bool, query map[string]string, root string, rootCap int64, credentialWarnings []string) error {
	rootLock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		return err
	}
	defer rootLock.Close()
	response, err := client.FetchEnvelope(ctx, historydownload.Request{Endpoint: endpoint, Query: query, Private: private})
	if err != nil {
		return err
	}
	receipt, err := historydownload.BuildInstrumentSnapshot(endpoint, response.Data, response.ReceiveTime)
	if err != nil {
		return err
	}
	queryHash, err := historydownload.Fingerprint(okxreadonly.SortedQuery(query))
	if err != nil {
		return err
	}
	rawHash := historydownload.HashBytes(response.Raw)
	warnings := append([]string(nil), credentialWarnings...)
	warnings = append(warnings, "instrument metadata is capture-time only; historical catalog prohibited")
	manifest := historydownload.Manifest{
		Endpoint: endpoint, Private: private, QueryHash: queryHash, PageRawHashes: []string{rawHash},
		RecordCount: len(response.Data), BytesWritten: int64(len(response.Raw)),
		ProjectionFields: receipt.Projection.Fields, ProjectionSchemaHash: receipt.Projection.SchemaHash,
		PresenceCounts: receipt.Projection.PresenceCounts, OmittedCounts: receipt.Projection.OmittedCounts,
		Clocks:           historydownload.Clock{ReceiveTime: receipt.KnownFrom, ProcessTime: time.Now().UTC()},
		ReceiveTimeRange: historydownload.ClockRange{Min: receipt.KnownFrom, Max: receipt.KnownFrom},
		ClockFitness:     "EVENT_CLOCK_NOT_APPLICABLE_SNAPSHOT", Warnings: warnings,
		StartedAt: receipt.KnownFrom, CompletedAt: time.Now().UTC(), PITStatus: historydownload.InstrumentPITStatus,
	}
	_, err = historydownload.PublishInstrumentGeneration(root, rootCap, rootLock, response.Raw, receipt, manifest, nil)
	return err
}

func eventClockRange(endpoint string, records []map[string]any) (time.Time, time.Time, error) {
	fields := []string{"cTime", "uTime"}
	if endpoint == historydownload.FillsEndpoint {
		fields = []string{"fillTime"}
	}
	var minTime, maxTime time.Time
	for _, record := range records {
		for _, field := range fields {
			raw, ok := record[field].(string)
			if !ok || raw == "" {
				return time.Time{}, time.Time{}, fmt.Errorf("%s record lacks required %s event clock", endpoint, field)
			}
			millis, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("%s invalid %s event clock", endpoint, field)
			}
			value := time.UnixMilli(millis).UTC()
			if minTime.IsZero() || value.Before(minTime) {
				minTime = value
			}
			if maxTime.IsZero() || value.After(maxTime) {
				maxTime = value
			}
		}
	}
	return minTime, maxTime, nil
}

func paginationCursor(endpoint string, records []map[string]any) (string, error) {
	if len(records) == 0 {
		return "", nil
	}
	field := "ordId"
	if endpoint == historydownload.FillsEndpoint {
		field = "billId"
	}
	value, ok := records[len(records)-1][field].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s final record lacks %s cursor", endpoint, field)
	}
	return value, nil
}

func copyQuery(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}
