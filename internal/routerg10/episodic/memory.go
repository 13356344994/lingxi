package episodic

import (
	"sort"
	"time"

	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventmap"
)

// Store holds in-memory episodic episodes for retrieval.
type Store struct {
	episodes []FrozenEpisode
}

// NewStore wraps a slice of frozen episodes.
func NewStore(episodes []FrozenEpisode) *Store {
	cp := append([]FrozenEpisode(nil), episodes...)
	return &Store{episodes: cp}
}

// Len returns episode count.
func (s *Store) Len() int { return len(s.episodes) }

// Retrieve returns up to MaxRetrievalK similar episodes sorted by similarity desc.
func (s *Store) Retrieve(q QueryVector, rawFields map[string]float64) ([]RetrievalHit, error) {
	if err := ValidateQuery(q, rawFields); err != nil {
		return nil, err
	}
	type scored struct {
		ep   FrozenEpisode
		score float64
	}
	var ranked []scored
	for _, ep := range s.episodes {
		ranked = append(ranked, scored{ep: ep, score: SimilarityScore(q, ep)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].ep.EventID < ranked[j].ep.EventID
		}
		return ranked[i].score > ranked[j].score
	})
	k := MaxRetrievalK
	if len(ranked) < k {
		k = len(ranked)
	}
	hits := make([]RetrievalHit, 0, k)
	for i := 0; i < k; i++ {
		ep := ranked[i].ep
		hits = append(hits, RetrievalHit{
			EpisodeHash: ep.EpisodeHash,
			EventID:     ep.EventID,
			Similarity:  ranked[i].score,
			OutcomeWin:  ep.Outcome.Win,
			OutcomeBPS:  ep.Outcome.NetBPS,
		})
	}
	return hits, nil
}

// Advise converts retrieval hits into open-gate memory advice.
func Advise(hits []RetrievalHit, residual ResidualModel) MemoryAdvice {
	advice := MemoryAdvice{
		SchemaVersion:  SchemaVersionAdvice,
		HitCount:       len(hits),
		SizeScale:      1.0,
		ResidualStatus: residual.Status(),
		Hits:           append([]RetrievalHit(nil), hits...),
	}
	for _, h := range hits {
		if h.OutcomeWin {
			advice.WinCount++
		} else {
			advice.LossCount++
		}
	}
	if advice.HitCount == 0 {
		advice.SizeScale = 0
		advice.AdviceCodes = append(advice.AdviceCodes, AdviceInsufficientSample)
		return advice
	}
	if advice.HitCount < MinSampleForFullSize {
		advice.SizeScale = float64(advice.HitCount) / float64(MinSampleForFullSize)
		advice.AdviceCodes = append(advice.AdviceCodes, AdviceInsufficientSample, AdviceReduceSize)
	}
	lossFrac := float64(advice.LossCount) / float64(advice.HitCount)
	if lossFrac > NegativeMajorityFrac {
		advice.VetoOpen = true
		advice.SizeScale = 0
		advice.AdviceCodes = append(advice.AdviceCodes, AdviceVetoOpen)
	} else if advice.SizeScale > 0 {
		advice.AdviceCodes = append(advice.AdviceCodes, AdviceNoVeto)
	}
	if residual.Enabled() && advice.HitCount > 0 {
		adj := residual.AdjustSizeScale(advice.SizeScale, hits)
		advice.SizeScale = adj
		if adj < 1.0 && !containsCode(advice.AdviceCodes, AdviceReduceSize) {
			advice.AdviceCodes = append(advice.AdviceCodes, AdviceReduceSize)
		}
	}
	return advice
}

// RetrieveAndAdvise is the combined retrieval + advice path.
func (s *Store) RetrieveAndAdvise(q QueryVector, rawFields map[string]float64, residual ResidualModel) (MemoryAdvice, error) {
	hits, err := s.Retrieve(q, rawFields)
	if err != nil {
		return MemoryAdvice{}, err
	}
	return Advise(hits, residual), nil
}

// BuildEpisodeFromEvent constructs one frozen episode from decision-time features and outcome.
func BuildEpisodeFromEvent(
	ev eventmap.DetectedEvent,
	out eventmap.EventOutcome,
	profileID string,
	phase float64,
	actionPath ActionPath,
	fp fingerprint.PersonalExecutionFingerprint,
	protocolHash string,
) (FrozenEpisode, error) {
	adj := residualmap.LookupPersonalAdjustments(fp, ev.Symbol, out.Direction)
	ep := FrozenEpisode{
		SchemaVersion: SchemaVersionEpisode,
		Generation:    Generation,
		ProtocolHash:  protocolHash,
		EventID:       ev.ID,
		Symbol:        ev.Symbol,
		ProfileID:     profileID,
		EventFamily:   ev.Family,
		EventPhase:    phase,
		DecisionTime:  ev.DecisionTime,
		State: StateVector{
			ReturnBPS:         ev.ReturnBPS,
			PriorReturnBPS:    ev.PriorReturnBPS,
			BTCRelativeBPS:    ev.BTCRelativeBPS,
			SessionBucket:     ev.SessionBucket,
			LiquidityBucket:   ev.LiquidityBucket,
			RollingVolumeUSDT: 0,
		},
		PersonalExec: PersonalExecSnapshot{
			FeeDeltaBPS:      adj.FeeDeltaBPS,
			SlipDeltaBPS:     adj.SlipDeltaBPS,
			LatencyP90MS:     adj.LatencyP90MS,
			FingerprintKnown: !adj.CriticalUnknown,
		},
		ActionPath: actionPath,
		Outcome: ConservativeOutcome{
			NetBPS:       out.ConservativeBPS,
			StressNetBPS: out.ConservativeBPS * protocol.CostStressMultiplier,
			Win:          out.ConservativeBPS >= protocol.PersonalConservativeExpectedNetMin,
		},
		FingerprintHash: fp.FingerprintHash,
	}
	hash, err := HashEpisode(ep)
	if err != nil {
		return FrozenEpisode{}, err
	}
	ep.EpisodeHash = hash
	return ep, nil
}

// SealIndex builds a sealed memory index artifact.
func SealIndex(episodes []FrozenEpisode, protocolHash string, builtAt time.Time) (MemoryIndex, error) {
	idx := MemoryIndex{
		SchemaVersion: SchemaVersionIndex,
		Generation:    Generation,
		ProtocolHash:  protocolHash,
		BuiltAt:       builtAt.UTC(),
		Episodes:      append([]FrozenEpisode(nil), episodes...),
	}
	hash, err := HashIndex(idx)
	if err != nil {
		return MemoryIndex{}, err
	}
	idx.IndexHash = hash
	return idx, nil
}

func containsCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}
