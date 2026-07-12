package routerresearch

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type Observation struct {
	CandidateID  string    `json:"candidate_id"`
	DecisionTime time.Time `json:"decision_time"`
	ClusterID    string    `json:"cluster_id"`
	NetBPS       float64   `json:"net_bps"`
}

type Options struct {
	Folds            int
	Purge            time.Duration
	Embargo          time.Duration
	MinSamples       int
	MinClusters      int
	FamilyWiseAlpha  float64
	ResearchDataHash string
}

type Fold struct {
	Fold      int     `json:"fold"`
	TrainN    int     `json:"train_n"`
	TestN     int     `json:"test_n"`
	TrainMean float64 `json:"train_mean_bps"`
	TestMean  float64 `json:"test_mean_bps"`
}

type Verdict struct {
	CandidateID                string  `json:"candidate_id"`
	Pass                       bool    `json:"pass"`
	Reason                     string  `json:"reason"`
	Samples                    int     `json:"samples"`
	Clusters                   int     `json:"clusters"`
	MeanBPS                    float64 `json:"mean_bps"`
	WorstLeaveOneClusterOutBPS float64 `json:"worst_leave_one_cluster_out_bps"`
	PValue                     float64 `json:"p_value"`
	HolmLimit                  float64 `json:"holm_limit"`
	Folds                      []Fold  `json:"folds"`
	EvidenceHash               string  `json:"evidence_hash"`
}

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	DataHash      string    `json:"data_hash"`
	Verdicts      []Verdict `json:"verdicts"`
}

func Run(observations []Observation, opts Options) (Report, error) {
	if opts.Folds <= 0 || opts.MinSamples <= 0 || opts.MinClusters < 2 ||
		opts.FamilyWiseAlpha <= 0 || opts.FamilyWiseAlpha >= 1 ||
		strings.TrimSpace(opts.ResearchDataHash) == "" {
		return Report{}, errors.New("routerresearch: invalid frozen options")
	}
	byCandidate := map[string][]Observation{}
	for _, observation := range observations {
		if strings.TrimSpace(observation.CandidateID) == "" || observation.DecisionTime.IsZero() ||
			strings.TrimSpace(observation.ClusterID) == "" || math.IsNaN(observation.NetBPS) ||
			math.IsInf(observation.NetBPS, 0) {
			return Report{}, errors.New("routerresearch: invalid observation")
		}
		byCandidate[observation.CandidateID] = append(byCandidate[observation.CandidateID], observation)
	}
	ids := make([]string, 0, len(byCandidate))
	for id := range byCandidate {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	report := Report{SchemaVersion: "lingxi.router-research.v1", DataHash: opts.ResearchDataHash}
	for _, id := range ids {
		verdict := evaluate(id, byCandidate[id], opts)
		report.Verdicts = append(report.Verdicts, verdict)
	}
	applyHolm(report.Verdicts, opts.FamilyWiseAlpha)
	for i := range report.Verdicts {
		hash, _, _ := datatruth.HashJSON(struct {
			DataHash string  `json:"data_hash"`
			Verdict  Verdict `json:"verdict"`
		}{opts.ResearchDataHash, report.Verdicts[i]})
		report.Verdicts[i].EvidenceHash = hash
	}
	return report, nil
}

func evaluate(id string, observations []Observation, opts Options) Verdict {
	sort.Slice(observations, func(i, j int) bool { return observations[i].DecisionTime.Before(observations[j].DecisionTime) })
	v := Verdict{CandidateID: id, Samples: len(observations), Reason: "ROBUSTNESS_GATE"}
	v.MeanBPS = meanObservations(observations)
	clusters := clusterMeans(observations)
	v.Clusters = len(clusters)
	if len(observations) < opts.MinSamples || len(clusters) < opts.MinClusters {
		v.Reason = "INCONCLUSIVE"
		return v
	}
	v.WorstLeaveOneClusterOutBPS = worstLeaveOneOut(clusters)
	if v.MeanBPS <= 0 || v.WorstLeaveOneClusterOutBPS <= 0 {
		return v
	}
	v.Folds = walkForward(observations, opts)
	if len(v.Folds) != opts.Folds {
		v.Reason = "INCONCLUSIVE"
		return v
	}
	for _, fold := range v.Folds {
		if fold.TrainN == 0 || fold.TestN == 0 || fold.TestMean <= 0 {
			return v
		}
	}
	positive := 0
	for _, value := range clusters {
		if value > 0 {
			positive++
		}
	}
	v.PValue = binomialUpperTail(len(clusters), positive)
	v.Reason = "PENDING_MULTIPLE_TESTING"
	return v
}

func walkForward(observations []Observation, opts Options) []Fold {
	if len(observations) < 2 {
		return nil
	}
	start := observations[0].DecisionTime
	end := observations[len(observations)-1].DecisionTime
	span := end.Sub(start)
	if span <= 0 {
		return nil
	}
	segment := span / time.Duration(opts.Folds+1)
	if segment <= 0 {
		return nil
	}
	var folds []Fold
	for i := 1; i <= opts.Folds; i++ {
		trainEnd := start.Add(segment * time.Duration(i))
		testStart := trainEnd.Add(opts.Purge + opts.Embargo)
		testEnd := testStart.Add(segment)
		if testStart.After(end) {
			break
		}
		var train, test []float64
		for _, observation := range observations {
			switch {
			case observation.DecisionTime.Before(trainEnd):
				train = append(train, observation.NetBPS)
			case !observation.DecisionTime.Before(testStart) && observation.DecisionTime.Before(testEnd):
				test = append(test, observation.NetBPS)
			}
		}
		folds = append(folds, Fold{
			Fold: i, TrainN: len(train), TestN: len(test),
			TrainMean: mean(train), TestMean: mean(test),
		})
	}
	return folds
}

func clusterMeans(observations []Observation) map[string]float64 {
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

func worstLeaveOneOut(clusters map[string]float64) float64 {
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

func applyHolm(verdicts []Verdict, alpha float64) {
	indexes := make([]int, 0)
	for i, verdict := range verdicts {
		if verdict.Reason == "PENDING_MULTIPLE_TESTING" {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return verdicts[indexes[i]].PValue < verdicts[indexes[j]].PValue })
	m := len(indexes)
	rejected := false
	for rank, index := range indexes {
		verdicts[index].HolmLimit = alpha / float64(m-rank)
		if rejected || verdicts[index].PValue > verdicts[index].HolmLimit {
			rejected = true
			verdicts[index].Pass = false
			verdicts[index].Reason = "FAMILY_MULTIPLE_TESTING"
		} else {
			verdicts[index].Pass = true
			verdicts[index].Reason = "PASS"
		}
	}
}

func binomialUpperTail(n, k int) float64 {
	var probability float64
	for i := k; i <= n; i++ {
		probability += float64(binomialCoefficient(n, i)) / math.Pow(2, float64(n))
	}
	return probability
}

func binomialCoefficient(n, k int) int64 {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	var result int64 = 1
	for i := 1; i <= k; i++ {
		result = result * int64(n-k+i) / int64(i)
	}
	return result
}

func meanObservations(observations []Observation) float64 {
	values := make([]float64, 0, len(observations))
	for _, observation := range observations {
		values = append(values, observation.NetBPS)
	}
	return mean(values)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func ValidateReport(report Report) error {
	if report.SchemaVersion == "" || report.DataHash == "" {
		return errors.New("routerresearch: incomplete report")
	}
	for _, verdict := range report.Verdicts {
		if verdict.CandidateID == "" || verdict.EvidenceHash == "" {
			return fmt.Errorf("routerresearch: incomplete verdict")
		}
	}
	return nil
}
