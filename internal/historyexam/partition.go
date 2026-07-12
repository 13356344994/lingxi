package historyexam

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Partition is the coarse role a correlated cluster plays. A cluster belongs to
// exactly one of these; no cluster may straddle two partitions.
type Partition string

const (
	PartitionResearch    Partition = "research"
	PartitionWalkForward Partition = "walk_forward"
	PartitionExam        Partition = "exam"
)

func validPartition(p Partition) bool {
	switch p {
	case PartitionResearch, PartitionWalkForward, PartitionExam:
		return true
	default:
		return false
	}
}

// Cluster is a synthetic fixture unit of correlated history (a contiguous time
// span within a single market regime and correlated instrument set). Its time
// range is an examiner-side hidden detail and never leaves this package to a
// researcher. Clusters are the atomic unit of leakage control.
type Cluster struct {
	ClusterID string       `json:"cluster_id"`
	Stratum   StratumInput `json:"stratum"`
	Start     time.Time    `json:"start"`
	End       time.Time    `json:"end"`
	Partition Partition    `json:"partition"`
}

// PartitionPlan is the full cluster assignment for one generation. EmbargoGap is
// the minimum time separation required between any two clusters that live in
// different partitions, so autocorrelation cannot leak an answer across the
// research / walk-forward / exam boundary.
type PartitionPlan struct {
	Clusters   []Cluster     `json:"clusters"`
	EmbargoGap time.Duration `json:"embargo_gap"`
}

type SplitPolicy struct {
	ResearchPermille    int
	WalkForwardPermille int
	EmbargoGap          time.Duration
}

// BuildChronologicalPlan assigns already-clustered history without consulting
// outcomes. Assignment is deterministic by (start,end,id): earliest clusters go
// to research, then walk-forward, and the final untouched tail becomes exams.
// The resulting plan must still satisfy cross-partition embargo.
func BuildChronologicalPlan(candidates []Cluster, policy SplitPolicy) (PartitionPlan, error) {
	if len(candidates) < 3 {
		return PartitionPlan{}, errors.New("at least three clusters are required for research/WF/exam")
	}
	if policy.ResearchPermille <= 0 || policy.WalkForwardPermille <= 0 ||
		policy.ResearchPermille+policy.WalkForwardPermille >= 1000 {
		return PartitionPlan{}, errors.New("split permille must leave positive research, WF, and exam shares")
	}
	ordered := append([]Cluster(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start.Equal(ordered[j].Start) {
			if ordered[i].End.Equal(ordered[j].End) {
				return ordered[i].ClusterID < ordered[j].ClusterID
			}
			return ordered[i].End.Before(ordered[j].End)
		}
		return ordered[i].Start.Before(ordered[j].Start)
	})
	researchN := len(ordered) * policy.ResearchPermille / 1000
	wfN := len(ordered) * policy.WalkForwardPermille / 1000
	if researchN < 1 {
		researchN = 1
	}
	if wfN < 1 {
		wfN = 1
	}
	if researchN+wfN >= len(ordered) {
		return PartitionPlan{}, errors.New("split leaves no exam clusters")
	}
	for i := range ordered {
		switch {
		case i < researchN:
			ordered[i].Partition = PartitionResearch
		case i < researchN+wfN:
			ordered[i].Partition = PartitionWalkForward
		default:
			ordered[i].Partition = PartitionExam
		}
	}
	plan := PartitionPlan{Clusters: ordered, EmbargoGap: policy.EmbargoGap}
	return plan, plan.Validate()
}

// Validate enforces the cluster isolation contract fail-closed:
//   - every cluster has a valid identity, stratum, ordered time range, and role;
//   - a cluster id never appears in more than one partition (no leakage);
//   - no two clusters in different partitions overlap or sit closer than the
//     embargo gap;
//   - the plan actually contains an exam partition to seal.
func (p PartitionPlan) Validate() error {
	if len(p.Clusters) == 0 {
		return errors.New("partition plan requires at least one cluster")
	}
	if p.EmbargoGap < 0 {
		return errors.New("embargo gap must not be negative")
	}
	partitionByCluster := make(map[string]Partition)
	counts := make(map[Partition]int)
	for i, c := range p.Clusters {
		id := strings.TrimSpace(c.ClusterID)
		if id == "" {
			return fmt.Errorf("cluster %d has an empty cluster id", i)
		}
		if !validPartition(c.Partition) {
			return fmt.Errorf("cluster %q has invalid partition %q", id, c.Partition)
		}
		if err := c.Stratum.Validate(); err != nil {
			return fmt.Errorf("cluster %q stratum invalid: %w", id, err)
		}
		if c.Start.IsZero() || c.End.IsZero() || !c.Start.Before(c.End) {
			return fmt.Errorf("cluster %q requires start strictly before end", id)
		}
		if prior, ok := partitionByCluster[id]; ok {
			if prior != c.Partition {
				return fmt.Errorf("cluster %q leaks across partitions %q and %q", id, prior, c.Partition)
			}
			return fmt.Errorf("cluster %q is declared more than once", id)
		}
		partitionByCluster[id] = c.Partition
		counts[c.Partition]++
	}
	if err := p.validateEmbargo(); err != nil {
		return err
	}
	if counts[PartitionExam] == 0 {
		return errors.New("partition plan has no exam clusters to seal")
	}
	return nil
}

// validateEmbargo checks every cross-partition pair for overlap and gap. Same
// partition pairs may abut freely; only boundary crossings need isolation.
func (p PartitionPlan) validateEmbargo() error {
	ordered := append([]Cluster(nil), p.Clusters...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start.Equal(ordered[j].Start) {
			return ordered[i].End.Before(ordered[j].End)
		}
		return ordered[i].Start.Before(ordered[j].Start)
	})
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			a, b := ordered[i], ordered[j]
			if a.Partition == b.Partition {
				continue
			}
			// ordered by start, so a.Start <= b.Start.
			if b.Start.Before(a.End) {
				return fmt.Errorf("clusters %q and %q overlap across partitions %q and %q", a.ClusterID, b.ClusterID, a.Partition, b.Partition)
			}
			if b.Start.Sub(a.End) < p.EmbargoGap {
				return fmt.Errorf("clusters %q and %q violate the cross-partition embargo of %s", a.ClusterID, b.ClusterID, p.EmbargoGap)
			}
		}
	}
	return nil
}

// ExamClusters returns the clusters assigned to the exam partition, sorted by
// start time so downstream sealing produces a deterministic, fixed order.
func (p PartitionPlan) ExamClusters() []Cluster {
	out := make([]Cluster, 0)
	for _, c := range p.Clusters {
		if c.Partition == PartitionExam {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out
}

// StrataCoverage reports, per partition, how many distinct market regimes are
// represented. It exposes only coarse counts keyed by an opaque stratum key,
// never a time range, so it is safe to summarize without leaking hidden ranges.
func (p PartitionPlan) StrataCoverage() (map[Partition]int, error) {
	perPartition := make(map[Partition]map[string]struct{})
	for _, c := range p.Clusters {
		key, err := c.Stratum.Key()
		if err != nil {
			return nil, err
		}
		if perPartition[c.Partition] == nil {
			perPartition[c.Partition] = make(map[string]struct{})
		}
		perPartition[c.Partition][key] = struct{}{}
	}
	out := make(map[Partition]int, len(perPartition))
	for part, set := range perPartition {
		out[part] = len(set)
	}
	return out, nil
}
