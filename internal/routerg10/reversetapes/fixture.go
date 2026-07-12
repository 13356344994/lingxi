package reversetapes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/residualmap"
)

var entryTriggerFromExec = map[string]string{
	"touch_not_fill_missed_profit": "touch_not_fill",
	"filled_profitable_slow":       "real_fill_grade_a",
	"unfilled_not_profitable":      "shadow_discovery_c",
}

// FixtureBundle is a sealed upstream bundle for path tests.
type FixtureBundle struct {
	Root               string
	ExclusionMapDir    string
	EntryProofDir      string
	ExecutionPolicyDir string
	ExitProofPath      string
	AnnouncementDir    string
}

// WriteFixtures materializes bound upstream artifacts for reverse-tape tests.
func WriteFixtures(root string) (FixtureBundle, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return FixtureBundle{}, err
	}
	exclusionDir := filepath.Join(root, "exclusion")
	entryDir := filepath.Join(root, "entry")
	execDir := filepath.Join(root, "execution")
	annDir := filepath.Join(root, "announcements")
	for _, d := range []string{exclusionDir, entryDir, execDir, annDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return FixtureBundle{}, err
		}
	}

	triggers := g10entryproof.DefaultFixtureTriggers()
	execTriggers := g10executionpolicy.DefaultFixtureTriggers()

	var entryProofs []g10entryproof.EntryProof
	var exitProofs []g10exitproof.Proof
	var policies []g10executionpolicy.FrozenPolicy
	var exclusionProofs []exclusionmap.ExclusionProof

	for _, tr := range triggers {
		if tr.TriggerID == "inverse_fail_closed" {
			continue
		}
		ep, err := g10entryproof.BuildEntryProof(tr.TriggerRecord)
		if err != nil {
			return FixtureBundle{}, err
		}
		entryProofs = append(entryProofs, ep)
		xp, err := g10exitproof.BuildProof(g10exitproof.Input{
			TriggerID: tr.TriggerID, DecisionTime: tr.DecisionTime,
			ExitDepthUSDT: tr.ExitDepthUSDT, CanExitBeforeMaintenance: tr.CanExitBeforeMaintenance,
			ExtremeExitSlippageBPS: tr.ExtremeExitSlippageBPS, PartialCloseFeasible: tr.PartialCloseFeasible,
		})
		if err != nil {
			return FixtureBundle{}, err
		}
		exitProofs = append(exitProofs, xp)
	}
	for _, tr := range execTriggers {
		fp, err := g10executionpolicy.FreezePolicy(tr, execTriggers)
		if err != nil {
			return FixtureBundle{}, err
		}
		if entryID, ok := entryTriggerFromExec[tr.TriggerID]; ok {
			fp.TriggerID = entryID
			hash, err := g10executionpolicy.CanonicalPolicyHash(fp)
			if err != nil {
				return FixtureBundle{}, err
			}
			fp.ExecutionPolicyHash = hash
		}
		policies = append(policies, fp)
	}

	exclusionProofs = append(exclusionProofs,
		buildFixtureExclusionProof("real_fill_grade_a", "BTC-USDT-SWAP", "long", 1, "shock-a"),
		buildFixtureExclusionProof("shadow_discovery_c", "ETH-USDT-SWAP", "short", 0, "shock-b"),
		buildFixtureExclusionProof("touch_not_fill", "BTC-USDT-SWAP", "long", 1, "shock-c"),
	)

	cfg := exclusionmap.RunConfig{
		SchemaVersion: exclusionmap.SchemaVersionConfig,
		HistoryRoot: root, OutputDir: exclusionDir,
		MapDirs: []string{"/map"}, AnnouncementDir: annDir,
		MaxFiles: 1, MaxLines: 100, CandidateLimit: 10,
	}
	constHash := "fixture-constitution"
	exclusionMap, err := exclusionmap.BuildExclusionMap(cfg, constHash, exclusionProofs)
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(exclusionDir, exclusionmap.OutputFileName), exclusionMap); err != nil {
		return FixtureBundle{}, err
	}

	entryCfg := g10entryproof.RunConfig{SchemaVersion: g10entryproof.ConfigSchema, Generation: g10entryproof.Generation, OutputDir: entryDir, MaxLines: 100}
	entryManifest, err := g10entryproof.BuildManifest(entryCfg, entryProofs, map[string]string{"triggers": "FIXTURE"})
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONLFile(filepath.Join(entryDir, g10entryproof.OutputProofsFile), entryProofs); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(entryDir, g10entryproof.OutputManifestFile), entryManifest); err != nil {
		return FixtureBundle{}, err
	}

	execCfg := g10executionpolicy.RunConfig{SchemaVersion: g10executionpolicy.ConfigSchema, Generation: g10executionpolicy.Generation, OutputDir: execDir, MaxLines: 100}
	execManifest, err := g10executionpolicy.BuildManifest(execCfg, policies)
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONLFile(filepath.Join(execDir, g10executionpolicy.OutputPoliciesFile), policies); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(execDir, g10executionpolicy.OutputManifestFile), execManifest); err != nil {
		return FixtureBundle{}, err
	}

	exitPath := filepath.Join(root, "exit_proofs.jsonl")
	if err := writeJSONLFile(exitPath, exitProofs); err != nil {
		return FixtureBundle{}, err
	}

	ledger, err := exclusionmap.BuildLedger(constHash, map[string]string{}, "empty-tape", "ann-m", nil)
	if err != nil {
		return FixtureBundle{}, err
	}
	exclusionManifest, err := exclusionmap.BuildManifest(
		cfg, "catalog", constHash, map[string]string{}, map[string]string{},
		"empty-tape", "ann-m", exclusionMap, ledger, map[string]string{},
	)
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(exclusionDir, exclusionmap.ManifestFileName), exclusionManifest); err != nil {
		return FixtureBundle{}, err
	}

	if err := os.WriteFile(filepath.Join(annDir, announcements.OutputAnnouncementsFile), []byte{}, 0o644); err != nil {
		return FixtureBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(annDir, announcements.OutputChainFile), []byte("{}"), 0o644); err != nil {
		return FixtureBundle{}, err
	}
	emptyTape := announcements.AnnouncementTape{Announcements: []announcements.Announcement{}}
	tapeHash, err := announcements.HashTape(emptyTape.Announcements)
	if err != nil {
		return FixtureBundle{}, err
	}
	emptyTape.TapeHash = tapeHash
	chain, err := announcements.BuildEventChain(nil, announcements.Generation)
	if err != nil {
		return FixtureBundle{}, err
	}
	annCfg := announcements.RunConfig{SchemaVersion: announcements.ConfigSchema, Generation: announcements.Generation, OutputDir: annDir}
	annManifest, err := announcements.BuildManifest(annCfg, emptyTape, chain)
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(annDir, announcements.OutputManifestFile), annManifest); err != nil {
		return FixtureBundle{}, err
	}
	if err := writeJSONFile(filepath.Join(annDir, announcements.OutputChainFile), chain); err != nil {
		return FixtureBundle{}, err
	}

	return FixtureBundle{
		Root: root, ExclusionMapDir: exclusionDir, EntryProofDir: entryDir,
		ExecutionPolicyDir: execDir, ExitProofPath: exitPath, AnnouncementDir: annDir,
	}, nil
}

func buildFixtureExclusionProof(eventID, symbol, direction string, delay int, shock string) exclusionmap.ExclusionProof {
	item := exclusionmap.EventWorkItem{
		ProfileID: "okx_1h", ShockCluster: shock,
		Event: residualmap.TriggeredEventEntry{
			EventID: eventID, Symbol: symbol, EventFamily: "extreme_reversal",
			Horizon: "1h", DecisionTime: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		},
	}
	result := exclusionmap.EvaluationResult{
		Accepted: true, AcceptReason: exclusionmap.AcceptPersonalConditionDependent,
		Direction: direction, Horizon: "1h", DelayBars: delay,
		Institution: exclusionmap.AxisExclusion{Axis: "institution", Excluded: true, PrimaryEvidence: "capacity_cliff"},
		Retail:      exclusionmap.AxisExclusion{Axis: "retail", Excluded: true, PrimaryEvidence: "min_lot_nonlinearity"},
		Bot:         exclusionmap.AxisExclusion{Axis: "bot", Excluded: true, PrimaryEvidence: "event_sparsity"},
		Provenance:  exclusionmap.Provenance{AnnouncementTapeHash: "empty-tape"},
	}
	proof, err := exclusionmap.BuildProof(item, result, "fixture-constitution")
	if err != nil {
		panic(err)
	}
	return proof
}

func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func writeJSONLFile(path string, items any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	switch v := items.(type) {
	case []g10entryproof.EntryProof:
		for _, item := range v {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	case []g10exitproof.Proof:
		for _, item := range v {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	case []g10executionpolicy.FrozenPolicy:
		for _, item := range v {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	}
	return nil
}
