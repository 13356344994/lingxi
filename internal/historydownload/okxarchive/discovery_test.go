package okxarchive

import (
	"encoding/json"
	"testing"
)

func TestSelectArchiveRequiresUniqueExactBoundEntry(t *testing.T) {
	request := archiveDiscoveryRequest()
	entry := archiveDiscoveryEntry(
		"BTC-USDT-SWAP", "SWAP", "1767225600000",
		"BTC-USDT-SWAP-trades-2026-01-01.zip",
		"https://static.okx.com/cdn/BTC-USDT-SWAP-trades-2026-01-01.zip?v=999", "0.0001",
	)
	selected, err := SelectArchive(discoveryEnvelope(entry), request)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Instrument != request.Instrument || selected.URLHash == "" || selected.DeclaredBytes <= 0 {
		t.Fatalf("selection not provenance-bound: %#v", selected)
	}
	unknown := archiveDiscoveryEntry(
		"BTC-USDT-SWAP", "SWAP", "1767225600000",
		"BTC-USDT-SWAP-trades-2026-01-01.zip",
		"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
	)
	unknown["futureField"] = "unexpected"

	cases := map[string][]map[string]any{
		"empty":             {},
		"unknown-structure": {unknown},
		"allswap": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "allswap-trades-2026-01-01.zip",
			"https://static.okx.com/allswap-trades-2026-01-01.zip", "0.0001",
		)},
		"duplicate": {entry, entry},
		"exact-plus-extra-day": {entry, archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767312000000", "BTC-USDT-SWAP-trades-2026-01-02.zip",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-02.zip", "0.0001",
		)},
		"exact-plus-extra-inst": {entry, archiveDiscoveryEntry(
			"ETH-USDT-SWAP", "SWAP", "1767225600000", "ETH-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/ETH-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
		)},
		"allswap-dual-day": {entry, archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767312000000", "allswap-trades-2026-01-02.zip",
			"https://static.okx.com/allswap-trades-2026-01-02.zip", "0.0001",
		)},
		"wrong-date": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767312000000", "BTC-USDT-SWAP-trades-2026-01-02.zip",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-02.zip", "0.0001",
		)},
		"wrong-inst": {archiveDiscoveryEntry(
			"ETH-USDT-SWAP", "SWAP", "1767225600000", "ETH-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/ETH-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
		)},
		"filename-inst": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "ETH-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/ETH-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
		)},
		"url-filename": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/different-trades-2026-01-01.zip", "0.0001",
		)},
		"wrong-host": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"https://example.com/BTC-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
		)},
		"http": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"http://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
		)},
		"non-zip": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.csv",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.csv", "0.0001",
		)},
		"size": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip", "100",
		)},
		"bad-query-key": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip?redirect=1", "0.0001",
		)},
		"bad-query-value": {archiveDiscoveryEntry(
			"BTC-USDT-SWAP", "SWAP", "1767225600000", "BTC-USDT-SWAP-trades-2026-01-01.zip",
			"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip?v=not-numeric", "0.0001",
		)},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SelectArchive(discoveryEnvelope(entries...), request); err == nil {
				t.Fatal("unsafe discovery response accepted")
			}
		})
	}
}

func TestSelectArchiveRejectsExtraDataGroup(t *testing.T) {
	request := archiveDiscoveryRequest()
	entry := archiveDiscoveryEntry(
		request.Instrument, request.InstType, "1767225600000",
		"BTC-USDT-SWAP-trades-2026-01-01.zip",
		"https://static.okx.com/BTC-USDT-SWAP-trades-2026-01-01.zip", "0.0001",
	)
	raw, _ := json.Marshal(map[string]any{
		"code": "0", "data": []any{
			map[string]any{"details": []any{entry}},
			map[string]any{"details": []any{entry}},
		},
	})
	if _, err := SelectArchive(raw, request); err == nil {
		t.Fatal("extra data group accepted")
	}
}

func TestSelectArchiveRejectsFutureModuleExtension(t *testing.T) {
	request := archiveDiscoveryRequest()
	request.Module = "6"
	if _, err := SelectArchive(discoveryEnvelope(), request); err == nil {
		t.Fatal("unapproved module accepted")
	}
}

func TestSelectArchiveRejectsBooksUntilNewGeneration(t *testing.T) {
	for module, marker := range map[string]string{"4": "400", "5": "5000"} {
		t.Run(module, func(t *testing.T) {
			request := archiveDiscoveryRequest()
			request.Module = module
			filename := "BTC-USDT-SWAP-books-" + marker + "-2026-01-01.zip"
			entry := archiveDiscoveryEntry(
				request.Instrument, request.InstType, "1767225600000", filename,
				"https://static.okx.com/"+filename, "0.0001",
			)
			if _, err := SelectArchive(discoveryEnvelope(entry), request); err == nil {
				t.Fatal("unverified book product accepted")
			}
		})
	}
}

func archiveDiscoveryRequest() DiscoveryRequest {
	return DiscoveryRequest{
		Module: "1", InstType: "SWAP", Instrument: "BTC-USDT-SWAP", UTCDate: "2026-01-01",
		BeginMS: 1767225600000, EndMS: 1767311999999, ArchiveCap: 1 << 20,
	}
}

func archiveDiscoveryEntry(instID, instType, date, filename, rawURL, size string) map[string]any {
	return map[string]any{
		"ccy": "", "dateRangeStart": "1767225600000", "dateRangeEnd": "1767312000000",
		"groupSizeMB": size, "instFamily": "", "instId": instID, "instType": instType,
		"groupDetails": []any{map[string]any{
			"dateTs": date, "filename": filename, "url": rawURL, "sizeMB": size,
		}},
	}
}

func discoveryEnvelope(entries ...map[string]any) []byte {
	details := make([]any, 0, len(entries))
	for _, entry := range entries {
		details = append(details, entry)
	}
	raw, _ := json.Marshal(map[string]any{
		"code": "0", "msg": "", "data": []any{map[string]any{
			"dateAggrType": "daily", "details": details, "totalSizeMB": "0.0001", "ts": "1767312001000",
		}},
	})
	return raw
}
