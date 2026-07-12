package outcome

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/datatruth"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// CubeSummary highlights reward-cube aggregates for one partition.
type CubeSummary struct {
	ProfileID                 string                    `json:"profile_id"`
	Partition                 string                    `json:"partition"`
	Generation                string                    `json:"generation"`
	ProtocolHash              string                    `json:"protocol_hash"`
	AvailablePrimaryFiles     int                       `json:"available_primary_files"`
	SelectedPrimaryFiles      int                       `json:"selected_primary_files"`
	SelectionTruncated        bool                      `json:"selection_truncated"`
	FullMarket                bool                      `json:"full_market"`
	CubeScope                 string                    `json:"cube_scope"`
	TotalRewards              int                       `json:"total_rewards"`
	GrossPositiveCells        int                       `json:"gross_positive_cells"`
	ConservativePositiveCells int                       `json:"conservative_positive_cells"`
	CapacityFailureRate       float64                   `json:"capacity_failure_rate"`
	WorstConservativeDeltaBPS float64                   `json:"worst_conservative_delta_bps"`
	ConservativeDeltaP10BPS   float64                   `json:"conservative_delta_p10_bps"`
	ByHorizon                 map[string]HorizonSummary `json:"by_horizon"`
	ByState                   map[string]int            `json:"by_state"`
	ExamActions               int                       `json:"exam_actions"`
}

type HorizonSummary struct {
	Rewards                  int     `json:"rewards"`
	GrossPositiveRate        float64 `json:"gross_positive_rate"`
	ConservativePositiveRate float64 `json:"conservative_positive_rate"`
	CapacityFailureRate      float64 `json:"capacity_failure_rate"`
	ConservativeDeltaP10BPS  float64 `json:"conservative_delta_p10_bps"`
}

func BuildCubeSummary(profileID string, cube PartitionCube) CubeSummary {
	s := CubeSummary{
		ProfileID:                 profileID,
		Partition:                 cube.Partition,
		AvailablePrimaryFiles:     cube.AvailablePrimaryFiles,
		SelectedPrimaryFiles:      cube.SelectedPrimaryFiles,
		SelectionTruncated:        cube.SelectionTruncated,
		FullMarket:                cube.FullMarket,
		CubeScope:                 cube.CubeScope,
		ByHorizon:                 map[string]HorizonSummary{},
		ByState:                   map[string]int{},
		WorstConservativeDeltaBPS: math.Inf(1),
		ExamActions:               g5protocol.ExamActions,
	}
	var grossPos, consPos int
	var weightedCapacity, weightedP10 float64
	hAgg := map[string]*horizonAgg{}
	for _, cell := range cube.Cells {
		stats := cell.Stats
		s.TotalRewards += stats.Count
		weightedCapacity += stats.CapacityFailureRate * float64(stats.Count)
		weightedP10 += stats.ConservativeDeltaP10BPS * float64(stats.Count)
		if stats.WorstConservativeDeltaBPS < s.WorstConservativeDeltaBPS {
			s.WorstConservativeDeltaBPS = stats.WorstConservativeDeltaBPS
		}
		if stats.GrossPositiveRate > 0 {
			grossPos++
		}
		if stats.ConservativePositiveRate > 0 {
			consPos++
		}
		s.ByState[cell.Key.StateLabel] += stats.Count
		agg := hAgg[cell.Key.Horizon]
		if agg == nil {
			agg = &horizonAgg{}
			hAgg[cell.Key.Horizon] = agg
		}
		agg.add(stats)
	}
	s.GrossPositiveCells = grossPos
	s.ConservativePositiveCells = consPos
	if s.TotalRewards > 0 {
		n := float64(s.TotalRewards)
		s.CapacityFailureRate = weightedCapacity / n
		s.ConservativeDeltaP10BPS = weightedP10 / n
	} else {
		s.WorstConservativeDeltaBPS = 0
	}
	horizons := make([]string, 0, len(hAgg))
	for h := range hAgg {
		horizons = append(horizons, h)
	}
	sort.Strings(horizons)
	for _, h := range horizons {
		s.ByHorizon[h] = hAgg[h].summary()
	}
	return s
}

type horizonAgg struct {
	count                                                          int
	grossRateSum, consRateSum, capacityRateSum, p10Sum             float64
}

func (a *horizonAgg) add(s CellStats) {
	a.count += s.Count
	n := float64(s.Count)
	a.grossRateSum += s.GrossPositiveRate * n
	a.consRateSum += s.ConservativePositiveRate * n
	a.capacityRateSum += s.CapacityFailureRate * n
	a.p10Sum += s.ConservativeDeltaP10BPS * n
}

func (a horizonAgg) summary() HorizonSummary {
	if a.count == 0 {
		return HorizonSummary{}
	}
	n := float64(a.count)
	return HorizonSummary{
		Rewards:                  a.count,
		GrossPositiveRate:        a.grossRateSum / n,
		ConservativePositiveRate: a.consRateSum / n,
		CapacityFailureRate:      a.capacityRateSum / n,
		ConservativeDeltaP10BPS:  a.p10Sum / n,
	}
}

func WriteSummary(outputDir string, summaries []CubeSummary) error {
	path := filepath.Join(outputDir, "g5_reward_cube_summary.json")
	for i := range summaries {
		summaries[i].Generation = g5protocol.Generation
		summaries[i].ProtocolHash = g5protocol.RouterProtocolHash
		summaries[i].ExamActions = g5protocol.ExamActions
	}
	payload := struct {
		Summaries   []CubeSummary `json:"summaries"`
		ExamActions int           `json:"exam_actions"`
	}{
		Summaries:   summaries,
		ExamActions: g5protocol.ExamActions,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RunConfig is the bounded scan configuration bound into checkpoints.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	ProfileID           string `json:"profile_id"`
	HistoryRoot         string `json:"history_root"`
	OutputDir           string `json:"output_dir"`
	MaxFiles            int    `json:"max_files"`
	MaxLines            int    `json:"max_lines"`
	CatalogSymbolLimit  int    `json:"catalog_symbol_limit"`
	ProgressEvery       int    `json:"progress_every"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
	AllowTruncatedSmoke bool   `json:"allow_truncated_smoke"`
}

func (c RunConfig) Validate() error {
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return fmt.Errorf("outcome: max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return fmt.Errorf("outcome: progress_every must be positive")
	}
	return nil
}

func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func WritePartitionCube(outputDir string, cube PartitionCube) error {
	name := fmt.Sprintf("cube_%s_%s.json", cube.Partition, cube.ProfileID)
	path := filepath.Join(outputDir, name)
	raw, err := json.MarshalIndent(cube, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
