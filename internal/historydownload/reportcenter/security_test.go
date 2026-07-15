package reportcenter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutcomeHeaderVariantsCannotBeMapped(t *testing.T) {
	variants := []string{
		"Realized PnL", "fill_pnl", "futureReturn", "unrealizedPnl",
		"P&L", "Closed P-L", "ROI", "Net Income", "Gain", "Loss",
	}
	for _, header := range variants {
		header := header
		t.Run(canonicalHeader(header), func(t *testing.T) {
			t.Run("cancelReason", func(t *testing.T) {
				columns := orderColumns()
				columns["cancelReason"] = header
				root, csvPath, mapping := fixture(t, OrderTemplate,
					"Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated,"+header+"\n"+
						"one,X,SWAP,canceled,limit,buy,1,1,1700000000000,1700000000001,secret\n", columns)
				assertRejectedWithoutCurrent(t, root, csvPath, mapping)
			})
			t.Run("fee", func(t *testing.T) {
				columns := tradingColumns("Trade")
				columns["fee"] = header
				root, csvPath, mapping := fixture(t, TradingTemplate,
					"Trade,Order,Instrument,Fill Price,Fill Size,Fill Time,Exec,"+header+",Fee Ccy\n"+
						"trade,order,X,1,1,1700000000000,T,secret,USDT\n", columns)
				assertRejectedWithoutCurrent(t, root, csvPath, mapping)
			})
		})
	}
}

func TestAuditedSourceAliasesPass(t *testing.T) {
	columns := map[string]string{
		"ordId": "Order ID", "instId": "Instrument ID", "instType": "Instrument Type",
		"state": "Order State", "ordType": "ordType", "side": "Side",
		"px": "Order Price", "sz": "Order Size", "cTime": "Creation Time",
		"uTime": "Update Time", "cancelReason": "Cancellation Reason",
	}
	root, csvPath, mapping := fixture(t, OrderTemplate,
		"Order ID,Instrument ID,Instrument Type,Order State,ordType,Side,Order Price,Order Size,Creation Time,Update Time,Cancellation Reason\n"+
			"one,X,SWAP,canceled,limit,buy,1,1,1700000000000,1700000000001,user\n", columns)
	if receipt, err := Inspect(inspectConfig(root, csvPath, mapping)); err != nil || !receipt.Accepted {
		t.Fatalf("audited aliases rejected: %#v %v", receipt, err)
	}
}

func TestUnknownSourceAliasRejectsButUnmappedHeaderIsInert(t *testing.T) {
	columns := orderColumns()
	columns["cancelReason"] = "Mystery Metric"
	body := "Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated,Mystery Metric\n" +
		"one,X,SWAP,filled,limit,buy,1,1,1700000000000,1700000000001,value\n"
	root, csvPath, mapping := fixture(t, OrderTemplate,
		body, columns)
	assertRejectedWithoutCurrent(t, root, csvPath, mapping)

	root, csvPath, mapping = fixture(t, OrderTemplate,
		body, orderColumns())
	receipt, err := Inspect(inspectConfig(root, csvPath, mapping))
	if err != nil || !receipt.Accepted {
		t.Fatalf("unmapped unknown header was not inert: %#v %v", receipt, err)
	}
	if !contains(receipt.HeaderNormalized, "mysterymetric") {
		t.Fatal("unmapped unknown header absent from schema receipt")
	}
}

func TestUnmappedOutcomeColumnsAreNamesOnly(t *testing.T) {
	root, csvPath, mapping := fixture(t, OrderTemplate,
		"Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated,Future Return,Unrealized-PnL\n"+
			"one,X,SWAP,filled,limit,buy,1,1,1700000000000,1700000000001,TOP_SECRET_A,TOP_SECRET_B\n",
		orderColumns())
	receipt, err := Inspect(inspectConfig(root, csvPath, mapping))
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.ForbiddenOutcomeColumns) != 2 ||
		receipt.ForbiddenOutcomePresence["futurereturn"] != 1 ||
		receipt.ForbiddenOutcomePresence["unrealizedpnl"] != 1 {
		t.Fatalf("outcome presence receipt missing: %#v", receipt)
	}
	current, _, err := VerifyCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := os.ReadFile(filepath.Join(root, "generations", current.Generation, "projection.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projection), "TOP_SECRET") || strings.Contains(string(projection), "futurereturn") {
		t.Fatal("unmapped outcome entered projection")
	}
}

func TestBillIDOnlyTradingIdentity(t *testing.T) {
	columns := tradingColumns("Bill")
	delete(columns, "tradeId")
	columns["billId"] = "Bill"
	root, csvPath, mapping := fixture(t, TradingTemplate,
		"Bill,Order,Instrument,Fill Price,Fill Size,Fill Time,Exec,Fee,Fee Ccy\n"+
			"bill-1,order-1,X,1,2,2026-01-01T12:00:00+08:00,T,0.1,USDT\n", columns)
	receipt, err := Inspect(inspectConfig(root, csvPath, mapping))
	if err != nil || receipt.IdentityPresence["billId"] != 1 {
		t.Fatalf("billId-only trading rejected: %#v %v", receipt, err)
	}
	if receipt.TimeRanges["fillTime"].Min != "2026-01-01T12:00:00+08:00" {
		t.Fatalf("timezone offset was discarded: %#v", receipt.TimeRanges)
	}
}

func TestInputMetadataAndSymlinkBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root, csvPath, mapping string)
	}{
		{"csv-mode", func(t *testing.T, _, csvPath, _ string) { must(t, os.Chmod(csvPath, 0o644)) }},
		{"mapping-mode", func(t *testing.T, _, _, mapping string) { must(t, os.Chmod(mapping, 0o644)) }},
		{"inbox-mode", func(t *testing.T, root, _, _ string) { must(t, os.Chmod(filepath.Join(root, "inbox"), 0o755)) }},
		{"csv-symlink", func(t *testing.T, _, csvPath, _ string) {
			target := csvPath + ".target"
			must(t, os.Rename(csvPath, target))
			must(t, os.Symlink(target, csvPath))
		}},
		{"mapping-symlink", func(t *testing.T, _, _, mapping string) {
			target := mapping + ".target"
			must(t, os.Rename(mapping, target))
			must(t, os.Symlink(target, mapping))
		}},
		{"inbox-symlink", func(t *testing.T, root, _, _ string) {
			inbox := filepath.Join(root, "inbox")
			target := inbox + ".target"
			must(t, os.Rename(inbox, target))
			must(t, os.Symlink(target, inbox))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, csvPath, mapping := fixture(t, OrderTemplate, validOrderCSV("one"), orderColumns())
			test.mutate(t, root, csvPath, mapping)
			assertRejectedWithoutCurrent(t, root, csvPath, mapping)
		})
	}
}

func TestNaiveTimestampAndMinusFormulaRejected(t *testing.T) {
	for name, body := range map[string]string{
		"naive-time":    strings.Replace(validOrderCSV("one"), "1700000000000", "2026-01-01 12:00:00", 1),
		"minus-formula": strings.Replace(validOrderCSV("one"), ",1,1,1700000000000", ",-1,1,1700000000000", 1),
	} {
		t.Run(name, func(t *testing.T) {
			root, csvPath, mapping := fixture(t, OrderTemplate, body, orderColumns())
			assertRejectedWithoutCurrent(t, root, csvPath, mapping)
		})
	}
}

func TestEpochParsingIsIndependentOfLocalTimezone(t *testing.T) {
	original := time.Local
	defer func() { time.Local = original }()

	time.Local = time.FixedZone("west", -7*60*60)
	first, err := parseTime("1700000000000")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = time.FixedZone("east", 8*60*60)
	second, err := parseTime("1700000000000")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) || first.Location() != time.UTC || second.Location() != time.UTC ||
		first.Format(time.RFC3339Nano) != second.Format(time.RFC3339Nano) {
		t.Fatalf("epoch parse depends on local timezone: %s %s", first, second)
	}
}

func TestVerifyCurrentRejectsEveryTamper(t *testing.T) {
	for _, target := range []string{"current", "receipt.json", "mapping.json", "projection.ndjson", "checkpoint.json", "extra", "raw"} {
		t.Run(target, func(t *testing.T) {
			root, csvPath, mapping := fixture(t, OrderTemplate, validOrderCSV("one"), orderColumns())
			if _, err := Inspect(inspectConfig(root, csvPath, mapping)); err != nil {
				t.Fatal(err)
			}
			current, _, err := VerifyCurrent(root)
			if err != nil {
				t.Fatal(err)
			}
			generation := filepath.Join(root, "generations", current.Generation)
			switch target {
			case "current":
				appendFile(t, filepath.Join(root, "current.json"), []byte("x"))
			case "extra":
				must(t, os.WriteFile(filepath.Join(generation, "extra"), nil, 0o600))
			case "raw":
				appendFile(t, csvPath, []byte("\n"))
			default:
				appendFile(t, filepath.Join(generation, target), []byte("x"))
			}
			if _, _, err := VerifyCurrent(root); err == nil {
				t.Fatalf("%s tamper accepted", target)
			}
		})
	}
}

func TestFaultsPreserveExistingCurrent(t *testing.T) {
	for _, faultPoint := range []string{"after_stream", "after_receipt", "after_generation", "before_current"} {
		t.Run(faultPoint, func(t *testing.T) {
			root, firstCSV, mapping := fixture(t, OrderTemplate, validOrderCSV("first"), orderColumns())
			if _, err := Inspect(inspectConfig(root, firstCSV, mapping)); err != nil {
				t.Fatal(err)
			}
			before, _, err := VerifyCurrent(root)
			if err != nil {
				t.Fatal(err)
			}
			secondCSV := filepath.Join(root, "inbox", "second.csv")
			must(t, os.WriteFile(secondCSV, []byte(validOrderCSV("second")), 0o600))
			cfg := inspectConfig(root, secondCSV, mapping)
			cfg.Fault = func(step string) error {
				if step == faultPoint {
					return errors.New("injected")
				}
				return nil
			}
			if _, err := Inspect(cfg); err == nil {
				t.Fatal("fault did not stop publication")
			}
			afterRaw, err := os.ReadFile(filepath.Join(root, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			var after Current
			if json.Unmarshal(afterRaw, &after) != nil || after.Hash != before.Hash {
				t.Fatal("existing current changed after failed publication")
			}
			if _, _, err := VerifyCurrent(root); err != nil {
				t.Fatalf("existing current became invalid: %v", err)
			}
		})
	}
}

func TestSameInodeSameSizeRawMutationFailsAndPreservesCurrent(t *testing.T) {
	root, firstCSV, mapping := fixture(t, OrderTemplate, validOrderCSV("first"), orderColumns())
	if _, err := Inspect(inspectConfig(root, firstCSV, mapping)); err != nil {
		t.Fatal(err)
	}
	before, _, err := VerifyCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	secondCSV := filepath.Join(root, "inbox", "second.csv")
	must(t, os.WriteFile(secondCSV, []byte(validOrderCSV("second")), 0o600))
	cfg := inspectConfig(root, secondCSV, mapping)
	cfg.Fault = func(step string) error {
		if step != "after_initial_hash" {
			return nil
		}
		file, err := os.OpenFile(secondCSV, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.WriteAt([]byte("X"), int64(strings.Index(validOrderCSV("second"), "second")))
		return err
	}
	receipt, err := Inspect(cfg)
	if err == nil || receipt.Failure != "SOURCE_CHANGED" {
		t.Fatalf("same-inode mutation did not fail as SOURCE_CHANGED: %#v %v", receipt, err)
	}
	after, _, err := VerifyCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Hash != before.Hash {
		t.Fatal("raw mutation changed existing current")
	}
}

func TestRootSwapNeverLeaksToReplacement(t *testing.T) {
	for _, hook := range []string{"after_initial_hash", "after_stream", "after_receipt", "after_generation", "before_current"} {
		t.Run(hook, func(t *testing.T) {
			root, csvPath, mapping := fixture(t, OrderTemplate, validOrderCSV("one"), orderColumns())
			moved := root + ".moved"
			cfg := inspectConfig(root, csvPath, mapping)
			cfg.Fault = func(step string) error {
				if step != hook {
					return nil
				}
				if err := os.Rename(root, moved); err != nil {
					return err
				}
				return os.Mkdir(root, 0o700)
			}
			_, _ = Inspect(cfg) // Either original-fd publication or fail-closed is valid.
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("write leaked into replacement root at %s: %#v", hook, entries)
			}
		})
	}
}

func inspectConfig(root, csvPath, mapping string) Config {
	return Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20}
}

func assertRejectedWithoutCurrent(t *testing.T, root, csvPath, mapping string) {
	t.Helper()
	receipt, err := Inspect(inspectConfig(root, csvPath, mapping))
	if err == nil || receipt.Accepted {
		t.Fatalf("unsafe input accepted: %#v", receipt)
	}
	if _, statErr := os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(statErr) {
		t.Fatal("rejection created current authority")
	}
}

func orderColumns() map[string]string {
	return map[string]string{"ordId": "Order", "instId": "Instrument", "instType": "Type", "state": "State", "ordType": "Order Type", "side": "Side", "px": "Price", "sz": "Size", "cTime": "Created", "uTime": "Updated"}
}

func tradingColumns(identityHeader string) map[string]string {
	return map[string]string{"tradeId": identityHeader, "ordId": "Order", "instId": "Instrument", "fillPx": "Fill Price", "fillSz": "Fill Size", "fillTime": "Fill Time", "execType": "Exec", "fee": "Fee", "feeCcy": "Fee Ccy"}
}

func validOrderCSV(id string) string {
	return "Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated\n" +
		id + ",X,SWAP,filled,limit,buy,1,1,1700000000000,1700000000001\n"
}

func appendFile(t *testing.T, path string, suffix []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(suffix); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	must(t, file.Close())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
