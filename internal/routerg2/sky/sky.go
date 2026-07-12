// Package sky 实现 g2 司天监(SkyBureau)历史行情层。
//
// 硬纪律(与 research/firstbattle/hierarchical_router_g2_preregistration.json 的 sky 块一致):
//   - 只用决策时刻之前"已完整收盘"的历史 bar(open_time + timescale <= decision_time);
//     禁止当前未收盘 bar、未来收益、当前 universe 回填。
//   - 特征只描述大行情(BTC 趋势效率 / 实现波动 / 168h 回撤 / funding 状态),不读取任何收益/结算/盈亏;
//     结构上 FeatureSet / Cutpoints 不含收益字段,无法泄漏 PnL。
//   - 分桶切点只在 G2 discovery research 区按特征分布拟合一次(Fit),冻结(Freeze)后原样应用到 forward/WF(Classify)。
//   - 数据缺失或支持域不足一律 OOD;OOD 由上层按 OOD_NO_TRADE 处理。
//   - SkyState 只表示行情,不隐含交易方向(标签中不出现 long/short)。
package sky

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

// 司天监维度名,与 manifest.sky.dimensions[].name 及 protocol.SkyDimensions 逐字一致。
const (
	DimReturn24h          = "btc_return_24h"
	DimTrendEfficiency24h = "btc_trend_efficiency_24h"
	DimRealizedVol24h     = "btc_realized_volatility_24h"
	DimDrawdown168h       = "btc_drawdown_168h"
	DimFundingState       = "okx_btc_funding_state"
)

// FitPartition 是唯一允许拟合分桶切点的分区标签(discovery research 区)。
const FitPartition = "discovery_research"

// Dimensions 是冻结的维度顺序;标签与迭代都按此顺序,保证确定性。
var Dimensions = []string{
	DimReturn24h,
	DimTrendEfficiency24h,
	DimRealizedVol24h,
	DimDrawdown168h,
	DimFundingState,
}

// dimLookbackHours 是每个维度的回看小时数(与 regime contract 一致)。
var dimLookbackHours = map[string]int{
	DimReturn24h:          24,
	DimTrendEfficiency24h: 24,
	DimRealizedVol24h:     24,
	DimDrawdown168h:       168,
	DimFundingState:       24,
}

// dimShortCode 是标签里的稳定短码(不含方向语义)。
var dimShortCode = map[string]string{
	DimReturn24h:          "ret",
	DimTrendEfficiency24h: "te",
	DimRealizedVol24h:     "rv",
	DimDrawdown168h:       "dd",
	DimFundingState:       "fund",
}

// bucketLabels 是三桶(两切点)的稳定标签。
var bucketLabels = []string{"low", "mid", "high"}

// Bar 是一根已收盘 1h K 线(open_time 为开盘时刻,覆盖 [open_time, open_time+1h))。
type Bar struct {
	OpenTime time.Time
	Close    float64
}

// FundingPoint 是一次已结算 funding。
type FundingPoint struct {
	Time time.Time
	Rate float64
}

// Series 是 BTC 的历史 1h bar 与 funding 序列(只含已知历史,不含未来)。
type Series struct {
	Bars    []Bar
	Funding []FundingPoint
}

// FeatureSet 是某个 decision_time 上四个司天监特征的取值。结构上不含任何收益字段。
type FeatureSet struct {
	DecisionTime time.Time
	Values       map[string]float64
	OOD          bool
	Missing      []string
}

// closedBarCloses 返回 open_time + 1h <= decisionTime 的已收盘 1h 收盘价,按时间升序。
func closedBarCloses(bars []Bar, decisionTime time.Time) []float64 {
	sorted := append([]Bar(nil), bars...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OpenTime.Before(sorted[j].OpenTime) })
	closes := make([]float64, 0, len(sorted))
	for _, bar := range sorted {
		if !bar.OpenTime.Add(time.Hour).After(decisionTime) { // open+1h <= decision
			closes = append(closes, bar.Close)
		}
	}
	return closes
}

func lastN(values []float64, n int) []float64 {
	if len(values) < n {
		return nil
	}
	return values[len(values)-n:]
}

func trendEfficiency(closes []float64) (float64, bool) {
	window := lastN(closes, dimLookbackHours[DimTrendEfficiency24h]+1)
	if window == nil {
		return 0, false
	}
	net := math.Abs(window[len(window)-1] - window[0])
	var path float64
	for i := 1; i < len(window); i++ {
		path += math.Abs(window[i] - window[i-1])
	}
	if path == 0 {
		return 0, true // 完全平坦:趋势效率为 0,并非缺数据
	}
	return net / path, true
}

func signedReturn(closes []float64) (float64, bool) {
	window := lastN(closes, dimLookbackHours[DimReturn24h]+1)
	if window == nil || window[0] <= 0 || window[len(window)-1] <= 0 {
		return 0, false
	}
	return window[len(window)-1]/window[0] - 1, true
}

func realizedVolatility(closes []float64) (float64, bool) {
	window := lastN(closes, dimLookbackHours[DimRealizedVol24h]+1)
	if window == nil {
		return 0, false
	}
	var sumSq float64
	for i := 1; i < len(window); i++ {
		if window[i-1] <= 0 || window[i] <= 0 {
			return 0, false
		}
		r := math.Log(window[i] / window[i-1])
		sumSq += r * r
	}
	return math.Sqrt(sumSq), true
}

func drawdown168h(closes []float64) (float64, bool) {
	window := lastN(closes, dimLookbackHours[DimDrawdown168h]+1)
	if window == nil {
		return 0, false
	}
	peak := window[0]
	for _, c := range window {
		if c > peak {
			peak = c
		}
	}
	if peak <= 0 {
		return 0, false
	}
	last := window[len(window)-1]
	return (peak - last) / peak, true
}

func fundingState(funding []FundingPoint, decisionTime time.Time) (float64, bool) {
	lookback := time.Duration(dimLookbackHours[DimFundingState]) * time.Hour
	windowStart := decisionTime.Add(-lookback)
	var sum float64
	var count int
	for _, point := range funding {
		if point.Time.After(decisionTime) { // 只用 decision_time 前已结算 funding
			continue
		}
		if point.Time.After(windowStart) {
			sum += point.Rate
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

// ComputeFeatures 从已收盘 bar / 已结算 funding 计算四个司天监特征;任一缺失即整体 OOD 并记录缺失维度。
func ComputeFeatures(series Series, decisionTime time.Time) FeatureSet {
	set := FeatureSet{DecisionTime: decisionTime, Values: map[string]float64{}}
	if decisionTime.IsZero() {
		set.OOD = true
		set.Missing = append([]string(nil), Dimensions...)
		return set
	}
	closes := closedBarCloses(series.Bars, decisionTime)

	compute := map[string]func() (float64, bool){
		DimReturn24h:          func() (float64, bool) { return signedReturn(closes) },
		DimTrendEfficiency24h: func() (float64, bool) { return trendEfficiency(closes) },
		DimRealizedVol24h:     func() (float64, bool) { return realizedVolatility(closes) },
		DimDrawdown168h:       func() (float64, bool) { return drawdown168h(closes) },
		DimFundingState:       func() (float64, bool) { return fundingState(series.Funding, decisionTime) },
	}
	for _, dim := range Dimensions {
		value, ok := compute[dim]()
		if !ok {
			set.OOD = true
			set.Missing = append(set.Missing, dim)
			continue
		}
		set.Values[dim] = value
	}
	return set
}

// Cutpoints 是冻结的分桶切点(每维两切点 -> 三桶),只在 discovery research 区拟合一次。
type Cutpoints struct {
	Partition  string               `json:"partition"`
	Quantiles  []float64            `json:"quantiles"`
	Cuts       map[string][]float64 `json:"cuts"`
	FitSamples int                  `json:"fit_samples"`
	Frozen     bool                 `json:"frozen"`
}

// Fit 只在 discovery research 区按特征分布拟合分桶切点;samples 只含特征值,结构上不含收益。
// OOD 样本被跳过;任一维度可用样本不足则报错(不静默产生无支撑的切点)。
func Fit(partition string, samples []FeatureSet, quantiles []float64) (Cutpoints, error) {
	if partition != FitPartition {
		return Cutpoints{}, fmt.Errorf("sky: cutpoints may only be fit on %q partition, got %q", FitPartition, partition)
	}
	if len(quantiles) == 0 {
		return Cutpoints{}, errors.New("sky: quantiles required")
	}
	for _, q := range quantiles {
		if q <= 0 || q >= 1 {
			return Cutpoints{}, fmt.Errorf("sky: invalid quantile %v", q)
		}
	}
	values := map[string][]float64{}
	var used int
	for _, sample := range samples {
		if sample.OOD {
			continue
		}
		used++
		for _, dim := range Dimensions {
			if v, ok := sample.Values[dim]; ok {
				values[dim] = append(values[dim], v)
			}
		}
	}
	cuts := map[string][]float64{}
	for _, dim := range Dimensions {
		series := values[dim]
		if len(series) < len(quantiles)+1 {
			return Cutpoints{}, fmt.Errorf("sky: dimension %q has %d in-support samples, need >= %d", dim, len(series), len(quantiles)+1)
		}
		cuts[dim] = quantileCuts(series, quantiles)
	}
	return Cutpoints{
		Partition:  partition,
		Quantiles:  append([]float64(nil), quantiles...),
		Cuts:       cuts,
		FitSamples: used,
	}, nil
}

// Freeze 标记切点为已冻结;冻结后应通过 Classify 原样应用到 forward/WF,不得重新 Fit。
func (c Cutpoints) Freeze() Cutpoints {
	c.Frozen = true
	return c
}

// Hash 是切点的可复现哈希,用于证明 forward/WF 用的正是 discovery research 冻结的同一套切点。
func (c Cutpoints) Hash() (string, error) {
	hash, _, err := datatruth.HashJSON(c)
	return hash, err
}

// quantileCuts 用线性插值(numpy 默认 type-7)在排序后的样本上取分位切点。
func quantileCuts(values []float64, quantiles []float64) []float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	cuts := make([]float64, len(quantiles))
	n := len(sorted)
	for i, q := range quantiles {
		pos := q * float64(n-1)
		lo := int(math.Floor(pos))
		frac := pos - float64(lo)
		if lo+1 >= n {
			cuts[i] = sorted[n-1]
			continue
		}
		cuts[i] = sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
	}
	return cuts
}

// SkyState 是大行情标签;只表示行情,不隐含交易方向。
type SkyState struct {
	Label   string         `json:"label"`
	Buckets map[string]int `json:"buckets"`
	OOD     bool           `json:"ood"`
}

// Classify 用冻结切点把特征映射到 SkyState;特征 OOD 或维度无切点即 SkyState OOD。
func Classify(features FeatureSet, cuts Cutpoints) SkyState {
	state := SkyState{Buckets: map[string]int{}}
	if features.OOD || !cuts.Frozen {
		state.OOD = true
		return state
	}
	parts := make([]string, 0, len(Dimensions))
	for _, dim := range Dimensions {
		value, ok := features.Values[dim]
		dimCuts, hasCuts := cuts.Cuts[dim]
		if !ok || !hasCuts || len(dimCuts) == 0 {
			state.OOD = true
			return state
		}
		bucket := bucketIndex(value, dimCuts)
		state.Buckets[dim] = bucket
		parts = append(parts, dimShortCode[dim]+"="+bucketLabel(bucket, len(dimCuts)+1))
	}
	state.Label = strings.Join(parts, "/")
	return state
}

// bucketIndex 返回 value 落入的桶:严格大于第 k 个切点则至少进第 k+1 桶。
func bucketIndex(value float64, cuts []float64) int {
	bucket := 0
	for _, cut := range cuts {
		if value > cut {
			bucket++
		}
	}
	return bucket
}

func bucketLabel(index, nBuckets int) string {
	if nBuckets == len(bucketLabels) && index >= 0 && index < len(bucketLabels) {
		return bucketLabels[index]
	}
	return fmt.Sprintf("b%d", index)
}
