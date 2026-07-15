package reportcenter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingxi/internal/historydownload"
)

func TestOrderInspectionBlocksOutcomesAndRawIDs(t *testing.T) {
	root, csvPath, mapping := fixture(t, OrderTemplate,
		"Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated,Client,Fill,Cancel,PnL,MFE\n"+
			"o-raw-1,BTC-USDT-SWAP,SWAP,canceled,limit,sell,100,2,1700000000000,1700000001000,c-raw,0,user_cancel,17,4\n"+
			"o-raw-2,BTC-USDT-SWAP,SWAP,expired,limit,buy,101,1,1700000002000,1700000003000,,0,expired,19,5\n"+
			"o-raw-3,BTC-USDT-SWAP,SWAP,no-fill,limit,buy,102,1,1700000004000,1700000005000,,0,,20,6\n",
		map[string]string{"ordId": "Order", "instId": "Instrument", "instType": "Type", "state": "State", "ordType": "Order Type", "side": "Side", "px": "Price", "sz": "Size", "cTime": "Created", "uTime": "Updated", "clOrdId": "Client", "accFillSz": "Fill", "cancelReason": "Cancel"})
	receipt, err := Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted || receipt.StateCounts["canceled"] != 1 || receipt.StateCounts["expired"] != 1 || receipt.StateCounts["no-fill"] != 1 || len(receipt.ForbiddenOutcomeColumns) != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	currentRaw, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	var current Current
	if err := json.Unmarshal(currentRaw, &current); err != nil || current.MappingSHA256 != receipt.MappingSHA256 {
		t.Fatalf("mapping was not bound: %s %v", currentRaw, err)
	}
	projection, err := os.ReadFile(filepath.Join(root, "generations", current.Generation, "projection.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"o-raw-1", "o-raw-2", "c-raw", "PnL", "MFE"} {
		if strings.Contains(string(projection), leaked) {
			t.Fatalf("raw ID or outcome leaked: %q", leaked)
		}
	}
}

func TestTradingAllowsMultipleFillsPerOrder(t *testing.T) {
	root, csvPath, mapping := fixture(t, TradingTemplate,
		"Trade,Order,Instrument,Fill Price,Fill Size,Fill Time,Exec,Fee,Fee Ccy\n"+
			"trade-1,order-1,BTC-USDT-SWAP,100,1,1700000000000,T,0.1,USDT\n"+
			"trade-2,order-1,BTC-USDT-SWAP,101,1,1700000001000,T,0.1,USDT\n",
		map[string]string{"tradeId": "Trade", "ordId": "Order", "instId": "Instrument", "fillPx": "Fill Price", "fillSz": "Fill Size", "fillTime": "Fill Time", "execType": "Exec", "fee": "Fee", "feeCcy": "Fee Ccy"})
	receipt, err := Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20})
	if err != nil || !receipt.Accepted || receipt.Rows != 2 {
		t.Fatalf("multiple fills were rejected: %#v %v", receipt, err)
	}
}

func TestRejectsDuplicateIdentityAndFormula(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate": "Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated\ndup,X,SWAP,filled,limit,buy,1,1,1700000000000,1700000000001\ndup,X,SWAP,filled,limit,buy,1,1,1700000000002,1700000000003\n",
		"formula":   "Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated\nx,X,SWAP,filled,limit,buy,=1,1,1700000000000,1700000000001\n",
	} {
		t.Run(name, func(t *testing.T) {
			root, csvPath, mapping := fixture(t, OrderTemplate, body, map[string]string{"ordId": "Order", "instId": "Instrument", "instType": "Type", "state": "State", "ordType": "Order Type", "side": "Side", "px": "Price", "sz": "Size", "cTime": "Created", "uTime": "Updated"})
			receipt, err := Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20})
			if err == nil || receipt.Accepted {
				t.Fatalf("unsafe CSV accepted: %#v", receipt)
			}
			if _, statErr := os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(statErr) {
				t.Fatal("failure created authority")
			}
		})
	}
}

func TestMappingDriftAndCrashDoNotPublishUnboundCurrent(t *testing.T) {
	root, csvPath, mapping := fixture(t, OrderTemplate,
		"Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated\none,X,SWAP,no-fill,limit,buy,1,1,1700000000000,1700000000001\n",
		map[string]string{"ordId": "Order", "instId": "Instrument", "instType": "Type", "state": "State", "ordType": "Order Type", "side": "Side", "px": "Price", "sz": "Size", "cTime": "Created", "uTime": "Updated"})
	_, err := Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20, Fault: func(step string) error {
		if step == "after_generation" {
			return errors.New("crash")
		}
		return nil
	}})
	if err == nil {
		t.Fatal("crash injection passed")
	}
	if _, statErr := os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(statErr) {
		t.Fatal("crash published current")
	}
	raw, err := os.ReadFile(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapping, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	currentRaw, _ := os.ReadFile(filepath.Join(root, "current.json"))
	var current Current
	_ = json.Unmarshal(currentRaw, &current)
	if current.MappingSHA256 != receipt.MappingSHA256 {
		t.Fatal("current mapping hash drift")
	}
}

func TestOwnerJournalLockExcludesConcurrentInspector(t *testing.T) {
	root, csvPath, mapping := fixture(t, OrderTemplate,
		"Order,Instrument,Type,State,Order Type,Side,Price,Size,Created,Updated\none,X,SWAP,filled,limit,buy,1,1,1700000000000,1700000000001\n",
		map[string]string{"ordId": "Order", "instId": "Instrument", "instType": "Type", "state": "State", "ordType": "Order Type", "side": "Side", "px": "Price", "sz": "Size", "cTime": "Created", "uTime": "Updated"})
	lock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = Inspect(Config{Root: root, Inbox: filepath.Join(root, "inbox"), CSVPath: csvPath, MappingPath: mapping, RootCap: 1 << 20})
	if !errors.Is(err, historydownload.ErrRootLocked) {
		t.Fatalf("competing owner entered inspection: %v", err)
	}
}

func fixture(t *testing.T, template Template, body string, columns map[string]string) (string, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	inbox := filepath.Join(root, "inbox")
	mappings := filepath.Join(root, "mappings")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mappings, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(inbox, "fixture.csv")
	if err := os.WriteFile(csvPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(mappings, "mapping.json")
	raw, _ := json.Marshal(Mapping{Template: template, Columns: columns})
	if err := os.WriteFile(mapping, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, csvPath, mapping
}
