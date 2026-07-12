package g3reverse

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/routerg3/protocol"
)

const (
	RecheckVerdictPass         = "RECHECK_PASS"
	RecheckVerdictClosed       = "CLOSED"
	RecheckVerdictInconclusive = "INCONCLUSIVE"
)

// RecheckOptions configures discovery-recheck on frozen rules.
type RecheckOptions struct {
	RecheckEvents []Event
	Nominations   []Nomination
	Rules         []Rule
	Manifest      SearchManifest
	RecheckAlpha  float64
	MinSamples    int
	MinClusters   int
	Generation    string
	ProtocolHash  string
	Now           time.Time
}

// RecheckVerdict is the per-nomination recheck outcome.
type RecheckVerdict struct {
	CanonicalRuleHash          string  `json:"canonical_rule_hash"`
	RuleOrdinal                int     `json:"rule_ordinal"`
	Status                     string  `json:"status"`
	Reason                     string  `json:"reason"`
	RecheckDataHash            string  `json:"recheck_data_hash"`
	SampleCount                int     `json:"sample_count"`
	ClusterCount               int     `json:"cluster_count"`
	MeanBPS                    float64 `json:"mean_bps"`
	WorstLeaveOneClusterOutBPS float64 `json:"worst_leave_one_cluster_out_bps"`
	PositiveClusters           int     `json:"positive_clusters"`
	ClusterSignPValue          float64 `json:"cluster_sign_p_value"`
	HolmLimit                  float64 `json:"holm_limit,omitempty"`
	SearchManifestHash         string  `json:"search_manifest_hash"`
	Generation                 string  `json:"generation"`
	ProtocolHash               string  `json:"protocol_hash"`
}

// RecheckReport is the full discovery-recheck receipt.
type RecheckReport struct {
	GeneratedAt        time.Time        `json:"generated_at"`
	Partition          string           `json:"partition"`
	RecheckDataHash    string           `json:"recheck_data_hash"`
	SearchManifestHash string           `json:"search_manifest_hash"`
	RecheckAlpha       float64          `json:"recheck_alpha"`
	MinSamples         int              `json:"min_samples"`
	MinClusters        int              `json:"min_clusters"`
	NominationCount    int              `json:"nomination_count"`
	Verdicts           []RecheckVerdict `json:"verdicts"`
	ExamActions        int              `json:"exam_actions"`
}

func validateRecheckOptions(opts RecheckOptions) error {
	if err := opts.Manifest.Validate(); err != nil {
		return err
	}
	if opts.Generation != opts.Manifest.Generation || opts.ProtocolHash != opts.Manifest.ProtocolHash {
		return errors.New("g3reverse: recheck generation/protocol must match search manifest")
	}
	if opts.Generation != protocol.Generation || opts.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("g3reverse: recheck must use frozen G3 protocol")
	}
	rulesByHash := map[string]Rule{}
	for _, rule := range opts.Rules {
		if rule.CanonicalHash != "" {
			rulesByHash[rule.CanonicalHash] = rule
		}
	}
	for _, nomination := range opts.Nominations {
		if nomination.Kind != KindCandidateNomination {
			return fmt.Errorf("g3reverse: recheck only accepts %s nominations", KindCandidateNomination)
		}
		if _, ok := rulesByHash[nomination.CanonicalRuleHash]; !ok {
			return fmt.Errorf("g3reverse: nomination %s missing frozen rule", nomination.CanonicalRuleHash)
		}
	}
	if opts.RecheckAlpha != protocol.RecheckAlpha ||
		opts.MinSamples != protocol.G3MinRecheckSamples ||
		opts.MinClusters != protocol.G3MinRecheckClusters {
		return errors.New("g3reverse: recheck gates must equal frozen production protocol")
	}
	return nil
}

// Recheck applies frozen rules to recheck-partition events for induction
// nominations. Rules are never refit; output is RECHECK_PASS, CLOSED, or
// INCONCLUSIVE — never RESEARCH_PASS.
func Recheck(opts RecheckOptions) (RecheckReport, error) {
	if err := validateRecheckOptions(opts); err != nil {
		return RecheckReport{}, err
	}
	eligible, _ := filterEvaluationEvents(opts.RecheckEvents)
	dataHash, err := HashEvents(eligible)
	if err != nil {
		return RecheckReport{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rulesByHash := map[string]Rule{}
	for _, rule := range opts.Rules {
		rulesByHash[rule.CanonicalHash] = rule
	}
	verdicts := make([]RecheckVerdict, 0, len(opts.Nominations))
	for _, nomination := range opts.Nominations {
		rule := rulesByHash[nomination.CanonicalRuleHash]
		verdict := evaluateRecheckNomination(rule, eligible, dataHash, opts)
		verdicts = append(verdicts, verdict)
	}
	applyRecheckHolm(verdicts, opts.RecheckAlpha)
	sort.Slice(verdicts, func(i, j int) bool {
		if verdicts[i].RuleOrdinal != verdicts[j].RuleOrdinal {
			return verdicts[i].RuleOrdinal < verdicts[j].RuleOrdinal
		}
		return verdicts[i].CanonicalRuleHash < verdicts[j].CanonicalRuleHash
	})
	return RecheckReport{
		GeneratedAt:        now.UTC(),
		Partition:          string(PartitionRecheck),
		RecheckDataHash:    dataHash,
		SearchManifestHash: opts.Manifest.ManifestHash,
		RecheckAlpha:       opts.RecheckAlpha,
		MinSamples:         opts.MinSamples,
		MinClusters:        opts.MinClusters,
		NominationCount:    len(opts.Nominations),
		Verdicts:           verdicts,
		ExamActions:        protocol.ExamActions,
	}, nil
}

func evaluateRecheckNomination(rule Rule, events []Event, dataHash string, opts RecheckOptions) RecheckVerdict {
	verdict := RecheckVerdict{
		CanonicalRuleHash: rule.CanonicalHash, RuleOrdinal: rule.Ordinal,
		RecheckDataHash: dataHash, SearchManifestHash: opts.Manifest.ManifestHash,
		Generation: opts.Generation, ProtocolHash: opts.ProtocolHash,
		Status: RecheckVerdictClosed, Reason: "ROBUSTNESS_GATE",
	}
	var samples []float64
	clusterValues := map[string][]float64{}
	for _, event := range events {
		fires, hasFeature := rule.Fires(event)
		if !hasFeature || !fires {
			continue
		}
		samples = append(samples, event.NetOutcomeBPS)
		clusterValues[event.Cluster] = append(clusterValues[event.Cluster], event.NetOutcomeBPS)
	}
	clusters := aggregateClusterMeans(clusterValues)
	verdict.SampleCount = len(samples)
	verdict.ClusterCount = len(clusters)
	verdict.MeanBPS = meanFloats(samples)
	if len(clusters) >= 2 {
		verdict.WorstLeaveOneClusterOutBPS = worstLeaveOneClusterOut(clusters)
	}
	verdict.PositiveClusters = positiveClusterCount(clusters)
	if verdict.SampleCount < opts.MinSamples || verdict.ClusterCount < opts.MinClusters {
		verdict.Status = RecheckVerdictInconclusive
		verdict.Reason = "INCONCLUSIVE"
		return verdict
	}
	if verdict.MeanBPS <= 0 || verdict.WorstLeaveOneClusterOutBPS <= 0 {
		verdict.Status = RecheckVerdictClosed
		verdict.Reason = "ROBUSTNESS_GATE"
		return verdict
	}
	verdict.ClusterSignPValue = binomialUpperTail(verdict.ClusterCount, verdict.PositiveClusters)
	verdict.Reason = "PENDING_MULTIPLE_TESTING"
	return verdict
}

func applyRecheckHolm(verdicts []RecheckVerdict, alpha float64) {
	indexes := make([]int, 0, len(verdicts))
	for i, verdict := range verdicts {
		if verdict.Reason == "PENDING_MULTIPLE_TESTING" {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		return verdicts[indexes[i]].ClusterSignPValue < verdicts[indexes[j]].ClusterSignPValue
	})
	m := len(indexes)
	rejected := false
	for rank, index := range indexes {
		limit := alpha / float64(m-rank)
		verdicts[index].HolmLimit = limit
		if rejected || verdicts[index].ClusterSignPValue > limit {
			rejected = true
			verdicts[index].Status = RecheckVerdictClosed
			verdicts[index].Reason = "FAMILY_MULTIPLE_TESTING"
		} else {
			verdicts[index].Status = RecheckVerdictPass
			verdicts[index].Reason = RecheckVerdictPass
		}
	}
	for i := range verdicts {
		if strings.EqualFold(verdicts[i].Status, "RESEARCH_PASS") {
			verdicts[i].Status = RecheckVerdictClosed
			verdicts[i].Reason = "FORBIDDEN_RESEARCH_PASS"
		}
	}
}
