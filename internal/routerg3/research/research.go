package research

import (
	"errors"
	"math"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
	"lingxi/internal/routerresearch"
)

type Boundary struct {
	ForwardStart time.Time
	ForwardEnd   time.Time
	WFStart      time.Time
	End          time.Time
}

type CandidateRule struct {
	CandidateID string
	Rule        g3reverse.Rule
}

type Options struct {
	MinForwardSamples  int
	MinForwardClusters int
	ForwardAlpha       float64
	WF                 routerresearch.Options
}

type Result struct {
	CandidateID                string                  `json:"candidate_id"`
	Status                     string                  `json:"status"`
	Reason                     string                  `json:"reason"`
	ForwardSamples             int                     `json:"forward_samples"`
	ForwardClusters            int                     `json:"forward_clusters"`
	ForwardMeanBPS             float64                 `json:"forward_mean_bps"`
	WorstLeaveOneClusterOutBPS float64                 `json:"worst_leave_one_cluster_out_bps"`
	ForwardPValue              float64                 `json:"forward_p_value,omitempty"`
	ForwardHolmLimit           float64                 `json:"forward_holm_limit,omitempty"`
	ForwardEvidenceHash        string                  `json:"forward_evidence_hash,omitempty"`
	WFVerdict                  *routerresearch.Verdict `json:"wf_verdict,omitempty"`
}

type Report struct {
	SchemaVersion   string   `json:"schema_version"`
	ForwardDataHash string   `json:"forward_data_hash"`
	WFDataHash      string   `json:"wf_data_hash"`
	Results         []Result `json:"results"`
}

func Run(
	candidates []CandidateRule,
	forwardEvents, wfEvents []g3reverse.Event,
	boundary Boundary,
	opts Options,
) (Report, error) {
	if err := validateInputs(candidates, forwardEvents, wfEvents, boundary, opts); err != nil {
		return Report{}, err
	}
	forwardHash, err := g3reverse.HashEvents(forwardEvents)
	if err != nil {
		return Report{}, err
	}
	wfHash, err := g3reverse.HashEvents(wfEvents)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion:   "lingxi.router-g3-research.v1",
		ForwardDataHash: forwardHash,
		WFDataHash:      wfHash,
		Results:         []Result{},
	}
	orderedCandidates := append([]CandidateRule(nil), candidates...)
	sort.Slice(orderedCandidates, func(i, j int) bool {
		return orderedCandidates[i].CandidateID < orderedCandidates[j].CandidateID
	})
	candidates = orderedCandidates
	var wfEligible []routerresearch.Observation
	eligible := map[string]int{}
	preliminary := map[string]int{}
	for _, candidate := range candidates {
		forwardObs := observationsForRule(candidate, forwardEvents)
		clusters := clusterMeanMap(forwardObs)
		result := Result{
			CandidateID: candidate.CandidateID, Status: "CLOSED", Reason: "FORWARD_ROBUSTNESS_GATE",
			ForwardSamples: len(forwardObs), ForwardClusters: len(clusters),
			ForwardMeanBPS: meanObservations(forwardObs),
		}
		if len(clusters) >= 2 {
			result.WorstLeaveOneClusterOutBPS = worstLeaveOneClusterOut(clusters)
		}
		if len(forwardObs) < opts.MinForwardSamples || len(clusters) < opts.MinForwardClusters {
			result.Reason = "INCONCLUSIVE"
			report.Results = append(report.Results, result)
			continue
		}
		if result.ForwardMeanBPS <= 0 || result.WorstLeaveOneClusterOutBPS <= 0 {
			report.Results = append(report.Results, result)
			continue
		}
		result.Reason = "PENDING_FORWARD_HOLM"
		result.ForwardPValue = positiveClusterPValue(clusters)
		report.Results = append(report.Results, result)
		preliminary[candidate.CandidateID] = len(report.Results) - 1
	}
	indexes := make([]int, 0, len(preliminary))
	for _, index := range preliminary {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return report.Results[indexes[i]].ForwardPValue < report.Results[indexes[j]].ForwardPValue
	})
	failed := false
	for rank, index := range indexes {
		limit := opts.ForwardAlpha / float64(len(indexes)-rank)
		report.Results[index].ForwardHolmLimit = limit
		if failed || report.Results[index].ForwardPValue > limit {
			failed = true
			report.Results[index].Status = "CLOSED"
			report.Results[index].Reason = "FORWARD_MULTIPLE_TESTING"
			continue
		}
		id := report.Results[index].CandidateID
		hash, _, _ := datatruth.HashJSON(struct {
			ForwardDataHash string                       `json:"forward_data_hash"`
			CandidateID     string                       `json:"candidate_id"`
			Observations    []routerresearch.Observation `json:"observations"`
		}{forwardHash, id, observationsForRule(findCandidate(candidates, id), forwardEvents)})
		report.Results[index].Status = "FORWARD_PASS"
		report.Results[index].Reason = "PENDING_WF"
		report.Results[index].ForwardEvidenceHash = hash
		eligible[id] = index
		candidate := findCandidate(candidates, id)
		wfEligible = append(wfEligible, observationsForRule(candidate, wfEvents)...)
	}
	if len(wfEligible) == 0 {
		return report, nil
	}
	wfReport, err := routerresearch.Run(wfEligible, opts.WF)
	if err != nil {
		return Report{}, err
	}
	for _, verdict := range wfReport.Verdicts {
		index, ok := eligible[verdict.CandidateID]
		if !ok {
			continue
		}
		report.Results[index].WFVerdict = &verdict
		if verdict.Pass {
			report.Results[index].Status = "RESEARCH_PASS"
			report.Results[index].Reason = "PASS"
		} else {
			report.Results[index].Status = "CLOSED"
			if verdict.Reason == "INCONCLUSIVE" {
				report.Results[index].Reason = "INCONCLUSIVE"
			} else {
				report.Results[index].Reason = verdict.Reason
			}
		}
	}
	return report, nil
}

func validateInputs(
	candidates []CandidateRule,
	forwardEvents, wfEvents []g3reverse.Event,
	boundary Boundary,
	opts Options,
) error {
	if boundary.ForwardStart.IsZero() || boundary.ForwardEnd.IsZero() ||
		boundary.WFStart.IsZero() || boundary.End.IsZero() ||
		!boundary.ForwardStart.Before(boundary.ForwardEnd) ||
		boundary.ForwardEnd.Add(time.Duration(protocol.PartitionEmbargoHours)*time.Hour).After(boundary.WFStart) ||
		!boundary.WFStart.Before(boundary.End) ||
		opts.MinForwardSamples != protocol.G3MinForwardSamples ||
		opts.MinForwardClusters != protocol.G3MinForwardClusters ||
		opts.ForwardAlpha != protocol.ForwardAlpha ||
		opts.WF.Folds != protocol.G3WalkForwardFolds ||
		opts.WF.Purge != time.Duration(protocol.G3PurgeHours)*time.Hour ||
		opts.WF.Embargo != time.Duration(protocol.G3WFEmbargoHours)*time.Hour ||
		opts.WF.MinSamples != protocol.G3WFMinSamples ||
		opts.WF.MinClusters != protocol.G3WFMinClusters ||
		opts.WF.FamilyWiseAlpha != protocol.WalkForwardAlpha ||
		opts.WF.ResearchDataHash == "" {
		return errors.New("routerg3 research: invalid frozen boundary/options")
	}
	seenIDs := map[string]bool{}
	seenRules := map[string]bool{}
	for _, candidate := range candidates {
		computedRuleHash, err := g3reverse.CanonicalRuleHash(candidate.Rule)
		if err != nil {
			return err
		}
		if candidate.CandidateID == "" || candidate.Rule.CanonicalHash == "" ||
			computedRuleHash != candidate.Rule.CanonicalHash ||
			seenIDs[candidate.CandidateID] || seenRules[candidate.Rule.CanonicalHash] {
			return errors.New("routerg3 research: candidate id and frozen rule must be non-empty and unique")
		}
		seenIDs[candidate.CandidateID] = true
		seenRules[candidate.Rule.CanonicalHash] = true
	}
	for _, event := range forwardEvents {
		if event.DecisionTime.Before(boundary.ForwardStart) || !event.DecisionTime.Before(boundary.ForwardEnd) {
			return errors.New("routerg3 research: forward event outside frozen window")
		}
	}
	for _, event := range wfEvents {
		if event.DecisionTime.Before(boundary.WFStart) || !event.DecisionTime.Before(boundary.End) {
			return errors.New("routerg3 research: walk-forward event outside frozen window")
		}
	}
	return nil
}

func findCandidate(candidates []CandidateRule, id string) CandidateRule {
	for _, candidate := range candidates {
		if candidate.CandidateID == id {
			return candidate
		}
	}
	return CandidateRule{}
}

func observationsForRule(
	candidate CandidateRule,
	events []g3reverse.Event,
) []routerresearch.Observation {
	var out []routerresearch.Observation
	for _, event := range events {
		fires, hasFeature := candidate.Rule.Fires(event)
		if !hasFeature || !fires {
			continue
		}
		out = append(out, routerresearch.Observation{
			CandidateID: candidate.CandidateID, DecisionTime: event.DecisionTime,
			ClusterID: event.Cluster, NetBPS: event.NetOutcomeBPS,
		})
	}
	return out
}

func positiveClusterPValue(clusters map[string]float64) float64 {
	positive := 0
	for _, value := range clusters {
		if value > 0 {
			positive++
		}
	}
	n := len(clusters)
	var probability float64
	for k := positive; k <= n; k++ {
		probability += float64(binomial(n, k)) / math.Pow(2, float64(n))
	}
	return probability
}

func clusterMeanMap(observations []routerresearch.Observation) map[string]float64 {
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, observation := range observations {
		sums[observation.ClusterID] += observation.NetBPS
		counts[observation.ClusterID]++
	}
	out := map[string]float64{}
	for cluster, sum := range sums {
		out[cluster] = sum / float64(counts[cluster])
	}
	return out
}

func worstLeaveOneClusterOut(clusters map[string]float64) float64 {
	if len(clusters) < 2 {
		return math.Inf(-1)
	}
	worst := math.Inf(1)
	for omitted := range clusters {
		var sum float64
		count := 0
		for cluster, value := range clusters {
			if cluster != omitted {
				sum += value
				count++
			}
		}
		value := sum / float64(count)
		if value < worst {
			worst = value
		}
	}
	return worst
}

func meanObservations(observations []routerresearch.Observation) float64 {
	if len(observations) == 0 {
		return 0
	}
	var sum float64
	for _, observation := range observations {
		sum += observation.NetBPS
	}
	return sum / float64(len(observations))
}

func binomial(n, k int) int64 {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	result := int64(1)
	for i := 1; i <= k; i++ {
		result = result * int64(n-k+i) / int64(i)
	}
	return result
}
