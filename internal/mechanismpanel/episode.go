package mechanismpanel

import (
	"fmt"
	"math"
	"sort"
	"time"

	"lingxi/internal/datatruth"
)

// ===== bounded 内存的小点位类型(既是内部工作态,也是 checkpoint 的落盘态;字段导出以便 JSON 序列化)。

type timedPoint struct {
	T time.Time `json:"t"`
	R time.Time `json:"r"`
	V float64   `json:"v"`
}

type timedNotional struct {
	T        time.Time `json:"t"`
	R        time.Time `json:"r"`
	Venue    string    `json:"venue"`
	PosSide  string    `json:"pos_side"`
	Notional float64   `json:"notional"`
}

type timedOI struct {
	T     time.Time `json:"t"`
	R     time.Time `json:"r"`
	Venue string    `json:"venue"`
	OI    float64   `json:"oi"`
}

// phaseState 记录某一个候选 phase(relay 或 exhaustion)在其所属原始 episode 内的检测状态。
// 只允许从 false->true 单向翻转一次(每个原始 episode 每个 phase 最多产出一次 EntryFeatures/
// FutureLabel),这正是"两个 phase 独立 ID/独立锚点"的运行时强制点。
type phaseState struct {
	Detected   bool      `json:"detected"`
	AnchorTime time.Time `json:"anchor_time,omitempty"` // = DecisionTime,一旦 Detected 便不再改变
}

// openEpisode 是一个仍在收集驱动/上下文事件的 episode 的完整工作态。
// 所有切片都受 AggregatorConfig.MaxContextBuffer 硬顶,保证内存不随源文件大小增长。
type openEpisode struct {
	ID              string    `json:"id"`
	Instrument      string    `json:"instrument"`
	StartTime       time.Time `json:"start_time"`
	LastForcingTime time.Time `json:"last_forcing_time"`
	LastTouch       time.Time `json:"last_touch"`

	ForcingEvents []timedNotional `json:"forcing_events"` // OKX 驱动流
	XVenueEvents  []timedNotional `json:"xvenue_events"`  // 跨所确认流
	Mids          []timedPoint    `json:"mids"`
	OI            []timedOI       `json:"oi"`
	Depth         []DepthRecord   `json:"depth"`

	StartMid   float64            `json:"start_mid,omitempty"`
	StartOI    map[string]float64 `json:"start_oi,omitempty"`
	StartDepth DepthRecord        `json:"start_depth"`

	OutOfOrder   int                     `json:"out_of_order"`
	Marks        []datatruth.QualityMark `json:"marks,omitempty"`
	MissingCtVal bool                    `json:"missing_ctval,omitempty"`

	// RelayPhase/ExhaustionPhase 是本包修复的核心泄漏点的运行时状态:同一 episode 绝不能
	// 用"结束时"的单一特征快照同时检验 relay 与 exhaustion——每个 phase 各自独立检测、独立
	// 锚点、最多各触发一次。见 computeFeaturesAsOf/maybeDetectRelay/maybeDetectExhaustion。
	RelayPhase      phaseState `json:"relay_phase"`
	ExhaustionPhase phaseState `json:"exhaustion_phase"`
}

// AggregatorConfig 控制 episode 聚合、双候选 phase 检测与 bounded-memory 行为。
//
// 以下字段与 research/firstbattle/forced_liquidation_preregistration.json 的
// episode/feature_time_truncation/phases 字段一一对应,数值必须保持一致
// (见 preregistration_test.go 的漂移检测)。
type AggregatorConfig struct {
	// GapThreshold = manifest episode.clustering.merge_gap_ms:
	//   (a) 同簇后续强平事件与上一事件的间隔超过此值则开新 episode(不合并);
	//   (b) exhaustion phase 的"gap 确认"信号——最后一个强平事件之后经过此间隔仍无新事件,
	//       即视为被迫流枯竭,可以评估 exhaustion 候选。
	GapThreshold time.Duration
	// MaxEpisodeDuration = manifest episode.max_episode_duration_ms。
	MaxEpisodeDuration time.Duration
	// MinTriggerNotionalUSDT = manifest episode.min_trigger_notional_usdt("冻结门"):
	// relay phase 要求累计被迫名义(全部venue)在触发时刻已达到此阈值。
	MinTriggerNotionalUSDT float64
	// FeatureLagMs = manifest feature_time_truncation.feature_lag_ms:两个 phase 的
	// DecisionTime 都等于其原始信号时刻(t_signal)加上这段固定延迟。
	FeatureLagMs int64
	// ExhaustionAbsorptionMin/ExhaustionMaxRemainingRatio = manifest 中
	// exhaustion_absorption_min / exhaustion_max_remaining_ratio(即 md 第1节
	// panic_liquidation 判据的 AbsorptionRatio>=0.60 与 Remaining<=0.20×Observed)。
	ExhaustionAbsorptionMin     float64
	ExhaustionMaxRemainingRatio float64

	MaxOpenEpisodes   int
	MaxPendingLabels  int
	MaxContextBuffer  int
	LabelHorizons     []time.Duration
	MaxLabelStaleness time.Duration
	InstrumentCatalog datatruth.InstrumentCatalog
	Now               clockFunc
}

// DefaultAggregatorConfig 返回与预注册 manifest 数值统一的默认阈值。
func DefaultAggregatorConfig() AggregatorConfig {
	return AggregatorConfig{
		GapThreshold:                time.Duration(UnifiedMergeGapMs) * time.Millisecond,
		MaxEpisodeDuration:          time.Duration(UnifiedMaxEpisodeDurationMs) * time.Millisecond,
		MinTriggerNotionalUSDT:      UnifiedMinTriggerNotionalUSDT,
		FeatureLagMs:                UnifiedFeatureLagMs,
		ExhaustionAbsorptionMin:     UnifiedExhaustionAbsorptionMin,
		ExhaustionMaxRemainingRatio: UnifiedExhaustionMaxRemainingRatio,
		MaxOpenEpisodes:             64,
		MaxPendingLabels:            128,
		MaxContextBuffer:            4096,
		LabelHorizons:               []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 300 * time.Second},
		MaxLabelStaleness:           time.Duration(UnifiedMaxLabelStalenessMs) * time.Millisecond,
	}
}

// Aggregator 是 join 框架的核心:消费按 event-time 排序的 Record,按 instrument 分组聚成
// episode,收口时输出 EntryFeatures,并把决策时刻之后的数据物理隔离进 pendingLabel 队列,
// 待前瞻窗口满足后输出 FutureLabel。
type Aggregator struct {
	cfg AggregatorConfig
	now clockFunc

	open    map[string]*openEpisode
	pending []*pendingLabel

	latestMid   map[string]timedPoint
	latestOI    map[string]map[string]timedPoint
	latestDepth map[string]DepthRecord

	watermark  time.Time
	episodeSeq int64

	onFeature func(EntryFeatures)
	onLabel   func(FutureLabel)
}

// NewAggregator 构造一个空白 Aggregator。onFeature/onLabel 在对应记录收口/完成时被同步调用,
// 调用方负责落盘(两个互不重叠的输出流,物理分离的第二重保证)。
func NewAggregator(cfg AggregatorConfig, onFeature func(EntryFeatures), onLabel func(FutureLabel)) *Aggregator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.LabelHorizons) == 0 {
		cfg = DefaultAggregatorConfig()
	}
	if onFeature == nil {
		onFeature = func(EntryFeatures) {}
	}
	if onLabel == nil {
		onLabel = func(FutureLabel) {}
	}
	return &Aggregator{
		cfg:         cfg,
		now:         cfg.Now,
		open:        map[string]*openEpisode{},
		latestMid:   map[string]timedPoint{},
		latestOI:    map[string]map[string]timedPoint{},
		latestDepth: map[string]DepthRecord{},
		onFeature:   onFeature,
		onLabel:     onLabel,
	}
}

// Ingest 消费一条已经过质量评估(三时钟/去重/乱序/缺口标记,见 datatruth.Observe)的记录。
// assessment.Accepted=false(重复)时按约定不应被调用;调用方(pipeline.go)负责过滤。
func (a *Aggregator) Ingest(rec Record, assessment datatruth.EventAssessment) error {
	t := rec.EventTime()
	if t.After(a.watermark) {
		a.watermark = t
	}
	switch rec.Kind {
	case KindLiquidation:
		if err := a.ingestLiquidation(rec, assessment); err != nil {
			return err
		}
	case KindXVenueLiquidation:
		a.ingestXVenue(rec, assessment)
	case KindMarketMids:
		a.ingestMids(rec, assessment)
	case KindXMetaOI:
		a.ingestOI(rec, assessment)
	case KindDepth:
		a.ingestDepth(rec, assessment)
	default:
		return fmt.Errorf("mechanismpanel: unknown record kind %q", rec.Kind)
	}
	a.housekeep()
	return nil
}

// Flush 强制收口所有仍开放的 episode、强制完成所有等待中的 label。
// 仅应在确认不会再有更多数据到来时调用(例如一次性按日期跑批的末尾)。
func (a *Aggregator) Flush() {
	instruments := make([]string, 0, len(a.open))
	for k := range a.open {
		instruments = append(instruments, k)
	}
	sort.Strings(instruments)
	for _, instrument := range instruments {
		ep := a.open[instrument]
		delete(a.open, instrument)
		// 流已耗尽:此后绝不会再有新的强平事件,这比"等够 GapThreshold"更强的沉默确认,
		// 因此可以安全地视为 gap 已确认,评估 exhaustion 候选。
		a.closeEpisode(ep, true, false, a.watermark)
	}
	for _, pl := range a.pending {
		a.emitLabel(pl)
	}
	a.pending = nil
}

func (a *Aggregator) ingestLiquidation(rec Record, assess datatruth.EventAssessment) error {
	lr := rec.Liquidation
	t := rec.EventTime()
	instrument := lr.Symbol
	ep := a.open[instrument]
	if ep != nil {
		gapElapsed := t.Sub(ep.LastForcingTime) > a.cfg.GapThreshold
		durationElapsed := t.Sub(ep.StartTime) > a.cfg.MaxEpisodeDuration
		if gapElapsed || durationElapsed {
			delete(a.open, instrument)
			// gap 收口(gapElapsed)才是"gap 确认",可以评估 exhaustion 候选;纯粹超过
			// MaxEpisodeDuration 的截断不代表被迫流已枯竭,不构成 exhaustion 的 gap 确认。
			a.closeEpisode(ep, gapElapsed, false, t)
			ep = nil
		}
	}
	if ep == nil {
		ep = a.openNewEpisode(instrument, t)
	}
	ctVal, found := CtValAt(a.cfg.InstrumentCatalog, instrument, t, t)
	if !found {
		ep.MissingCtVal = true
		ctVal = 1
	}
	notional := lr.Qty * ctVal * lr.BkPx
	ep.ForcingEvents = append(ep.ForcingEvents, timedNotional{T: t, R: rec.Envelope.ReceiveTime, Venue: "okx", PosSide: lr.PosSide, Notional: notional})
	ep.ForcingEvents = capSlice(ep.ForcingEvents, a.cfg.MaxContextBuffer)
	ep.LastForcingTime = t
	ep.LastTouch = t
	a.applyQuality(ep, assess)
	a.maybeDetectRelay(ep, t, false)
	a.enforceOpenCapacity()
	return nil
}

func (a *Aggregator) ingestXVenue(rec Record, assess datatruth.EventAssessment) {
	xv := rec.XVenue
	t := rec.EventTime()
	ep, ok := a.open[xv.Symbol]
	if !ok {
		return
	}
	notional := xv.Qty * xv.Price
	ep.XVenueEvents = append(ep.XVenueEvents, timedNotional{T: t, R: rec.Envelope.ReceiveTime, Venue: xv.Venue, PosSide: xv.PosSide, Notional: notional})
	ep.XVenueEvents = capSlice(ep.XVenueEvents, a.cfg.MaxContextBuffer)
	ep.LastTouch = t
	a.applyQuality(ep, assess)
	a.maybeDetectRelay(ep, t, false)
}

func (a *Aggregator) ingestMids(rec Record, assess datatruth.EventAssessment) {
	t := rec.EventTime()
	for sym, mid := range rec.Mids.Mids {
		a.latestMid[sym] = timedPoint{T: t, R: rec.Envelope.ReceiveTime, V: mid}
		if ep, ok := a.open[sym]; ok {
			ep.Mids = append(ep.Mids, timedPoint{T: t, R: rec.Envelope.ReceiveTime, V: mid})
			ep.Mids = capSlice(ep.Mids, a.cfg.MaxContextBuffer)
			ep.LastTouch = t
			a.applyQuality(ep, assess)
		}
		for _, pl := range a.pending {
			if pl.instrument == sym {
				pl.absorbFuture(t, mid)
			}
		}
	}
}

func (a *Aggregator) ingestOI(rec Record, assess datatruth.EventAssessment) {
	t := rec.EventTime()
	for venue, symMap := range rec.OI.Venues {
		for sym, arr := range symMap {
			if len(arr) < 2 {
				continue
			}
			oi := arr[1]
			if a.latestOI[sym] == nil {
				a.latestOI[sym] = map[string]timedPoint{}
			}
			a.latestOI[sym][venue] = timedPoint{T: t, R: rec.Envelope.ReceiveTime, V: oi}
			if ep, ok := a.open[sym]; ok {
				ep.OI = append(ep.OI, timedOI{T: t, R: rec.Envelope.ReceiveTime, Venue: venue, OI: oi})
				ep.OI = capSlice(ep.OI, a.cfg.MaxContextBuffer)
				ep.LastTouch = t
				a.applyQuality(ep, assess)
			}
		}
	}
}

func (a *Aggregator) ingestDepth(rec Record, assess datatruth.EventAssessment) {
	d := *rec.Depth
	d.ReceiveTime = rec.Envelope.ReceiveTime
	a.latestDepth[d.Symbol] = d
	if ep, ok := a.open[d.Symbol]; ok {
		ep.Depth = append(ep.Depth, d)
		ep.Depth = capSlice(ep.Depth, a.cfg.MaxContextBuffer)
		ep.LastTouch = d.RecordedAt
		a.applyQuality(ep, assess)
	}
}

func (a *Aggregator) openNewEpisode(instrument string, t time.Time) *openEpisode {
	a.episodeSeq++
	ep := &openEpisode{
		ID:              fmt.Sprintf("%s-%d-%d", instrument, t.UnixMilli(), a.episodeSeq),
		Instrument:      instrument,
		StartTime:       t,
		LastForcingTime: t,
		LastTouch:       t,
	}
	if v, ok := a.latestMid[instrument]; ok && knownBy(v.R, t) {
		ep.StartMid = v.V
	}
	if m, ok := a.latestOI[instrument]; ok {
		ep.StartOI = make(map[string]float64, len(m))
		for venue, tv := range m {
			if knownBy(tv.R, t) {
				ep.StartOI[venue] = tv.V
			}
		}
	}
	if d, ok := a.latestDepth[instrument]; ok && knownBy(d.ReceiveTime, t) {
		ep.StartDepth = d
	}
	a.open[instrument] = ep
	return ep
}

func (a *Aggregator) applyQuality(ep *openEpisode, assess datatruth.EventAssessment) {
	ep.Marks = append(ep.Marks, assess.Quality...)
	if assess.OutOfOrder {
		ep.OutOfOrder++
	}
}

// enforceOpenCapacity 保证同时打开的 episode 数不超过 MaxOpenEpisodes(bounded memory)。
// 超限时强制收口最久未更新的一个,标记 EvictedCapacity。
func (a *Aggregator) enforceOpenCapacity() {
	if a.cfg.MaxOpenEpisodes <= 0 || len(a.open) <= a.cfg.MaxOpenEpisodes {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, ep := range a.open {
		if oldestKey == "" || ep.LastTouch.Before(oldest) {
			oldestKey, oldest = k, ep.LastTouch
		}
	}
	if oldestKey == "" {
		return
	}
	ep := a.open[oldestKey]
	delete(a.open, oldestKey)
	a.closeEpisode(ep, false, true, a.watermark)
}

// housekeep 基于全局 watermark(已见到的最大 event-time)推进 episode 的 gap/超时收口与
// pending label 的完成/超时检查,即使某个 instrument 之后再没有新事件也不会无限占内存。
func (a *Aggregator) housekeep() {
	for instrument, ep := range a.open {
		gapElapsed := a.watermark.Sub(ep.LastForcingTime) > a.cfg.GapThreshold
		durationElapsed := a.watermark.Sub(ep.StartTime) > a.cfg.MaxEpisodeDuration
		if gapElapsed || durationElapsed {
			delete(a.open, instrument)
			a.closeEpisode(ep, gapElapsed, false, a.watermark)
		}
	}
	if len(a.pending) == 0 {
		return
	}
	maxHorizon := maxDuration(a.cfg.LabelHorizons)
	remaining := a.pending[:0]
	for _, pl := range a.pending {
		if pl.isDone() || a.watermark.Sub(pl.decisionTime) > maxHorizon {
			a.emitLabel(pl)
			continue
		}
		remaining = append(remaining, pl)
	}
	a.pending = remaining
	a.enforcePendingCapacity()
}

func (a *Aggregator) enforcePendingCapacity() {
	for a.cfg.MaxPendingLabels > 0 && len(a.pending) > a.cfg.MaxPendingLabels {
		oldest := 0
		for i, pl := range a.pending {
			if pl.decisionTime.Before(a.pending[oldest].decisionTime) {
				oldest = i
			}
		}
		pl := a.pending[oldest]
		a.pending = append(a.pending[:oldest], a.pending[oldest+1:]...)
		a.emitLabel(pl)
	}
}

func (a *Aggregator) emitLabel(pl *pendingLabel) {
	a.onLabel(pl.finalize())
}

// closeEpisode 在原始 episode 收口时做两件事:
//  1. 以最后一个强平时刻尝试补发 relay(若先前未触发);
//  2. 仅当 gapConfirmed=true 时评估 exhaustion(避免把 duration/evicted 误当"枯竭确认")。
func (a *Aggregator) closeEpisode(ep *openEpisode, gapConfirmed bool, evictedCapacity bool, closeTime time.Time) {
	if closeTime.IsZero() {
		closeTime = a.watermark
	}
	if closeTime.IsZero() {
		closeTime = ep.LastForcingTime
	}
	a.maybeDetectRelay(ep, ep.LastForcingTime, evictedCapacity)
	if gapConfirmed {
		a.maybeDetectExhaustion(ep, closeTime, evictedCapacity)
	}
}

func capSlice[T any](s []T, max int) []T {
	if max <= 0 || len(s) <= max {
		return s
	}
	return append([]T(nil), s[len(s)-max:]...)
}

func maxDuration(ds []time.Duration) time.Duration {
	var m time.Duration
	for _, d := range ds {
		if d > m {
			m = d
		}
	}
	return m
}

func (a *Aggregator) maybeDetectRelay(ep *openEpisode, asOf time.Time, evictedCapacity bool) {
	if ep.RelayPhase.Detected || asOf.IsZero() {
		return
	}
	// relay 只允许锚在"OKX 强平事件时刻"本身,避免被后续 xvenue/mid 事件回填触发。
	if !asOf.Equal(ep.LastForcingTime) {
		return
	}
	features, decisionMid := computeFeaturesAsOf(ep, a.cfg, PhaseRelay, asOf, evictedCapacity, a.now())
	if features.ForcedFlow.EventCount == 0 {
		return
	}
	if features.ForcedFlow.TotalNotional < a.cfg.MinTriggerNotionalUSDT && !evictedCapacity {
		return
	}
	if !features.Acceleration.Accelerating && !evictedCapacity {
		return
	}
	a.emitPhase(ep, features, decisionMid)
}

func (a *Aggregator) maybeDetectExhaustion(ep *openEpisode, asOf time.Time, evictedCapacity bool) {
	if ep.ExhaustionPhase.Detected || asOf.IsZero() {
		return
	}
	features, decisionMid := computeFeaturesAsOf(ep, a.cfg, PhaseExhaustion, asOf, evictedCapacity, a.now())
	if features.ForcedFlow.TotalNotional <= 0 || !features.Absorption.Observed {
		return
	}
	if features.Acceleration.Accelerating {
		return
	}
	lastNotional := latestForcingNotional(filteredTimedNotionalAtOrBefore(ep.ForcingEvents, asOf))
	if lastNotional <= 0 {
		return
	}
	remainingRatio := lastNotional / features.ForcedFlow.TotalNotional
	if remainingRatio > a.cfg.ExhaustionMaxRemainingRatio {
		return
	}
	if features.Absorption.AbsorptionRatio < a.cfg.ExhaustionAbsorptionMin {
		return
	}
	a.emitPhase(ep, features, decisionMid)
}

func (a *Aggregator) emitPhase(ep *openEpisode, features EntryFeatures, decisionMid float64) {
	if !a.markPhaseDetected(ep, features.Episode.Phase, features.Episode.DecisionTime) {
		return
	}
	a.onFeature(features)
	pl := newPendingLabelForPhase(
		features.Episode.EpisodeID,
		features.Episode.RawEpisodeID,
		features.Episode.Phase,
		ep.Instrument,
		features.Episode.DecisionTime,
		decisionMid,
		a.cfg.LabelHorizons,
	)
	pl.maxStalenessMs = a.cfg.MaxLabelStaleness.Milliseconds()
	for _, p := range sortedTimedPoints(ep.Mids) {
		if p.T.After(features.Episode.DecisionTime) {
			pl.absorbFuture(p.T, p.V)
		}
	}
	if pl.isDone() {
		a.emitLabel(pl)
		return
	}
	a.pending = append(a.pending, pl)
	a.enforcePendingCapacity()
}

func (a *Aggregator) markPhaseDetected(ep *openEpisode, phase PhaseKind, decisionTime time.Time) bool {
	switch phase {
	case PhaseRelay:
		if ep.RelayPhase.Detected {
			return false
		}
		ep.RelayPhase.Detected = true
		ep.RelayPhase.AnchorTime = decisionTime
		return true
	case PhaseExhaustion:
		if ep.ExhaustionPhase.Detected {
			return false
		}
		ep.ExhaustionPhase.Detected = true
		ep.ExhaustionPhase.AnchorTime = decisionTime
		return true
	default:
		return false
	}
}

// computeFeaturesAsOf 是纯函数:只读 ep 中 T<=AsOfTime 的数据,产出单个 phase 的 EntryFeatures
// 以及该 phase 的 decisionMid(标签参考价)。
func computeFeaturesAsOf(ep *openEpisode, cfg AggregatorConfig, phase PhaseKind, asOf time.Time, evictedCapacity bool, processTime time.Time) (EntryFeatures, float64) {
	forcingWithin := filteredTimedNotionalAtOrBefore(ep.ForcingEvents, asOf)
	xvenueWithin := filteredTimedNotionalAtOrBefore(ep.XVenueEvents, asOf)
	midsWithin := filteredTimedPointsAtOrBefore(ep.Mids, asOf)
	oiWithin := filteredTimedOIAtOrBefore(ep.OI, asOf)
	depthWithin := filteredDepthAtOrBefore(ep.Depth, asOf)

	venueNotional := map[string]float64{}
	var longSum, shortSum float64
	count := 0
	for _, fe := range forcingWithin {
		venueNotional["okx"] += fe.Notional
		switch fe.PosSide {
		case "long":
			longSum += fe.Notional
		case "short":
			shortSum += fe.Notional
		}
		count++
	}
	for _, xv := range xvenueWithin {
		venueNotional[xv.Venue] += xv.Notional
		count++
	}
	var total float64
	for _, v := range venueNotional {
		total += v
	}
	dominant := "mixed"
	switch {
	case longSum > shortSum*1.05:
		dominant = "long_squeezed"
	case shortSum > longSum*1.05:
		dominant = "short_squeezed"
	}
	forced := ForcedFlow{VenueNotional: venueNotional, TotalNotional: total, DominantSide: dominant, EventCount: count}

	var accel Acceleration
	if len(forcingWithin) > 0 {
		windowSec := asOf.Sub(ep.StartTime).Seconds()
		midpoint := ep.StartTime.Add(asOf.Sub(ep.StartTime) / 2)
		var first, second float64
		for _, fe := range forcingWithin {
			if fe.T.After(midpoint) {
				second += fe.Notional
			} else {
				first += fe.Notional
			}
		}
		rate := 0.0
		if windowSec > 0 {
			rate = (first + second) / windowSec
		}
		accel = Acceleration{FirstHalfNotional: first, SecondHalfNotional: second, RatePerSecond: rate, Delta: second - first, Accelerating: second > first}
	}

	oiChange := computeOIChange(ep.StartOI, oiWithin)

	dominantPosSide := dominantSideToPosSide(dominant)
	seenVenue := map[string]bool{}
	var confirmedVenues []string
	var lagMs int64
	var xvNotional float64
	firstMatch := true
	for _, xv := range xvenueWithin {
		if dominantPosSide == "" || xv.PosSide != dominantPosSide {
			continue
		}
		xvNotional += xv.Notional
		if firstMatch {
			lagMs = xv.T.Sub(ep.StartTime).Milliseconds()
			firstMatch = false
		}
		if !seenVenue[xv.Venue] {
			seenVenue[xv.Venue] = true
			confirmedVenues = append(confirmedVenues, xv.Venue)
		}
	}
	sort.Strings(confirmedVenues)
	cross := CrossVenueConfirmation{
		Confirmed: len(confirmedVenues) > 0,
		Venues:    confirmedVenues,
		LagMs:     lagMs,
		Notional:  xvNotional,
	}

	var path []PricePathPoint
	lastMid := ep.StartMid
	for _, p := range midsWithin {
		path = append(path, PricePathPoint{OffsetMs: p.T.Sub(ep.StartTime).Milliseconds(), Mid: p.V})
		lastMid = p.V
	}
	absorb := computeAbsorption(ep.StartMid, ep.StartDepth, total, path, lastMid, depthWithin)

	marks := dedupMarks(ep.Marks)
	quality := DataQuality{
		Marks:            marks,
		OutOfOrderInputs: ep.OutOfOrder,
		MissingCtVal:     ep.MissingCtVal,
		MissingOI:        !oiChange.Observed,
		MissingDepth:     !absorb.Observed,
		EvictedCapacity:  evictedCapacity,
	}

	decisionTime := decisionTimeFromAsOf(cfg.FeatureLagMs, asOf)
	meta := EpisodeMeta{
		EpisodeID:    phaseEpisodeID(ep.ID, phase),
		RawEpisodeID: ep.ID,
		Phase:        phase,
		Instrument:   ep.Instrument,
		StartTime:    ep.StartTime,
		AsOfTime:     asOf,
		DecisionTime: decisionTime,
		ProcessTime:  processTime,
	}
	features := EntryFeatures{
		Episode:      meta,
		ForcedFlow:   forced,
		Acceleration: accel,
		OI:           oiChange,
		CrossVenue:   cross,
		Absorption:   absorb,
		PricePath:    path,
		Quality:      quality,
	}
	return features, lastMid
}

func computeOIChange(start map[string]float64, oiWithin []timedOI) OIChange {
	// 选定本 episode 使用的 OI venue:StartOI 中已知的第一个(按字母序,确定性),
	// 若开仓时没有历史快照则用窗口内首次出现的 venue。
	venue := ""
	if len(start) > 0 {
		keys := make([]string, 0, len(start))
		for k := range start {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		venue = keys[0]
	} else if len(oiWithin) > 0 {
		venue = oiWithin[0].Venue
	}
	if venue == "" {
		return OIChange{}
	}
	startOI, haveStart := start[venue]
	endOI := startOI
	haveEnd := haveStart
	for _, o := range oiWithin {
		if o.Venue == venue {
			endOI = o.OI
			haveEnd = true
			if !haveStart {
				startOI = o.OI
				haveStart = true
			}
		}
	}
	if !haveStart || !haveEnd {
		return OIChange{Venue: venue}
	}
	drop := startOI - endOI
	ratio := 0.0
	if startOI != 0 {
		ratio = drop / startOI
	}
	return OIChange{Venue: venue, StartOI: startOI, EndOI: endOI, Drop: drop, DropRatio: ratio, Observed: true}
}

func computeAbsorption(startMid float64, startDepth DepthRecord, totalNotional float64, path []PricePathPoint, lastMid float64, depthWithin []DepthRecord) Absorption {
	entryMid := startMid
	if entryMid <= 0 && len(path) > 0 {
		entryMid = path[0].Mid
	}
	var absorb Absorption
	if entryMid > 0 && lastMid > 0 {
		absorb.PriceMoveBps = (lastMid/entryMid - 1) * 10000
	}
	d := startDepth
	for _, dr := range depthWithin {
		d = dr
	}
	depthNotional := topDepthNotional(d)
	if depthNotional > 0 {
		absorb.TopDepthNotional = depthNotional
		absorb.ExpectedMoveBps = (totalNotional / depthNotional) * 10000
		if absorb.ExpectedMoveBps > 0 {
			absorb.AbsorptionRatio = 1 - math.Min(1, math.Abs(absorb.PriceMoveBps)/absorb.ExpectedMoveBps)
		}
		absorb.Observed = true
	}
	return absorb
}

func topDepthNotional(d DepthRecord) float64 {
	if d.DepthNotional > 0 {
		return d.DepthNotional
	}
	return d.BidPx*d.BidQty + d.AskPx*d.AskQty
}

func phaseEpisodeID(rawEpisodeID string, phase PhaseKind) string {
	return fmt.Sprintf("%s#%s", rawEpisodeID, phase)
}

func decisionTimeFromAsOf(featureLagMs int64, asOf time.Time) time.Time {
	return asOf.Add(time.Duration(featureLagMs) * time.Millisecond)
}

func dominantSideToPosSide(dominant string) string {
	switch dominant {
	case "long_squeezed":
		return "long"
	case "short_squeezed":
		return "short"
	default:
		return ""
	}
}

func latestForcingNotional(forcing []timedNotional) float64 {
	if len(forcing) == 0 {
		return 0
	}
	return forcing[len(forcing)-1].Notional
}

func filteredTimedNotionalAtOrBefore(in []timedNotional, asOf time.Time) []timedNotional {
	if len(in) == 0 {
		return nil
	}
	out := make([]timedNotional, 0, len(in))
	for _, v := range in {
		if !v.T.After(asOf) && knownBy(v.R, asOf) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

func filteredTimedPointsAtOrBefore(in []timedPoint, asOf time.Time) []timedPoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]timedPoint, 0, len(in))
	for _, v := range in {
		if !v.T.After(asOf) && knownBy(v.R, asOf) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

func sortedTimedPoints(in []timedPoint) []timedPoint {
	if len(in) == 0 {
		return nil
	}
	out := append([]timedPoint(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

func filteredTimedOIAtOrBefore(in []timedOI, asOf time.Time) []timedOI {
	if len(in) == 0 {
		return nil
	}
	out := make([]timedOI, 0, len(in))
	for _, v := range in {
		if !v.T.After(asOf) && knownBy(v.R, asOf) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

func filteredDepthAtOrBefore(in []DepthRecord, asOf time.Time) []DepthRecord {
	if len(in) == 0 {
		return nil
	}
	out := make([]DepthRecord, 0, len(in))
	for _, v := range in {
		if !v.RecordedAt.After(asOf) && knownBy(v.ReceiveTime, asOf) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt.Before(out[j].RecordedAt) })
	return out
}

func knownBy(receiveTime, asOf time.Time) bool {
	return !receiveTime.IsZero() && !receiveTime.After(asOf)
}

func dedupMarks(marks []datatruth.QualityMark) []datatruth.QualityMark {
	if len(marks) == 0 {
		return nil
	}
	seen := map[datatruth.QualityMark]bool{}
	var out []datatruth.QualityMark
	for _, m := range marks {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
