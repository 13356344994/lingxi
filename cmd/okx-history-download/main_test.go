package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"lingxi/internal/historydownload"
	"lingxi/internal/historydownload/okxarchive"
	"lingxi/internal/historydownload/okxreadonly"
)

func TestPaginationCursorUsesEndpointSpecificLastRecord(t *testing.T) {
	cursor, err := paginationCursor(historydownload.OrdersEndpoint, []map[string]any{{"ordId": "100"}, {"ordId": "99"}})
	if err != nil || cursor != "99" {
		t.Fatalf("orders after cursor = %q, %v", cursor, err)
	}
	cursor, err = paginationCursor(historydownload.FillsEndpoint, []map[string]any{{"tradeId": "200", "billId": "300"}, {"tradeId": "199", "billId": "299"}})
	if err != nil || cursor != "299" {
		t.Fatalf("fills after cursor = %q, %v", cursor, err)
	}
	if _, err := paginationCursor(historydownload.FillsEndpoint, []map[string]any{{"tradeId": "present-but-not-a-cursor"}}); err == nil {
		t.Fatal("missing fills cursor accepted")
	}
}

func TestConnectedArchiveSmokeRequiresFrozenSingleDayInputsBeforeNetwork(t *testing.T) {
	contract := filepath.Join("..", "..", "research", "firstbattle", "route_data_contracts.json")
	err := run([]string{"--mode", "archive-smoke", "--connect", "--contract", contract})
	if err == nil || !strings.Contains(err.Error(), "requires one --inst-type") {
		t.Fatalf("connected archive smoke was not fail-closed: %v", err)
	}
}

func TestConnectRejectsCustomRawRootBeforeNetwork(t *testing.T) {
	contract := filepath.Join("..", "..", "research", "firstbattle", "route_data_contracts.json")
	err := run([]string{
		"--mode", "archive-smoke", "--connect", "--contract", contract, "--raw-root", secureCLIRoot(t),
	})
	if err == nil || !strings.Contains(err.Error(), "canonical mode-specific default raw root") {
		t.Fatalf("custom connected root accepted: %v", err)
	}
}

func TestEventClockRangeIsEndpointSpecific(t *testing.T) {
	minTime, maxTime, err := eventClockRange(historydownload.OrdersEndpoint, []map[string]any{
		{"cTime": "500", "uTime": "2000"}, {"cTime": "750", "uTime": "1000"},
	})
	if err != nil || minTime.UnixMilli() != 500 || maxTime.UnixMilli() != 2000 {
		t.Fatalf("orders range=%v..%v err=%v", minTime, maxTime, err)
	}
	if _, _, err := eventClockRange(historydownload.FillsEndpoint, []map[string]any{{"uTime": "1000"}}); err == nil {
		t.Fatal("fills accepted wrong event clock")
	}
}

func TestPrivatePagesRequiresAndIsolatesInstrumentType(t *testing.T) {
	if err := downloadPrivatePages(context.Background(), &okxreadonly.Client{}, historydownload.OrdersEndpoint, map[string]string{}, secureCLIRoot(t), 1<<20, nil); err == nil {
		t.Fatal("missing instType accepted")
	}
	calls := 0
	client := &okxreadonly.Client{
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
		HTTP: &http.Client{Transport: cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			body := `{"code":"0","data":[]}`
			if calls == 1 {
				body = `{"code":"0","data":[{"ordId":"1","cTime":"1000","uTime":"2000"}]}`
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	root := secureCLIRoot(t)
	if err := downloadPrivatePages(context.Background(), client, historydownload.OrdersEndpoint, map[string]string{"instType": "SWAP"}, root, 1<<20, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, strings.TrimPrefix(historydownload.OrdersEndpoint, "/"), "SWAP", "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("instType-isolated manifest absent: %v", err)
	}
	var manifest historydownload.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ClockFitness != "ARCHIVE_ORIGINAL_RECEIVE_TIME_ABSENT" ||
		manifest.DenominatorFitness != "INCOMPLETE_API_SLICE" ||
		!containsWarning(manifest.Warnings, "denominator incomplete") {
		t.Fatalf("private history gaps not explicit: %#v", manifest)
	}
}

func TestPrivateResumeUsesRawAfterButPersistsOnlyTokens(t *testing.T) {
	root := secureCLIRoot(t)
	firstCalls := 0
	firstClient := &okxreadonly.Client{
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
		HTTP: &http.Client{Transport: cliRoundTripFunc(func(*http.Request) (*http.Response, error) {
			firstCalls++
			if firstCalls > 1 {
				return nil, errors.New("injected interruption containing 920002")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"0","data":[{"ordId":"920003","clOrdId":"client920003","cTime":"1000","uTime":"2000"},{"ordId":"920002","clOrdId":"client920002","cTime":"1100","uTime":"2100"}]}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	if err := downloadPrivatePages(context.Background(), firstClient, historydownload.OrdersEndpoint, map[string]string{"instType": "SWAP"}, root, 1<<20, nil); err == nil || strings.Contains(err.Error(), "920002") {
		t.Fatalf("first run did not fail safely: %v", err)
	}

	resumeCalls := 0
	resumeClient := &okxreadonly.Client{
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
		HTTP: &http.Client{Transport: cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			resumeCalls++
			body := `{"code":"0","data":[]}`
			if resumeCalls == 1 {
				if got := request.URL.Query().Get("after"); got != "920002" {
					t.Fatalf("resumed transport after=%q", got)
				}
				body = `{"code":"0","data":[{"ordId":"910003","clOrdId":"client910003","cTime":"1200","uTime":"2200"},{"ordId":"910002","clOrdId":"client910002","cTime":"1300","uTime":"2300"}]}`
			} else if got := request.URL.Query().Get("after"); got != "910002" {
				t.Fatalf("second transport after=%q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header),
			}, nil
		})},
	}
	if err := downloadPrivatePages(context.Background(), resumeClient, historydownload.OrdersEndpoint, map[string]string{"instType": "SWAP"}, root, 1<<20, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, strings.TrimPrefix(historydownload.OrdersEndpoint, "/"), "SWAP")
	for _, name := range []string{"checkpoint.json", "manifest.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"920003", "920002", "client920003", "client920002", "910003", "910002", "client910003", "client910002"} {
			if bytes.Contains(raw, []byte(id)) {
				t.Fatalf("%s leaked raw identifier", name)
			}
		}
	}
}

func containsWarning(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

func TestInstrumentSnapshotPublishesHashBoundManifest(t *testing.T) {
	now := time.Now().UTC()
	client := &okxreadonly.Client{HTTP: &http.Client{Transport: cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":"0","data":[{"instId":"BTC-USDT","instType":"SPOT","state":"live"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}}
	root := secureCLIRoot(t)
	if err := downloadInstrumentSnapshot(context.Background(), client, historydownload.PublicInstruments, false, map[string]string{"instType": "SPOT"}, root, 1<<20, nil); err != nil {
		t.Fatal(err)
	}
	current, manifest, err := historydownload.VerifyInstrumentCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PITStatus != historydownload.InstrumentPITStatus || manifest.ArtifactHashes["snapshot-receipt.json"] == "" ||
		manifest.ReceiveTimeRange.Min.IsZero() || manifest.RecordCount != 1 || current.Generation == "" {
		t.Fatalf("snapshot manifest not evidence-bound: %#v at %v", manifest, now)
	}
}

func TestArchiveSmokeExactDiscoveryDownloadAndAuthority(t *testing.T) {
	root := secureCLIRoot(t)
	request := archiveTestRequest("2026-01-01")
	client, downloads := archiveFakeClient(t, request, false, false)
	if err := downloadArchiveSmoke(context.Background(), client, request, root, 4<<20, nil); err != nil {
		t.Fatal(err)
	}
	current, manifest, err := okxarchive.VerifyArchiveCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if *downloads != 1 || current.Instrument != request.Instrument || current.UTCDate != request.UTCDate ||
		manifest.ClockFitness != "ARCHIVE_ORIGINAL_RECEIVE_TIME_ABSENT" ||
		manifest.PITStatus != "PUBLIC_ARCHIVE_SMOKE_ONLY/PIT_UNPROVEN" ||
		len(manifest.ArtifactHashes) != 4 ||
		!manifest.EventTimeRange.Min.Equal(manifest.EventTimeRange.Max) ||
		manifest.EventTimeRange.Min.UnixMilli() != request.BeginMS {
		t.Fatalf("archive authority mismatch: current=%#v manifest=%#v downloads=%d", current, manifest, *downloads)
	}
}

func TestArchiveSmokeRejectsAllswapWithoutDownloading(t *testing.T) {
	root := secureCLIRoot(t)
	request := archiveTestRequest("2026-01-01")
	client, downloads := archiveFakeClient(t, request, true, false)
	if err := downloadArchiveSmoke(context.Background(), client, request, root, 4<<20, nil); err == nil {
		t.Fatal("allswap discovery accepted")
	}
	if *downloads != 0 {
		t.Fatal("allswap rejection still downloaded ZIP")
	}
}

func TestArchiveSmokeRejectsDeclaredSizeBeforeDownloading(t *testing.T) {
	root := secureCLIRoot(t)
	request := archiveTestRequest("2026-01-01")
	client, downloads := archiveFakeClient(t, request, false, true)
	if err := downloadArchiveSmoke(context.Background(), client, request, root, 4<<20, nil); err == nil {
		t.Fatal("oversize discovery accepted")
	}
	if *downloads != 0 {
		t.Fatal("oversize rejection still downloaded ZIP")
	}
}

func TestArchiveSmokeRootCapChecksActualDiscoveryWrite(t *testing.T) {
	root := secureCLIRoot(t)
	request := archiveTestRequest("2026-01-01")
	request.ArchiveCap = 128
	client, downloads := archiveFakeClient(t, request, false, false)
	if err := downloadArchiveSmoke(context.Background(), client, request, root, 200, nil); err == nil {
		t.Fatal("actual discovery write exceeded root cap")
	}
	if *downloads != 0 {
		t.Fatal("root-cap failure still downloaded ZIP")
	}
}

func TestArchiveGenerationFaultsPreserveOldAuthority(t *testing.T) {
	root := secureCLIRoot(t)
	first := archiveTestRequest("2026-01-01")
	firstClient, _ := archiveFakeClient(t, first, false, false)
	if err := downloadArchiveSmoke(context.Background(), firstClient, first, root, 4<<20, nil); err != nil {
		t.Fatal(err)
	}
	old, _, err := okxarchive.VerifyArchiveCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	second := archiveTestRequest("2025-12-31")
	for _, step := range []string{
		"after_discovery", "after_zip", "after_extract",
		"after_receipt", "after_manifest", "after_generation", "before_current",
	} {
		t.Run(step, func(t *testing.T) {
			client, _ := archiveFakeClient(t, second, false, false)
			err := downloadArchiveSmoke(context.Background(), client, second, root, 4<<20, func(actual string) error {
				if actual == step {
					return errors.New("injected archive crash")
				}
				return nil
			})
			if err == nil {
				t.Fatal("archive fault did not interrupt publication")
			}
			current, _, verifyErr := okxarchive.VerifyArchiveCurrent(root)
			if verifyErr != nil {
				t.Fatal(verifyErr)
			}
			if current.Generation != old.Generation || current.ManifestHash != old.ManifestHash {
				t.Fatalf("old archive authority changed after %s", step)
			}
		})
	}
}

func TestArchiveCurrentRejectsTamperedDiscovery(t *testing.T) {
	root := secureCLIRoot(t)
	request := archiveTestRequest("2026-01-01")
	client, _ := archiveFakeClient(t, request, false, false)
	if err := downloadArchiveSmoke(context.Background(), client, request, root, 4<<20, nil); err != nil {
		t.Fatal(err)
	}
	current, _, err := okxarchive.VerifyArchiveCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "archive-smoke", current.Generation, "discovery.json")
	if err := os.WriteFile(path, []byte(`{"code":"0","data":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := okxarchive.VerifyArchiveCurrent(root); err == nil {
		t.Fatal("tampered discovery authority accepted")
	}
}

func TestArchiveRequestRejectsFutureAndMultiValueInputs(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if _, err := archiveRequest("1", "SWAP", "BTC-USDT-SWAP", "2026-07-13", 1<<20, now); err == nil {
		t.Fatal("T+1 date accepted")
	}
	if _, err := archiveRequest("1", "SWAP", "BTC-USDT-SWAP,ETH-USDT-SWAP", "2026-07-12", 1<<20, now); err == nil {
		t.Fatal("multi-instrument request accepted")
	}
	if _, err := archiveRequest("6", "SWAP", "BTC-USDT-SWAP", "2026-07-12", 1<<20, now); err == nil {
		t.Fatal("future module accepted")
	}
	if _, err := archiveRequest("4", "SWAP", "BTC-USDT-SWAP", "2026-07-12", 1<<20, now); err == nil {
		t.Fatal("unverified books module accepted")
	}
}

func archiveTestRequest(date string) okxarchive.DiscoveryRequest {
	parsed, _ := time.Parse("2006-01-02", date)
	return okxarchive.DiscoveryRequest{
		Module: "1", InstType: "SWAP", Instrument: "BTC-USDT-SWAP", UTCDate: date,
		BeginMS: parsed.UnixMilli(), EndMS: parsed.Add(24*time.Hour).UnixMilli() - 1, ArchiveCap: 1 << 20,
	}
}

func archiveFakeClient(
	t *testing.T,
	request okxarchive.DiscoveryRequest,
	allswap bool,
	oversize bool,
) (*okxreadonly.Client, *int) {
	t.Helper()
	filename := request.Instrument + "-trades-" + request.UTCDate + ".zip"
	size := "0.0001"
	if oversize {
		size = "100"
	}
	url := "https://static.okx.com/archive/" + filename + "?v=999"
	details := []any{map[string]any{
		"ccy": "", "dateRangeStart": strconv.FormatInt(request.BeginMS, 10),
		"dateRangeEnd": strconv.FormatInt(request.EndMS+1, 10), "groupSizeMB": size,
		"instFamily": "", "instId": request.Instrument, "instType": request.InstType,
		"groupDetails": []any{map[string]any{
			"dateTs": strconv.FormatInt(request.BeginMS, 10), "filename": filename, "sizeMB": size, "url": url,
		}},
	}}
	if allswap {
		extraDate := time.UnixMilli(request.BeginMS).Add(24 * time.Hour)
		extraFilename := "allswap-trades-" + extraDate.Format("2006-01-02") + ".zip"
		details = append(details, map[string]any{
			"ccy": "", "dateRangeStart": strconv.FormatInt(request.BeginMS, 10),
			"dateRangeEnd": strconv.FormatInt(request.EndMS+1, 10), "groupSizeMB": size,
			"instFamily": "", "instId": request.Instrument, "instType": request.InstType,
			"groupDetails": []any{map[string]any{
				"dateTs": strconv.FormatInt(extraDate.UnixMilli(), 10), "filename": extraFilename,
				"sizeMB": size, "url": "https://static.okx.com/archive/" + extraFilename + "?v=999",
			}},
		})
	}
	discovery := map[string]any{
		"code": "0", "msg": "", "data": []any{map[string]any{
			"dateAggrType": "daily", "details": details, "totalSizeMB": size,
			"ts": strconv.FormatInt(request.EndMS+1000, 10),
		}},
	}
	discoveryRaw, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	zipRaw := archiveZIPFixture(t, request.BeginMS)
	downloads := 0
	client := &okxreadonly.Client{HTTP: &http.Client{Transport: cliRoundTripFunc(func(requestHTTP *http.Request) (*http.Response, error) {
		if requestHTTP.Method != http.MethodGet {
			t.Fatalf("archive transport method=%s", requestHTTP.Method)
		}
		if requestHTTP.URL.Host == "www.okx.com" {
			query := requestHTTP.URL.Query()
			if len(query) != 6 || query.Get("module") != request.Module ||
				query.Get("instType") != request.InstType || query.Get("instIdList") != request.Instrument ||
				query.Get("dateAggrType") != "daily" ||
				query.Get("begin") != strconv.FormatInt(request.BeginMS, 10) ||
				query.Get("end") != strconv.FormatInt(request.EndMS, 10) {
				t.Fatalf("unexpected discovery query: %v", query)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(discoveryRaw)), Header: make(http.Header),
			}, nil
		}
		if requestHTTP.URL.Host == "static.okx.com" {
			if requestHTTP.URL.Query().Get("v") != "999" {
				t.Fatalf("numeric archive version query lost: %s", requestHTTP.URL.RawQuery)
			}
			downloads++
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(zipRaw)), Header: make(http.Header),
			}, nil
		}
		t.Fatalf("unexpected archive host %s", requestHTTP.URL.Host)
		return nil, nil
	})}}
	return client, &downloads
}

func archiveZIPFixture(t *testing.T, eventMS int64) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("trades.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(entry, "ts,px,sz,side\n%d,1,2,buy\n", eventMS); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type cliRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cliRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func secureCLIRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
