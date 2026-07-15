package counterfactual

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
	"strconv"
	"strings"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/fundingarchive"
	"lingxi/internal/datatruth"
)

const (
	DatasetManifestHash = "f9dc0944919709a49f765fe3291cf60a46952919f796add47d892af5f2808a6d"
	DatasetReviewerHash = "5fbb51bf0bab3be2649b57ba76ed68b2f714cda8a5fc7c0722a1a53f4c52edfb"
	G10ReviewerHash     = "9de865f7620f0252b22ed8bf53f1a6e008e9df5a021ac8e4564baff2899fc6b8"
)

type DatasetManifest struct {
	SchemaVersion string                  `json:"schema_version"`
	ManifestHash  string                  `json:"manifest_hash"`
	Shards        []datav3.ShardReceiptV2 `json:"shards"`
}

func LoadDiscoveryCandidates(root string, progress io.Writer) ([]Candidate, DatasetManifest, error) {
	path := filepath.Join(root, "research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3/dataset_manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, DatasetManifest{}, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, DatasetManifest{}, err
	}
	declared, _ := generic["manifest_hash"].(string)
	delete(generic, "manifest_hash")
	computed, _, err := datatruth.HashJSON(generic)
	if err != nil || declared != DatasetManifestHash || computed != declared {
		return nil, DatasetManifest{}, errors.New("hash-bound V3 dataset manifest mismatch")
	}
	var manifest DatasetManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, DatasetManifest{}, err
	}
	base := filepath.Dir(path)
	var candidates []Candidate
	for _, shard := range manifest.Shards {
		if shard.Partition != "discovery" {
			continue
		}
		rows, err := loadDiscoveryShard(base, shard)
		if err != nil {
			return nil, DatasetManifest{}, err
		}
		for _, row := range rows {
			candidates = append(candidates, CandidateFor(row))
		}
		if progress != nil {
			fmt.Fprintf(progress, "stage=read_shard path=%s rows=%d candidates=%d\n", shard.Path, len(rows), len(candidates))
		}
	}
	SortCandidates(candidates)
	return candidates, manifest, nil
}

func loadDiscoveryShard(base string, receipt datav3.ShardReceiptV2) ([]datav3.StateRowV2, error) {
	if receipt.Partition != "discovery" || !strings.HasPrefix(receipt.Path, "partition=discovery/") {
		return nil, errors.New("validation/leaf_wf/holdout shard refused")
	}
	path := filepath.Join(base, filepath.FromSlash(receipt.Path))
	hash, err := datav3.HashFileStreaming(path)
	if err != nil || hash != receipt.SHA256 {
		return nil, fmt.Errorf("discovery shard hash mismatch: %s", receipt.Path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
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
		if row.SchemaVersion != datav3.StateSchemaVersionV2 || row.Partition != "discovery" {
			return nil, errors.New("non-discovery state row refused")
		}
		rows = append(rows, row)
	}
	if int64(len(rows)) != receipt.Rows {
		return nil, fmt.Errorf("discovery shard row count mismatch: %s", receipt.Path)
	}
	return rows, nil
}

func LoadBars(root, instrument, expectedHash string) (map[int64]Bar, error) {
	path := filepath.Join(root, "records/history/okx_1h", instrument+".jsonl")
	hash, err := datav3.HashFileStreaming(path)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || hash != expectedHash {
		return nil, fmt.Errorf("raw bar source hash mismatch for %s", instrument)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	type rawBar struct {
		time                      int64
		open, high, low, close, q float64
	}
	var rawBars []rawBar
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if len(row.Data) < 8 {
			return nil, errors.New("raw bar has fewer than eight fields")
		}
		timestamp, err := strconv.ParseInt(row.Data[0], 10, 64)
		if err != nil {
			return nil, err
		}
		values := [5]float64{}
		for i, source := range []int{1, 2, 3, 4, 7} {
			values[i], err = strconv.ParseFloat(row.Data[source], 64)
			if err != nil || values[i] < 0 || (i < 4 && values[i] == 0) {
				return nil, errors.New("invalid raw OHLCV value")
			}
		}
		rawBars = append(rawBars, rawBar{timestamp, values[0], values[1], values[2], values[3], values[4]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(rawBars, func(i, j int) bool { return rawBars[i].time < rawBars[j].time })
	out := make(map[int64]Bar, len(rawBars))
	var rolling float64
	for i, raw := range rawBars {
		rolling += raw.q
		if i >= 24 {
			rolling -= rawBars[i-24].q
		}
		if i < 23 || raw.time-rawBars[i-23].time != 23*hourMS {
			continue
		}
		out[raw.time] = Bar{
			TimeMS: raw.time, Open: raw.open, High: raw.high, Low: raw.low, Close: raw.close,
			RollingQuoteVolumeUSDT: rolling,
		}
	}
	return out, nil
}

func LoadFunding(root string) (FundingSource, fundingarchive.Provenance, error) {
	raw, provenance, err := fundingarchive.LoadVerifiedSource(root)
	if err != nil {
		return FundingSource{}, fundingarchive.Provenance{}, err
	}
	source := FundingSource{Events: make(map[string][]FundingEvent, len(raw)), CompleteDays: provenance.CompleteDays}
	for instrument, byTime := range raw {
		events := make([]FundingEvent, 0, len(byTime))
		for timestamp, text := range byTime {
			rate, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return FundingSource{}, fundingarchive.Provenance{}, err
			}
			events = append(events, FundingEvent{TimeMS: timestamp, Rate: rate})
		}
		sort.Slice(events, func(i, j int) bool { return events[i].TimeMS < events[j].TimeMS })
		source.Events[instrument] = events
	}
	return source, provenance, nil
}
