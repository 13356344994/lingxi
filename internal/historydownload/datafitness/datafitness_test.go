package datafitness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/historydownload"
)

func TestMeasureProjectionCases(t *testing.T) {
	t.Run("no conflict and multiple fills", func(t *testing.T) {
		cfg := fixture(t,
			[]map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")},
			[]map[string]any{fillRow("o1", "t1", "BTC-USDT-SWAP", "1000"), fillRow("o1", "t2", "BTC-USDT-SWAP", "1001")},
			[]string{"BTC-USDT-SWAP"})
		got, err := Measure(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.IdentityConflicts.OrderCount != 0 || got.Counts.MatchedOrderTokens != 1 || got.Counts.FillsPerOrder["2"] != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("cross endpoint drift", func(t *testing.T) {
		cfg := fixture(t, []map[string]any{orderRow("o1", "DATA-USDT-SWAP-OFF", "1", "1000")},
			[]map[string]any{fillRow("o1", "t1", "IP-USDT-SWAP", "1000"), fillRow("o1", "t2", "IP-USDT-SWAP", "1001")}, []string{"IP-USDT-SWAP"})
		got, err := Measure(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.IdentityConflicts.OrderCount != 1 || got.IdentityConflicts.FillRowCount != 2 || got.Counts.UnionSnapshotMissing != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("unmatched", func(t *testing.T) {
		cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")},
			[]map[string]any{fillRow("missing", "t1", "BTC-USDT-SWAP", "1000")}, []string{"BTC-USDT-SWAP"})
		got, err := Measure(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Counts.UnmatchedFillOrderTokens != 1 {
			t.Fatalf("%+v", got.Counts)
		}
	})
	t.Run("duplicate token rejected", func(t *testing.T) {
		cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000"), orderRow("o1", "BTC-USDT-SWAP", "1", "1001")}, nil, nil)
		if _, err := Measure(cfg); err == nil {
			t.Fatal("duplicate accepted")
		}
	})
	t.Run("outcome field rejected", func(t *testing.T) {
		row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000")
		row["outcome"] = "injected"
		cfg := fixture(t, []map[string]any{row}, nil, nil)
		if _, err := Measure(cfg); err == nil {
			t.Fatal("outcome accepted")
		}
	})
	t.Run("snapshot missing and day share", func(t *testing.T) {
		cfg := fixture(t,
			[]map[string]any{orderRow("o1", "OLD-USDT-SWAP", "0", "1000"), orderRow("o2", "BTC-USDT-SWAP", "1", "86400000"), orderRow("o3", "BTC-USDT-SWAP", "1", "172800000")},
			[]map[string]any{fillRow("o2", "t2", "BTC-USDT-SWAP", "86400000")}, []string{"BTC-USDT-SWAP"})
		got, err := Measure(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Counts.NoFillOrders != 1 || got.Counts.UnionSnapshotMissing != 1 || got.Counts.UTCOrderDays != 3 || got.Counts.TopTwoOrderDaySharePct != 66.66666666666667 {
			t.Fatalf("%+v", got.Counts)
		}
	})
}

func TestMeasureRejectsTamperedInstrumentArtifact(t *testing.T) {
	cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, nil, []string{"BTC-USDT-SWAP"})
	currentRaw, err := os.ReadFile(filepath.Join(cfg.PublicDataRoot, "instruments", "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	var current historydownload.InstrumentCurrent
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.PublicDataRoot, "instruments", current.Generation, "snapshot-raw.json"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Measure(cfg); err == nil {
		t.Fatal("tampered public artifact accepted")
	}
}

func TestOutcomeFirewallCoversFrozenContractClasses(t *testing.T) {
	keys := []string{
		"future_return", "return", "pnl", "fillPnl", "realized_pnl", "realized_usdt",
		"profit", "winner", "future_outcome", "mfe", "mae", "upnl_after_decision", "outcome",
		"Future-Return", "REALIZED.PNL", "upnlAfterDecision", "FILL_PNL",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if !forbiddenKey(key) {
				t.Fatalf("accepted forbidden schema field %q", key)
			}
			row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000")
			row[key] = "forbidden"
			if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
				t.Fatalf("accepted forbidden key %q", key)
			}
		})
	}
	t.Run("nested outcome key", func(t *testing.T) {
		row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000")
		row["meta"] = map[string]any{"pnl": "forbidden"}
		if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
			t.Fatal("accepted nested outcome key")
		}
	})
}

func TestProjectionSchemaAndScalarValidation(t *testing.T) {
	tests := []struct {
		name  string
		value any
		key   string
	}{
		{"extra harmless key", "x", "harmless"},
		{"map value", map[string]any{"x": "y"}, "category"},
		{"array value", []any{"x"}, "category"},
		{"null value", nil, "category"},
		{"number value", 1, "category"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000")
			row[tc.key] = tc.value
			if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
				t.Fatalf("accepted invalid projection value %#v", tc.value)
			}
		})
	}
	t.Run("manifest schema hash drift", func(t *testing.T) {
		cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, nil, nil)
		path := filepath.Join(cfg.OrdersRoot, "manifest.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest historydownload.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.ProjectionSchemaHash = "drift"
		manifest, err = historydownload.SealManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(manifest)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Measure(cfg); err == nil {
			t.Fatal("accepted manifest schema hash drift")
		}
	})
}

func TestStrictProjectionDecimals(t *testing.T) {
	for _, value := range []string{"", "-1", "NaN", "abc", "1/2", "1e2", "."} {
		t.Run("accFillSz="+value, func(t *testing.T) {
			row := orderRow("o1", "BTC-USDT-SWAP", value, "1000")
			if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
				t.Fatalf("accepted invalid accFillSz %q", value)
			}
		})
	}
	t.Run("zero accFillSz is no-fill", func(t *testing.T) {
		got, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "0", "1000")}, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		if got.Counts.NoFillOrders != 1 || got.Counts.OrdersAccFillPositive != 0 {
			t.Fatalf("bad zero classification: %+v", got.Counts)
		}
	})
	for _, field := range []string{"sz", "px"} {
		t.Run("order "+field, func(t *testing.T) {
			row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000")
			row[field] = "not-a-number"
			if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
				t.Fatalf("accepted invalid %s", field)
			}
		})
	}
	for _, field := range []string{"fillPx", "fillSz"} {
		t.Run("fill "+field, func(t *testing.T) {
			fill := fillRow("o1", "t1", "BTC-USDT-SWAP", "1000")
			fill[field] = "0"
			if _, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, []map[string]any{fill}, nil)); err == nil {
				t.Fatalf("accepted non-positive %s", field)
			}
		})
	}
}

func TestProjectionEvidenceDeterministicAndMutationSensitive(t *testing.T) {
	cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")},
		[]map[string]any{fillRow("o1", "t1", "BTC-USDT-SWAP", "1000")}, []string{"BTC-USDT-SWAP"})
	first, err := Measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptHash != again.ReceiptHash || first.ProjectionEvidence.Orders.AggregateHash != again.ProjectionEvidence.Orders.AggregateHash {
		t.Fatal("identical input produced unstable receipt")
	}
	path := filepath.Join(cfg.OrdersRoot, "projection", "page-000001.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ReceiptHash == first.ReceiptHash || changed.ProjectionEvidence.Orders.AggregateHash == first.ProjectionEvidence.Orders.AggregateHash {
		t.Fatal("projection mutation did not change evidence and receipt hashes")
	}
}

func TestReceiptHashSelfExclusion(t *testing.T) {
	got, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	want, err := ReceiptFingerprint(got)
	if err != nil || want != got.ReceiptHash {
		t.Fatalf("receipt hash mismatch: %s %s %v", got.ReceiptHash, want, err)
	}
	got.ReceiptHash = "ignored"
	again, _ := ReceiptFingerprint(got)
	if again != want {
		t.Fatal("receipt hash was not self-excluding")
	}
}

func TestProjectionCompletenessAndIdentityGates(t *testing.T) {
	t.Run("trailing JSON", func(t *testing.T) {
		cfg := fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, nil, nil)
		path := filepath.Join(cfg.OrdersRoot, "projection", "page-000001.json")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(`{}`)
		_ = f.Close()
		if _, err := Measure(cfg); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	})
	t.Run("invalid timestamp", func(t *testing.T) {
		row := orderRow("o1", "BTC-USDT-SWAP", "1", "1000x")
		if _, err := Measure(fixture(t, []map[string]any{row}, nil, nil)); err == nil {
			t.Fatal("invalid timestamp accepted")
		}
	})
	t.Run("duplicate trade token", func(t *testing.T) {
		fills := []map[string]any{fillRow("o1", "t1", "BTC-USDT-SWAP", "1000"), fillRow("o1", "t1", "BTC-USDT-SWAP", "1001")}
		if _, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, fills, nil)); err == nil {
			t.Fatal("duplicate trade token accepted")
		}
	})
	t.Run("duplicate bill token", func(t *testing.T) {
		first := fillRow("o1", "t1", "BTC-USDT-SWAP", "1000")
		second := fillRow("o1", "t2", "BTC-USDT-SWAP", "1001")
		second["billIdToken"] = first["billIdToken"]
		if _, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, []map[string]any{first, second}, nil)); err == nil {
			t.Fatal("duplicate bill token accepted")
		}
	})
	t.Run("missing bill token", func(t *testing.T) {
		fill := fillRow("o1", "t1", "BTC-USDT-SWAP", "1000")
		delete(fill, "billIdToken")
		if _, err := Measure(fixture(t, []map[string]any{orderRow("o1", "BTC-USDT-SWAP", "1", "1000")}, []map[string]any{fill}, nil)); err == nil {
			t.Fatal("missing bill token accepted")
		}
	})
}

func fixture(t *testing.T, orders, fills []map[string]any, instruments []string) Config {
	t.Helper()
	privateRoot, publicRoot := t.TempDir(), t.TempDir()
	if err := os.Chmod(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	makePrivate := func(endpoint string, rows []map[string]any) string {
		root := filepath.Join(privateRoot, endpoint)
		if err := os.MkdirAll(filepath.Join(root, "projection"), 0o700); err != nil {
			t.Fatal(err)
		}
		page, _ := json.Marshal(rows)
		if err := os.WriteFile(filepath.Join(root, "projection", "page-000001.json"), page, 0o644); err != nil {
			t.Fatal(err)
		}
		fields, schemaHash, err := historydownload.ProjectionSchema(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		m, err := historydownload.SealManifest(historydownload.Manifest{
			Endpoint: endpoint, Private: true, QueryHash: "fixture-query",
			PageRawHashes: []string{"projection-page-not-raw"}, RecordCount: len(rows),
			ProjectionFields: fields, ProjectionSchemaHash: schemaHash,
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	records := make([]map[string]any, 0, len(instruments))
	for _, id := range instruments {
		records = append(records, map[string]any{
			"instId": id, "instType": "SWAP", "ctType": "linear", "minSz": "1",
			"ctVal": "1", "ctValCcy": "USDT", "lotSz": "1", "tickSz": "0.1",
			"state": "live", "listTime": "1",
		})
	}
	at := time.Unix(10, 0).UTC()
	receipt, err := historydownload.BuildInstrumentSnapshot(historydownload.PublicInstruments, records, at)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := historydownload.AcquireRootLock(publicRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := historydownload.PublishInstrumentGeneration(publicRoot, 1<<20, lock, []byte(`{"data":[]}`), receipt,
		historydownload.Manifest{Endpoint: historydownload.PublicInstruments, RecordCount: len(records), ProjectionFields: receipt.Projection.Fields, ProjectionSchemaHash: receipt.Projection.SchemaHash, PresenceCounts: receipt.Projection.PresenceCounts, Clocks: historydownload.Clock{ReceiveTime: at}, ReceiveTimeRange: historydownload.ClockRange{Min: at, Max: at}}, nil); err != nil {
		t.Fatal(err)
	}
	return Config{OrdersRoot: makePrivate(historydownload.OrdersEndpoint, orders), FillsRoot: makePrivate(historydownload.FillsEndpoint, fills), PublicDataRoot: publicRoot}
}

func orderRow(token, inst, fill, ms string) map[string]any {
	return map[string]any{
		"accFillSz": fill, "cTime": ms, "category": "normal", "fee": "0", "feeCcy": "USDT",
		"instId": inst, "instType": "SWAP", "ordIdToken": token, "ordType": "ioc",
		"posSide": "net", "px": "1", "side": "buy", "source": "", "state": "filled",
		"sz": "1", "uTime": ms,
	}
}
func fillRow(orderToken, tradeToken, inst, ms string) map[string]any {
	return map[string]any{
		"ordIdToken": orderToken, "tradeIdToken": tradeToken, "billIdToken": tradeToken + "b",
		"instId": inst, "instType": "SWAP", "execType": "T", "fillTime": ms, "ts": ms,
		"fee": "0", "feeCcy": "USDT", "fillPx": "1", "fillSz": "1",
		"posSide": "net", "side": "buy",
	}
}
