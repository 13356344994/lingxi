package entryproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/exitproof"
)

// FixtureBundle is a sealed test bundle for CLI and red-team fixtures.
type FixtureBundle struct {
	SchemaVersion string            `json:"schema_version"`
	Proofs        []EntryProof      `json:"proofs"`
	ExitProofs    []exitproof.Proof `json:"exit_proofs"`
	BundleHash    string            `json:"bundle_hash"`
}

const SchemaVersionFixture = "lingxi.routerg10.entry-proof.fixture.v1"

// FixtureTrigger carries entry and exit fields for bundled fixtures.
type FixtureTrigger struct {
	TriggerRecord
	ExitDepthUSDT            float64 `json:"exit_depth_usdt"`
	CanExitBeforeMaintenance bool    `json:"can_exit_before_maintenance"`
	ExtremeExitSlippageBPS   float64 `json:"extreme_exit_slippage_bps"`
	PartialCloseFeasible     bool    `json:"partial_close_feasible"`
}

// WriteFixtures materializes entry+exit proof fixtures for path tests.
func WriteFixtures(outputDir string) (FixtureBundle, error) {
	triggers := DefaultFixtureTriggers()
	proofs := make([]EntryProof, 0, len(triggers))
	exits := make([]exitproof.Proof, 0, len(triggers))
	for _, tr := range triggers {
		ep, err := BuildEntryProof(tr.TriggerRecord)
		if err != nil {
			if tr.TriggerID == "inverse_fail_closed" {
				continue
			}
			return FixtureBundle{}, err
		}
		proofs = append(proofs, ep)
		xp, err := exitproof.BuildProof(exitproof.Input{
			TriggerID:                tr.TriggerID,
			DecisionTime:             tr.DecisionTime,
			ExitDepthUSDT:            tr.ExitDepthUSDT,
			CanExitBeforeMaintenance: tr.CanExitBeforeMaintenance,
			ExtremeExitSlippageBPS:   tr.ExtremeExitSlippageBPS,
			PartialCloseFeasible:     tr.PartialCloseFeasible,
		})
		if err != nil {
			return FixtureBundle{}, err
		}
		exits = append(exits, xp)
	}
	bundle := FixtureBundle{
		SchemaVersion: SchemaVersionFixture,
		Proofs:        proofs,
		ExitProofs:    exits,
	}
	hash, err := hashFixtureBundle(bundle)
	if err != nil {
		return FixtureBundle{}, err
	}
	bundle.BundleHash = hash
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return FixtureBundle{}, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "entry_proof_fixtures.json"), raw, 0o644); err != nil {
		return FixtureBundle{}, err
	}
	return bundle, nil
}

func hashFixtureBundle(b FixtureBundle) (string, error) {
	payload := b
	payload.BundleHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// DefaultFixtureTriggers returns frozen red-team scenarios.
func DefaultFixtureTriggers() []FixtureTrigger {
	const (
		walHash        = "a1b2c3d4e5f6789012345678abcdef01"
		settlementHash = "b2c3d4e5f6789012345678abcdef0123"
		makerHash      = "c3d4e5f6789012345678abcdef012345"
		shadowHash     = "d4e5f6789012345678abcdef01234567"
	)
	return []FixtureTrigger{
		{
			TriggerRecord: TriggerRecord{
				TriggerID: "touch_not_fill", DecisionTime: parseTime("2026-07-01T10:00:00Z"),
				Symbol: "BTC-USDT", Venue: "okx", Side: "long", Price: 100000,
				OrderType: "maker", DecisionVisibleInfo: "shock_confirmed",
				MinSz: "1", LotSz: "0.01", CtVal: "0.01", CtType: "linear",
				ExecutableDepthUSDT: 5000, ParticipationRate: 0.05, LatencyMS: 45,
				MaxWaitMS: 300000, MakerTouched: true, MakerFilled: false,
				ArtifactBinding: ArtifactBinding{
					MakerArtifactHash: makerHash,
					DepthArtifactHash: makerHash,
				},
			},
			ExitDepthUSDT: 8000, CanExitBeforeMaintenance: true,
			ExtremeExitSlippageBPS: 12, PartialCloseFeasible: true,
		},
		{
			TriggerRecord: TriggerRecord{
				TriggerID: "real_fill_grade_a", DecisionTime: parseTime("2026-07-01T10:30:00Z"),
				Symbol: "BTC-USDT", Venue: "okx", Side: "long", Price: 100000,
				OrderType: "maker", DecisionVisibleInfo: "wal_confirmed",
				MinSz: "1", LotSz: "0.01", CtVal: "0.01", CtType: "linear",
				ExecutableDepthUSDT: 6000, ParticipationRate: 0.04, LatencyMS: 30,
				RealFill: true, MakerFilled: true, HasSettlement: true,
				ArtifactBinding: ArtifactBinding{
					WALArtifactHash:        walHash,
					SettlementArtifactHash: settlementHash,
					DepthArtifactHash:      settlementHash,
				},
			},
			ExitDepthUSDT: 9000, CanExitBeforeMaintenance: true,
			ExtremeExitSlippageBPS: 10, PartialCloseFeasible: true,
		},
		{
			TriggerRecord: TriggerRecord{
				TriggerID: "shadow_discovery_c", DecisionTime: parseTime("2026-07-01T11:00:00Z"),
				Symbol: "ETH-USDT", Venue: "okx", Side: "short", Price: 3500,
				OrderType: "maker", DecisionVisibleInfo: "shadow_executable",
				MinSz: "1", LotSz: "0.01", CtVal: "0.1", CtType: "linear",
				ExecutableDepthUSDT: 2000, ParticipationRate: 0.08, LatencyMS: 90,
				HasShadow: true, ShadowSourceID: "fast_executable_shadow_events",
				ArtifactBinding: ArtifactBinding{
					ShadowSourceHash: shadowHash,
					DepthArtifactHash: shadowHash,
				},
			},
			ExitDepthUSDT: 1500, CanExitBeforeMaintenance: true,
			ExtremeExitSlippageBPS: 20, PartialCloseFeasible: true,
		},
		{
			TriggerRecord: TriggerRecord{
				TriggerID: "inverse_fail_closed", DecisionTime: parseTime("2026-07-01T11:30:00Z"),
				Symbol: "BTC-USD-SWAP", Venue: "okx", Side: "long", Price: 100000,
				OrderType: "maker", DecisionVisibleInfo: "inverse_boundary",
				MinSz: "1", LotSz: "1", CtVal: "100", CtType: "inverse",
				ExecutableDepthUSDT: 1000, ParticipationRate: 0.01, LatencyMS: 50,
				EvidenceGrade: GradePublicConservativeBoundary,
			},
			ExitDepthUSDT: 0, CanExitBeforeMaintenance: false,
			ExtremeExitSlippageBPS: 50, PartialCloseFeasible: false,
		},
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// WriteTriggerFixtures writes triggers JSONL for runner tests.
func WriteTriggerFixtures(path string) error {
	triggers := DefaultFixtureTriggers()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, tr := range triggers {
		if tr.TriggerID == "inverse_fail_closed" {
			continue
		}
		if err := enc.Encode(tr.TriggerRecord); err != nil {
			return err
		}
	}
	return nil
}
