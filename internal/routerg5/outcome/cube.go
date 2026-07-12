package outcome

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"sync"
	"time"

	"lingxi/internal/datatruth"
)

// CellStats holds streaming aggregates for one reward-cube cell.
type CellStats struct {
	Count                     int           `json:"reward_count"`
	GrossSum                  float64       `json:"-"`
	ConservativeSum           float64       `json:"-"`
	StressSum                 float64       `json:"-"`
	NetSum                    float64       `json:"-"`
	MarginSum                 float64       `json:"-"`
	TurnoverSum               float64       `json:"-"`
	GrossPositive             int           `json:"-"`
	ConservativePositive      int           `json:"-"`
	CapacityFailures          int           `json:"-"`
	ConservativeSamples       []valueSample `json:"-"`
	WorstConservativeDeltaBPS float64       `json:"worst_conservative_delta_bps"`

	GrossDeltaBPSMean             float64 `json:"gross_equity_delta_bps_mean"`
	ConservativeDeltaBPSMean      float64 `json:"conservative_equity_delta_bps_mean"`
	StressDeltaBPSMean            float64 `json:"stress_equity_delta_bps_mean"`
	NetDeltaBPSMean               float64 `json:"net_equity_delta_bps_mean"`
	MarginUSDTMean                float64 `json:"margin_usdt_mean"`
	TurnoverUSDTMean              float64 `json:"turnover_usdt_mean"`
	GrossPositiveRate             float64 `json:"gross_positive_rate"`
	ConservativePositiveRate      float64 `json:"conservative_positive_rate"`
	CapacityFailureRate           float64 `json:"capacity_failure_rate"`
	ConservativeDeltaP10BPS       float64 `json:"conservative_delta_p10_bps"`
	ConservativeDeltaP50BPS       float64 `json:"conservative_delta_p50_bps"`
	ConservativeDeltaP90BPS       float64 `json:"conservative_delta_p90_bps"`
}

type valueSample struct {
	Priority uint64  `json:"priority"`
	Value    float64 `json:"value"`
}

func (s *CellStats) Add(rec RewardRecord) {
	s.Count++
	s.GrossSum += rec.GrossEquityDeltaBPS
	s.ConservativeSum += rec.ConservativeEquityDeltaBPS
	s.StressSum += rec.StressEquityDeltaBPS
	s.NetSum += rec.NetEquityDeltaBPS
	s.MarginSum += rec.MarginUSDT
	s.TurnoverSum += rec.TurnoverUSDT
	if s.Count == 1 || rec.ConservativeEquityDeltaBPS < s.WorstConservativeDeltaBPS {
		s.WorstConservativeDeltaBPS = rec.ConservativeEquityDeltaBPS
	}
	s.addQuantileSample(rec)
	if rec.GrossEquityDeltaBPS > 0 {
		s.GrossPositive++
	}
	if rec.ConservativeEquityDeltaBPS > 0 {
		s.ConservativePositive++
	}
	if rec.CapacityFail {
		s.CapacityFailures++
	}
}

func (s CellStats) Finalize() CellStats {
	out := s
	if out.Count > 0 {
		n := float64(out.Count)
		out.GrossDeltaBPSMean = out.GrossSum / n
		out.ConservativeDeltaBPSMean = out.ConservativeSum / n
		out.StressDeltaBPSMean = out.StressSum / n
		out.NetDeltaBPSMean = out.NetSum / n
		out.MarginUSDTMean = out.MarginSum / n
		out.TurnoverUSDTMean = out.TurnoverSum / n
		out.GrossPositiveRate = float64(out.GrossPositive) / n
		out.ConservativePositiveRate = float64(out.ConservativePositive) / n
		out.CapacityFailureRate = float64(out.CapacityFailures) / n
		values := make([]float64, len(out.ConservativeSamples))
		for i := range out.ConservativeSamples {
			values[i] = out.ConservativeSamples[i].Value
		}
		sort.Float64s(values)
		out.ConservativeDeltaP10BPS = boundedQuantile(values, 0.10)
		out.ConservativeDeltaP50BPS = boundedQuantile(values, 0.50)
		out.ConservativeDeltaP90BPS = boundedQuantile(values, 0.90)
	}
	return out
}

func (s *CellStats) addQuantileSample(rec RewardRecord) {
	key := rec.Symbol + "|" + rec.DecisionTime.UTC().Format(time.RFC3339Nano) + "|" + rec.Label
	sum := sha256.Sum256([]byte(key))
	s.ConservativeSamples = append(s.ConservativeSamples, valueSample{
		Priority: binary.BigEndian.Uint64(sum[:8]),
		Value:    rec.ConservativeEquityDeltaBPS,
	})
	sort.Slice(s.ConservativeSamples, func(i, j int) bool {
		if s.ConservativeSamples[i].Priority == s.ConservativeSamples[j].Priority {
			return s.ConservativeSamples[i].Value < s.ConservativeSamples[j].Value
		}
		return s.ConservativeSamples[i].Priority < s.ConservativeSamples[j].Priority
	})
	if len(s.ConservativeSamples) > QuantileReservoirPerCell {
		s.ConservativeSamples = s.ConservativeSamples[:QuantileReservoirPerCell]
	}
}

func boundedQuantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	pos := q * float64(len(values)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(values) {
		return values[len(values)-1]
	}
	frac := pos - float64(lo)
	return values[lo] + frac*(values[lo+1]-values[lo])
}

// CubeStore aggregates reward records into bounded partition cubes.
type CubeStore struct {
	mu             sync.Mutex
	cells          map[string]map[CellKey]*cellBucket
	reservoir      map[string]map[CellKey]*WinnerReservoir
	trainingLabels *TrainingLabelReservoir
}

type cellBucket struct {
	stats CellStats
}

type CellSnapshot struct {
	Key                  CellKey       `json:"key"`
	Stats                CellStats     `json:"stats"`
	GrossSum             float64       `json:"gross_sum"`
	ConservativeSum      float64       `json:"conservative_sum"`
	StressSum            float64       `json:"stress_sum"`
	NetSum               float64       `json:"net_sum"`
	MarginSum            float64       `json:"margin_sum"`
	TurnoverSum          float64       `json:"turnover_sum"`
	GrossPositive        int           `json:"gross_positive"`
	ConservativePositive int           `json:"conservative_positive"`
	CapacityFailures     int           `json:"capacity_failures"`
	QuantileSamples      []valueSample `json:"quantile_samples"`
	Winners              []WinnerEntry `json:"winners,omitempty"`
}

type StoreSnapshot struct {
	Partitions     map[string][]CellSnapshot `json:"partitions"`
	TrainingLabels []TrainingLabelEntry      `json:"training_labels,omitempty"`
}

func NewCubeStore() *CubeStore {
	return &CubeStore{
		cells:          map[string]map[CellKey]*cellBucket{},
		reservoir:      map[string]map[CellKey]*WinnerReservoir{},
		trainingLabels: NewTrainingLabelReservoir(DiscoveryTrainingLabelReservoirCap()),
	}
}

func (m *CubeStore) Add(rec RewardRecord, liq, symbolClass string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	part := m.cells[rec.Partition]
	if part == nil {
		part = map[CellKey]*cellBucket{}
		m.cells[rec.Partition] = part
	}
	key := CellKeyFromRecord(rec, liq, symbolClass)
	bucket := part[key]
	if bucket == nil {
		bucket = &cellBucket{}
		part[key] = bucket
	}
	bucket.stats.Add(rec)
	if rec.Partition == PartitionDiscovery {
		m.trainingLabels.Consider(rec)
		if rec.ConservativeEquityDeltaBPS > 0 {
			rpart := m.reservoir[rec.Partition]
			if rpart == nil {
				rpart = map[CellKey]*WinnerReservoir{}
				m.reservoir[rec.Partition] = rpart
			}
			res := rpart[key]
			if res == nil {
				res = NewWinnerReservoir(WinnerReservoirPerCell)
				rpart[key] = res
			}
			res.Consider(WinnerEntry{
				Symbol:                     rec.Symbol,
				DecisionTime:               rec.DecisionTime,
				Label:                      rec.Label,
				ConservativeEquityDeltaBPS: rec.ConservativeEquityDeltaBPS,
			})
		}
	}
}

// PartitionCube is the serialized cube for one partition.
type PartitionCube struct {
	ProfileID             string                     `json:"profile_id"`
	Partition             string                     `json:"partition"`
	AvailablePrimaryFiles int                        `json:"available_primary_files"`
	SelectedPrimaryFiles  int                        `json:"selected_primary_files"`
	SelectionTruncated    bool                       `json:"selection_truncated"`
	FullMarket            bool                       `json:"full_market"`
	CubeScope             string                     `json:"cube_scope"`
	Cells                 map[string]PartitionCubeCell `json:"cells"`
	CellCount             int                        `json:"cell_count"`
}

type PartitionCubeCell struct {
	Key     CellKey   `json:"key"`
	Stats   CellStats `json:"stats"`
	Winners []WinnerEntry `json:"winners,omitempty"`
}

func (m *CubeStore) ExportPartitionScoped(profileID, partition string, scope CubeScope) PartitionCube {
	m.mu.Lock()
	defer m.mu.Unlock()
	part := m.cells[partition]
	out := PartitionCube{
		ProfileID:             profileID,
		Partition:             partition,
		AvailablePrimaryFiles: scope.AvailablePrimaryFiles,
		SelectedPrimaryFiles:  scope.SelectedPrimaryFiles,
		SelectionTruncated:    scope.SelectionTruncated,
		FullMarket:            scope.FullMarket,
		CubeScope:             scope.CubeScope,
		Cells:                 map[string]PartitionCubeCell{},
	}
	if part == nil {
		return out
	}
	keys := make([]CellKey, 0, len(part))
	for k := range part {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return cellKeyString(keys[i]) < cellKeyString(keys[j]) })
	for _, key := range keys {
		stats := part[key].stats.Finalize()
		cell := PartitionCubeCell{Key: key, Stats: stats}
		if partition == PartitionDiscovery {
			if res := m.reservoir[partition][key]; res != nil {
				cell.Winners = res.Entries()
			}
		}
		out.Cells[cellKeyString(key)] = cell
	}
	out.CellCount = len(out.Cells)
	return out
}

func (m *CubeStore) TrainingLabels() []TrainingLabelEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.trainingLabels == nil {
		return nil
	}
	return m.trainingLabels.Entries()
}

func (m *CubeStore) CellCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, part := range m.cells {
		total += len(part)
	}
	return total
}

func (m *CubeStore) Hash() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type export struct {
		Cells     map[string]map[string]CellStats     `json:"cells"`
		Reservoir map[string]map[string][]WinnerEntry `json:"reservoir"`
	}
	payload := export{
		Cells:     map[string]map[string]CellStats{},
		Reservoir: map[string]map[string][]WinnerEntry{},
	}
	for partName, part := range m.cells {
		payload.Cells[partName] = map[string]CellStats{}
		for key, bucket := range part {
			payload.Cells[partName][cellKeyString(key)] = bucket.stats.Finalize()
		}
	}
	for partName, part := range m.reservoir {
		payload.Reservoir[partName] = map[string][]WinnerEntry{}
		for key, res := range part {
			payload.Reservoir[partName][cellKeyString(key)] = res.Entries()
		}
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func (m *CubeStore) ReservoirRiskHash() (string, error) {
	snapshot := m.Snapshot()
	hash, _, err := datatruth.HashJSON(snapshot)
	return hash, err
}

func (m *CubeStore) Snapshot() StoreSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := StoreSnapshot{Partitions: map[string][]CellSnapshot{}}
	for partition, part := range m.cells {
		keys := make([]CellKey, 0, len(part))
		for key := range part {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return cellKeyString(keys[i]) < cellKeyString(keys[j]) })
		for _, key := range keys {
			s := part[key].stats
			snap := CellSnapshot{
				Key: key, Stats: s,
				GrossSum: s.GrossSum, ConservativeSum: s.ConservativeSum, StressSum: s.StressSum,
				NetSum: s.NetSum, MarginSum: s.MarginSum, TurnoverSum: s.TurnoverSum,
				GrossPositive: s.GrossPositive, ConservativePositive: s.ConservativePositive,
				CapacityFailures: s.CapacityFailures,
				QuantileSamples:  append([]valueSample(nil), s.ConservativeSamples...),
			}
			if res := m.reservoir[partition][key]; res != nil {
				snap.Winners = res.Entries()
			}
			out.Partitions[partition] = append(out.Partitions[partition], snap)
		}
	}
	if m.trainingLabels != nil {
		out.TrainingLabels = m.trainingLabels.Entries()
	}
	return out
}

func RestoreCubeStore(snapshot StoreSnapshot) *CubeStore {
	store := NewCubeStore()
	for partition, cells := range snapshot.Partitions {
		store.cells[partition] = map[CellKey]*cellBucket{}
		for _, snap := range cells {
			s := snap.Stats
			s.GrossSum, s.ConservativeSum = snap.GrossSum, snap.ConservativeSum
			s.StressSum, s.NetSum = snap.StressSum, snap.NetSum
			s.MarginSum, s.TurnoverSum = snap.MarginSum, snap.TurnoverSum
			s.GrossPositive, s.ConservativePositive = snap.GrossPositive, snap.ConservativePositive
			s.CapacityFailures = snap.CapacityFailures
			s.ConservativeSamples = append([]valueSample(nil), snap.QuantileSamples...)
			store.cells[partition][snap.Key] = &cellBucket{stats: s}
			if len(snap.Winners) > 0 {
				if store.reservoir[partition] == nil {
					store.reservoir[partition] = map[CellKey]*WinnerReservoir{}
				}
				res := NewWinnerReservoir(WinnerReservoirPerCell)
				for _, winner := range snap.Winners {
					res.Consider(winner)
				}
				store.reservoir[partition][snap.Key] = res
			}
		}
	}
	if len(snapshot.TrainingLabels) > 0 {
		store.trainingLabels = NewTrainingLabelReservoir(DiscoveryTrainingLabelReservoirCap())
		for _, entry := range snapshot.TrainingLabels {
			store.trainingLabels.Consider(RewardRecord{
				Symbol:                     entry.Symbol,
				Partition:                  PartitionDiscovery,
				DecisionTime:               entry.DecisionTime,
				StartState:                 entry.StartState,
				StateLabel:                 entry.StateLabel,
				Action:                     entry.Action,
				Label:                      entry.Label,
				ConservativeEquityDeltaBPS: entry.ConservativeEquityDeltaBPS,
				StressEquityDeltaBPS:       entry.StressEquityDeltaBPS,
				MarginUSDT:                 entry.MarginUSDT,
				TurnoverUSDT:               entry.TurnoverUSDT,
			})
		}
	}
	return store
}

func cellKeyString(k CellKey) string {
	return k.StateLabel + "|" + k.Verb + "|" + k.Direction + "|" + k.Horizon + "|" +
		formatSize(k.SizeFraction) + "|" + itoa(k.DelayBars) + "|" + k.SoftRegime + "|" +
		k.Liquidity + "|" + k.SymbolClass
}

func formatSize(v float64) string {
	return fmtSize(v)
}

func fmtSize(v float64) string {
	return strconvFormatFloat(v)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 12)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func strconvFormatFloat(v float64) string {
	// keep stable two-decimal formatting without fmt import churn
	whole := int64(v * 100)
	sign := ""
	if whole < 0 {
		sign = "-"
		whole = -whole
	}
	return sign + itoa(int(whole/100)) + "." + pad2(int(whole%100))
}

func pad2(v int) string {
	if v < 10 {
		return "0" + itoa(v)
	}
	return itoa(v)
}

// CubeScope describes bounded catalog selection metadata.
type CubeScope struct {
	AvailablePrimaryFiles int    `json:"available_primary_files"`
	SelectedPrimaryFiles  int    `json:"selected_primary_files"`
	SelectionTruncated    bool   `json:"selection_truncated"`
	FullMarket            bool   `json:"full_market"`
	CubeScope             string `json:"cube_scope"`
}
