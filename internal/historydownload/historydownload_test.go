package historydownload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	datatruthhistory "lingxi/internal/datatruth/history"
)

func TestProjectOmitsOutcomesAndUnknownFields(t *testing.T) {
	got, err := Project(OrdersEndpoint, []map[string]any{{
		"ordId": "1", "state": "filled", "fillPnl": "9", "PnL": "2", "MFE": "3", "futureField": "x",
	}}, TokenStore{SaltPath: filepath.Join(t.TempDir(), "salt")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0]["ordIdToken"] == nil || got.Records[0]["fillPnl"] != nil {
		t.Fatalf("outcome firewall failed: %#v", got)
	}
	if got.OmittedCounts["fillPnl"] != 1 || got.OmittedCounts["futureField"] != 1 {
		t.Fatalf("schema drift fields not recorded: %#v", got.OmittedCounts)
	}
}

func TestFrozenContractAndProjectionFirewallAgree(t *testing.T) {
	path := filepath.Join("..", "..", "research", "firstbattle", "route_data_contracts.json")
	root, canonical, err := ResolveRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContract(root, canonical); err != nil {
		t.Fatalf("frozen contract bindings failed validation: %v", err)
	}
}

func TestRepositoryResolutionAndOutputBoundary(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	contract := filepath.Join(repo, "research", "firstbattle", "route_data_contracts.json")
	if err := os.MkdirAll(filepath.Dir(contract), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module lingxi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contract, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, resolvedContract, err := ResolveRepository(contract)
	if err != nil || resolvedRoot != repo || resolvedContract != contract {
		t.Fatalf("repository resolution failed: root=%q contract=%q err=%v", resolvedRoot, resolvedContract, err)
	}

	deepCopy := filepath.Join(repo, "nested", "deeper", "route_data_contracts.json")
	if err := os.MkdirAll(filepath.Dir(deepCopy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deepCopy, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveRepository(deepCopy); err == nil {
		t.Fatal("non-canonical deep contract copy accepted")
	}

	if err := os.MkdirAll(filepath.Join(repo, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		filepath.Join(repo, "cmd", "new-output"),
		filepath.Join(repo, "records"),
		filepath.Join(repo, "records", "not-created"),
	} {
		if _, err := ValidateOutputRoot(candidate, repo); err == nil {
			t.Fatalf("repository output accepted: %s", candidate)
		}
	}
	link := filepath.Join(base, "records-link")
	if err := os.Symlink(filepath.Join(repo, "records"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateOutputRoot(filepath.Join(link, "future"), repo); err == nil {
		t.Fatal("symlink into records accepted")
	}
	outside := filepath.Join(base, "outside", "future")
	if canonical, err := ValidateOutputRoot(outside, repo); err != nil || canonical == "" {
		t.Fatalf("outside output rejected: %q %v", canonical, err)
	}
}

func TestFrozenBindingCoverageTamperAndMissing(t *testing.T) {
	if len(frozenBindingSpecs) != 11 {
		t.Fatalf("binding coverage changed without test update: %d", len(frozenBindingSpecs))
	}
	root, bindings := buildBindingFixture(t)
	if err := validateFrozenBindings(root, bindings); err != nil {
		t.Fatalf("baseline fixture failed binding validation: %v", err)
	}
	for _, spec := range frozenBindingSpecs {
		spec := spec
		t.Run(spec.Key+"-tamper", func(t *testing.T) {
			root, bindings := buildBindingFixture(t)
			path := filepath.Join(root, filepath.FromSlash(spec.Path))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var artifact map[string]any
			if err := json.Unmarshal(raw, &artifact); err != nil {
				t.Fatal(err)
			}
			if spec.Recompute {
				artifact["payload"] = "tampered"
			} else {
				setNested(artifact, spec.SelfField, "tampered-declared-hash")
			}
			writeJSONFile(t, path, artifact)
			if err := validateFrozenBindings(root, bindings); err == nil {
				t.Fatal("tampered binding accepted")
			}
		})
		t.Run(spec.Key+"-missing", func(t *testing.T) {
			root, bindings := buildBindingFixture(t)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(spec.Path))); err != nil {
				t.Fatal(err)
			}
			if err := validateFrozenBindings(root, bindings); err == nil {
				t.Fatal("missing binding artifact accepted")
			}
		})
	}
}

func TestHistoryCatalogNestedTamperingFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*datatruthhistory.HistoryCatalog)
	}{
		{"file-hash", func(catalog *datatruthhistory.HistoryCatalog) {
			catalog.Families[0].Files[0].FileHash = strings.Repeat("b", 64)
		}},
		{"lines", func(catalog *datatruthhistory.HistoryCatalog) {
			catalog.Families[0].Files[0].Lines++
		}},
		{"deleted-entry", func(catalog *datatruthhistory.HistoryCatalog) {
			catalog.Families[0].Files = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, bindings := buildBindingFixture(t)
			path := filepath.Join(root, "research", "firstbattle", "history_catalog.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var catalog datatruthhistory.HistoryCatalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				t.Fatal(err)
			}
			test.mutate(&catalog)
			writeJSONFile(t, path, catalog)
			if err := validateFrozenBindings(root, bindings); err == nil {
				t.Fatal("nested history catalog tampering accepted")
			}
		})
	}
}

func buildBindingFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	bindings := map[string]any{"binding_policy": "test"}
	for _, spec := range frozenBindingSpecs {
		if spec.Key == "history_data_hash" {
			file := datatruthhistory.FileStats{
				Family: "fixture", Path: "fixture.jsonl", Lines: 1,
				Fields: []string{"ts"}, FileHash: strings.Repeat("a", 64), BytesHashed: 1,
			}
			dataHash := datatruth.HashStrings("fixture\x00fixture.jsonl\x00" + file.FileHash + "\x001")
			schemaHash, _, err := datatruth.HashJSON(map[string]any{
				"schema_version": datatruthhistory.CatalogSchemaVersion,
				"files": []any{map[string]any{
					"family": "fixture", "path": "fixture.jsonl", "fields": []any{"ts"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			catalog := datatruthhistory.HistoryCatalog{
				SchemaVersion: datatruthhistory.CatalogSchemaVersion,
				DatasetID:     "history.fixture." + dataHash[:12],
				RootDir:       filepath.Join(root, "history"),
				Families: []datatruthhistory.FamilyCatalog{{
					Name: "fixture", Files: []datatruthhistory.FileStats{file},
					FilesScanned: 1, LinesScanned: 1, Fields: []string{"ts"},
				}},
				FilesScanned: 1, LinesScanned: 1, DataHash: dataHash, SchemaHash: schemaHash,
			}
			bindings[spec.Key] = dataHash
			writeJSONFile(t, filepath.Join(root, filepath.FromSlash(spec.Path)), catalog)
			continue
		}
		artifact := map[string]any{"payload": spec.Key}
		setNested(artifact, spec.SelfField, nil)
		expected := "history-corpus-hash"
		if spec.Recompute {
			var err error
			expected, err = hash(artifact)
			if err != nil {
				t.Fatal(err)
			}
		}
		setNested(artifact, spec.SelfField, expected)
		bindings[spec.Key] = expected
		writeJSONFile(t, filepath.Join(root, filepath.FromSlash(spec.Path)), artifact)
	}
	return root, bindings
}

func setNested(document map[string]any, path []string, value any) {
	current := document
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointTamperRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := SaveCheckpoint(path, Checkpoint{Endpoint: "/api/v5/public/instruments"}); err != nil {
		t.Fatal(err)
	}
	var cp map[string]any
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &cp)
	cp["cursor_token"] = "tampered"
	b, _ = json.Marshal(cp)
	_ = os.WriteFile(path, b, 0o600)
	if _, _, err := LoadCheckpoint(path); err == nil {
		t.Fatal("expected checkpoint integrity error")
	}
}

func TestDownloadResumesAndHonorsDiskCap(t *testing.T) {
	root := t.TempDir()
	_, schemaHash, err := ProjectionSchema(OrdersEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	config := DownloadConfig{
		Endpoint: OrdersEndpoint, RawDirectory: filepath.Join(root, "raw"),
		ProjectionDirectory: filepath.Join(root, "projection"), CheckpointPath: filepath.Join(root, "checkpoint.json"),
		ManifestPath: filepath.Join(root, "manifest.json"), QueryHash: "query", SchemaHash: schemaHash, DiskCapBytes: 1 << 20,
		RootDirectory: root, RootCapBytes: 1 << 20,
		RootLock:   testRootLock(t, root),
		TokenStore: TokenStore{SaltPath: filepath.Join(root, "salt")},
	}
	calls := 0
	fetch := func(_ context.Context, cursor string) (Page, error) {
		calls++
		if cursor == "" {
			return Page{Raw: []byte(`{"data":[{"ordId":"200"}]}`), Records: []map[string]any{{"ordId": "200"}}, NextCursor: "200", ReceiveTime: time.Now(), EventTimeMin: time.Now(), EventTimeMax: time.Now()}, nil
		}
		if cursor == "200" {
			return Page{Raw: []byte(`{"data":[{"ordId":"100"}]}`), Records: []map[string]any{{"ordId": "100"}}, NextCursor: "100", ReceiveTime: time.Now(), EventTimeMin: time.Now(), EventTimeMax: time.Now()}, nil
		}
		return Page{Raw: []byte(`{"data":[]}`), ReceiveTime: time.Now()}, nil
	}
	got, err := Download(context.Background(), config, fetch)
	if err != nil || got.Pages != 2 || got.Records != 2 {
		t.Fatalf("download: %+v, %v", got, err)
	}
	if _, err := Download(context.Background(), config, fetch); err != nil || calls != 3 {
		t.Fatalf("resume should not refetch: calls=%d err=%v", calls, err)
	}
	config.DiskCapBytes = 3
	config.CheckpointPath = filepath.Join(root, "cap.json")
	config.ManifestPath = filepath.Join(root, "cap-manifest.json")
	config.RawDirectory = filepath.Join(root, "cap-raw")
	config.ProjectionDirectory = filepath.Join(root, "cap-projection")
	config.TokenStore = TokenStore{SaltPath: filepath.Join(root, "cap-salt")}
	if _, err := Download(context.Background(), config, fetch); err == nil {
		t.Fatal("expected disk cap error")
	}
}

func TestDownloadRetries(t *testing.T) {
	root := t.TempDir()
	_, schemaHash, err := ProjectionSchema(OrdersEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	config := DownloadConfig{
		Endpoint: OrdersEndpoint, RawDirectory: filepath.Join(root, "raw"), ProjectionDirectory: filepath.Join(root, "projection"),
		CheckpointPath: filepath.Join(root, "checkpoint.json"), ManifestPath: filepath.Join(root, "manifest.json"),
		QueryHash: "query", SchemaHash: schemaHash, DiskCapBytes: 1 << 20, MaxRetries: 1, RetryDelay: time.Millisecond,
		RootDirectory: root, RootCapBytes: 1 << 20,
		RootLock:   testRootLock(t, root),
		TokenStore: TokenStore{SaltPath: filepath.Join(root, "salt")},
	}
	calls := 0
	_, err = Download(context.Background(), config, func(context.Context, string) (Page, error) {
		calls++
		if calls == 1 {
			return Page{}, errors.New("temporary")
		}
		return Page{Raw: []byte(`{"data":[]}`), ReceiveTime: time.Now()}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("retry failed calls=%d err=%v", calls, err)
	}
}

func TestUntrustedSafeMessageIsCollapsed(t *testing.T) {
	cfg := testDownloadConfig(t, t.TempDir())
	_, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		return Page{}, maliciousSafeError{}
	})
	if err == nil || strings.Contains(err.Error(), "cursor") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe SafeMessage escaped: %v", err)
	}
}

type maliciousSafeError struct{}

func (maliciousSafeError) Error() string       { return "opaque" }
func (maliciousSafeError) SafeMessage() string { return "cursor=987654321 secret=leaked" }

func TestDownloadPublicationRecoveryAndManifestAuthority(t *testing.T) {
	root := t.TempDir()
	cfg := testDownloadConfig(t, root)
	fetchCalls := 0
	fetch := func(_ context.Context, cursor string) (Page, error) {
		fetchCalls++
		now := time.Now()
		if cursor != "" {
			return Page{Raw: []byte(`{"data":[]}`), ReceiveTime: now}, nil
		}
		return Page{
			Raw: []byte(`{"data":[{"ordId":"700001"}]}`), Records: []map[string]any{{"ordId": "700001"}}, NextCursor: "700001",
			ReceiveTime: now, EventTimeMin: now, EventTimeMax: now,
		}, nil
	}
	cfg.PublishManifest = func(path string, manifest Manifest) error {
		if err := PublishManifest(path, manifest); err != nil {
			return err
		}
		return errors.New("injected crash after manifest publish")
	}
	if _, err := Download(context.Background(), cfg, fetch); err == nil {
		t.Fatal("injected manifest failure accepted")
	}
	if err := os.WriteFile(cfg.CheckpointPath+".tmp", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectionDirectory, "page-999999.json.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.PublishManifest = nil
	result, err := Download(context.Background(), cfg, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if fetchCalls != 2 || result.Manifest.ManifestHash == nil {
		t.Fatalf("recovery refetched or lacked manifest: calls=%d result=%#v", fetchCalls, result)
	}
	if err := os.Remove(cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Download(context.Background(), cfg, fetch); err == nil {
		t.Fatal("published checkpoint falsely succeeded without manifest")
	}
	if fetchCalls != 2 {
		t.Fatal("published-state verification refetched data")
	}
}

func TestDownloadIdentityAndDuplicateAccounting(t *testing.T) {
	cfg := testDownloadConfig(t, t.TempDir())
	if _, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		return Page{Raw: []byte(`{"data":[{}]}`), Records: []map[string]any{{}}, ReceiveTime: time.Now()}, nil
	}); err == nil || !strings.Contains(err.Error(), "identity missing") {
		t.Fatalf("missing identity did not fail closed: %v", err)
	}
	cp, exists, err := LoadCheckpoint(cfg.CheckpointPath)
	if err != nil || !exists || cp.MissingIDCount != 1 || cp.RecordCount != 0 {
		t.Fatalf("missing identity audit mismatch: cp=%#v exists=%v err=%v", cp, exists, err)
	}

	cfg = testDownloadConfig(t, t.TempDir())
	cfg.Endpoint = FillsEndpoint
	_, cfg.SchemaHash, err = ProjectionSchema(FillsEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	page := 0
	result, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		page++
		now := time.Now()
		if page == 1 {
			return Page{Raw: []byte(`{"data":[{"tradeId":"900","billId":"300"}]}`), Records: []map[string]any{{"tradeId": "900", "billId": "300"}}, NextCursor: "300", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now}, nil
		}
		if page == 2 {
			return Page{Raw: []byte(`{"data":[{"tradeId":"900","billId":"200"},{"tradeId":"800","billId":"100"}]}`), Records: []map[string]any{{"tradeId": "900", "billId": "200"}, {"tradeId": "800", "billId": "100"}}, NextCursor: "100", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now}, nil
		}
		return Page{Raw: []byte(`{"data":[]}`), ReceiveTime: now}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Records != 2 || result.Manifest.DuplicateCount != 1 {
		t.Fatalf("duplicate accounting mismatch: %#v", result)
	}
}

func TestCheckpointBindsCriticalConfiguration(t *testing.T) {
	root := t.TempDir()
	base := testDownloadConfig(t, root)
	base.PublishManifest = func(string, Manifest) error { return errors.New("leave pending") }
	now := time.Now()
	if _, err := Download(context.Background(), base, func(_ context.Context, cursor string) (Page, error) {
		if cursor != "" {
			return Page{Raw: []byte(`{"data":[]}`), ReceiveTime: now}, nil
		}
		return Page{Raw: []byte(`{"data":[{"ordId":"500"}]}`), Records: []map[string]any{{"ordId": "500"}}, NextCursor: "500", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now}, nil
	}); err == nil {
		t.Fatal("expected injected pending state")
	}
	cases := map[string]func(*DownloadConfig){
		"manifest": func(c *DownloadConfig) { c.ManifestPath += ".other" },
		"privacy":  func(c *DownloadConfig) { c.Private = !c.Private },
		"cap":      func(c *DownloadConfig) { c.DiskCapBytes++ },
		"root-cap": func(c *DownloadConfig) { c.RootCapBytes++ },
		"root":     func(c *DownloadConfig) { c.RootDirectory = filepath.Dir(c.RootDirectory) },
		"salt":     func(c *DownloadConfig) { c.TokenStore.SaltPath += ".other" },
		"semantics": func(c *DownloadConfig) {
			c.DenominatorFitness = "DIFFERENT"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
				t.Fatal("mismatched checkpoint attempted fetch")
				return Page{}, nil
			}); err == nil {
				t.Fatal("critical configuration mismatch accepted")
			}
		})
	}
}

func TestRawIDsNeverLeaveRawAndResumeReconstructsAfter(t *testing.T) {
	root := t.TempDir()
	cfg := testDownloadConfig(t, root)
	firstRaw := []byte(`{"code":"0","data":[{"ordId":"900003","clOrdId":"client900003"},{"ordId":"900002","clOrdId":"client900002"}]}`)
	firstCalls := 0
	_, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		firstCalls++
		if firstCalls == 1 {
			now := time.Now()
			return Page{
				Raw: firstRaw, Records: []map[string]any{
					{"ordId": "900003", "clOrdId": "client900003"},
					{"ordId": "900002", "clOrdId": "client900002"},
				},
				NextCursor: "900002", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now,
			}, nil
		}
		return Page{}, errors.New("injected crash before second page")
	})
	if err == nil {
		t.Fatal("expected injected interruption")
	}
	checkNoRawIDs(t, cfg.CheckpointPath, "900003", "900002", "client900003", "client900002")

	resumeCalls := 0
	result, err := Download(context.Background(), cfg, func(_ context.Context, after string) (Page, error) {
		resumeCalls++
		now := time.Now()
		switch resumeCalls {
		case 1:
			if after != "900002" {
				t.Fatalf("resume after=%q, want reconstructed cursor", after)
			}
			return Page{
				Raw: []byte(`{"code":"0","data":[{"ordId":"800003","clOrdId":"client800003"},{"ordId":"800002","clOrdId":"client800002"}]}`),
				Records: []map[string]any{
					{"ordId": "800003", "clOrdId": "client800003"},
					{"ordId": "800002", "clOrdId": "client800002"},
				},
				NextCursor: "800002", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now,
			}, nil
		default:
			if after != "800002" {
				t.Fatalf("next after=%q", after)
			}
			return Page{Raw: []byte(`{"code":"0","data":[]}`), ReceiveTime: now}, nil
		}
	})
	if err != nil || result.Records != 4 {
		t.Fatalf("resume failed: result=%#v err=%v", result, err)
	}
	ids := []string{"900003", "900002", "client900003", "client900002", "800003", "800002", "client800003", "client800002"}
	checkNoRawIDs(t, cfg.CheckpointPath, ids...)
	checkNoRawIDs(t, cfg.ManifestPath, ids...)
}

func TestFillsBillAndTradeIDsAreTokenized(t *testing.T) {
	root := t.TempDir()
	cfg := testDownloadConfig(t, root)
	cfg.Endpoint = FillsEndpoint
	var err error
	_, cfg.SchemaHash, err = ProjectionSchema(FillsEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = Download(context.Background(), cfg, func(_ context.Context, after string) (Page, error) {
		calls++
		now := time.Now()
		if calls == 1 {
			if after != "" {
				t.Fatal("unexpected initial cursor")
			}
			return Page{
				Raw: []byte(`{"code":"0","data":[{"tradeId":"710003","billId":"810003"},{"tradeId":"710002","billId":"810002"}]}`),
				Records: []map[string]any{
					{"tradeId": "710003", "billId": "810003"},
					{"tradeId": "710002", "billId": "810002"},
				},
				NextCursor: "810002", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now,
			}, nil
		}
		if after != "810002" {
			t.Fatalf("fills after=%q", after)
		}
		return Page{Raw: []byte(`{"code":"0","data":[]}`), ReceiveTime: now}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"710003", "710002", "810003", "810002"}
	checkNoRawIDs(t, cfg.CheckpointPath, ids...)
	checkNoRawIDs(t, cfg.ManifestPath, ids...)
}

func TestPaginationMonotonicityRejectsDisorderAndBoundaryReversal(t *testing.T) {
	cases := []struct {
		name     string
		previous string
		records  []map[string]any
		next     string
	}{
		{"unordered", "", []map[string]any{{"ordId": "100"}, {"ordId": "200"}}, "200"},
		{"duplicate", "", []map[string]any{{"ordId": "200"}, {"ordId": "200"}}, "200"},
		{"boundary-reversal", "200", []map[string]any{{"ordId": "300"}, {"ordId": "100"}}, "100"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePageOrder(OrdersEndpoint, test.previous, test.records, test.next); err == nil {
				t.Fatal("invalid pagination order accepted")
			}
		})
	}
	if err := validatePageOrder(FillsEndpoint, "400", []map[string]any{
		{"tradeId": "9", "billId": "300"}, {"tradeId": "8", "billId": "200"},
	}, "200"); err != nil {
		t.Fatalf("valid fills billId ordering rejected: %v", err)
	}
}

func TestRootCapIncludesSiblingEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "other-endpoint.bin"), make([]byte, 900), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testDownloadConfig(t, root)
	cfg.RootCapBytes = 950
	now := time.Now()
	if _, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		return Page{
			Raw: []byte(`{"data":[{"ordId":"300"}]}`), Records: []map[string]any{{"ordId": "300"}},
			NextCursor: "300", ReceiveTime: now, EventTimeMin: now, EventTimeMax: now,
		}, nil
	}); err == nil || !strings.Contains(err.Error(), "root disk cap") {
		t.Fatalf("root aggregate cap not enforced: %v", err)
	}
}

func TestManagedPathsCannotEscapeRootCap(t *testing.T) {
	root := t.TempDir()
	cfg := testDownloadConfig(t, root)
	cfg.ManifestPath = filepath.Join(filepath.Dir(root), "escaped-manifest.json")
	if _, err := Download(context.Background(), cfg, func(context.Context, string) (Page, error) {
		t.Fatal("escaped configuration attempted fetch")
		return Page{}, nil
	}); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("managed root escape accepted: %v", err)
	}
}

func checkNoRawIDs(t *testing.T, path string, ids ...string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if bytes.Contains(raw, []byte(id)) {
			t.Fatalf("%s leaked raw identifier", filepath.Base(path))
		}
	}
}

func testDownloadConfig(t *testing.T, root string) DownloadConfig {
	t.Helper()
	_, schemaHash, err := ProjectionSchema(OrdersEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	return DownloadConfig{
		Endpoint: OrdersEndpoint, RawDirectory: filepath.Join(root, "raw"), ProjectionDirectory: filepath.Join(root, "projection"),
		CheckpointPath: filepath.Join(root, "checkpoint.json"), ManifestPath: filepath.Join(root, "manifest.json"),
		QueryHash: "query", SchemaHash: schemaHash, DiskCapBytes: 1 << 20, Private: true,
		RootDirectory: root, RootCapBytes: 1 << 20,
		RootLock:   testRootLock(t, root),
		TokenStore: TokenStore{SaltPath: filepath.Join(root, "salt")},
	}
}

func testRootLock(t *testing.T, root string) *RootLock {
	t.Helper()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}
