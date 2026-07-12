package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg2/candidates"
	"lingxi/internal/routerg2/protocol"
	g2research "lingxi/internal/routerg2/research"
	"lingxi/internal/routerresearch"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g2-research", flag.ContinueOnError)
	prizePath := fs.String("prize-report", "", "G2 prize report")
	searchPath := fs.String("search-space", "", "frozen G2 search space")
	forwardPath := fs.String("forward-events", "", "public forward research events")
	wfPath := fs.String("wf-events", "", "public WF events")
	boundaryPath := fs.String("boundaries", "", "frozen partition boundaries")
	registryPath := fs.String("registry", "", "candidate registry output")
	reportPath := fs.String("output", "", "G2 forward/WF report")
	maxLines := fs.Int("max-lines", 0, "positive bounded lines per event file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxLines <= 0 || *maxLines > protocol.BackwardMaxLines {
		return fmt.Errorf("--max-lines must be positive and within frozen G2 bound")
	}
	var prize g2prize.Report
	if err := readJSON(*prizePath, &prize); err != nil {
		return err
	}
	var space g2prize.SearchSpace
	if err := readJSON(*searchPath, &space); err != nil {
		return err
	}
	if err := space.Validate(); err != nil {
		return err
	}
	var boundaries g2prize.Boundaries
	if err := readJSON(*boundaryPath, &boundaries); err != nil {
		return err
	}
	forwardEvents, err := readEvents(*forwardPath, *maxLines)
	if err != nil {
		return err
	}
	wfEvents, err := readEvents(*wfPath, *maxLines)
	if err != nil {
		return err
	}
	if err := validatePartitionEvents(forwardEvents, boundaries.ForwardStart, boundaries.WFStart, "forward"); err != nil {
		return err
	}
	if err := validatePartitionEvents(wfEvents, boundaries.WFStart, boundaries.End, "WF"); err != nil {
		return err
	}
	comboByKey := map[string]g2prize.Combination{}
	for _, combo := range space.Combinations {
		comboByKey[combo.Key] = combo
	}
	candidateRegistry := registry.Registry{
		SchemaVersion: "lingxi.router-candidates.v1",
		Candidates:    []registry.Candidate{},
	}
	var allObservations []routerresearch.Observation
	for _, nomination := range prize.Nominations {
		combo, ok := comboByKey[nomination.Key]
		if !ok {
			return fmt.Errorf("nomination %q not in frozen search space", nomination.Key)
		}
		definition, err := candidates.FromPrizeNomination(nomination, candidates.PrizeRegistration{
			RequiredData:       []string{"okx_1h", "okx_funding"},
			Invalidators:       []string{"funding_flip_reverted", "deadline_reached"},
			CostModel:          "g2_full_cost_12bps",
			ForcedBehavior:     "funding payer side flips at settlement and carry positions must rebalance",
			CompetitionDefense: "sky-conditioned low-capacity settlement window",
			DeadlineRule:       "next_funding_settlement_or_horizon",
		})
		if err != nil {
			return err
		}
		candidateRegistry, err = candidates.Register(candidateRegistry, definition, time.Now().UTC())
		if err != nil {
			return err
		}
		candidateRegistry, err = registry.Freeze(candidateRegistry, candidates.CanonicalID(definition), time.Now().UTC())
		if err != nil {
			return err
		}
		for _, event := range append(append([]g2prize.Event(nil), forwardEvents...), wfEvents...) {
			fires, hasFeature := combo.Fires(event)
			if !hasFeature || !fires {
				continue
			}
			allObservations = append(allObservations, routerresearch.Observation{
				CandidateID:  candidates.CanonicalID(definition),
				DecisionTime: event.DecisionTime, ClusterID: event.Cluster, NetBPS: event.NetOutcomeBPS,
			})
		}
	}
	wfHash, err := g2prize.HashEvents(wfEvents)
	if err != nil {
		return err
	}
	report, err := g2research.Run(allObservations, g2research.Boundary{
		DiscoveryEnd: boundaries.DiscoveryEnd, ForwardStart: boundaries.ForwardStart,
		WFStart: boundaries.WFStart, End: boundaries.End,
	}, g2research.Options{
		MinForwardSamples:  protocol.G2MinForwardSamples,
		MinForwardClusters: protocol.G2MinForwardClusters,
		DiscoveryEmbargo:   time.Duration(protocol.PartitionEmbargoHours) * time.Hour,
		ForwardAlpha:       protocol.ForwardFamilyAlpha,
		WF: routerresearch.Options{
			Folds:      protocol.G2WalkForwardFolds,
			Purge:      time.Duration(protocol.G2PurgeHours) * time.Hour,
			Embargo:    time.Duration(protocol.G2WFEmbargoHours) * time.Hour,
			MinSamples: protocol.G2WFMinSamples, MinClusters: protocol.G2WFMinClusters,
			FamilyWiseAlpha: protocol.ForwardFamilyAlpha, ResearchDataHash: wfHash,
		},
	})
	if err != nil {
		return err
	}
	resultByID := map[string]g2research.Result{}
	for _, result := range report.Results {
		resultByID[result.CandidateID] = result
	}
	for _, candidate := range append([]registry.Candidate(nil), candidateRegistry.Candidates...) {
		result, ok := resultByID[candidate.ID]
		if !ok {
			candidateRegistry, err = registry.RecordForwardFalsification(
				candidateRegistry, candidate.ID, false, prize.DiscoveryDataHash, "INCONCLUSIVE", time.Now().UTC(),
			)
		} else {
			pass := result.Status == "RESEARCH_PASS"
			reason := result.Reason
			if pass {
				reason = "PASS"
			}
			evidence := result.ForwardEvidenceHash
			if evidence == "" {
				evidence = prize.DiscoveryDataHash
			}
			candidateRegistry, err = registry.RecordForwardFalsification(
				candidateRegistry, candidate.ID, pass, evidence, reason, time.Now().UTC(),
			)
		}
		if err != nil {
			return err
		}
	}
	if err := registry.Save(*registryPath, candidateRegistry); err != nil {
		return err
	}
	candidateRegistry, err = registry.Load(*registryPath)
	if err != nil {
		return err
	}
	return writeJSON(*reportPath, struct {
		SchemaVersion string            `json:"schema_version"`
		ProtocolHash  string            `json:"protocol_hash"`
		Research      g2research.Report `json:"research"`
		Registry      registry.Registry `json:"registry"`
		ExamActions   int               `json:"exam_actions"`
	}{
		SchemaVersion: "lingxi.router-g2-stage.v1", ProtocolHash: protocol.RouterProtocolHash,
		Research: report, Registry: candidateRegistry, ExamActions: 0,
	})
}

func validatePartitionEvents(events []g2prize.Event, start, end time.Time, label string) error {
	for _, event := range events {
		if event.DecisionTime.Before(start) || !event.DecisionTime.Before(end) {
			return fmt.Errorf("%s event %q is outside frozen partition", label, event.ID)
		}
	}
	return nil
}

func readJSON(path string, output any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func readEvents(path string, maxLines int) ([]g2prize.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var events []g2prize.Event
	for scanner.Scan() {
		if len(events) >= maxLines {
			return nil, fmt.Errorf("event file exceeds --max-lines")
		}
		var event g2prize.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
