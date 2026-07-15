package pathvalidation

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
)

// AllowedPartitions is the exhaustive, frozen set of partitions this package
// will ever load rows from. "discovery" is excluded because its labels are
// sealed and already read by the tournament; "router_holdout" is excluded
// because router_holdout_unblinded=false for the entire duration of this
// validation stage.
var AllowedPartitions = map[string]bool{"validation": true, "leaf_wf": true}

// LoadPartitionRows streams every row of the requested partitions (only
// "validation" and/or "leaf_wf") from the already hash-verified dataset
// manifest, re-verifying each shard's own sha256 before decoding it. Any
// other partition name is refused unconditionally.
func LoadPartitionRows(root string, manifest counterfactual.DatasetManifest, partitions []string) (map[string][]datav3.StateRowV2, error) {
	for _, p := range partitions {
		if !AllowedPartitions[p] {
			return nil, fmt.Errorf("pathvalidation refuses partition %q: only validation and leaf_wf are permitted", p)
		}
	}
	requested := make(map[string]bool, len(partitions))
	for _, p := range partitions {
		requested[p] = true
	}
	base := filepath.Join(root, "research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3")
	out := make(map[string][]datav3.StateRowV2, len(partitions))
	shards := append([]datav3.ShardReceiptV2(nil), manifest.Shards...)
	sort.Slice(shards, func(i, j int) bool {
		if shards[i].Partition != shards[j].Partition {
			return shards[i].Partition < shards[j].Partition
		}
		return shards[i].Month < shards[j].Month
	})
	for _, shard := range shards {
		if !requested[shard.Partition] {
			continue
		}
		if !AllowedPartitions[shard.Partition] {
			return nil, fmt.Errorf("dataset manifest shard %s carries a refused partition", shard.Path)
		}
		rows, err := loadShard(base, shard)
		if err != nil {
			return nil, err
		}
		out[shard.Partition] = append(out[shard.Partition], rows...)
	}
	return out, nil
}

func loadShard(base string, receipt datav3.ShardReceiptV2) ([]datav3.StateRowV2, error) {
	if !AllowedPartitions[receipt.Partition] {
		return nil, errors.New("pathvalidation shard loader refuses non validation/leaf_wf partition")
	}
	expectedPrefix := "partition=" + receipt.Partition + "/"
	if len(receipt.Path) < len(expectedPrefix) || receipt.Path[:len(expectedPrefix)] != expectedPrefix {
		return nil, fmt.Errorf("shard path %q does not match declared partition %q", receipt.Path, receipt.Partition)
	}
	path := filepath.Join(base, filepath.FromSlash(receipt.Path))
	hash, err := datav3.HashFileStreaming(path)
	if err != nil {
		return nil, err
	}
	if hash != receipt.SHA256 {
		return nil, fmt.Errorf("shard hash mismatch: %s", receipt.Path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(file, 1<<20))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	decoder := json.NewDecoder(zr)
	rows := make([]datav3.StateRowV2, 0, receipt.Rows)
	for {
		var row datav3.StateRowV2
		if err := decoder.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if row.SchemaVersion != datav3.StateSchemaVersionV2 {
			return nil, errors.New("unexpected state row schema")
		}
		if row.Partition != receipt.Partition {
			return nil, fmt.Errorf("row partition %q does not match shard partition %q", row.Partition, receipt.Partition)
		}
		if !AllowedPartitions[row.Partition] {
			return nil, errors.New("pathvalidation refuses a row outside validation/leaf_wf")
		}
		rows = append(rows, row)
	}
	if int64(len(rows)) != receipt.Rows {
		return nil, fmt.Errorf("row count mismatch for shard %s: got %d want %d", receipt.Path, len(rows), receipt.Rows)
	}
	return rows, nil
}
