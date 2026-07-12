package mechanismpanel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"lingxi/internal/datatruth"
)

// pendingLabelSnapshot 是 pendingLabel 的可落盘镶像(pendingLabel 本身字段未导出,
// 避免把内部工作态误当输出格式使用)。
type pendingLabelSnapshot struct {
	EpisodeID    string           `json:"episode_id"`
	Instrument   string           `json:"instrument"`
	DecisionTime time.Time        `json:"decision_time"`
	DecisionMid  float64          `json:"decision_mid"`
	HorizonsMs   []int64          `json:"horizons_ms"`
	Results      []LabelHorizon   `json:"results"`
	Touched      []bool           `json:"touched"`
	Frozen       []bool           `json:"frozen"`
	Future       []PricePathPoint `json:"future"`
	MaxHorizonMs int64            `json:"max_horizon_ms"`
}

func (pl *pendingLabel) snapshot() pendingLabelSnapshot {
	return pendingLabelSnapshot{
		EpisodeID:    pl.episodeID,
		Instrument:   pl.instrument,
		DecisionTime: pl.decisionTime,
		DecisionMid:  pl.decisionMid,
		HorizonsMs:   append([]int64(nil), pl.horizonsMs...),
		Results:      append([]LabelHorizon(nil), pl.results...),
		Touched:      append([]bool(nil), pl.touched...),
		Frozen:       append([]bool(nil), pl.frozen...),
		Future:       append([]PricePathPoint(nil), pl.future...),
		MaxHorizonMs: pl.maxHorizonMs,
	}
}

func restorePendingLabel(s pendingLabelSnapshot) *pendingLabel {
	return &pendingLabel{
		episodeID:    s.EpisodeID,
		instrument:   s.Instrument,
		decisionTime: s.DecisionTime,
		decisionMid:  s.DecisionMid,
		horizonsMs:   append([]int64(nil), s.HorizonsMs...),
		results:      append([]LabelHorizon(nil), s.Results...),
		touched:      append([]bool(nil), s.Touched...),
		frozen:       append([]bool(nil), s.Frozen...),
		future:       append([]PricePathPoint(nil), s.Future...),
		maxHorizonMs: s.MaxHorizonMs,
	}
}

// AggregatorSnapshot 是 Aggregator 全部工作态的可落盘镶像,足以在进程重启后逐字节还原
// (bounded 状态,不含任何源文件内容本身)。
type AggregatorSnapshot struct {
	Watermark   time.Time                        `json:"watermark"`
	EpisodeSeq  int64                            `json:"episode_seq"`
	Open        []*openEpisode                   `json:"open"`
	Pending     []pendingLabelSnapshot           `json:"pending"`
	LatestMid   map[string]timedPoint            `json:"latest_mid"`
	LatestOI    map[string]map[string]timedPoint `json:"latest_oi"`
	LatestDepth map[string]DepthRecord           `json:"latest_depth"`
}

// Snapshot 导出 Aggregator 当前的全部工作态。
func (a *Aggregator) Snapshot() AggregatorSnapshot {
	snap := AggregatorSnapshot{
		Watermark:   a.watermark,
		EpisodeSeq:  a.episodeSeq,
		LatestMid:   a.latestMid,
		LatestOI:    a.latestOI,
		LatestDepth: a.latestDepth,
	}
	for _, ep := range a.open {
		snap.Open = append(snap.Open, ep)
	}
	for _, pl := range a.pending {
		snap.Pending = append(snap.Pending, pl.snapshot())
	}
	return snap
}

// RestoreAggregator 从落盘的 AggregatorSnapshot 重建一个可继续消费新记录的 Aggregator。
func RestoreAggregator(cfg AggregatorConfig, snap AggregatorSnapshot, onFeature func(EntryFeatures), onLabel func(FutureLabel)) *Aggregator {
	a := NewAggregator(cfg, onFeature, onLabel)
	a.watermark = snap.Watermark
	a.episodeSeq = snap.EpisodeSeq
	if snap.LatestMid != nil {
		a.latestMid = snap.LatestMid
	}
	if snap.LatestOI != nil {
		a.latestOI = snap.LatestOI
	}
	if snap.LatestDepth != nil {
		a.latestDepth = snap.LatestDepth
	}
	for _, ep := range snap.Open {
		a.open[ep.Instrument] = ep
	}
	for _, ps := range snap.Pending {
		a.pending = append(a.pending, restorePendingLabel(ps))
	}
	return a
}

// ===== 红队#12:checkpoint 必须绑定输入文件身份与完整配置身份,resume 时任何不一致都要
// fail closed(拒绝恢复),不能悄悄继续(可能跳过/错位数据)也不能悄悄清零重跑(会让已经
// 落盘的 features/labels 文件被重复 append)。=====

// inputIdentityPrefixBytes 是身份指纹读取的文件头部大小上限:无论文件本身多大(生产
// records 可能是几个GB的增量日志),这里永远只做一次 bounded 的头部读取,不整份扫描。
const inputIdentityPrefixBytes = 64 * 1024

// InputIdentity 是单路输入文件在某次 checkpoint 落盘时刻的身份指纹:路径 + 大小 + mtime
// + 文件头部哈希。增量日志文件只会在末尾追加,所以头部哈希在文件长大之后依然稳定;
// 一旦头部哈希或路径变了,基本可以断定"这不是原来那份文件"(被替换/被截断重写)。
type InputIdentity struct {
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	ModTime    time.Time `json:"mod_time"`
	PrefixHash string    `json:"prefix_hash"`
}

// computeInputIdentity 计算单个输入文件"此刻"的身份指纹,哈希覆盖文件头部至多
// inputIdentityPrefixBytes 字节——用于落盘 checkpoint 时记录"文件现在长这样"。是一次
// O(1) 的 bounded 操作,不管文件实际有多大。
//
// 注意:这个哈希只在"文件大小不再变化"的前提下可以直接互相比较;对一份还在增长的
// 增量日志,两次分别在不同大小时调用本函数会得到不同的哈希(因为窗口末尾的字节不同)。
// resume 时的一致性判断不能直接比较两次 computeInputIdentity 的结果,必须用
// hashInputPrefix 只重新哈希"checkpoint 记录时已经存在的那一段"(见 VerifyCheckpointIdentity)。
func computeInputIdentity(path string) (InputIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return InputIdentity{}, fmt.Errorf("mechanismpanel: stat input %s: %w", path, err)
	}
	hash, err := hashInputPrefix(path, info.Size())
	if err != nil {
		return InputIdentity{}, err
	}
	return InputIdentity{
		Path:       path,
		SizeBytes:  info.Size(),
		ModTime:    info.ModTime().UTC(),
		PrefixHash: hash,
	}, nil
}

// hashInputPrefix 哈希 path 的前 min(upTo, inputIdentityPrefixBytes) 字节,是一次
// bounded 读取,不管文件实际有多大。resume 时用它重新哈希"checkpoint 记录时文件已经
// 有多大"这段范围,而不是当前(可能已经长大的)文件大小,这样对一份只会被追加、从不
// 修改已写内容的增量日志,不管它后来长到多大,同一段头部的哈希永远保持不变。
func hashInputPrefix(path string, upTo int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("mechanismpanel: open input %s for identity: %w", path, err)
	}
	defer f.Close()
	if upTo > inputIdentityPrefixBytes {
		upTo = inputIdentityPrefixBytes
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, upTo); err != nil && err != io.EOF {
		return "", fmt.Errorf("mechanismpanel: hash input %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// computeInputIdentities 为 SourcePaths 中每个非空路径计算身份指纹,key 与
// streamKeyForKind(见 pipeline.go)保持一致的命名空间,外加一次性载入的
// instrument_catalog(它同样是"输入文件身份"的一部分,只是没有流式 offset)。
func computeInputIdentities(paths SourcePaths) (map[string]InputIdentity, error) {
	out := map[string]InputIdentity{}
	for key, path := range activeSourcePaths(paths) {
		id, err := computeInputIdentity(path)
		if err != nil {
			return nil, err
		}
		out[key] = id
	}
	return out, nil
}

// AggregatorFingerprint 是 AggregatorConfig 里"改了就意味着不同 generation"的全部阈值
// 字段(不含 Now/InstrumentCatalog:前者是时钟注入,后者本身是一路独立输入,已经由
// InputIdentity 的 instrument_catalog 项覆盖)。
type AggregatorFingerprint struct {
	GapThreshold                time.Duration   `json:"gap_threshold"`
	MaxEpisodeDuration          time.Duration   `json:"max_episode_duration"`
	MinTriggerNotionalUSDT      float64         `json:"min_trigger_notional_usdt"`
	FeatureLagMs                int64           `json:"feature_lag_ms"`
	ExhaustionAbsorptionMin     float64         `json:"exhaustion_absorption_min"`
	ExhaustionMaxRemainingRatio float64         `json:"exhaustion_max_remaining_ratio"`
	MaxOpenEpisodes             int             `json:"max_open_episodes"`
	MaxPendingLabels            int             `json:"max_pending_labels"`
	MaxContextBuffer            int             `json:"max_context_buffer"`
	LabelHorizons               []time.Duration `json:"label_horizons"`
	MaxLabelStaleness           time.Duration   `json:"max_label_staleness"`
}

func newAggregatorFingerprint(cfg AggregatorConfig) AggregatorFingerprint {
	return AggregatorFingerprint{
		GapThreshold:                cfg.GapThreshold,
		MaxEpisodeDuration:          cfg.MaxEpisodeDuration,
		MinTriggerNotionalUSDT:      cfg.MinTriggerNotionalUSDT,
		FeatureLagMs:                cfg.FeatureLagMs,
		ExhaustionAbsorptionMin:     cfg.ExhaustionAbsorptionMin,
		ExhaustionMaxRemainingRatio: cfg.ExhaustionMaxRemainingRatio,
		MaxOpenEpisodes:             cfg.MaxOpenEpisodes,
		MaxPendingLabels:            cfg.MaxPendingLabels,
		MaxContextBuffer:            cfg.MaxContextBuffer,
		LabelHorizons:               append([]time.Duration(nil), cfg.LabelHorizons...),
		MaxLabelStaleness:           cfg.MaxLabelStaleness,
	}
}

// ConfigFingerprint 是"完整配置身份"里,输入文件身份之外、真正影响输出语义的全部旋钮:
// episode 聚合阈值 + 日期过滤 + 迟到判定窗口。刻意排除纯运行期旋钮
// (MaxLines/ProgressEveryN/OnProgress/CheckpointPath/FinalizeAtEOF/Now)——它们只决定
// "这一次跑多远/多吵/末尾是否强制收口",不改变已经产出、已经落盘的 features/labels 的
// 含义,允许在同一 generation 内的多次 resume 之间自由调整(例如:中途 resume 不
// finalize,直到确认到达真实 EOF 的最后一次才加 --finalize)。
type ConfigFingerprint struct {
	Aggregator       AggregatorFingerprint `json:"aggregator"`
	Date             string                `json:"date"`
	LateAfter        time.Duration         `json:"late_after"`
	MaxDedupeEntries int                   `json:"max_dedupe_entries"`
}

func newConfigFingerprint(cfg Config) ConfigFingerprint {
	maxDedupeEntries := cfg.MaxDedupeEntries
	if maxDedupeEntries <= 0 {
		maxDedupeEntries = UnifiedMaxDedupeEntries
	}
	return ConfigFingerprint{
		Aggregator:       newAggregatorFingerprint(cfg.Aggregator),
		Date:             cfg.Date,
		LateAfter:        cfg.LateAfter,
		MaxDedupeEntries: maxDedupeEntries,
	}
}

// hash 返回该指纹的稳定摘要,只用于日志/错误信息里给人看;实际一致性判断始终用
// reflect.DeepEqual 逐字段比较(见 VerifyCheckpointIdentity),避免哈希碰撞掩盖差异。
func (f ConfigFingerprint) hash() string {
	b, err := json.Marshal(f)
	if err != nil {
		return "unhashable:" + err.Error()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ErrCheckpointMismatch 是 resume 时输入文件身份或配置身份与 checkpoint 记录不一致的
// 错误类;调用方(NewPipeline)必须据此拒绝恢复(fail closed),不能带着不一致的
// offset/config 继续跑,也不能悄悄清空 checkpoint 从零开始。
var ErrCheckpointMismatch = errors.New("mechanismpanel: checkpoint identity mismatch")

// VerifyCheckpointIdentity 校验一个已加载的 checkpoint 记录的输入文件身份与配置指纹
// 是否与当前 cfg 一致(红队#12)。任何不一致都返回一个 wrap 了 ErrCheckpointMismatch
// 的错误。只读取每个输入文件的头部(inputIdentityPrefixBytes),不扫描整份文件。
func VerifyCheckpointIdentity(cfg Config, cp Checkpoint) error {
	if cp.Version != checkpointVersion {
		return fmt.Errorf("%w: checkpoint version %d does not carry input/config identity (want version %d)",
			ErrCheckpointMismatch, cp.Version, checkpointVersion)
	}
	wantPaths := activeSourcePaths(cfg.Sources)
	if len(wantPaths) != len(cp.InputIdentities) {
		return fmt.Errorf("%w: source set changed (checkpoint has %d input(s), config has %d)",
			ErrCheckpointMismatch, len(cp.InputIdentities), len(wantPaths))
	}
	for key, path := range wantPaths {
		recorded, ok := cp.InputIdentities[key]
		if !ok {
			return fmt.Errorf("%w: source %q is new, not present in checkpoint", ErrCheckpointMismatch, key)
		}
		if recorded.Path != path {
			return fmt.Errorf("%w: source %q path changed (checkpoint=%q, current=%q)",
				ErrCheckpointMismatch, key, recorded.Path, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("mechanismpanel: stat input %s: %w", path, err)
		}
		if info.Size() < recorded.SizeBytes {
			return fmt.Errorf("%w: source %q shrank since checkpoint (was %d bytes, now %d bytes)",
				ErrCheckpointMismatch, key, recorded.SizeBytes, info.Size())
		}
		// 只重新哈希"checkpoint 记录时文件已经有多大"这段范围,而不是当前(可能已经
		// 长大很多的)完整文件——append-only 增量日志的这段范围永远不变,重新读取也
		// 始终 bounded 在 inputIdentityPrefixBytes 之内。
		rehash, err := hashInputPrefix(path, recorded.SizeBytes)
		if err != nil {
			return err
		}
		if rehash != recorded.PrefixHash {
			return fmt.Errorf("%w: source %q file identity changed (checkpoint prefix_hash=%s, current=%s); file was likely replaced or truncated-and-rewritten",
				ErrCheckpointMismatch, key, recorded.PrefixHash, rehash)
		}
	}
	want := newConfigFingerprint(cfg)
	if !reflect.DeepEqual(want, cp.ConfigFingerprint) {
		return fmt.Errorf("%w: config fingerprint changed (checkpoint=%s, current=%s)",
			ErrCheckpointMismatch, cp.ConfigHash, want.hash())
	}
	return nil
}

// activeSourcePaths 枚举 SourcePaths 中每个非空路径,key 与 computeInputIdentities
// 一致,只用于比对"当前配置下活跃的输入源集合"跟 checkpoint 记录的 key 集合是否一致
// (源被新增/移除会改变 join 拓扑,必须 fail closed)。
func activeSourcePaths(paths SourcePaths) map[string]string {
	out := map[string]string{}
	add := func(key, path string) {
		if path != "" {
			out[key] = path
		}
	}
	add("okx_liquidation", paths.Liquidation)
	add("xvenue_liquidation", paths.XVenueLiquidation)
	add("market_mids", paths.MarketMids)
	add("xmeta_oi", paths.XMetaOI)
	add("depth", paths.Depth)
	add("instrument_catalog", paths.InstrumentCatalog)
	return out
}

// Checkpoint 是整条 pipeline(多源读取位置 + 去重/乱序状态 + episode 聚合状态 + 进度计数)
// 的完整续读凭证。InputIdentities/ConfigFingerprint/ConfigHash 绑定了产出这份 checkpoint
// 时的输入文件身份与完整配置身份(红队#12);resume 前必须先用 VerifyCheckpointIdentity
// 校验,不一致必须 fail closed。
type Checkpoint struct {
	Version           int                              `json:"version"`
	SourceOffsets     map[string]int64                 `json:"source_offsets"`
	StreamStates      map[string]datatruth.StreamState `json:"stream_states"`
	LinesProcessed    int64                            `json:"lines_processed"`
	FeaturesEmitted   int64                            `json:"features_emitted"`
	LabelsEmitted     int64                            `json:"labels_emitted"`
	Aggregator        AggregatorSnapshot               `json:"aggregator"`
	InputIdentities   map[string]InputIdentity         `json:"input_identities"`
	ConfigFingerprint ConfigFingerprint                `json:"config_fingerprint"`
	ConfigHash        string                           `json:"config_hash"`
	SavedAt           time.Time                        `json:"saved_at"`
}

const checkpointVersion = 2

// SaveCheckpoint 原子落盘(临时文件 + rename),与本仓库其它状态文件的写法一致。
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.Version = checkpointVersion
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("mechanismpanel: marshal checkpoint: %w", err)
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mechanismpanel: mkdir checkpoint dir: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("mechanismpanel: write checkpoint tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("mechanismpanel: rename checkpoint: %w", err)
	}
	return nil
}

// LoadCheckpoint 读取一个已存在的 checkpoint;文件不存在时返回 (zero, false, nil)。
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, false, nil
		}
		return Checkpoint{}, false, fmt.Errorf("mechanismpanel: read checkpoint: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return Checkpoint{}, false, fmt.Errorf("mechanismpanel: decode checkpoint: %w", err)
	}
	return cp, true, nil
}
