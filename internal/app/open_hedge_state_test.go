package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssessPersistedOpenHedgeExposure(t *testing.T) {
	entries := []persistedOpenHedgeEntry{
		{
			Symbol:             "BTCUSDT",
			Pair:               "BTCUSDT",
			SpotBaseAsset:      "BTC",
			HedgeQty:           0.01,
			ReferenceSpotPrice: 100000,
		},
	}

	assessment := assessPersistedOpenHedgeExposure(
		entries,
		map[string]float64{"BTC": 0.0102},
		map[string]float64{"BTCUSDT": -0.0101},
		nil,
		0.0001,
		10,
	)
	if !assessment.HasExposure {
		t.Fatal("expected residual exposure to be detected")
	}
	if len(assessment.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(assessment.Issues))
	}
	if len(assessment.Labels) != 1 || !strings.Contains(assessment.Labels[0], "BTCUSDT") {
		t.Fatalf("expected dashboard label for BTCUSDT, got %+v", assessment.Labels)
	}
}

func TestPersistedOpenHedgeLifecycleRoundTrip(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), liveOpenHedgesStateName)

	hedge := OpenHedge{
		Target:   FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"},
		HedgeQty: 0.01,
	}
	hedge.markPendingValidation("opened")
	hedge.markActive("validated")

	entries := buildPersistedOpenHedgeEntries([]OpenHedge{hedge}, "run-1", "cycle-1")
	if entries[0].LifecycleState != OpenHedgeLifecycleActive {
		t.Fatalf("expected persisted lifecycle active, got %q", entries[0].LifecycleState)
	}
	if entries[0].LifecycleReason != "validated" {
		t.Fatalf("expected persisted lifecycle reason, got %q", entries[0].LifecycleReason)
	}

	if err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{Hedges: entries}); err != nil {
		t.Fatalf("write error: %v", err)
	}
	loaded, err := loadPersistedOpenHedgeState(statePath)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if len(loaded.Hedges) != 1 || loaded.Hedges[0].LifecycleState != OpenHedgeLifecycleActive {
		t.Fatalf("lifecycle not preserved across persist/load: %+v", loaded.Hedges)
	}

	// 重启重建：恢复的对冲应携带权威生命周期态（active），而非未知/空闲态。
	rebuilt := rebuildOpenHedgeFromEntry(loaded.Hedges[0])
	if rebuilt.lifecycleOrderGate() != OpenHedgeLifecycleActive {
		t.Fatalf("rebuilt hedge should restore active lifecycle, got %q", rebuilt.lifecycleOrderGate())
	}
}

func TestPersistedOpenHedgeLegacyDefaultsActive(t *testing.T) {
	// 旧台账无 lifecycle_state 字段（零值）→ 退化为 active，避免重启后被当作"空闲"而误判可开新仓。
	legacy := persistedOpenHedgeEntry{Symbol: "ETHUSDT", Pair: "ETHUSDT", SpotBaseAsset: "ETH", HedgeQty: 0.1}
	if legacy.lifecycleOrDefault() != OpenHedgeLifecycleActive {
		t.Fatalf("legacy entry should default to active, got %q", legacy.lifecycleOrDefault())
	}
	rebuilt := rebuildOpenHedgeFromEntry(legacy)
	if rebuilt.LifecycleState != OpenHedgeLifecycleActive {
		t.Fatalf("rebuilt legacy hedge should be active, got %q", rebuilt.LifecycleState)
	}
}

func TestReconcilePersistedOpenHedgesClearsStaleState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, liveOpenHedgesStateName)
	err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{
		RunID: "run-1",
		Hedges: []persistedOpenHedgeEntry{
			{
				Symbol:             "ETHUSDT",
				Pair:               "ETHUSDT",
				SpotBaseAsset:      "ETH",
				HedgeQty:           0.1,
				ReferenceSpotPrice: 3000,
			},
		},
	})
	if err != nil {
		t.Fatalf("writePersistedOpenHedgeState() error = %v", err)
	}

	engine := &FundingRateSniperEngine{
		cfg: EngineConfig{
			RiskConfig: RiskConfig{
				ResidualPositionAlertQty: 0.0001,
				ResidualSpotAlertUSDT:    10,
			},
		},
		openHedgeStatePath: statePath,
		logger:             log.New(os.Stdout, "", 0),
	}

	if err := engine.reconcilePersistedOpenHedges(map[string]float64{"ETH": 0}, map[string]float64{"ETHUSDT": 0}); err != nil {
		t.Fatalf("reconcilePersistedOpenHedges() error = %v", err)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected stale open hedge state to be removed, statErr=%v", statErr)
	}
}

func TestReconcilePersistedOpenHedgesBlocksResidualExposure(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, liveOpenHedgesStateName)
	err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{
		RunID: "run-2",
		Hedges: []persistedOpenHedgeEntry{
			{
				Symbol:             "SOLUSDT",
				Pair:               "SOLUSDT",
				SpotBaseAsset:      "SOL",
				HedgeQty:           2,
				ReferenceSpotPrice: 150,
			},
		},
	})
	if err != nil {
		t.Fatalf("writePersistedOpenHedgeState() error = %v", err)
	}

	engine := &FundingRateSniperEngine{
		cfg: EngineConfig{
			RiskConfig: RiskConfig{
				ResidualPositionAlertQty: 0.0001,
				ResidualSpotAlertUSDT:    10,
			},
		},
		openHedgeStatePath: statePath,
	}

	err = engine.reconcilePersistedOpenHedges(map[string]float64{"SOL": 2.1}, map[string]float64{"SOLUSDT": -2})
	if err == nil {
		t.Fatal("expected reconcilePersistedOpenHedges() to block residual exposure")
	}
	if !strings.Contains(err.Error(), "SOLUSDT") {
		t.Fatalf("expected error to mention SOLUSDT, got %v", err)
	}
}
