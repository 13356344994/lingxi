package eventvalidation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

const checkpointSchemaVersion = "lingxi.router-g6-validation.checkpoint.v1"

type checkpointShockBlock struct {
	Cluster string  `json:"cluster"`
	Sum     float64 `json:"sum"`
	Count   int     `json:"count"`
}

type checkpointTierStats struct {
	Count         int                `json:"count"`
	Days          []string           `json:"days"`
	Symbols       []string           `json:"symbols"`
	ShockClusters []string           `json:"shock_clusters"`
	GrossSum      float64            `json:"gross_sum"`
	FeeSum        float64            `json:"fee_sum"`
	ConsSum       float64            `json:"cons_sum"`
	StressSum     float64            `json:"stress_sum"`
	CapacityFails int                `json:"capacity_fails"`
	PositiveSum   float64            `json:"positive_sum"`
	MaxPositive   float64            `json:"max_positive"`
	DayPositive   map[string]float64 `json:"day_positive"`
	DelaySum      map[int]float64    `json:"delay_sum"`
	DelayCount    map[int]int        `json:"delay_count"`
	WinnerCount   int                `json:"winner_count"`
	LossCount     int                `json:"loss_count"`
	NeutralCount  int                `json:"neutral_count"`
	ShockBlocks   []checkpointShockBlock `json:"shock_blocks"`
	DelayMeanBPS  map[int]float64    `json:"delay_mean_bps"`
}

type checkpointCardStats struct {
	EventCardHash string                       `json:"event_card_hash"`
	Tiers         map[int]checkpointTierStats  `json:"tiers"`
}

// Checkpoint binds resume state with catalog/config/map/card integrity.
type Checkpoint struct {
	SchemaVersion         string                       `json:"schema_version"`
	Generation            string                       `json:"generation"`
	ProtocolHash          string                       `json:"protocol_hash"`
	HistoryDataHash       string                       `json:"history_data_hash"`
	ConfigHash            string                       `json:"config_hash"`
	CatalogHash           string                       `json:"catalog_hash"`
	CardManifestHash      string                       `json:"card_manifest_hash"`
	EventCardHash         string                       `json:"event_card_hash"`
	ExperimentHash        string                       `json:"experiment_hash"`
	MapManifestHashes     map[string]string            `json:"map_manifest_hashes"`
	OutputDir             string                       `json:"output_dir"`
	NextCardIndex         int                          `json:"next_card_index"`
	NextSymbolIndexByCard map[string]int               `json:"next_symbol_index_by_card"`
	CardStats             []checkpointCardStats        `json:"card_stats"`
	LinesProcessed        int64                        `json:"lines_processed"`
	Done                  bool                         `json:"done"`
	SavedAt               time.Time                    `json:"saved_at"`
	CheckpointHash        string                       `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("eventvalidation: checkpoint path required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, false, nil
		}
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	if cp.SchemaVersion != checkpointSchemaVersion {
		return Checkpoint{}, false, errors.New("eventvalidation: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("eventvalidation: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = checkpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	hash, err := hashCheckpoint(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashCheckpoint(cp Checkpoint) (string, error) {
	payload := cp
	payload.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// VerifyCheckpointBinding rejects drift across catalog/config/maps/cards.
func VerifyCheckpointBinding(
	cp Checkpoint,
	cfg RunConfig,
	catalogHash string,
	bundle CardBundle,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("eventvalidation: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("eventvalidation: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("eventvalidation: checkpoint config hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("eventvalidation: checkpoint catalog hash mismatch")
	}
	if cp.CardManifestHash != cfg.CardManifestHash || cp.CardManifestHash != bundle.Manifest.ManifestHash {
		return errors.New("eventvalidation: checkpoint card manifest hash mismatch")
	}
	if cp.EventCardHash != bundle.Manifest.EventCardHash {
		return errors.New("eventvalidation: checkpoint event card hash mismatch")
	}
	if cp.ExperimentHash != bundle.Manifest.ExperimentHash {
		return errors.New("eventvalidation: checkpoint experiment hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("eventvalidation: checkpoint output dir mismatch")
	}
	for profile, want := range mapManifestHashes {
		if got := cp.MapManifestHashes[profile]; got != want {
			return fmt.Errorf("eventvalidation: checkpoint map manifest hash mismatch for %s", profile)
		}
	}
	return nil
}

func exportCardStats(accumulators []*cardAccumulator, cards []reverseevent.FrozenEventCard) []checkpointCardStats {
	out := make([]checkpointCardStats, len(cards))
	for i, card := range cards {
		tiers := map[int]checkpointTierStats{}
		if i < len(accumulators) && accumulators[i] != nil {
			for tier, acc := range accumulators[i].tiers {
				tiers[tier] = acc.snapshot()
			}
		}
		out[i] = checkpointCardStats{EventCardHash: card.EventCardHash, Tiers: tiers}
	}
	return out
}

func restoreAccumulators(stats []checkpointCardStats) []*cardAccumulator {
	out := make([]*cardAccumulator, len(stats))
	for i, cardStats := range stats {
		acc := newCardAccumulator()
		for tier, tierStats := range cardStats.Tiers {
			acc.tiers[tier] = restoreTierAccumulator(tierStats)
		}
		out[i] = acc
	}
	return out
}
