package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const crossVenueSwitchIntentsFileName = "switch_intents.json"

var (
	switchIntentsFileLocksMu sync.Mutex
	switchIntentsFileLocks   = make(map[string]*sync.Mutex)
)

func switchIntentsFileLock(path string) *sync.Mutex {
	switchIntentsFileLocksMu.Lock()
	defer switchIntentsFileLocksMu.Unlock()
	if m, ok := switchIntentsFileLocks[path]; ok {
		return m
	}
	m := &sync.Mutex{}
	switchIntentsFileLocks[path] = m
	return m
}

func (e *CrossVenueExecutor) switchIntentsLock() *sync.Mutex {
	return switchIntentsFileLock(e.switchIntentsPath())
}

type crossVenueSwitchIntent struct {
	Symbol             string    `json:"symbol"`
	FromHighVenue      string    `json:"from_high_venue,omitempty"`
	FromLowVenue       string    `json:"from_low_venue,omitempty"`
	TargetHighVenue    string    `json:"target_high_venue,omitempty"`
	TargetLowVenue     string    `json:"target_low_venue,omitempty"`
	DecisionCycleID    string    `json:"decision_cycle_id,omitempty"`
	SnapshotID         string    `json:"snapshot_id,omitempty"`
	SwitchSequence     string    `json:"switch_sequence,omitempty"`
	PartialSwitchPhase string    `json:"partial_switch_phase,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	ExpectedGainUSDT   float64   `json:"expected_gain_usdt,omitempty"`
	TargetNetEV        float64   `json:"target_net_ev,omitempty"`
	TargetRankScore    float64   `json:"target_rank_score,omitempty"`
	TargetNotionalUSDT float64   `json:"target_notional_usdt,omitempty"`
	TargetSizeMult     float64   `json:"target_size_multiplier,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at,omitempty"`
}

func (e *CrossVenueExecutor) switchIntentsPath() string {
	return filepath.Join(crossVenueRecordDir(e.recordDir), crossVenueSwitchIntentsFileName)
}

func (e *CrossVenueExecutor) loadSwitchIntentsUnlocked() ([]crossVenueSwitchIntent, error) {
	path := e.switchIntentsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var intents []crossVenueSwitchIntent
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &intents); err != nil {
		return nil, err
	}
	return intents, nil
}

func (e *CrossVenueExecutor) loadSwitchIntents() ([]crossVenueSwitchIntent, error) {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()
	return e.loadSwitchIntentsUnlocked()
}

func (e *CrossVenueExecutor) saveSwitchIntentsUnlocked(intents []crossVenueSwitchIntent) error {
	return writeJSONFile(e.switchIntentsPath(), intents)
}

func (e *CrossVenueExecutor) saveSwitchIntents(intents []crossVenueSwitchIntent) error {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()
	return e.saveSwitchIntentsUnlocked(intents)
}

func (e *CrossVenueExecutor) upsertSwitchIntent(intent crossVenueSwitchIntent) error {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()

	intents, err := e.loadSwitchIntentsUnlocked()
	if err != nil {
		return err
	}
	out := make([]crossVenueSwitchIntent, 0, len(intents)+1)
	replaced := false
	for _, existing := range intents {
		if crossVenueSameSwitchIntent(existing, intent) {
			out = append(out, intent)
			replaced = true
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, intent)
	}
	return e.saveSwitchIntentsUnlocked(out)
}

func (e *CrossVenueExecutor) updateSwitchIntentPhase(symbol, targetHighVenue, targetLowVenue, phase string) error {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()

	intents, err := e.loadSwitchIntentsUnlocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range intents {
		if intents[i].Symbol == symbol && intents[i].TargetHighVenue == targetHighVenue && intents[i].TargetLowVenue == targetLowVenue {
			intents[i].PartialSwitchPhase = phase
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return e.saveSwitchIntentsUnlocked(intents)
}

func (e *CrossVenueExecutor) updateSwitchIntentAttempt(symbol, targetHighVenue, targetLowVenue, phase, reason string) error {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()

	intents, err := e.loadSwitchIntentsUnlocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range intents {
		if intents[i].Symbol == symbol && intents[i].TargetHighVenue == targetHighVenue && intents[i].TargetLowVenue == targetLowVenue {
			intents[i].PartialSwitchPhase = phase
			if reason != "" {
				intents[i].Reason = reason
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return e.saveSwitchIntentsUnlocked(intents)
}

func (e *CrossVenueExecutor) switchIntentForOpportunity(opp CrossVenueOpportunity, now time.Time) (crossVenueSwitchIntent, bool) {
	for _, intent := range e.activeSwitchIntents(now) {
		if crossVenueSwitchIntentMatchesOpportunity(intent, opp) {
			return intent, true
		}
	}
	return crossVenueSwitchIntent{}, false
}

func (e *CrossVenueExecutor) switchIntentForTarget(symbol, targetHighVenue, targetLowVenue string, now time.Time) (crossVenueSwitchIntent, bool) {
	for _, intent := range e.activeSwitchIntents(now) {
		if strings.EqualFold(intent.Symbol, symbol) &&
			strings.EqualFold(intent.TargetHighVenue, targetHighVenue) &&
			strings.EqualFold(intent.TargetLowVenue, targetLowVenue) {
			return intent, true
		}
	}
	return crossVenueSwitchIntent{}, false
}

func (e *CrossVenueExecutor) removeSwitchIntent(symbol, targetHighVenue, targetLowVenue string) error {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()

	intents, err := e.loadSwitchIntentsUnlocked()
	if err != nil {
		return err
	}
	out := make([]crossVenueSwitchIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.Symbol == symbol && intent.TargetHighVenue == targetHighVenue && intent.TargetLowVenue == targetLowVenue {
			continue
		}
		out = append(out, intent)
	}
	return e.saveSwitchIntentsUnlocked(out)
}

func (e *CrossVenueExecutor) activeSwitchIntents(now time.Time) []crossVenueSwitchIntent {
	mu := e.switchIntentsLock()
	mu.Lock()
	defer mu.Unlock()

	intents, err := e.loadSwitchIntentsUnlocked()
	if err != nil {
		if e.logger != nil {
			e.logger.Printf("[跨所执行] 读取 switch intents 失败: %v", err)
		}
		return nil
	}
	out := make([]crossVenueSwitchIntent, 0, len(intents))
	changed := false
	for _, intent := range intents {
		if !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.After(now) {
			changed = true
			continue
		}
		out = append(out, intent)
	}
	if changed {
		_ = e.saveSwitchIntentsUnlocked(out)
	}
	return out
}

func crossVenueSameSwitchIntent(a, b crossVenueSwitchIntent) bool {
	return a.Symbol == b.Symbol &&
		a.TargetHighVenue == b.TargetHighVenue &&
		a.TargetLowVenue == b.TargetLowVenue
}

func crossVenueSwitchIntentMatchesOpportunity(intent crossVenueSwitchIntent, opp CrossVenueOpportunity) bool {
	return strings.EqualFold(intent.Symbol, opp.Symbol) &&
		strings.EqualFold(intent.TargetHighVenue, opp.HighVenue) &&
		strings.EqualFold(intent.TargetLowVenue, opp.LowVenue)
}

func prioritizeCrossVenueOpportunitiesForSwitches(opps []CrossVenueOpportunity, intents []crossVenueSwitchIntent) []CrossVenueOpportunity {
	if len(opps) == 0 || len(intents) == 0 {
		return opps
	}
	intents = prioritizeCrossVenueSwitchIntents(intents)
	matched := make([]CrossVenueOpportunity, 0, len(opps))
	remaining := make([]CrossVenueOpportunity, 0, len(opps))
	for _, opp := range opps {
		if crossVenueOpportunityMatchesAnySwitchIntent(opp, intents) {
			matched = append(matched, opp)
		} else {
			remaining = append(remaining, opp)
		}
	}
	return append(matched, remaining...)
}

func prioritizeCrossVenueSwitchIntents(intents []crossVenueSwitchIntent) []crossVenueSwitchIntent {
	out := append([]crossVenueSwitchIntent(nil), intents...)
	sort.SliceStable(out, func(i, j int) bool {
		return crossVenueSwitchIntentPhasePriority(out[i].PartialSwitchPhase) > crossVenueSwitchIntentPhasePriority(out[j].PartialSwitchPhase)
	})
	return out
}

func crossVenueSwitchIntentPhasePriority(phase string) int {
	switch strings.TrimSpace(phase) {
	case "open_new_second_leg":
		return 60
	case "open_new_first_leg":
		return 55
	case "staged_open_new_second_leg":
		return 52
	case "open_new_retry":
		return 50
	case "rollback_new":
		return 45
	case "staged_close_old":
		return 32
	case "open_new":
		return 40
	case "closed_old":
		return 35
	case "close_old":
		return 30
	case "pending":
		return 20
	default:
		return 0
	}
}

func crossVenueSwitchIntentNeedsOpenContinuation(intent crossVenueSwitchIntent) bool {
	switch strings.TrimSpace(intent.PartialSwitchPhase) {
	case "open_new", "open_new_first_leg", "open_new_second_leg", "open_new_retry", "rollback_new":
		return true
	default:
		return false
	}
}

func filterCrossVenueOpportunitiesForSwitchContinuation(opps []CrossVenueOpportunity, intents []crossVenueSwitchIntent) []CrossVenueOpportunity {
	if len(opps) == 0 || len(intents) == 0 {
		return opps
	}
	required := make([]crossVenueSwitchIntent, 0, len(intents))
	for _, intent := range prioritizeCrossVenueSwitchIntents(intents) {
		if crossVenueSwitchIntentNeedsOpenContinuation(intent) {
			required = append(required, intent)
		}
	}
	if len(required) == 0 {
		return opps
	}
	filtered := make([]CrossVenueOpportunity, 0, len(opps))
	for _, opp := range opps {
		if crossVenueOpportunityMatchesAnySwitchIntent(opp, required) {
			filtered = append(filtered, opp)
		}
	}
	return filtered
}

func crossVenueOpportunityMatchesAnySwitchIntent(opp CrossVenueOpportunity, intents []crossVenueSwitchIntent) bool {
	for _, intent := range intents {
		if crossVenueSwitchIntentMatchesOpportunity(intent, opp) {
			return true
		}
	}
	return false
}

func crossVenueSwitchIntentExpiry(now time.Time) time.Time { return now.Add(30 * time.Minute) }
