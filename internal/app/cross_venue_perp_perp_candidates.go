package app

import (
	"path/filepath"
	"sort"
	"strings"
)

// perpPerpCandidateLogTopN P3-1 取消 Top30 后,perp-perp 候选可达数百/轮。为兼顾"排行榜诊断可见性"
// (含有机会但不能开仓的对 + 其 reject 原因)与"jsonl 体量",按 SpreadAPR 降序记录前 N 条;
// 此外任何正 EV / watchlist 即使排在 N 之后也补记。
const perpPerpCandidateLogTopN = 60

// AppendCrossVenuePerpPerpCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendCrossVenuePerpPerpCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity, enrich *UnifiedCandidateLogEnricher) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)

	// 按 SpreadAPR 降序:排行榜展示最有意义的机会(含不可开的,带 reject 原因),而非只剩正 EV 那一条。
	sorted := make([]UnifiedOpportunity, len(opps))
	copy(sorted, opps)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].SpreadAPR != sorted[j].SpreadAPR {
			return sorted[i].SpreadAPR > sorted[j].SpreadAPR
		}
		return sorted[i].ExpectedNetEVUSDT > sorted[j].ExpectedNetEVUSDT
	})

	for idx, opp := range sorted {
		// 前 N 条全记(含不可开+原因);N 之后只补记正 EV/watchlist,避免 jsonl 暴涨。
		if idx >= perpPerpCandidateLogTopN && !crossVenuePerpPerpCandidateAlwaysLog(opp) {
			continue
		}
		notional := rotationMaxLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunityWithRotation(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional, DefaultRotationConfig(), enrichPerpPerpCandidateEnricher(opp, enrich))
		_ = appendJSONLRecord(path, rec)
	}
}

// crossVenuePerpPerpCandidateAlwaysLog 排在 TopN 之外仍必记的对:正 EV 或观察名单(极端币不漏)。
func crossVenuePerpPerpCandidateAlwaysLog(opp UnifiedOpportunity) bool {
	if opp.ExpectedNetEVUSDT > 0 {
		return true
	}
	return crossVenuePerpPerpDiagWatched(opp.Symbol)
}

func enrichPerpPerpCandidateEnricher(opp UnifiedOpportunity, base *UnifiedCandidateLogEnricher) *UnifiedCandidateLogEnricher {
	if base == nil {
		return nil
	}
	out := *base
	if out.CrossVenueOpp == nil && out.CrossVenueOppsByKey != nil {
		if key, ok := crossVenueOppKeyFromUnified(opp); ok {
			if cv, found := out.CrossVenueOppsByKey[key]; found {
				cvCopy := cv
				out.CrossVenueOpp = &cvCopy
			}
		}
	}
	return &out
}
