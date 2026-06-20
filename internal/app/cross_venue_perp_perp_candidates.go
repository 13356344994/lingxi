package app

import (
	"path/filepath"
	"strings"
)

// AppendCrossVenuePerpPerpCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendCrossVenuePerpPerpCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		// 体量控制(P3-1 取消 Top30 后,全量 perp-perp 可达数百/轮):仅记录有意义的行——
		// 可开(无 reject) / netEV>0 / watchlist 观察币;其余深度为负的对不落盘,避免 jsonl 暴涨。
		// 注意:决策池(rotationCandidates)仍吃全量,此过滤只作用于诊断日志。
		if !crossVenuePerpPerpCandidateLoggable(opp) {
			continue
		}
		notional := rotationMaxLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

// crossVenuePerpPerpCandidateLoggable 判定一条 perp-perp 候选是否值得写入 unified_candidates.jsonl。
func crossVenuePerpPerpCandidateLoggable(opp UnifiedOpportunity) bool {
	if len(opp.RejectReasons) == 0 {
		return true
	}
	if opp.ExpectedNetEVUSDT > 0 {
		return true
	}
	return crossVenuePerpPerpDiagWatched(opp.Symbol)
}
