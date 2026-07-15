package historydownload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentialsRequiresNestedOKXMapping(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested.yaml")
	if err := os.WriteFile(nested, []byte("binance:\n  api: other\n  secret: other\nokx:\n  api: okx-key\n  secret: okx-secret\n  passphrase: okx-pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	credentials, warnings, err := LoadCredentials(nested)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected auditable permission warning, got %#v", warnings)
	}
	if credentials.Key != "okx-key" || credentials.Secret != "okx-secret" || credentials.Passphrase != "okx-pass" {
		t.Fatal("loader did not select only nested OKX fields")
	}

	flat := filepath.Join(dir, "flat.yaml")
	if err := os.WriteFile(flat, []byte("api: wrong-venue\nsecret: wrong-venue\npassphrase: wrong-venue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCredentials(flat); err == nil {
		t.Fatal("flat credentials accepted in production loader")
	}
}
