package announcements

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestParseOKXRawBody(t *testing.T) {
	raw, err := os.ReadFile("testdata/okx_raw.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// JSONL: take first line only.
	if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
		raw = raw[:idx]
	}
	line, err := ParseArchiveLine(raw)
	if err != nil {
		t.Fatal(err)
	}
	items, err := AdaptArchiveLine(line, VenueOKX)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 1 {
		t.Fatalf("expected at least 1 announcement, got %d", len(items))
	}
	found := false
	for _, a := range items {
		if a.Symbol == "PANW-USDT" || a.Symbol == "PANW" {
			found = true
			if a.Kind != KindDelisting {
				t.Fatalf("expected delisting, got %q", a.Kind)
			}
			if a.ExecutionVenue != ExecutionVenue {
				t.Fatalf("execution venue must be OKX")
			}
			if a.InfoOnly {
				t.Fatal("OKX announcement must not be info-only")
			}
		}
	}
	if !found {
		t.Fatalf("unexpected symbols: %+v", items)
	}
}

func TestParseNormalizedBinance(t *testing.T) {
	line := ArchiveLine{
		Venue:       VenueBinance,
		Format:      "normalized_v1",
		PublishedAt: time.Date(2024, 5, 20, 12, 0, 0, 0, time.UTC),
		FirstSeenAt: time.Date(2024, 5, 20, 13, 0, 0, 0, time.UTC),
		SourceHash:  "binance_list_eth_v1",
		SourceURL:   "https://www.binance.com/en/support/announcement/list-eth",
		Title:       "Binance Will List ETH USDT Perpetual",
		Symbols:     []string{"ETH-USDT"},
		Kind:        KindListing,
	}
	items, err := AdaptArchiveLine(line, VenueBinance)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatal("expected one item")
	}
	if !items[0].InfoOnly {
		t.Fatal("binance must be info-only")
	}
}

func TestParseBinanceBodyArray(t *testing.T) {
	body := `[{"title":"Binance Will Delist ABC-USDT","code":"abc","publishDate":1717228800000}]`
	line := ArchiveLine{
		Venue:     VenueBinance,
		Body:      json.RawMessage(body),
		BodyHash:  "binance_body_1",
		FetchedAt: time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC),
	}
	items, err := binanceAdapter{}.Adapt(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != KindDelisting {
		t.Fatalf("unexpected parse: %+v", items)
	}
}

func TestParseBybitBody(t *testing.T) {
	body := `{"result":{"list":[{"title":"Bybit to suspend XYZ-USDT","url":"https://bybit.com/x","publishTime":1717228800000}]}}`
	line := ArchiveLine{
		Venue:     VenueBybit,
		Body:      json.RawMessage(body),
		BodyHash:  "bybit_body_1",
		FetchedAt: time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC),
	}
	items, err := bybitAdapter{}.Adapt(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != KindSuspension {
		t.Fatalf("unexpected parse: %+v", items)
	}
}

func TestParseGateBody(t *testing.T) {
	body := `{"list":[{"title":"Gate will list QQQ-USDT","url":"https://gate.io/x","published_at":1717228800}]}`
	line := ArchiveLine{
		Venue:     VenueGate,
		Body:      json.RawMessage(body),
		BodyHash:  "gate_body_1",
		FetchedAt: time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC),
	}
	items, err := gateAdapter{}.Adapt(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != KindListing {
		t.Fatalf("unexpected parse: %+v", items)
	}
}