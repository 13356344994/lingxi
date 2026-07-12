package outcome

import (
	"sort"
	"time"
)

// WinnerEntry is one bounded discovery winner kept for reverse learning.
type WinnerEntry struct {
	Symbol                     string    `json:"symbol"`
	DecisionTime               time.Time `json:"decision_time"`
	Label                      string    `json:"label"`
	ConservativeEquityDeltaBPS float64   `json:"conservative_equity_delta_bps"`
}

// WinnerReservoir keeps a fixed-capacity deterministic winner set per cell.
type WinnerReservoir struct {
	cap     int
	entries []WinnerEntry
}

func NewWinnerReservoir(cap int) *WinnerReservoir {
	return &WinnerReservoir{cap: cap}
}

func (r *WinnerReservoir) Consider(entry WinnerEntry) {
	r.entries = append(r.entries, entry)
	sort.Slice(r.entries, func(i, j int) bool {
		return winnerLess(r.entries[i], r.entries[j])
	})
	if len(r.entries) > r.cap {
		r.entries = r.entries[:r.cap]
	}
}

func (r *WinnerReservoir) Entries() []WinnerEntry {
	out := append([]WinnerEntry(nil), r.entries...)
	sort.Slice(out, func(i, j int) bool { return winnerLess(out[i], out[j]) })
	return out
}

func winnerLess(a, b WinnerEntry) bool {
	if a.ConservativeEquityDeltaBPS != b.ConservativeEquityDeltaBPS {
		return a.ConservativeEquityDeltaBPS > b.ConservativeEquityDeltaBPS
	}
	if !a.DecisionTime.Equal(b.DecisionTime) {
		return a.DecisionTime.Before(b.DecisionTime)
	}
	if a.Symbol != b.Symbol {
		return a.Symbol < b.Symbol
	}
	return a.Label < b.Label
}
