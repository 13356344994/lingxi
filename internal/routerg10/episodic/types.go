package episodic

import "time"

// StateVector is the frozen continuous market state at decision time.
type StateVector struct {
	ReturnBPS         float64 `json:"return_bps"`
	PriorReturnBPS    float64 `json:"prior_return_bps"`
	BTCRelativeBPS    float64 `json:"btc_relative_bps"`
	BarRangeBPS       float64 `json:"bar_range_bps"`
	SessionBucket     string  `json:"session_bucket,omitempty"`
	LiquidityBucket   string  `json:"liquidity_bucket,omitempty"`
	RollingVolumeUSDT float64 `json:"rolling_volume_usdt"`
}

// PersonalExecSnapshot captures personal execution conditions at decision time.
type PersonalExecSnapshot struct {
	FeeDeltaBPS      float64 `json:"fee_delta_bps"`
	SlipDeltaBPS     float64 `json:"slip_delta_bps"`
	LatencyP90MS     float64 `json:"latency_p90_ms"`
	FingerprintKnown bool    `json:"fingerprint_known"`
}

// ConservativeOutcome is the frozen post-event label stored with the episode.
type ConservativeOutcome struct {
	NetBPS       float64 `json:"conservative_net_bps"`
	StressNetBPS float64 `json:"stress_net_bps"`
	Win          bool    `json:"win"`
}

// ActionPath records the executed action sequence for one historical event.
type ActionPath []string

// FrozenEpisode is one sealed historical case in episodic memory.
type FrozenEpisode struct {
	SchemaVersion   string                `json:"schema_version"`
	Generation      string                `json:"generation"`
	ProtocolHash    string                `json:"protocol_hash"`
	EpisodeHash     string                `json:"episode_hash"`
	EventID         string                `json:"event_id"`
	Symbol          string                `json:"symbol"`
	ProfileID       string                `json:"profile_id"`
	EventFamily     string                `json:"event_family"`
	EventPhase      float64               `json:"event_phase"`
	DecisionTime    time.Time             `json:"decision_time"`
	State           StateVector           `json:"state"`
	PersonalExec    PersonalExecSnapshot  `json:"personal_exec"`
	ActionPath      ActionPath            `json:"action_path"`
	Outcome         ConservativeOutcome   `json:"outcome"`
	FingerprintHash string                `json:"fingerprint_hash,omitempty"`
}

// QueryVector is the similarity query; must not include outcome fields.
type QueryVector struct {
	Symbol       string               `json:"symbol"`
	ProfileID    string               `json:"profile_id"`
	EventFamily  string               `json:"event_family"`
	EventPhase   float64              `json:"event_phase"`
	State        StateVector          `json:"state"`
	PersonalExec PersonalExecSnapshot `json:"personal_exec"`
}

// TraceMemoryHit is a decision-time memory retrieval row without outcome labels.
type TraceMemoryHit struct {
	EpisodeHash string  `json:"episode_hash"`
	EventID     string  `json:"event_id"`
	Similarity  float64 `json:"similarity"`
}

// TraceHitsFrom strips outcome labels before BrainTrace/state hashing.
func TraceHitsFrom(hits []RetrievalHit) []TraceMemoryHit {
	out := make([]TraceMemoryHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, TraceMemoryHit{
			EpisodeHash: h.EpisodeHash,
			EventID:     h.EventID,
			Similarity:  h.Similarity,
		})
	}
	return out
}

// RetrievalHit is one ranked similar episode.
type RetrievalHit struct {
	EpisodeHash string  `json:"episode_hash"`
	EventID     string  `json:"event_id"`
	Similarity  float64 `json:"similarity"`
	OutcomeWin  bool    `json:"outcome_win"`
	OutcomeBPS  float64 `json:"conservative_net_bps"`
}

// MemoryAdvice summarizes episodic evidence for counterfactual gating.
type MemoryAdvice struct {
	SchemaVersion  string         `json:"schema_version"`
	HitCount       int            `json:"hit_count"`
	WinCount       int            `json:"win_count"`
	LossCount      int            `json:"loss_count"`
	VetoOpen       bool           `json:"veto_open"`
	SizeScale      float64        `json:"size_scale"`
	AdviceCodes    []string       `json:"advice_codes"`
	Hits           []RetrievalHit `json:"hits"`
	ResidualStatus string         `json:"residual_status"`
}

// MemoryIndex is a sealed episodic store artifact.
type MemoryIndex struct {
	SchemaVersion string          `json:"schema_version"`
	Generation    string          `json:"generation"`
	ProtocolHash  string          `json:"protocol_hash"`
	BuiltAt       time.Time       `json:"built_at"`
	Episodes      []FrozenEpisode `json:"episodes"`
	IndexHash     string          `json:"index_hash"`
}
