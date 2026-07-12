package historyexam

import (
	"fmt"
	"testing"
	"time"
)

func day(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

func regime(vol string) StratumInput {
	return StratumInput{Axes: map[string]string{"volatility": vol, "funding_state": "positive"}}
}

func isolatedPlan() PartitionPlan {
	return PartitionPlan{
		EmbargoGap: 24 * time.Hour,
		Clusters: []Cluster{
			{ClusterID: "r1", Stratum: regime("high"), Start: day(2024, 1, 1), End: day(2024, 2, 1), Partition: PartitionResearch},
			{ClusterID: "w1", Stratum: regime("low"), Start: day(2024, 3, 1), End: day(2024, 4, 1), Partition: PartitionWalkForward},
			{ClusterID: "e1", Stratum: regime("high"), Start: day(2024, 5, 1), End: day(2024, 6, 1), Partition: PartitionExam},
			{ClusterID: "e2", Stratum: regime("low"), Start: day(2024, 7, 1), End: day(2024, 8, 1), Partition: PartitionExam},
		},
	}
}

func TestPartitionPlanAcceptsIsolatedClusters(t *testing.T) {
	if err := isolatedPlan().Validate(); err != nil {
		t.Fatalf("cleanly isolated plan should pass: %v", err)
	}
}

func TestClusterMayNotLeakAcrossPartitions(t *testing.T) {
	plan := isolatedPlan()
	// Same cluster id reused in two partitions is leakage.
	plan.Clusters = append(plan.Clusters, Cluster{
		ClusterID: "r1", Stratum: regime("high"), Start: day(2024, 9, 1), End: day(2024, 10, 1), Partition: PartitionExam,
	})
	if err := plan.Validate(); err == nil {
		t.Fatal("a cluster appearing in two partitions must be rejected as leakage")
	}
}

func TestCrossPartitionOverlapRejected(t *testing.T) {
	plan := isolatedPlan()
	// Make exam e1 overlap the research cluster r1.
	plan.Clusters[2].Start = day(2024, 1, 15)
	plan.Clusters[2].End = day(2024, 2, 15)
	if err := plan.Validate(); err == nil {
		t.Fatal("overlapping research and exam clusters must be rejected")
	}
}

func TestCrossPartitionEmbargoRejected(t *testing.T) {
	plan := isolatedPlan()
	// Research ends 2024-02-01; put exam start only 1h later, below the 24h embargo.
	plan.Clusters[2].Start = day(2024, 2, 1).Add(time.Hour)
	plan.Clusters[2].End = day(2024, 2, 20)
	if err := plan.Validate(); err == nil {
		t.Fatal("insufficient cross-partition embargo must be rejected")
	}
}

func TestSamePartitionMayAbut(t *testing.T) {
	plan := isolatedPlan()
	// Two exam clusters abutting is fine (no cross-partition boundary).
	plan.Clusters[3].Start = plan.Clusters[2].End
	plan.Clusters[3].End = plan.Clusters[2].End.Add(30 * 24 * time.Hour)
	if err := plan.Validate(); err != nil {
		t.Fatalf("same-partition clusters may abut: %v", err)
	}
}

func TestPlanRequiresExamClusters(t *testing.T) {
	plan := isolatedPlan()
	plan.Clusters[2].Partition = PartitionResearch
	plan.Clusters[3].Partition = PartitionResearch
	if err := plan.Validate(); err == nil {
		t.Fatal("a plan with no exam clusters must be rejected")
	}
}

func TestExamClustersAreSortedByStart(t *testing.T) {
	got := isolatedPlan().ExamClusters()
	if len(got) != 2 || got[0].ClusterID != "e1" || got[1].ClusterID != "e2" {
		t.Fatalf("exam clusters must be sorted deterministically: %+v", got)
	}
}

func TestBuildChronologicalPlanLeavesUntouchedExamTail(t *testing.T) {
	var candidates []Cluster
	for i := 0; i < 10; i++ {
		start := day(2024, 1, 1).Add(time.Duration(i*48) * time.Hour)
		candidates = append(candidates, Cluster{
			ClusterID: fmt.Sprintf("c-%02d", i), Stratum: regime("high"),
			Start: start, End: start.Add(24 * time.Hour),
		})
	}
	plan, err := BuildChronologicalPlan(candidates, SplitPolicy{
		ResearchPermille: 600, WalkForwardPermille: 200, EmbargoGap: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[Partition]int{}
	for _, cluster := range plan.Clusters {
		counts[cluster.Partition]++
	}
	if counts[PartitionResearch] != 6 || counts[PartitionWalkForward] != 2 || counts[PartitionExam] != 2 {
		t.Fatalf("unexpected split: %+v", counts)
	}
}
