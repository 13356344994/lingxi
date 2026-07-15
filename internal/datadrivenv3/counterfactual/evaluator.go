// Package counterfactual implements the single g8/g10 discovery target
// evaluator. It contains no model implementation and exposes no reward vector.
package counterfactual

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/accountstate"
	"lingxi/internal/datadrivenv3/modelapi"
	"lingxi/internal/datatruth"
)

const (
	Seed               = "seed42"
	hourMS       int64 = 60 * 60 * 1000
	floatEpsilon       = 1e-12
)

var (
	fractions = []float64{-1, -.5, -.25, -.1, 0, .1, .25, .5, 1}
	horizons  = []int{1, 4, 24}
)

type Bar struct {
	TimeMS                 int64
	Open                   float64
	High                   float64
	Low                    float64
	Close                  float64
	RollingQuoteVolumeUSDT float64
}

type FundingEvent struct {
	TimeMS int64
	Rate   float64
}

type FundingSource struct {
	Events       map[string][]FundingEvent
	CompleteDays map[string]bool
}

type Candidate struct {
	Row       datav3.StateRowV2
	ContextID string
	TierUSDT  float64
	Priority  string
}

type Prepared struct {
	Candidate Candidate
	Bars      map[int64]Bar
	Funding   FundingSource
	Account   modelapi.AccountContext
	position  position
}

type Label struct {
	Example             modelapi.TrainingExample
	MarketStateHash     string
	AccountContextHash  string
	TargetSelectionHash string
}

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

func CandidateFor(row datav3.StateRowV2) Candidate {
	contextID := ContextID(row.EventID)
	tier := CapitalTier(row.EventID)
	return Candidate{
		Row:       row,
		ContextID: contextID,
		TierUSDT:  tier,
		Priority:  SHA256Hex(row.EventID + contextID + Seed),
	}
}

func ContextID(eventID string) string {
	contexts := []string{"FLAT|age=0|fraction=0"}
	for _, direction := range []string{"LONG", "SHORT"} {
		for _, age := range []int{1, 4, 24} {
			for _, fraction := range []string{"0.1", "0.25", "0.5", "1"} {
				contexts = append(contexts, fmt.Sprintf("%s|age=%d|fraction=%s", direction, age, fraction))
			}
		}
	}
	sum := sha256.Sum256([]byte(eventID + Seed))
	return contexts[binary.BigEndian.Uint64(sum[:8])%uint64(len(contexts))]
}

func CapitalTier(eventID string) float64 {
	tiers := [...]float64{30, 100, 1000}
	sum := sha256.Sum256([]byte(Seed + eventID + "capital_tier"))
	return tiers[binary.BigEndian.Uint64(sum[:8])%uint64(len(tiers))]
}

func Prepare(candidate Candidate, bars map[int64]Bar, funding FundingSource) (Prepared, bool, error) {
	row := candidate.Row
	if row.Partition != "discovery" {
		return Prepared{}, false, errors.New("counterfactual refuses non-discovery row")
	}
	if candidate.TierUSDT != 30 && candidate.TierUSDT != 100 && candidate.TierUSDT != 1000 {
		return Prepared{}, false, errors.New("invalid capital tier")
	}
	direction, age, fraction, err := parseContext(candidate.ContextID)
	if err != nil {
		return Prepared{}, false, err
	}
	decisionBar, ok := bars[row.DecisionMS]
	if !ok {
		return Prepared{}, false, nil
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
			return Prepared{}, false, nil
		}
		startMS = entryMS
		notional := fraction * candidate.TierUSDT
		entryCostBPS, err := OneWayCostBPS(entryBar, candidate.TierUSDT)
		if err != nil {
			return Prepared{}, false, err
		}
		cumulativeFunding = fundingCashflow(funding.Events[row.Instrument], entryMS, row.DecisionMS, direction*notional)
		cash = candidate.TierUSDT - notional*entryCostBPS/10000 + cumulativeFunding
		entryPrice = entryBar.Open
		p = position{Fraction: direction * fraction, Notional: notional, Entry: entryPrice}
	}
	if !barsContiguous(bars, startMS, row.DecisionMS+27*hourMS) ||
		!funding.Covers(startMS, row.DecisionMS+27*hourMS) {
		return Prepared{}, false, nil
	}
	equity := cash + unrealized(p, decisionBar.Close)
	if cash < 0 || equity <= 0 || p.Notional > 5*equity+floatEpsilon {
		return Prepared{}, false, nil
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
	return Prepared{Candidate: candidate, Bars: bars, Funding: funding, Account: accountContext, position: p}, true, nil
}

func Evaluate(prepared Prepared) (Label, error) {
	startWealth := prepared.Account.CashUSDT +
		unrealized(prepared.position, prepared.Bars[prepared.Candidate.Row.DecisionMS].Close)
	bestReward := math.Inf(-1)
	var best accountstate.TargetIntent
	first := true
	for _, target := range Targets() {
		ending, err := runTarget(prepared, target)
		if err != nil {
			return Label{}, err
		}
		reward := math.Inf(-1)
		if !ending.Ruined {
			reward = ending.Cash + ending.Withdrawals - startWealth
		}
		if first || reward > bestReward || (reward == bestReward && targetBefore(target, best, prepared.position.Fraction)) {
			first, bestReward, best = false, reward, target
		}
	}
	market := modelapi.MarketState{
		SchemaVersion: modelapi.SchemaVersion,
		EventID:       prepared.Candidate.Row.EventID,
		DecisionMS:    prepared.Candidate.Row.DecisionMS,
		Features:      prepared.Candidate.Row.Values,
		MissingBits:   prepared.Candidate.Row.MissingBits,
	}
	intent := modelapi.TargetIntent{
		TargetSignedPositionFraction: best.TargetSignedPositionFraction,
		TargetHorizonHours:           best.TargetHorizonHours,
		DelayBars:                    best.DelayBars,
	}
	example := modelapi.TrainingExample{
		SchemaVersion:   modelapi.SchemaVersion,
		Kind:            modelapi.ExampleDiscovery,
		ExampleID:       SHA256Hex(prepared.Candidate.Row.EventID + prepared.Candidate.ContextID),
		Market:          market,
		Account:         prepared.Account,
		SyntheticTarget: intent,
	}
	marketHash, _, err := datatruth.HashJSON(market)
	if err != nil {
		return Label{}, err
	}
	accountHash, _, err := datatruth.HashJSON(prepared.Account)
	if err != nil {
		return Label{}, err
	}
	selectionHash, _, err := datatruth.HashJSON(struct {
		Market  string                `json:"market_state_hash"`
		Account string                `json:"account_context_hash"`
		Intent  modelapi.TargetIntent `json:"selected_target_intent"`
	}{marketHash, accountHash, intent})
	if err != nil {
		return Label{}, err
	}
	return Label{Example: example, MarketStateHash: marketHash, AccountContextHash: accountHash, TargetSelectionHash: selectionHash}, nil
}

func Targets() []accountstate.TargetIntent {
	out := make([]accountstate.TargetIntent, 0, 108)
	for _, fraction := range fractions {
		for _, horizon := range horizons {
			for delay := 0; delay <= 3; delay++ {
				out = append(out, accountstate.TargetIntent{TargetSignedPositionFraction: fraction, TargetHorizonHours: horizon, DelayBars: delay})
			}
		}
	}
	return out
}

func runTarget(p Prepared, target accountstate.TargetIntent) (account, error) {
	a := account{Cash: p.Account.CashUSDT, Position: p.position}
	decisionMS := p.Candidate.Row.DecisionMS
	executionMS := decisionMS + int64(target.DelayBars)*hourMS
	expiryMS := executionMS + int64(target.TargetHorizonHours)*hourMS
	if target.DelayBars == 0 {
		if err := transition(&a, target.TargetSignedPositionFraction, p.Bars[decisionMS], p.Candidate.TierUSDT); err != nil {
			return account{}, err
		}
	}
	previous := decisionMS
	for current := decisionMS + hourMS; current <= expiryMS; current += hourMS {
		bar := p.Bars[current]
		a.Cash += fundingCashflow(p.Funding.Events[p.Candidate.Row.Instrument], previous, current, a.Position.Fraction*a.Position.Notional/math.Max(math.Abs(a.Position.Fraction), floatEpsilon))
		if ruined(a, bar.Close) {
			a.Ruined = true
			return a, nil
		}
		if current == executionMS {
			if err := transition(&a, target.TargetSignedPositionFraction, bar, p.Candidate.TierUSDT); err != nil {
				return account{}, err
			}
		}
		if current == expiryMS {
			if err := transition(&a, 0, bar, p.Candidate.TierUSDT); err != nil {
				return account{}, err
			}
		}
		if a.Position.Notional == 0 {
			withdraw(&a, bar.Close, p.Candidate.TierUSDT)
		}
		if ruined(a, bar.Close) {
			a.Ruined = true
			return a, nil
		}
		previous = current
	}
	return a, nil
}

func transition(a *account, targetFraction float64, bar Bar, tier float64) error {
	current := a.Position.Fraction
	if math.Abs(current-targetFraction) <= floatEpsilon {
		return nil
	}
	exitCost, err := OneWayCostBPS(bar, tier)
	if err != nil {
		return err
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
		entryCost, err := OneWayCostBPS(bar, tier)
		if err != nil {
			return err
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

func OneWayCostBPS(bar Bar, tier float64) (float64, error) {
	roundTrip, err := datav3.DynamicExecutionCostBPS(datav3.RewardCostInput{
		Known:                  true,
		RoundTripFeeBPS:        10,
		SpreadBPS:              0,
		CapitalUSDT:            tier,
		RollingQuoteVolumeUSDT: bar.RollingQuoteVolumeUSDT,
		BarHigh:                bar.High,
		BarLow:                 bar.Low,
		BarMid:                 (bar.High + bar.Low) / 2,
		StressMultiplier:       1,
	})
	return .5 * roundTrip, err
}

func (f FundingSource) Covers(startMS, endMS int64) bool {
	if endMS < startMS {
		return false
	}
	start := time.UnixMilli(startMS).UTC()
	end := time.UnixMilli(endMS).UTC()
	for d := day(start); !d.After(day(end)); d = d.AddDate(0, 0, 1) {
		if !f.CompleteDays[d.Format("2006-01-02")] {
			return false
		}
	}
	return true
}

func targetBefore(a, b accountstate.TargetIntent, current float64) bool {
	turnA, turnB := math.Abs(a.TargetSignedPositionFraction-current), math.Abs(b.TargetSignedPositionFraction-current)
	if turnA != turnB {
		return turnA < turnB
	}
	absA, absB := math.Abs(a.TargetSignedPositionFraction), math.Abs(b.TargetSignedPositionFraction)
	if absA != absB {
		return absA < absB
	}
	if a.TargetHorizonHours != b.TargetHorizonHours {
		return a.TargetHorizonHours < b.TargetHorizonHours
	}
	if a.DelayBars != b.DelayBars {
		return a.DelayBars < b.DelayBars
	}
	if (a.TargetSignedPositionFraction == 0) != (b.TargetSignedPositionFraction == 0) {
		return a.TargetSignedPositionFraction == 0
	}
	return a.TargetSignedPositionFraction < b.TargetSignedPositionFraction
}

func fundingCashflow(events []FundingEvent, afterMS, throughMS int64, signedNotional float64) float64 {
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

func barsContiguous(bars map[int64]Bar, startMS, endMS int64) bool {
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

func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func SortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].Row.EventID < candidates[j].Row.EventID
	})
}
