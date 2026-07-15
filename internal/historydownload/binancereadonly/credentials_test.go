package binancereadonly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsExplicitMappingPrecedesLegacyFlat(t *testing.T) {
	path := writeSecret(t, `
api: legacy-api
secret: legacy-secret
binance:
  api: explicit-api
  secret: explicit-secret
okx:
  api: never-use
  secret: never-use
`)
	credentials, _, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.APIKey != "explicit-api" || credentials.Secret != "explicit-secret" {
		t.Fatal("explicit Binance mapping did not take precedence")
	}
}

func TestCredentialsAcceptRepositoryLegacyShapeWithoutPrintingValues(t *testing.T) {
	example := filepath.Join("..", "..", "..", "secret.example.yaml")
	credentials, warnings, err := LoadCredentials(example)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.APIKey != "YOUR_BINANCE_API_KEY" || credentials.Secret != "YOUR_BINANCE_API_SECRET" {
		t.Fatal("legacy repository example was not parsed")
	}
	joined := strings.Join(warnings, "\n")
	if strings.Contains(joined, credentials.APIKey) || strings.Contains(joined, credentials.Secret) {
		t.Fatal("credential values leaked into warnings")
	}
}

func TestCredentialsNeverSelectOtherVenue(t *testing.T) {
	path := writeSecret(t, "okx:\n  api: wrong\n  secret: wrong\n")
	if _, _, err := LoadCredentials(path); err == nil {
		t.Fatal("other venue credentials accepted")
	}
}

func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
