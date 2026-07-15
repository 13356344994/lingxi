package fundingarchive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

func TestPlanAndQueryBindModuleAndDate(t *testing.T) {
	d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := BuildPlan(d, d.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Module != "3" || len(plan.Dates) != 3 || plan.Dates[0] != "2024-01-01" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	q := discoveryQuery(d)
	if q.Get("module") != "3" || q.Get("dateAggrType") != "daily" || q.Get("begin") != "1704067200000" || q.Get("end") != "1704153599999" {
		t.Fatalf("unexpected query: %v", q)
	}
}

func TestDiscoverySelectsOnlyRequestedDate(t *testing.T) {
	body := []byte(`{"code":"0","data":[{"details":[{"groupDetails":[
{"filename":"allswap-fundingrates-2024-01-02.zip","url":"https://static.okx.com/cdn/okex/traderecords/swaprates/daily/20240102/allswap-fundingrates-2024-01-02.zip?v=999"},
{"filename":"allswap-fundingrates-2024-01-01.zip","url":"https://static.okx.com/cdn/okex/traderecords/swaprates/daily/20240101/allswap-fundingrates-2024-01-01.zip?v=999"}]}]}]}`)
	d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := ParseDiscovery(body, d)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Filename != "allswap-fundingrates-2024-01-01.zip" {
		t.Fatalf("selected %+v", got)
	}
}

func TestZIPSchemaDedupAndConflict(t *testing.T) {
	d := "2024-01-01"
	file := &discoveryFile{Filename: "allswap-fundingrates-" + d + ".zip", URL: "https://static.okx.com/cdn/okex/traderecords/swaprates/daily/20240101/allswap-fundingrates-2024-01-01.zip?v=999"}
	symbols := map[string]struct{}{"BTC-USDT-SWAP": {}}
	good := makeZIP(t, strings.Join([]string{
		CSVHeader,
		"BTC-USDT-SWAP,0.0001,1704067200000",
		"BTC-USDT-SWAP,0.0001,1704067200000",
		"NOT-CATALOG-SWAP,0.1,1704067200000",
		"",
	}, "\n"))
	result, body, err := verifyDay(d, file, good, time.Unix(1, 0), time.Unix(2, 0), symbols)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 1 || bytes.Count(body, []byte{'\n'}) != 1 {
		t.Fatalf("result=%+v body=%s", result, body)
	}
	withUnknown := makeZIP(t, CSVHeader+"\nBTC-USDT-SWAP,None,1704067200000\n")
	unknownResult, _, err := verifyDay(d, file, withUnknown, time.Time{}, time.Time{}, symbols)
	if err != nil || unknownResult.Rows != 0 || unknownResult.UnknownRows != 1 {
		t.Fatalf("unknown signed rate handling: %+v %v", unknownResult, err)
	}
	conflict := makeZIP(t, CSVHeader+"\nBTC-USDT-SWAP,0.0001,1704067200000\nBTC-USDT-SWAP,0.0002,1704067200000\n")
	if _, _, err := verifyDay(d, file, conflict, time.Time{}, time.Time{}, symbols); err == nil {
		t.Fatal("expected duplicate conflict")
	}
}

func TestZIPHeaderAndCRC(t *testing.T) {
	d := "2024-01-01"
	file := &discoveryFile{Filename: "allswap-fundingrates-" + d + ".zip", URL: "https://static.okx.com/cdn/okex/traderecords/swaprates/daily/20240101/allswap-fundingrates-2024-01-01.zip?v=999"}
	symbols := map[string]struct{}{"BTC-USDT-SWAP": {}}
	badHeader := makeZIP(t, "instrument_name,rate,funding_time\n")
	if _, _, err := verifyDay(d, file, badHeader, time.Time{}, time.Time{}, symbols); err == nil {
		t.Fatal("expected header rejection")
	}
	raw := makeZIP(t, CSVHeader+"\nBTC-USDT-SWAP,0.1,1704067200000\n")
	needle := []byte("BTC-USDT-SWAP")
	at := bytes.Index(raw, needle)
	if at < 0 {
		t.Fatal("stored ZIP body not found")
	}
	raw[at] = 'X'
	if _, _, err := verifyDay(d, file, raw, time.Time{}, time.Time{}, symbols); err == nil {
		t.Fatal("expected CRC rejection")
	}
}

func TestCheckpointTamperAndVerifiedManifest(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "records/history/okx_funding_v3")
	if err := os.MkdirAll(filepath.Join(base, "daily"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "event", InstID: "BTC-USDT-SWAP", FundingRate: "0.1", FundingTime: 1, SourceID: SourceID, SourceURLHash: "url", DailyRawHash: "raw"}
	line, _ := json.Marshal(event)
	line = append(line, '\n')
	if err := os.WriteFile(filepath.Join(base, "daily/2024-01-01.jsonl"), line, 0o644); err != nil {
		t.Fatal(err)
	}
	result := DailyResult{Date: "2024-01-01", Status: "COMPLETE", RawHash: "raw", OutputHash: hashBytes(line), Rows: 1}
	manifest := Manifest{SchemaVersion: "lingxi.okx-funding-archive-manifest.v1", PlanHash: "plan", SourceID: SourceID, Header: CSVHeader, DateCount: 1, CompleteDays: 1, Rows: 1, Days: []DailyResult{result}}
	copy := manifest
	manifest.ManifestHash, _, _ = datatruth.HashJSON(copy)
	if err := writeAtomicJSON(filepath.Join(base, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{"schema_version": "lingxi.okx-funding-archive-receipt.v1", "manifest_hash": manifest.ManifestHash, "plan_hash": manifest.PlanHash, "complete_days": 1, "gap_days": 0, "rows": 1}
	if err := writeAtomicJSON(filepath.Join(base, "receipt.json"), receipt); err != nil {
		t.Fatal(err)
	}
	got, err := LoadVerified(root)
	if err != nil || got["BTC-USDT-SWAP"][1] != "0.1" {
		t.Fatalf("load: %v %+v", err, got)
	}
	if err := os.WriteFile(filepath.Join(base, "daily/2024-01-01.jsonl"), []byte("tamper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerified(root); err == nil {
		t.Fatal("expected tamper rejection")
	}
}

func makeZIP(t *testing.T, body string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	header := &zip.FileHeader{Name: "allswap-fundingrates-2024-01-01.csv", Method: zip.Store}
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
