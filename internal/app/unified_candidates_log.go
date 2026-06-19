package app

import (
	"path/filepath"
	"strings"
	"time"
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

// UnifiedCandidateLogRecord 是候选级诊断日志的单行 JSONL 格式(plan-only)。
type UnifiedCandidateLogRecord struct {
	CycleID                string             `json:"cycle_id"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	Venues                 []string           `json:"venues"`
	ExpectedNetEVUSDT      float64            `json:"expected_net_ev_usdt"`
	ExpectedFundingUSDT    float64            `json:"expected_funding_usdt,omitempty"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue"`
	CapitalEfficiencyScore float64            `json:"capital_efficiency_score"`
	RejectReasons          []string           `json:"reject_reasons"`
	WouldExecute           bool               `json:"would_execute"`
	NotionalUSDT           float64            `json:"notional_usdt"`
	RawFundingRate         float64            `json:"raw_funding_rate,omitempty"`
	FundingRatePerHour     float64            `json:"funding_rate_per_hour,omitempty"`
	FundingAPR             float64            `json:"funding_apr,omitempty"`
	SpreadAPR              float64            `json:"spread_apr,omitempty"`
	HighFundingRate        float64            `json:"high_funding_rate,omitempty"`
	LowFundingRate         float64            `json:"low_funding_rate,omitempty"`
	FundingIntervalHours   int                `json:"funding_interval_hours,omitempty"`
	NextFundingTime        string             `json:"next_funding_time,omitempty"`
	HighNextFundingTime    string             `json:"high_next_funding_time,omitempty"`
	LowNextFundingTime     string             `json:"low_next_funding_time,omitempty"`
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
	_ = cfg
	return UnifiedCandidateLogRecord{
		CycleID:                cycleID,
		StrategyType:           unified.StrategyType,
		Symbol:                 unified.Symbol,
		Venues:                 venues,
		ExpectedNetEVUSDT:      unified.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:    unified.ExpectedFundingUSDT,
		CapitalRequiredByVenue: unified.CapitalRequiredByVenue,
		CapitalEfficiencyScore: unified.CapitalEfficiencyScore,
		RejectReasons:          unified.RejectReasons,
		WouldExecute:           wouldExecute,
		NotionalUSDT:           notionalUSDT,
		RawFundingRate:         unified.RawFundingRate,
		FundingRatePerHour:     unified.FundingRatePerHour,
		FundingAPR:             unified.FundingAPR,
		SpreadAPR:              unified.SpreadAPR,
		HighFundingRate:        unified.HighFundingRate,
		LowFundingRate:         unified.LowFundingRate,
		FundingIntervalHours:   unified.FundingIntervalHours,
		NextFundingTime:        unified.NextFundingTime,
		HighNextFundingTime:    unified.HighNextFundingTime,
		LowNextFundingTime:     unified.LowNextFundingTime,
	}
}

type crossVenueUnifiedCandidateTrace struct {
	Opp             CrossVenueOpportunity
	Gate            crossVenueGateResult
	RejectReasons   []string
	WouldExecute    bool
	NotionalUSDT    float64
	HasGateNotional bool
}

func crossVenueUnifiedCandidateKey(opp CrossVenueOpportunity) string {
	return opp.Symbol + "|" + opp.HighVenue + "|" + opp.LowVenue
}

// appendUnifiedCandidateLogs best-effort 写入本轮所有候选诊断(plan);写盘失败吞掉,不影响主循环。
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

func unifiedRejectedOpportunityWithFunding(
	strategyType, snapshotID, symbol, highVenue, lowVenue, reason string,
	fund CrossVenueSpotPerpFundingSnap,
	now time.Time,
	displayRate float64,
) UnifiedOpportunity {
	opp := UnifiedOpportunity{
		ID:               unifiedOpportunityID(strategyType, symbol, snapshotID, highVenue, lowVenue),
		StrategyType:     strategyType,
		Symbol:           symbol,
		RejectReasons:    []string{reason},
		RiskScore:        1,
		DataQualityScore: 0,
	}
	if highVenue != "" || lowVenue != "" {
		legs := make([]UnifiedOpportunityLeg, 0, 2)
		if highVenue != "" {
			legs = append(legs, UnifiedOpportunityLeg{Venue: highVenue})
		}
		if lowVenue != "" && lowVenue != highVenue {
			legs = append(legs, UnifiedOpportunityLeg{Venue: lowVenue})
		}
		opp.Legs = legs
	}
	applyUnifiedFundingSnapDisplay(&opp, fund, now, displayRate)
	return opp
}

func unifiedMinutesUntilFunding(nextFundingRFC3339 string, now time.Time) float64 {
	nextFundingRFC3339 = strings.TrimSpace(nextFundingRFC3339)
	if nextFundingRFC3339 == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, nextFundingRFC3339)
	if err != nil {
		return 0
	}
	mins := t.Sub(now).Minutes()
	if mins < 0 {
		return 0
	}
	return mins
}

func unifiedLandscapeSortKey(rec UnifiedCandidateLogRecord) float64 {
	if rec.SpreadAPR > 0 {
		return rec.SpreadAPR
	}
	if rec.FundingAPR > 0 {
		return rec.FundingAPR
	}
	if rec.RawFundingRate > 0 && rec.FundingIntervalHours > 0 {
		return fundingAnnualizedAPR(rec.RawFundingRate, rec.FundingIntervalHours)
	}
	if rec.FundingRatePerHour > 0 {
		return rec.FundingRatePerHour * 24 * 365
	}
	return 0
}
