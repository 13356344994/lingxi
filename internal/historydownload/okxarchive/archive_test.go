package okxarchive

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsUnapprovedHosts(t *testing.T) {
	for _, raw := range []string{"http://static.okx.com/a.zip", "https://evil.example/a.zip", "https://static.okx.com/a.csv"} {
		if _, err := ValidateURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestDownloadZIPStreamsGETAndEnforcesCap(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		cap        int64
		wantErr    bool
		wantExists bool
	}{
		{name: "success", status: http.StatusOK, body: "zip", cap: 3, wantExists: true},
		{name: "over-cap", status: http.StatusOK, body: "toolarge", cap: 3, wantErr: true},
		{name: "non-2xx", status: http.StatusBadGateway, body: "error", cap: 10, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method := ""
			client := &http.Client{Transport: archiveRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				method = request.Method
				return &http.Response{
					StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header),
				}, nil
			})}
			path := filepath.Join(t.TempDir(), "archive.zip")
			_, err := DownloadZIP(context.Background(), client, "https://static.okx.com/archive.zip", path, test.cap)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
			if method != http.MethodGet {
				t.Fatalf("method=%q", method)
			}
			_, statErr := os.Stat(path)
			if (statErr == nil) != test.wantExists {
				t.Fatalf("exists=%v want=%v", statErr == nil, test.wantExists)
			}
			matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp-*"))
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("temporary files leaked: %#v err=%v", matches, globErr)
			}
		})
	}
}

type archiveRoundTripFunc func(*http.Request) (*http.Response, error)

func (f archiveRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestExtractRejectsZipSlip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, _ := w.Create("../escape.csv")
	_, _ = entry.Write([]byte("x\n"))
	_ = w.Close()
	_ = f.Close()
	if _, err := ExtractCSV(path, t.TempDir(), 100); err == nil {
		t.Fatal("expected zip-slip rejection")
	}
}

func TestExtractSecondFileFailureRollsBackWholeArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	first, err := writer.Create("first.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("ts,px\n1,2\n")); err != nil {
		t.Fatal(err)
	}
	second, err := writer.Create("second.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write([]byte("invalid")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "published")
	if _, err := ExtractCSV(path, destination, 1<<20); err == nil {
		t.Fatal("invalid second file accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial extraction published: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(filepath.Dir(destination), filepath.Base(destination)+".staging-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging extraction leaked: %#v err=%v", staging, err)
	}
}

func TestSmokeReceiptRequiresData(t *testing.T) {
	csv := filepath.Join(t.TempDir(), "trades.csv")
	if err := os.WriteFile(csv, []byte("ts,px,sz,side\n1767225600000,1,2,buy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := DiscoveryRequest{
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
		BeginMS: 1767225600000, EndMS: 1767311999999, ArchiveCap: 1 << 20,
	}
	selected := SelectedArchive{
		Filename: "BTC-USDT-trades-2026-01-01.zip", URLHash: "url-hash", DeclaredBytes: 1024,
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
	}
	receipt, err := BuildSmokeReceipt(csv, "zip-hash", request, selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySmokeReceipt(receipt, request, selected); err != nil {
		t.Fatal(err)
	}
	if !receipt.EventTimeMin.Equal(receipt.EventTimeMax) || receipt.EventTimeMin.UnixMilli() != request.BeginMS {
		t.Fatalf("single-row event range was expanded: %#v", receipt)
	}
	if err := VerifySmokeReceipt(SmokeReceipt{}, request, selected); err == nil {
		t.Fatal("empty receipt accepted")
	}
}

func TestSmokeReceiptComputesActualRangeFromUnorderedRows(t *testing.T) {
	request := DiscoveryRequest{
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
		BeginMS: 1767225600000, EndMS: 1767311999999, ArchiveCap: 1 << 20,
	}
	selected := SelectedArchive{
		Filename: "BTC-USDT-trades-2026-01-01.zip", URLHash: "url-hash", DeclaredBytes: 1024,
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
	}
	path := filepath.Join(t.TempDir(), "trades.csv")
	if err := os.WriteFile(path, []byte(
		"ts,px,sz,side\n1767225605000,1,2,buy\n1767225601000,1,2,sell\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := BuildSmokeReceipt(path, "zip-hash", request, selected)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EventTimeMin.UnixMilli() != 1767225601000 || receipt.EventTimeMax.UnixMilli() != 1767225605000 {
		t.Fatalf("unordered event range incorrect: %#v", receipt)
	}
}

func TestSmokeReceiptRejectsWrongDayAndSchema(t *testing.T) {
	request := DiscoveryRequest{
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
		BeginMS: 1767225600000, EndMS: 1767311999999, ArchiveCap: 1 << 20,
	}
	selected := SelectedArchive{
		Filename: "BTC-USDT-trades-2026-01-01.zip", URLHash: "url-hash", DeclaredBytes: 1024,
		Module: "1", InstType: "SPOT", Instrument: "BTC-USDT", UTCDate: "2026-01-01",
	}
	for name, content := range map[string]string{
		"wrong-day":     "ts,px,sz,side\n1767312000000,1,2,buy\n",
		"missing-side":  "ts,px,sz\n1767225600000,1,2\n",
		"missing-clock": "px,sz,side\n1,2,buy\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.csv")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildSmokeReceipt(path, "zip-hash", request, selected); err == nil {
				t.Fatal("invalid CSV accepted")
			}
		})
	}
}
