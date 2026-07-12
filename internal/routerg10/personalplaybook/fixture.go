package personalplaybook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/reversetapes"
)

// FixtureBundle is a sealed reverse-tapes output for path tests.
type FixtureBundle struct {
	Root              string
	ReverseTapesDir   string
	ConstitutionHash  string
	CertifiedTapes    []reversetapes.ExecutableProfitTape
	DiscoveryTapes    []reversetapes.ExecutableProfitTape
}

// WriteFixtures materializes reverse-tapes outputs with certified and discovery tapes.
func WriteFixtures(root string) (FixtureBundle, error) {
	dir := filepath.Join(root, "reverse_tapes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return FixtureBundle{}, err
	}

	constHash, err := g10constitution.Hash()
	if err != nil {
		return FixtureBundle{}, err
	}
	certified := []reversetapes.ExecutableProfitTape{
		syntheticCertifiedTape(constHash, "evt-a", "BTC-USDT-SWAP", reversetapes.FamilyAnnouncementShock, "shock-a",
			g10executionpolicy.ActionBoundedMaker, "capacity_cliff", "min_lot_nonlinearity", "event_sparsity"),
		syntheticCertifiedTape(constHash, "evt-b", "ETH-USDT-SWAP", reversetapes.FamilyMinLotNonlinearity, "shock-b",
			g10executionpolicy.ActionConditionalMarketableLimit, "absolute_profit_insufficient", "long_idle_required", "queue_crowding"),
		syntheticNotPersonalTape(constHash, "evt-c", "DOGE-USDT-SWAP", reversetapes.FamilyUnknown, "",
			g10executionpolicy.ActionBoundedMaker, "", "", ""),
	}
	discovery := []reversetapes.ExecutableProfitTape{
		syntheticDiscoveryTape(constHash, "evt-d", "SOL-USDT-SWAP", reversetapes.FamilyRareDynamicAbandon),
	}

	for i := range certified {
		hash, err := hashTapePayload(certified[i])
		if err != nil {
			return FixtureBundle{}, err
		}
		certified[i].TapeHash = hash
	}
	for i := range discovery {
		hash, err := hashTapePayload(discovery[i])
		if err != nil {
			return FixtureBundle{}, err
		}
		discovery[i].TapeHash = hash
	}

	ledger, err := reversetapes.BuildLedger(
		constHash, "map-hash", "ex-manifest", "entry-manifest", "exec-manifest", "ann-tape", "ann-manifest",
		nil,
	)
	if err != nil {
		return FixtureBundle{}, err
	}
	manifest, err := reversetapes.BuildManifest(
		reversetapes.RunConfig{
			SchemaVersion: reversetapes.SchemaVersionConfig,
			OutputDir:     dir, CandidateLimit: 10,
			HistoryDataHash: g10protocolv2.HistoryDataHash,
		},
		g10protocolv2.HistoryDataHash, constHash,
		reversetapes.InputBundle{ConstitutionHash: constHash},
		ledger, map[string]string{},
		len(certified), len(discovery), 0,
	)
	if err != nil {
		return FixtureBundle{}, err
	}

	if err := writeJSONFile(filepath.Join(dir, reversetapes.LedgerFileName), ledger); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(dir, reversetapes.ManifestFileName), manifest); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeTapesJSONL(filepath.Join(dir, reversetapes.OutputTapesFile), certified); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeTapesJSONL(filepath.Join(dir, reversetapes.OutputDiscoveryFile), discovery); err != nil {
		return FixtureBundle{}, err
	}

	return FixtureBundle{
		Root: root, ReverseTapesDir: dir, ConstitutionHash: constHash,
		CertifiedTapes: certified, DiscoveryTapes: discovery,
	}, nil
}

func syntheticCertifiedTape(constHash, eventID, symbol, family, shock, action, inst, retail, bot string) reversetapes.ExecutableProfitTape {
	return syntheticTape(constHash, eventID, symbol, family, shock, action, inst, retail, bot, reversetapes.TapeStatusExecutableCertified, true)
}

func syntheticNotPersonalTape(constHash, eventID, symbol, family, shock, action, inst, retail, bot string) reversetapes.ExecutableProfitTape {
	return syntheticTape(constHash, eventID, symbol, family, shock, action, inst, retail, bot, reversetapes.TapeStatusExecutableCertified, true)
}

func syntheticDiscoveryTape(constHash, eventID, symbol, family string) reversetapes.ExecutableProfitTape {
	t := syntheticTape(constHash, eventID, symbol, family, "shock-d", g10executionpolicy.ActionBoundedMaker,
		"capacity_cliff", "min_lot_nonlinearity", "event_sparsity",
		reversetapes.TapeStatusDiscoveryOnly, false)
	t.RealizedFill = false
	return t
}

func syntheticTape(constHash, eventID, symbol, family, shock, action, inst, retail, bot, status string, fill bool) reversetapes.ExecutableProfitTape {
	decision := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	return reversetapes.ExecutableProfitTape{
		SchemaVersion:      reversetapes.SchemaVersionTape,
		Generation:         g10protocolv2.Generation,
		ProtocolHash:       g10protocolv2.RouterProtocolHash,
		EventID:            eventID,
		Symbol:             symbol,
		ProfileID:          "okx_1h",
		EventFamily:        "extreme_reversal",
		PersonalTapeFamily: family,
		Horizon:            "1h",
		DecisionTime:       decision,
		ShockCluster:       shock,
		Status:             status,
		ProofBinding: reversetapes.ProofBinding{
			ConstitutionHash:     constHash,
			ExclusionProofHash:   "exclusion-" + eventID,
			EntryProofHash:       "entry-" + eventID,
			ExecutionPolicyHash:  "exec-" + eventID,
			ExitProofHash:        "exit-" + eventID,
			AnnouncementTapeHash: "ann-tape",
		},
		DecisionBefore: reversetapes.DecisionBeforeState{
			Direction:              "LONG",
			DelayBars:              0,
			FrozenExecutionAction:  action,
			DecisionVisibleInfo:    "fixture_visible_info",
			DynamicMinNotionalUSDT: 5,
			ExecutableDepthUSDT:    500,
			VisibleAnnouncementIDs: []string{"ann-1"},
			InstitutionExclude:     inst,
			RetailExclude:          retail,
			BotExclude:             bot,
			MakerQuadrant:          g10executionpolicy.QuadrantFilledProfitable,
			NoChaseAfterMiss:       true,
			NoLongLivedMaker:       true,
		},
		OutcomeLabel: reversetapes.OutcomeLabel{
			ConservativeNetBPS: 25, LabelOnly: true, MakerFilled: fill,
		},
		AbsoluteProfitUSDT: 12,
		MaxCapacityUSDT:    200,
		MaxDrawdownUSDT:    0,
		TerminalLossUSDT:   0,
		RealizedFill:       fill,
		DiscoveryMode:      DiscoveryMode,
	}
}

func hashTapePayload(tape reversetapes.ExecutableProfitTape) (string, error) {
	payload := tape
	payload.TapeHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func writeTapesJSONL(path string, tapes []reversetapes.ExecutableProfitTape) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, tape := range tapes {
		if err := enc.Encode(tape); err != nil {
			return err
		}
	}
	return nil
}
