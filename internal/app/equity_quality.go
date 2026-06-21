package app

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lingxi/internal/money"
)

const (
	// equityPeakConfirmSampleCount 连续 complete 样本数后才抬升 peak(挡单样本毛刺)。
	equityPeakConfirmSampleCount = 3
	// equityExpectedVenueLookback 推导预期 venue 集合时回看的 timestamp 样本数。
	equityExpectedVenueLookback = 10
	// equityVenueZeroLookback 判定 venue=0 是否可疑时回看的 complete 样本数。
	equityVenueZeroLookback = 5
	// equityMinVenuePresenceRatio 近期样本中 venue 出现比例下限(推导预期集合)。
	equityMinVenuePresenceRatio = 0.5
)

// realEquityQualityResult 真实现金权益回放质量闸结果(供 auto-flatten 与观测)。
type realEquityQualityResult struct {
	Passed                  bool
	Reason                  string
	PeakConfirmed           bool
	CurrentSampleComplete   bool
	CurrentSampleValid      bool
	ExpectedVenues          []string
	LatestCompleteTotalUSDT float64
}

type equityTimestampSample struct {
	TS        string
	ByVenue   map[string]float64
	Complete  *bool
	TotalUSDT float64
}

func loadEquityTimestampSamples(recordDir string) ([]equityTimestampSample, error) {
	path := filepath.Join(crossVenueRecordDir(recordDir), equitySnapshotsFileName)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	byTS := make(map[string]*equityTimestampSample)
	var tsOrder []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if strings.TrimSpace(rec.TS) == "" || strings.TrimSpace(rec.Venue) == "" {
			continue
		}
		s, ok := byTS[rec.TS]
		if !ok {
			s = &equityTimestampSample{
				TS:      rec.TS,
				ByVenue: make(map[string]float64),
			}
			byTS[rec.TS] = s
			tsOrder = append(tsOrder, rec.TS)
		}
		s.ByVenue[rec.Venue] = rec.CashUSDT
		if rec.Complete != nil {
			s.Complete = rec.Complete
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(tsOrder)
	out := make([]equityTimestampSample, 0, len(tsOrder))
	for _, ts := range tsOrder {
		s := byTS[ts]
		var total float64
		for _, cash := range s.ByVenue {
			total = money.SumUSDT(total, cash)
		}
		s.TotalUSDT = total
		out = append(out, *s)
	}
	return out, nil
}

func deriveExpectedEquityVenues(samples []equityTimestampSample) map[string]struct{} {
	if len(samples) == 0 {
		return nil
	}
	lookback := equityExpectedVenueLookback
	if len(samples) < lookback {
		lookback = len(samples)
	}
	recent := samples[len(samples)-lookback:]

	venueCounts := make(map[string]int)
	for _, s := range recent {
		for venue := range s.ByVenue {
			venueCounts[venue]++
		}
	}
	threshold := int(math.Ceil(float64(lookback) * equityMinVenuePresenceRatio))
	expected := make(map[string]struct{})
	for venue, count := range venueCounts {
		if count >= threshold {
			expected[venue] = struct{}{}
		}
	}
	if len(expected) > 0 {
		return expected
	}
	best := recent[len(recent)-1]
	for _, s := range recent {
		if len(s.ByVenue) > len(best.ByVenue) {
			best = s
		}
	}
	for venue := range best.ByVenue {
		expected[venue] = struct{}{}
	}
	return expected
}

func expectedEquityVenueNames(expected map[string]struct{}) []string {
	if len(expected) == 0 {
		return nil
	}
	out := make([]string, 0, len(expected))
	for venue := range expected {
		out = append(out, venue)
	}
	sort.Strings(out)
	return out
}

func missingEquityVenues(sample equityTimestampSample, expected map[string]struct{}) []string {
	if len(expected) == 0 {
		return nil
	}
	var missing []string
	for venue := range expected {
		if _, ok := sample.ByVenue[venue]; !ok {
			missing = append(missing, venue)
		}
	}
	sort.Strings(missing)
	return missing
}

func isEquitySampleExplicitlyIncomplete(sample equityTimestampSample) bool {
	return sample.Complete != nil && !*sample.Complete
}

func isEquitySampleComplete(sample equityTimestampSample, expected map[string]struct{}) bool {
	if isEquitySampleExplicitlyIncomplete(sample) {
		return false
	}
	if len(expected) == 0 {
		return len(sample.ByVenue) > 0
	}
	return len(missingEquityVenues(sample, expected)) == 0
}

func venueHadRecentNonZeroCash(venue string, priorComplete []equityTimestampSample) bool {
	lookback := equityVenueZeroLookback
	for i := len(priorComplete) - 1; i >= 0 && lookback > 0; i-- {
		s := priorComplete[i]
		cash, ok := s.ByVenue[venue]
		if !ok {
			continue
		}
		lookback--
		if cash > 0 {
			return true
		}
	}
	return false
}

func isEquitySampleValid(sample equityTimestampSample, priorComplete []equityTimestampSample) (bool, string) {
	for venue, cash := range sample.ByVenue {
		if cash <= 0 && venueHadRecentNonZeroCash(venue, priorComplete) {
			return false, "venue " + venue + " 现金读成 0"
		}
	}
	if sample.TotalUSDT <= 0 && len(priorComplete) > 0 {
		last := priorComplete[len(priorComplete)-1]
		if last.TotalUSDT > 0 {
			return false, "总权益读成 0"
		}
	}
	return true, ""
}

func assessLatestEquitySampleQuality(samples []equityTimestampSample, expected map[string]struct{}) (complete, valid bool, reason string) {
	if len(samples) == 0 {
		return false, false, "无权益样本"
	}
	latest := samples[len(samples)-1]
	complete = isEquitySampleComplete(latest, expected)
	if !complete {
		missing := missingEquityVenues(latest, expected)
		if len(missing) > 0 {
			return false, false, "最新样本 venue 残缺: 缺 " + strings.Join(missing, ",")
		}
		return false, false, "最新样本标记为 incomplete"
	}
	priorComplete := filterCompleteValidEquitySamples(samples[:len(samples)-1], expected)
	valid, reason = isEquitySampleValid(latest, priorComplete)
	return complete, valid, reason
}

func filterCompleteValidEquitySamples(samples []equityTimestampSample, expected map[string]struct{}) []equityTimestampSample {
	var out []equityTimestampSample
	for _, sample := range samples {
		if !isEquitySampleComplete(sample, expected) {
			continue
		}
		valid, _ := isEquitySampleValid(sample, out)
		if !valid {
			continue
		}
		out = append(out, sample)
	}
	return out
}

func computeConfirmedRealEquityWaterMark(samples []equityTimestampSample) (current, peak, drawdown float64, quality realEquityQualityResult) {
	expected := deriveExpectedEquityVenues(samples)
	quality.ExpectedVenues = expectedEquityVenueNames(expected)

	completeValid := filterCompleteValidEquitySamples(samples, expected)
	if len(completeValid) == 0 {
		quality.Reason = "无 complete 且 valid 的权益样本"
		return 0, 0, 0, quality
	}

	current = completeValid[len(completeValid)-1].TotalUSDT
	quality.LatestCompleteTotalUSDT = current
	latestComplete, latestValid, latestReason := assessLatestEquitySampleQuality(samples, expected)
	quality.CurrentSampleComplete = latestComplete
	quality.CurrentSampleValid = latestValid
	if !latestComplete || !latestValid {
		quality.Reason = latestReason
	} else {
		quality.Passed = true
	}

	recentTotals := make([]float64, 0, equityPeakConfirmSampleCount)
	for _, sample := range completeValid {
		current = sample.TotalUSDT
		recentTotals = append(recentTotals, sample.TotalUSDT)
		if len(recentTotals) > equityPeakConfirmSampleCount {
			recentTotals = recentTotals[len(recentTotals)-equityPeakConfirmSampleCount:]
		}
		if len(recentTotals) < equityPeakConfirmSampleCount {
			continue
		}
		minTotal := recentTotals[0]
		maxTotal := recentTotals[0]
		for _, total := range recentTotals[1:] {
			if total < minTotal {
				minTotal = total
			}
			if total > maxTotal {
				maxTotal = total
			}
		}
		if minTotal > peak {
			peak = maxTotal
			quality.PeakConfirmed = true
		}
	}
	drawdown = money.SumUSDT(peak, -current)
	if drawdown < 0 {
		drawdown = 0
	}
	if !quality.Passed && quality.Reason == "" {
		quality.Reason = "最新权益样本未通过质量闸"
	}
	return current, peak, drawdown, quality
}

func computeRealEquityWaterMarkWithQuality(recordDir string) (current, peak, drawdown float64, quality realEquityQualityResult) {
	samples, err := loadEquityTimestampSamples(recordDir)
	if err != nil {
		if os.IsNotExist(err) {
			quality.Reason = "权益快照文件不存在"
			return 0, 0, 0, quality
		}
		quality.Reason = "读取权益快照失败"
		return 0, 0, 0, quality
	}
	return computeConfirmedRealEquityWaterMark(samples)
}

func realEquityDrawdownPrimarilyFundFlow(snap accountRiskSnapshot, cfg RiskConfig) bool {
	adjust := snap.DailyFundFlowAdjustUSDT
	minStep := effectiveAccountRiskMinFundFlowStepUSDT(cfg)
	if math.Abs(adjust) < minStep {
		return false
	}
	if snap.RealEquityDrawdownUSDT <= 0 {
		return false
	}
	residual := money.SumUSDT(snap.RealEquityDrawdownUSDT, -math.Abs(adjust))
	return residual <= minStep*0.5
}
