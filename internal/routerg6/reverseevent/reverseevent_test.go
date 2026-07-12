package reverseevent

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
	"lingxi/internal/routerg6/protocol"
)

func TestWinnerReverseDerivationDeterministic(t *testing.T) {
	groups, thresholds := syntheticGroups()
	limit := 16
	c1, l1, err := Derive(groups, thresholds, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, l2, err := Derive(groups, thresholds, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := HashEventCards(c1)
	h2, _ := HashEventCards(c2)
	if h1 != h2 {
		t.Fatal("derivation must be deterministic")
	}
	if l1.LedgerHash != l2.LedgerHash {
		t.Fatal("ledger hash drift")
	}
	for _, card := range c1 {
		if err := ValidateEventCard(card); err != nil {
			t.Fatalf("card invalid: %v", err)
		}
		if card.ValidationStage != ValidationStagePending {
			t.Fatalf("card must await full verification, got %q", card.ValidationStage)
		}
		if card.TradeAllowed || card.ExamEligible {
			t.Fatal("unexplained cards must not trade or exam")
		}
		if containsPassStatus(card.Status) || containsPassStatus(card.ValidationStage) {
			t.Fatal("reverse discovery must not emit pass status")
		}
	}
}

func TestFullSampleFollowUpStage(t *testing.T) {
	groups, thresholds := syntheticGroups()
	cards, ledger, err := Derive(groups, thresholds, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Fatal("expected cards from synthetic winners")
	}
	for _, card := range cards {
		for _, field := range protocol.EventCardRequiredFields {
			if err := requireCardField(card, field); err != nil {
				t.Fatalf("missing required field %s: %v", field, err)
			}
		}
		if card.EventCompletion == "" || card.PayerStatus != PayerStatusUnexplained {
			t.Fatalf("card missing completion/payer fields: %+v", card)
		}
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
}

func TestWindowNonOverlapRejectsCollidingWinners(t *testing.T) {
	decision := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	group := WinnerGroup{
		Key: "test", ProfileID: eventmap.ProfileOKX1H, ManifestHash: "m",
		CellKey: eventmap.CellKey{
			EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon:     "1h", Direction: "long", DelayBars: 0, CapitalTier: 30,
		},
		ShockCluster: "shock-2024-03-01",
		Winners: []eventmap.WinnerEntry{
			{EventID: "a", Symbol: "AAA-USDT-SWAP", DecisionTime: decision, EventFamily: eventmap.FamilyExtremeContinuation, ConservativeBPS: 12},
			{EventID: "b", Symbol: "AAA-USDT-SWAP", DecisionTime: decision.Add(30 * time.Minute), EventFamily: eventmap.FamilyExtremeContinuation, ConservativeBPS: 10},
		},
	}
	spec1h, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	if len(NonOverlapViolations(group, eventmap.CooldownBarsForEvent(spec1h, "1h", 0), spec1h.BarDuration)) == 0 {
		t.Fatal("expected non-overlap violation for same symbol/family/date on 1h profile")
	}
	thresholds := map[string]eventmap.FrozenThresholds{
		eventmap.ProfileOKX1H: {ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20},
	}
	cards, ledger, err := Derive([]WinnerGroup{group}, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("non-overlap group must not register card, got %d", len(cards))
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

func TestFairRoundRobinAcrossFamilyDirectionHorizon(t *testing.T) {
	groups, thresholds := syntheticGroups()
	cards, _, err := Derive(groups, thresholds, 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]bool{}
	directions := map[string]bool{}
	horizons := map[string]bool{}
	for _, card := range cards {
		families[card.EventFamily] = true
		directions[card.Direction] = true
		horizons[card.Horizon] = true
	}
	if len(families) < 2 || !directions["long"] || !directions["short"] || len(horizons) < 2 {
		t.Fatalf("fair coverage missing: families=%v directions=%v horizons=%v", families, directions, horizons)
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

func TestCardHashAndLedgerTamperDetection(t *testing.T) {
	groups, thresholds := syntheticGroups()
	cards, ledger, err := Derive(groups, thresholds, 4, nil)
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
	card := cards[0]
	card.EventCardHash = "deadbeef"
	if err := ValidateEventCard(card); err == nil {
		t.Fatal("tampered card hash must fail")
	}
}

func TestCheckpointTamperDetection(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: dir, OutputDir: dir,
		MapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000, CardLimit: 8,
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

func TestCardLimit256(t *testing.T) {
	if MaxCardCandidates != 256 {
		t.Fatalf("MaxCardCandidates drift: %d", MaxCardCandidates)
	}
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		MapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 100, CardLimit: 257,
		ProgressEvery: 1, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("card_limit above 256 must fail validation")
	}
}

func syntheticGroups() ([]WinnerGroup, map[string]eventmap.FrozenThresholds) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	families := []string{
		eventmap.FamilyExtremeContinuation,
		eventmap.FamilyExtremeReversal,
		eventmap.FamilySessionLiquidity,
		eventmap.FamilyBTCRelative,
	}
	directions := []string{"long", "short"}
	horizons := []string{"1h", "4h", "24h"}
	profiles := []string{eventmap.ProfileOKX1H, eventmap.ProfileOKX5M}
	var groups []WinnerGroup
	for pi, profile := range profiles {
		for fi, family := range families {
			for di, direction := range directions {
				for hi, horizon := range horizons {
					decision := base.Add(time.Duration(pi*24+fi*6+di*3+hi) * time.Hour)
					key := groupKey(profile, eventmap.CellKey{
						EventFamily: family, Horizon: horizon, Direction: direction,
						DelayBars: di % 2, CapitalTier: 30 + hi*10,
					}, fmt.Sprintf("shock-%d", pi*100+fi*10+hi))
					groups = append(groups, WinnerGroup{
						Key: key, ProfileID: profile, ManifestHash: hashString(profile),
						CellKey: eventmap.CellKey{
							EventFamily: family, Horizon: horizon, Direction: direction,
							DelayBars: di % 2, CapitalTier: 30 + hi*10,
						},
						ShockCluster: fmt.Sprintf("shock-%d", pi*100+fi*10+hi),
						Winners: []eventmap.WinnerEntry{{
							EventID:         eventmap.BuildEventID("AAA-USDT-SWAP", decision, family),
							Symbol:          "AAA-USDT-SWAP",
							DecisionTime:    decision,
							EventFamily:     family,
							Horizon:         horizon,
							Direction:       direction,
							DelayBars:       di % 2,
							CapitalTier:     30 + hi*10,
							ConservativeBPS: 12 + float64(fi),
							ShockCluster:    fmt.Sprintf("shock-%d", pi*100+fi*10+hi),
							MapManifestHash: hashString(profile),
						}},
						PriorityTier: fi,
					})
				}
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

func TestSameDayMultipleShockClustersDistinctGroups(t *testing.T) {
	decision := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	cell := eventmap.CellKey{
		EventFamily: eventmap.FamilyExtremeContinuation,
		Horizon:     "1h", Direction: "long", DelayBars: 0, CapitalTier: 30,
	}
	bundle := MapBundle{
		Manifest: eventmap.MapManifest{ProfileID: eventmap.ProfileOKX1H, ManifestHash: "manifest-a"},
		Discovery: eventmap.PartitionMap{
			Cells: map[string]eventmap.PartitionMapCell{
				"k": {
					Key: cell,
					Winners: []eventmap.WinnerEntry{
						{
							EventID: "a", Symbol: "AAA-USDT-SWAP", DecisionTime: decision,
							ConservativeBPS: 12, ShockCluster: "cluster-am",
							MapManifestHash: "manifest-a",
						},
						{
							EventID: "b", Symbol: "BBB-USDT-SWAP", DecisionTime: decision.Add(time.Hour),
							ConservativeBPS: 10, ShockCluster: "cluster-pm",
							MapManifestHash: "manifest-a",
						},
					},
				},
			},
		},
	}
	groups := CollectWinnerGroups([]MapBundle{bundle})
	if len(groups) != 2 {
		t.Fatalf("same UTC day with different shock clusters must produce 2 groups, got %d", len(groups))
	}
	for _, group := range groups {
		if group.ShockCluster == "" || group.ShockCluster == "none" {
			t.Fatalf("group must carry shock cluster, got %q", group.ShockCluster)
		}
		if group.ManifestHash != "manifest-a" {
			t.Fatalf("group manifest binding missing: %q", group.ManifestHash)
		}
		if len(group.Winners) == 0 || group.Winners[0].ShockCluster == "" {
			t.Fatal("winner entry must carry shock cluster")
		}
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

func TestManifestSelfHash(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		MapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 100, CardLimit: 4,
		ProgressEvery: 1, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	groups, thresholds := syntheticGroups()
	cards, ledger, err := Derive(groups, thresholds, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	coverage, coverageHash, err := BuildCoverage(groups, cards)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(cfg, protocol.HistoryDataHash, map[string]string{"okx_1h": "m"}, nil, cards, ledger, coverage, coverageHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EventCardManifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.ManifestHash = ""
	want, _, err := datatruth.HashJSON(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestHash != want {
		t.Fatal("manifest self-hash mismatch")
	}
}
