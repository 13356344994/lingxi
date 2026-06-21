package app

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	carryScannerMinFundingCaptureProbability = 0.30
	carryScannerFundingCapturePriorMean      = 0.50
	carryScannerFundingCapturePriorStrength  = 20.0
)

// FundingCaptureEstimateConfig 控制 settlement 回测的 capture 概率贝叶斯收缩。
type FundingCaptureEstimateConfig struct {
	PriorMean     float64
	PriorStrength float64
	Floor         float64
	Ceiling       float64
}

func defaultFundingCaptureEstimateConfig() FundingCaptureEstimateConfig {
	return FundingCaptureEstimateConfig{
		PriorMean:     carryScannerFundingCapturePriorMean,
		PriorStrength: carryScannerFundingCapturePriorStrength,
		Floor:         carryScannerMinFundingCaptureProbability,
		Ceiling:       1.0,
	}
}

type fundingCaptureSample struct {
	StrategyType         string
	ExpectedFundingUSDT  float64
	RealizedFundingUSDT  float64
	FundingIntervalHours int
}

func fundingCaptureRatio(sample fundingCaptureSample) (float64, bool) {
	if sample.ExpectedFundingUSDT <= 0 {
		return 0, false
	}
	ratio := sample.RealizedFundingUSDT / sample.ExpectedFundingUSDT
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return ratio, true
}

func bayesianFundingCaptureProbability(ratios []float64, cfg FundingCaptureEstimateConfig) float64 {
	if cfg.PriorMean <= 0 {
		cfg.PriorMean = carryScannerFundingCapturePriorMean
	}
	if cfg.PriorStrength <= 0 {
		cfg.PriorStrength = carryScannerFundingCapturePriorStrength
	}
	if cfg.Floor <= 0 {
		cfg.Floor = carryScannerMinFundingCaptureProbability
	}
	if cfg.Ceiling <= 0 {
		cfg.Ceiling = 1.0
	}
	sum := 0.0
	n := 0.0
	for _, r := range ratios {
		sum += r
		n++
	}
	est := (cfg.PriorStrength*cfg.PriorMean + sum) / (cfg.PriorStrength + n)
	if est < cfg.Floor {
		return cfg.Floor
	}
	if est > cfg.Ceiling {
		return cfg.Ceiling
	}
	return est
}

func estimateFundingCaptureProbabilityForStrategy(records []fundingCaptureSample, strategyType string) float64 {
	cfg := defaultFundingCaptureEstimateConfig()
	want := strings.TrimSpace(strategyType)
	ratios := make([]float64, 0, len(records))
	for _, rec := range records {
		if want != "" && rec.StrategyType != want {
			continue
		}
		ratio, ok := fundingCaptureRatio(rec)
		if !ok {
			continue
		}
		ratios = append(ratios, ratio)
	}
	if len(ratios) == 0 {
		return carryScannerDefaultFundingCaptureProbability
	}
	return bayesianFundingCaptureProbability(ratios, cfg)
}

func loadFundingCaptureSamples(recordDir string) ([]fundingCaptureSample, error) {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	var out []fundingCaptureSample

	perpRecs, err := loadTailJSONLFile[CrossVenueSettlement](filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range perpRecs {
		if sample, ok := crossVenueSettlementCaptureSample(rec); ok {
			out = append(out, sample)
		}
	}

	svspRecs, err := loadTailJSONLFile[SingleVenueSpotPerpSettlement](filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svspRecs {
		if sample, ok := singleVenueSpotPerpSettlementCaptureSample(rec); ok {
			out = append(out, sample)
		}
	}

	cvspRecs, err := loadTailJSONLFile[CrossVenueSpotPerpSettlement](filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvspRecs {
		if sample, ok := crossVenueSpotPerpSettlementCaptureSample(rec); ok {
			out = append(out, sample)
		}
	}

	svpsRecs, err := loadTailJSONLFile[SingleVenuePerpSpotSettlement](filepath.Join(singleVenuePerpSpotRecordDir(dir), svpsSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svpsRecs {
		if sample, ok := singleVenuePerpSpotSettlementCaptureSample(rec); ok {
			out = append(out, sample)
		}
	}

	cvpsRecs, err := loadTailJSONLFile[CrossVenuePerpSpotSettlement](filepath.Join(crossVenuePerpSpotRecordDir(dir), crossVenuePerpSpotSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvpsRecs {
		if sample, ok := crossVenuePerpSpotSettlementCaptureSample(rec); ok {
			out = append(out, sample)
		}
	}

	return out, nil
}

func estimateFundingCaptureProbabilityFromRecords(recordDir, strategyType string) (float64, bool) {
	samples, err := loadFundingCaptureSamples(recordDir)
	if err != nil || len(samples) == 0 {
		return carryScannerDefaultFundingCaptureProbability, false
	}
	want := strings.TrimSpace(strategyType)
	filtered := samples
	if want != "" {
		filtered = make([]fundingCaptureSample, 0, len(samples))
		for _, s := range samples {
			if s.StrategyType == want {
				filtered = append(filtered, s)
			}
		}
	}
	if len(filtered) == 0 {
		return carryScannerDefaultFundingCaptureProbability, false
	}
	return estimateFundingCaptureProbabilityForStrategy(filtered, ""), true
}

func fundingCaptureExpectedOneSettlement(totalExpected float64, hold time.Duration, fundingIntervalHours int) float64 {
	if totalExpected <= 0 || hold <= 0 {
		return 0
	}
	if fundingIntervalHours <= 0 {
		fundingIntervalHours = defaultFundingCaptureIntervalHours
	}
	interval := time.Duration(fundingIntervalHours) * time.Hour
	settlements := hold / interval
	if settlements < 1 {
		return 0
	}
	return totalExpected / float64(settlements)
}

func crossVenueSettlementCaptureIntervalHours(rec CrossVenueSettlement) int {
	interval := 0
	if rec.HighIntervalHours > 0 {
		if interval == 0 || rec.HighIntervalHours < interval {
			interval = rec.HighIntervalHours
		}
	}
	if rec.LowIntervalHours > 0 {
		if interval == 0 || rec.LowIntervalHours < interval {
			interval = rec.LowIntervalHours
		}
	}
	if interval <= 0 {
		return defaultFundingCaptureIntervalHours
	}
	return interval
}

func crossVenueSettlementCaptureSample(rec CrossVenueSettlement) (fundingCaptureSample, bool) {
	interval := crossVenueSettlementCaptureIntervalHours(rec)
	if !settlementHeldThroughFirstFunding(rec.OpenTime, rec.CloseTime, interval) {
		return fundingCaptureSample{}, false
	}
	expected := fundingCaptureExpectedOneSettlement(rec.EstimatedFunding, rec.CloseTime.Sub(rec.OpenTime), interval)
	if expected <= 0 {
		return fundingCaptureSample{}, false
	}
	return fundingCaptureSample{
		StrategyType:         UnifiedStrategyPerpPerp,
		ExpectedFundingUSDT:  expected,
		RealizedFundingUSDT:  rec.RealizedFunding,
		FundingIntervalHours: interval,
	}, true
}

func singleVenueSpotPerpSettlementCaptureSample(rec SingleVenueSpotPerpSettlement) (fundingCaptureSample, bool) {
	interval := rec.FundingIntervalHours
	if interval <= 0 {
		interval = defaultFundingCaptureIntervalHours
	}
	if !settlementHeldThroughFirstFunding(rec.OpenTime, rec.CloseTime, interval) {
		return fundingCaptureSample{}, false
	}
	expected := rec.ExpectedFundingUSDT
	if expected <= 0 {
		return fundingCaptureSample{}, false
	}
	expectedOne := fundingCaptureExpectedOneSettlement(expected, rec.CloseTime.Sub(rec.OpenTime), interval)
	if expectedOne <= 0 {
		return fundingCaptureSample{}, false
	}
	return fundingCaptureSample{
		StrategyType:         UnifiedStrategySingleVenueSpotPerp,
		ExpectedFundingUSDT:  expectedOne,
		RealizedFundingUSDT:  rec.RealizedFunding,
		FundingIntervalHours: interval,
	}, true
}

func crossVenueSpotPerpSettlementCaptureSample(rec CrossVenueSpotPerpSettlement) (fundingCaptureSample, bool) {
	interval := rec.PerpIntervalHours
	if interval <= 0 {
		interval = defaultFundingCaptureIntervalHours
	}
	if !settlementHeldThroughFirstFunding(rec.OpenTime, rec.CloseTime, interval) {
		return fundingCaptureSample{}, false
	}
	expected := rec.ExpectedFundingUSDT
	if expected <= 0 {
		return fundingCaptureSample{}, false
	}
	expectedOne := fundingCaptureExpectedOneSettlement(expected, rec.CloseTime.Sub(rec.OpenTime), interval)
	if expectedOne <= 0 {
		return fundingCaptureSample{}, false
	}
	return fundingCaptureSample{
		StrategyType:         UnifiedStrategyCrossVenueSpotPerp,
		ExpectedFundingUSDT:  expectedOne,
		RealizedFundingUSDT:  rec.RealizedFunding,
		FundingIntervalHours: interval,
	}, true
}

func singleVenuePerpSpotSettlementCaptureSample(rec SingleVenuePerpSpotSettlement) (fundingCaptureSample, bool) {
	interval := rec.FundingIntervalHours
	if interval <= 0 {
		interval = defaultFundingCaptureIntervalHours
	}
	if !settlementHeldThroughFirstFunding(rec.OpenTime, rec.CloseTime, interval) {
		return fundingCaptureSample{}, false
	}
	expected := rec.ExpectedFundingUSDT
	if expected <= 0 {
		return fundingCaptureSample{}, false
	}
	expectedOne := fundingCaptureExpectedOneSettlement(expected, rec.CloseTime.Sub(rec.OpenTime), interval)
	if expectedOne <= 0 {
		return fundingCaptureSample{}, false
	}
	return fundingCaptureSample{
		StrategyType:         UnifiedStrategySingleVenuePerpSpot,
		ExpectedFundingUSDT:  expectedOne,
		RealizedFundingUSDT:  rec.RealizedFunding,
		FundingIntervalHours: interval,
	}, true
}

func crossVenuePerpSpotSettlementCaptureSample(rec CrossVenuePerpSpotSettlement) (fundingCaptureSample, bool) {
	interval := rec.PerpIntervalHours
	if interval <= 0 {
		interval = defaultFundingCaptureIntervalHours
	}
	if !settlementHeldThroughFirstFunding(rec.OpenTime, rec.CloseTime, interval) {
		return fundingCaptureSample{}, false
	}
	expected := rec.ExpectedFundingUSDT
	if expected <= 0 {
		return fundingCaptureSample{}, false
	}
	expectedOne := fundingCaptureExpectedOneSettlement(expected, rec.CloseTime.Sub(rec.OpenTime), interval)
	if expectedOne <= 0 {
		return fundingCaptureSample{}, false
	}
	return fundingCaptureSample{
		StrategyType:         UnifiedStrategyCrossVenuePerpSpot,
		ExpectedFundingUSDT:  expectedOne,
		RealizedFundingUSDT:  rec.RealizedFunding,
		FundingIntervalHours: interval,
	}, true
}

func applyEstimatedFundingCaptureProbability(cfg *CrossVenueSpotPerpScannerConfig, recordDir, strategyType string) {
	if cfg == nil {
		return
	}
	if est, ok := estimateFundingCaptureProbabilityFromRecords(recordDir, strategyType); ok {
		cfg.FundingCaptureProbability = est
	}
}

func scannerFundingCaptureProbability(recordDir, strategyType string, configured float64) float64 {
	if configured > 0 {
		return carryScannerNormalizedFundingCaptureProbability(configured)
	}
	if est, ok := estimateFundingCaptureProbabilityFromRecords(recordDir, strategyType); ok {
		return carryScannerNormalizedFundingCaptureProbability(est)
	}
	return carryScannerDefaultFundingCaptureProbability
}

func refreshCrossVenueExecutorFundingCaptureProbability(cfg *CrossVenueExecutorConfig, recordDir string) {
	if cfg == nil {
		return
	}
	cfg.FundingCaptureProbability = scannerFundingCaptureProbability(recordDir, UnifiedStrategyPerpPerp, 0)
}

func crossVenueExecutorFundingCaptureProbability(cfg CrossVenueExecutorConfig) float64 {
	if cfg.FundingCaptureProbability > 0 {
		return cfg.FundingCaptureProbability
	}
	return carryScannerDefaultFundingCaptureProbability
}
