// Package accountstate implements the sealed-label neutral account runner.
// It deliberately accepts prices, funding, and model targets only; it neither
// imports StateRowV2 nor exposes future or terminal PnL to a policy.
package accountstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"

	"lingxi/internal/datatruth"
)

const (
	SchemaVersion   = "lingxi.data-driven-router-v3.account-state.v1"
	ProtocolG6Hash  = "08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b"
	maxLeverage     = 5.0
	microUSDT       = 1_000_000
	floatComparison = 1e-12
)

type Direction string

const (
	Flat  Direction = "FLAT"
	Long  Direction = "LONG"
	Short Direction = "SHORT"
)

type DerivedVerb string

const (
	NoTrade   DerivedVerb = "NO_TRADE"
	OpenLong  DerivedVerb = "LONG"
	OpenShort DerivedVerb = "SHORT"
	Hold      DerivedVerb = "HOLD"
	Add       DerivedVerb = "ADD"
	Trim      DerivedVerb = "TRIM"
	Close     DerivedVerb = "CLOSE"
	Reverse   DerivedVerb = "REVERSE"
)

// Position is intentionally a fixed JSON schema. All values are observed at
// DecisionTime; terminal PnL is not represented anywhere in AccountContext.
type Position struct {
	Direction                   Direction `json:"direction"`
	SignedPositionFraction      float64   `json:"signed_position_fraction"`
	FixedNotionalUSDT           float64   `json:"fixed_notional_usdt"`
	WeightedAverageEntryPrice   float64   `json:"weighted_average_entry_price"`
	OpenedAtMS                  int64     `json:"opened_at_ms"`
	PositionAgeBars             int       `json:"position_age_bars"`
	CumulativeSignedFundingUSDT float64   `json:"cumulative_signed_funding_usdt"`
}

type Pending struct {
	Intent        TargetIntent `json:"intent"`
	SubmittedAtMS int64        `json:"submitted_at_ms"`
	MaturesAtBar  int          `json:"matures_at_bar"`
}

type TargetIntent struct {
	TargetSignedPositionFraction float64 `json:"target_signed_position_fraction"`
	TargetHorizonHours           int     `json:"target_horizon_hours"`
	DelayBars                    int     `json:"delay_bars"`
}

type AccountContext struct {
	SchemaVersion                string    `json:"schema_version"`
	PositionDirection            Direction `json:"position_direction"`
	SignedPositionFraction       float64   `json:"signed_position_fraction"`
	FixedNotionalUSDT            float64   `json:"fixed_notional_usdt"`
	WeightedAverageEntryPrice    float64   `json:"weighted_average_entry_price"`
	OpenedAtMS                   int64     `json:"opened_at_ms"`
	PositionAgeBars              int       `json:"position_age_bars"`
	DecisionTimeUnrealizedReturn float64   `json:"decision_time_unrealized_return"`
	CumulativeSignedFundingUSDT  float64   `json:"cumulative_signed_funding_usdt"`
	CashUSDT                     float64   `json:"cash_usdt"`
	MarkedEquityUSDT             float64   `json:"marked_equity_usdt"`
	RetainedCapitalBaseUSDT      float64   `json:"retained_capital_base_usdt"`
	CumulativeWithdrawalsUSDT    float64   `json:"cumulative_withdrawals_usdt"`
	GrossLeverage                float64   `json:"gross_leverage"`
	FreeEquityUSDT               float64   `json:"free_equity_usdt"`
	OpenPositionCount            int       `json:"open_position_count"`
	PendingActionCount           int       `json:"pending_action_count"`
}

type TransitionTrace struct {
	SchemaVersion     string       `json:"schema_version"`
	Bar               int          `json:"bar"`
	DecisionMS        int64        `json:"decision_ms"`
	Intent            TargetIntent `json:"intent"`
	DerivedVerb       DerivedVerb  `json:"derived_verb"`
	BeforeContextHash string       `json:"before_context_hash"`
	AfterContextHash  string       `json:"after_context_hash"`
	Events            []string     `json:"events"`
	TransitionHash    string       `json:"transition_hash"`
}

type Config struct {
	InitialCashUSDT         float64
	RetainedCapitalBaseUSDT float64
	NotionalPerFractionUSDT float64
	EntryCostBPS            float64
	ExitCostBPS             float64
	MaintenanceMarginKnown  bool
	MaintenanceMarginRate   float64
	FrozenCloseCostBPS      float64
}

type Tick struct {
	DecisionMS  int64
	MarkPrice   float64
	FundingRate float64 // Positive funding is paid by longs and received by shorts.
	Intent      *TargetIntent
}

type State struct {
	config      Config
	position    Position
	pending     []Pending
	cash        float64
	withdrawals float64
	lastMark    float64
	bar         int
	ruined      bool
}

func New(config Config) (*State, error) {
	if config.InitialCashUSDT < 0 || config.RetainedCapitalBaseUSDT <= 0 || config.NotionalPerFractionUSDT <= 0 ||
		config.EntryCostBPS < 0 || config.ExitCostBPS < 0 || config.MaintenanceMarginRate < 0 || config.FrozenCloseCostBPS < 0 {
		return nil, errors.New("invalid account configuration")
	}
	return &State{config: config, cash: config.InitialCashUSDT, position: Position{Direction: Flat}}, nil
}

func (i TargetIntent) Validate() error {
	if i.TargetHorizonHours != 1 && i.TargetHorizonHours != 4 && i.TargetHorizonHours != 24 {
		return errors.New("target horizon must be 1, 4, or 24 hours")
	}
	if i.DelayBars < 0 || i.DelayBars > 3 {
		return errors.New("delay bars must be in [0,3]")
	}
	for _, allowed := range []float64{-1, -.5, -.25, -.1, 0, .1, .25, .5, 1} {
		if math.Abs(i.TargetSignedPositionFraction-allowed) <= floatComparison {
			return nil
		}
	}
	return errors.New("target fraction is not on the frozen grid")
}

func DeriveVerb(current, target float64) (DerivedVerb, error) {
	if !onGrid(current) || !onGrid(target) {
		return "", errors.New("current and target fractions must be on the frozen grid")
	}
	if current == 0 {
		if target == 0 {
			return NoTrade, nil
		}
		if target > 0 {
			return OpenLong, nil
		}
		return OpenShort, nil
	}
	if target == 0 {
		return Close, nil
	}
	if math.Signbit(current) != math.Signbit(target) {
		return Reverse, nil
	}
	a, b := math.Abs(current), math.Abs(target)
	if math.Abs(a-b) <= floatComparison {
		return Hold, nil
	}
	if b > a {
		return Add, nil
	}
	return Trim, nil
}

func (s *State) Context(mark float64) (AccountContext, error) {
	if mark <= 0 || !finite(mark) {
		return AccountContext{}, errors.New("positive finite mark price required")
	}
	equity := s.equity(mark)
	free := math.Max(0, equity-s.requiredReserve())
	p := s.position
	unrealized := 0.0
	if p.Direction != Flat {
		unrealized = float64(directionSign(p.Direction)) * (mark/p.WeightedAverageEntryPrice - 1)
	}
	leverage := 0.0
	if equity > 0 {
		leverage = p.FixedNotionalUSDT / equity
	}
	return AccountContext{
		SchemaVersion: SchemaVersion, PositionDirection: p.Direction,
		SignedPositionFraction: p.SignedPositionFraction, FixedNotionalUSDT: p.FixedNotionalUSDT,
		WeightedAverageEntryPrice: p.WeightedAverageEntryPrice, OpenedAtMS: p.OpenedAtMS,
		PositionAgeBars: p.PositionAgeBars, DecisionTimeUnrealizedReturn: unrealized,
		CumulativeSignedFundingUSDT: p.CumulativeSignedFundingUSDT, CashUSDT: s.cash,
		MarkedEquityUSDT: equity, RetainedCapitalBaseUSDT: s.config.RetainedCapitalBaseUSDT,
		CumulativeWithdrawalsUSDT: s.withdrawals, GrossLeverage: leverage, FreeEquityUSDT: free,
		OpenPositionCount: countPosition(p), PendingActionCount: len(s.pending),
	}, nil
}

func HashAccountContext(context AccountContext) (string, error) {
	if context.SchemaVersion != SchemaVersion {
		return "", errors.New("unexpected account context schema")
	}
	hash, _, err := datatruth.HashJSON(context)
	return hash, err
}

func HashPosition(position Position) (string, error) {
	hash, _, err := datatruth.HashJSON(struct {
		SchemaVersion string   `json:"schema_version"`
		Position      Position `json:"position"`
	}{SchemaVersion, position})
	return hash, err
}

func HashPending(pending Pending) (string, error) {
	hash, _, err := datatruth.HashJSON(struct {
		SchemaVersion string  `json:"schema_version"`
		Pending       Pending `json:"pending"`
	}{SchemaVersion, pending})
	return hash, err
}

func HashTargetIntent(intent TargetIntent) (string, error) {
	if err := intent.Validate(); err != nil {
		return "", err
	}
	hash, _, err := datatruth.HashJSON(struct {
		SchemaVersion string       `json:"schema_version"`
		Intent        TargetIntent `json:"intent"`
	}{SchemaVersion, intent})
	return hash, err
}

func HashDerivedVerb(verb DerivedVerb) (string, error) {
	switch verb {
	case NoTrade, OpenLong, OpenShort, Hold, Add, Trim, Close, Reverse:
	default:
		return "", errors.New("unknown derived verb")
	}
	hash, _, err := datatruth.HashJSON(struct {
		SchemaVersion string      `json:"schema_version"`
		Verb          DerivedVerb `json:"derived_verb"`
	}{SchemaVersion, verb})
	return hash, err
}

// Step marks the existing position, settles current funding, matures earlier
// intents, then performs any eligible withdrawal before opening new risk.
func (s *State) Step(tick Tick) (TransitionTrace, error) {
	if tick.MarkPrice <= 0 || !finite(tick.MarkPrice) || !finite(tick.FundingRate) {
		return TransitionTrace{}, errors.New("positive finite mark and finite funding required")
	}
	before, err := s.Context(tick.MarkPrice)
	if err != nil {
		return TransitionTrace{}, err
	}
	beforeHash, err := HashAccountContext(before)
	if err != nil {
		return TransitionTrace{}, err
	}
	trace := TransitionTrace{SchemaVersion: SchemaVersion, Bar: s.bar, DecisionMS: tick.DecisionMS, BeforeContextHash: beforeHash}
	if s.ruined {
		trace.Events = []string{"RUIN_STOP"}
		return s.finishTrace(trace, tick.MarkPrice)
	}
	var events []string
	s.settleFunding(tick.FundingRate, &events)
	matured := s.takeMatured()
	for _, p := range matured {
		verb, err := DeriveVerb(s.position.SignedPositionFraction, p.Intent.TargetSignedPositionFraction)
		if err != nil {
			return TransitionTrace{}, err
		}
		trace.Intent, trace.DerivedVerb = p.Intent, verb
		s.executeReductionOrClose(p.Intent, tick.MarkPrice, &events)
	}
	if tick.Intent != nil {
		if err := tick.Intent.Validate(); err != nil {
			return TransitionTrace{}, err
		}
		if tick.Intent.DelayBars > 0 {
			s.pending = append(s.pending, Pending{Intent: *tick.Intent, SubmittedAtMS: tick.DecisionMS, MaturesAtBar: s.bar + tick.Intent.DelayBars})
			trace.Intent, trace.DerivedVerb = *tick.Intent, NoTrade
			events = append(events, "PENDING_SUBMITTED")
		} else {
			trace.Intent = *tick.Intent
			verb, err := DeriveVerb(s.position.SignedPositionFraction, tick.Intent.TargetSignedPositionFraction)
			if err != nil {
				return TransitionTrace{}, err
			}
			trace.DerivedVerb = verb
			s.executeReductionOrClose(*tick.Intent, tick.MarkPrice, &events)
			matured = append(matured, Pending{Intent: *tick.Intent})
		}
	}
	s.withdraw(tick.MarkPrice, &events)
	for _, p := range matured {
		s.openOrAdd(p.Intent, tick.DecisionMS, tick.MarkPrice, &events)
	}
	if s.position.Direction != Flat {
		s.position.PositionAgeBars++
	}
	s.lastMark, s.bar = tick.MarkPrice, s.bar+1
	trace.Events = events
	return s.finishTrace(trace, tick.MarkPrice)
}

func (s *State) FinishEOF(mark float64) ([]Pending, error) {
	if mark <= 0 || !finite(mark) {
		return nil, errors.New("positive finite mark price required")
	}
	discarded := append([]Pending(nil), s.pending...)
	s.pending = nil // Protocol g6: unmatured actions are discarded at EOF.
	return discarded, nil
}

func (s *State) finishTrace(trace TransitionTrace, mark float64) (TransitionTrace, error) {
	after, err := s.Context(mark)
	if err != nil {
		return TransitionTrace{}, err
	}
	trace.AfterContextHash, err = HashAccountContext(after)
	if err != nil {
		return TransitionTrace{}, err
	}
	clone := trace
	clone.TransitionHash = ""
	trace.TransitionHash, _, err = datatruth.HashJSON(clone)
	return trace, err
}

func (s *State) settleFunding(rate float64, events *[]string) {
	if s.position.Direction == Flat || rate == 0 {
		return
	}
	cashflow := -float64(directionSign(s.position.Direction)) * s.position.FixedNotionalUSDT * rate
	s.cash += cashflow
	s.position.CumulativeSignedFundingUSDT += cashflow
	*events = append(*events, "FUNDING_SETTLED")
	s.checkRuin(events)
}

func (s *State) takeMatured() []Pending {
	var ready, left []Pending
	for _, pending := range s.pending {
		if pending.MaturesAtBar <= s.bar {
			ready = append(ready, pending)
		} else {
			left = append(left, pending)
		}
	}
	s.pending = left
	return ready
}

func (s *State) executeReductionOrClose(intent TargetIntent, mark float64, events *[]string) {
	verb, _ := DeriveVerb(s.position.SignedPositionFraction, intent.TargetSignedPositionFraction)
	switch verb {
	case Close, Reverse:
		s.closeFraction(1, mark, events)
	case Trim:
		current := math.Abs(s.position.SignedPositionFraction)
		reduce := (current - math.Abs(intent.TargetSignedPositionFraction)) / current
		s.closeFraction(reduce, mark, events)
	}
}

func (s *State) openOrAdd(intent TargetIntent, decisionMS int64, mark float64, events *[]string) {
	if s.ruined || intent.TargetSignedPositionFraction == 0 {
		return
	}
	verb, _ := DeriveVerb(s.position.SignedPositionFraction, intent.TargetSignedPositionFraction)
	if verb != OpenLong && verb != OpenShort && verb != Add && verb != Reverse {
		return
	}
	targetNotional := math.Abs(intent.TargetSignedPositionFraction) * s.config.NotionalPerFractionUSDT
	addNotional := targetNotional - s.position.FixedNotionalUSDT
	if addNotional <= floatComparison {
		return
	}
	cost := addNotional * s.config.EntryCostBPS / 10000
	equityAfterCost := s.equity(mark) - cost
	if s.cash-cost < -floatComparison || equityAfterCost <= 0 || targetNotional > maxLeverage*equityAfterCost+floatComparison {
		*events = append(*events, "OPEN_REJECTED_HARD_GATE")
		return
	}
	newDirection := directionFor(intent.TargetSignedPositionFraction)
	if s.position.Direction == Flat {
		s.position = Position{Direction: newDirection, SignedPositionFraction: intent.TargetSignedPositionFraction, FixedNotionalUSDT: addNotional, WeightedAverageEntryPrice: mark, OpenedAtMS: decisionMS}
		*events = append(*events, "OPEN")
	} else {
		s.position.WeightedAverageEntryPrice = (s.position.WeightedAverageEntryPrice*s.position.FixedNotionalUSDT + mark*addNotional) / targetNotional
		s.position.FixedNotionalUSDT = targetNotional
		s.position.SignedPositionFraction = intent.TargetSignedPositionFraction
		*events = append(*events, "ADD")
	}
	s.cash -= cost
	s.checkRuin(events)
}

func (s *State) closeFraction(fraction, mark float64, events *[]string) {
	if s.position.Direction == Flat || fraction <= 0 {
		return
	}
	fraction = math.Min(1, fraction)
	notional := s.position.FixedNotionalUSDT * fraction
	pnl := notional * float64(directionSign(s.position.Direction)) * (mark/s.position.WeightedAverageEntryPrice - 1)
	funding := s.position.CumulativeSignedFundingUSDT * fraction
	cost := notional * s.config.ExitCostBPS / 10000
	s.cash += pnl - cost // funding was settled to cash when it accrued.
	s.position.CumulativeSignedFundingUSDT -= funding
	if fraction >= 1-floatComparison {
		s.position = Position{Direction: Flat}
		*events = append(*events, "CLOSE")
	} else {
		s.position.FixedNotionalUSDT -= notional
		s.position.SignedPositionFraction *= 1 - fraction
		*events = append(*events, "TRIM")
	}
	s.checkRuin(events)
}

func (s *State) withdraw(mark float64, events *[]string) {
	if s.position.Direction != Flat && !s.config.MaintenanceMarginKnown {
		*events = append(*events, "WITHDRAWAL_DEFERRED_MMR_UNKNOWN")
		return
	}
	for s.equity(mark) >= 1.5*s.config.RetainedCapitalBaseUSDT {
		amount := floorMicro(math.Min(.5*s.config.RetainedCapitalBaseUSDT, math.Max(0, s.equity(mark)-s.requiredReserve())))
		if amount <= 0 || amount > s.cash+floatComparison {
			return
		}
		s.cash -= amount
		if s.cash < -floatComparison || s.equity(mark) < 0 || s.position.FixedNotionalUSDT > maxLeverage*s.equity(mark)+floatComparison {
			s.cash += amount
			*events = append(*events, "WITHDRAWAL_BLOCKED_SOLVENCY")
			return
		}
		s.withdrawals += amount
		*events = append(*events, "WITHDRAWAL")
	}
}

func (s *State) equity(mark float64) float64 {
	if s.position.Direction == Flat {
		return s.cash
	}
	return s.cash + s.position.FixedNotionalUSDT*float64(directionSign(s.position.Direction))*(mark/s.position.WeightedAverageEntryPrice-1)
}
func (s *State) requiredReserve() float64 {
	leverage := s.position.FixedNotionalUSDT / maxLeverage
	maintenance := s.position.FixedNotionalUSDT*s.config.MaintenanceMarginRate + s.position.FixedNotionalUSDT*s.config.FrozenCloseCostBPS/10000
	return math.Max(leverage, maintenance)
}
func (s *State) checkRuin(events *[]string) {
	if s.cash < -floatComparison {
		s.cash, s.ruined = 0, true
		*events = append(*events, "RUIN")
	}
}
func directionFor(fraction float64) Direction {
	if fraction > 0 {
		return Long
	}
	return Short
}
func directionSign(d Direction) int {
	if d == Long {
		return 1
	}
	if d == Short {
		return -1
	}
	return 0
}
func countPosition(p Position) int {
	if p.Direction == Flat {
		return 0
	}
	return 1
}
func onGrid(v float64) bool {
	err := (TargetIntent{TargetSignedPositionFraction: v, TargetHorizonHours: 1}).Validate()
	return err == nil
}
func finite(v float64) bool        { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func floorMicro(v float64) float64 { return math.Floor(v*microUSDT) / microUSDT }

// CanonicalContext is a hypothetical discovery-only account context. It
// contains no reward fields and cannot read real labels.
type CanonicalContext struct {
	EventID  string         `json:"event_id"`
	Context  AccountContext `json:"account_context"`
	Priority string         `json:"priority"`
}

func CanonicalDiscoveryContexts(eventID string, budget int) ([]CanonicalContext, error) {
	if eventID == "" || budget < 0 {
		return nil, errors.New("event ID and non-negative budget required")
	}
	var out []CanonicalContext
	for _, dir := range []Direction{Flat, Long, Short} {
		for _, age := range []int{1, 4, 24} {
			for _, size := range []float64{.1, .25, .5, 1} {
				signed := size * float64(directionSign(dir))
				if dir == Flat {
					signed = 0
				}
				ctx := AccountContext{SchemaVersion: SchemaVersion, PositionDirection: dir, SignedPositionFraction: signed,
					FixedNotionalUSDT: math.Abs(signed) * 100, WeightedAverageEntryPrice: 100, PositionAgeBars: age,
					CashUSDT: 100, MarkedEquityUSDT: 100, RetainedCapitalBaseUSDT: 100,
					OpenPositionCount: map[bool]int{true: 0, false: 1}[dir == Flat]}
				h, _, err := datatruth.HashJSON(struct {
					EventID string         `json:"event_id"`
					Context AccountContext `json:"context"`
				}{eventID, ctx})
				if err != nil {
					return nil, err
				}
				out = append(out, CanonicalContext{EventID: eventID, Context: ctx, Priority: h})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	if budget < len(out) {
		out = out[:budget]
	}
	return out, nil
}

type Policy func(AccountContext) (TargetIntent, error)

// RunBaseline is the single runner entry point for policies and baselines.
// Its policy input is limited to DecisionTime AccountContext.
func RunBaseline(config Config, ticks []Tick, policy Policy) ([]TransitionTrace, error) {
	state, err := New(config)
	if err != nil {
		return nil, err
	}
	traces := make([]TransitionTrace, 0, len(ticks))
	for _, tick := range ticks {
		if policy != nil {
			ctx, err := state.Context(tick.MarkPrice)
			if err != nil {
				return nil, err
			}
			intent, err := policy(ctx)
			if err != nil {
				return nil, err
			}
			tick.Intent = &intent
		}
		trace, err := state.Step(tick)
		if err != nil {
			return nil, err
		}
		traces = append(traces, trace)
	}
	return traces, nil
}

func CodeHash() string {
	sum := sha256.Sum256([]byte(SchemaVersion + "\x00" + ProtocolG6Hash))
	return hex.EncodeToString(sum[:])
}

func (s *State) String() string {
	return fmt.Sprintf("cash=%.6f position=%s", s.cash, s.position.Direction)
}
