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

const rotationOpenCooldownFileName = "rotation_open_cooldown.json"

// rotationOpenCooldownStore 记录开仓失败后的 (strategy,symbol,venue-pair) 冷却(内存+可选落盘)。
type rotationOpenCooldownStore struct {
	mu         sync.Mutex
	recordDir  string
	cooldown   time.Duration
	expiresAt  map[string]time.Time
	loaded     bool
}

func newRotationOpenCooldownStore(recordDir string, cooldown time.Duration) *rotationOpenCooldownStore {
	if cooldown <= 0 {
		cooldown = 15 * time.Minute
	}
	return &rotationOpenCooldownStore{
		recordDir: strings.TrimSpace(recordDir),
		cooldown:  cooldown,
		expiresAt: make(map[string]time.Time),
	}
}

func rotationOpportunityVenuePair(opp UnifiedOpportunity) string {
	venues := make([]string, 0, len(opp.Legs))
	seen := map[string]struct{}{}
	for _, leg := range opp.Legs {
		v := strings.TrimSpace(leg.Venue)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		venues = append(venues, v)
	}
	// 合并 CapitalRequiredByVenue 中的所(opp.Legs 可能只登记单腿,跨所机会的另一所只在资金需求里)。
	for v := range opp.CapitalRequiredByVenue {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		venues = append(venues, v)
	}
	sort.Strings(venues)
	return strings.Join(venues, "+")
}

func rotationOpenCooldownKey(strategyType, symbol, venuePair string) string {
	return strings.TrimSpace(strategyType) + "|" + strings.TrimSpace(symbol) + "|" + strings.TrimSpace(venuePair)
}

func (s *rotationOpenCooldownStore) ensureLoaded() {
	if s == nil || s.loaded {
		return
	}
	s.loaded = true
	if s.recordDir == "" {
		return
	}
	path := filepath.Join(s.recordDir, rotationOpenCooldownFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var onDisk struct {
		Entries map[string]time.Time `json:"entries"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	now := time.Now().UTC()
	for k, exp := range onDisk.Entries {
		if exp.After(now) {
			s.expiresAt[k] = exp.UTC()
		}
	}
}

func (s *rotationOpenCooldownStore) persistLocked() {
	if s == nil || s.recordDir == "" {
		return
	}
	path := filepath.Join(s.recordDir, rotationOpenCooldownFileName)
	payload := struct {
		Entries map[string]time.Time `json:"entries"`
	}{Entries: s.expiresAt}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = os.MkdirAll(s.recordDir, 0o755)
	_ = os.WriteFile(path, data, 0o644)
}

func (s *rotationOpenCooldownStore) pruneLocked(now time.Time) {
	for k, exp := range s.expiresAt {
		if !exp.After(now) {
			delete(s.expiresAt, k)
		}
	}
}

func (s *rotationOpenCooldownStore) RecordFailure(strategyType, symbol string, venuePair string, now time.Time) {
	if s == nil {
		return
	}
	key := rotationOpenCooldownKey(strategyType, symbol, venuePair)
	if key == "||" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	s.pruneLocked(now)
	s.expiresAt[key] = now.Add(s.cooldown)
	s.persistLocked()
}

func (s *rotationOpenCooldownStore) RecordFailureOpportunity(opp UnifiedOpportunity, now time.Time) {
	s.RecordFailure(opp.StrategyType, opp.Symbol, rotationOpportunityVenuePair(opp), now)
}

func (s *rotationOpenCooldownStore) IsBlocked(strategyType, symbol, venuePair string, now time.Time) bool {
	if s == nil {
		return false
	}
	key := rotationOpenCooldownKey(strategyType, symbol, venuePair)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	s.pruneLocked(now)
	exp, ok := s.expiresAt[key]
	return ok && exp.After(now)
}

func (s *rotationOpenCooldownStore) blockedPredicate(nowFn func() time.Time) func(opp UnifiedOpportunity) bool {
	if s == nil {
		return nil
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	return func(opp UnifiedOpportunity) bool {
		return s.IsBlocked(opp.StrategyType, opp.Symbol, rotationOpportunityVenuePair(opp), nowFn().UTC())
	}
}
