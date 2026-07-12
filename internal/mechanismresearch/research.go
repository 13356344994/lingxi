package mechanismresearch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
	"lingxi/internal/mechanismpanel"
	"lingxi/internal/money"
)

const (
	HypothesisRelayContinuation  = "relay_continuation"
	HypothesisExhaustionReversal = "exhaustion_reversal"

	// relayMaxAbsorption 仅用于预注册记录；接力/竭尽的实际判据以 exhaustion 阈值为界
	// （relay = 通过基础门且「未竭尽」），与线上 mechanism.PanicLiquidationExpert 同构。
	relayMaxAbsorption = 0.60
	// exhaustionMinAbsorption / exhaustionMaxRemainingPct 必须与线上专家常量
	// mechanism.panicExhaustionAbsorption / panicExhaustionRemainingFrac 逐字一致，
	// 由 TestResearchExpertIsomorphism 交叉校验；任一漂移则测试失败（漂移即 fail closed）。
	exhaustionMinAbsorption   = 0.60
	exhaustionMaxRemainingPct = 0.20
	defaultSeed               = int64(20260711)

	// minIndependentClusters 是报告有效 shock cluster CI 所需的最少独立簇数。
	// 少于此数不得报告有效 cluster CI（单一相关簇无法证明效应跨簇稳健）。
	minIndependentClusters = 2
)

type Options struct {
	InputPath        string
	FeaturePath      string
	LabelPath        string
	LabelHorizonMS   int64
	MaxLines         int
	CostBPS          float64
	Seed             int64
	BootstrapSamples int
	WalkForwardFolds int
	// PurgeEpisodes / EmbargoEpisodes 仅为向后兼容保留，不再用于切分（切分已改为按时间）。
	PurgeEpisodes   int
	EmbargoEpisodes int
	// PurgeDuration / EmbargoDuration 是按时间的 walk-forward purge/embargo。
	// 有效 purge 至少覆盖最大 label horizon，避免训练标签窥探测试期。
	PurgeDuration   time.Duration
	EmbargoDuration time.Duration
	ProgressEvery   int
	Progress        io.Writer
	Now             time.Time
}

type Report struct {
	Scorecard Scorecard `json:"scorecard"`
	Details   Details   `json:"details,omitempty"`
}

type Scorecard struct {
	GeneratedAt      time.Time                  `json:"generated_at"`
	Status           string                     `json:"status"`
	Reasons          []string                   `json:"reasons,omitempty"`
	InputPath        string                     `json:"input_path"`
	RowsRead         int                        `json:"rows_read"`
	Episodes         int                        `json:"episodes"`
	EligibleEpisodes int                        `json:"eligible_episodes"`
	Hypotheses       map[string]HypothesisScore `json:"hypotheses"`
	Excluded         map[string]int             `json:"excluded,omitempty"`
}

type Details struct {
	Preregistration Preregistration `json:"preregistration"`
	Episodes        []EpisodeResult `json:"episodes"`
}

type Preregistration struct {
	Hypotheses              []string `json:"hypotheses"`
	RelayMaxAbsorption      float64  `json:"relay_max_absorption"`
	ExhaustionMinAbsorption float64  `json:"exhaustion_min_absorption"`
	ExhaustionMaxRemaining  float64  `json:"exhaustion_max_remaining_ratio"`
	NoAutoThresholdTuning   bool     `json:"no_auto_threshold_tuning"`
}

type HypothesisScore struct {
	Name                 string              `json:"name"`
	Preregistered        bool                `json:"preregistered"`
	Episodes             int                 `json:"episodes"`
	GrossMeanBPS         float64             `json:"gross_mean_bps"`
	CostMeanBPS          float64             `json:"cost_mean_bps"`
	NetMeanBPS           float64             `json:"net_mean_bps"`
	DirectionSign        int                 `json:"direction_sign"`
	NetCI95              ConfidenceInterval  `json:"net_ci95"`
	DailyBlockCI95       ConfidenceInterval  `json:"daily_block_ci95"`
	ShockClusters        int                 `json:"shock_clusters"`
	ShockClusterCIValid  bool                `json:"shock_cluster_ci_valid"`
	ShockClusterCI95     ConfidenceInterval  `json:"shock_cluster_block_ci95"`
	WalkForward          WalkForwardSummary  `json:"anchored_walk_forward"`
	Concentration        ConcentrationReport `json:"concentration"`
	DirectionBreakdown   DirectionBreakdown  `json:"direction_breakdown"`
	BootstrapSeed        int64               `json:"bootstrap_seed"`
	BootstrapSamples     int                 `json:"bootstrap_samples"`
	PurgeEpisodes        int                 `json:"purge_episodes"`
	EmbargoEpisodes      int                 `json:"embargo_episodes"`
	FixedCostBPSFallback float64             `json:"fixed_cost_bps_fallback"`
}

type ConfidenceInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type WalkForwardSummary struct {
	Folds             int               `json:"folds"`
	PositiveTestFolds int               `json:"positive_test_folds"`
	TestMeanBPS       float64           `json:"test_mean_bps"`
	PurgeMS           int64             `json:"purge_ms"`
	EmbargoMS         int64             `json:"embargo_ms"`
	LabelHorizonMS    int64             `json:"label_horizon_ms"`
	PurgedEpisodes    int               `json:"purged_episodes"`
	Splits            []WalkForwardFold `json:"splits,omitempty"`
}

type WalkForwardFold struct {
	Fold          int     `json:"fold"`
	TrainEpisodes int     `json:"train_episodes"`
	TestEpisodes  int     `json:"test_episodes"`
	TrainMeanBPS  float64 `json:"train_mean_bps"`
	TestMeanBPS   float64 `json:"test_mean_bps"`
}

type ConcentrationReport struct {
	MaxSingleInstrumentShare float64 `json:"max_single_instrument_share"`
	MaxSingleDayShare        float64 `json:"max_single_day_share"`
	MaxSingleMonthShare      float64 `json:"max_single_month_share"`
}

type DirectionBreakdown struct {
	LongEpisodes  int     `json:"long_episodes"`
	ShortEpisodes int     `json:"short_episodes"`
	LongNetMean   float64 `json:"long_net_mean_bps"`
	ShortNetMean  float64 `json:"short_net_mean_bps"`
}

type EpisodeResult struct {
	ID           string    `json:"id"`
	Instrument   string    `json:"instrument"`
	Day          string    `json:"day"`
	Month        string    `json:"month"`
	ShockCluster string    `json:"shock_cluster"`
	Hypothesis   string    `json:"hypothesis"`
	Direction    string    `json:"direction"`
	OutcomeBPS   float64   `json:"outcome_bps"`
	GrossBPS     float64   `json:"gross_bps"`
	CostBPS      float64   `json:"cost_bps"`
	NetBPS       float64   `json:"net_bps"`
	Time         time.Time `json:"time"`
}

// episode 是一条已判定的研究样本前体：接力/竭尽假设与方向在装载时确定（neutral 路径用与线上
// 专家同构的判据；panel 路径消费上游已提交的物理 phase），hypothesis 为空表示不合格。
type episode struct {
	id           string
	instrument   string
	shockCluster string
	eventTime    time.Time
	hypothesis   string
	direction    mechanism.Direction
	outcomeBPS   float64
	costBPS      float64
}

type sample struct {
	EpisodeResult
	sortTime time.Time
}

func Run(opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	if strings.TrimSpace(opts.InputPath) == "" && (strings.TrimSpace(opts.FeaturePath) == "" || strings.TrimSpace(opts.LabelPath) == "") {
		return Report{}, errors.New("input path or both feature/label paths are required")
	}
	var (
		episodes []episode
		rows     int
		excluded map[string]int
		err      error
	)
	if opts.FeaturePath != "" && opts.LabelPath != "" {
		episodes, rows, excluded, err = loadPanelEpisodes(opts)
	} else {
		episodes, rows, excluded, err = loadEpisodes(opts)
	}
	if err != nil {
		return Report{}, err
	}
	samples := make([]sample, 0, len(episodes))
	for _, ep := range episodes {
		if ep.hypothesis == "" {
			continue
		}
		gross := directionSign(ep.direction) * ep.outcomeBPS
		day := ep.eventTime.UTC().Format("2006-01-02")
		month := ep.eventTime.UTC().Format("2006-01")
		samples = append(samples, sample{
			EpisodeResult: EpisodeResult{
				ID: ep.id, Instrument: ep.instrument, Day: day, Month: month,
				ShockCluster: ep.shockCluster, Hypothesis: ep.hypothesis, Direction: string(ep.direction),
				OutcomeBPS: ep.outcomeBPS, GrossBPS: gross, CostBPS: ep.costBPS, NetBPS: gross - ep.costBPS,
				Time: ep.eventTime.UTC(),
			},
			sortTime: ep.eventTime.UTC(),
		})
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].sortTime.Equal(samples[j].sortTime) {
			return samples[i].ID < samples[j].ID
		}
		return samples[i].sortTime.Before(samples[j].sortTime)
	})

	// 切分在假设筛选之前、基于全部合格样本的时间轴一次性建立，避免每个假设各自调切分造成泄漏。
	plan := buildWalkForwardPlan(samples, opts)

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	scorecard := Scorecard{
		GeneratedAt: now.UTC(), Status: "RESEARCH_NO_GO", InputPath: nonEmpty(opts.InputPath, opts.FeaturePath),
		RowsRead: rows, Episodes: len(episodes), EligibleEpisodes: len(samples),
		Hypotheses: map[string]HypothesisScore{}, Excluded: excluded,
	}
	for _, h := range []string{HypothesisRelayContinuation, HypothesisExhaustionReversal} {
		scorecard.Hypotheses[h] = scoreHypothesis(h, samplesForHypothesis(samples, h), opts, plan)
	}
	for name, score := range scorecard.Hypotheses {
		if score.Episodes == 0 {
			scorecard.Reasons = append(scorecard.Reasons, name+":no_eligible_episodes")
			continue
		}
		if score.NetMeanBPS <= 0 {
			scorecard.Reasons = append(scorecard.Reasons, name+":net_mean_not_positive")
		}
		if score.NetCI95.Low <= 0 {
			scorecard.Reasons = append(scorecard.Reasons, name+":episode_ci_crosses_zero")
		}
		if !score.ShockClusterCIValid {
			// 少于 2 个独立 shock cluster：不得报告有效 cluster CI，据此 fail closed。
			scorecard.Reasons = append(scorecard.Reasons, name+":insufficient_shock_clusters")
		} else if score.ShockClusterCI95.Low <= 0 {
			scorecard.Reasons = append(scorecard.Reasons, name+":shock_cluster_ci_crosses_zero")
		}
	}
	sort.Strings(scorecard.Reasons)
	if len(scorecard.Reasons) == 0 {
		scorecard.Status = "RESEARCH_PASS"
	}
	return Report{
		Scorecard: scorecard,
		Details: Details{
			Preregistration: Preregistration{
				Hypotheses:         []string{HypothesisRelayContinuation, HypothesisExhaustionReversal},
				RelayMaxAbsorption: relayMaxAbsorption, ExhaustionMinAbsorption: exhaustionMinAbsorption,
				ExhaustionMaxRemaining: exhaustionMaxRemainingPct, NoAutoThresholdTuning: true,
			},
			Episodes: episodeResults(samples),
		},
	}, nil
}

func ExamScorecard(report Report) Scorecard {
	return report.Scorecard
}

func ResearchReport(report Report) Report {
	return report
}

func normalizeOptions(opts Options) Options {
	if opts.Seed == 0 {
		opts.Seed = defaultSeed
	}
	if opts.BootstrapSamples <= 0 {
		opts.BootstrapSamples = 500
	}
	if opts.WalkForwardFolds <= 0 {
		opts.WalkForwardFolds = 3
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = 1000
	}
	if opts.PurgeEpisodes < 0 {
		opts.PurgeEpisodes = 0
	}
	if opts.EmbargoEpisodes < 0 {
		opts.EmbargoEpisodes = 0
	}
	if opts.PurgeDuration < 0 {
		opts.PurgeDuration = 0
	}
	if opts.EmbargoDuration < 0 {
		opts.EmbargoDuration = 0
	}
	return opts
}

func loadEpisodes(opts Options) ([]episode, int, map[string]int, error) {
	f, err := os.Open(opts.InputPath)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	episodes := []episode{}
	excluded := map[string]int{}
	line := 0
	for scanner.Scan() {
		line++
		if opts.MaxLines > 0 && line > opts.MaxLines {
			break
		}
		ep, card, ok, err := parseEpisode(scanner.Bytes(), opts)
		if err != nil {
			return nil, line, excluded, fmt.Errorf("%s line %d: %w", opts.InputPath, line, err)
		}
		if ok {
			if hyp, dir, eligible, reason := classifyPanicCard(card); eligible {
				ep.hypothesis, ep.direction = hyp, dir
			} else {
				excluded[reason]++
			}
			episodes = append(episodes, ep)
		}
		if opts.Progress != nil && line%opts.ProgressEvery == 0 {
			fmt.Fprintf(opts.Progress, "%s processed=%d episodes=%d\n", filepath.Base(opts.InputPath), line, len(episodes))
		}
	}
	return episodes, line, excluded, scanner.Err()
}

func loadPanelEpisodes(opts Options) ([]episode, int, map[string]int, error) {
	labels, err := loadPanelLabels(opts.LabelPath, opts.LabelHorizonMS)
	if err != nil {
		return nil, 0, nil, err
	}
	f, err := os.Open(opts.FeaturePath)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()
	excluded := map[string]int{}
	var episodes []episode
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if opts.MaxLines > 0 && line > opts.MaxLines {
			break
		}
		var feature mechanismpanel.EntryFeatures
		if err := json.Unmarshal(scanner.Bytes(), &feature); err != nil {
			return nil, line, excluded, fmt.Errorf("%s line %d: %w", opts.FeaturePath, line, err)
		}
		if reason, bad := badQuality(feature.Quality); bad {
			excluded[reason]++
			continue
		}
		label, ok := labels[feature.Episode.EpisodeID]
		if !ok {
			excluded["missing_label"]++
			continue
		}
		// join 硬校验：instrument / phase / decision time 必须一致，否则视为标签串接错误，fail closed。
		if err := validateJoin(feature, label, opts.FeaturePath, line); err != nil {
			return nil, line, excluded, err
		}
		horizon, ok := selectLabelHorizon(label, opts.LabelHorizonMS)
		if !ok || !horizon.Observed {
			excluded["incomplete_label"]++
			continue
		}
		hyp, dir, cluster, ok, reason := classifyPanelFeature(feature)
		if !ok {
			excluded[reason]++
			continue
		}
		episodes = append(episodes, episode{
			id: feature.Episode.EpisodeID, instrument: feature.Episode.Instrument,
			shockCluster: cluster, eventTime: feature.Episode.DecisionTime.UTC(),
			hypothesis: hyp, direction: dir, outcomeBPS: horizon.ForwardRetBps, costBPS: opts.CostBPS,
		})
		if opts.Progress != nil && line%opts.ProgressEvery == 0 {
			fmt.Fprintf(opts.Progress, "%s processed=%d episodes=%d\n", filepath.Base(opts.FeaturePath), line, len(episodes))
		}
	}
	return episodes, line, excluded, scanner.Err()
}

// badQuality 明确剔除坏质量 episode：缺 ctVal / OI / 深度、bounded-memory 提前收口、乱序输入。
func badQuality(q mechanismpanel.DataQuality) (string, bool) {
	for _, mark := range q.Marks {
		switch mark {
		case datatruth.QualityClockInvalid,
			datatruth.QualityLate,
			datatruth.QualityDuplicate,
			datatruth.QualityOutOfOrder,
			datatruth.QualityGap,
			datatruth.QualityCorrection,
			datatruth.QualityPayloadHashMissing:
			return "quality_" + string(mark), true
		}
	}
	switch {
	case q.MissingCtVal:
		return "missing_ctval", true
	case q.MissingOI:
		return "missing_oi", true
	case q.MissingDepth:
		return "missing_depth", true
	case q.EvictedCapacity:
		return "evicted_capacity", true
	case q.OutOfOrderInputs > 0:
		return "out_of_order_inputs", true
	}
	return "", false
}

// validateJoin 校验 feature 与其按 EpisodeID join 到的 label 在 instrument / phase / decision time
// 上完全一致；任一不一致都说明 join 串接错误（ID 冲突或数据损坏），必须 fail closed。
func validateJoin(f mechanismpanel.EntryFeatures, l mechanismpanel.FutureLabel, path string, line int) error {
	if f.Episode.Instrument != l.Instrument {
		return fmt.Errorf("%s line %d: join instrument mismatch for episode %q: feature=%q label=%q", path, line, f.Episode.EpisodeID, f.Episode.Instrument, l.Instrument)
	}
	if f.Episode.Phase != l.Phase {
		return fmt.Errorf("%s line %d: join phase mismatch for episode %q: feature=%q label=%q", path, line, f.Episode.EpisodeID, f.Episode.Phase, l.Phase)
	}
	if !f.Episode.DecisionTime.Equal(l.DecisionTime) {
		return fmt.Errorf("%s line %d: join decision_time mismatch for episode %q: feature=%s label=%s", path, line, f.Episode.EpisodeID, f.Episode.DecisionTime.UTC(), l.DecisionTime.UTC())
	}
	return nil
}

func loadPanelLabels(path string, horizonMS int64) (map[string]mechanismpanel.FutureLabel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]mechanismpanel.FutureLabel{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var label mechanismpanel.FutureLabel
		if err := json.Unmarshal(scanner.Bytes(), &label); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		// 重复 label EpisodeID 是标签流完整性错误（一个 phase 至多一条标签），必须报错。
		if seen[label.EpisodeID] {
			return nil, fmt.Errorf("%s line %d: duplicate label episode_id %q", path, line, label.EpisodeID)
		}
		seen[label.EpisodeID] = true
		if _, ok := selectLabelHorizon(label, horizonMS); ok {
			out[label.EpisodeID] = label
		}
	}
	return out, scanner.Err()
}

func selectLabelHorizon(label mechanismpanel.FutureLabel, horizonMS int64) (mechanismpanel.LabelHorizon, bool) {
	if len(label.Horizons) == 0 {
		return mechanismpanel.LabelHorizon{}, false
	}
	if horizonMS <= 0 {
		horizonMS = 60000
	}
	for _, h := range label.Horizons {
		if h.HorizonMs == horizonMS {
			return h, true
		}
	}
	return mechanismpanel.LabelHorizon{}, false
}

// classifyPanelFeature 消费上游 panel 的物理 phase 决策：relay/exhaustion 在正确锚点、用与线上
// 专家同一套统一阈值上游一次性做出（两个 phase 物理隔离），research 侧绝不用单张结束特征重新判定
// （那正是 panel 修复的泄漏），也绝不用 SecondHalfNotional 冒充 remaining 造独立证据。
// 无可识别 phase → fail closed。shock cluster 在 EntryFeatures 中不可观测，故留空，由簇门 fail closed。
func classifyPanelFeature(f mechanismpanel.EntryFeatures) (string, mechanism.Direction, string, bool, string) {
	var pressure mechanism.Direction
	switch f.ForcedFlow.DominantSide {
	case "long_squeezed":
		pressure = mechanism.DirectionShort
	case "short_squeezed":
		pressure = mechanism.DirectionLong
	default:
		return "", mechanism.DirectionFlat, "", false, "invalid_forced_side"
	}
	switch f.Episode.Phase {
	case mechanismpanel.PhaseRelay:
		return HypothesisRelayContinuation, pressure, "", true, ""
	case mechanismpanel.PhaseExhaustion:
		return HypothesisExhaustionReversal, pressure.Opposite(), "", true, ""
	default:
		return "", mechanism.DirectionFlat, "", false, "unknown_phase"
	}
}

func parseEpisode(raw []byte, opts Options) (episode, mechanism.MechanismEventCard, bool, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return episode{}, mechanism.MechanismEventCard{}, false, err
	}
	outcome, ok := firstFloat(obj, "outcome_bps", "forward_return_bps", "return_bps", "realized_bps")
	if !ok || !isFinite(outcome) {
		return episode{}, mechanism.MechanismEventCard{}, false, nil
	}
	var card mechanism.MechanismEventCard
	if cardRaw, ok := lookup(obj, "card", "Card"); ok {
		if err := json.Unmarshal(cardRaw, &card); err != nil {
			return episode{}, mechanism.MechanismEventCard{}, false, err
		}
	}
	overlayCardFromNeutral(obj, &card)
	eventTime := firstTime(obj, card.EventTimeUnixMs)
	if eventTime.IsZero() {
		eventTime = time.Unix(0, 0).UTC()
	}
	id := firstString(obj, "episode_id", "id", "decision_id")
	if id == "" {
		id = card.ID
	}
	instrument := firstString(obj, "instrument", "symbol")
	if instrument == "" {
		instrument = card.Instrument
	}
	cluster := firstString(obj, "shock_cluster", "cluster")
	if cluster == "" {
		cluster = card.Transmission.Cluster
	}
	if cluster == "" {
		cluster = "unknown"
	}
	cost, ok := firstFloat(obj, "cost_bps", "fee_bps", "cost_pressure_bps")
	if !ok {
		cost = opts.CostBPS + math.Max(0, card.Executability.ExpectedSlippageBps)
	}
	if !isFinite(cost) || cost < 0 {
		cost = opts.CostBPS
	}
	return episode{
		id: id, instrument: instrument, shockCluster: cluster, eventTime: eventTime,
		outcomeBPS: outcome, costBPS: cost,
	}, card, true, nil
}

func overlayCardFromNeutral(obj map[string]json.RawMessage, card *mechanism.MechanismEventCard) {
	if card == nil {
		return
	}
	if card.ID == "" {
		card.ID = firstString(obj, "episode_id", "id", "decision_id")
	}
	if card.Instrument == "" {
		card.Instrument = firstString(obj, "instrument", "symbol")
	}
	if card.Transmission.Cluster == "" {
		card.Transmission.Cluster = firstString(obj, "shock_cluster", "cluster")
	}
	if side := firstString(obj, "forced_side", "pressure_direction", "side"); side != "" {
		card.ForcedFlow.Side = mechanism.Direction(side)
	}
	if v, ok := firstFloat(obj, "absorption_ratio"); ok {
		card.Transmission.AbsorptionRatio = v
	}
	if v, ok := firstFloat(obj, "remaining_notional", "remaining_notional_usdt"); ok {
		card.ForcedFlow.RemainingNotional = mechanismNotional(v)
	}
	if v, ok := firstFloat(obj, "observed_notional", "observed_notional_usdt"); ok {
		card.ForcedFlow.ObservedNotional = mechanismNotional(v)
	}
	if v, ok := firstBool(obj, "decaying", "forced_flow_decaying"); ok {
		card.ForcedFlow.Decaying = v
	}
	if v, ok := firstFloat(obj, "expected_slippage_bps"); ok {
		card.Executability.ExpectedSlippageBps = v
	}
	if v, ok := firstBool(obj, "taker_feasible"); ok {
		card.Executability.TakerFeasible = v
	}
}

func mechanismNotional(v float64) money.Micros {
	return money.Micros(math.Round(v * 1_000_000))
}

// panicExhausted 与线上 mechanism.PanicLiquidationExpert 的竭尽判据逐字同构：
//
//	exhausted = Decaying && Observed>0 && AbsorptionRatio>=0.60 && Remaining <= Observed*0.20
//
// 使用与专家相同的定点金额运算（money.MulRate）以保证在 micro 级别的比较结果完全一致；
// TestResearchExpertIsomorphism 在卡片网格上交叉校验二者一致。
func panicExhausted(observed, remaining money.Micros, absorption float64, decaying bool) bool {
	return decaying && observed > 0 &&
		absorption >= exhaustionMinAbsorption &&
		int64(remaining) <= int64(observed.MulRate(exhaustionMaxRemainingPct))
}

// classifyPanicCard 是 neutral 路径的资格判据，与线上 PanicLiquidationExpert 的接力/竭尽方向判据同构：
//   - 需要可交易的被迫方向、可观测的合法吸收比例、可观测且为正的剩余被迫名义（对应专家的容量门）；
//   - 竭尽（exhausted）→ 逆被迫流；否则接力延续 → 顺被迫流。
//
// 任一必要观测缺失（remaining 不可观测/非正、吸收非法、方向缺失）一律 fail closed，绝不编造。
func classifyPanicCard(card mechanism.MechanismEventCard) (string, mechanism.Direction, bool, string) {
	side := card.ForcedFlow.Side
	if !side.IsDirectional() {
		return "", mechanism.DirectionFlat, false, "no_forced_side"
	}
	if !validRatio(card.Transmission.AbsorptionRatio) {
		return "", mechanism.DirectionFlat, false, "invalid_absorption"
	}
	remaining := card.ForcedFlow.RemainingNotional
	if remaining <= 0 {
		// 没有可观测且为正的剩余被迫名义 → 无法判定接力/竭尽（也无机制容量），fail closed。
		return "", mechanism.DirectionFlat, false, "remaining_unobservable"
	}
	if panicExhausted(card.ForcedFlow.ObservedNotional, remaining, card.Transmission.AbsorptionRatio, card.ForcedFlow.Decaying) {
		return HypothesisExhaustionReversal, side.Opposite(), true, ""
	}
	return HypothesisRelayContinuation, side, true, ""
}

func scoreHypothesis(name string, samples []sample, opts Options, plan wfPlan) HypothesisScore {
	score := HypothesisScore{
		Name: name, Preregistered: true, Episodes: len(samples), BootstrapSeed: opts.Seed,
		BootstrapSamples: opts.BootstrapSamples, PurgeEpisodes: opts.PurgeEpisodes,
		EmbargoEpisodes: opts.EmbargoEpisodes, FixedCostBPSFallback: opts.CostBPS,
	}
	if len(samples) == 0 {
		return score
	}
	net := values(samples, func(s sample) float64 { return s.NetBPS })
	score.GrossMeanBPS = mean(values(samples, func(s sample) float64 { return s.GrossBPS }))
	score.CostMeanBPS = mean(values(samples, func(s sample) float64 { return s.CostBPS }))
	score.NetMeanBPS = mean(net)
	score.DirectionSign = sign(score.NetMeanBPS)
	score.NetCI95 = percentileCI(bootstrapMeans(net, opts.BootstrapSamples, rand.New(rand.NewSource(opts.Seed+11))))
	score.DailyBlockCI95 = percentileCI(blockBootstrap(samples, opts.BootstrapSamples, opts.Seed+23, func(s sample) string { return s.Day }))
	score.ShockClusters = distinctClusters(samples)
	if score.ShockClusters >= minIndependentClusters {
		score.ShockClusterCI95 = percentileCI(blockBootstrap(samples, opts.BootstrapSamples, opts.Seed+37, func(s sample) string { return s.ShockCluster }))
		score.ShockClusterCIValid = true
	}
	score.WalkForward = evaluateWalkForward(samples, plan)
	score.Concentration = concentration(samples)
	score.DirectionBreakdown = directionBreakdown(samples)
	return score
}

func samplesForHypothesis(samples []sample, hypothesis string) []sample {
	out := make([]sample, 0, len(samples))
	for _, s := range samples {
		if s.Hypothesis == hypothesis {
			out = append(out, s)
		}
	}
	return out
}

func episodeResults(samples []sample) []EpisodeResult {
	out := make([]EpisodeResult, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.EpisodeResult)
	}
	return out
}

// wfPlan 是在假设筛选之前、基于全部合格样本时间轴建立的锚定 walk-forward 切分方案。
type wfPlan struct {
	bounds         []wfBounds
	purgeMS        int64
	embargoMS      int64
	labelHorizonMS int64
}

type wfBounds struct {
	trainStart time.Time
	trainEnd   time.Time
	testStart  time.Time
	testEnd    time.Time
}

// buildWalkForwardPlan 按时间（而非样本计数）建立锚定扩窗切分：训练期末与测试期首之间留出
// purge+embargo 的时间间隔，且有效 purge 至少覆盖最大 label horizon，杜绝训练标签窥探测试期。
// 无时间跨度则不产生任何折（fail closed，不伪造切分）。
func buildWalkForwardPlan(samples []sample, opts Options) wfPlan {
	labelHorizon := labelHorizonDuration(opts)
	purge := opts.PurgeDuration
	if purge < labelHorizon {
		purge = labelHorizon
	}
	embargo := opts.EmbargoDuration
	plan := wfPlan{purgeMS: purge.Milliseconds(), embargoMS: embargo.Milliseconds(), labelHorizonMS: labelHorizon.Milliseconds()}
	if len(samples) < 2 {
		return plan
	}
	t0 := samples[0].sortTime
	tN := samples[len(samples)-1].sortTime
	span := tN.Sub(t0)
	if span <= 0 {
		return plan
	}
	folds := opts.WalkForwardFolds
	seg := span / time.Duration(folds+1)
	if seg <= 0 {
		return plan
	}
	gap := purge + embargo
	for f := 1; f <= folds; f++ {
		trainEnd := t0.Add(seg * time.Duration(f))
		testStart := trainEnd.Add(gap)
		if !testStart.Before(tN) {
			break
		}
		testEnd := testStart.Add(seg)
		if testEnd.After(tN) {
			testEnd = tN
		}
		plan.bounds = append(plan.bounds, wfBounds{trainStart: t0, trainEnd: trainEnd, testStart: testStart, testEnd: testEnd})
	}
	return plan
}

// evaluateWalkForward 在既定时间切分上评估某一假设：训练期 [trainStart,trainEnd)、
// 测试期 [testStart,testEnd]，落在 [trainEnd,testStart) 时间间隔内的样本被 purge。
func evaluateWalkForward(samples []sample, plan wfPlan) WalkForwardSummary {
	summary := WalkForwardSummary{PurgeMS: plan.purgeMS, EmbargoMS: plan.embargoMS, LabelHorizonMS: plan.labelHorizonMS}
	if len(plan.bounds) == 0 {
		return summary
	}
	for i, b := range plan.bounds {
		var train, test []float64
		purged := 0
		for _, s := range samples {
			t := s.sortTime
			switch {
			case !t.Before(b.trainStart) && t.Before(b.trainEnd):
				train = append(train, s.NetBPS)
			case !t.Before(b.testStart) && !t.After(b.testEnd):
				test = append(test, s.NetBPS)
			case !t.Before(b.trainEnd) && t.Before(b.testStart):
				purged++
			}
		}
		summary.Splits = append(summary.Splits, WalkForwardFold{
			Fold: i + 1, TrainEpisodes: len(train), TestEpisodes: len(test),
			TrainMeanBPS: mean(train), TestMeanBPS: mean(test),
		})
		summary.PurgedEpisodes += purged
	}
	summary.Folds = len(summary.Splits)
	testMeans := make([]float64, 0, len(summary.Splits))
	for _, split := range summary.Splits {
		testMeans = append(testMeans, split.TestMeanBPS)
		if split.TestMeanBPS > 0 {
			summary.PositiveTestFolds++
		}
	}
	summary.TestMeanBPS = mean(testMeans)
	return summary
}

func labelHorizonDuration(opts Options) time.Duration {
	ms := opts.LabelHorizonMS
	if ms <= 0 {
		ms = 60000
	}
	return time.Duration(ms) * time.Millisecond
}

func concentration(samples []sample) ConcentrationReport {
	n := len(samples)
	if n == 0 {
		return ConcentrationReport{}
	}
	return ConcentrationReport{
		MaxSingleInstrumentShare: maxShare(samples, func(s sample) string { return s.Instrument }),
		MaxSingleDayShare:        maxShare(samples, func(s sample) string { return s.Day }),
		MaxSingleMonthShare:      maxShare(samples, func(s sample) string { return s.Month }),
	}
}

func directionBreakdown(samples []sample) DirectionBreakdown {
	var out DirectionBreakdown
	var longVals, shortVals []float64
	for _, s := range samples {
		switch s.Direction {
		case string(mechanism.DirectionLong):
			out.LongEpisodes++
			longVals = append(longVals, s.NetBPS)
		case string(mechanism.DirectionShort):
			out.ShortEpisodes++
			shortVals = append(shortVals, s.NetBPS)
		}
	}
	out.LongNetMean = mean(longVals)
	out.ShortNetMean = mean(shortVals)
	return out
}

// distinctClusters 统计独立 shock cluster 数，忽略空串（不可观测的簇不计为独立簇）。
func distinctClusters(samples []sample) int {
	set := map[string]bool{}
	for _, s := range samples {
		if strings.TrimSpace(s.ShockCluster) == "" {
			continue
		}
		set[s.ShockCluster] = true
	}
	return len(set)
}

func blockBootstrap(samples []sample, draws int, seed int64, key func(sample) string) []float64 {
	if len(samples) == 0 {
		return nil
	}
	blocks := map[string][]float64{}
	keys := []string{}
	for _, s := range samples {
		k := key(s)
		if _, ok := blocks[k]; !ok {
			keys = append(keys, k)
		}
		blocks[k] = append(blocks[k], s.NetBPS)
	}
	if len(keys) == 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, 0, draws)
	for i := 0; i < draws; i++ {
		vals := make([]float64, 0, len(samples))
		for len(vals) < len(samples) {
			block := blocks[keys[rng.Intn(len(keys))]]
			vals = append(vals, block...)
		}
		if len(vals) > len(samples) {
			vals = vals[:len(samples)]
		}
		out = append(out, mean(vals))
	}
	return out
}

func bootstrapMeans(vals []float64, draws int, rng *rand.Rand) []float64 {
	if len(vals) == 0 {
		return nil
	}
	out := make([]float64, 0, draws)
	for i := 0; i < draws; i++ {
		sum := 0.0
		for range vals {
			sum += vals[rng.Intn(len(vals))]
		}
		out = append(out, sum/float64(len(vals)))
	}
	return out
}

func percentileCI(vals []float64) ConfidenceInterval {
	if len(vals) == 0 {
		return ConfidenceInterval{}
	}
	sort.Float64s(vals)
	return ConfidenceInterval{Low: percentileSorted(vals, 0.025), High: percentileSorted(vals, 0.975)}
}

func percentileSorted(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	pos := p * float64(len(vals)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return vals[lo]
	}
	frac := pos - float64(lo)
	return vals[lo]*(1-frac) + vals[hi]*frac
}

func values(samples []sample, pick func(sample) float64) []float64 {
	out := make([]float64, 0, len(samples))
	for _, s := range samples {
		out = append(out, pick(s))
	}
	return out
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func maxShare(samples []sample, key func(sample) string) float64 {
	counts := map[string]int{}
	maxCount := 0
	for _, s := range samples {
		k := key(s)
		counts[k]++
		if counts[k] > maxCount {
			maxCount = counts[k]
		}
	}
	return float64(maxCount) / float64(len(samples))
}

func validRatio(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

func firstString(obj map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := lookup(obj, key)
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstFloat(obj map[string]json.RawMessage, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := lookup(obj, key)
		if !ok {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var n json.Number
		if dec.Decode(&n) == nil {
			v, err := n.Float64()
			if err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

func firstBool(obj map[string]json.RawMessage, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := lookup(obj, key)
		if !ok {
			continue
		}
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b, true
		}
	}
	return false, false
}

func firstTime(obj map[string]json.RawMessage, cardUnixMs int64) time.Time {
	for _, key := range []string{"event_time", "recorded_at", "time"} {
		raw, ok := lookup(obj, key)
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return ts.UTC()
			}
		}
	}
	if v, ok := firstFloat(obj, "event_time_unix_ms", "time_unix_ms"); ok {
		return time.UnixMilli(int64(v)).UTC()
	}
	if cardUnixMs > 0 {
		return time.UnixMilli(cardUnixMs).UTC()
	}
	return time.Time{}
}

func lookup(obj map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			return raw, true
		}
		for existing, raw := range obj {
			if strings.EqualFold(existing, key) {
				return raw, true
			}
		}
	}
	return nil, false
}

func directionSign(direction mechanism.Direction) float64 {
	switch direction {
	case mechanism.DirectionLong:
		return 1
	case mechanism.DirectionShort:
		return -1
	default:
		return 0
	}
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
