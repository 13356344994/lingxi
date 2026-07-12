package research

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerresearch"
)

type Boundary struct {
	DiscoveryEnd time.Time
	ForwardStart time.Time
	WFStart      time.Time
	End          time.Time
}

type Options struct {
	MinForwardSamples  int
	MinForwardClusters int
	DiscoveryEmbargo   time.Duration
	ForwardAlpha       float64
	WF                 routerresearch.Options
}

type Result struct {
	CandidateID         string                  `json:"candidate_id"`
	Status              string                  `json:"status"`
	Reason              string                  `json:"reason"`
	ForwardSamples      int                     `json:"forward_samples"`
	ForwardClusters     int                     `json:"forward_clusters"`
	ForwardEvidenceHash string                  `json:"forward_evidence_hash,omitempty"`
	ForwardPValue       float64                 `json:"forward_p_value,omitempty"`
	ForwardHolmLimit    float64                 `json:"forward_holm_limit,omitempty"`
	WFVerdict           *routerresearch.Verdict `json:"wf_verdict,omitempty"`
}

type Report struct {
	SchemaVersion string   `json:"schema_version"`
	Results       []Result `json:"results"`
}

func Run(observations []routerresearch.Observation, boundary Boundary, opts Options) (Report, error) {
	if boundary.DiscoveryEnd.IsZero() || boundary.ForwardStart.IsZero() ||
		boundary.WFStart.IsZero() || boundary.End.IsZero() ||
		boundary.ForwardStart.Before(boundary.DiscoveryEnd) ||
		boundary.ForwardStart.Sub(boundary.DiscoveryEnd) < opts.DiscoveryEmbargo ||
		!boundary.ForwardStart.Before(boundary.WFStart) ||
		!boundary.WFStart.Before(boundary.End) ||
		opts.MinForwardSamples <= 0 || opts.MinForwardClusters < 2 ||
		opts.ForwardAlpha <= 0 || opts.ForwardAlpha >= 1 {
		return Report{}, errors.New("routerg2 research: invalid frozen boundary/options")
	}
	forwardByID := map[string][]routerresearch.Observation{}
	wfByID := map[string][]routerresearch.Observation{}
	for _, observation := range observations {
		if strings.TrimSpace(observation.CandidateID) == "" || observation.DecisionTime.IsZero() {
			return Report{}, errors.New("routerg2 research: invalid observation")
		}
		switch {
		case !observation.DecisionTime.Before(boundary.ForwardStart):
			if observation.DecisionTime.Before(boundary.WFStart) {
				forwardByID[observation.CandidateID] = append(forwardByID[observation.CandidateID], observation)
			} else if observation.DecisionTime.Before(boundary.End) {
				wfByID[observation.CandidateID] = append(wfByID[observation.CandidateID], observation)
			}
		}
	}
	ids := map[string]bool{}
	for id := range forwardByID {
		ids[id] = true
	}
	for id := range wfByID {
		ids[id] = true
	}
	var ordered []string
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	report := Report{SchemaVersion: "lingxi.router-g2-research.v1", Results: []Result{}}
	var wfEligible []routerresearch.Observation
	eligible := map[string]int{}
	preliminary := map[string]int{}
	for _, id := range ordered {
		forward := forwardByID[id]
		clusters := distinctClusters(forward)
		result := Result{
			CandidateID: id, Status: "CLOSED", Reason: "FORWARD_ROBUSTNESS_GATE",
			ForwardSamples: len(forward), ForwardClusters: clusters,
		}
		if len(forward) < opts.MinForwardSamples || clusters < opts.MinForwardClusters {
			result.Reason = "INCONCLUSIVE"
			report.Results = append(report.Results, result)
			continue
		}
		if mean(forward) <= 0 || worstClusterMean(forward) <= 0 {
			report.Results = append(report.Results, result)
			continue
		}
		result.Reason = "PENDING_FORWARD_HOLM"
		result.ForwardPValue = positiveClusterPValue(forward)
		report.Results = append(report.Results, result)
		preliminary[id] = len(report.Results) - 1
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
		hash, _, _ := datatruth.HashJSON(forwardByID[id])
		report.Results[index].Status = "FORWARD_PASS"
		report.Results[index].Reason = "PENDING_WF"
		report.Results[index].ForwardEvidenceHash = hash
		eligible[id] = index
		wfEligible = append(wfEligible, wfByID[id]...)
	}
	if len(wfEligible) == 0 {
		return report, nil
	}
	wfReport, err := routerresearch.Run(wfEligible, opts.WF)
	if err != nil {
		return Report{}, err
	}
	for i := range wfReport.Verdicts {
		verdict := wfReport.Verdicts[i]
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
			report.Results[index].Reason = verdict.Reason
		}
	}
	return report, nil
}

func positiveClusterPValue(observations []routerresearch.Observation) float64 {
	means := clusterMeanMap(observations)
	positive := 0
	for _, value := range means {
		if value > 0 {
			positive++
		}
	}
	n := len(means)
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

func distinctClusters(observations []routerresearch.Observation) int {
	set := map[string]bool{}
	for _, observation := range observations {
		if observation.ClusterID != "" {
			set[observation.ClusterID] = true
		}
	}
	return len(set)
}

func mean(observations []routerresearch.Observation) float64 {
	if len(observations) == 0 {
		return 0
	}
	var sum float64
	for _, observation := range observations {
		sum += observation.NetBPS
	}
	return sum / float64(len(observations))
}

func worstClusterMean(observations []routerresearch.Observation) float64 {
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, observation := range observations {
		sums[observation.ClusterID] += observation.NetBPS
		counts[observation.ClusterID]++
	}
	worst := 0.0
	first := true
	for cluster, sum := range sums {
		value := sum / float64(counts[cluster])
		if first || value < worst {
			worst = value
			first = false
		}
	}
	return worst
}
