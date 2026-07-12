// Package g3reverse implements the G3 outcome-first reverse-induction event
// pool: hourly closed okx_1h bar decision points, long/short × 1h/4h/24h
// events stratified by SkyState + SorterDomain, with four research partitions
// and a winner index for induction-only hypothesis generation.
package g3reverse

import (
	"fmt"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
)

// Horizon is one of the three preregistered outcome windows.
type Horizon string

const (
	Horizon1h  Horizon = "1h"
	Horizon4h  Horizon = "4h"
	Horizon24h Horizon = "24h"
)

func (h Horizon) Valid() bool {
	switch h {
	case Horizon1h, Horizon4h, Horizon24h:
		return true
	default:
		return false
	}
}

func (h Horizon) Hours() int {
	switch h {
	case Horizon1h:
		return 1
	case Horizon4h:
		return 4
	case Horizon24h:
		return 24
	default:
		return 0
	}
}

// OutcomeClass classifies net-after-cost outcome relative to the neutral band.
type OutcomeClass string

const (
	OutcomeWinner  OutcomeClass = "winner"
	OutcomeLoss    OutcomeClass = "loss"
	OutcomeNeutral OutcomeClass = "neutral"
)

func classifyOutcome(net, band float64) OutcomeClass {
	switch {
	case net > band:
		return OutcomeWinner
	case net < -band:
		return OutcomeLoss
	default:
		return OutcomeNeutral
	}
}

// Partition names the four frozen research time slices.
type Partition string

const (
	PartitionInduction Partition = "induction"
	PartitionRecheck   Partition = "recheck"
	PartitionForward   Partition = "forward"
	PartitionWalkFwd   Partition = "walk_forward"
)

// Event is one completed research observation at a closed-bar decision point.
// Features contains only values known at FeatureAsOf; absent keys are missing
// (never silently zero-filled). MissingFeatures lists catalog keys unavailable.
type Event struct {
	ID              string              `json:"id"`
	Symbol          string              `json:"symbol"`
	Cluster         string              `json:"cluster"`
	SkyState        string              `json:"sky_state"`
	SorterDomain    string              `json:"sorter_domain"`
	LiquidityBucket string              `json:"liquidity_bucket"`
	Horizon         Horizon             `json:"horizon"`
	DecisionTime    time.Time           `json:"decision_time"`
	FeatureAsOf     time.Time           `json:"feature_as_of"`
	EntryTime       time.Time           `json:"entry_time"`
	ExitTime        time.Time           `json:"exit_time"`
	EntryPrice      float64             `json:"entry_price"`
	ExitPrice       float64             `json:"exit_price"`
	Direction       mechanism.Direction `json:"direction"`
	Features        map[string]float64  `json:"features"`
	MissingFeatures []string            `json:"missing_features,omitempty"`
	NetOutcomeBPS   float64             `json:"net_outcome_bps"`
	OutcomeClass    OutcomeClass        `json:"outcome_class"`
}

// Config bounds one deterministic event-regeneration run.
type Config struct {
	HistoryRoot     string
	MaxSymbols      int
	MaxFundingLines int
	MaxPriceLines   int
	MaxFamilyLines  int
	ProgressEvery   int
	OnProgress      func(Progress)
	OnReject        func(reason string)
	Catalog         *historydata.HistoryCatalog
	SkyCutpoints    sky.Cutpoints
	Start           time.Time
	End             time.Time
}

// Boundaries defines four partition windows anchored on okx_funding span with
// 168h embargoes between adjacent slices.
type Boundaries struct {
	Start           time.Time `json:"start"`
	InductionEnd    time.Time `json:"induction_end"`
	RecheckStart    time.Time `json:"recheck_start"`
	RecheckEnd      time.Time `json:"recheck_end"`
	ForwardStart    time.Time `json:"forward_start"`
	ForwardEnd      time.Time `json:"forward_end"`
	WFStart         time.Time `json:"wf_start"`
	End             time.Time `json:"end"`
	EmbargoHours    int       `json:"embargo_hours"`
	InductionPerm   int       `json:"reverse_induction_permille"`
	RecheckPerm     int       `json:"recheck_permille"`
	ForwardPerm     int       `json:"forward_permille"`
	WalkForwardPerm int       `json:"walk_forward_permille"`
}

func (b Boundaries) Window(p Partition) (start, end time.Time) {
	switch p {
	case PartitionInduction:
		return b.Start, b.InductionEnd
	case PartitionRecheck:
		return b.RecheckStart, b.RecheckEnd
	case PartitionForward:
		return b.ForwardStart, b.ForwardEnd
	case PartitionWalkFwd:
		return b.WFStart, b.End
	default:
		return time.Time{}, time.Time{}
	}
}

// Progress reports bounded scan progress.
type Progress struct {
	SymbolsDone  int    `json:"symbols_done"`
	SymbolsTotal int    `json:"symbols_total"`
	Events       int    `json:"events"`
	Symbol       string `json:"symbol"`
	Partition    string `json:"partition,omitempty"`
}

// WinnerIndexEntry references one induction-partition winner for hypothesis work.
type WinnerIndexEntry struct {
	EventID       string       `json:"event_id"`
	Symbol        string       `json:"symbol"`
	SkyState      string       `json:"sky_state"`
	SorterDomain  string       `json:"sorter_domain"`
	Horizon       Horizon      `json:"horizon"`
	Direction     string       `json:"direction"`
	DecisionTime  time.Time    `json:"decision_time"`
	NetOutcomeBPS float64      `json:"net_outcome_bps"`
	OutcomeClass  OutcomeClass `json:"outcome_class"`
}

// WinnerIndex is induction-only; full event pools retain loss/neutral elsewhere.
type WinnerIndex struct {
	Generation   string             `json:"generation"`
	ProtocolHash string             `json:"protocol_hash"`
	Entries      []WinnerIndexEntry `json:"entries"`
	IndexHash    string             `json:"index_hash"`
}

// GenerationStats summarizes one four-partition regeneration run.
type GenerationStats struct {
	ExamActions               int                       `json:"exam_actions"`
	EventsByPartition         map[string]int            `json:"events_by_partition"`
	WinnersByPartition        map[string]int            `json:"winners_by_partition"`
	RejectionsByPartition     map[string]map[string]int `json:"rejections_by_partition"`
	EligibleByPartitionAndSky map[string]map[string]int `json:"eligible_by_partition_and_sky"`
	OutcomeByPartition        map[string]map[string]int `json:"outcome_by_partition"`
}

func eventID(symbol string, decisionMs int64, horizon Horizon, direction mechanism.Direction) string {
	return fmt.Sprintf("%s-%d-%s-%s", symbol, decisionMs, horizon, direction)
}

func isFinite(v float64) bool {
	return !((v != v) || v > 1e308 || v < -1e308)
}
