package mechanismpanel

import (
	"context"
	"fmt"
	"sort"
	"time"

	"lingxi/internal/datatruth"
)

// SourcePaths 是六路输入的文件路径;留空表示该流不参与本次运行(仍可正常 join 剩余流)。
type SourcePaths struct {
	Liquidation       string // OKX 强平事件(驱动流)
	XVenueLiquidation string // 跨所强平(确认流)
	MarketMids        string // 全市场 mid 标尺
	XMetaOI           string // xmeta funding/OI 快照
	Depth             string // 盘口深度快照
	InstrumentCatalog string // instrument ctVal 点在时目录(非流式,一次性载入)
}

// ProgressStats 是"进度每N行"回调携带的运行时快照。
type ProgressStats struct {
	LinesProcessed  int64     `json:"lines_processed"`
	FeaturesEmitted int64     `json:"features_emitted"`
	LabelsEmitted   int64     `json:"labels_emitted"`
	Watermark       time.Time `json:"watermark"`
	OpenEpisodes    int       `json:"open_episodes"`
	PendingLabels   int       `json:"pending_labels"`
}

// Config 是整条 pipeline 的运行配置。
type Config struct {
	Sources    SourcePaths
	Aggregator AggregatorConfig

	// Date 限定只处理该 UTC 日期(YYYY-MM-DD)内 event-time 落在 [date,date+1d) 的记录;
	// 空字符串表示不限日期。跳过的记录仍计入 LinesProcessed/checkpoint 偏移量。
	Date string

	// MaxLines>0 时,处理满这么多条(从所有源归并后的)记录即停止,用于抽样/限流跑批。
	MaxLines int64

	// ProgressEveryN>0 时,每处理这么多行调用一次 OnProgress。
	ProgressEveryN int64
	OnProgress     func(ProgressStats)

	// CheckpointPath 非空时,Run 结束(或提前停止)时落盘续读位置;若该文件已存在则先从它恢复。
	CheckpointPath string

	// FinalizeAtEOF 为 true 且归并器天然耗尽(而非因 MaxLines 提前停止)时,强制收口所有
	// 仍开放的 episode 与仍等待中的 label。默认 false:保留状态给未来 resume(适合"文件仍在增长"的场景)。
	FinalizeAtEOF bool

	LateAfter time.Duration
	// MaxDedupeEntries bounds each stream's in-memory/checkpoint duplicate window.
	// Older sequence/hash pairs are discarded deterministically; zero uses the preregistered default.
	MaxDedupeEntries int
	Now              clockFunc
}

// RunResult 汇总一次 Run 的结果。
type RunResult struct {
	Stats        ProgressStats
	ReachedEOF   bool // 归并器自然耗尽(所有源都读到文件末尾),而非因 MaxLines 提前停止
	Checkpointed bool
	Interrupted  bool // ctx 被取消而提前停止(仍会按 CheckpointPath 落盘,支持后续 resume)
}

// SinkFailure 包住 onFeature/onLabel 回调报告的落盘失败,由 FeatureSinkFunc/LabelSinkFunc
// 转换成一次立即终止 Run 的错误(红队#12:写失败立即终止,输出与 offset 推进保持一致)。
// 调用方可用 errors.As 判断"是不是因为 feature/label 落盘失败提前终止的",而不是输入源
// 本身出的错。
type SinkFailure struct {
	Kind string // "feature" 或 "label"
	Err  error
}

func (e *SinkFailure) Error() string {
	return fmt.Sprintf("mechanismpanel: %s sink failed, aborting before checkpoint: %v", e.Kind, e.Err)
}

func (e *SinkFailure) Unwrap() error { return e.Err }

// FeatureSinkFunc 把一个可能失败的 EntryFeatures 落盘函数(例如 JSONLWriter.Write)适配
// 成 NewPipeline 要求的 onFeature 回调形状。一旦 fn 返回错误,Pipeline.Run 会立即终止
// (不会再处理任何后续记录),并且不会落盘 checkpoint——因为 checkpoint 只在 Run 正常走完
// 主循环之后才保存一次,提前 panic-recover 退出这条路径必然跳过那次保存,所以磁盘上的
// checkpoint offset 绝不会超过"已经确认写盘成功"的那一条记录(见 Run 里的 recover)。
func FeatureSinkFunc(fn func(EntryFeatures) error) func(EntryFeatures) {
	return func(f EntryFeatures) {
		if err := fn(f); err != nil {
			panic(&SinkFailure{Kind: "feature", Err: err})
		}
	}
}

// LabelSinkFunc 是 FeatureSinkFunc 的 FutureLabel 版本。
func LabelSinkFunc(fn func(FutureLabel) error) func(FutureLabel) {
	return func(l FutureLabel) {
		if err := fn(l); err != nil {
			panic(&SinkFailure{Kind: "label", Err: err})
		}
	}
}

// Pipeline 把 Source + Merger + datatruth 质量评估 + Aggregator + Checkpoint 串起来。
type Pipeline struct {
	cfg           Config
	sources       []Source
	merger        *Merger
	agg           *Aggregator
	streamStates  map[string]datatruth.StreamState
	resumeOffsets map[string]int64
	dateFilter    *dateWindow
	now           clockFunc

	stats ProgressStats
}

type dateWindow struct{ start, end time.Time }

func parseDateFilter(date string) (*dateWindow, error) {
	if date == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, fmt.Errorf("mechanismpanel: bad date %q (want YYYY-MM-DD): %w", date, err)
	}
	d = d.UTC()
	return &dateWindow{start: d, end: d.Add(24 * time.Hour)}, nil
}

func (w *dateWindow) contains(t time.Time) bool {
	if w == nil {
		return true
	}
	t = t.UTC()
	return !t.Before(w.start) && t.Before(w.end)
}

func streamKeyForKind(k Kind) string {
	switch k {
	case KindLiquidation:
		return "okx_liquidation"
	case KindXVenueLiquidation:
		return "xvenue_liquidation"
	case KindMarketMids:
		return "market_mids"
	case KindXMetaOI:
		return "xmeta_oi"
	case KindDepth:
		return "depth"
	default:
		return string(k)
	}
}

type sourceCtor func(path string, skipLines int64, now clockFunc) (Source, error)

func buildSources(paths SourcePaths, offsets map[string]int64, now clockFunc) ([]Source, error) {
	var out []Source
	add := func(name string, ctor sourceCtor, path string) error {
		if path == "" {
			return nil
		}
		src, err := ctor(path, offsets[name], now)
		if err != nil {
			return err
		}
		out = append(out, src)
		return nil
	}
	if err := add("okx_liquidation", NewLiquidationSource, paths.Liquidation); err != nil {
		return nil, err
	}
	if err := add("xvenue_liquidation", NewXVenueLiquidationSource, paths.XVenueLiquidation); err != nil {
		return nil, err
	}
	if err := add("market_mids", NewMarketMidsSource, paths.MarketMids); err != nil {
		return nil, err
	}
	if err := add("xmeta_oi", NewXMetaOISource, paths.XMetaOI); err != nil {
		return nil, err
	}
	if err := add("depth", NewDepthSource, paths.Depth); err != nil {
		return nil, err
	}
	return out, nil
}

// NewPipeline 构造一条可运行的 pipeline。若 cfg.CheckpointPath 指向一个已存在的 checkpoint,
// 自动从中恢复读取位置、去重状态与 episode/label 工作态,实现 resume。
func NewPipeline(cfg Config, onFeature func(EntryFeatures), onLabel func(FutureLabel)) (*Pipeline, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxDedupeEntries <= 0 {
		cfg.MaxDedupeEntries = UnifiedMaxDedupeEntries
	}
	if onFeature == nil {
		onFeature = func(EntryFeatures) {}
	}
	if onLabel == nil {
		onLabel = func(FutureLabel) {}
	}
	dw, err := parseDateFilter(cfg.Date)
	if err != nil {
		return nil, err
	}

	var checkpoint Checkpoint
	haveCheckpoint := false
	if cfg.CheckpointPath != "" {
		cp, ok, err := LoadCheckpoint(cfg.CheckpointPath)
		if err != nil {
			return nil, err
		}
		if ok {
			// 红队#12:恢复前必须校验输入文件身份与完整配置身份,任何不一致 fail closed——
			// 拒绝构造 Pipeline,而不是悄悄带着错位的 offset/config 继续跑,也不是悄悄清零
			// 从头开始(那会让已经写出的 features/labels 文件被重复 append)。
			if err := VerifyCheckpointIdentity(cfg, cp); err != nil {
				return nil, fmt.Errorf("mechanismpanel: refusing to resume from checkpoint %s: %w", cfg.CheckpointPath, err)
			}
		}
		checkpoint, haveCheckpoint = cp, ok
	}

	offsets := map[string]int64{}
	streamStates := map[string]datatruth.StreamState{}
	if haveCheckpoint {
		if checkpoint.SourceOffsets != nil {
			offsets = checkpoint.SourceOffsets
		}
		if checkpoint.StreamStates != nil {
			streamStates = checkpoint.StreamStates
		}
	}

	sources, err := buildSources(cfg.Sources, offsets, cfg.Now)
	if err != nil {
		return nil, err
	}

	if cfg.Sources.InstrumentCatalog != "" {
		cat, err := LoadInstrumentCatalog(cfg.Sources.InstrumentCatalog)
		if err != nil {
			return nil, err
		}
		cfg.Aggregator.InstrumentCatalog = cat
	}
	cfg.Aggregator.Now = cfg.Now

	p := &Pipeline{
		cfg: cfg, sources: sources, streamStates: streamStates,
		resumeOffsets: offsets, dateFilter: dw, now: cfg.Now,
	}

	wrappedFeature := func(f EntryFeatures) {
		p.stats.FeaturesEmitted++
		onFeature(f)
	}
	wrappedLabel := func(l FutureLabel) {
		p.stats.LabelsEmitted++
		onLabel(l)
	}

	if haveCheckpoint {
		p.agg = RestoreAggregator(cfg.Aggregator, checkpoint.Aggregator, wrappedFeature, wrappedLabel)
		p.stats.LinesProcessed = checkpoint.LinesProcessed
		p.stats.FeaturesEmitted = checkpoint.FeaturesEmitted
		p.stats.LabelsEmitted = checkpoint.LabelsEmitted
	} else {
		p.agg = NewAggregator(cfg.Aggregator, wrappedFeature, wrappedLabel)
	}

	merger, err := NewMerger(sources)
	if err != nil {
		return nil, err
	}
	p.merger = merger
	return p, nil
}

// Close 释放所有底层文件句柄。
func (p *Pipeline) Close() error {
	var firstErr error
	for _, s := range p.sources {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Run 消费归并流直至耗尽、达到 MaxLines 或 ctx 被取消,期间按 ProgressEveryN 汇报进度,
// 停止时(若配置了 CheckpointPath)落盘续读状态。
//
// 写失败立即终止(红队#12):如果 onFeature/onLabel 是用 FeatureSinkFunc/LabelSinkFunc
// 包出来的,落盘失败会以 *SinkFailure 的形式 panic;下面的 recover 把它转换成一次正常
// 返回的错误,并且直接跳过 saveCheckpoint——因为 saveCheckpoint 只在主循环正常走完之后
// 才被调用一次,这条 panic-recover 路径必然绕开它,所以磁盘上的 checkpoint 永远不会记录
// 一条"输出没有确认落盘"的 offset。非 *SinkFailure 的 panic 会原样重新抛出。
func (p *Pipeline) Run(ctx context.Context) (result RunResult, runErr error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		sf, ok := r.(*SinkFailure)
		if !ok {
			panic(r)
		}
		result, runErr = p.result(false), sf
	}()

	reachedEOF := false
	interrupted := false
loop:
	for {
		select {
		case <-ctx.Done():
			// 中断也要走到下面的落盘路径(红队#12:支持中断恢复),而不是直接丢弃已经
			// 处理过的进度——效果上与"提前达到 MaxLines"完全一致,只是原因不同。
			interrupted = true
			break loop
		default:
		}

		rec, ok, err := p.merger.Next()
		if err != nil {
			return p.result(false), err
		}
		if !ok {
			reachedEOF = true
			break loop
		}

		key := streamKeyForKind(rec.Kind)
		nextState, assessment := datatruth.Observe(p.streamStates[key], rec.Envelope, datatruth.ObserveOptions{LateAfter: p.cfg.LateAfter})
		nextState = compactStreamState(nextState, p.cfg.MaxDedupeEntries)
		p.streamStates[key] = nextState
		rec.Envelope = assessment.Event

		p.stats.LinesProcessed++
		p.reportProgress(false)

		if assessment.Accepted && p.dateFilter.contains(rec.EventTime()) {
			if err := p.agg.Ingest(rec, assessment); err != nil {
				return p.result(false), err
			}
		}

		if p.cfg.MaxLines > 0 && p.stats.LinesProcessed >= p.cfg.MaxLines {
			break loop
		}
	}

	if reachedEOF && p.cfg.FinalizeAtEOF {
		p.agg.Flush()
	}
	p.reportProgress(true)

	checkpointed := false
	if p.cfg.CheckpointPath != "" {
		if err := p.saveCheckpoint(); err != nil {
			return p.result(reachedEOF), err
		}
		checkpointed = true
	}
	res := p.result(reachedEOF)
	res.Checkpointed = checkpointed
	res.Interrupted = interrupted
	if interrupted {
		return res, ctx.Err()
	}
	return res, nil
}

func compactStreamState(state datatruth.StreamState, maxEntries int) datatruth.StreamState {
	if maxEntries <= 0 || len(state.SeenSequences) <= maxEntries {
		return state
	}
	sequences := make([]int64, 0, len(state.SeenSequences))
	for seq := range state.SeenSequences {
		sequences = append(sequences, seq)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for _, seq := range sequences[:len(sequences)-maxEntries] {
		hash := state.SeenSequences[seq]
		delete(state.SeenSequences, seq)
		delete(state.SeenHashes, hash)
	}
	return state
}

func (p *Pipeline) result(reachedEOF bool) RunResult {
	return RunResult{Stats: p.currentStats(), ReachedEOF: reachedEOF}
}

func (p *Pipeline) currentStats() ProgressStats {
	s := p.stats
	s.Watermark = p.agg.watermark
	s.OpenEpisodes = len(p.agg.open)
	s.PendingLabels = len(p.agg.pending)
	return s
}

func (p *Pipeline) reportProgress(force bool) {
	if p.cfg.OnProgress == nil {
		return
	}
	if force || (p.cfg.ProgressEveryN > 0 && p.stats.LinesProcessed%p.cfg.ProgressEveryN == 0) {
		p.cfg.OnProgress(p.currentStats())
	}
}

func (p *Pipeline) saveCheckpoint() error {
	// Merger 会为每个源预取一行；只能保存真正吐出并处理过的行数，
	// 否则 resume 会跳过“已读但未处理”的预取记录。
	emitted := p.merger.EmittedCounts()
	offsets := make(map[string]int64, len(emitted))
	for name, count := range emitted {
		offsets[name] = p.resumeOffsets[name] + count
	}
	// 红队#12:把这一刻的输入文件身份与完整配置身份一起绑进 checkpoint,下次 resume 前
	// 用 VerifyCheckpointIdentity 校验;只读文件头部,不重新扫描整份输入。
	identities, err := computeInputIdentities(p.cfg.Sources)
	if err != nil {
		return err
	}
	fingerprint := newConfigFingerprint(p.cfg)
	cp := Checkpoint{
		SourceOffsets:     offsets,
		StreamStates:      p.streamStates,
		LinesProcessed:    p.stats.LinesProcessed,
		FeaturesEmitted:   p.stats.FeaturesEmitted,
		LabelsEmitted:     p.stats.LabelsEmitted,
		Aggregator:        p.agg.Snapshot(),
		InputIdentities:   identities,
		ConfigFingerprint: fingerprint,
		ConfigHash:        fingerprint.hash(),
		SavedAt:           p.now(),
	}
	return SaveCheckpoint(p.cfg.CheckpointPath, cp)
}
