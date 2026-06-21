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
	Rank                     int                `json:"rank"`
	StrategyType             string             `json:"strategy_type"`
	Symbol                   string             `json:"symbol"`
	Venues                   []string           `json:"venues"`
	ExpectedNetEVUSDT        float64            `json:"expected_net_ev_usdt"`
	ExpectedFundingUSDT      float64            `json:"expected_funding_usdt,omitempty"`
	CapitalAwareNetEVUSDT    float64            `json:"capital_aware_net_ev_usdt,omitempty"`
	CapitalAwareNotionalUSDT float64            `json:"capital_aware_notional_usdt,omitempty"`
	ScannerEstimateOnly      bool               `json:"scanner_estimate_only,omitempty"`
	CapitalRequiredByVenue   map[string]float64 `json:"capital_required_by_venue"`
	CapitalEfficiencyScore   float64            `json:"capital_efficiency_score"`
	LandscapeSortKey         float64            `json:"landscape_sort_key"`
	PrimarySortKey           float64            `json:"primary_sort_key"`
	RankReason               string             `json:"rank_reason"`
	WouldExecute             bool               `json:"would_execute"` // scanner 口径,兼容旧面板
	ScannerWouldExecute      bool               `json:"scanner_would_execute"`
	RotationWouldExecute     bool               `json:"rotation_would_execute"`
	RejectReasons            []string           `json:"reject_reasons"`
	NotionalUSDT             float64            `json:"notional_usdt,omitempty"`
	RawFundingRate           float64            `json:"raw_funding_rate,omitempty"`
	FundingRatePerHour       float64            `json:"funding_rate_per_hour,omitempty"`
	FundingAPR               float64            `json:"funding_apr,omitempty"`
	SpreadAPR                float64            `json:"spread_apr,omitempty"`
	HighFundingRate          float64            `json:"high_funding_rate,omitempty"`
	LowFundingRate           float64            `json:"low_funding_rate,omitempty"`
	FundingIntervalHours     int                `json:"funding_interval_hours,omitempty"`
	NextFundingTime          string             `json:"next_funding_time,omitempty"`
	HighNextFundingTime      string             `json:"high_next_funding_time,omitempty"`
	LowNextFundingTime       string             `json:"low_next_funding_time,omitempty"`
	MinutesUntilFunding      float64            `json:"minutes_until_funding,omitempty"`
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

// UnifiedStrategyLeaderboard 是单个策略的独立排行(总榜下的分组视图)。
type UnifiedStrategyLeaderboard struct {
	StrategyType              string                    `json:"strategy_type"`
	TotalCandidates           int                       `json:"total_candidates"`
	DisplayedCandidates       int                       `json:"displayed_candidates"`
	WouldExecuteCount         int                       `json:"would_execute_count"`
	ScannerWouldExecuteCount  int                       `json:"scanner_would_execute_count"`
	RotationWouldExecuteCount int                       `json:"rotation_would_execute_count"`
	Opportunities             []UnifiedLeaderboardEntry `json:"opportunities"`
}

// UnifiedLeaderboardResponse 是 /api/unified/leaderboard 的 JSON 响应。
type UnifiedLeaderboardResponse struct {
	CycleID                   string                       `json:"cycle_id"`
	CycleScannedAt            string                       `json:"cycle_scanned_at,omitempty"`
	CycleAgeSeconds           float64                      `json:"cycle_age_seconds,omitempty"`
	CycleStale                bool                         `json:"cycle_stale"`
	CycleStaleMaxAgeSeconds   float64                      `json:"cycle_stale_max_age_seconds,omitempty"`
	GeneratedAt               string                       `json:"generated_at"`
	SourcePath                string                       `json:"source_path"`
	TotalCandidates           int                          `json:"total_candidates"`
	DisplayedCandidates       int                          `json:"displayed_candidates"`
	WouldExecuteCount         int                          `json:"would_execute_count"`
	ScannerWouldExecuteCount  int                          `json:"scanner_would_execute_count"`
	RotationWouldExecuteCount int                          `json:"rotation_would_execute_count"`
	Opportunities             []UnifiedLeaderboardEntry    `json:"opportunities"`
	Strategies                []UnifiedStrategyLeaderboard `json:"strategies"`
	RejectReasonsTop          []UnifiedReasonCount         `json:"reject_reasons_top"`
	VenueCapitalInsufficient  []UnifiedVenueCapitalBlock   `json:"venue_capital_insufficient_top"`
	Note                      string                       `json:"note,omitempty"`
}

// unifiedLeaderboardStrategyOrder 分策略排行的固定展示顺序。
var unifiedLeaderboardStrategyOrder = []string{
	UnifiedStrategySingleVenueSpotPerp,
	UnifiedStrategyCrossVenueSpotPerp,
	UnifiedStrategySingleVenuePerpSpot,
	UnifiedStrategyCrossVenuePerpSpot,
	UnifiedStrategyPerpPerp,
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

const unifiedCycleIDTimeLayout = "20060102T150405.000000000"

func parseUnifiedCycleIDTime(cycleID string) (time.Time, bool) {
	cycleID = strings.TrimSpace(cycleID)
	for _, prefix := range []string{"cvsnap-", "cvcycle-"} {
		if !strings.HasPrefix(cycleID, prefix) {
			continue
		}
		ts := strings.TrimPrefix(cycleID, prefix)
		t, err := time.Parse(unifiedCycleIDTimeLayout, ts)
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

func latestUnifiedCycleID(records []UnifiedCandidateLogRecord) string {
	seen := map[string]struct{}{}
	var latestParsedID string
	var latestParsed time.Time
	var hasParsed bool
	fallbackLatest := ""
	for _, rec := range records {
		cycle := strings.TrimSpace(rec.CycleID)
		if cycle == "" {
			continue
		}
		if _, ok := seen[cycle]; ok {
			continue
		}
		seen[cycle] = struct{}{}
		if t, ok := parseUnifiedCycleIDTime(cycle); ok {
			if !hasParsed || t.After(latestParsed) {
				latestParsed = t
				latestParsedID = cycle
				hasParsed = true
			}
			continue
		}
		if fallbackLatest == "" || cycle > fallbackLatest {
			fallbackLatest = cycle
		}
	}
	if hasParsed {
		return latestParsedID
	}
	return fallbackLatest
}

func unifiedCandidateDedupeKey(rec UnifiedCandidateLogRecord) string {
	venues := strings.Join(rec.Venues, "+")
	return rec.StrategyType + "|" + rec.Symbol + "|" + venues
}

func unifiedScannerWouldExecute(rec UnifiedCandidateLogRecord) bool {
	if rec.ScannerWouldExecute {
		return true
	}
	return rec.WouldExecute
}

func unifiedRecordHasCapitalAware(rec UnifiedCandidateLogRecord) bool {
	return rec.CapitalAwareNotionalUSDT > 0
}

func unifiedCapitalAwareNetEV(rec UnifiedCandidateLogRecord) float64 {
	if unifiedRecordHasCapitalAware(rec) {
		return rec.CapitalAwareNetEVUSDT
	}
	return rec.ExpectedNetEVUSDT
}

func unifiedDisplayNotionalUSDT(rec UnifiedCandidateLogRecord) float64 {
	if unifiedRecordHasCapitalAware(rec) {
		return rec.CapitalAwareNotionalUSDT
	}
	return rec.NotionalUSDT
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
		lastIdx[unifiedCandidateDedupeKey(rec)] = i
	}
	out := make([]UnifiedCandidateLogRecord, 0, len(lastIdx))
	for i, rec := range records {
		key := unifiedCandidateDedupeKey(rec)
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

func unifiedNearestFundingMinutes(rec UnifiedCandidateLogRecord, now time.Time) float64 {
	interval := rec.FundingIntervalHours
	high := strings.TrimSpace(rec.HighNextFundingTime)
	low := strings.TrimSpace(rec.LowNextFundingTime)
	if high != "" && low != "" {
		highMins := unifiedMinutesUntilFundingWithInterval(high, now, interval)
		lowMins := unifiedMinutesUntilFundingWithInterval(low, now, interval)
		switch {
		case highMins > 0 && lowMins > 0:
			if lowMins < highMins {
				return lowMins
			}
			return highMins
		case highMins > 0:
			return highMins
		case lowMins > 0:
			return lowMins
		default:
			return 0
		}
	}
	if high != "" {
		return unifiedMinutesUntilFundingWithInterval(high, now, interval)
	}
	if low != "" {
		return unifiedMinutesUntilFundingWithInterval(low, now, interval)
	}
	return unifiedMinutesUntilFundingWithInterval(rec.NextFundingTime, now, interval)
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
	mins := unifiedNearestFundingMinutes(rec, now)
	settlePart := ""
	if mins > 0 {
		settlePart = fmt.Sprintf("，距结算 %.0f min", mins)
	}
	scannerExec := unifiedScannerWouldExecute(rec)
	execLabel := "扫描未通过"
	if scannerExec {
		execLabel = "扫描通过"
	}
	if rec.RotationWouldExecute {
		execLabel += "·轮动闸可开"
	} else if unifiedRecordHasCapitalAware(rec) {
		execLabel += "·轮动闸不可开"
	}
	capNetEV := unifiedCapitalAwareNetEV(rec)
	capNotional := unifiedDisplayNotionalUSDT(rec)
	capPart := ""
	if unifiedRecordHasCapitalAware(rec) {
		capPart = fmt.Sprintf("，按可投名义 %.2f USDT 净EV %.4f", capNotional, capNetEV)
	} else {
		capPart = fmt.Sprintf("，扫描估算名义 %.2f USDT 净EV %.4f", rec.NotionalUSDT, rec.ExpectedNetEVUSDT)
	}
	return fmt.Sprintf(
		"排名第 %d（%s）：%s %.2f%%；%s @ %s%s%s",
		rank, execLabel, fundingLabel, fundingVal*100, rec.Symbol, venueList, capPart, settlePart,
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
		if unifiedScannerWouldExecute(rec) {
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

// unifiedLeaderboardDisplayable 返回默认榜单是否展示该候选。
// 结构性拒绝行(no_spot_market/not_borrowable/方向不支持等)常只有费率/APR,没有 EV/名义/资金占用;
// 它们保留在拒绝原因统计里,但不挤占默认“机会”榜单。
func unifiedLeaderboardDisplayable(rec UnifiedCandidateLogRecord) bool {
	if unifiedScannerWouldExecute(rec) || rec.RotationWouldExecute {
		return true
	}
	if rec.ExpectedNetEVUSDT != 0 || rec.ExpectedFundingUSDT != 0 || rec.NotionalUSDT > 0 {
		return true
	}
	return sumCapitalRequired(rec.CapitalRequiredByVenue) > 0
}

func filterUnifiedLeaderboardDisplayRecords(records []UnifiedCandidateLogRecord) []UnifiedCandidateLogRecord {
	out := make([]UnifiedCandidateLogRecord, 0, len(records))
	for _, rec := range records {
		if unifiedLeaderboardDisplayable(rec) {
			out = append(out, rec)
		}
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
	rec          UnifiedCandidateLogRecord
	landscapeKey float64
	primaryKey   float64
	original     int
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
	scannerExec := unifiedScannerWouldExecute(rec)
	hasCapital := unifiedRecordHasCapitalAware(rec)
	return UnifiedLeaderboardEntry{
		Rank:                     rank,
		StrategyType:             rec.StrategyType,
		Symbol:                   rec.Symbol,
		Venues:                   venues,
		ExpectedNetEVUSDT:        rec.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:      rec.ExpectedFundingUSDT,
		CapitalAwareNetEVUSDT:    rec.CapitalAwareNetEVUSDT,
		CapitalAwareNotionalUSDT: rec.CapitalAwareNotionalUSDT,
		ScannerEstimateOnly:      !hasCapital,
		CapitalRequiredByVenue:   capital,
		CapitalEfficiencyScore:   rec.CapitalEfficiencyScore,
		LandscapeSortKey:         landscapeKey,
		PrimarySortKey:           unifiedPrimarySortKey(rec),
		RankReason:               buildUnifiedRankReason(rank, rec, landscapeKey, now),
		WouldExecute:             scannerExec,
		ScannerWouldExecute:      scannerExec,
		RotationWouldExecute:     rec.RotationWouldExecute,
		RejectReasons:            rejectReasons,
		NotionalUSDT:             rec.NotionalUSDT,
		RawFundingRate:           rec.RawFundingRate,
		FundingRatePerHour:       rec.FundingRatePerHour,
		FundingAPR:               rec.FundingAPR,
		SpreadAPR:                rec.SpreadAPR,
		HighFundingRate:          rec.HighFundingRate,
		LowFundingRate:           rec.LowFundingRate,
		FundingIntervalHours:     rec.FundingIntervalHours,
		NextFundingTime:          rec.NextFundingTime,
		HighNextFundingTime:      rec.HighNextFundingTime,
		LowNextFundingTime:       rec.LowNextFundingTime,
		MinutesUntilFunding:      unifiedNearestFundingMinutes(rec, now),
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
	resp.CycleStaleMaxAgeSeconds = rotationSnapshotMaxAge.Seconds()
	if scannedAt, ok := parseUnifiedCycleIDTime(cycleID); ok {
		resp.CycleScannedAt = scannedAt.Format(time.RFC3339)
		resp.CycleAgeSeconds = now.Sub(scannedAt).Seconds()
		if resp.CycleAgeSeconds < 0 {
			resp.CycleAgeSeconds = 0
		}
		resp.CycleStale = resp.CycleAgeSeconds > rotationSnapshotMaxAge.Seconds()
	}
	scannerCount, rotationCount := countUnifiedExecuteFlags(cycleRecords)
	resp.ScannerWouldExecuteCount = scannerCount
	resp.RotationWouldExecuteCount = rotationCount
	resp.WouldExecuteCount = scannerCount

	// 总排行(全策略混排)。
	displayRecords := filterUnifiedLeaderboardDisplayRecords(cycleRecords)
	resp.Opportunities, _ = rankUnifiedRecords(displayRecords, topN, now)
	resp.DisplayedCandidates = len(resp.Opportunities)

	// 分策略排行:每个策略独立排序+独立 rank,固定顺序展示(无候选也保留空分组,便于前端固定 5 个区块)。
	byStrategy := make(map[string][]UnifiedCandidateLogRecord, len(unifiedLeaderboardStrategyOrder))
	for _, rec := range cycleRecords {
		byStrategy[rec.StrategyType] = append(byStrategy[rec.StrategyType], rec)
	}
	resp.Strategies = make([]UnifiedStrategyLeaderboard, 0, len(unifiedLeaderboardStrategyOrder))
	for _, st := range unifiedLeaderboardStrategyOrder {
		sub := byStrategy[st]
		subScanner, subRotation := countUnifiedExecuteFlags(sub)
		displaySub := filterUnifiedLeaderboardDisplayRecords(sub)
		opps, _ := rankUnifiedRecords(displaySub, topN, now)
		resp.Strategies = append(resp.Strategies, UnifiedStrategyLeaderboard{
			StrategyType:              st,
			TotalCandidates:           len(sub),
			DisplayedCandidates:       len(opps),
			WouldExecuteCount:         subScanner,
			ScannerWouldExecuteCount:  subScanner,
			RotationWouldExecuteCount: subRotation,
			Opportunities:             opps,
		})
	}

	resp.RejectReasonsTop = buildUnifiedReasonCounts(cycleRecords, 15)
	resp.VenueCapitalInsufficient = buildVenueCapitalInsufficientTop(cycleRecords, 10)
	return resp
}

func countUnifiedExecuteFlags(records []UnifiedCandidateLogRecord) (scannerCount, rotationCount int) {
	for _, rec := range records {
		if unifiedScannerWouldExecute(rec) {
			scannerCount++
		}
		if rec.RotationWouldExecute {
			rotationCount++
		}
	}
	return scannerCount, rotationCount
}

// rankUnifiedRecords 对一组候选排序并产出带 rank 的展示条目;返回条目与其中扫描通过数。
// 排序口径:轮动闸可开置顶 → 按可投名义净EV降序 → landscape(费率/价差 APR)降序 → symbol 升序 → 原始顺序。
func rankUnifiedRecords(records []UnifiedCandidateLogRecord, topN int, now time.Time) ([]UnifiedLeaderboardEntry, int) {
	if len(records) == 0 {
		return []UnifiedLeaderboardEntry{}, 0
	}
	wouldExecuteCount := 0
	sortable := make([]unifiedCandidateSortable, len(records))
	for i, rec := range records {
		sortable[i] = unifiedCandidateSortable{
			rec:          rec,
			landscapeKey: unifiedLandscapeSortKey(rec),
			primaryKey:   unifiedPrimarySortKey(rec),
			original:     i,
		}
		if unifiedScannerWouldExecute(rec) {
			wouldExecuteCount++
		}
	}
	sort.SliceStable(sortable, func(i, j int) bool {
		iRot := sortable[i].rec.RotationWouldExecute
		jRot := sortable[j].rec.RotationWouldExecute
		if iRot != jRot {
			return iRot
		}
		iCapEV := unifiedCapitalAwareNetEV(sortable[i].rec)
		jCapEV := unifiedCapitalAwareNetEV(sortable[j].rec)
		if iCapEV != jCapEV {
			return iCapEV > jCapEV
		}
		if sortable[i].landscapeKey != sortable[j].landscapeKey {
			return sortable[i].landscapeKey > sortable[j].landscapeKey
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

	out := make([]UnifiedLeaderboardEntry, 0, len(sortable))
	for i, item := range sortable {
		out = append(out, leaderboardEntryFromRecord(item.rec, i+1, item.landscapeKey, now))
	}
	return out, wouldExecuteCount
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
