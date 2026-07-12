package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg2/sky"
	g3candidates "lingxi/internal/routerg3/candidates"
	"lingxi/internal/routerg3/protocol"
	g3research "lingxi/internal/routerg3/research"
	"lingxi/internal/routerresearch"
)

var (
	verifyCanonicalForwardWF = defaultVerifyCanonicalForwardWF
	validateCatalog          = historydata.ValidateCatalog
)

type attributionReportFile struct {
	SchemaVersion        string                             `json:"schema_version"`
	DiscoveryReceiptHash string                             `json:"discovery_receipt_hash"`
	RecheckDataHash      string                             `json:"recheck_data_hash"`
	Decisions            []g3candidates.AttributionDecision `json:"decisions"`
	Attributed           int                                `json:"attributed"`
	NarrativeOnly        int                                `json:"narrative_only"`
	Ambiguous            int                                `json:"ambiguous"`
	Rejected             int                                `json:"rejected"`
	Closed               int                                `json:"closed"`
	ExamActions          int                                `json:"exam_actions"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("router-g3-research", flag.ContinueOnError)
	bundleDir := fs.String("bundle-dir", "", "G3 events research bundle directory")
	discoveryDir := fs.String("discovery-dir", "", "G3 discovery output directory")
	inductionDir := fs.String("induction-dir", "", "G3 induction output directory with sealed search manifest")
	historyRoot := fs.String("history-root", "", "canonical research-only history root")
	catalogPath := fs.String("history-catalog", "", "canonical HistoryCatalog JSON")
	outputDir := fs.String("output-dir", "", "explicit research output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum event lines to read per partition; unbounded scanning is forbidden")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", "HMAC key env for event+search manifests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bundleDir == "" || *discoveryDir == "" || *inductionDir == "" ||
		*historyRoot == "" || *catalogPath == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("--bundle-dir, --discovery-dir, --induction-dir, --history-root, --history-catalog, --output-dir and --checkpoint are required")
	}
	if *maxLines <= 0 || *maxLines > protocol.ReverseInductionMaxLines {
		return errors.New("router-g3-research: --max-lines must be a positive integer within frozen bound")
	}
	signingKey := []byte(os.Getenv(*keyEnv))
	if len(signingKey) < 32 {
		return fmt.Errorf("HMAC key environment variable %s must be >=32 bytes", *keyEnv)
	}

	absoluteBundle, err := filepath.Abs(*bundleDir)
	if err != nil {
		return err
	}
	absoluteDiscovery, err := filepath.Abs(*discoveryDir)
	if err != nil {
		return err
	}
	absoluteInduction, err := filepath.Abs(*inductionDir)
	if err != nil {
		return err
	}
	absoluteOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}

	catalog, err := loadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	if err := validateCatalog(catalog); err != nil {
		return err
	}
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("router-g3-research: history catalog data hash mismatch")
	}

	var eventManifest g3reverse.Manifest
	if err := readJSON(filepath.Join(absoluteBundle, "event_manifest.json"), &eventManifest); err != nil {
		return err
	}
	if err := g3reverse.VerifyManifestSignature(eventManifest, signingKey); err != nil {
		return err
	}
	if eventManifest.Generation != protocol.Generation || eventManifest.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("router-g3-research: event manifest protocol mismatch")
	}
	if eventManifest.SourceDataHash != catalog.DataHash {
		return errors.New("router-g3-research: event manifest catalog binding mismatch")
	}
	eventManifestFileHash, err := hashJSONFile(filepath.Join(absoluteBundle, "event_manifest.json"))
	if err != nil {
		return err
	}

	var boundaries g3reverse.Boundaries
	if err := readJSON(filepath.Join(absoluteBundle, "partition_boundaries.json"), &boundaries); err != nil {
		return err
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		return err
	}
	if boundariesHash != eventManifest.PartitionBoundariesHash {
		return errors.New("router-g3-research: partition boundaries hash mismatch")
	}

	var cutpoints sky.Cutpoints
	if err := readJSON(filepath.Join(absoluteBundle, "sky_cutpoints.json"), &cutpoints); err != nil {
		return err
	}
	if !cutpoints.Frozen {
		return errors.New("router-g3-research: frozen sky cutpoints are required")
	}

	var searchManifest g3reverse.SearchManifest
	if err := readJSON(filepath.Join(absoluteInduction, "search_manifest.json"), &searchManifest); err != nil {
		return err
	}
	if err := g3reverse.VerifySearchManifestSignature(searchManifest, signingKey); err != nil {
		return err
	}
	if searchManifest.EventManifestHash != eventManifest.ManifestHash {
		return errors.New("router-g3-research: search manifest event binding mismatch")
	}
	searchManifestFileHash, err := hashJSONFile(filepath.Join(absoluteInduction, "search_manifest.json"))
	if err != nil {
		return err
	}

	var ledger g3reverse.AlphaLedger
	if err := readJSON(filepath.Join(absoluteInduction, "alpha_ledger.json"), &ledger); err != nil {
		return err
	}
	if err := ledger.Validate(); err != nil {
		return err
	}
	if ledger.LedgerHash != searchManifest.AlphaLedgerHash {
		return errors.New("router-g3-research: ledger hash mismatch")
	}

	var rules []g3reverse.Rule
	if err := readJSON(filepath.Join(absoluteInduction, "rules.json"), &rules); err != nil {
		return err
	}
	rulesHash, err := g3reverse.HashRules(rules)
	if err != nil {
		return err
	}
	if rulesHash != searchManifest.CanonicalDerivedRulesHash {
		return errors.New("router-g3-research: rules hash mismatch")
	}

	var recheckReport g3reverse.RecheckReport
	if err := readJSON(filepath.Join(absoluteDiscovery, "recheck_report.json"), &recheckReport); err != nil {
		return err
	}
	recheckReportHash, err := hashJSONFile(filepath.Join(absoluteDiscovery, "recheck_report.json"))
	if err != nil {
		return err
	}

	var nominationsPayload struct {
		Nominations []g3reverse.Nomination `json:"nominations"`
	}
	if err := readJSON(filepath.Join(absoluteDiscovery, "nominations.json"), &nominationsPayload); err != nil {
		return err
	}
	nominationsHash, err := hashJSONFile(filepath.Join(absoluteDiscovery, "nominations.json"))
	if err != nil {
		return err
	}
	if recheckReport.SearchManifestHash != searchManifest.ManifestHash {
		return errors.New("router-g3-research: recheck report search manifest mismatch")
	}
	inductionEvaluationHash, err := hashJSONFile(filepath.Join(absoluteDiscovery, "induction_evaluation.json"))
	if err != nil {
		return err
	}
	controlSummaryHash, err := hashJSONFile(filepath.Join(absoluteDiscovery, "control_summary.json"))
	if err != nil {
		return err
	}
	var discoveryReceipt g3reverse.DiscoveryReceipt
	if err := readJSON(filepath.Join(absoluteDiscovery, "discovery_receipt.json"), &discoveryReceipt); err != nil {
		return err
	}
	if err := g3reverse.VerifyDiscoveryReceipt(discoveryReceipt, signingKey); err != nil {
		return err
	}
	if discoveryReceipt.EventManifestHash != eventManifestFileHash ||
		discoveryReceipt.SearchManifestHash != searchManifestFileHash ||
		discoveryReceipt.InductionEvaluationHash != inductionEvaluationHash ||
		discoveryReceipt.RecheckReportHash != recheckReportHash ||
		discoveryReceipt.NominationsHash != nominationsHash ||
		discoveryReceipt.ControlSummaryHash != controlSummaryHash {
		return errors.New("router-g3-research: discovery receipt artifact binding mismatch")
	}

	forwardEvents, err := readBoundedEventJSONL(filepath.Join(absoluteBundle, "g3_forward", "events.jsonl"), *maxLines)
	if err != nil {
		return err
	}
	if err := validateEventsInWindow(forwardEvents, boundaries.ForwardStart, boundaries.ForwardEnd, "forward"); err != nil {
		return err
	}
	forwardHash, err := g3reverse.HashEvents(forwardEvents)
	if err != nil {
		return err
	}
	if forwardHash != eventManifest.ForwardEventHash {
		return errors.New("router-g3-research: forward event hash mismatch")
	}

	wfEvents, err := readBoundedEventJSONL(filepath.Join(absoluteBundle, "g3_wf", "events.jsonl"), *maxLines)
	if err != nil {
		return err
	}
	if err := validateEventsInWindow(wfEvents, boundaries.WFStart, boundaries.End, "walk_forward"); err != nil {
		return err
	}
	wfHash, err := g3reverse.HashEvents(wfEvents)
	if err != nil {
		return err
	}
	if wfHash != eventManifest.WalkForwardEventHash {
		return errors.New("router-g3-research: walk-forward event hash mismatch")
	}

	if err := verifyCanonicalForwardWF(ctx, *historyRoot, catalog, absoluteBundle, boundaries, cutpoints, eventManifest); err != nil {
		return err
	}

	configHash, err := configFingerprint(*maxLines)
	if err != nil {
		return err
	}
	inputHash, err := inputFingerprint(
		eventManifest.ManifestHash, searchManifest.ManifestHash, discoveryReceipt.ReceiptHash, rulesHash, ledger.LedgerHash,
		recheckReportHash, nominationsHash, forwardHash, wfHash, boundariesHash, catalog.DataHash,
	)
	if err != nil {
		return err
	}
	cpBinding := checkpointBinding{
		EventManifestHash: eventManifest.ManifestHash, SearchManifestHash: searchManifest.ManifestHash,
		DiscoveryReceiptHash: discoveryReceipt.ReceiptHash,
		RulesHash:            rulesHash, LedgerHash: ledger.LedgerHash,
		RecheckReportHash: recheckReportHash, NominationsHash: nominationsHash,
		ForwardEventHash: forwardHash, WalkForwardEventHash: wfHash,
		PartitionBoundariesHash: boundariesHash, CatalogDataHash: catalog.DataHash,
		ConfigHash: configHash, InputHash: inputHash, OutputDir: absoluteOutput, MaxLines: *maxLines,
	}

	cp, exists, err := loadCheckpoint(*checkpointPath)
	if err != nil {
		return err
	}
	if exists {
		if err := cp.validateBinding(cpBinding); err != nil {
			return err
		}
		if cp.Done {
			if err := verifyCompletedOutputs(absoluteOutput, cp); err != nil {
				return err
			}
			summary, _ := readStageSummary(filepath.Join(absoluteOutput, "g3_stage_summary.json"))
			fmt.Printf("{\"resumed\":true,\"research_pass\":%d,\"system_status\":%q,\"exam_actions\":%d}\n",
				summary.ResearchPassLeaves, summary.SystemStatus, protocol.ExamActions)
			return nil
		}
	} else {
		cp = newCheckpoint(cpBinding)
	}

	rulesByHash := map[string]g3reverse.Rule{}
	for _, rule := range rules {
		if rule.CanonicalHash == "" || rulesByHash[rule.CanonicalHash].CanonicalHash != "" {
			return errors.New("router-g3-research: frozen rules must have unique canonical hashes")
		}
		rulesByHash[rule.CanonicalHash] = rule
	}
	verdictByHash := map[string]g3reverse.RecheckVerdict{}
	for _, verdict := range recheckReport.Verdicts {
		if verdict.CanonicalRuleHash == "" || verdictByHash[verdict.CanonicalRuleHash].CanonicalRuleHash != "" {
			return errors.New("router-g3-research: recheck verdicts must have unique rule hashes")
		}
		verdictByHash[verdict.CanonicalRuleHash] = verdict
	}

	candidateRegistry := registry.Registry{
		SchemaVersion: "lingxi.router-candidates.v1",
		Candidates:    []registry.Candidate{},
	}
	var candidateRules []g3research.CandidateRule
	attributionReport := attributionReportFile{
		SchemaVersion:        "lingxi.router-g3-attribution.v1",
		DiscoveryReceiptHash: discoveryReceipt.ReceiptHash,
		RecheckDataHash:      recheckReport.RecheckDataHash,
		Decisions:            []g3candidates.AttributionDecision{},
		ExamActions:          protocol.ExamActions,
	}
	now := time.Now().UTC()
	seenNominations := map[string]bool{}
	for _, nomination := range nominationsPayload.Nominations {
		if nomination.CanonicalRuleHash == "" || seenNominations[nomination.CanonicalRuleHash] {
			return errors.New("router-g3-research: nominations must have unique rule hashes")
		}
		seenNominations[nomination.CanonicalRuleHash] = true
		verdict, ok := verdictByHash[nomination.CanonicalRuleHash]
		if !ok {
			return fmt.Errorf("router-g3-research: nomination %s missing recheck verdict", nomination.CanonicalRuleHash)
		}
		if verdict.Status != g3reverse.RecheckVerdictPass {
			continue
		}
		rule, ok := rulesByHash[nomination.CanonicalRuleHash]
		if !ok {
			return fmt.Errorf("router-g3-research: missing frozen rule for %s", nomination.CanonicalRuleHash)
		}
		def, decision, err := g3candidates.DecideNomination(nomination, verdict, rule, searchManifest)
		if err != nil {
			return err
		}
		attributionReport.Decisions = append(attributionReport.Decisions, decision)
		switch decision.Status {
		case g3candidates.StatusAttributed:
			attributionReport.Attributed++
		case g3candidates.StatusNarrativeOnly:
			attributionReport.NarrativeOnly++
			attributionReport.Closed++
			continue
		case g3candidates.StatusAmbiguous:
			attributionReport.Ambiguous++
			attributionReport.Closed++
			continue
		case g3candidates.StatusRejected:
			attributionReport.Rejected++
			attributionReport.Closed++
			continue
		default:
			return fmt.Errorf("router-g3-research: unknown attribution status %q", decision.Status)
		}
		candidateRegistry, err = g3candidates.Register(candidateRegistry, def, now)
		if err != nil {
			return err
		}
		id := g3candidates.CanonicalID(def)
		candidateRegistry, err = registry.Freeze(candidateRegistry, id, now)
		if err != nil {
			return err
		}
		candidateRules = append(candidateRules, g3research.CandidateRule{CandidateID: id, Rule: rule})
	}

	researchReport, err := g3research.Run(candidateRules, forwardEvents, wfEvents, g3research.Boundary{
		ForwardStart: boundaries.ForwardStart, ForwardEnd: boundaries.ForwardEnd,
		WFStart: boundaries.WFStart, End: boundaries.End,
	}, g3research.Options{
		MinForwardSamples: protocol.G3MinForwardSamples, MinForwardClusters: protocol.G3MinForwardClusters,
		ForwardAlpha: protocol.ForwardAlpha,
		WF: routerresearch.Options{
			Folds:      protocol.G3WalkForwardFolds,
			Purge:      time.Duration(protocol.G3PurgeHours) * time.Hour,
			Embargo:    time.Duration(protocol.G3WFEmbargoHours) * time.Hour,
			MinSamples: protocol.G3WFMinSamples, MinClusters: protocol.G3WFMinClusters,
			FamilyWiseAlpha: protocol.WalkForwardAlpha, ResearchDataHash: wfHash,
		},
	})
	if err != nil {
		return err
	}

	resultByID := map[string]g3research.Result{}
	closedCount := attributionReport.Closed
	inconclusiveCount := 0
	for _, result := range researchReport.Results {
		resultByID[result.CandidateID] = result
		switch {
		case result.Status == "RESEARCH_PASS":
		case result.Reason == "INCONCLUSIVE":
			inconclusiveCount++
		default:
			closedCount++
		}
	}
	researchPass := 0
	for _, candidate := range append([]registry.Candidate(nil), candidateRegistry.Candidates...) {
		result, ok := resultByID[candidate.ID]
		if !ok {
			candidateRegistry, err = registry.RecordForwardFalsification(
				candidateRegistry, candidate.ID, false, recheckReport.RecheckDataHash, "INCONCLUSIVE", now,
			)
		} else {
			pass := result.Status == "RESEARCH_PASS"
			reason := result.Reason
			if pass {
				reason = "PASS"
				researchPass++
			}
			evidence := result.ForwardEvidenceHash
			if evidence == "" {
				evidence = recheckReport.RecheckDataHash
			}
			candidateRegistry, err = registry.RecordForwardFalsification(
				candidateRegistry, candidate.ID, pass, evidence, reason, now,
			)
		}
		if err != nil {
			return err
		}
	}

	systemStatus := "NO_GO"
	if researchPass > 0 {
		systemStatus = "RESEARCH_PASS"
	}
	stageSummary := stageSummaryFile{
		SchemaVersion: "lingxi.router-g3-stage.v1", Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, RegisteredCandidates: len(candidateRegistry.Candidates),
		FrozenRuleCount: len(rules), RecheckPassCount: len(attributionReport.Decisions),
		AttributedCount: attributionReport.Attributed, NarrativeOnlyCount: attributionReport.NarrativeOnly,
		AmbiguousCount: attributionReport.Ambiguous, RejectedCount: attributionReport.Rejected,
		ClosedCount: closedCount, InconclusiveCount: inconclusiveCount,
		ResearchPassLeaves: researchPass, SystemStatus: systemStatus, ExamActions: protocol.ExamActions,
		EventManifestHash: eventManifest.ManifestHash, SearchManifestHash: searchManifest.ManifestHash,
		DiscoveryReceiptHash: discoveryReceipt.ReceiptHash, RecheckDataHash: recheckReport.RecheckDataHash,
		RecheckReportHash: recheckReportHash, ForwardEventHash: forwardHash, WalkForwardEventHash: wfHash,
	}

	if err := os.MkdirAll(absoluteOutput, 0o755); err != nil {
		return err
	}
	registryPath := filepath.Join(absoluteOutput, "g3_candidate_registry.json")
	if err := registry.Save(registryPath, candidateRegistry); err != nil {
		return err
	}
	candidateRegistry, err = registry.Load(registryPath)
	if err != nil {
		return err
	}
	artifacts := map[string]any{
		"g3_attribution_report.json": attributionReport,
		"g3_research_report.json": struct {
			SchemaVersion        string            `json:"schema_version"`
			ProtocolHash         string            `json:"protocol_hash"`
			DiscoveryReceiptHash string            `json:"discovery_receipt_hash"`
			RecheckDataHash      string            `json:"recheck_data_hash"`
			FrozenRuleCount      int               `json:"frozen_rule_count"`
			CandidateCount       int               `json:"candidate_count"`
			ClosedCount          int               `json:"closed_count"`
			InconclusiveCount    int               `json:"inconclusive_count"`
			ResearchPassCount    int               `json:"research_pass_count"`
			Research             g3research.Report `json:"research"`
			Registry             registry.Registry `json:"registry"`
			ExamActions          int               `json:"exam_actions"`
		}{
			SchemaVersion: "lingxi.router-g3-stage.v1", ProtocolHash: protocol.RouterProtocolHash,
			DiscoveryReceiptHash: discoveryReceipt.ReceiptHash, RecheckDataHash: recheckReport.RecheckDataHash,
			FrozenRuleCount: len(rules), CandidateCount: len(candidateRegistry.Candidates),
			ClosedCount: closedCount, InconclusiveCount: inconclusiveCount, ResearchPassCount: researchPass,
			Research: researchReport, Registry: candidateRegistry, ExamActions: protocol.ExamActions,
		},
		"g3_stage_summary.json": stageSummary,
	}
	for name, value := range artifacts {
		if err := writeJSONAtomic(filepath.Join(absoluteOutput, name), value); err != nil {
			return err
		}
	}

	cp.Done = true
	cp.RegistryHash, err = hashJSONFile(filepath.Join(absoluteOutput, "g3_candidate_registry.json"))
	if err != nil {
		return err
	}
	cp.AttributionReportHash, err = hashJSONFile(filepath.Join(absoluteOutput, "g3_attribution_report.json"))
	if err != nil {
		return err
	}
	cp.ResearchReportHash, err = hashJSONFile(filepath.Join(absoluteOutput, "g3_research_report.json"))
	if err != nil {
		return err
	}
	cp.StageSummaryHash, err = hashJSONFile(filepath.Join(absoluteOutput, "g3_stage_summary.json"))
	if err != nil {
		return err
	}
	if err := saveCheckpoint(*checkpointPath, cp); err != nil {
		return err
	}
	fmt.Printf("{\"registered\":%d,\"research_pass\":%d,\"system_status\":%q,\"exam_actions\":%d}\n",
		len(candidateRegistry.Candidates), researchPass, systemStatus, protocol.ExamActions)
	return nil
}

func defaultVerifyCanonicalForwardWF(
	ctx context.Context,
	historyRoot string,
	catalog historydata.HistoryCatalog,
	bundleDir string,
	boundaries g3reverse.Boundaries,
	cutpoints sky.Cutpoints,
	manifest g3reverse.Manifest,
) error {
	if err := g3reverse.VerifySelectedFiles(&catalog, historyRoot, manifest.MaxSymbols); err != nil {
		return fmt.Errorf("router-g3-research: canonical history verification failed: %w", err)
	}
	cfg := g3reverse.Config{
		HistoryRoot: historyRoot, Catalog: &catalog, SkyCutpoints: cutpoints,
		MaxSymbols: manifest.MaxSymbols, MaxFundingLines: manifest.MaxFundingLines,
		MaxPriceLines: manifest.MaxPriceLines, MaxFamilyLines: manifest.MaxFamilyLines,
	}
	for _, spec := range []struct {
		partition g3reverse.Partition
		want      string
		label     string
	}{
		{g3reverse.PartitionForward, manifest.ForwardEventHash, "forward"},
		{g3reverse.PartitionWalkFwd, manifest.WalkForwardEventHash, "walk_forward"},
	} {
		start, end := boundaries.Window(spec.partition)
		cfg.Start, cfg.End = start, end
		events, err := g3reverse.BuildEvents(ctx, cfg)
		if err != nil {
			return fmt.Errorf("router-g3-research: canonical %s regeneration failed: %w", spec.label, err)
		}
		hash, err := g3reverse.HashEvents(events)
		if err != nil {
			return err
		}
		if hash != spec.want {
			return fmt.Errorf("router-g3-research: canonical %s hash mismatch", spec.label)
		}
	}
	_ = bundleDir
	return nil
}

func loadCatalog(path string) (historydata.HistoryCatalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return historydata.HistoryCatalog{}, err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return historydata.HistoryCatalog{}, err
	}
	return catalog, nil
}

func readJSON(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readBoundedEventJSONL(path string, maxLines int) ([]g3reverse.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var events []g3reverse.Event
	lines := 0
	for scanner.Scan() {
		lines++
		if lines > maxLines {
			return nil, fmt.Errorf("router-g3-research: exceeded max-lines=%d at %s", maxLines, path)
		}
		var event g3reverse.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func validateEventsInWindow(events []g3reverse.Event, start, end time.Time, partition string) error {
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return fmt.Errorf("router-g3-research: invalid %s partition window", partition)
	}
	for _, event := range events {
		if event.DecisionTime.Before(start) || !event.DecisionTime.Before(end) {
			return fmt.Errorf(
				"router-g3-research: event %s decision time outside %s partition [%s,%s)",
				event.ID, partition, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano),
			)
		}
	}
	return nil
}

func hashJSONFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}
