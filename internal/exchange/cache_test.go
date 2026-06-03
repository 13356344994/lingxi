package exchange

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoricalPayloadCacheSaveLoadRoundTrip(t *testing.T) {
	cache, err := NewHistoricalPayloadCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}

	rawURL := "https://example.test/fapi/v1/fundingRate?symbol=BTCUSDT"
	payload := []byte(`[{"symbol":"BTCUSDT"}]`)
	if err := cache.Save(rawURL, payload); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, ok, loadErr := cache.Load(rawURL)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if !ok || string(got) != string(payload) {
		t.Fatalf("unexpected load result: ok=%t payload=%s", ok, string(got))
	}
}

func TestHistoricalPayloadCacheRecoversFromCorruptedBucket(t *testing.T) {
	cache, err := NewHistoricalPayloadCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}

	rawURL := "https://example.test/fapi/v1/klines?symbol=BTCUSDT"
	path := cache.bucketPath(rawURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{broken-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, ok, loadErr := cache.Load(rawURL)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if ok || len(got) != 0 {
		t.Fatalf("expected corrupted bucket to be treated as cache miss, got ok=%t payload=%s", ok, string(got))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected corrupted bucket file to be removed, statErr=%v", statErr)
	}
}

func TestHistoricalPayloadCacheInvalidatesVersionMismatch(t *testing.T) {
	cache, err := NewHistoricalPayloadCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}

	rawURL := "https://example.test/api/v3/klines?symbol=ETHUSDT"
	entryKey := cache.entryKey(rawURL)
	path := cache.bucketPath(rawURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	legacyBucket := historicalCacheBucket{
		Version: historicalCacheSchemaVersion - 1,
		Entries: map[string]string{
			entryKey: `{"legacy":true}`,
		},
	}
	encoded, marshalErr := json.Marshal(legacyBucket)
	if marshalErr != nil {
		t.Fatalf("json.Marshal() error = %v", marshalErr)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, ok, loadErr := cache.Load(rawURL)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if ok || len(got) != 0 {
		t.Fatalf("expected legacy version to be invalidated, got ok=%t payload=%s", ok, string(got))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected legacy bucket file to be removed, statErr=%v", statErr)
	}
}

func TestHistoricalPayloadCacheExpiresStaleBucketOnLoad(t *testing.T) {
	cache, err := NewHistoricalPayloadCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}

	rawURL := "https://example.test/fapi/v1/fundingRate?symbol=SOLUSDT"
	payload := []byte(`[{"symbol":"SOLUSDT"}]`)
	if err := cache.Save(rawURL, payload); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path := cache.bucketPath(rawURL)
	staleTime := time.Now().Add(-historicalCacheRetention - time.Hour)
	if err := os.Chtimes(path, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	got, ok, loadErr := cache.Load(rawURL)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if ok || len(got) != 0 {
		t.Fatalf("expected stale bucket to be treated as cache miss, got ok=%t payload=%s", ok, string(got))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected stale bucket file to be removed, statErr=%v", statErr)
	}
}

func TestHistoricalPayloadCacheCleansExpiredBucketsOnInit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "funding", "BTCUSDT.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	bucket := historicalCacheBucket{
		Version: historicalCacheSchemaVersion,
		Entries: map[string]string{"legacy": `{"ok":true}`},
	}
	encoded, marshalErr := json.Marshal(bucket)
	if marshalErr != nil {
		t.Fatalf("json.Marshal() error = %v", marshalErr)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	staleTime := time.Now().Add(-historicalCacheRetention - time.Hour)
	if err := os.Chtimes(path, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	cache, err := NewHistoricalPayloadCache(dir)
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}
	if cache == nil {
		t.Fatal("expected cache to be initialized")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected expired bucket file to be removed during init, statErr=%v", statErr)
	}
}
