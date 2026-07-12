package eventmap

import "fmt"

// WindowTracker enforces per-key non-overlapping event windows with per-mark cooldown.
type WindowTracker struct {
	lastEnd map[string]int
}

// NewWindowTracker creates a dedup tracker; cooldown is supplied per Mark call.
func NewWindowTracker() *WindowTracker {
	return &WindowTracker{lastEnd: map[string]int{}}
}

// CardTrackerKey identifies one symbol/family stream for validation/WF cards.
func CardTrackerKey(symbol, family string) string {
	return symbol + "|" + family
}

// OutcomeTrackerKey identifies one discovery outcome stream (horizon+delay isolated).
func OutcomeTrackerKey(symbol, family, horizon string, delayBars int) string {
	return fmt.Sprintf("%s|%s|%s|%d", symbol, family, horizon, delayBars)
}

// AllowKey reports whether a new event at barIndex may start for one tracker key.
func (w *WindowTracker) AllowKey(key string, barIndex int) bool {
	last, ok := w.lastEnd[key]
	if !ok {
		return true
	}
	return barIndex >= last
}

// MarkKey records the exclusive end bar for one accepted window.
func (w *WindowTracker) MarkKey(key string, barIndex, cooldownBars int) {
	if cooldownBars < 1 {
		cooldownBars = 1
	}
	end := barIndex + cooldownBars
	if prev, ok := w.lastEnd[key]; ok && prev > end {
		return
	}
	w.lastEnd[key] = end
}

// AllowCard reports whether a card-level trigger may fire (validation/WF).
func (w *WindowTracker) AllowCard(symbol, family string, barIndex int) bool {
	return w.AllowKey(CardTrackerKey(symbol, family), barIndex)
}

// MarkCard records one card-level non-overlap window.
func (w *WindowTracker) MarkCard(symbol, family string, barIndex, cooldownBars int) {
	w.MarkKey(CardTrackerKey(symbol, family), barIndex, cooldownBars)
}

// AllowOutcome reports whether one discovery outcome may emit.
func (w *WindowTracker) AllowOutcome(symbol, family, horizon string, delayBars, barIndex int) bool {
	return w.AllowKey(OutcomeTrackerKey(symbol, family, horizon, delayBars), barIndex)
}

// MarkOutcome records one discovery outcome non-overlap window.
func (w *WindowTracker) MarkOutcome(symbol, family, horizon string, delayBars, barIndex, cooldownBars int) {
	w.MarkKey(OutcomeTrackerKey(symbol, family, horizon, delayBars), barIndex, cooldownBars)
}
