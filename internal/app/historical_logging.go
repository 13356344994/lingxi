package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的「日志与跳过原因聚合」内核
// —— 分组跳过摘要、主因判定、候选/分组/成交日志记录构造、最优摘要候选挑选，以及月度模拟日志
// 落盘与归档（同包搬移，行为不变），让主文件收敛到「编排 + 模拟状态机」职责并向单文件 ≤ 800 行
// 门禁靠拢。

func summarizeMonthlyGroupSkip(group monthlyEventGroup, evaluated []monthlyCandidate, groupReason string, groupTimeout time.Duration) monthlyCandidate {
	summary := monthlyCandidate{
		Reason: "no_viable_candidate",
		Notes:  []string{"本轮所有候选经过收益与流动性校验后都未通过"},
	}
	if strings.TrimSpace(groupReason) != "" {
		summary.Reason = groupReason
		summary.Notes = []string{
			fmt.Sprintf("groupTimeout=%s", groupTimeout),
			fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
			fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		}
	}
	if len(evaluated) == 0 {
		return summary
	}
	reasonCounts := make(map[string]int)
	for _, candidate := range evaluated {
		reason := strings.TrimSpace(candidate.Reason)
		if reason == "" {
			continue
		}
		reasonCounts[reason]++
	}
	if len(reasonCounts) == 0 {
		return summary
	}
	primaryReason := dominantMonthlyGroupSkipReason(reasonCounts)
	summary.Reason = nonEmpty(primaryReason, summary.Reason)
	notes := []string{
		fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		"reasonBreakdown=" + summarizeMonthlyReasonCounts(reasonCounts, nil),
	}
	if focus := firstMonthlyCandidateByReason(evaluated, primaryReason); focus != nil {
		if focus.Symbol != "" {
			notes = append(notes, "sampleSymbol="+focus.Symbol)
		}
		notes = append(notes, firstMatchingMonthlyNotes(focus.Notes,
			"safetyBufferUSDT=",
			"safetyBufferGap=",
			"estimatedNetPnL=",
			"entryExecutionCost=",
			"spotLiquidityShare=",
			"futuresLiquidityShare=",
			"premiumIndexClose=",
			"markIndexBasis=",
		)...)
	}
	if strings.TrimSpace(groupReason) != "" && groupReason != summary.Reason {
		notes = append(notes, "groupReason="+groupReason)
	}
	summary.Notes = notes
	return summary
}

func dominantMonthlyGroupSkipReason(reasonCounts map[string]int) string {
	if len(reasonCounts) == 0 {
		return ""
	}
	bestReason := ""
	bestCount := -1
	for reason, count := range reasonCounts {
		if reason == "screening_prefilter_excluded" && len(reasonCounts) > 1 {
			continue
		}
		if count > bestCount || (count == bestCount && reason < bestReason) {
			bestReason = reason
			bestCount = count
		}
	}
	if strings.TrimSpace(bestReason) != "" {
		return bestReason
	}
	for reason, count := range reasonCounts {
		if count > bestCount || (count == bestCount && reason < bestReason) {
			bestReason = reason
			bestCount = count
		}
	}
	return bestReason
}

func firstMonthlyCandidateByReason(candidates []monthlyCandidate, reason string) *monthlyCandidate {
	for index := range candidates {
		if candidates[index].Reason == reason {
			return &candidates[index]
		}
	}
	return nil
}

func firstMatchingMonthlyNotes(notes []string, prefixes ...string) []string {
	if len(notes) == 0 || len(prefixes) == 0 {
		return nil
	}
	matches := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		for _, note := range notes {
			if strings.HasPrefix(note, prefix) {
				matches = append(matches, note)
				break
			}
		}
	}
	return matches
}

func newMonthlyLogAggregation() *monthlyLogAggregation {
	return &monthlyLogAggregation{
		counts:  make(map[string]int),
		samples: make(map[string][]string),
	}
}

func (a *monthlyLogAggregation) add(reason string, symbol string) {
	if a == nil || strings.TrimSpace(reason) == "" {
		return
	}
	a.counts[reason]++
	if strings.TrimSpace(symbol) == "" {
		return
	}
	if len(a.samples[reason]) >= 5 {
		return
	}
	a.samples[reason] = append(a.samples[reason], symbol)
}

func (r *BacktestRunner) logMonthlyGroupAggregation(group monthlyEventGroup, screeningCount int, agg *monthlyLogAggregation, groupReason string) {
	if r.logger == nil {
		return
	}
	parts := make([]string, 0, 4)
	parts = append(parts,
		fmt.Sprintf("原始候选=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("保留候选=%d", len(group.Items)),
		fmt.Sprintf("预筛后=%d", screeningCount),
	)
	for _, reason := range []string{"candidate_build_timeout", "candidate_pricing_timeout", "candidate_finalize_timeout"} {
		count := agg.counts[reason]
		if count <= 0 {
			continue
		}
		part := fmt.Sprintf("%s=%d", reason, count)
		if len(agg.samples[reason]) > 0 {
			part += "[" + strings.Join(agg.samples[reason], ",") + "]"
		}
		parts = append(parts, part)
	}
	if strings.TrimSpace(groupReason) != "" {
		parts = append(parts, "groupReason="+groupReason)
	}
	if len(parts) <= 2 && strings.TrimSpace(groupReason) == "" {
		return
	}
	r.logger.Printf("历史模拟评估摘要 fundingTime=%s %s", group.FundingTime.Format(time.RFC3339), strings.Join(parts, " | "))
}

func buildMonthlyTradeLogRecord(candidate *monthlyCandidate, action string, equityBefore float64, equityAfter float64, drawdownRate float64) MonthlySimulationLogRecord {
	return MonthlySimulationLogRecord{
		RecordedAt:              time.Now().UTC().Format(time.RFC3339),
		FundingTime:             candidate.FundingTime.Format(time.RFC3339),
		Action:                  action,
		Symbol:                  candidate.Symbol,
		EstimatedFundingRate:    formatFloat(candidate.EstimatedFundingRate),
		FundingRate:             formatFloat(candidate.FundingRate),
		EstimatedFundingIncome:  formatFloat(candidate.EstimatedFundingIncome),
		FundingIncome:           formatFloat(candidate.FundingIncome),
		HedgeQty:                formatFloat(candidate.HedgeQty),
		SpotEntryPrice:          formatFloat(candidate.SpotEntryPrice),
		FuturesEntryPrice:       formatFloat(candidate.FuturesEntryPrice),
		SpotExitPrice:           formatFloat(candidate.SpotExitPrice),
		FuturesExitPrice:        formatFloat(candidate.FuturesExitPrice),
		OpenBasisRate:           formatFloat(candidate.OpenBasisRate),
		CloseBasisRate:          formatFloat(candidate.CloseBasisRate),
		SpotEntryQuoteVolume:    formatFloat(candidate.SpotEntryQuoteVolume),
		FuturesEntryQuoteVolume: formatFloat(candidate.FuturesEntryQuoteVolume),
		SpotExitQuoteVolume:     formatFloat(candidate.SpotExitQuoteVolume),
		FuturesExitQuoteVolume:  formatFloat(candidate.FuturesExitQuoteVolume),
		SpotEntrySlipRate:       formatFloat(candidate.SpotEntrySlipRate),
		FuturesEntrySlipRate:    formatFloat(candidate.FuturesEntrySlipRate),
		SpotExitSlipRate:        formatFloat(candidate.SpotExitSlipRate),
		FuturesExitSlipRate:     formatFloat(candidate.FuturesExitSlipRate),
		SpotEntryImpactRate:     formatFloat(candidate.SpotEntryImpactRate),
		FuturesEntryImpactRate:  formatFloat(candidate.FuturesEntryImpactRate),
		SpotExitImpactRate:      formatFloat(candidate.SpotExitImpactRate),
		FuturesExitImpactRate:   formatFloat(candidate.FuturesExitImpactRate),
		SpotEntryFee:            formatFloat(candidate.SpotEntryFee),
		FuturesEntryFee:         formatFloat(candidate.FuturesEntryFee),
		SpotExitFee:             formatFloat(candidate.SpotExitFee),
		FuturesExitFee:          formatFloat(candidate.FuturesExitFee),
		OpenBasisPenalty:        formatFloat(candidate.OpenBasisPenalty),
		ProjectedExitCost:       formatFloat(candidate.ProjectedExitCost),
		MinLiquidityRequired:    formatFloat(candidate.MinLiquidityRequired),
		SpotGrossPnL:            formatFloat(candidate.SpotGrossPnL),
		FuturesGrossPnL:         formatFloat(candidate.FuturesGrossPnL),
		TotalFees:               formatFloat(candidate.TotalFees),
		TotalFriction:           formatFloat(candidate.TotalFriction),
		EstimatedNetPnL:         formatFloat(candidate.EstimatedNetPnL),
		RealizedNetPnL:          formatFloat(candidate.RealizedNetPnL),
		EquityBefore:            formatFloat(equityBefore),
		EquityAfter:             formatFloat(equityAfter),
		DrawdownRate:            formatFloat(drawdownRate),
		Reason:                  candidate.Reason,
		Notes:                   strings.Join(candidate.Notes, " | "),
	}
}

func buildMonthlyGroupSummaryLogRecord(group monthlyEventGroup, evaluated []monthlyCandidate, selected *monthlyCandidate, equity float64, drawdownRate float64, groupReason string) MonthlySimulationLogRecord {
	focus := selected
	if focus == nil {
		focus = bestMonthlyCandidateForSummary(evaluated)
	}
	reasonCounts := make(map[string]int)
	viableCount := 0
	for index := range evaluated {
		reason := strings.TrimSpace(evaluated[index].Reason)
		if reason == "" {
			viableCount++
			continue
		}
		reasonCounts[reason]++
	}
	summaryReason := "group_without_trade"
	if selected != nil {
		summaryReason = "selected_for_trade"
	} else if strings.TrimSpace(groupReason) != "" {
		summaryReason = groupReason
	} else if dominant := dominantMonthlyGroupSkipReason(reasonCounts); strings.TrimSpace(dominant) != "" {
		summaryReason = dominant
	}
	notes := []string{
		fmt.Sprintf("rawCandidates=%d", maxInt(group.RawCount, len(group.Items))),
		fmt.Sprintf("retainedCandidates=%d", len(group.Items)),
		fmt.Sprintf("evaluated=%d", len(evaluated)),
		fmt.Sprintf("viableCandidates=%d", viableCount),
		"reasonBreakdown=" + summarizeMonthlyReasonCounts(reasonCounts, nil),
	}
	if focus != nil {
		if focus.Symbol != "" {
			notes = append(notes, "focusSymbol="+focus.Symbol)
		}
		focusReason := focus.Reason
		if strings.TrimSpace(focusReason) == "" && selected != nil {
			focusReason = "selected_for_trade"
		}
		if strings.TrimSpace(focusReason) != "" {
			notes = append(notes, "focusReason="+focusReason)
		}
		notes = append(notes,
			fmt.Sprintf("focusEstimatedNetPnL=%.6f", focus.EstimatedNetPnL),
			fmt.Sprintf("focusEstimatedFundingIncome=%.6f", focus.EstimatedFundingIncome),
			fmt.Sprintf("focusActualFundingIncome=%.6f", focus.FundingIncome),
		)
		notes = append(notes, firstMatchingMonthlyNotes(focus.Notes,
			"safetyBufferUSDT=",
			"safetyBufferGap=",
			"estimatedNetPnL=",
			"entryExecutionCost=",
			"spotLiquidityShare=",
			"futuresLiquidityShare=",
			"premiumIndexClose=",
			"markIndexBasis=",
			"prefilterRank=",
			"screeningRank=",
		)...)
	}
	if strings.TrimSpace(groupReason) != "" && groupReason != summaryReason {
		notes = append(notes, "groupReason="+groupReason)
	}
	record := MonthlySimulationLogRecord{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		FundingTime:  group.FundingTime.Format(time.RFC3339),
		Action:       "group_summary",
		EquityBefore: formatFloat(equity),
		EquityAfter:  formatFloat(equity),
		DrawdownRate: formatFloat(drawdownRate),
		Reason:       summaryReason,
		Notes:        strings.Join(notes, " | "),
	}
	if focus != nil {
		record.Symbol = focus.Symbol
		record.EstimatedFundingRate = formatFloat(focus.EstimatedFundingRate)
		record.FundingRate = formatFloat(focus.FundingRate)
		record.EstimatedFundingIncome = formatFloat(focus.EstimatedFundingIncome)
		record.FundingIncome = formatFloat(focus.FundingIncome)
		record.HedgeQty = formatFloat(focus.HedgeQty)
		record.SpotEntryPrice = formatFloat(focus.SpotEntryPrice)
		record.FuturesEntryPrice = formatFloat(focus.FuturesEntryPrice)
		record.OpenBasisRate = formatFloat(focus.OpenBasisRate)
		record.SpotEntryQuoteVolume = formatFloat(focus.SpotEntryQuoteVolume)
		record.FuturesEntryQuoteVolume = formatFloat(focus.FuturesEntryQuoteVolume)
		record.TotalFees = formatFloat(focus.TotalFees)
		record.TotalFriction = formatFloat(focus.TotalFriction)
		record.EstimatedNetPnL = formatFloat(focus.EstimatedNetPnL)
		record.RealizedNetPnL = formatFloat(focus.RealizedNetPnL)
	}
	return record
}

func buildMonthlyCandidateLogRecord(candidate monthlyCandidate, action string, equityBefore float64, equityAfter float64, drawdownRate float64, reason string, extraNotes ...string) MonthlySimulationLogRecord {
	notes := append([]string{}, candidate.Notes...)
	notes = append(notes, extraNotes...)
	candidate.Reason = reason
	candidate.Notes = notes
	return buildMonthlyTradeLogRecord(&candidate, action, equityBefore, equityAfter, drawdownRate)
}

func bestMonthlyCandidateForSummary(candidates []monthlyCandidate) *monthlyCandidate {
	if len(candidates) == 0 {
		return nil
	}
	bestIndex := -1
	for index := range candidates {
		if bestIndex == -1 || betterMonthlySummaryCandidate(candidates[index], candidates[bestIndex]) {
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &candidates[bestIndex]
}

func betterMonthlySummaryCandidate(candidate monthlyCandidate, current monthlyCandidate) bool {
	if candidate.EstimatedNetPnL != current.EstimatedNetPnL {
		return candidate.EstimatedNetPnL > current.EstimatedNetPnL
	}
	if math.Abs(candidate.EstimatedFundingRate) != math.Abs(current.EstimatedFundingRate) {
		return math.Abs(candidate.EstimatedFundingRate) > math.Abs(current.EstimatedFundingRate)
	}
	if monthlyReasonPriority(candidate.Reason) != monthlyReasonPriority(current.Reason) {
		return monthlyReasonPriority(candidate.Reason) < monthlyReasonPriority(current.Reason)
	}
	return candidate.Symbol < current.Symbol
}

func monthlyReasonPriority(reason string) int {
	switch strings.TrimSpace(reason) {
	case "":
		return 0
	case "funding_rate_extreme":
		return 1
	case "funding_jump_too_large":
		return 2
	case "funding_direction_reversed":
		return 3
	case "estimated_net_below_safety_buffer":
		return 4
	case "entry_liquidity_share_too_high":
		return 5
	case "entry_liquidity_extremely_poor":
		return 6
	case "estimated_funding_direction_disabled":
		return 7
	case "estimated_funding_non_positive":
		return 8
	case "screening_topn_excluded":
		return 9
	case "screening_prefilter_excluded":
		return 10
	default:
		return 11
	}
}

func writeMonthlySimulationLogs(recordDir string, cfg EngineConfig, logs []MonthlySimulationLogRecord) error {
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return fmt.Errorf("创建记录目录失败: %w", err)
	}
	metadata := buildTraceRecordMetadata(cfg)
	for idx := range logs {
		if strings.TrimSpace(logs[idx].ConfigHash) == "" {
			logs[idx].TraceRecordMetadata = metadata
		}
	}

	path := filepath.Join(recordDir, monthlySimulationLogCSVName)
	if err := writeMonthlySimulationLogFile(path, logs); err != nil {
		return err
	}

	if len(logs) == 0 {
		return nil
	}
	archiveDir := filepath.Join(recordDir, monthlySimulationArchiveDirName)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("创建月度模拟归档目录失败: %w", err)
	}
	archiveName := time.Now().UTC().Format("20060102_150405") + "_" + monthlySimulationLogCSVName
	return writeMonthlySimulationLogFile(filepath.Join(archiveDir, archiveName), logs)
}

func writeMonthlySimulationLogFile(path string, logs []MonthlySimulationLogRecord) error {
	if err := writeJSONFile(path, logs); err != nil {
		return fmt.Errorf("写入月度模拟日志失败: %w", err)
	}
	_, err := compactJSONByRecordedAt(path, time.Now().UTC(), reportCSVRetention, reportCSVMaxFileSizeBytes)
	return err
}
