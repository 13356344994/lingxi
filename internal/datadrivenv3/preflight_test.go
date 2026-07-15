package datadrivenv3

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/datadrivenv3/fundingarchive"
)

func TestProtocolChain(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProtocolChain(root); err != nil {
		t.Fatalf("protocol chain drift: %v", err)
	}
}

func TestProfileIdentityIncludesProfile(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if EventID("okx_1h", "okx", "BTC-USDT-SWAP", at, "x", "v1") == EventID("other", "okx", "BTC-USDT-SWAP", at, "x", "v1") {
		t.Fatal("profile must participate in event identity")
	}
}

func TestPartitionAndGlobalPurge(t *testing.T) {
	b := [3]time.Time{time.Unix(0, 0), time.Unix(int64((100 * time.Hour).Seconds()), 0), time.Unix(int64((200 * time.Hour).Seconds()), 0)}
	if got := Partition(time.Unix(int64((50*time.Hour).Seconds()), 0), b); got != "validation" {
		t.Fatalf("partition=%s", got)
	}
	if !Purged(b[0].Add(24*time.Hour), b) {
		t.Fatal("24h boundary must purge")
	}
	if Purged(time.Unix(int64((50*time.Hour).Seconds()), 0), b) {
		t.Fatal("distant timestamp must not purge")
	}
}

func TestFundingCoverageUsesUTCDaysAndActualEvents(t *testing.T) {
	entry := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC).UnixMilli()
	exit := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC).UnixMilli()
	if !completeUTCDays(fundingarchive.Provenance{
		CompleteDays: map[string]bool{"2026-01-01": true, "2026-01-02": true},
		StartDate:    "2026-01-01",
		EndDate:      "2026-01-02",
	}, entry, exit) {
		t.Fatal("complete interval days must make funding knowable")
	}
	if completeUTCDays(fundingarchive.Provenance{
		CompleteDays: map[string]bool{"2026-01-01": true},
		StartDate:    "2026-01-01",
		EndDate:      "2026-01-01",
		GapDays:      1,
	}, entry, exit) {
		t.Fatal("missing interval day must be unknown")
	}
	if !fundingCrosses([]int64{entry + 1}, entry, exit) {
		t.Fatal("actual event must be observed")
	}
	if fundingCrosses([]int64{entry}, entry, exit) {
		t.Fatal("entry settlement is outside the open interval")
	}
}

func TestKnownCostCoverage(t *testing.T) {
	c := Coverage{Eligible: 100, KnownCost: 94}
	if c.KnownRatio() >= MinCostKnown {
		t.Fatal("coverage gate must reject below 95%")
	}
}

func TestFundingDiscoverySelectsOnlyExactModuleThreeAllswap(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	filename := "allswap-fundingrates-2026-01-01.zip"
	url := "https://static.okx.com/cdn/okex/traderecords/swaprates/daily/20260101/" + filename + "?v=999"
	raw, err := json.Marshal(map[string]any{"code": "0", "data": []any{map[string]any{
		"dateAggrType": "daily", "details": []any{map[string]any{"groupDetails": []any{map[string]any{
			"dateTs": "1767225600000", "filename": filename, "url": url,
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, absent, err := selectFundingArchive(raw, day)
	if err != nil || absent || got != url {
		t.Fatalf("selection=%q absent=%v err=%v", got, absent, err)
	}
	raw = bytes.Replace(raw, []byte("?v=999"), []byte("?v=2"), 1)
	if _, _, err := selectFundingArchive(raw, day); err == nil {
		t.Fatal("non-approved static URL accepted")
	}
}

func TestFundingZIPSchemaAndConflictsFailClosed(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	file, err := writer.Create("funding.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("instrument_name,funding_rate,funding_time\nBTC-USDT-SWAP,0.0001,1767225600000\nBTC-USDT-SWAP,0.0001,1767225600000\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	records, unknownRows, err := decodeFundingZIP(raw.Bytes(), day)
	if err != nil || unknownRows != 0 || len(records) != 1 || records[0].FundingRate != "0.0001" {
		t.Fatalf("records=%#v unknown=%d err=%v", records, unknownRows, err)
	}
	if err := addFunding(map[int64]string{1: "0.1"}, 1, "0.2"); err == nil {
		t.Fatal("conflicting duplicate funding accepted")
	}
}
