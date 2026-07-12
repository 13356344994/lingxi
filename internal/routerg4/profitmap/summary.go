package profitmap

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/datatruth"
)

// DiscoverySummary highlights gross opportunities erased by costs.
type DiscoverySummary struct {
	ProfileID                 string                    `json:"profile_id"`
	Partition                 string                    `json:"partition"`
	Generation                string                    `json:"generation"`
	ProtocolHash              string                    `json:"protocol_hash"`
	AvailablePrimaryFiles     int                       `json:"available_primary_files"`
	SelectedPrimaryFiles      int                       `json:"selected_primary_files"`
	SelectionTruncated        bool                      `json:"selection_truncated"`
	FullMarket                bool                      `json:"full_market"`
	MapScope                  string                    `json:"map_scope"`
	TotalOpportunities        int                       `json:"total_opportunities"`
	GrossPositiveCells        int                       `json:"gross_positive_cells"`
	ConservativePositiveCells int                       `json:"conservative_positive_cells"`
	CostWipeoutCells          int                       `json:"cost_wipeout_cells"`
	CostWipeoutRate           float64                   `json:"cost_wipeout_rate"`
	CapacityFailureRate       float64                   `json:"capacity_failure_rate"`
	WorstConservativeBPS      float64                   `json:"worst_conservative_bps"`
	ConservativeP10BPS        float64                   `json:"conservative_p10_bps"`
	ByHorizon                 map[string]HorizonSummary `json:"by_horizon"`
	BySymbolClass             map[string]int            `json:"by_symbol_class"`
	ExamActions               int                       `json:"exam_actions"`
}

type HorizonSummary struct {
	Opportunities            int     `json:"opportunities"`
	GrossPositiveRate        float64 `json:"gross_positive_rate"`
	ConservativePositiveRate float64 `json:"conservative_positive_rate"`
	CostWipeoutRate          float64 `json:"cost_wipeout_rate"`
	CapacityFailureRate      float64 `json:"capacity_failure_rate"`
	ConservativeP10BPS       float64 `json:"conservative_p10_bps"`
}

// BuildDiscoverySummary aggregates discovery partition map cells.
func BuildDiscoverySummary(profileID string, pm PartitionMap) DiscoverySummary {
	s := DiscoverySummary{
		ProfileID:             profileID,
		Partition:             pm.Partition,
		AvailablePrimaryFiles: pm.AvailablePrimaryFiles,
		SelectedPrimaryFiles:  pm.SelectedPrimaryFiles,
		SelectionTruncated:    pm.SelectionTruncated,
		FullMarket:            pm.FullMarket,
		MapScope:              pm.MapScope,
		ByHorizon:             map[string]HorizonSummary{},
		BySymbolClass:         map[string]int{},
		WorstConservativeBPS:  math.Inf(1),
	}
	var grossPos, consPos, wipeout int
	var weightedWipeout, weightedCapacity, weightedP10 float64
	hAgg := map[string]*horizonAgg{}
	for _, cell := range pm.Cells {
		stats := cell.Stats
		s.TotalOpportunities += stats.Count
		weightedWipeout += stats.CostWipeoutRate * float64(stats.Count)
		weightedCapacity += stats.CapacityFailureRate * float64(stats.Count)
		weightedP10 += stats.ConservativeP10BPS * float64(stats.Count)
		if stats.WorstConservativeBPS < s.WorstConservativeBPS {
			s.WorstConservativeBPS = stats.WorstConservativeBPS
		}
		if stats.GrossPositiveRate > 0 {
			grossPos++
		}
		if stats.ConservativePositiveRate > 0 {
			consPos++
		}
		if stats.CostWipeoutRate > 0 {
			wipeout++
		}
		s.BySymbolClass[cell.Key.SymbolClass] += stats.Count
		agg := hAgg[cell.Key.Horizon]
		if agg == nil {
			agg = &horizonAgg{}
			hAgg[cell.Key.Horizon] = agg
		}
		agg.add(stats)
	}
	s.GrossPositiveCells = grossPos
	s.ConservativePositiveCells = consPos
	s.CostWipeoutCells = wipeout
	if s.TotalOpportunities > 0 {
		n := float64(s.TotalOpportunities)
		s.CostWipeoutRate = weightedWipeout / n
		s.CapacityFailureRate = weightedCapacity / n
		s.ConservativeP10BPS = weightedP10 / n
	} else {
		s.WorstConservativeBPS = 0
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
	count                                                           int
	grossRateSum, consRateSum, wipeRateSum, capacityRateSum, p10Sum float64
}

func (a *horizonAgg) add(s CellStats) {
	a.count += s.Count
	n := float64(s.Count)
	a.grossRateSum += s.GrossPositiveRate * n
	a.consRateSum += s.ConservativePositiveRate * n
	a.wipeRateSum += s.CostWipeoutRate * n
	a.capacityRateSum += s.CapacityFailureRate * n
	a.p10Sum += s.ConservativeP10BPS * n
}

func (a horizonAgg) summary() HorizonSummary {
	if a.count == 0 {
		return HorizonSummary{}
	}
	n := float64(a.count)
	return HorizonSummary{
		Opportunities:            a.count,
		GrossPositiveRate:        a.grossRateSum / n,
		ConservativePositiveRate: a.consRateSum / n,
		CostWipeoutRate:          a.wipeRateSum / n,
		CapacityFailureRate:      a.capacityRateSum / n,
		ConservativeP10BPS:       a.p10Sum / n,
	}
}

// WriteSummary atomically writes g4_profit_map_summary.json.
func WriteSummary(outputDir string, summaries []DiscoverySummary) error {
	path := filepath.Join(outputDir, "g4_profit_map_summary.json")
	payload := struct {
		Summaries   []DiscoverySummary `json:"summaries"`
		ExamActions int                `json:"exam_actions"`
	}{
		Summaries: summaries,
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

// ConfigFingerprint hashes all CLI knobs for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
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
		return fmt.Errorf("profitmap: max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return fmt.Errorf("profitmap: progress_every must be positive")
	}
	return nil
}
