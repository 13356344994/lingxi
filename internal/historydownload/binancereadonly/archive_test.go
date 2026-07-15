package binancereadonly

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCSVRejectsOutcomeFixture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "outcome.csv")
	if err := os.WriteFile(path, []byte("Order Id,Status,Executed Qty,Update Time,Realized PnL\n1,CANCELED,0,1,99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCSVSchema(path); err == nil {
		t.Fatal("outcome schema accepted")
	}
}

func TestSummarizeAcceptsRequiredIdentityAndZeroFillColumns(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "orders.csv")
	if err := os.WriteFile(path, []byte("Order Id,Status,Executed Qty,Update Time\n1,CANCELED,0.00000000,1783987200000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := SummarizeZeroFill(path)
	if err != nil || summary.Counts["CANCELED"] != 1 {
		t.Fatalf("safe CSV rejected: summary=%+v err=%v", summary, err)
	}
}

func TestFDArtifactRejectsZipSlip(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, _ := writer.Create("../orders.csv")
	_, _ = entry.Write([]byte("Order Id,Status,Executed Qty,Update Time\n1,CANCELED,0,1\n"))
	_ = writer.Close()
	root := secureRoot(t)
	authority, err := acquireAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.close()
	base, err := mkdirAt(authority.root, pilotDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive.Bytes())), Header: make(http.Header)}, nil
	})}
	if _, err := processArtifactFD(context.Background(), client, "https://www.binance.com/orders.zip", authority.root, base); err == nil {
		t.Fatal("zip-slip archive accepted")
	}
}

func TestExecutedQtyStrictDecimalZero(t *testing.T) {
	for _, value := range []string{"0", "0.00000000", "-0", "-0.000"} {
		zero, err := isDecimalZero(value)
		if err != nil || !zero {
			t.Fatalf("%q: zero=%v err=%v", value, zero, err)
		}
	}
	for _, value := range []string{"1", "0.0001"} {
		zero, err := isDecimalZero(value)
		if err != nil || zero {
			t.Fatalf("%q: zero=%v err=%v", value, zero, err)
		}
	}
	for _, value := range []string{"-1", "NaN", "Inf", "1e-9", "1/2", "", ".", "1."} {
		if _, err := isDecimalZero(value); err == nil {
			t.Fatalf("invalid executedQty accepted: %q", value)
		}
	}
}

func writeZIP(t *testing.T, path, name, content string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
