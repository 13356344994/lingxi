package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g3-discovery.checkpoint.v2"

type checkpointFile struct {
	SchemaVersion           string    `json:"schema_version"`
	Generation              string    `json:"generation"`
	ProtocolHash            string    `json:"protocol_hash"`
	EventManifestHash       string    `json:"event_manifest_hash"`
	SearchManifestHash      string    `json:"search_manifest_hash"`
	RulesHash               string    `json:"rules_hash"`
	LedgerHash              string    `json:"ledger_hash"`
	InductionEventHash      string    `json:"induction_event_hash"`
	RecheckEventHash        string    `json:"recheck_event_hash"`
	PartitionBoundariesHash string    `json:"partition_boundaries_hash"`
	ConfigHash              string    `json:"config_hash"`
	InputHash               string    `json:"input_hash"`
	OutputDir               string    `json:"output_dir"`
	MaxLines                int       `json:"max_lines"`
	Done                    bool      `json:"done"`
	InductionEvaluationHash string    `json:"induction_evaluation_hash,omitempty"`
	RecheckReportHash       string    `json:"recheck_report_hash,omitempty"`
	NominationsHash         string    `json:"nominations_hash,omitempty"`
	ControlSummaryHash      string    `json:"control_summary_hash,omitempty"`
	DiscoveryReceiptHash    string    `json:"discovery_receipt_hash,omitempty"`
	SavedAt                 time.Time `json:"saved_at"`
	CheckpointHash          string    `json:"checkpoint_hash"`
}

type checkpointBinding struct {
	EventManifestHash       string
	SearchManifestHash      string
	RulesHash               string
	LedgerHash              string
	InductionEventHash      string
	RecheckEventHash        string
	PartitionBoundariesHash string
	ConfigHash              string
	InputHash               string
	OutputDir               string
	MaxLines                int
}

type runConfig struct {
	MaxLines              int
	MatchWindowMS         int64
	NeutralBandBPS        float64
	PermutationDraws      int
	BootstrapSamples      int
	ExactPermutationMax   int
	Seed                  int64
	ReverseInductionAlpha float64
	RecheckAlpha          float64
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g3-discovery", flag.ContinueOnError)
	bundleDir := fs.String("bundle-dir", "", "G3 events research bundle directory")
	inductionDir := fs.String("induction-dir", "", "G3 induction output directory with sealed search manifest")
	outputDir := fs.String("output-dir", "", "explicit discovery output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum event lines to read per partition; unbounded scanning is forbidden")
	keyEnv := fs.String("hmac-key-env", "LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", "HMAC key env for event+search manifests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bundleDir == "" || *inductionDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("--bundle-dir, --induction-dir, --output-dir and --checkpoint are required")
	}
	if *maxLines <= 0 || *maxLines > protocol.ReverseInductionMaxLines {
		return errors.New("router-g3-discovery: --max-lines must be a positive integer; unbounded scanning is forbidden")
	}
	signingKey := []byte(os.Getenv(*keyEnv))
	if len(signingKey) < 32 {
		return fmt.Errorf("HMAC key environment variable %s must be >=32 bytes", *keyEnv)
	}

	absoluteBundle, err := filepath.Abs(*bundleDir)
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

	var eventManifest g3reverse.Manifest
	if err := readJSON(filepath.Join(absoluteBundle, "event_manifest.json"), &eventManifest); err != nil {
		return err
	}
	if err := g3reverse.VerifyManifestSignature(eventManifest, signingKey); err != nil {
		return err
	}
	if eventManifest.Generation != protocol.Generation || eventManifest.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("router-g3-discovery: event manifest protocol mismatch")
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
		return errors.New("router-g3-discovery: partition boundaries hash mismatch")
	}
	if !boundaries.Start.Before(boundaries.InductionEnd) ||
		!boundaries.RecheckStart.Before(boundaries.RecheckEnd) ||
		!boundaries.ForwardStart.Before(boundaries.ForwardEnd) ||
		!boundaries.WFStart.Before(boundaries.End) {
		return errors.New("router-g3-discovery: four partition boundaries are invalid")
	}

	var searchManifest g3reverse.SearchManifest
	if err := readJSON(filepath.Join(absoluteInduction, "search_manifest.json"), &searchManifest); err != nil {
		return err
	}
	if err := g3reverse.VerifySearchManifestSignature(searchManifest, signingKey); err != nil {
		return err
	}
	if searchManifest.EventManifestHash != eventManifest.ManifestHash {
		return errors.New("router-g3-discovery: search manifest event binding mismatch")
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
		return errors.New("router-g3-discovery: ledger hash mismatch")
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
		return errors.New("router-g3-discovery: rules hash mismatch")
	}

	inductionEvents, err := readBoundedEventJSONL(filepath.Join(absoluteBundle, "g3_induction", "events.jsonl"), *maxLines)
	if err != nil {
		return err
	}
	if err := validateEventsInWindow(inductionEvents, boundaries.Start, boundaries.InductionEnd, "induction"); err != nil {
		return err
	}
	inductionHash, err := g3reverse.HashEvents(inductionEvents)
	if err != nil {
		return err
	}
	if inductionHash != eventManifest.InductionEventHash || inductionHash != searchManifest.InductionEventHash {
		return errors.New("router-g3-discovery: induction event hash mismatch")
	}

	recheckEvents, err := readBoundedEventJSONL(filepath.Join(absoluteBundle, "g3_recheck", "events.jsonl"), *maxLines)
	if err != nil {
		return err
	}
	if err := validateEventsInWindow(recheckEvents, boundaries.RecheckStart, boundaries.RecheckEnd, "recheck"); err != nil {
		return err
	}
	recheckHash, err := g3reverse.HashEvents(recheckEvents)
	if err != nil {
		return err
	}
	if recheckHash != eventManifest.RecheckEventHash {
		return errors.New("router-g3-discovery: recheck event hash mismatch")
	}

	cfg := runConfig{
		MaxLines: *maxLines, MatchWindowMS: protocol.ReverseInductionMatchWindowMs,
		NeutralBandBPS:        protocol.ReverseInductionNeutralBandBPS,
		PermutationDraws:      protocol.ReverseInductionPermutationDraws,
		BootstrapSamples:      protocol.ReverseInductionBootstrapSamples,
		ExactPermutationMax:   protocol.ReverseInductionExactPermMax,
		Seed:                  protocol.ReverseInductionSeed,
		ReverseInductionAlpha: protocol.ReverseInductionAlpha,
		RecheckAlpha:          protocol.RecheckAlpha,
	}
	configHash, err := configFingerprint(cfg)
	if err != nil {
		return err
	}
	inputHash, err := inputFingerprint(inductionHash, recheckHash, rulesHash, ledger.LedgerHash, searchManifest.ManifestHash)
	if err != nil {
		return err
	}
	cpBinding := checkpointBinding{
		EventManifestHash: eventManifest.ManifestHash, SearchManifestHash: searchManifest.ManifestHash,
		RulesHash: rulesHash, LedgerHash: ledger.LedgerHash,
		InductionEventHash: inductionHash, RecheckEventHash: recheckHash,
		PartitionBoundariesHash: boundariesHash, ConfigHash: configHash,
		InputHash: inputHash, OutputDir: absoluteOutput, MaxLines: *maxLines,
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
			if err := verifyCompletedOutputs(
				absoluteOutput, cp, signingKey,
				eventManifestFileHash, searchManifestFileHash,
			); err != nil {
				return err
			}
			fmt.Printf("{\"resumed\":true,\"nominations\":%d,\"exam_actions\":%d}\n",
				countNominations(filepath.Join(absoluteOutput, "nominations.json")), protocol.ExamActions)
			return nil
		}
	} else {
		cp = newCheckpoint(cpBinding)
	}

	inductionReport, controlSummary, err := g3reverse.EvaluateInduction(g3reverse.EvaluationOptions{
		Events: inductionEvents, Rules: rules, Manifest: searchManifest, Ledger: ledger,
		NeutralBandBPS:   cfg.NeutralBandBPS,
		MatchWindow:      time.Duration(cfg.MatchWindowMS) * time.Millisecond,
		PermutationDraws: cfg.PermutationDraws, BootstrapSamples: cfg.BootstrapSamples,
		ExactPermutationMax: cfg.ExactPermutationMax, Seed: cfg.Seed,
		ReverseInductionAlpha: cfg.ReverseInductionAlpha,
		Generation:            protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	})
	if err != nil {
		return err
	}
	recheckReport, err := g3reverse.Recheck(g3reverse.RecheckOptions{
		RecheckEvents: recheckEvents, Nominations: inductionReport.Nominations,
		Rules: rules, Manifest: searchManifest, RecheckAlpha: cfg.RecheckAlpha,
		MinSamples: protocol.G3MinRecheckSamples, MinClusters: protocol.G3MinRecheckClusters,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(absoluteOutput, 0o755); err != nil {
		return err
	}
	nominationsPayload := struct {
		GeneratedAt        time.Time              `json:"generated_at"`
		SearchManifestHash string                 `json:"search_manifest_hash"`
		Nominations        []g3reverse.Nomination `json:"nominations"`
		ExamActions        int                    `json:"exam_actions"`
	}{
		GeneratedAt: inductionReport.GeneratedAt, SearchManifestHash: searchManifest.ManifestHash,
		Nominations: inductionReport.Nominations, ExamActions: protocol.ExamActions,
	}
	artifacts := map[string]any{
		"induction_evaluation.json": inductionReport,
		"recheck_report.json":       recheckReport,
		"nominations.json":          nominationsPayload,
		"control_summary.json":      controlSummary,
	}
	for name, value := range artifacts {
		if err := writeJSONAtomic(filepath.Join(absoluteOutput, name), value); err != nil {
			return err
		}
	}

	inductionEvaluationHash, err := hashJSONFile(filepath.Join(absoluteOutput, "induction_evaluation.json"))
	if err != nil {
		return err
	}
	recheckReportHash, err := hashJSONFile(filepath.Join(absoluteOutput, "recheck_report.json"))
	if err != nil {
		return err
	}
	nominationsHash, err := hashJSONFile(filepath.Join(absoluteOutput, "nominations.json"))
	if err != nil {
		return err
	}
	controlSummaryHash, err := hashJSONFile(filepath.Join(absoluteOutput, "control_summary.json"))
	if err != nil {
		return err
	}
	receipt, err := g3reverse.NewDiscoveryReceipt(g3reverse.DiscoveryReceiptBindings{
		EventManifestHash:       eventManifestFileHash,
		SearchManifestHash:      searchManifestFileHash,
		InductionEvaluationHash: inductionEvaluationHash,
		RecheckReportHash:       recheckReportHash,
		NominationsHash:         nominationsHash,
		ControlSummaryHash:      controlSummaryHash,
	})
	if err != nil {
		return err
	}
	receipt, err = g3reverse.SignDiscoveryReceipt(receipt, signingKey, "router-g3-discovery")
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(absoluteOutput, "discovery_receipt.json"), receipt); err != nil {
		return err
	}
	receiptFileHash, err := hashJSONFile(filepath.Join(absoluteOutput, "discovery_receipt.json"))
	if err != nil {
		return err
	}

	cp.Done = true
	cp.InductionEvaluationHash = inductionEvaluationHash
	cp.RecheckReportHash = recheckReportHash
	cp.NominationsHash = nominationsHash
	cp.ControlSummaryHash = controlSummaryHash
	cp.DiscoveryReceiptHash = receiptFileHash
	if err := saveCheckpoint(*checkpointPath, cp); err != nil {
		return err
	}
	fmt.Printf("{\"nominations\":%d,\"rejected_rules\":%d,\"recheck_verdicts\":%d,\"exam_actions\":%d}\n",
		len(inductionReport.Nominations), len(inductionReport.RejectedRules),
		len(recheckReport.Verdicts), protocol.ExamActions)
	return nil
}

func newCheckpoint(binding checkpointBinding) checkpointFile {
	return checkpointFile{
		SchemaVersion: checkpointSchemaVersion,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		EventManifestHash: binding.EventManifestHash, SearchManifestHash: binding.SearchManifestHash,
		RulesHash: binding.RulesHash, LedgerHash: binding.LedgerHash,
		InductionEventHash: binding.InductionEventHash, RecheckEventHash: binding.RecheckEventHash,
		PartitionBoundariesHash: binding.PartitionBoundariesHash,
		ConfigHash:              binding.ConfigHash, InputHash: binding.InputHash,
		OutputDir: binding.OutputDir, MaxLines: binding.MaxLines,
	}
}

func (cp checkpointFile) validateBinding(binding checkpointBinding) error {
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash ||
		cp.EventManifestHash != binding.EventManifestHash ||
		cp.SearchManifestHash != binding.SearchManifestHash ||
		cp.RulesHash != binding.RulesHash ||
		cp.LedgerHash != binding.LedgerHash ||
		cp.InductionEventHash != binding.InductionEventHash ||
		cp.RecheckEventHash != binding.RecheckEventHash ||
		cp.PartitionBoundariesHash != binding.PartitionBoundariesHash ||
		cp.ConfigHash != binding.ConfigHash ||
		cp.InputHash != binding.InputHash ||
		cp.MaxLines != binding.MaxLines ||
		filepath.Clean(cp.OutputDir) != filepath.Clean(binding.OutputDir) {
		return errors.New("router-g3-discovery: checkpoint input or config drift")
	}
	return nil
}

func verifyCompletedOutputs(
	outputDir string,
	cp checkpointFile,
	signingKey []byte,
	eventManifestFileHash, searchManifestFileHash string,
) error {
	checks := map[string]string{
		"induction_evaluation.json": cp.InductionEvaluationHash,
		"recheck_report.json":       cp.RecheckReportHash,
		"nominations.json":          cp.NominationsHash,
		"control_summary.json":      cp.ControlSummaryHash,
		"discovery_receipt.json":    cp.DiscoveryReceiptHash,
	}
	for name, want := range checks {
		got, err := hashJSONFile(filepath.Join(outputDir, name))
		if err != nil {
			return err
		}
		if want == "" || got != want {
			return fmt.Errorf("router-g3-discovery: completed checkpoint %s drift", name)
		}
	}
	var receipt g3reverse.DiscoveryReceipt
	if err := readJSON(filepath.Join(outputDir, "discovery_receipt.json"), &receipt); err != nil {
		return err
	}
	if err := g3reverse.VerifyDiscoveryReceipt(receipt, signingKey); err != nil {
		return err
	}
	if receipt.EventManifestHash != eventManifestFileHash ||
		receipt.SearchManifestHash != searchManifestFileHash ||
		receipt.InductionEvaluationHash != cp.InductionEvaluationHash ||
		receipt.RecheckReportHash != cp.RecheckReportHash ||
		receipt.NominationsHash != cp.NominationsHash ||
		receipt.ControlSummaryHash != cp.ControlSummaryHash ||
		receipt.ExamActions != protocol.ExamActions {
		return errors.New("router-g3-discovery: discovery receipt artifact binding mismatch")
	}
	return nil
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
			return nil, fmt.Errorf("router-g3-discovery: exceeded max-lines=%d at %s", maxLines, path)
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
		return fmt.Errorf("router-g3-discovery: invalid %s partition window", partition)
	}
	for _, event := range events {
		if event.DecisionTime.Before(start) || !event.DecisionTime.Before(end) {
			return fmt.Errorf(
				"router-g3-discovery: event %s decision time outside %s partition [%s,%s)",
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

func countNominations(path string) int {
	var payload struct {
		Nominations []g3reverse.Nomination `json:"nominations"`
	}
	if err := readJSON(path, &payload); err != nil {
		return 0
	}
	return len(payload.Nominations)
}
