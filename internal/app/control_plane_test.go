package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestControlPlaneDefaultStateAllowsLegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	st := cp.PersistedState()
	if st.GlobalHalt || !st.AllowNewOpens || !st.AllowManageOrders || !st.AllowVerifiedCloses ||
		!st.PerpPerpLiveEnabled || !st.RotationLiveEnabled {
		t.Fatalf("default state should not tighten gates: %+v", st)
	}
	if cp.IsGlobalHaltActive() {
		t.Fatal("default should not be globally halted")
	}
}

func TestControlPlanePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	if err := cp.SetGlobalHalt(true); err != nil {
		t.Fatalf("SetGlobalHalt: %v", err)
	}
	cp2 := NewControlPlane(dir, nil, nil)
	if !cp2.PersistedState().GlobalHalt {
		t.Fatal("reload should restore global_halt=true")
	}
	if err := cp2.SetGlobalHalt(false); err != nil {
		t.Fatalf("clear halt: %v", err)
	}
	cp3 := NewControlPlane(dir, nil, nil)
	if cp3.PersistedState().GlobalHalt {
		t.Fatal("reload should restore global_halt=false")
	}
}

func TestControlPlaneHaltFileEquivalentToGlobalHalt(t *testing.T) {
	dir := t.TempDir()
	haltPath := filepath.Join(dir, ".halt-live")
	if err := os.WriteFile(haltPath, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.NetworkConfig.EmergencyStopFile = ".halt-live"
	cp := NewControlPlane(dir, emergencyStopPaths(cfg), nil)
	if !cp.IsGlobalHaltActive() {
		t.Fatal(".halt-live should activate effective global halt")
	}
	view := cp.BuildStatusView(true, true, true, false)
	if !view.HaltFileActive || !view.EffectiveGlobalHalt {
		t.Fatalf("status view: %+v", view)
	}
}

func TestCrossVenueDefaultControlPlaneDoesNotTightenGates(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	makeExec := func(cp *ControlPlane) *CrossVenueExecutor {
		e := &CrossVenueExecutor{
			cfg: cfg,
			scanner: &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
				Opportunities: []CrossVenueOpportunity{
					{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
				},
			}},
			venues:       map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
			store:        mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
			logger:       testCrossVenueLogger(),
			nowFn:        func() time.Time { return now },
			recordDir:    cfg.NetworkConfig.RecordDir,
			controlPlane: cp,
		}
		e.openingsEnabled.Store(false)
		return e
	}
	withoutCP := makeExec(nil)
	withoutCP.tick(context.Background(), true)
	withDefaultCP := makeExec(NewControlPlane(cfg.NetworkConfig.RecordDir, emergencyStopPaths(cfg), nil))
	withDefaultCP.tick(context.Background(), true)
	if withoutCP.lastCycle.HaltReason != withDefaultCP.lastCycle.HaltReason {
		t.Fatalf("default control plane changed halt reason: without=%q with=%q",
			withoutCP.lastCycle.HaltReason, withDefaultCP.lastCycle.HaltReason)
	}
	if withDefaultCP.lastCycle.HaltReason != "openings_disabled" {
		t.Fatalf("halt reason = %q, want openings_disabled", withDefaultCP.lastCycle.HaltReason)
	}
}

func TestCrossVenueGlobalHaltStopsOpensButManageRuns(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	dir := t.TempDir()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	cp := NewControlPlane(dir, emergencyStopPaths(cfg), nil)
	if err := cp.SetGlobalHalt(true); err != nil {
		t.Fatal(err)
	}
	e := &CrossVenueExecutor{
		cfg: cfg,
		scanner: &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
			},
		}},
		venues:       map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:        mustCrossVenueStore(t, dir),
		logger:       testCrossVenueLogger(),
		nowFn:        func() time.Time { return now },
		recordDir:    dir,
		controlPlane: cp,
	}
	e.openingsEnabled.Store(true)
	e.tick(context.Background(), true)
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("global_halt must block new opens")
	}
}

func TestUnifiedRotationGlobalHaltSkipsScanButManageRuns(t *testing.T) {
	svsp := &fakeRotationExecutor{strategyType: UnifiedStrategySingleVenueSpotPerp}
	r := testUnifiedRotationRunnerWithFakes(t, svsp)
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	if err := cp.SetGlobalHalt(true); err != nil {
		t.Fatal(err)
	}
	r.SetControlPlane(cp)
	r.SetActive(true)
	var scanCalls int32
	r.scanCandidatesFn = func(ctx context.Context, snap CrossVenueScanSnapshot) ([]UnifiedOpportunity, map[string]float64, error) {
		atomic.AddInt32(&scanCalls, 1)
		return nil, map[string]float64{bybit.VenueName: 100}, nil
	}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return RotationPlan{}
	}
	r.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "snap-halt", ScannedAt: r.nowFn().UTC()}}

	r.runCycle(context.Background())
	if scanCalls != 0 {
		t.Fatalf("global_halt scanCalls = %d, want 0", scanCalls)
	}
	if svsp.manageCalls != 1 {
		t.Fatalf("global_halt manageCalls = %d, want 1", svsp.manageCalls)
	}
}

func TestStrategyStopSetsGlobalHalt(t *testing.T) {
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	s := &DashboardServer{controlPlane: cp}
	resp := s.executeStrategyStop()
	found := false
	for _, step := range resp.Steps {
		if step.Step == "control_plane_global_halt_on" && step.Success {
			found = true
		}
	}
	if !found {
		t.Fatalf("stop should set global halt: %+v", resp.Steps)
	}
	if !cp.PersistedState().GlobalHalt {
		t.Fatal("global_halt should be persisted")
	}
}

func TestBuildControlPlaneStatusViewShowsEffectiveState(t *testing.T) {
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	_ = cp.SetGlobalHalt(false)
	s := &DashboardServer{controlPlane: cp}
	view := s.buildControlPlaneStatusView()
	if view.PerpPerpMode != "OFF" || view.RotationMode != "OFF" {
		t.Fatalf("expected OFF modes without live config: %+v", view)
	}
}

func TestControlPlaneBuildStatusViewRotationActiveWithoutGlobalHalt(t *testing.T) {
	dir := t.TempDir()
	cp := NewControlPlane(dir, nil, nil)
	view := cp.BuildStatusView(false, false, true, false)
	if !view.RotationActive || !view.RotationCanOpen {
		t.Fatalf("rotation active with default gates should allow open: %+v", view)
	}
	if view.RotationMode != "LIVE" {
		t.Fatalf("rotation mode = %q, want LIVE", view.RotationMode)
	}
}
