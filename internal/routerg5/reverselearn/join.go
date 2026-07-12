package reverselearn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
)

// JoinKey is symbol + decision time for label/context merge.
func JoinKey(symbol string, decision time.Time) string {
	return symbol + "\x00" + decision.UTC().Format(time.RFC3339Nano)
}

// LoadDiscoveryContexts reads discovery-partition rows from feature shards.
func LoadDiscoveryContexts(featureDir string) (map[string]features.DecisionContext, error) {
	manifestPath := filepath.Join(featureDir, "feature_context_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest features.ContextManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	out := map[string]features.DecisionContext{}
	for _, shard := range manifest.Shards {
		rows, err := features.ReadContextShard(filepath.Join(featureDir, shard.FileName))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Partition != outcome.PartitionDiscovery {
				continue
			}
			key := JoinKey(row.Symbol, row.DecisionTime)
			out[key] = row
		}
	}
	return out, nil
}

// JoinDiscoveryExamples merges unbiased training labels with discovery contexts.
func JoinDiscoveryExamples(labels []outcome.TrainingLabelEntry, contexts map[string]features.DecisionContext) ([]TrainingExample, error) {
	var out []TrainingExample
	for _, label := range labels {
		key := JoinKey(label.Symbol, label.DecisionTime)
		ctx, ok := contexts[key]
		if !ok {
			continue
		}
		if err := features.RejectForbiddenContext(ctx.Features); err != nil {
			return nil, fmt.Errorf("reverselearn: forbidden feature leak for %s: %w", key, err)
		}
		out = append(out, TrainingExample{
			Symbol:       label.Symbol,
			DecisionTime: label.DecisionTime.UTC(),
			StateLabel:   label.StateLabel,
			StartState:   label.StartState,
			Action:       label.Action,
			Label:        label.Label,
			RewardBPS:    label.ConservativeEquityDeltaBPS,
			Features:     ctx.Features,
			Missing:      append([]string(nil), ctx.Missing...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DecisionTime.Equal(out[j].DecisionTime) {
			return out[i].DecisionTime.Before(out[j].DecisionTime)
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		if out[i].StateLabel != out[j].StateLabel {
			return out[i].StateLabel < out[j].StateLabel
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}
