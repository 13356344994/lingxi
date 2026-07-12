package profitmap

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"sync"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

// A broad opportunity map can contain tens of thousands of cells. Thirty-two
// deterministic hash samples per cell keeps tail estimates useful while
// bounding full-market memory and checkpoint size.
const maxQuantileSamplesPerCell = protocol.MapQuantileReservoirPerCell

type valueSample struct {
	Priority uint64  `json:"priority"`
	Value    float64 `json:"value"`
}

// CellStats holds streaming aggregates for one map cell.
type CellStats struct {
	Count                int           `json:"opportunity_count"`
	GrossSum             float64       `json:"-"`
	FeeNetSum            float64       `json:"-"`
	ConservativeSum      float64       `json:"-"`
	GrossPositive        int           `json:"-"`
	FeeNetPositive       int           `json:"-"`
	ConservativePositive int           `json:"-"`
	CostWipeout          int           `json:"-"`
	CapacityFailures     int           `json:"-"`
	ConservativeSamples  []valueSample `json:"-"`
	WorstConservativeBPS float64       `json:"worst_conservative_bps"`

	GrossBPSMean             float64 `json:"gross_bps_mean"`
	FeeNetBPSMean            float64 `json:"fee_net_bps_mean"`
	ConservativeNetBPSMean   float64 `json:"conservative_net_bps_mean"`
	GrossPositiveRate        float64 `json:"gross_positive_rate"`
	FeeNetPositiveRate       float64 `json:"fee_net_positive_rate"`
	ConservativePositiveRate float64 `json:"conservative_net_positive_rate"`
	CostWipeoutRate          float64 `json:"cost_wipeout_rate"`
	CapacityFailureRate      float64 `json:"capacity_failure_rate"`
	ConservativeP10BPS       float64 `json:"conservative_p10_bps"`
	ConservativeP50BPS       float64 `json:"conservative_p50_bps"`
	ConservativeP90BPS       float64 `json:"conservative_p90_bps"`
}

func (s *CellStats) Add(opp Opportunity) {
	s.Count++
	s.GrossSum += opp.GrossBPS
	s.FeeNetSum += opp.FeeNetBPS
	s.ConservativeSum += opp.ConservativeBPS
	if s.Count == 1 || opp.ConservativeBPS < s.WorstConservativeBPS {
		s.WorstConservativeBPS = opp.ConservativeBPS
	}
	s.addQuantileSample(opp)
	if opp.GrossBPS > 0 {
		s.GrossPositive++
	}
	if opp.FeeNetBPS > 0 {
		s.FeeNetPositive++
	}
	if opp.ConservativeBPS > 0 {
		s.ConservativePositive++
	}
	if opp.CostWipeout {
		s.CostWipeout++
	}
	if opp.CapacityFail {
		s.CapacityFailures++
	}
}

func (s CellStats) Finalize() CellStats {
	out := s
	if out.Count > 0 {
		n := float64(out.Count)
		out.GrossBPSMean = out.GrossSum / n
		out.FeeNetBPSMean = out.FeeNetSum / n
		out.ConservativeNetBPSMean = out.ConservativeSum / n
		out.GrossPositiveRate = float64(out.GrossPositive) / n
		out.FeeNetPositiveRate = float64(out.FeeNetPositive) / n
		out.ConservativePositiveRate = float64(out.ConservativePositive) / n
		out.CostWipeoutRate = float64(out.CostWipeout) / n
		out.CapacityFailureRate = float64(out.CapacityFailures) / n
		values := make([]float64, len(out.ConservativeSamples))
		for i := range out.ConservativeSamples {
			values[i] = out.ConservativeSamples[i].Value
		}
		sort.Float64s(values)
		out.ConservativeP10BPS = boundedQuantile(values, 0.10)
		out.ConservativeP50BPS = boundedQuantile(values, 0.50)
		out.ConservativeP90BPS = boundedQuantile(values, 0.90)
	}
	return out
}

func (s *CellStats) addQuantileSample(opp Opportunity) {
	key := opp.Symbol + "|" + opp.DecisionTime.UTC().Format(time.RFC3339Nano) + "|" +
		opp.Horizon + "|" + string(opp.Direction) + "|" + itoa(opp.CapitalTier)
	sum := sha256.Sum256([]byte(key))
	s.ConservativeSamples = append(s.ConservativeSamples, valueSample{
		Priority: binary.BigEndian.Uint64(sum[:8]), Value: opp.ConservativeBPS,
	})
	sort.Slice(s.ConservativeSamples, func(i, j int) bool {
		if s.ConservativeSamples[i].Priority == s.ConservativeSamples[j].Priority {
			return s.ConservativeSamples[i].Value < s.ConservativeSamples[j].Value
		}
		return s.ConservativeSamples[i].Priority < s.ConservativeSamples[j].Priority
	})
	if len(s.ConservativeSamples) > maxQuantileSamplesPerCell {
		s.ConservativeSamples = s.ConservativeSamples[:maxQuantileSamplesPerCell]
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

// MapStore aggregates opportunities into partition maps with bounded cells.
type MapStore struct {
	mu        sync.Mutex
	cells     map[string]map[CellKey]*cellBucket
	reservoir map[string]map[CellKey]*WinnerReservoir
}

type cellBucket struct {
	stats CellStats
}

type CellSnapshot struct {
	Key                  CellKey       `json:"key"`
	Stats                CellStats     `json:"stats"`
	GrossSum             float64       `json:"gross_sum"`
	FeeNetSum            float64       `json:"fee_net_sum"`
	ConservativeSum      float64       `json:"conservative_sum"`
	GrossPositive        int           `json:"gross_positive"`
	FeeNetPositive       int           `json:"fee_net_positive"`
	ConservativePositive int           `json:"conservative_positive"`
	CostWipeout          int           `json:"cost_wipeout"`
	CapacityFailures     int           `json:"capacity_failures"`
	QuantileSamples      []valueSample `json:"quantile_samples"`
	Winners              []WinnerEntry `json:"winners,omitempty"`
}

type StoreSnapshot struct {
	Partitions map[string][]CellSnapshot `json:"partitions"`
}

func NewMapStore() *MapStore {
	return &MapStore{
		cells:     map[string]map[CellKey]*cellBucket{},
		reservoir: map[string]map[CellKey]*WinnerReservoir{},
	}
}

func (m *MapStore) Add(opp Opportunity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	part := m.cells[opp.Partition]
	if part == nil {
		part = map[CellKey]*cellBucket{}
		m.cells[opp.Partition] = part
	}
	key := cellKeyFromOpportunity(opp)
	bucket := part[key]
	if bucket == nil {
		bucket = &cellBucket{}
		part[key] = bucket
	}
	bucket.stats.Add(opp)
	if opp.Partition == PartitionDiscovery && opp.ConservativeBPS > 0 {
		rpart := m.reservoir[opp.Partition]
		if rpart == nil {
			rpart = map[CellKey]*WinnerReservoir{}
			m.reservoir[opp.Partition] = rpart
		}
		res := rpart[key]
		if res == nil {
			res = NewWinnerReservoir(MaxWinnersPerCell)
			rpart[key] = res
		}
		res.Consider(WinnerEntry{
			Symbol:          opp.Symbol,
			DecisionTime:    opp.DecisionTime,
			Horizon:         opp.Horizon,
			Direction:       string(opp.Direction),
			CapitalTier:     opp.CapitalTier,
			ConservativeBPS: opp.ConservativeBPS,
		})
	}
}

func cellKeyFromOpportunity(opp Opportunity) CellKey {
	return CellKey{
		SkyCoarse:   opp.SkyCoarse,
		Liquidity:   opp.LiquidityBucket,
		SymbolClass: opp.SymbolClass,
		Horizon:     opp.Horizon,
		Direction:   string(opp.Direction),
		CapitalTier: opp.CapitalTier,
	}
}

// PartitionMap is the serialized map for one partition.
type PartitionMap struct {
	ProfileID             string                      `json:"profile_id"`
	Partition             string                      `json:"partition"`
	AvailablePrimaryFiles int                         `json:"available_primary_files"`
	SelectedPrimaryFiles  int                         `json:"selected_primary_files"`
	SelectionTruncated    bool                        `json:"selection_truncated"`
	FullMarket            bool                        `json:"full_market"`
	MapScope              string                      `json:"map_scope"`
	Cells                 map[string]PartitionMapCell `json:"cells"`
	CellCount             int                         `json:"cell_count"`
}

type PartitionMapCell struct {
	Key     CellKey       `json:"key"`
	Stats   CellStats     `json:"stats"`
	Winners []WinnerEntry `json:"winners,omitempty"`
}

func (m *MapStore) ExportPartition(profileID, partition string) PartitionMap {
	return m.ExportPartitionScoped(profileID, partition, MapScope{
		FullMarket: true, MapScope: protocol.FullMarketScope,
	})
}

func (m *MapStore) ExportPartitionScoped(profileID, partition string, scope MapScope) PartitionMap {
	m.mu.Lock()
	defer m.mu.Unlock()
	part := m.cells[partition]
	out := PartitionMap{
		ProfileID:             profileID,
		Partition:             partition,
		AvailablePrimaryFiles: scope.AvailablePrimaryFiles,
		SelectedPrimaryFiles:  scope.SelectedPrimaryFiles,
		SelectionTruncated:    scope.SelectionTruncated,
		FullMarket:            scope.FullMarket,
		MapScope:              scope.MapScope,
		Cells:                 map[string]PartitionMapCell{},
	}
	if part == nil {
		return out
	}
	keys := make([]CellKey, 0, len(part))
	for k := range part {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return cellKeyString(keys[i]) < cellKeyString(keys[j])
	})
	for _, key := range keys {
		stats := part[key].stats.Finalize()
		cell := PartitionMapCell{Key: key, Stats: stats}
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

func (m *MapStore) CellCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, part := range m.cells {
		total += len(part)
	}
	return total
}

func (m *MapStore) Hash() (string, error) {
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

func (m *MapStore) ReservoirRiskHash() (string, error) {
	snapshot := m.Snapshot()
	hash, _, err := datatruth.HashJSON(snapshot)
	return hash, err
}

func (m *MapStore) Snapshot() StoreSnapshot {
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
				Key: key, Stats: s, GrossSum: s.GrossSum, FeeNetSum: s.FeeNetSum,
				ConservativeSum: s.ConservativeSum, GrossPositive: s.GrossPositive,
				FeeNetPositive: s.FeeNetPositive, ConservativePositive: s.ConservativePositive,
				CostWipeout: s.CostWipeout, CapacityFailures: s.CapacityFailures,
				QuantileSamples: append([]valueSample(nil), s.ConservativeSamples...),
			}
			if res := m.reservoir[partition][key]; res != nil {
				snap.Winners = res.Entries()
			}
			out.Partitions[partition] = append(out.Partitions[partition], snap)
		}
	}
	return out
}

func RestoreMapStore(snapshot StoreSnapshot) *MapStore {
	store := NewMapStore()
	for partition, cells := range snapshot.Partitions {
		store.cells[partition] = map[CellKey]*cellBucket{}
		for _, snap := range cells {
			s := snap.Stats
			s.GrossSum, s.FeeNetSum, s.ConservativeSum = snap.GrossSum, snap.FeeNetSum, snap.ConservativeSum
			s.GrossPositive, s.FeeNetPositive = snap.GrossPositive, snap.FeeNetPositive
			s.ConservativePositive, s.CostWipeout = snap.ConservativePositive, snap.CostWipeout
			s.CapacityFailures = snap.CapacityFailures
			s.ConservativeSamples = append([]valueSample(nil), snap.QuantileSamples...)
			store.cells[partition][snap.Key] = &cellBucket{stats: s}
			if len(snap.Winners) > 0 {
				if store.reservoir[partition] == nil {
					store.reservoir[partition] = map[CellKey]*WinnerReservoir{}
				}
				res := NewWinnerReservoir(MaxWinnersPerCell)
				for _, winner := range snap.Winners {
					res.Consider(winner)
				}
				store.reservoir[partition][snap.Key] = res
			}
		}
	}
	return store
}

func cellKeyString(k CellKey) string {
	return k.SkyCoarse + "|" + k.Liquidity + "|" + k.SymbolClass + "|" + k.Horizon + "|" + k.Direction + "|" + itoa(k.CapitalTier)
}

func itoa(v int) string {
	return sortKeyInt(v)
}

func sortKeyInt(v int) string {
	// small helper without fmt import churn in hot path
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
