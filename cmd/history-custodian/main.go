// history-custodian is the examiner/custodian-side tool for the historical exam
// boundary. It accepts explicit hidden plans and never prints their ranges or paths.
//
// The tool holds hidden material (time ranges, fixture bytes, strata) in a
// 0600 manifest and NEVER prints hidden paths or time ranges to stdout/stderr.
// The only researcher-visible outputs are public Ticket commitments and signed
// results.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
	"lingxi/internal/historyexam"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "seal":
		return seal(args[1:])
	case "commitments":
		return commitments(args[1:])
	case "unseal-next":
		return unsealNext(args[1:])
	case "grade":
		return grade(args[1:])
	case "crash":
		return crash(args[1:])
	case "result":
		return result(args[1:])
	default:
		return usage()
	}
}

// planInput is examiner-side only and may bind either synthetic fixture observations
// or a prebuilt hidden historical paper file.
type planInput struct {
	Generation          string         `json:"generation"`
	ProtocolHash        string         `json:"protocol_hash"`
	EmbargoHours        int            `json:"embargo_hours"`
	ResearchPermille    int            `json:"research_permille"`
	WalkForwardPermille int            `json:"walk_forward_permille"`
	Clusters            []clusterInput `json:"clusters"`
	Papers              []paperInput   `json:"papers"`
}

type clusterInput struct {
	ClusterID string            `json:"cluster_id"`
	Axes      map[string]string `json:"axes"`
	Start     time.Time         `json:"start"`
	End       time.Time         `json:"end"`
	Partition string            `json:"partition"`
}

type paperInput struct {
	ExamID       string    `json:"exam_id"`
	Queue        int       `json:"queue"`
	Kind         string    `json:"kind"`
	Family       string    `json:"family"`
	AlphaSpend   int       `json:"alpha_spend"`
	ClusterIDs   []string  `json:"cluster_ids"`
	Nonce        string    `json:"nonce"`
	Observations []float64 `json:"observations"`
	FixturePath  string    `json:"fixture_path"`
}

func readPlan(path string) (planInput, error) {
	if strings.TrimSpace(path) == "" {
		return planInput{}, errors.New("explicit --plan path is required (synthetic fixture only)")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return planInput{}, err
	}
	var p planInput
	if err := json.Unmarshal(b, &p); err != nil {
		return planInput{}, err
	}
	return p, nil
}

// buildManifest converts a synthetic plan into a hidden exam manifest. It
// validates cluster isolation first, then binds each paper to its exam clusters.
func buildManifest(p planInput) (historyexam.HiddenManifest, error) {
	if strings.TrimSpace(p.Generation) == "" {
		return historyexam.HiddenManifest{}, errors.New("plan requires a generation id")
	}
	if strings.TrimSpace(p.ProtocolHash) == "" {
		return historyexam.HiddenManifest{}, errors.New("plan requires a protocol hash")
	}
	clusters := make([]historyexam.Cluster, 0, len(p.Clusters))
	needsAssignment := false
	for _, c := range p.Clusters {
		cl := historyexam.Cluster{
			ClusterID: c.ClusterID,
			Stratum:   historyexam.StratumInput{Axes: c.Axes},
			Start:     c.Start,
			End:       c.End,
			Partition: historyexam.Partition(c.Partition),
		}
		clusters = append(clusters, cl)
		if cl.Partition == "" {
			needsAssignment = true
		}
	}
	var plan historyexam.PartitionPlan
	var err error
	if needsAssignment {
		researchPermille := p.ResearchPermille
		walkForwardPermille := p.WalkForwardPermille
		if researchPermille == 0 {
			researchPermille = 600
		}
		if walkForwardPermille == 0 {
			walkForwardPermille = 200
		}
		plan, err = historyexam.BuildChronologicalPlan(clusters, historyexam.SplitPolicy{
			ResearchPermille: researchPermille, WalkForwardPermille: walkForwardPermille,
			EmbargoGap: time.Duration(p.EmbargoHours) * time.Hour,
		})
	} else {
		plan = historyexam.PartitionPlan{
			Clusters: clusters, EmbargoGap: time.Duration(p.EmbargoHours) * time.Hour,
		}
		err = plan.Validate()
	}
	if err != nil {
		return historyexam.HiddenManifest{}, err
	}
	byID := make(map[string]historyexam.Cluster)
	for _, cluster := range plan.Clusters {
		byID[cluster.ClusterID] = cluster
	}
	papers := make([]historyexam.HiddenPaper, 0, len(p.Papers))
	for _, pi := range p.Papers {
		spec, err := paperSpec(pi, byID)
		if err != nil {
			return historyexam.HiddenManifest{}, err
		}
		paper, err := historyexam.NewHiddenPaper(p.ProtocolHash, spec)
		if err != nil {
			return historyexam.HiddenManifest{}, err
		}
		papers = append(papers, paper)
	}
	manifest := historyexam.HiddenManifest{
		Generation: p.Generation,
		Partition:  historyexam.PartitionExam,
		Papers:     papers,
	}
	if err := manifest.Validate(); err != nil {
		return historyexam.HiddenManifest{}, err
	}
	return manifest, nil
}

func paperSpec(pi paperInput, byID map[string]historyexam.Cluster) (historyexam.PaperSpec, error) {
	if len(pi.ClusterIDs) == 0 {
		return historyexam.PaperSpec{}, fmt.Errorf("paper %q references no clusters", pi.ExamID)
	}
	var (
		start, end time.Time
		stratumKey string
		stratum    historyexam.StratumInput
	)
	for i, id := range pi.ClusterIDs {
		cl, ok := byID[id]
		if !ok {
			return historyexam.PaperSpec{}, fmt.Errorf("paper %q references unknown cluster %q", pi.ExamID, id)
		}
		if cl.Partition != historyexam.PartitionExam {
			return historyexam.PaperSpec{}, fmt.Errorf("paper %q references non-exam cluster %q", pi.ExamID, id)
		}
		key, err := cl.Stratum.Key()
		if err != nil {
			return historyexam.PaperSpec{}, err
		}
		if i == 0 {
			start, end, stratumKey, stratum = cl.Start, cl.End, key, cl.Stratum
			continue
		}
		if key != stratumKey {
			return historyexam.PaperSpec{}, fmt.Errorf("paper %q mixes clusters from different strata", pi.ExamID)
		}
		if cl.Start.Before(start) {
			start = cl.Start
		}
		if cl.End.After(end) {
			end = cl.End
		}
	}
	var fixture []byte
	if strings.TrimSpace(pi.FixturePath) == "" {
		var err error
		fixture, err = json.Marshal(historyexam.SyntheticFixture{Observations: pi.Observations})
		if err != nil {
			return historyexam.PaperSpec{}, err
		}
	}
	return historyexam.PaperSpec{
		ExamID:      pi.ExamID,
		Queue:       pi.Queue,
		Kind:        gauntlet.Kind(pi.Kind),
		Family:      pi.Family,
		AlphaSpend:  pi.AlphaSpend,
		Stratum:     stratum,
		ClusterIDs:  pi.ClusterIDs,
		Start:       start,
		End:         end,
		Fixture:     fixture,
		FixturePath: strings.TrimSpace(pi.FixturePath),
		Nonce:       pi.Nonce,
	}, nil
}

func seal(args []string) error {
	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	planPath := fs.String("plan", "", "examiner-side hidden plan path")
	hiddenPath := fs.String("hidden-manifest", "", "examiner-side hidden manifest path (written 0600)")
	statePath := fs.String("state", "", "public gauntlet state path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := readPlan(*planPath)
	if err != nil {
		return err
	}
	manifest, err := buildManifest(p)
	if err != nil {
		return err
	}
	if err := historyexam.SaveHiddenManifest(*hiddenPath, manifest); err != nil {
		return err
	}
	tickets := manifest.Commitments()
	budgets := make(map[string]int)
	for _, t := range tickets {
		budgets[t.Family] += t.AlphaSpend
	}
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	state, err = historyexam.CreateGeneration(state, gauntlet.FixedAccess{}, p.Generation, tickets, budgets, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := saveState(*statePath, state); err != nil {
		return err
	}
	return printPublicCommitments(tickets)
}

func commitments(args []string) error {
	fs := flag.NewFlagSet("commitments", flag.ContinueOnError)
	hiddenPath := fs.String("hidden-manifest", "", "examiner-side hidden manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	manifest, err := historyexam.LoadHiddenManifest(*hiddenPath)
	if err != nil {
		return err
	}
	return printPublicCommitments(manifest.Commitments())
}

func unsealNext(args []string) error {
	fs := flag.NewFlagSet("unseal-next", flag.ContinueOnError)
	statePath := fs.String("state", "", "public gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	state, opened, err := gauntlet.UnsealNext(state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := saveState(*statePath, state); err != nil {
		return err
	}
	// Coarse status only: no hidden range or fixture path is ever printed.
	return printJSON(map[string]any{"status": "unsealed_and_consumable", "queue": opened.Order})
}

func grade(args []string) error {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	statePath := fs.String("state", "", "public gauntlet state path")
	hiddenPath := fs.String("hidden-manifest", "", "examiner-side hidden manifest path")
	generation := fs.String("generation", "", "generation id")
	keyEnv := fs.String("hmac-key-env", "LINGXI_GAUNTLET_TEST_HMAC_KEY", "HMAC signing key environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := os.Getenv(*keyEnv)
	if len(key) < 32 {
		return fmt.Errorf("HMAC key in %s must be at least 32 bytes", *keyEnv)
	}
	manifest, err := historyexam.LoadHiddenManifest(*hiddenPath)
	if err != nil {
		return err
	}
	if len(manifest.Papers) == 0 {
		return errors.New("hidden manifest has no papers")
	}
	protocol := manifest.Papers[0].ProtocolHash
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	evaluator, ok := historyexam.EvaluatorForProtocol(protocol)
	if !ok {
		return fmt.Errorf("EXAM_UNAVAILABLE: no reviewed evaluator registered for protocol %s", protocol)
	}
	state, res, err := historyexam.Grade(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, manifest, evaluator, time.Now().UTC(), exam.HMACSigner{KeyID: "history-custodian", Key: []byte(key)})
	if err != nil {
		return err
	}
	if err := saveState(*statePath, state); err != nil {
		return err
	}
	return printJSON(res)
}

func crash(args []string) error {
	fs := flag.NewFlagSet("crash", flag.ContinueOnError)
	statePath := fs.String("state", "", "public gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	keyEnv := fs.String("hmac-key-env", "LINGXI_GAUNTLET_TEST_HMAC_KEY", "HMAC signing key environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := os.Getenv(*keyEnv)
	if len(key) < 32 {
		return fmt.Errorf("HMAC key in %s must be at least 32 bytes", *keyEnv)
	}
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	state, res, err := gauntlet.Crash(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, time.Now().UTC(), exam.HMACSigner{KeyID: "history-custodian", Key: []byte(key)})
	if err != nil {
		return err
	}
	if err := saveState(*statePath, state); err != nil {
		return err
	}
	return printJSON(res)
}

func result(args []string) error {
	fs := flag.NewFlagSet("result", flag.ContinueOnError)
	statePath := fs.String("state", "", "public gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	res, err := gauntlet.ResearchReceipt(state, gauntlet.FixedAccess{}, gauntlet.RoleResearcher, *generation)
	if err != nil {
		return err
	}
	return printJSON(res)
}

func loadState(path string) (gauntlet.State, error) {
	if strings.TrimSpace(path) == "" {
		return gauntlet.State{}, errors.New("explicit --state path is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return gauntlet.State{}, nil
		}
		return gauntlet.State{}, err
	}
	var state gauntlet.State
	return state, json.Unmarshal(b, &state)
}

func saveState(path string, state gauntlet.State) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("explicit --state path is required")
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// printPublicCommitments emits only public Ticket fields. It never touches the
// hidden manifest's time ranges, fixture bytes, strata, or cluster identities.
func printPublicCommitments(tickets []gauntlet.Ticket) error {
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].Queue < tickets[j].Queue })
	return printJSON(tickets)
}

func printJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func usage() error {
	return errors.New("usage: history-custodian seal|commitments|unseal-next|grade|crash|result; hidden ranges/paths are never printed")
}
