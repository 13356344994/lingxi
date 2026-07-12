package outcome

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// TrainingLabelEntry is one unbiased discovery training label. Never a feature input.
type TrainingLabelEntry struct {
	Symbol                     string                      `json:"symbol"`
	DecisionTime               time.Time                   `json:"decision_time"`
	StartState                 actionspace.PositionState   `json:"start_state"`
	StateLabel                 string                      `json:"state_label"`
	Action                     actionspace.Action          `json:"action"`
	Label                      string                      `json:"label"`
	ConservativeEquityDeltaBPS float64                     `json:"conservative_equity_delta_bps"`
	StressEquityDeltaBPS       float64                     `json:"stress_equity_delta_bps"`
	MarginUSDT                 float64                     `json:"margin_usdt"`
	TurnoverUSDT               float64                     `json:"turnover_usdt"`
	Priority                   uint64                      `json:"priority"`
}

// TrainingLabelReservoir keeps a profile-level deterministic unbiased label set.
type TrainingLabelReservoir struct {
	cap     int
	entries []TrainingLabelEntry
}

func NewTrainingLabelReservoir(cap int) *TrainingLabelReservoir {
	if cap <= 0 {
		cap = g5protocol.DiscoveryTrainingLabelReservoir
	}
	return &TrainingLabelReservoir{cap: cap}
}

func TrainingLabelPriority(symbol string, decision time.Time, stateLabel, label string) uint64 {
	key := symbol + "|" + decision.UTC().Format(time.RFC3339Nano) + "|" + stateLabel + "|" + label
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}

const trainingReservoirTrimBatch = 4096

func (r *TrainingLabelReservoir) Consider(rec RewardRecord) {
	if rec.Partition != PartitionDiscovery {
		return
	}
	entry := TrainingLabelEntry{
		Symbol:                     rec.Symbol,
		DecisionTime:               rec.DecisionTime.UTC(),
		StartState:                 rec.StartState,
		StateLabel:                 rec.StateLabel,
		Action:                     rec.Action,
		Label:                      rec.Label,
		ConservativeEquityDeltaBPS: rec.ConservativeEquityDeltaBPS,
		StressEquityDeltaBPS:       rec.StressEquityDeltaBPS,
		MarginUSDT:                 rec.MarginUSDT,
		TurnoverUSDT:               rec.TurnoverUSDT,
		Priority:                   TrainingLabelPriority(rec.Symbol, rec.DecisionTime, rec.StateLabel, rec.Label),
	}
	r.entries = append(r.entries, entry)
	if len(r.entries) <= r.cap+trainingReservoirTrimBatch {
		return
	}
	r.trim()
}

func (r *TrainingLabelReservoir) trim() {
	sort.Slice(r.entries, func(i, j int) bool { return trainingLabelLess(r.entries[i], r.entries[j]) })
	if len(r.entries) > r.cap {
		r.entries = r.entries[:r.cap]
	}
}

func trainingLabelLess(a, b TrainingLabelEntry) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if !a.DecisionTime.Equal(b.DecisionTime) {
		return a.DecisionTime.Before(b.DecisionTime)
	}
	if a.Symbol != b.Symbol {
		return a.Symbol < b.Symbol
	}
	return a.Label < b.Label
}

func (r *TrainingLabelReservoir) Entries() []TrainingLabelEntry {
	r.trim()
	out := append([]TrainingLabelEntry(nil), r.entries...)
	sort.Slice(out, func(i, j int) bool { return trainingLabelLess(out[i], out[j]) })
	return out
}

func (r *TrainingLabelReservoir) Count() int {
	return len(r.entries)
}
