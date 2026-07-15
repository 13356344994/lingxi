package pathvalidation

import (
	"testing"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
)

func TestLoadPartitionRowsRefusesDiscovery(t *testing.T) {
	root := realRepoRoot(t)
	manifest := counterfactual.DatasetManifest{Shards: []datav3.ShardReceiptV2{
		{Partition: "discovery", Path: "partition=discovery/month=2024-01.jsonl.gz"},
	}}
	if _, err := LoadPartitionRows(root, manifest, []string{"discovery"}); err == nil {
		t.Fatal("expected the discovery partition to be refused")
	}
}

func TestLoadPartitionRowsRefusesRouterHoldout(t *testing.T) {
	root := realRepoRoot(t)
	manifest := counterfactual.DatasetManifest{Shards: []datav3.ShardReceiptV2{
		{Partition: "router_holdout", Path: "partition=router_holdout/month=2026-02.jsonl.gz"},
	}}
	if _, err := LoadPartitionRows(root, manifest, []string{"router_holdout"}); err == nil {
		t.Fatal("expected router_holdout to be refused")
	}
}

func TestLoadShardRefusesNonAllowedPartition(t *testing.T) {
	if _, err := loadShard(t.TempDir(), datav3.ShardReceiptV2{Partition: "discovery", Path: "partition=discovery/month=x.jsonl.gz"}); err == nil {
		t.Fatal("expected loadShard to refuse a discovery-partition receipt")
	}
	if _, err := loadShard(t.TempDir(), datav3.ShardReceiptV2{Partition: "router_holdout", Path: "partition=router_holdout/month=x.jsonl.gz"}); err == nil {
		t.Fatal("expected loadShard to refuse a router_holdout-partition receipt")
	}
}

func TestLoadPartitionRowsRefusesShardPartitionPathMismatch(t *testing.T) {
	manifest := counterfactual.DatasetManifest{Shards: []datav3.ShardReceiptV2{
		{Partition: "validation", Path: "partition=leaf_wf/month=2025-10.jsonl.gz"},
	}}
	if _, err := LoadPartitionRows(t.TempDir(), manifest, []string{"validation"}); err == nil {
		t.Fatal("expected a partition/path mismatch to be refused")
	}
}

func TestAllowedPartitionsIsExactlyValidationAndLeafWF(t *testing.T) {
	if len(AllowedPartitions) != 2 || !AllowedPartitions["validation"] || !AllowedPartitions["leaf_wf"] {
		t.Fatalf("AllowedPartitions drifted: %+v", AllowedPartitions)
	}
	if AllowedPartitions["discovery"] || AllowedPartitions["router_holdout"] {
		t.Fatal("discovery/router_holdout must never be allowed")
	}
}
