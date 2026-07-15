package historydownload

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstrumentGenerationFaultsPreserveOldAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	firstTime := time.Unix(100, 0).UTC()
	firstReceipt, err := BuildInstrumentSnapshot(PublicInstruments, []map[string]any{{
		"instId": "BTC-USDT", "instType": "SPOT", "state": "live",
	}}, firstTime)
	if err != nil {
		t.Fatal(err)
	}
	firstCurrent, err := PublishInstrumentGeneration(
		root, 1<<20, lock, []byte(`{"generation":1}`), firstReceipt,
		instrumentTestManifest(firstReceipt, firstTime), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyInstrumentCurrent(root); err != nil {
		t.Fatal(err)
	}

	secondTime := time.Unix(200, 0).UTC()
	secondReceipt, err := BuildInstrumentSnapshot(PublicInstruments, []map[string]any{{
		"instId": "ETH-USDT", "instType": "SPOT", "state": "live",
	}}, secondTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"after_raw", "after_receipt", "after_manifest", "after_generation", "before_current"} {
		t.Run(step, func(t *testing.T) {
			_, err := PublishInstrumentGeneration(
				root, 1<<20, lock, []byte(`{"generation":2}`), secondReceipt,
				instrumentTestManifest(secondReceipt, secondTime),
				func(actual string) error {
					if actual == step {
						return errors.New("injected fault")
					}
					return nil
				},
			)
			if err == nil {
				t.Fatal("fault injection did not stop publication")
			}
			current, _, verifyErr := VerifyInstrumentCurrent(root)
			if verifyErr != nil {
				t.Fatal(verifyErr)
			}
			if current.Generation != firstCurrent.Generation || current.ManifestHash != firstCurrent.ManifestHash {
				t.Fatalf("old authority changed after %s", step)
			}
		})
	}
}

func TestInstrumentCurrentVerifiesArtifactHashes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	captured := time.Unix(300, 0).UTC()
	receipt, err := BuildInstrumentSnapshot(PublicInstruments, []map[string]any{{
		"instId": "BTC-USDT", "instType": "SPOT", "state": "live",
	}}, captured)
	if err != nil {
		t.Fatal(err)
	}
	current, err := PublishInstrumentGeneration(
		root, 1<<20, lock, []byte(`{"generation":1}`), receipt,
		instrumentTestManifest(receipt, captured), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "instruments", current.Generation, "snapshot-raw.json")
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyInstrumentCurrent(root); err == nil {
		t.Fatal("tampered generation artifact accepted")
	}
}

func instrumentTestManifest(receipt InstrumentSnapshotReceipt, captured time.Time) Manifest {
	return Manifest{
		Endpoint: PublicInstruments, QueryHash: "query", RecordCount: len(receipt.Projection.Records),
		ProjectionFields: receipt.Projection.Fields, ProjectionSchemaHash: receipt.Projection.SchemaHash,
		PresenceCounts: receipt.Projection.PresenceCounts, OmittedCounts: receipt.Projection.OmittedCounts,
		Clocks:           Clock{ReceiveTime: captured, ProcessTime: captured},
		ReceiveTimeRange: ClockRange{Min: captured, Max: captured},
		ClockFitness:     "EVENT_CLOCK_NOT_APPLICABLE_SNAPSHOT", StartedAt: captured, CompletedAt: captured,
		PITStatus: InstrumentPITStatus,
	}
}
