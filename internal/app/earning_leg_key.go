package app

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const earningLegDiagLogFileName = "earning_leg_diag.jsonl"

// EarningLegKey 标识一条「赚资金费」的 perp 腿: symbol + venue + 费率方向(pos/neg)。
type EarningLegKey string

func formatEarningLegKey(symbol, venue, direction string) EarningLegKey {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	venue = strings.TrimSpace(venue)
	direction = strings.TrimSpace(direction)
	return EarningLegKey(fmt.Sprintf("%s|%s|%s", symbol, venue, direction))
}

// EarningLegKeyInfo 单候选的 earning leg 解析结果。
type EarningLegKeyInfo struct {
	PrimaryEarningLegKey   EarningLegKey   `json:"primary_earning_leg_key"`
	OccupiedEarningLegKeys []EarningLegKey `json:"occupied_earning_leg_keys"`
}

// EarningLegCandidateEntry 冲突组内的一条候选明细(只读诊断)。
type EarningLegCandidateEntry struct {
	OpportunityID     string          `json:"opportunity_id"`
	StrategyType      string          `json:"strategy_type"`
	Symbol            string          `json:"symbol"`
	ExpectedNetEVUSDT float64         `json:"expected_net_ev_usdt"`
	PrimaryKey        EarningLegKey   `json:"primary_key"`
	OccupiedKeys      []EarningLegKey `json:"occupied_keys"`
}

// EarningLegGroup 同一 earning leg key 下的候选分组。
type EarningLegGroup struct {
	Key        EarningLegKey              `json:"key"`
	Symbol     string                     `json:"symbol"`
	Venue      string                     `json:"venue"`
	Direction  string                     `json:"direction"`
	Conflict   bool                       `json:"conflict"`
	Candidates []EarningLegCandidateEntry `json:"candidates"`
}

// EarningLegDiagSummary 汇总统计。
type EarningLegDiagSummary struct {
	RawCandidateCount        int `json:"raw_candidate_count"`
	UniqueKeyCount           int `json:"unique_key_count"`
	ConflictKeyCount         int `json:"conflict_key_count"`
	DuplicatedCandidateCount int `json:"duplicated_candidate_count"`
}

// EarningLegDiag 单轮 earning leg 冲突诊断记录(jsonl 一行)。
type EarningLegDiag struct {
	Source     string                `json:"source"`
	CycleID    string                `json:"cycle_id,omitempty"`
	SnapshotID string                `json:"snapshot_id,omitempty"`
	RecordedAt string                `json:"recorded_at"`
	Summary    EarningLegDiagSummary `json:"summary"`
	Groups     []EarningLegGroup     `json:"groups"`
}

func earningLegDirectionFromPerpSide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "long":
		return "neg"
	default:
		// short 及未知侧按正费率空永续口径。
		return "pos"
	}
}

func parseEarningLegKey(key EarningLegKey) (symbol, venue, direction string) {
	parts := strings.SplitN(string(key), "|", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func earningLegKeysForOpportunity(opp UnifiedOpportunity) EarningLegKeyInfo {
	if opp.StrategyType == UnifiedStrategyPerpPerp {
		return earningLegKeysForPerpPerp(opp)
	}
	return earningLegKeysForCarry(opp)
}

func earningLegKeysForCarry(opp UnifiedOpportunity) EarningLegKeyInfo {
	for _, leg := range opp.Legs {
		if strings.ToLower(strings.TrimSpace(leg.Market)) != "perp" {
			continue
		}
		dir := earningLegDirectionFromPerpSide(leg.Side)
		key := formatEarningLegKey(opp.Symbol, leg.Venue, dir)
		return EarningLegKeyInfo{
			PrimaryEarningLegKey:   key,
			OccupiedEarningLegKeys: []EarningLegKey{key},
		}
	}
	return EarningLegKeyInfo{}
}

func earningLegKeysForPerpPerp(opp UnifiedOpportunity) EarningLegKeyInfo {
	type keyedRate struct {
		key  EarningLegKey
		abs  float64
		rate float64
	}
	var earning []keyedRate

	highVenue, lowVenue := "", ""
	highRate, lowRate := opp.HighFundingRate, opp.LowFundingRate
	for _, leg := range opp.Legs {
		if strings.ToLower(strings.TrimSpace(leg.Market)) != "perp" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(leg.Side)) {
		case "short":
			highVenue = leg.Venue
		case "long":
			lowVenue = leg.Venue
		}
	}
	if highVenue != "" && highRate > 0 {
		earning = append(earning, keyedRate{
			key:  formatEarningLegKey(opp.Symbol, highVenue, "pos"),
			abs:  math.Abs(highRate),
			rate: highRate,
		})
	}
	if lowVenue != "" && lowRate < 0 {
		earning = append(earning, keyedRate{
			key:  formatEarningLegKey(opp.Symbol, lowVenue, "neg"),
			abs:  math.Abs(lowRate),
			rate: lowRate,
		})
	}
	if len(earning) == 0 {
		return EarningLegKeyInfo{}
	}
	sort.Slice(earning, func(i, j int) bool {
		if earning[i].abs == earning[j].abs {
			return earning[i].key < earning[j].key
		}
		return earning[i].abs > earning[j].abs
	})
	occupied := make([]EarningLegKey, 0, len(earning))
	for _, item := range earning {
		occupied = append(occupied, item.key)
	}
	return EarningLegKeyInfo{
		PrimaryEarningLegKey:   earning[0].key,
		OccupiedEarningLegKeys: occupied,
	}
}

// diagnoseEarningLegConflicts 对候选切片做只读诊断:按 earning leg key 分组并统计冲突。
func diagnoseEarningLegConflicts(candidates []UnifiedOpportunity) (EarningLegDiagSummary, []EarningLegGroup) {
	type groupAcc struct {
		group EarningLegGroup
	}
	byKey := map[EarningLegKey]*groupAcc{}

	for _, opp := range candidates {
		info := earningLegKeysForOpportunity(opp)
		if info.PrimaryEarningLegKey == "" || len(info.OccupiedEarningLegKeys) == 0 {
			continue
		}
		entry := EarningLegCandidateEntry{
			OpportunityID:     opp.ID,
			StrategyType:      opp.StrategyType,
			Symbol:            opp.Symbol,
			ExpectedNetEVUSDT: opp.ExpectedNetEVUSDT,
			PrimaryKey:        info.PrimaryEarningLegKey,
			OccupiedKeys:      append([]EarningLegKey(nil), info.OccupiedEarningLegKeys...),
		}
		for _, key := range info.OccupiedEarningLegKeys {
			acc, ok := byKey[key]
			if !ok {
				symbol, venue, direction := parseEarningLegKey(key)
				acc = &groupAcc{group: EarningLegGroup{
					Key:       key,
					Symbol:    symbol,
					Venue:     venue,
					Direction: direction,
				}}
				byKey[key] = acc
			}
			acc.group.Candidates = append(acc.group.Candidates, entry)
		}
	}

	groups := make([]EarningLegGroup, 0, len(byKey))
	conflictKeys := 0
	duplicated := 0
	for _, acc := range byKey {
		group := acc.group
		if len(group.Candidates) > 1 {
			group.Conflict = true
			conflictKeys++
			duplicated += len(group.Candidates) - 1
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Key < groups[j].Key
	})

	summary := EarningLegDiagSummary{
		RawCandidateCount:        len(candidates),
		UniqueKeyCount:           len(groups),
		ConflictKeyCount:         conflictKeys,
		DuplicatedCandidateCount: duplicated,
	}
	return summary, groups
}

func appendEarningLegDiag(recordDir string, diag EarningLegDiag) {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return
	}
	if strings.TrimSpace(diag.RecordedAt) == "" {
		diag.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	path := filepath.Join(opportunitiesRecordDir(recordDir), earningLegDiagLogFileName)
	_ = appendJSONLRecord(path, diag)
}

// recordEarningLegDiag 对候选做只读诊断并 best-effort 写入 earning_leg_diag.jsonl。
func recordEarningLegDiag(recordDir, source, snapshotID, cycleID string, candidates []UnifiedOpportunity) EarningLegDiagSummary {
	summary, groups := diagnoseEarningLegConflicts(candidates)
	if strings.TrimSpace(recordDir) != "" {
		appendEarningLegDiag(recordDir, EarningLegDiag{
			Source:     source,
			CycleID:    cycleID,
			SnapshotID: snapshotID,
			Summary:    summary,
			Groups:     groups,
		})
	}
	return summary
}
