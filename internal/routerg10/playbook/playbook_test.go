package playbook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

func TestWinnerReverseDerivationDeterministic(t *testing.T) {
	groups, thresholds := syntheticGroups()
	limit := 16
	p1, l1, err := Derive(groups, thresholds, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, l2, err := Derive(groups, thresholds, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := HashPlaybooks(p1)
	h2, _ := HashPlaybooks(p2)
	if h1 != h2 {
		t.Fatal("derivation must be deterministic")
	}
	if l1.LedgerHash != l2.LedgerHash {
		t.Fatal("ledger hash drift")
	}
	for _, pb := range p1 {
		if err := ValidatePlaybook(pb); err != nil {
			t.Fatalf("playbook invalid: %v", err)
		}
		if pb.Status != protocol.PersonalWildCandidateStatus {
			t.Fatalf("status must be %s, got %q", protocol.PersonalWildCandidateStatus, pb.Status)
		}
		if pb.ValidationStage != ValidationStagePending {
			t.Fatalf("playbook must await full verification, got %q", pb.ValidationStage)
		}
		if pb.TradeAllowed || pb.ExamEligible {
			t.Fatal("personal wild candidates must not trade or exam")
		}
		if containsPassStatus(pb.Status) || containsPassStatus(pb.ValidationStage) {
			t.Fatal("reverse discovery must not emit pass status")
		}
	}
}

func TestFullSampleFollowUpStage(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, ledger, err := Derive(groups, thresholds, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) == 0 {
		t.Fatal("expected playbooks from synthetic winners")
	}
	for _, pb := range playbooks {
		for _, field := range protocol.PlaybookRequiredFields {
			if err := requirePlaybookField(pb, field); err != nil {
				t.Fatalf("missing required field %s: %v", field, err)
			}
		}
		if pb.EventCompletion == "" || pb.PayerStatus != PayerStatusUnexplained {
			t.Fatalf("playbook missing completion/payer fields: %+v", pb)
		}
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
}

func TestMultiDirectionBranchesPresent(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, _, err := Derive(groups, thresholds, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pb := range playbooks {
		if err := validateBranches(pb); err != nil {
			t.Fatalf("branches invalid: %v", err)
		}
		actions := map[string]bool{
			pb.LongBranch.Action:  true,
			pb.ShortBranch.Action: true,
			pb.FlatBranch.Action:  true,
		}
		if !actions["LONG"] || !actions["SHORT"] || !actions["NO_TRADE"] {
			t.Fatalf("playbook must expose LONG, SHORT, NO_TRADE actions across branches, got %+v %+v %+v",
				pb.LongBranch, pb.ShortBranch, pb.FlatBranch)
		}
	}
}

func TestUnknownTriggerFieldFailClosed(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, _, err := Derive(groups, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) == 0 {
		t.Fatal("expected playbooks")
	}
	bad := playbooks[0]
	bad.TriggerConditions.Conditions = append(bad.TriggerConditions.Conditions, TriggerCondition{
		Field: "winner_label",
		Op:    "eq",
		Value: 1,
	})
	if err := validateTriggerConditions(bad.TriggerConditions); err == nil {
		t.Fatal("winner label in trigger must be rejected")
	}
	if err := ValidatePlaybook(bad); err == nil {
		t.Fatal("playbook with forbidden trigger must fail validation")
	}
}

func TestUnknownTriggerOpFailClosed(t *testing.T) {
	spec := TriggerSpec{
		EventFamily: eventmap.FamilyExtremeContinuation,
		Conditions: []TriggerCondition{{
			Field: "abs_return_bps",
			Op:    "contains",
			Value: 50,
		}},
	}
	if err := validateTriggerConditions(spec); err == nil {
		t.Fatal("unknown trigger op must fail closed")
	}
}

func TestLookaheadRejectionForbiddenTriggerField(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, _, err := Derive(groups, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) == 0 {
		t.Fatal("expected playbooks")
	}
	bad := playbooks[0]
	bad.TriggerConditions.Conditions = append(bad.TriggerConditions.Conditions, TriggerCondition{
		Field: "future_outcome",
		Op:    "eq",
		Value: 1,
	})
	if err := validateTriggerConditions(bad.TriggerConditions); err == nil {
		t.Fatal("future_outcome in trigger must be rejected")
	}
	if err := ValidatePlaybook(bad); err == nil {
		t.Fatal("playbook with lookahead trigger must fail validation")
	}
}

func TestWindowNonOverlapRejectsCollidingWinners(t *testing.T) {
	decision := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	group := ResidualWinnerGroup{
		Key: "test", ProfileID: eventmap.ProfileOKX1H, ManifestHash: "m",
		CellKey: ResidualCellKey{
			EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon:     "1h", DelayBars: 0,
		},
		ShockCluster: "shock-2024-03-01",
		Winners: []ResidualWinnerEntry{
			{EventID: "a", Symbol: "AAA-USDT-SWAP", DecisionTime: decision, EventFamily: eventmap.FamilyExtremeContinuation, Horizon: "1h", UserTierConservativeBPS: 12},
			{EventID: "b", Symbol: "AAA-USDT-SWAP", DecisionTime: decision.Add(30 * time.Minute), EventFamily: eventmap.FamilyExtremeContinuation, Horizon: "1h", UserTierConservativeBPS: 10},
		},
	}
	spec1h, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	if len(NonOverlapViolations(group, eventmap.CooldownBarsForEvent(spec1h, "1h", 0), spec1h.BarDuration)) == 0 {
		t.Fatal("expected non-overlap violation for same symbol/family/date on 1h profile")
	}
	thresholds := map[string]eventmap.FrozenThresholds{
		eventmap.ProfileOKX1H: {ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20},
	}
	playbooks, ledger, err := Derive([]ResidualWinnerGroup{group}, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) != 0 {
		t.Fatalf("non-overlap group must not register playbook, got %d", len(playbooks))
	}
	found := false
	for _, attempt := range ledger.Attempts {
		if attempt.Status == LedgerStatusNonOverlap {
			found = true
		}
	}
	if !found {
		t.Fatal("ledger must record non-overlap skip")
	}
}

func TestFairRoundRobinAcrossFamilyHorizon(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, _, err := Derive(groups, thresholds, 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]bool{}
	horizons := map[string]bool{}
	for _, pb := range playbooks {
		families[pb.EventFamily] = true
		horizons[pb.Horizon] = true
	}
	if len(families) < 2 || len(horizons) < 2 {
		t.Fatalf("fair coverage missing: families=%v horizons=%v", families, horizons)
	}
	ordered := fairGroupOrder(groups)
	if len(ordered) == 0 {
		t.Fatal("expected scheduled groups")
	}
	firstKeys := map[string]bool{}
	for i := 0; i < minInt(8, len(ordered)); i++ {
		firstKeys[ordered[i].CellKey.EventFamily] = true
	}
	if len(firstKeys) < 2 {
		t.Fatal("round-robin must not consume budget from one family prefix only")
	}
}

func TestPlaybookHashAndLedgerTamperDetection(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, ledger, err := Derive(groups, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
	bad := ledger
	bad.Attempts[0].Status = "evil"
	if err := ValidateLedger(bad); err == nil {
		t.Fatal("tampered ledger status must fail")
	}
	pb := playbooks[0]
	pb.PlaybookHash = "deadbeef"
	if err := ValidatePlaybook(pb); err == nil {
		t.Fatal("tampered playbook hash must fail")
	}
}

func TestCheckpointTamperDetection(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: dir, OutputDir: dir,
		ResidualMapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000, PlaybookLimit: 8,
		ProgressEvery: 1000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CatalogHash: "abc", ConfigHash: cfgHash,
		MapManifestHashes: map[string]string{"okx_1h": "m"}, OutputDir: dir, Done: false,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
}

func TestPlaybookLimit64(t *testing.T) {
	if MaxPlaybookCandidates != 64 {
		t.Fatalf("MaxPlaybookCandidates drift: %d", MaxPlaybookCandidates)
	}
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		ResidualMapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 100, PlaybookLimit: 65,
		ProgressEvery: 1, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("playbook_limit above 64 must fail validation")
	}
}

func TestDedupTrackerKeySkipsDuplicateOutcomeHorizonDelay(t *testing.T) {
	decision := time.Date(2024, 4, 1, 8, 0, 0, 0, time.UTC)
	cell := ResidualCellKey{
		EventFamily: eventmap.FamilyExtremeContinuation,
		Horizon:     "1h", DelayBars: 0,
	}
	g1 := ResidualWinnerGroup{
		Key: "g1", ProfileID: eventmap.ProfileOKX1H, ManifestHash: "m", CellKey: cell,
		ShockCluster: "cluster-a",
		Winners: []ResidualWinnerEntry{{
			EventID: "a", Symbol: "AAA-USDT-SWAP", DecisionTime: decision,
			EventFamily: cell.EventFamily, Horizon: cell.Horizon, DelayBars: 0,
			UserTierConservativeBPS: 15, EligibleCapitalTiers: []string{"actual", "30"},
			ShockCluster: "cluster-a",
		}},
	}
	g2 := ResidualWinnerGroup{
		Key: "g2", ProfileID: eventmap.ProfileOKX1H, ManifestHash: "m", CellKey: cell,
		ShockCluster: "cluster-b",
		Winners: []ResidualWinnerEntry{{
			EventID: "b", Symbol: "AAA-USDT-SWAP", DecisionTime: decision.Add(48 * time.Hour),
			EventFamily: cell.EventFamily, Horizon: cell.Horizon, DelayBars: 0,
			UserTierConservativeBPS: 14, EligibleCapitalTiers: []string{"actual"},
			ShockCluster: "cluster-b",
		}},
	}
	thresholds := map[string]eventmap.FrozenThresholds{
		eventmap.ProfileOKX1H: {ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20},
	}
	playbooks, ledger, err := Derive([]ResidualWinnerGroup{g1, g2}, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) != 1 {
		t.Fatalf("dedup must allow only one playbook for same symbol|family|horizon|delay, got %d", len(playbooks))
	}
	found := false
	for _, attempt := range ledger.Attempts {
		if attempt.Status == LedgerStatusDeduped {
			found = true
		}
	}
	if !found {
		t.Fatal("ledger must record dedup skip")
	}
}

func TestResidualMapBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bundle := syntheticResidualBundle(eventmap.ProfileOKX1H, "manifest-a")
	if err := WriteResidualMapBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadResidualMapBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Manifest.ManifestHash == "" {
		t.Fatal("loaded manifest hash must be set")
	}
	if loaded.Manifest.ProfileID != bundle.Manifest.ProfileID {
		t.Fatal("profile mismatch after round trip")
	}
	groups := CollectResidualGroups([]ResidualMapBundle{loaded})
	if len(groups) == 0 {
		t.Fatal("expected groups from loaded bundle")
	}
}

func TestManifestAndOutputArtifacts(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, ledger, err := Derive(groups, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	coverage, coverageHash, err := BuildCoverage(groups, playbooks)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := RunConfig{
		SchemaVersion:   SchemaVersionConfig,
		HistoryRoot:     dir,
		OutputDir:       dir,
		ResidualMapDirs: []string{dir},
		MaxFiles:        10,
		MaxLines:        1000,
		PlaybookLimit:   4,
		ProgressEvery:   100,
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	manifest, err := BuildManifest(
		cfg, protocol.HistoryDataHash,
		map[string]string{"okx_1h": "m"}, map[string]string{"okx_1h/residual_map_thresholds.json": "t"},
		playbooks, ledger, coverage, coverageHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	summary := BuildSummary(groups, playbooks, ledger, manifest, countWinners(groups), 4, "", "")
	if err := WriteOutputs(dir, playbooks, ledger, manifest, summary); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(filepath.Join(dir, "event_playbook_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputArtifacts(dir, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestFailClosedResidualProducesZeroPlaybooks(t *testing.T) {
	dir := t.TempDir()
	bundle := syntheticFailClosedResidualBundle(eventmap.ProfileOKX1H, FailClosedFingerprintCoverage)
	if err := WriteResidualMapBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadResidualMapBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	groups := CollectResidualGroups([]ResidualMapBundle{loaded})
	if countWinners(groups) != 0 {
		t.Fatal("fail-closed bundle must have zero winners")
	}
	playbooks, ledger, err := Derive(groups, map[string]eventmap.FrozenThresholds{
		eventmap.ProfileOKX1H: bundle.Thresholds,
	}, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) != 0 {
		t.Fatalf("playbooks = %d want 0", len(playbooks))
	}
	noCandidatesReason, residualFailClosedReason, err := resolveNoCandidatesBinding(
		[]ResidualMapBundle{loaded}, 0, len(playbooks),
	)
	if err != nil {
		t.Fatal(err)
	}
	if noCandidatesReason != NoCandidatesReason {
		t.Fatalf("no_candidates_reason = %q want %q", noCandidatesReason, NoCandidatesReason)
	}
	if residualFailClosedReason != FailClosedFingerprintCoverage {
		t.Fatalf("residual_fail_closed_reason = %q want %q",
			residualFailClosedReason, FailClosedFingerprintCoverage)
	}
	coverage, coverageHash, err := BuildCoverage(groups, playbooks)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: dir, OutputDir: dir,
		ResidualMapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000, PlaybookLimit: 8,
		ProgressEvery: 100, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	manifest, err := BuildManifest(
		cfg, protocol.HistoryDataHash,
		map[string]string{eventmap.ProfileOKX1H: loaded.Manifest.ManifestHash},
		loaded.Manifest.ArtifactHashes,
		playbooks, ledger, coverage, coverageHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	summary := BuildSummary(groups, playbooks, ledger, manifest, 0, 8, noCandidatesReason, residualFailClosedReason)
	if summary.ActualPlaybookCount != 0 || summary.WinnerSeedCount != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.NoCandidatesReason != NoCandidatesReason {
		t.Fatalf("summary no_candidates_reason = %q", summary.NoCandidatesReason)
	}
	if summary.ResidualFailClosedReason != FailClosedFingerprintCoverage {
		t.Fatalf("summary residual_fail_closed_reason = %q", summary.ResidualFailClosedReason)
	}
	if err := WriteOutputs(dir, playbooks, ledger, manifest, summary); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputArtifacts(dir, manifest); err != nil {
		t.Fatal(err)
	}
}

const FailClosedFingerprintCoverage = "FINGERPRINT_NO_CRITICAL_KNOWN_COVERAGE"

func syntheticFailClosedResidualBundle(profileID, failClosedReason string) ResidualMapBundle {
	thresholds := eventmap.FrozenThresholds{
		ProfileID: profileID, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20,
	}
	manifest := ResidualMapManifest{
		SchemaVersion:            ResidualMapManifestSchema,
		Generation:               protocol.Generation,
		ProtocolHash:             protocol.RouterProtocolHash,
		HistoryDataHash:          protocol.HistoryDataHash,
		ProfileID:                profileID,
		CatalogHash:              protocol.HistoryDataHash,
		CandidateStatus:          protocol.PersonalWildCandidateStatus,
		ResidualTierLabels:       protocol.ResidualTiersUSDT,
		ResidualFailClosedReason: failClosedReason,
	}
	discovery := PartitionResidualMap{
		SchemaVersion: ResidualMapDiscoverySchema,
		ProfileID:     profileID,
		Partition:     eventmap.PartitionDiscovery,
		Cells:         map[string]ResidualMapCell{},
	}
	return ResidualMapBundle{
		Manifest: manifest, Discovery: discovery, Thresholds: thresholds,
	}
}

func syntheticGroups() ([]ResidualWinnerGroup, map[string]eventmap.FrozenThresholds) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	families := []string{
		eventmap.FamilyExtremeContinuation,
		eventmap.FamilyExtremeReversal,
		eventmap.FamilySessionLiquidity,
		eventmap.FamilyBTCRelative,
	}
	horizons := []string{"1h", "4h", "24h"}
	profiles := []string{eventmap.ProfileOKX1H, eventmap.ProfileOKX5M}
	var groups []ResidualWinnerGroup
	for pi, profile := range profiles {
		for fi, family := range families {
			for hi, horizon := range horizons {
				decision := base.Add(time.Duration(pi*72+fi*18+hi*6) * time.Hour)
				key := groupKey(profile, ResidualCellKey{
					EventFamily: family, Horizon: horizon, DelayBars: fi % 2,
				}, fmt.Sprintf("shock-%d", pi*100+fi*10+hi))
				groups = append(groups, ResidualWinnerGroup{
					Key: key, ProfileID: profile, ManifestHash: hashString(profile),
					CellKey: ResidualCellKey{
						EventFamily: family, Horizon: horizon, DelayBars: fi % 2,
					},
					ShockCluster: fmt.Sprintf("shock-%d", pi*100+fi*10+hi),
					Winners: []ResidualWinnerEntry{{
						EventID: eventmap.BuildEventID("AAA-USDT-SWAP", decision, family),
						Symbol:  "AAA-USDT-SWAP", DecisionTime: decision,
						EventFamily: family, Horizon: horizon, DelayBars: fi % 2,
						UserTierConservativeBPS: 12 + float64(fi),
						EligibleCapitalTiers:    []string{"actual", "30", "100"},
						MaxCapacityUSDT:         500,
						ShockCluster:            fmt.Sprintf("shock-%d", pi*100+fi*10+hi),
						MapManifestHash:         hashString(profile),
					}},
					PriorityTier: fi,
				})
			}
		}
	}
	thresholds := map[string]eventmap.FrozenThresholds{
		eventmap.ProfileOKX1H: {
			ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20,
			LowLiquidityQuote: 1e5, VolumeSpikeQuote: 2e5, BTCRelativeAbsBPS: 30,
		},
		eventmap.ProfileOKX5M: {
			ProfileID: eventmap.ProfileOKX5M, ExtremeReturnAbsBPS: 40, ExtremeRMSBPS: 15,
			LowLiquidityQuote: 8e4, VolumeSpikeQuote: 15e4, BTCRelativeAbsBPS: 25,
		},
	}
	return groups, thresholds
}

func syntheticResidualBundle(profileID, manifestLabel string) ResidualMapBundle {
	groups, thresholds := syntheticGroups()
	cells := map[string]ResidualMapCell{}
	for _, group := range groups {
		if group.ProfileID != profileID {
			continue
		}
		cells[group.Key] = ResidualMapCell{Key: group.CellKey, Winners: group.Winners}
	}
	manifest := ResidualMapManifest{
		SchemaVersion:      ResidualMapManifestSchema,
		Generation:         protocol.Generation,
		ProtocolHash:       protocol.RouterProtocolHash,
		HistoryDataHash:    protocol.HistoryDataHash,
		ProfileID:          profileID,
		CatalogHash:        protocol.HistoryDataHash,
		CandidateStatus:    protocol.PersonalWildCandidateStatus,
		ResidualTierLabels: protocol.ResidualTiersUSDT,
	}
	discovery := PartitionResidualMap{
		SchemaVersion: ResidualMapDiscoverySchema,
		ProfileID:     profileID,
		Partition:     eventmap.PartitionDiscovery,
		Cells:         cells,
	}
	_ = manifestLabel
	return ResidualMapBundle{
		Manifest: manifest, Discovery: discovery, Thresholds: thresholds[profileID],
	}
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestCanonicalHashStableAcrossMarshal(t *testing.T) {
	groups, thresholds := syntheticGroups()
	playbooks, _, err := Derive(groups, thresholds, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw1, _ := json.Marshal(playbooks[0])
	raw2, _ := json.Marshal(playbooks[0])
	h1, _, _ := datatruth.HashJSON(json.RawMessage(raw1))
	h2, _, _ := datatruth.HashJSON(json.RawMessage(raw2))
	if h1 != h2 {
		t.Fatal("marshal of same playbook must be stable")
	}
}
