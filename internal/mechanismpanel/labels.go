package mechanismpanel

import "time"

// pendingLabel 是 FutureLabel 的工作态,只允许通过 absorbFuture 写入数据,且 absorbFuture
// 对 t<=decisionTime 的调用是无操作的(物理分离的运行时闸门,而非仅靠约定)。
type pendingLabel struct {
	episodeID    string
	rawEpisodeID string
	phase        PhaseKind
	instrument   string
	decisionTime time.Time
	decisionMid  float64

	horizonsMs []int64
	results    []LabelHorizon
	touched    []bool // 是否已经至少收到一个 offset<=horizon 的样本(用于运行中 max/min 的首触发初始化)
	frozen     []bool // 是否已经到达/越过 horizon,不再接受新样本

	future         []PricePathPoint
	maxHorizonMs   int64
	maxStalenessMs int64
}

func newPendingLabel(episodeID, instrument string, decisionTime time.Time, decisionMid float64, horizons []time.Duration) *pendingLabel {
	return newPendingLabelForPhase(episodeID, episodeID, "", instrument, decisionTime, decisionMid, horizons)
}

func newPendingLabelForPhase(episodeID, rawEpisodeID string, phase PhaseKind, instrument string, decisionTime time.Time, decisionMid float64, horizons []time.Duration) *pendingLabel {
	pl := &pendingLabel{
		episodeID:      episodeID,
		rawEpisodeID:   rawEpisodeID,
		phase:          phase,
		instrument:     instrument,
		decisionTime:   decisionTime,
		decisionMid:    decisionMid,
		maxStalenessMs: UnifiedMaxLabelStalenessMs,
	}
	for _, h := range horizons {
		hm := h.Milliseconds()
		pl.horizonsMs = append(pl.horizonsMs, hm)
		pl.results = append(pl.results, LabelHorizon{HorizonMs: hm})
		pl.touched = append(pl.touched, false)
		pl.frozen = append(pl.frozen, false)
		if hm > pl.maxHorizonMs {
			pl.maxHorizonMs = hm
		}
	}
	return pl
}

// absorbFuture 是本包唯一允许把"未来数据"写入标签状态的函数。守卫条件
// !t.After(decisionTime) 保证决策时刻及之前的任何数据都不可能进入 FutureLabel——
// 即使调用方(编程错误地)把过去数据传进来,也会被这里拦截而不是被物理分离规则"信任"。
func (pl *pendingLabel) absorbFuture(t time.Time, mid float64) {
	if !t.After(pl.decisionTime) {
		return
	}
	offsetMs := t.Sub(pl.decisionTime).Milliseconds()
	pl.future = append(pl.future, PricePathPoint{OffsetMs: offsetMs, Mid: mid})

	haveRet := pl.decisionMid > 0 && mid > 0
	var retBps float64
	if haveRet {
		retBps = (mid/pl.decisionMid - 1) * 10000
	}
	for i, hm := range pl.horizonsMs {
		if pl.frozen[i] {
			continue
		}
		if offsetMs <= hm && haveRet {
			r := &pl.results[i]
			if !pl.touched[i] {
				pl.touched[i] = true
				r.MaxAdverseBps = retBps
				r.MaxFavorableBps = retBps
			} else {
				if retBps < r.MaxAdverseBps {
					r.MaxAdverseBps = retBps
				}
				if retBps > r.MaxFavorableBps {
					r.MaxFavorableBps = retBps
				}
			}
		}
		if offsetMs >= hm {
			r := &pl.results[i]
			if haveRet && offsetMs-hm <= pl.maxStalenessMs {
				r.ForwardRetBps = retBps
				r.Observed = true
			}
			pl.frozen[i] = true
		}
	}
}

// isDone 判断该 pendingLabel 是否已经不再需要更多未来数据:所有 horizon 都已冻结,
// 或者已经越过最大 horizon(此后无论怎样也不会再有新 horizon 首次达标)。
func (pl *pendingLabel) isDone() bool {
	allFrozen := true
	for _, f := range pl.frozen {
		if !f {
			allFrozen = false
			break
		}
	}
	if allFrozen {
		return true
	}
	if len(pl.future) == 0 {
		return false
	}
	last := pl.future[len(pl.future)-1]
	return last.OffsetMs > pl.maxHorizonMs
}

func (pl *pendingLabel) finalize() FutureLabel {
	complete := true
	for i, f := range pl.frozen {
		if !f || !pl.results[i].Observed {
			complete = false
			break
		}
	}
	rawEpisodeID := pl.rawEpisodeID
	if rawEpisodeID == "" {
		rawEpisodeID = pl.episodeID
	}
	return FutureLabel{
		EpisodeID:       pl.episodeID,
		RawEpisodeID:    rawEpisodeID,
		Phase:           pl.phase,
		Instrument:      pl.instrument,
		DecisionTime:    pl.decisionTime,
		Horizons:        append([]LabelHorizon(nil), pl.results...),
		FuturePricePath: append([]PricePathPoint(nil), pl.future...),
		Complete:        complete,
	}
}
