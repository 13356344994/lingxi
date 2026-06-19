package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UnifiedLeaderboardEntry 是排行榜中单条机会的只读展示结构。
type UnifiedLeaderboardEntry struct {
	Rank                   int                `json:"rank"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	Venues                 []string           `json:"venues"`
	ExpectedNetEVUSDT      float64            `json:"expected_net_ev_usdt"`
	ExpectedFundingUSDT    float64            `json:"expected_funding_usdt,omitempty"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue"`
	CapitalEfficiencyScore float64            `json:"capital_efficiency_score"`
	LandscapeSortKey       float64            `json:"landscape_sort_key"`
	PrimarySortKey         float64            `json:"primary_sort_key"`
	RankReason             string             `json:"rank_reason"`
	WouldExecute           bool               `json:"would_execute"`
	RejectReasons          []string           `json:"reject_reasons"`
	NotionalUSDT           float64            `json:"notional_usdt,omitempty"`
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
	MinutesUntilFunding    float64            `json:"minutes_until_funding,omitempty"`
}

// UnifiedReasonCount 统计 reject_reason 出现次数。
type UnifiedReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// UnifiedVenueCapitalBlock 统计某交易所因资金/保证金不足被挡次数。
type UnifiedVenueCapitalBlock struct {
	Venue string `json:"venue"`
	Count int    `json:"count"`
}

// UnifiedLeaderboardResponse 是 /api/unified/leaderboard 的 JSON 响应。
type UnifiedLeaderboardResponse struct {
	CycleID                  string                     `json:"cycle_id"`
	GeneratedAt              string                     `json:"generated_at"`
	SourcePath               string                     `json:"source_path"`
	TotalCandidates          int                        `json:"total_candidates"`
	WouldExecuteCount        int                        `json:"would_execute_count"`
	Opportunities            []UnifiedLeaderboardEntry  `json:"opportunities"`
	RejectReasonsTop         []UnifiedReasonCount       `json:"reject_reasons_top"`
	VenueCapitalInsufficient []UnifiedVenueCapitalBlock `json:"venue_capital_insufficient_top"`
	Note                     string                     `json:"note,omitempty"`
}

// UnifiedCandidatesLogPath 返回 unified_candidates.jsonl 的绝对/相对路径。
func UnifiedCandidatesLogPath(recordBase string) string {
	return filepath.Join(opportunitiesRecordDir(recordBase), unifiedCandidatesLogFileName)
}

func (e *CrossVenueExecutor) UnifiedCandidatesLogPath() string {
	if e == nil {
		return UnifiedCandidatesLogPath("records")
	}
	return e.unifiedCandidatesLogPath()
}

// readUnifiedCandidatesJSONL 容错读取 JSONL：文件不存在返回空切片；坏行跳过。
func readUnifiedCandidatesJSONL(path string) ([]UnifiedCandidateLogRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("打开 unified candidates 日志失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := make([]UnifiedCandidateLogRecord, 0, 128)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec UnifiedCandidateLogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 unified candidates 日志失败: %w", err)
	}
	return out, nil
}

func latestUnifiedCycleID(records []UnifiedCandidateLogRecord) string {
	latest := ""
	for _, rec := range records {
		cycle := strings.TrimSpace(rec.CycleID)
		if cycle == "" {
			continue
		}
		if latest == "" || cycle > latest {
			latest = cycle
		}
	}
	return latest
}

func filterUnifiedCandidatesByCycle(records []UnifiedCandidateLogRecord, cycleID string) []UnifiedCandidateLogRecord {
	if cycleID == "" {
		return nil
	}
	out := make([]UnifiedCandidateLogRecord, 0, len(records))
	for _, rec := range records {
		if rec.CycleID == cycleID {
			out = append(out, rec)
		}
	}
	return out
}

func dedupeUnifiedCandidatesLatest(records []UnifiedCandidateLogRecord) []UnifiedCandidateLogRecord {
	if len(records) == 0 {
		return records
	}
	lastIdx := make(map[string]int, len(records))
	for i, rec := range records {
		key := rec.StrategyType + "|" + rec.Symbol
		lastIdx[key] = i
	}
	out := make([]UnifiedCandidateLogRecord, 0, len(lastIdx))
	for i, rec := range records {
		key := rec.StrategyType + "|" + rec.Symbol
		if lastIdx[key] != i {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func sumCapitalRequired(m map[string]float64) float64 {
	total := 0.0
	for _, v := range m {
		if v > 0 {
			total += v
		}
	}
	return total
}

// unifiedPrimarySortKey 保留 EV/资本效率排序键(次排序/兼容字段)。
func unifiedPrimarySortKey(rec UnifiedCandidateLogRecord) float64 {
	if rec.CapitalEfficiencyScore != 0 {
		return rec.CapitalEfficiencyScore
	}
	totalCapital := sumCapitalRequired(rec.CapitalRequiredByVenue)
	if totalCapital <= 0 {
		return rec.ExpectedNetEVUSDT
	}
	return rec.ExpectedNetEVUSDT / totalCapital
}

func buildUnifiedRankReason(rank int, rec UnifiedCandidateLogRecord, landscapeKey float64, now time.Time) string {
	venueList := strings.Join(rec.Venues, "+")
	if venueList == "" {
		venueList = "—"
	}
	fundingLabel := "费率 APR"
	fundingVal := rec.FundingAPR
	if rec.SpreadAPR > 0 {
		fundingLabel = "价差 APR"
		fundingVal = rec.SpreadAPR
	}
	mins := unifiedMinutesUntilFunding(rec.NextFundingTime, now)
	settlePart := ""
	if mins > 0 {
		settlePart = fmt.Sprintf("，距结算 %.0f min", mins)
	}
	execLabel := "不可执行"
	if rec.WouldExecute {
		execLabel = "可执行"
	}
	return fmt.Sprintf(
		"排名第 %d（%s）：%s %.2f%%；%s @ %s，预计净EV %.4f USDT，预计资金费 %.4f USDT，名义 %.2f USDT%s",
		rank, execLabel, fundingLabel, fundingVal*100, rec.Symbol, venueList,
		rec.ExpectedNetEVUSDT, rec.ExpectedFundingUSDT, rec.NotionalUSDT, settlePart,
	)
}

func classifyVenueCapitalInsufficient(reason string, venues []string) (venue string, ok bool) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", false
	}
	isCapital := strings.Contains(reason, "永续可用") && strings.Contains(reason, "< 所需")
	isCapital = isCapital || strings.Contains(reason, "保证金读取失败")
	if !isCapital {
		return "", false
	}
	switch {
	case strings.Contains(reason, "高费所") && len(venues) >= 1:
		return venues[0], true
	case strings.Contains(reason, "低费所") && len(venues) >= 2:
		return venues[1], true
	}
	lower := strings.ToLower(reason)
	for _, v := range venues {
		if v != "" && strings.Contains(lower, strings.ToLower(v)) {
			return v, true
		}
	}
	return "unknown", true
}

func buildUnifiedReasonCounts(records []UnifiedCandidateLogRecord, topN int) []UnifiedReasonCount {
	counts := map[string]int{}
	for _, rec := range records {
		if rec.WouldExecute {
			continue
		}
		reasons := rec.RejectReasons
		if len(reasons) == 0 {
			counts["(no reject reason recorded)"]++
			continue
		}
		for _, reason := range reasons {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				continue
			}
			counts[reason]++
		}
	}
	return topUnifiedReasonCounts(counts, topN)
}

func buildVenueCapitalInsufficientTop(records []UnifiedCandidateLogRecord, topN int) []UnifiedVenueCapitalBlock {
	counts := map[string]int{}
	for _, rec := range records {
		for _, reason := range rec.RejectReasons {
			venue, ok := classifyVenueCapitalInsufficient(reason, rec.Venues)
			if !ok {
				continue
			}
			counts[venue]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	type pair struct {
		venue string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for venue, count := range counts {
		pairs = append(pairs, pair{venue: venue, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].venue < pairs[j].venue
	})
	if topN <= 0 {
		topN = 10
	}
	if len(pairs) > topN {
		pairs = pairs[:topN]
	}
	out := make([]UnifiedVenueCapitalBlock, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, UnifiedVenueCapitalBlock{Venue: p.venue, Count: p.count})
	}
	return out
}

func topUnifiedReasonCounts(counts map[string]int, topN int) []UnifiedReasonCount {
	if len(counts) == 0 {
		return nil
	}
	type pair struct {
		reason string
		count  int
	}
	pairs := make([]pair, 0, len(counts))
	for reason, count := range counts {
		pairs = append(pairs, pair{reason: reason, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].reason < pairs[j].reason
	})
	if topN <= 0 {
		topN = 15
	}
	if len(pairs) > topN {
		pairs = pairs[:topN]
	}
	out := make([]UnifiedReasonCount, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, UnifiedReasonCount{Reason: p.reason, Count: p.count})
	}
	return out
}

type unifiedCandidateSortable struct {
	rec           UnifiedCandidateLogRecord
	landscapeKey  float64
	primaryKey    float64
	original      int
}

func leaderboardEntryFromRecord(rec UnifiedCandidateLogRecord, rank int, landscapeKey float64, now time.Time) UnifiedLeaderboardEntry {
	rejectReasons := rec.RejectReasons
	if rejectReasons == nil {
		rejectReasons = []string{}
	}
	capital := rec.CapitalRequiredByVenue
	if capital == nil {
		capital = map[string]float64{}
	}
	venues := rec.Venues
	if venues == nil {
		venues = []string{}
	}
	return UnifiedLeaderboardEntry{
		Rank:                   rank,
		StrategyType:           rec.StrategyType,
		Symbol:                 rec.Symbol,
		Venues:                 venues,
		ExpectedNetEVUSDT:      rec.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:    rec.ExpectedFundingUSDT,
		CapitalRequiredByVenue: capital,
		CapitalEfficiencyScore: rec.CapitalEfficiencyScore,
		LandscapeSortKey:       landscapeKey,
		PrimarySortKey:         unifiedPrimarySortKey(rec),
		RankReason:             buildUnifiedRankReason(rank, rec, landscapeKey, now),
		WouldExecute:           rec.WouldExecute,
		RejectReasons:          rejectReasons,
		NotionalUSDT:           rec.NotionalUSDT,
		RawFundingRate:         rec.RawFundingRate,
		FundingRatePerHour:     rec.FundingRatePerHour,
		FundingAPR:             rec.FundingAPR,
		SpreadAPR:              rec.SpreadAPR,
		HighFundingRate:        rec.HighFundingRate,
		LowFundingRate:         rec.LowFundingRate,
		FundingIntervalHours:   rec.FundingIntervalHours,
		NextFundingTime:        rec.NextFundingTime,
		HighNextFundingTime:    rec.HighNextFundingTime,
		LowNextFundingTime:     rec.LowNextFundingTime,
		MinutesUntilFunding:    unifiedMinutesUntilFunding(rec.NextFundingTime, now),
	}
}

// BuildUnifiedLeaderboard 从全部 JSONL 记录中取最近 cycle_id 的候选并排序。
func BuildUnifiedLeaderboard(records []UnifiedCandidateLogRecord, topN int) UnifiedLeaderboardResponse {
	now := time.Now().UTC()
	resp := UnifiedLeaderboardResponse{
		GeneratedAt: now.Format(time.RFC3339),
	}
	if len(records) == 0 {
		resp.Note = "暂无 unified candidates 记录"
		return resp
	}
	cycleID := latestUnifiedCycleID(records)
	cycleRecords := filterUnifiedCandidatesByCycle(records, cycleID)
	cycleRecords = dedupeUnifiedCandidatesLatest(cycleRecords)
	if len(cycleRecords) == 0 {
		resp.Note = "最近 cycle 无候选记录"
		return resp
	}
	resp.CycleID = cycleID
	resp.TotalCandidates = len(cycleRecords)

	sortable := make([]unifiedCandidateSortable, len(cycleRecords))
	for i, rec := range cycleRecords {
		sortable[i] = unifiedCandidateSortable{
			rec:          rec,
			landscapeKey: unifiedLandscapeSortKey(rec),
			primaryKey:   unifiedPrimarySortKey(rec),
			original:     i,
		}
		if rec.WouldExecute {
			resp.WouldExecuteCount++
		}
	}
	sort.SliceStable(sortable, func(i, j int) bool {
		if sortable[i].rec.WouldExecute != sortable[j].rec.WouldExecute {
			return sortable[i].rec.WouldExecute
		}
		if sortable[i].landscapeKey != sortable[j].landscapeKey {
			return sortable[i].landscapeKey > sortable[j].landscapeKey
		}
		if sortable[i].rec.ExpectedNetEVUSDT != sortable[j].rec.ExpectedNetEVUSDT {
			return sortable[i].rec.ExpectedNetEVUSDT > sortable[j].rec.ExpectedNetEVUSDT
		}
		if sortable[i].rec.Symbol != sortable[j].rec.Symbol {
			return sortable[i].rec.Symbol < sortable[j].rec.Symbol
		}
		return sortable[i].original < sortable[j].original
	})

	if topN <= 0 {
		topN = len(sortable)
	}
	if len(sortable) > topN {
		sortable = sortable[:topN]
	}

	resp.Opportunities = make([]UnifiedLeaderboardEntry, 0, len(sortable))
	for i, item := range sortable {
		resp.Opportunities = append(resp.Opportunities, leaderboardEntryFromRecord(item.rec, i+1, item.landscapeKey, now))
	}
	resp.RejectReasonsTop = buildUnifiedReasonCounts(cycleRecords, 15)
	resp.VenueCapitalInsufficient = buildVenueCapitalInsufficientTop(cycleRecords, 10)
	return resp
}

// LoadUnifiedLeaderboard 从 JSONL 路径加载并构建排行榜。
func LoadUnifiedLeaderboard(path string, topN int) (UnifiedLeaderboardResponse, error) {
	records, err := readUnifiedCandidatesJSONL(path)
	if err != nil {
		return UnifiedLeaderboardResponse{}, err
	}
	resp := BuildUnifiedLeaderboard(records, topN)
	resp.SourcePath = path
	return resp, nil
}
