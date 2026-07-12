package mechanismresearch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
	"lingxi/internal/mechanismpanel"
	"lingxi/internal/money"
)

func TestBadQualityRejectsCausalIntegrityMarks(t *testing.T) {
	for _, mark := range []datatruth.QualityMark{
		datatruth.QualityClockInvalid,
		datatruth.QualityLate,
		datatruth.QualityOutOfOrder,
		datatruth.QualityGap,
		datatruth.QualityCorrection,
	} {
		if reason, bad := badQuality(mechanismpanel.DataQuality{Marks: []datatruth.QualityMark{mark}}); !bad || reason == "" {
			t.Fatalf("mark %q must fail closed, got reason=%q bad=%v", mark, reason, bad)
		}
	}
}

func TestRunScoresPreregisteredHypotheses(t *testing.T) {
	input := writeFixture(t, strings.Join([]string{
		`{"episode_id":"r1","instrument":"BTC-USDT","event_time":"2026-07-01T00:00:00Z","shock_cluster":"liq-a","forced_side":"long","remaining_notional":100,"observed_notional":200,"decaying":false,"absorption_ratio":0.20,"outcome_bps":20,"cost_bps":2}`,
		`{"episode_id":"r2","instrument":"ETH-USDT","event_time":"2026-07-01T01:00:00Z","shock_cluster":"liq-a","forced_side":"short","remaining_notional":80,"observed_notional":200,"decaying":false,"absorption_ratio":0.30,"outcome_bps":-18,"cost_bps":2}`,
		`{"episode_id":"r3","instrument":"SOL-USDT","event_time":"2026-07-02T00:00:00Z","shock_cluster":"liq-b","forced_side":"long","remaining_notional":60,"observed_notional":150,"decaying":false,"absorption_ratio":0.40,"outcome_bps":15,"cost_bps":1}`,
		`{"episode_id":"e1","instrument":"BTC-USDT","event_time":"2026-07-03T00:00:00Z","shock_cluster":"liq-c","forced_side":"long","remaining_notional":10,"observed_notional":100,"decaying":true,"absorption_ratio":0.85,"outcome_bps":-15,"cost_bps":1}`,
		`{"episode_id":"e2","instrument":"ETH-USDT","event_time":"2026-07-03T01:00:00Z","shock_cluster":"liq-c","forced_side":"short","remaining_notional":15,"observed_notional":100,"decaying":true,"absorption_ratio":0.90,"outcome_bps":18,"cost_bps":2}`,
		`{"episode_id":"e3","instrument":"SOL-USDT","event_time":"2026-07-04T00:00:00Z","shock_cluster":"liq-d","forced_side":"long","remaining_notional":8,"observed_notional":100,"decaying":true,"absorption_ratio":0.80,"outcome_bps":-12,"cost_bps":1}`,
	}, "\n")+"\n")
	var progress bytes.Buffer
	report, err := Run(Options{
		InputPath: input, Seed: 7, BootstrapSamples: 100, WalkForwardFolds: 2,
		ProgressEvery: 2, Progress: &progress, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scorecard.Status != "RESEARCH_PASS" || len(report.Scorecard.Reasons) != 0 {
		t.Fatalf("scorecard=%+v", report.Scorecard)
	}
	if report.Scorecard.RowsRead != 6 || report.Scorecard.Episodes != 6 || report.Scorecard.EligibleEpisodes != 6 {
		t.Fatalf("counts=%+v", report.Scorecard)
	}
	relay := report.Scorecard.Hypotheses[HypothesisRelayContinuation]
	if relay.Episodes != 3 || relay.DirectionBreakdown.LongEpisodes != 2 || relay.DirectionBreakdown.ShortEpisodes != 1 {
		t.Fatalf("relay=%+v", relay)
	}
	if relay.NetCI95.Low <= 0 || relay.DailyBlockCI95.Low <= 0 || relay.ShockClusterCI95.Low <= 0 {
		t.Fatalf("relay ci=%+v", relay)
	}
	exhaustion := report.Scorecard.Hypotheses[HypothesisExhaustionReversal]
	if exhaustion.Episodes != 3 || exhaustion.DirectionBreakdown.LongEpisodes != 1 || exhaustion.DirectionBreakdown.ShortEpisodes != 2 {
		t.Fatalf("exhaustion=%+v", exhaustion)
	}
	if !strings.Contains(progress.String(), "processed=2") || !strings.Contains(progress.String(), "episodes=6") {
		t.Fatalf("progress not shown: %q", progress.String())
	}
	if !report.Details.Preregistration.NoAutoThresholdTuning {
		t.Fatalf("missing preregistration discipline: %+v", report.Details.Preregistration)
	}
}

func TestExamScorecardOmitsResearchDetails(t *testing.T) {
	input := writeFixture(t, `{"episode_id":"r1","instrument":"BTC-USDT","event_time":"2026-07-01T00:00:00Z","shock_cluster":"liq-a","forced_side":"long","remaining_notional":100,"observed_notional":200,"decaying":false,"absorption_ratio":0.20,"outcome_bps":20,"cost_bps":2}`+"\n")
	report, err := Run(Options{InputPath: input, Seed: 7, BootstrapSamples: 20, Now: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ExamScorecard(report))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "details") || strings.Contains(body, "preregistration") {
		t.Fatalf("exam payload leaked details: %s", body)
	}
	if !strings.Contains(body, "relay_continuation") {
		t.Fatalf("exam scorecard missing minimal hypothesis score: %s", body)
	}
}

func TestRunJoinsPhysicalFeatureAndLabelStreamsAndRejectsMissingQuality(t *testing.T) {
	dir := t.TempDir()
	featuresPath := filepath.Join(dir, "features.jsonl")
	labelsPath := filepath.Join(dir, "labels.jsonl")
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	good := mechanismpanel.EntryFeatures{
		Episode:      mechanismpanel.EpisodeMeta{EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at},
		ForcedFlow:   mechanismpanel.ForcedFlow{TotalNotional: 100, DominantSide: "long_squeezed"},
		Acceleration: mechanismpanel.Acceleration{FirstHalfNotional: 40, SecondHalfNotional: 60, Accelerating: true},
		Absorption:   mechanismpanel.Absorption{AbsorptionRatio: 0.2, TopDepthNotional: 1000, Observed: true},
		OI:           mechanismpanel.OIChange{Observed: true},
	}
	bad := good
	bad.Episode.EpisodeID = "raw2#relay"
	bad.Episode.RawEpisodeID = "raw2"
	bad.Quality.MissingCtVal = true
	writeObjects(t, featuresPath, good, bad)
	writeObjects(t, labelsPath,
		mechanismpanel.FutureLabel{EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at, Horizons: []mechanismpanel.LabelHorizon{{HorizonMs: 60000, ForwardRetBps: -20, Observed: true}}},
		mechanismpanel.FutureLabel{EpisodeID: "raw2#relay", RawEpisodeID: "raw2", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at, Horizons: []mechanismpanel.LabelHorizon{{HorizonMs: 60000, ForwardRetBps: -20, Observed: true}}},
	)
	report, err := Run(Options{
		FeaturePath: featuresPath, LabelPath: labelsPath, LabelHorizonMS: 60000,
		CostBPS: 1, BootstrapSamples: 20, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scorecard.Episodes != 1 || report.Scorecard.Excluded["missing_ctval"] != 1 {
		t.Fatalf("scorecard=%+v", report.Scorecard)
	}
	if report.Scorecard.Hypotheses[HypothesisRelayContinuation].Episodes != 1 {
		t.Fatalf("feature/label join 未形成接力样本: %+v", report.Scorecard.Hypotheses)
	}
}

// TestResearchExpertIsomorphism 交叉校验研究侧 classifyPanicCard 与线上
// mechanism.PanicLiquidationExpert 的接力/竭尽方向判据完全同构：凡专家接单的卡片，研究侧必须
// 给出相同的假设归属与方向；专家因容量（remaining<=0）拒绝时，研究侧亦 fail closed。
func TestResearchExpertIsomorphism(t *testing.T) {
	observed := 1_000_000.0
	absorptions := []float64{0.0, 0.3, 0.6, 0.61, 0.9, 1.0}
	remainings := []float64{0, 1_000, 199_999, 200_000, 200_001, 800_000}
	for _, decaying := range []bool{false, true} {
		for _, side := range []mechanism.Direction{mechanism.DirectionLong, mechanism.DirectionShort} {
			for _, absorption := range absorptions {
				for _, remaining := range remainings {
					card := isomorphismCard(side, observed, remaining, absorption, decaying)
					ev := mechanism.PanicLiquidationExpert{}.Evaluate(card)
					hyp, dir, ok, reason := classifyPanicCard(card)
					if !ev.Accepted {
						if remaining <= 0 && ok {
							t.Fatalf("expert rejected (capacity) but research eligible: side=%v abs=%v rem=%v decaying=%v", side, absorption, remaining, decaying)
						}
						continue
					}
					if !ok {
						t.Fatalf("expert accepted but research excluded (%s): side=%v abs=%v rem=%v decaying=%v", reason, side, absorption, remaining, decaying)
					}
					wantExhaustion := ev.Thesis.Direction == side.Opposite()
					gotExhaustion := hyp == HypothesisExhaustionReversal
					if wantExhaustion != gotExhaustion {
						t.Fatalf("hypothesis mismatch: expert exhaustion=%v research=%q (abs=%v rem=%v decaying=%v)", wantExhaustion, hyp, absorption, remaining, decaying)
					}
					if dir != ev.Thesis.Direction {
						t.Fatalf("direction mismatch: expert=%q research=%q (abs=%v rem=%v decaying=%v)", ev.Thesis.Direction, dir, absorption, remaining, decaying)
					}
				}
			}
		}
	}
}

func isomorphismCard(side mechanism.Direction, observed, remaining, absorption float64, decaying bool) mechanism.MechanismEventCard {
	payer := mechanism.PayerShortLiquidations
	if side == mechanism.DirectionShort {
		payer = mechanism.PayerLongLiquidations
	}
	return mechanism.MechanismEventCard{
		ID: "iso", Instrument: "X-USDT-SWAP",
		Regime: mechanism.Regime{Kind: mechanism.RegimePanicLiquidation, DeadlineUnixMs: 10_000},
		ForcedFlow: mechanism.ForcedFlow{
			Payer: payer, Side: side,
			ObservedNotional:  money.FromUSDT(observed),
			RemainingNotional: money.FromUSDT(remaining),
			Decaying:          decaying,
		},
		Transmission:  mechanism.Transmission{Cluster: "c", AbsorptionRatio: absorption},
		Executability: mechanism.Executability{TakerFeasible: true, LotNotional: money.FromUSDT(10), TopDepthNotional: money.FromUSDT(10_000_000)},
		Quality:       mechanism.Quality{ObservationConfidence: 0.7},
	}
}

// TestDuplicateLabelEpisodeIDErrors 重复 label EpisodeID 必须报错（标签流完整性硬门）。
func TestDuplicateLabelEpisodeIDErrors(t *testing.T) {
	dir := t.TempDir()
	featuresPath := filepath.Join(dir, "features.jsonl")
	labelsPath := filepath.Join(dir, "labels.jsonl")
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	feat := mechanismpanel.EntryFeatures{
		Episode:      mechanismpanel.EpisodeMeta{EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at},
		ForcedFlow:   mechanismpanel.ForcedFlow{TotalNotional: 100, DominantSide: "long_squeezed"},
		Acceleration: mechanismpanel.Acceleration{FirstHalfNotional: 40, SecondHalfNotional: 60, Accelerating: true},
		Absorption:   mechanismpanel.Absorption{AbsorptionRatio: 0.2, TopDepthNotional: 1000, Observed: true},
		OI:           mechanismpanel.OIChange{Observed: true},
	}
	writeObjects(t, featuresPath, feat)
	label := mechanismpanel.FutureLabel{EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at, Horizons: []mechanismpanel.LabelHorizon{{HorizonMs: 60000, ForwardRetBps: -20, Observed: true}}}
	writeObjects(t, labelsPath, label, label)
	_, err := Run(Options{FeaturePath: featuresPath, LabelPath: labelsPath, LabelHorizonMS: 60000, Now: time.Unix(1, 0).UTC()})
	if err == nil || !strings.Contains(err.Error(), "duplicate label episode_id") {
		t.Fatalf("expected duplicate label error, got %v", err)
	}
}

// TestJoinValidationRejectsMismatch join 必须校验 instrument/phase/decision time，不一致即报错。
func TestJoinValidationRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	featuresPath := filepath.Join(dir, "features.jsonl")
	labelsPath := filepath.Join(dir, "labels.jsonl")
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	feat := mechanismpanel.EntryFeatures{
		Episode:      mechanismpanel.EpisodeMeta{EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay, Instrument: "BTC-USDT", DecisionTime: at},
		ForcedFlow:   mechanismpanel.ForcedFlow{TotalNotional: 100, DominantSide: "long_squeezed"},
		Acceleration: mechanismpanel.Acceleration{FirstHalfNotional: 40, SecondHalfNotional: 60, Accelerating: true},
		Absorption:   mechanismpanel.Absorption{AbsorptionRatio: 0.2, TopDepthNotional: 1000, Observed: true},
		OI:           mechanismpanel.OIChange{Observed: true},
	}
	writeObjects(t, featuresPath, feat)
	// 同 EpisodeID，但 instrument 不一致：join 串接错误，必须 fail closed。
	writeObjects(t, labelsPath, mechanismpanel.FutureLabel{
		EpisodeID: "raw1#relay", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseRelay,
		Instrument: "ETH-USDT", DecisionTime: at,
		Horizons: []mechanismpanel.LabelHorizon{{HorizonMs: 60000, ForwardRetBps: -20, Observed: true}},
	})
	_, err := Run(Options{FeaturePath: featuresPath, LabelPath: labelsPath, LabelHorizonMS: 60000, Now: time.Unix(1, 0).UTC()})
	if err == nil || !strings.Contains(err.Error(), "join instrument mismatch") {
		t.Fatalf("expected join instrument mismatch error, got %v", err)
	}
}

// TestPanelExhaustionPhaseDrivesReversalWithoutFabricatedRemaining 验证 panel 路径按物理 phase
// 判定竭尽反转，方向为逆被迫流；且不依赖被去除的 SecondHalfNotional-冒充-remaining 证据
// （此处 SecondHalfNotional>FirstHalfNotional，旧逻辑会误判为“加速、剩余充沛”的接力）。
func TestPanelExhaustionPhaseDrivesReversalWithoutFabricatedRemaining(t *testing.T) {
	dir := t.TempDir()
	featuresPath := filepath.Join(dir, "features.jsonl")
	labelsPath := filepath.Join(dir, "labels.jsonl")
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	feat := mechanismpanel.EntryFeatures{
		Episode:      mechanismpanel.EpisodeMeta{EpisodeID: "raw1#exhaustion", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseExhaustion, Instrument: "BTC-USDT", DecisionTime: at},
		ForcedFlow:   mechanismpanel.ForcedFlow{TotalNotional: 100, DominantSide: "short_squeezed"},
		Acceleration: mechanismpanel.Acceleration{FirstHalfNotional: 40, SecondHalfNotional: 60, Accelerating: true},
		Absorption:   mechanismpanel.Absorption{AbsorptionRatio: 0.8, TopDepthNotional: 1000, Observed: true},
		OI:           mechanismpanel.OIChange{Observed: true},
	}
	writeObjects(t, featuresPath, feat)
	writeObjects(t, labelsPath, mechanismpanel.FutureLabel{
		EpisodeID: "raw1#exhaustion", RawEpisodeID: "raw1", Phase: mechanismpanel.PhaseExhaustion,
		Instrument: "BTC-USDT", DecisionTime: at,
		Horizons: []mechanismpanel.LabelHorizon{{HorizonMs: 60000, ForwardRetBps: 30, Observed: true}},
	})
	report, err := Run(Options{FeaturePath: featuresPath, LabelPath: labelsPath, LabelHorizonMS: 60000, Now: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scorecard.Hypotheses[HypothesisRelayContinuation].Episodes != 0 {
		t.Fatalf("exhaustion phase must not produce relay sample: %+v", report.Scorecard.Hypotheses)
	}
	ex := report.Scorecard.Hypotheses[HypothesisExhaustionReversal]
	if ex.Episodes != 1 {
		t.Fatalf("exhaustion phase should produce 1 sample: %+v", ex)
	}
	// short_squeezed → 上行被迫压力(long)，竭尽逆之 → 做空。
	if len(report.Details.Episodes) != 1 || report.Details.Episodes[0].Direction != string(mechanism.DirectionShort) {
		t.Fatalf("exhaustion direction should reverse to short: %+v", report.Details.Episodes)
	}
}

// TestFewShockClustersFailsClosed 少于 2 个独立 shock cluster 时不得报告有效 cluster CI，且据此 NO_GO。
func TestFewShockClustersFailsClosed(t *testing.T) {
	input := writeFixture(t, strings.Join([]string{
		`{"episode_id":"r1","instrument":"BTC-USDT","event_time":"2026-07-01T00:00:00Z","shock_cluster":"liq-a","forced_side":"long","remaining_notional":100,"observed_notional":200,"decaying":false,"absorption_ratio":0.20,"outcome_bps":20,"cost_bps":2}`,
		`{"episode_id":"r2","instrument":"ETH-USDT","event_time":"2026-07-02T00:00:00Z","shock_cluster":"liq-a","forced_side":"short","remaining_notional":80,"observed_notional":200,"decaying":false,"absorption_ratio":0.30,"outcome_bps":-18,"cost_bps":2}`,
	}, "\n")+"\n")
	report, err := Run(Options{InputPath: input, Seed: 7, BootstrapSamples: 100, WalkForwardFolds: 2, Now: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	relay := report.Scorecard.Hypotheses[HypothesisRelayContinuation]
	if relay.ShockClusters != 1 || relay.ShockClusterCIValid {
		t.Fatalf("single-cluster relay must not report valid cluster CI: %+v", relay)
	}
	if relay.ShockClusterCI95.Low != 0 || relay.ShockClusterCI95.High != 0 {
		t.Fatalf("single-cluster relay cluster CI must be unreported: %+v", relay.ShockClusterCI95)
	}
	if report.Scorecard.Status != "RESEARCH_NO_GO" {
		t.Fatalf("insufficient clusters must fail closed: %+v", report.Scorecard)
	}
	if !containsReason(report.Scorecard.Reasons, HypothesisRelayContinuation+":insufficient_shock_clusters") {
		t.Fatalf("missing insufficient_shock_clusters reason: %+v", report.Scorecard.Reasons)
	}
}

// TestNeutralRemainingUnobservableFailsClosed 无可观测 remaining 时不得编造，episode fail closed。
func TestNeutralRemainingUnobservableFailsClosed(t *testing.T) {
	input := writeFixture(t, `{"episode_id":"r1","instrument":"BTC-USDT","event_time":"2026-07-01T00:00:00Z","shock_cluster":"liq-a","forced_side":"long","observed_notional":200,"decaying":false,"absorption_ratio":0.20,"outcome_bps":20,"cost_bps":2}`+"\n")
	report, err := Run(Options{InputPath: input, Seed: 7, BootstrapSamples: 20, Now: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scorecard.EligibleEpisodes != 0 {
		t.Fatalf("episode without observable remaining must be excluded: %+v", report.Scorecard)
	}
	if report.Scorecard.Excluded["remaining_unobservable"] != 1 {
		t.Fatalf("expected remaining_unobservable exclusion: %+v", report.Scorecard.Excluded)
	}
}

func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "episodes.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeObjects(t *testing.T, path string, values ...any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, value := range values {
		if err := enc.Encode(value); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
