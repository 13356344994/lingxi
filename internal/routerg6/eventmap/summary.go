package eventmap

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/routerg6/protocol"
)

// DiscoverySummary highlights event outcomes erased by costs.
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
	TotalOutcomes             int                       `json:"total_outcomes"`
	ConservativePositiveCells int                       `json:"conservative_positive_cells"`
	CostWipeoutCells          int                       `json:"cost_wipeout_cells"`
	CostWipeoutRate           float64                   `json:"cost_wipeout_rate"`
	CapacityFailureRate       float64                   `json:"capacity_failure_rate"`
	WorstConservativeBPS      float64                   `json:"worst_conservative_bps"`
	ConservativeP10BPS        float64                   `json:"conservative_p10_bps"`
	ByEventFamily             map[string]FamilySummary  `json:"by_event_family"`
	ByHorizon                 map[string]HorizonSummary `json:"by_horizon"`
	ExamActions               int                       `json:"exam_actions"`
}

type FamilySummary struct {
	Outcomes                 int     `json:"outcomes"`
	ConservativePositiveRate float64 `json:"conservative_positive_rate"`
	CostWipeoutRate          float64 `json:"cost_wipeout_rate"`
}

type HorizonSummary struct {
	Outcomes                 int     `json:"outcomes"`
	ConservativePositiveRate float64 `json:"conservative_positive_rate"`
	CostWipeoutRate          float64 `json:"cost_wipeout_rate"`
}

// BuildDiscoverySummary aggregates discovery partition map cells.
func BuildDiscoverySummary(profileID string, pm PartitionMap) DiscoverySummary {
	s := DiscoverySummary{
		ProfileID:             profileID,
		Partition:             pm.Partition,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		AvailablePrimaryFiles: pm.AvailablePrimaryFiles,
		SelectedPrimaryFiles:  pm.SelectedPrimaryFiles,
		SelectionTruncated:    pm.SelectionTruncated,
		FullMarket:            pm.FullMarket,
		MapScope:              pm.MapScope,
		ByEventFamily:         map[string]FamilySummary{},
		ByHorizon:             map[string]HorizonSummary{},
		WorstConservativeBPS:  math.Inf(1),
		ExamActions:           protocol.ExamActions,
	}
	var weightedWipeout, weightedCapacity, weightedP10 float64
	fAgg := map[string]*familyAgg{}
	hAgg := map[string]*horizonAgg{}
	for _, cell := range pm.Cells {
		stats := cell.Stats
		s.TotalOutcomes += stats.Count
		weightedWipeout += stats.CostWipeoutRate * float64(stats.Count)
		weightedCapacity += stats.CapacityFailureRate * float64(stats.Count)
		weightedP10 += stats.ConservativeP10BPS * float64(stats.Count)
		if stats.WorstConservativeBPS < s.WorstConservativeBPS {
			s.WorstConservativeBPS = stats.WorstConservativeBPS
		}
		if stats.ConservativePositiveRate > 0 {
			s.ConservativePositiveCells++
		}
		if stats.CostWipeoutRate > 0 {
			s.CostWipeoutCells++
		}
		fa := fAgg[cell.Key.EventFamily]
		if fa == nil {
			fa = &familyAgg{}
			fAgg[cell.Key.EventFamily] = fa
		}
		fa.add(stats)
		ha := hAgg[cell.Key.Horizon]
		if ha == nil {
			ha = &horizonAgg{}
			hAgg[cell.Key.Horizon] = ha
		}
		ha.add(stats)
	}
	if s.TotalOutcomes > 0 {
		n := float64(s.TotalOutcomes)
		s.CostWipeoutRate = weightedWipeout / n
		s.CapacityFailureRate = weightedCapacity / n
		s.ConservativeP10BPS = weightedP10 / n
	} else {
		s.WorstConservativeBPS = 0
	}
	families := make([]string, 0, len(fAgg))
	for f := range fAgg {
		families = append(families, f)
	}
	sort.Strings(families)
	for _, f := range families {
		s.ByEventFamily[f] = fAgg[f].summary()
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

type familyAgg struct {
	count                     int
	consRateSum, wipeRateSum  float64
}

func (a *familyAgg) add(s CellStats) {
	a.count += s.Count
	n := float64(s.Count)
	a.consRateSum += s.ConservativePositiveRate * n
	a.wipeRateSum += s.CostWipeoutRate * n
}

func (a familyAgg) summary() FamilySummary {
	if a.count == 0 {
		return FamilySummary{}
	}
	n := float64(a.count)
	return FamilySummary{
		Outcomes:                 a.count,
		ConservativePositiveRate: a.consRateSum / n,
		CostWipeoutRate:          a.wipeRateSum / n,
	}
}

type horizonAgg struct {
	count                    int
	consRateSum, wipeRateSum float64
}

func (a *horizonAgg) add(s CellStats) {
	a.count += s.Count
	n := float64(s.Count)
	a.consRateSum += s.ConservativePositiveRate * n
	a.wipeRateSum += s.CostWipeoutRate * n
}

func (a horizonAgg) summary() HorizonSummary {
	if a.count == 0 {
		return HorizonSummary{}
	}
	n := float64(a.count)
	return HorizonSummary{
		Outcomes:                 a.count,
		ConservativePositiveRate: a.consRateSum / n,
		CostWipeoutRate:          a.wipeRateSum / n,
	}
}

// WriteSummary atomically writes g6_event_map_summary.json.
func WriteSummary(outputDir string, summaries []DiscoverySummary) error {
	path := filepath.Join(outputDir, "g6_event_map_summary.json")
	payload := struct {
		Summaries   []DiscoverySummary `json:"summaries"`
		ExamActions int                `json:"exam_actions"`
	}{
		Summaries:   summaries,
		ExamActions: protocol.ExamActions,
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

const (
	scanProgressEveryLines     = 100000
	scanCheckpointEverySymbols = 25
)

// ProgressEveryLines returns the frozen progress cadence.
func ProgressEveryLines() int {
	return scanProgressEveryLines
}
