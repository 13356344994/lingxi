package pathvalidation

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
)

// This file reimplements the g10 counterfactual/account/cost primitive
// (counterfactual.Prepare / runTarget / transition / OneWayCostBPS /
// fundingCashflow / ruin / withdraw) for the validation and leaf_wf
// partitions. counterfactual.Prepare hard-refuses any row whose Partition is
// not "discovery" by design (it is the sealed discovery-label primitive), so
// this validation owner reimplements the identical account/cost mechanics
// here rather than editing that sealed package. Every low-level cost/account
// building block (datav3.DynamicExecutionCostBPS, the fee/spread/impact/
// toxicity formula, the 5x leverage cap, the funding-cashflow window, the
// withdrawal/solvency rule) is unchanged from g10.
//
// Unlike counterfactual.Evaluate (which searches all 108 targets to build a
// sealed discovery label), EvaluateIntent below scores exactly one supplied
// target intent — the intent a sealed owner policy actually predicted (or,
// for delay/cost-stress robustness checks, the same fraction/horizon under a
// different delay or a stressed cost multiplier). No label search, no best-
// of-108 selection, ever happens here.

const (
	hourMS       int64 = 60 * 60 * 1000
	floatEpsilon       = 1e-12
)

type position struct {
	Fraction float64
	Notional float64
	Entry    float64
}

type account struct {
	Cash        float64
	Withdrawals float64
	Position    position
	Ruined      bool
}

// PreparedRow is a validation/leaf_wf row bound to one synthetic
// account/context/capital-tier combination, exactly as g10 builds it for
// discovery, plus the price/funding series needed to chronologically walk
// forward any candidate target intent.
type PreparedRow struct {
	Candidate counterfactual.Candidate
	Bars      map[int64]counterfactual.Bar
	Funding   counterfactual.FundingSource
	Account   modelapi.AccountContext
	position  position
}

// Outcome is the result of evaluating exactly one target intent against one
// prepared row.
type Outcome struct {
	Ruin        bool
	Matured     bool
	NetReward   float64
	UnknownCost bool
}

// PrepareRow mirrors counterfactual.Prepare exactly, except it accepts rows
// from the validation or leaf_wf partition instead of discovery. It returns
// ok=false (not an error) whenever the row is not eligible for a full,
// contiguous, funding-complete 27-hour evaluation window, matching g10's own
// "honest non-selection" behavior rather than approximating with partial data.
func PrepareRow(candidate counterfactual.Candidate, bars map[int64]counterfactual.Bar, funding counterfactual.FundingSource) (PreparedRow, bool, error) {
	row := candidate.Row
	if !AllowedPartitions[row.Partition] {
		return PreparedRow{}, false, fmt.Errorf("pathvalidation refuses partition %q", row.Partition)
	}
	if candidate.TierUSDT != 30 && candidate.TierUSDT != 100 && candidate.TierUSDT != 1000 {
		return PreparedRow{}, false, errors.New("invalid capital tier")
	}
	direction, age, fraction, err := parseContext(candidate.ContextID)
	if err != nil {
		return PreparedRow{}, false, err
	}
	decisionBar, ok := bars[row.DecisionMS]
	if !ok {
		return PreparedRow{}, false, nil
	}
	startMS := row.DecisionMS
	p := position{}
	cash := candidate.TierUSDT
	cumulativeFunding := 0.0
	entryPrice := 0.0
	if fraction != 0 {
		entryMS := row.DecisionMS - int64(age)*hourMS
		entryBar, exists := bars[entryMS]
		if !exists || !funding.Covers(entryMS, row.DecisionMS) {
			return PreparedRow{}, false, nil
		}
		startMS = entryMS
		notional := fraction * candidate.TierUSDT
		entryCostBPS, err := oneWayCostBPS(entryBar, candidate.TierUSDT, 1)
		if err != nil {
			return PreparedRow{}, false, err
		}
		cumulativeFunding = fundingCashflow(funding.Events[row.Instrument], entryMS, row.DecisionMS, direction*notional)
		cash = candidate.TierUSDT - notional*entryCostBPS/10000 + cumulativeFunding
		entryPrice = entryBar.Open
		p = position{Fraction: direction * fraction, Notional: notional, Entry: entryPrice}
	}
	if !barsContiguous(bars, startMS, row.DecisionMS+27*hourMS) ||
		!funding.Covers(startMS, row.DecisionMS+27*hourMS) {
		return PreparedRow{}, false, nil
	}
	equity := cash + unrealized(p, decisionBar.Close)
	if cash < 0 || equity <= 0 || p.Notional > 5*equity+floatEpsilon {
		return PreparedRow{}, false, nil
	}
	accountContext := modelapi.AccountContext{
		SignedPositionFraction:       p.Fraction,
		FixedNotionalUSDT:            p.Notional,
		WeightedAverageEntryPrice:    entryPrice,
		PositionAgeBars:              age,
		DecisionTimeUnrealizedReturn: signedReturn(p, decisionBar.Close),
		CumulativeSignedFundingUSDT:  cumulativeFunding,
		CashUSDT:                     cash,
		MarkedEquityUSDT:             equity,
		RetainedCapitalBaseUSDT:      candidate.TierUSDT,
		GrossLeverage:                leverage(p, equity),
		FreeEquityUSDT:               math.Max(0, equity-p.Notional/5),
		OpenPositionCount:            boolInt(p.Notional > 0),
	}
	return PreparedRow{Candidate: candidate, Bars: bars, Funding: funding, Account: accountContext, position: p}, true, nil
}

// EvaluateIntent walks the prepared account forward through exactly one
// candidate target intent, applying the g10 delay/horizon/funding/cost
// mechanics unchanged. stressMultiplier is 1 for base cost and 1.5 for the
// frozen g1 cost-stress scenario; both share this single code path so
// entry, exit, and stress legs cannot drift from each other.
func EvaluateIntent(p PreparedRow, target modelapi.TargetIntent, stressMultiplier float64) (Outcome, error) {
	if err := target.Validate(); err != nil {
		return Outcome{}, err
	}
	startWealth := p.Account.CashUSDT + unrealized(p.position, p.Bars[p.Candidate.Row.DecisionMS].Close)
	a := account{Cash: p.Account.CashUSDT, Position: p.position}
	decisionMS := p.Candidate.Row.DecisionMS
	executionMS := decisionMS + int64(target.DelayBars)*hourMS
	expiryMS := executionMS + int64(target.TargetHorizonHours)*hourMS
	if target.DelayBars == 0 {
		if err := transition(&a, target.TargetSignedPositionFraction, p.Bars[decisionMS], p.Candidate.TierUSDT, stressMultiplier); err != nil {
			return unknownCostOutcome(err)
		}
	}
	previous := decisionMS
	for current := decisionMS + hourMS; current <= expiryMS; current += hourMS {
		bar, ok := p.Bars[current]
		if !ok {
			return Outcome{}, fmt.Errorf("missing bar at %d within a row PrepareRow already certified contiguous", current)
		}
		denom := math.Max(math.Abs(a.Position.Fraction), floatEpsilon)
		a.Cash += fundingCashflow(p.Funding.Events[p.Candidate.Row.Instrument], previous, current, a.Position.Fraction*a.Position.Notional/denom)
		if ruined(a, bar.Close) {
			return Outcome{Ruin: true}, nil
		}
		if current == executionMS {
			if err := transition(&a, target.TargetSignedPositionFraction, bar, p.Candidate.TierUSDT, stressMultiplier); err != nil {
				return unknownCostOutcome(err)
			}
		}
		if current == expiryMS {
			if err := transition(&a, 0, bar, p.Candidate.TierUSDT, stressMultiplier); err != nil {
				return unknownCostOutcome(err)
			}
		}
		if a.Position.Notional == 0 {
			withdraw(&a, bar.Close, p.Candidate.TierUSDT)
		}
		if ruined(a, bar.Close) {
			return Outcome{Ruin: true}, nil
		}
		previous = current
	}
	return Outcome{Matured: true, NetReward: a.Cash + a.Withdrawals - startWealth}, nil
}

func unknownCostOutcome(err error) (Outcome, error) {
	if errors.Is(err, errInvalidCost) {
		return Outcome{UnknownCost: true}, nil
	}
	return Outcome{}, err
}

var errInvalidCost = errors.New("invalid execution cost input")

func transition(a *account, targetFraction float64, bar counterfactual.Bar, tier, stress float64) error {
	current := a.Position.Fraction
	if math.Abs(current-targetFraction) <= floatEpsilon {
		return nil
	}
	exitCost, err := oneWayCostBPS(bar, tier, stress)
	if err != nil {
		return errInvalidCost
	}
	opposite := current != 0 && targetFraction != 0 && math.Signbit(current) != math.Signbit(targetFraction)
	targetNotional := math.Abs(targetFraction) * tier
	if opposite || targetNotional < a.Position.Notional-floatEpsilon || targetFraction == 0 {
		closeNotional := a.Position.Notional
		if !opposite && targetFraction != 0 {
			closeNotional -= targetNotional
		}
		a.Cash += realized(a.Position, closeNotional, bar.Close) - closeNotional*exitCost/10000
		a.Position.Notional -= closeNotional
		if a.Position.Notional <= floatEpsilon {
			a.Position = position{}
		} else {
			a.Position.Fraction = math.Copysign(a.Position.Notional/tier, current)
		}
	}
	if a.Position.Notional == 0 {
		withdraw(a, bar.Close, tier)
	}
	if targetNotional > a.Position.Notional+floatEpsilon {
		add := targetNotional - a.Position.Notional
		entryCost, err := oneWayCostBPS(bar, tier, stress)
		if err != nil {
			return errInvalidCost
		}
		a.Cash -= add * entryCost / 10000
		if a.Position.Notional == 0 {
			a.Position = position{Fraction: targetFraction, Notional: add, Entry: bar.Close}
		} else {
			a.Position.Entry = (a.Position.Entry*a.Position.Notional + bar.Close*add) / targetNotional
			a.Position.Notional = targetNotional
			a.Position.Fraction = targetFraction
		}
	}
	return nil
}

func oneWayCostBPS(bar counterfactual.Bar, tier, stress float64) (float64, error) {
	roundTrip, err := datav3.DynamicExecutionCostBPS(datav3.RewardCostInput{
		Known:                  true,
		RoundTripFeeBPS:        10,
		SpreadBPS:              0,
		CapitalUSDT:            tier,
		RollingQuoteVolumeUSDT: bar.RollingQuoteVolumeUSDT,
		BarHigh:                bar.High,
		BarLow:                 bar.Low,
		BarMid:                 (bar.High + bar.Low) / 2,
		StressMultiplier:       stress,
	})
	return .5 * roundTrip, err
}

func fundingCashflow(events []counterfactual.FundingEvent, afterMS, throughMS int64, signedNotional float64) float64 {
	if throughMS <= afterMS || len(events) == 0 {
		return 0
	}
	start := sort.Search(len(events), func(index int) bool {
		return events[index].TimeMS > afterMS
	})
	end := sort.Search(len(events), func(index int) bool {
		return events[index].TimeMS > throughMS
	})
	var cashflow float64
	for _, event := range events[start:end] {
		cashflow -= signedNotional * event.Rate
	}
	return cashflow
}

func parseContext(id string) (direction float64, age int, fraction float64, err error) {
	if id == "FLAT|age=0|fraction=0" {
		return 0, 0, 0, nil
	}
	parts := strings.Split(id, "|")
	if len(parts) != 3 || (parts[0] != "LONG" && parts[0] != "SHORT") {
		return 0, 0, 0, errors.New("invalid canonical context")
	}
	age, err = strconv.Atoi(strings.TrimPrefix(parts[1], "age="))
	if err != nil {
		return 0, 0, 0, err
	}
	fraction, err = strconv.ParseFloat(strings.TrimPrefix(parts[2], "fraction="), 64)
	if err != nil {
		return 0, 0, 0, err
	}
	direction = 1
	if parts[0] == "SHORT" {
		direction = -1
	}
	return direction, age, fraction, nil
}

func barsContiguous(bars map[int64]counterfactual.Bar, startMS, endMS int64) bool {
	for timestamp := startMS; timestamp <= endMS; timestamp += hourMS {
		if _, ok := bars[timestamp]; !ok {
			return false
		}
	}
	return true
}

func unrealized(p position, mark float64) float64 {
	if p.Notional == 0 {
		return 0
	}
	return math.Copysign(1, p.Fraction) * p.Notional * (mark/p.Entry - 1)
}

func realized(p position, notional, mark float64) float64 {
	if p.Notional == 0 {
		return 0
	}
	return math.Copysign(1, p.Fraction) * notional * (mark/p.Entry - 1)
}

func signedReturn(p position, mark float64) float64 {
	if p.Notional == 0 {
		return 0
	}
	return math.Copysign(1, p.Fraction) * (mark/p.Entry - 1)
}

func leverage(p position, equity float64) float64 {
	if equity <= 0 {
		return math.Inf(1)
	}
	return p.Notional / equity
}

func ruined(a account, mark float64) bool {
	equity := a.Cash + unrealized(a.Position, mark)
	return a.Cash < -floatEpsilon || equity <= 0 || a.Position.Notional > 5*equity+floatEpsilon
}

func withdraw(a *account, mark, retainedBase float64) {
	for a.Cash+unrealized(a.Position, mark) >= 1.5*retainedBase {
		equity := a.Cash + unrealized(a.Position, mark)
		reserve := a.Position.Notional / 5
		amount := math.Floor(math.Min(.5*retainedBase, math.Max(0, equity-reserve))*1e6) / 1e6
		if amount <= 0 || amount > a.Cash {
			return
		}
		a.Cash -= amount
		a.Withdrawals += amount
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
