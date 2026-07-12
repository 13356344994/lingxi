package mechanismpanel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

// independentEpisodeLiquidationFixture 生成 n 条互相独立的强平事件(各自不同 symbol,
// 彼此间隔足够远),写到 dir/liq.jsonl。只用临时小 fixture,不涉及 mids/xvenue/oi/depth
// 联接。
func independentEpisodeLiquidationFixture(t *testing.T, dir string, n int) string {
	t.Helper()
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		lines = append(lines, fmt.Sprintf(`{"recorded_at":"%s","symbol":"SYM%02d-USDT","pos_side":"long","side":"sell","qty":1,"bk_px":100}`, at.Format(time.RFC3339), i))
	}
	path := filepath.Join(dir, "liq.jsonl")
	writeLines(t, path, lines)
	return path
}

// independentEpisodeAggregatorConfig 搭配 independentEpisodeLiquidationFixture:
// GapThreshold 大到永不自然收口,MaxOpenEpisodes=1 强制每打开第 k+1 个独立 symbol 的
// episode 时,立即以 bounded-memory 淘汰的方式收口第 k 个(见 episode.go
// enforceOpenCapacity)。这条收口路径不经过尚在改造中的 relay/exhaustion phase 检测
// (那条路径目前对本包其它测试也不稳定,见 TestPipelineBoundedMemoryEvictsUnderCapacityPressure
// 的已知 16/20 失败,与本次红队#12 改动无关),因此产出的 onFeature 调用次数是
// n-1、完全确定的,适合用来精确控制"第几次 onFeature 调用失败"。
func independentEpisodeAggregatorConfig() AggregatorConfig {
	cfg := testAggregatorConfig()
	cfg.GapThreshold = time.Hour
	cfg.MaxOpenEpisodes = 1
	return cfg
}

// TestPipelineRunAbortsImmediatelyOnFeatureSinkFailure covers 红队#12"写失败立即终止":
// a FeatureSinkFunc-wrapped sink that fails on the 3rd call must stop Run right there —
// no 4th/5th record gets processed — and Run's returned error must unwrap to the
// *SinkFailure via errors.As.
func TestPipelineRunAbortsImmediatelyOnFeatureSinkFailure(t *testing.T) {
	dir := t.TempDir()
	liq := independentEpisodeLiquidationFixture(t, dir, 5)

	var succeeded int
	failAt := 3
	sink := FeatureSinkFunc(func(EntryFeatures) error {
		succeeded++
		if succeeded == failAt {
			return fmt.Errorf("simulated disk full")
		}
		return nil
	})

	cfg := Config{
		Sources:    SourcePaths{Liquidation: liq},
		Aggregator: independentEpisodeAggregatorConfig(),
	}
	p, err := NewPipeline(cfg, sink, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()

	_, err = p.Run(context.Background())
	if err == nil {
		t.Fatalf("expected Run to return an error when the feature sink fails")
	}
	var sf *SinkFailure
	if !errors.As(err, &sf) {
		t.Fatalf("expected error to unwrap to *SinkFailure, got %v (%T)", err, err)
	}
	if sf.Kind != "feature" {
		t.Fatalf("expected SinkFailure.Kind=feature, got %q", sf.Kind)
	}
	if succeeded != failAt {
		t.Fatalf("expected exactly %d successful sink calls before the failing one, got %d", failAt, succeeded)
	}
}

// TestPipelineRunAbortsImmediatelyOnLabelSinkFailure is the FutureLabel counterpart.
func TestPipelineRunAbortsImmediatelyOnLabelSinkFailure(t *testing.T) {
	var calls int
	sink := LabelSinkFunc(func(FutureLabel) error {
		calls++
		return fmt.Errorf("simulated write error")
	})

	cfg := Config{
		Sources:       testSourcePaths(),
		Aggregator:    testAggregatorConfig(),
		FinalizeAtEOF: true,
	}
	p, err := NewPipeline(cfg, nil, sink)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()

	_, err = p.Run(context.Background())
	if err == nil {
		t.Fatalf("expected Run to return an error when the label sink fails")
	}
	var sf *SinkFailure
	if !errors.As(err, &sf) {
		t.Fatalf("expected error to unwrap to *SinkFailure, got %v (%T)", err, err)
	}
	if sf.Kind != "label" {
		t.Fatalf("expected SinkFailure.Kind=label, got %q", sf.Kind)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 label sink call, got %d", calls)
	}
}

// TestPipelineWriteFailureDoesNotAdvanceCheckpoint covers 红队#12"输出与 offset 推进
// 一致":a run that resumes from a good checkpoint and then hits a sink failure must
// leave the on-disk checkpoint exactly as it was before the failed attempt — the
// failure must not get "silently absorbed" into a newly advanced offset.
func TestPipelineWriteFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	dir := t.TempDir()
	liq := independentEpisodeLiquidationFixture(t, dir, 5)
	cpPath := filepath.Join(dir, "checkpoint.json")

	cfg1 := Config{
		Sources:        SourcePaths{Liquidation: liq},
		Aggregator:     independentEpisodeAggregatorConfig(),
		MaxLines:       3,
		CheckpointPath: cpPath,
	}
	p1, err := NewPipeline(cfg1, nil, nil)
	if err != nil {
		t.Fatalf("NewPipeline part1: %v", err)
	}
	res1, err := p1.Run(context.Background())
	if err != nil {
		t.Fatalf("Run part1: %v", err)
	}
	p1.Close()
	if !res1.Checkpointed {
		t.Fatalf("expected part1 to checkpoint")
	}
	before, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatalf("read checkpoint after part1: %v", err)
	}

	failingSink := FeatureSinkFunc(func(EntryFeatures) error {
		return fmt.Errorf("simulated write error on resume")
	})
	cfg2 := Config{
		Sources:        SourcePaths{Liquidation: liq},
		Aggregator:     independentEpisodeAggregatorConfig(),
		CheckpointPath: cpPath,
	}
	p2, err := NewPipeline(cfg2, failingSink, nil)
	if err != nil {
		t.Fatalf("NewPipeline part2 (resume): %v", err)
	}
	defer p2.Close()
	res2, err := p2.Run(context.Background())
	if err == nil {
		t.Fatalf("expected part2 to fail on the sink error")
	}
	if res2.Checkpointed {
		t.Fatalf("expected part2 to NOT checkpoint after a sink failure")
	}

	after, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatalf("read checkpoint after part2: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("checkpoint on disk must be unchanged after a failed write:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestPipelineContextCancelCheckpointsAndResumeReachesEOFWithoutDuplicates covers
// 红队#12"支持有进度、checkpoint 和中断恢复"以及"context cancel 不得重复 append":
// cancelling ctx mid-run must still checkpoint cleanly at the last fully-processed
// record (Interrupted=true, Checkpointed=true, err wraps context.Canceled), and
// resuming afterwards to completion must reproduce exactly the same features/labels
// as an uninterrupted baseline run — no duplicates, nothing skipped.
func TestPipelineContextCancelCheckpointsAndResumeReachesEOFWithoutDuplicates(t *testing.T) {
	var wantFeatures []EntryFeatures
	var wantLabels []FutureLabel
	base := Config{Sources: testSourcePaths(), Aggregator: testAggregatorConfig(), FinalizeAtEOF: true}
	pb, err := NewPipeline(base, func(f EntryFeatures) { wantFeatures = append(wantFeatures, f) }, func(l FutureLabel) { wantLabels = append(wantLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline baseline: %v", err)
	}
	defer pb.Close()
	if _, err := pb.Run(context.Background()); err != nil {
		t.Fatalf("Run baseline: %v", err)
	}

	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")

	var gotFeatures []EntryFeatures
	var gotLabels []FutureLabel
	ctx, cancel := context.WithCancel(context.Background())
	const cancelAfterLines = 6
	part1cfg := Config{
		Sources: testSourcePaths(), Aggregator: testAggregatorConfig(),
		CheckpointPath: cpPath, ProgressEveryN: 1,
		OnProgress: func(s ProgressStats) {
			if s.LinesProcessed >= cancelAfterLines {
				cancel()
			}
		},
	}
	p1, err := NewPipeline(part1cfg, func(f EntryFeatures) { gotFeatures = append(gotFeatures, f) }, func(l FutureLabel) { gotLabels = append(gotLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline part1: %v", err)
	}
	res1, err := p1.Run(ctx)
	p1.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Run to return context.Canceled, got %v", err)
	}
	if !res1.Interrupted {
		t.Fatalf("expected RunResult.Interrupted=true")
	}
	if !res1.Checkpointed {
		t.Fatalf("expected an interrupted run to still checkpoint cleanly")
	}
	if res1.ReachedEOF {
		t.Fatalf("an interrupted run must not report ReachedEOF")
	}

	part2cfg := Config{Sources: testSourcePaths(), Aggregator: testAggregatorConfig(), CheckpointPath: cpPath, FinalizeAtEOF: true}
	p2, err := NewPipeline(part2cfg, func(f EntryFeatures) { gotFeatures = append(gotFeatures, f) }, func(l FutureLabel) { gotLabels = append(gotLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline part2 (resume): %v", err)
	}
	defer p2.Close()
	res2, err := p2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run part2: %v", err)
	}
	if !res2.ReachedEOF {
		t.Fatalf("resumed run should reach EOF")
	}

	if len(gotFeatures) != len(wantFeatures) || len(gotLabels) != len(wantLabels) {
		t.Fatalf("resumed run produced %d/%d features/labels, want %d/%d (no dup/no loss)", len(gotFeatures), len(gotLabels), len(wantFeatures), len(wantLabels))
	}
	sortFeatures := func(fs []EntryFeatures) {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Episode.EpisodeID < fs[j].Episode.EpisodeID })
	}
	sortFeatures(wantFeatures)
	sortFeatures(gotFeatures)
	for i := range wantFeatures {
		w, g := wantFeatures[i], gotFeatures[i]
		if w.Episode.Instrument != g.Episode.Instrument || !w.Episode.StartTime.Equal(g.Episode.StartTime) || !w.Episode.DecisionTime.Equal(g.Episode.DecisionTime) {
			t.Fatalf("episode %d mismatch after cancel+resume: want %+v got %+v", i, w.Episode, g.Episode)
		}
	}
}

// TestPipelineNewPipelineRefusesResumeOnConfigDrift is the integration counterpart of
// the checkpoint-level config-fingerprint tests: NewPipeline itself must refuse to
// construct a Pipeline when a semantic Aggregator threshold changed between the
// checkpointed run and the resume attempt.
func TestPipelineNewPipelineRefusesResumeOnConfigDrift(t *testing.T) {
	dir := t.TempDir()
	liq := independentEpisodeLiquidationFixture(t, dir, 2)
	cpPath := filepath.Join(dir, "checkpoint.json")

	cfg := Config{
		Sources:        SourcePaths{Liquidation: liq},
		Aggregator:     independentEpisodeAggregatorConfig(),
		MaxLines:       1,
		CheckpointPath: cpPath,
	}
	p, err := NewPipeline(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p.Close()

	cfg.Aggregator.MinTriggerNotionalUSDT = cfg.Aggregator.MinTriggerNotionalUSDT + 12345
	if _, err := NewPipeline(cfg, nil, nil); err == nil {
		t.Fatalf("expected NewPipeline to refuse resume after Aggregator config drift")
	} else if !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected error wrapping ErrCheckpointMismatch, got %v", err)
	}
}

func testSourcePaths() SourcePaths {
	return SourcePaths{
		Liquidation:       filepath.Join("testdata", "liquidation.jsonl"),
		XVenueLiquidation: filepath.Join("testdata", "xvenue.jsonl"),
		MarketMids:        filepath.Join("testdata", "mids.jsonl"),
		XMetaOI:           filepath.Join("testdata", "xmeta.jsonl"),
		Depth:             filepath.Join("testdata", "depth.jsonl"),
		InstrumentCatalog: filepath.Join("testdata", "instrument_meta.json"),
	}
}

func TestPipelineEndToEndProducesExpectedEpisode(t *testing.T) {
	var features []EntryFeatures
	var labels []FutureLabel
	cfg := Config{
		Sources:       testSourcePaths(),
		Aggregator:    testAggregatorConfig(),
		FinalizeAtEOF: true,
	}
	p, err := NewPipeline(cfg, func(f EntryFeatures) { features = append(features, f) }, func(l FutureLabel) { labels = append(labels, l) })
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()

	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.ReachedEOF {
		t.Fatalf("expected ReachedEOF=true")
	}
	if len(features) != 1 || len(labels) != 1 {
		t.Fatalf("expected 1 feature + 1 label, got %d/%d", len(features), len(labels))
	}
	if res.Stats.FeaturesEmitted != 1 || res.Stats.LabelsEmitted != 1 {
		t.Fatalf("stats mismatch: %+v", res.Stats)
	}
}

func TestPipelineDateFilterExcludesOutOfWindowRecords(t *testing.T) {
	// The fixture's episode lives entirely on 2026-07-11 (mids extend a few seconds into
	// 2026-07-10 23:59:57 for the pre-open anchor, but the driving liquidation events are all
	// on 07-11). Filtering to 07-10 must ingest nothing from the driver stream.
	var features []EntryFeatures
	cfg := Config{
		Sources:       testSourcePaths(),
		Aggregator:    testAggregatorConfig(),
		Date:          "2026-07-10",
		FinalizeAtEOF: true,
	}
	p, err := NewPipeline(cfg, func(f EntryFeatures) { features = append(features, f) }, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(features) != 0 {
		t.Fatalf("expected 0 episodes when filtered to a date with no liquidation events, got %d", len(features))
	}

	// Filtering to the correct date must reproduce the full episode.
	features = nil
	cfg.Date = "2026-07-11"
	p2, err := NewPipeline(cfg, func(f EntryFeatures) { features = append(features, f) }, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p2.Close()
	if _, err := p2.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 episode on the correct date, got %d", len(features))
	}
}

func TestPipelineMaxLinesStopsEarly(t *testing.T) {
	cfg := Config{
		Sources:    testSourcePaths(),
		Aggregator: testAggregatorConfig(),
		MaxLines:   3,
	}
	p, err := NewPipeline(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ReachedEOF {
		t.Fatalf("expected ReachedEOF=false when capped by MaxLines")
	}
	if res.Stats.LinesProcessed != 3 {
		t.Fatalf("LinesProcessed = %d, want 3", res.Stats.LinesProcessed)
	}
}

func TestPipelineProgressCallbackFiresEveryNLines(t *testing.T) {
	var calls []ProgressStats
	cfg := Config{
		Sources:        testSourcePaths(),
		Aggregator:     testAggregatorConfig(),
		ProgressEveryN: 5,
		FinalizeAtEOF:  true,
		OnProgress:     func(s ProgressStats) { calls = append(calls, s) },
	}
	p, err := NewPipeline(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	total := res.Stats.LinesProcessed
	wantMidRunCalls := int(total / 5)
	// +1 for the unconditional final report.
	if len(calls) < wantMidRunCalls {
		t.Fatalf("expected at least %d progress calls (every 5 of %d lines), got %d", wantMidRunCalls, total, len(calls))
	}
	last := calls[len(calls)-1]
	if last.LinesProcessed != total {
		t.Fatalf("final progress call LinesProcessed = %d, want %d (total)", last.LinesProcessed, total)
	}
	for i, c := range calls[:len(calls)-1] {
		if c.LinesProcessed%5 != 0 {
			t.Errorf("progress call %d fired at non-multiple-of-5 line count: %d", i, c.LinesProcessed)
		}
	}
}

func TestPipelineCheckpointResumeMatchesUninterruptedRun(t *testing.T) {
	// Baseline: run the whole fixture in one shot.
	var wantFeatures []EntryFeatures
	var wantLabels []FutureLabel
	base := Config{Sources: testSourcePaths(), Aggregator: testAggregatorConfig(), FinalizeAtEOF: true}
	pb, err := NewPipeline(base, func(f EntryFeatures) { wantFeatures = append(wantFeatures, f) }, func(l FutureLabel) { wantLabels = append(wantLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline baseline: %v", err)
	}
	defer pb.Close()
	if _, err := pb.Run(context.Background()); err != nil {
		t.Fatalf("Run baseline: %v", err)
	}

	// Interrupted: stop after a handful of lines, checkpoint, then resume to completion.
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")

	var gotFeatures []EntryFeatures
	var gotLabels []FutureLabel
	part1cfg := Config{Sources: testSourcePaths(), Aggregator: testAggregatorConfig(), MaxLines: 6, CheckpointPath: cpPath}
	p1, err := NewPipeline(part1cfg, func(f EntryFeatures) { gotFeatures = append(gotFeatures, f) }, func(l FutureLabel) { gotLabels = append(gotLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline part1: %v", err)
	}
	res1, err := p1.Run(context.Background())
	if err != nil {
		t.Fatalf("Run part1: %v", err)
	}
	p1.Close()
	if res1.ReachedEOF {
		t.Fatalf("part1 should stop early due to MaxLines, not reach EOF")
	}
	if !res1.Checkpointed {
		t.Fatalf("expected part1 to checkpoint")
	}

	part2cfg := Config{Sources: testSourcePaths(), Aggregator: testAggregatorConfig(), CheckpointPath: cpPath, FinalizeAtEOF: true}
	p2, err := NewPipeline(part2cfg, func(f EntryFeatures) { gotFeatures = append(gotFeatures, f) }, func(l FutureLabel) { gotLabels = append(gotLabels, l) })
	if err != nil {
		t.Fatalf("NewPipeline part2 (resume): %v", err)
	}
	defer p2.Close()
	res2, err := p2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run part2: %v", err)
	}
	if !res2.ReachedEOF {
		t.Fatalf("part2 (resumed to completion) should reach EOF")
	}

	if len(gotFeatures) != len(wantFeatures) || len(gotLabels) != len(wantLabels) {
		t.Fatalf("resumed run produced %d/%d features/labels, want %d/%d", len(gotFeatures), len(gotLabels), len(wantFeatures), len(wantLabels))
	}
	sortFeatures := func(fs []EntryFeatures) {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Episode.EpisodeID < fs[j].Episode.EpisodeID })
	}
	sortFeatures(wantFeatures)
	sortFeatures(gotFeatures)
	for i := range wantFeatures {
		w, g := wantFeatures[i], gotFeatures[i]
		if w.Episode.Instrument != g.Episode.Instrument || !w.Episode.StartTime.Equal(g.Episode.StartTime) || !w.Episode.DecisionTime.Equal(g.Episode.DecisionTime) {
			t.Fatalf("episode %d mismatch after resume: want %+v got %+v", i, w.Episode, g.Episode)
		}
		if !almostEqual(w.ForcedFlow.TotalNotional, g.ForcedFlow.TotalNotional, 1e-6) {
			t.Fatalf("episode %d ForcedFlow mismatch after resume: want %v got %v", i, w.ForcedFlow.TotalNotional, g.ForcedFlow.TotalNotional)
		}
		if !almostEqual(w.OI.DropRatio, g.OI.DropRatio, 1e-9) {
			t.Fatalf("episode %d OI mismatch after resume: want %+v got %+v", i, w.OI, g.OI)
		}
	}
}

// TestPipelineBoundedMemoryEvictsUnderCapacityPressure drives many concurrently-open episodes
// (one per instrument, all left open with no closing event) past MaxOpenEpisodes and checks
// that the aggregator both (a) never holds more than the configured cap and (b) still emits a
// feature (marked EvictedCapacity) for every evicted episode instead of silently dropping it.
// The final open episodes remain below the preregistered trigger and therefore must not become
// research candidates merely because EOF was reached.
func TestPipelineBoundedMemoryEvictsUnderCapacityPressure(t *testing.T) {
	dir := t.TempDir()
	liqPath := filepath.Join(dir, "liq.jsonl")

	const numInstruments = 20
	const maxOpen = 4
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	var lines []string
	for i := 0; i < numInstruments; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		line := fmt.Sprintf(`{"recorded_at":"%s","symbol":"SYM%02d-USDT","pos_side":"long","side":"sell","qty":1,"bk_px":100}`, at.Format(time.RFC3339), i)
		lines = append(lines, line)
	}
	writeLines(t, liqPath, lines)

	aggCfg := testAggregatorConfig()
	aggCfg.MaxOpenEpisodes = maxOpen
	aggCfg.GapThreshold = time.Hour // never naturally closes within this test's 1s-apart events

	var features []EntryFeatures
	cfg := Config{
		Sources:       SourcePaths{Liquidation: liqPath},
		Aggregator:    aggCfg,
		FinalizeAtEOF: true,
	}
	p, err := NewPipeline(cfg, func(f EntryFeatures) { features = append(features, f) }, nil)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer p.Close()
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// numInstruments-maxOpen episodes get evicted along the way (capacity pressure). The
	// remaining maxOpen are sub-threshold natural EOF closures and correctly emit no candidate.
	if len(features) != numInstruments-maxOpen {
		t.Fatalf("expected every capacity-evicted instrument to emit a feature, got %d/%d", len(features), numInstruments-maxOpen)
	}
	evicted := 0
	for _, f := range features {
		if f.Quality.EvictedCapacity {
			evicted++
		}
	}
	if evicted != numInstruments-maxOpen {
		t.Errorf("expected %d capacity-evicted episodes, got %d", numInstruments-maxOpen, evicted)
	}
}

func TestPipelineRejectsBadDateFormat(t *testing.T) {
	cfg := Config{Sources: testSourcePaths(), Date: "not-a-date"}
	if _, err := NewPipeline(cfg, nil, nil); err == nil {
		t.Fatalf("expected error for malformed --date")
	}
}

func TestCompactStreamStateBoundsCheckpointDedupeWindow(t *testing.T) {
	state := datatruth.StreamState{
		SeenSequences: map[int64]string{},
		SeenHashes:    map[string]string{},
	}
	for i := int64(1); i <= 10; i++ {
		hash := fmt.Sprintf("hash-%d", i)
		state.SeenSequences[i] = hash
		state.SeenHashes[hash] = fmt.Sprintf("event-%d", i)
		state.LastSequence = i
	}
	state = compactStreamState(state, 4)
	if len(state.SeenSequences) != 4 || len(state.SeenHashes) != 4 {
		t.Fatalf("dedupe state not bounded: sequences=%d hashes=%d", len(state.SeenSequences), len(state.SeenHashes))
	}
	for i := int64(1); i <= 6; i++ {
		if _, ok := state.SeenSequences[i]; ok {
			t.Fatalf("old sequence %d should have been evicted", i)
		}
	}
	for i := int64(7); i <= 10; i++ {
		if _, ok := state.SeenSequences[i]; !ok {
			t.Fatalf("recent sequence %d should be retained", i)
		}
	}
}

func TestJSONLWriterAppendsLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")
	w, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if err := w.Write(map[string]int{"a": 1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(b) != "{\"a\":1}\n" {
		t.Fatalf("unexpected output: %q", string(b))
	}
}
