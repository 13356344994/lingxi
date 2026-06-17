package app

import (
	"path/filepath"
	"strings"
)

const unifiedCandidatesLogFileName = "unified_candidates.jsonl"

func opportunitiesRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "opportunities")
}

func (e *CrossVenueExecutor) unifiedCandidatesLogPath() string {
	base := "records"
	if e != nil && strings.TrimSpace(e.recordDir) != "" {
		base = e.recordDir
	}
	return filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
}

// UnifiedCandidateLogRecord 是候选级诊断日志的单行 JSONL 格式(shadow-only)。
type UnifiedCandidateLogRecord struct {
	CycleID                string             `json:"cycle_id"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	Venues                 []string           `json:"venues"`
	ExpectedNetEVUSDT      float64            `json:"expected_net_ev_usdt"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue"`
	CapitalEfficiencyScore float64            `json:"capital_efficiency_score"`
	RejectReasons          []string           `json:"reject_reasons"`
	WouldExecute           bool               `json:"would_execute"`
	NotionalUSDT           float64            `json:"notional_usdt"`
}

func unifiedCandidateLogFromOpportunity(cfg CrossVenueExecutorConfig, cycleID string, unified UnifiedOpportunity, wouldExecute bool, notionalUSDT float64) UnifiedCandidateLogRecord {
	venues := make([]string, 0, len(unified.Legs))
	for _, leg := range unified.Legs {
		if leg.Venue != "" {
			venues = append(venues, leg.Venue)
		}
	}
	if len(venues) == 0 {
		venues = nil
	}
	return UnifiedCandidateLogRecord{
		CycleID:                cycleID,
		StrategyType:           unified.StrategyType,
		Symbol:                 unified.Symbol,
		Venues:                 venues,
		ExpectedNetEVUSDT:      unified.ExpectedNetEVUSDT,
		CapitalRequiredByVenue: unified.CapitalRequiredByVenue,
		CapitalEfficiencyScore: unified.CapitalEfficiencyScore,
		RejectReasons:          unified.RejectReasons,
		WouldExecute:           wouldExecute,
		NotionalUSDT:           notionalUSDT,
	}
}

type crossVenueUnifiedCandidateTrace struct {
	Opp            CrossVenueOpportunity
	Gate           crossVenueGateResult
	RejectReasons  []string
	WouldExecute   bool
	NotionalUSDT   float64
	HasGateNotional bool
}

func crossVenueUnifiedCandidateKey(opp CrossVenueOpportunity) string {
	return opp.Symbol + "|" + opp.HighVenue + "|" + opp.LowVenue
}

// appendUnifiedCandidateLogs best-effort 写入本轮所有候选诊断(shadow);写盘失败吞掉,不影响主循环。
func (e *CrossVenueExecutor) appendUnifiedCandidateLogs(cycleID string, cfg CrossVenueExecutorConfig, traces []crossVenueUnifiedCandidateTrace) {
	if e == nil || strings.TrimSpace(cycleID) == "" || len(traces) == 0 {
		return
	}
	path := e.unifiedCandidatesLogPath()
	for _, trace := range traces {
		notional := trace.NotionalUSDT
		refPrice := 0.0
		if trace.HasGateNotional {
			refPrice = (trace.Gate.HighMid + trace.Gate.LowMid) / 2
			if notional <= 0 {
				notional = trace.Gate.NotionalUSDT
			}
		}
		if notional <= 0 {
			notional = trace.Opp.TradableNotional
		}
		unified := UnifiedOpportunityFromCrossVenue(cfg, trace.Opp, trace.RejectReasons, notional, refPrice)
		rec := unifiedCandidateLogFromOpportunity(cfg, cycleID, unified, trace.WouldExecute, notional)
		_ = appendJSONLRecord(path, rec)
	}
}
