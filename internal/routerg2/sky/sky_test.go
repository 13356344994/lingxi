package sky

import (
	"math"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hourlyBars(closes []float64) []Bar {
	bars := make([]Bar, len(closes))
	for i, c := range closes {
		bars[i] = Bar{OpenTime: base.Add(time.Duration(i) * time.Hour), Close: c}
	}
	return bars
}

// decisionAfter 返回恰好让前 n 根 1h bar 已收盘的 decision_time。
func decisionAfter(n int) time.Time {
	return base.Add(time.Duration(n) * time.Hour)
}

func TestTrendEfficiencyMonotonicIsOne(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 100 + float64(i) // 严格单调:net == path
	}
	value, ok := trendEfficiency(closes)
	if !ok {
		t.Fatal("expected computable trend efficiency")
	}
	if math.Abs(value-1.0) > 1e-9 {
		t.Fatalf("monotonic trend efficiency = %v, want 1.0", value)
	}
}

func TestSignedReturnSeparatesUpAndDownMarkets(t *testing.T) {
	up := make([]float64, 25)
	down := make([]float64, 25)
	for i := range up {
		up[i] = 100 + float64(i)
		down[i] = 124 - float64(i)
	}
	upValue, upOK := signedReturn(up)
	downValue, downOK := signedReturn(down)
	if !upOK || !downOK || upValue <= 0 || downValue >= 0 {
		t.Fatalf("signed market direction missing: up=%v down=%v", upValue, downValue)
	}
}

func TestTrendEfficiencyZigzagBelowOne(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		if i%2 == 0 {
			closes[i] = 100
		} else {
			closes[i] = 101
		}
	}
	value, ok := trendEfficiency(closes)
	if !ok || value >= 1.0 {
		t.Fatalf("zigzag trend efficiency = %v (ok=%v), want < 1.0", value, ok)
	}
}

func TestRealizedVolatilityFlatIsZero(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 100
	}
	value, ok := realizedVolatility(closes)
	if !ok || value != 0 {
		t.Fatalf("flat realized volatility = %v (ok=%v), want 0", value, ok)
	}
}

func TestDrawdown168hPeakToLast(t *testing.T) {
	closes := make([]float64, 169)
	for i := range closes {
		closes[i] = 50
	}
	closes[100] = 100 // peak
	closes[168] = 90  // last
	value, ok := drawdown168h(closes)
	if !ok {
		t.Fatal("expected computable drawdown")
	}
	if math.Abs(value-0.10) > 1e-9 {
		t.Fatalf("drawdown = %v, want 0.10", value)
	}
}

func TestInsufficientBarsAreOOD(t *testing.T) {
	// 只有 10 根 bar,不足以支撑 24h/168h 维度 -> OOD 并记录缺失维度。
	series := Series{Bars: hourlyBars(constSlice(10, 100))}
	features := ComputeFeatures(series, decisionAfter(10))
	if !features.OOD {
		t.Fatal("insufficient history must be OOD")
	}
	if len(features.Missing) == 0 {
		t.Fatal("OOD features must record missing dimensions")
	}
}

func TestDecisionTimeExcludesUnclosedBar(t *testing.T) {
	// 200 根正常 bar + 1 根"决策时刻尚未收盘"的异常 bar;后者必须被排除,不影响特征。
	closes := constSlice(200, 100)
	series := Series{Bars: hourlyBars(closes), Funding: hourlyFunding(200, 0.0001)}
	decision := decisionAfter(200)
	baseFeatures := ComputeFeatures(series, decision)
	if baseFeatures.OOD {
		t.Fatalf("expected in-support features, missing=%v", baseFeatures.Missing)
	}

	// 追加一根 open_time == decision 的未收盘 bar(open+1h > decision),给它极端值。
	poisoned := append([]Bar(nil), series.Bars...)
	poisoned = append(poisoned, Bar{OpenTime: decision, Close: 1e9})
	poisonedFeatures := ComputeFeatures(Series{Bars: poisoned, Funding: series.Funding}, decision)
	if poisonedFeatures.Values[DimRealizedVol24h] != baseFeatures.Values[DimRealizedVol24h] ||
		poisonedFeatures.Values[DimTrendEfficiency24h] != baseFeatures.Values[DimTrendEfficiency24h] {
		t.Fatal("未收盘 bar 泄漏进了司天监特征(违反 decision_time_rule)")
	}
}

func TestFitOnlyOnDiscoveryResearchPartition(t *testing.T) {
	samples := rampSamples(9)
	if _, err := Fit("forward", samples, []float64{0.3333333333, 0.6666666667}); err == nil {
		t.Fatal("fitting cutpoints outside discovery_research must be rejected")
	}
	if _, err := Fit(FitPartition, samples, []float64{0.3333333333, 0.6666666667}); err != nil {
		t.Fatalf("discovery_research fit failed: %v", err)
	}
}

func TestFrozenCutpointsClassifyIntoThreeBucketsAndCarryNoDirection(t *testing.T) {
	cuts, err := Fit(FitPartition, rampSamples(9), []float64{0.3333333333, 0.6666666667})
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	frozen := cuts.Freeze()
	if !frozen.Frozen {
		t.Fatal("Freeze must set Frozen")
	}

	features := FeatureSet{Values: map[string]float64{
		DimReturn24h:          2,
		DimTrendEfficiency24h: 2, // low
		DimRealizedVol24h:     4, // mid
		DimDrawdown168h:       7, // high
		DimFundingState:       9, // high
	}}
	state := Classify(features, frozen)
	if state.OOD {
		t.Fatal("in-support features must not be OOD")
	}
	if state.Buckets[DimReturn24h] != 0 || state.Buckets[DimTrendEfficiency24h] != 0 || state.Buckets[DimRealizedVol24h] != 1 ||
		state.Buckets[DimDrawdown168h] != 2 || state.Buckets[DimFundingState] != 2 {
		t.Fatalf("unexpected buckets: %+v", state.Buckets)
	}
	if state.Label != "ret=low/te=low/rv=mid/dd=high/fund=high" {
		t.Fatalf("label = %q", state.Label)
	}
	// SkyState 只表示行情,不得隐含交易方向。
	if strings.Contains(state.Label, "long") || strings.Contains(state.Label, "short") {
		t.Fatalf("SkyState label leaked a direction: %q", state.Label)
	}
}

func TestUnfrozenCutpointsCannotClassify(t *testing.T) {
	cuts, err := Fit(FitPartition, rampSamples(9), []float64{0.3333333333, 0.6666666667})
	if err != nil {
		t.Fatal(err)
	}
	state := Classify(rampSamples(1)[0], cuts)
	if !state.OOD {
		t.Fatal("unfrozen discovery cutpoints must not be usable in forward/WF")
	}
}

func TestFrozenCutpointsApplyIdenticallyToForwardAndWF(t *testing.T) {
	cuts, err := Fit(FitPartition, rampSamples(9), []float64{0.3333333333, 0.6666666667})
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	frozen := cuts.Freeze()
	hash1, err := frozen.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// 冻结后应用于任意后段样本:同样的特征值必然得到同样的桶(不重新拟合)。
	features := FeatureSet{Values: map[string]float64{
		DimReturn24h: 5, DimTrendEfficiency24h: 5, DimRealizedVol24h: 5, DimDrawdown168h: 5, DimFundingState: 5,
	}}
	forward := Classify(features, frozen)
	wf := Classify(features, frozen)
	if forward.Label != wf.Label {
		t.Fatalf("frozen cutpoints produced non-deterministic labels: %q vs %q", forward.Label, wf.Label)
	}
	hash2, _ := frozen.Hash()
	if hash1 != hash2 || hash1 == "" {
		t.Fatalf("cutpoint hash not stable: %q vs %q", hash1, hash2)
	}
}

func TestOODFeaturesClassifyOOD(t *testing.T) {
	cuts, err := Fit(FitPartition, rampSamples(9), []float64{0.3333333333, 0.6666666667})
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	state := Classify(FeatureSet{OOD: true, Missing: []string{DimFundingState}}, cuts.Freeze())
	if !state.OOD {
		t.Fatal("OOD features must classify to OOD SkyState")
	}
}

func constSlice(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func hourlyFunding(n int, rate float64) []FundingPoint {
	out := make([]FundingPoint, n)
	for i := range out {
		out[i] = FundingPoint{Time: base.Add(time.Duration(i) * time.Hour), Rate: rate}
	}
	return out
}

// rampSamples 构造 n 个特征样本,每个维度取值 1..n(用于分位切点拟合)。
func rampSamples(n int) []FeatureSet {
	samples := make([]FeatureSet, n)
	for i := 0; i < n; i++ {
		value := float64(i + 1)
		samples[i] = FeatureSet{Values: map[string]float64{
			DimReturn24h:          value,
			DimTrendEfficiency24h: value,
			DimRealizedVol24h:     value,
			DimDrawdown168h:       value,
			DimFundingState:       value,
		}}
	}
	return samples
}
