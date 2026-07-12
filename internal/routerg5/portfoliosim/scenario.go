package portfoliosim

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// ScenarioEngine replays frozen policy over one partition slice.
type ScenarioEngine struct {
	Policy    reverselearn.FrozenPolicy
	Bars      BarsBundle
	Profile   features.Profile
	Horizons  []string
	Partition string
}

// RunScenarios executes the frozen equity x delay x stress grid over timestamps.
func (e ScenarioEngine) RunScenarios(
	featureRows map[string][]features.DecisionContext,
	timestamps []string,
) []ScenarioResult {
	if len(timestamps) == 0 {
		return nil
	}
	var scenarios []ScenarioResult
	for _, equity := range g5protocol.ValidationInitialEquities {
		for _, delay := range g5protocol.ValidationDelayBars {
			stressLevels := []float64{1.0}
			if delay == 0 {
				stressLevels = append(stressLevels, g5protocol.CostStressMultiplier)
			}
			for _, stress := range stressLevels {
				scenarios = append(scenarios, e.runOneScenario(featureRows, timestamps, equity, delay, stress))
			}
		}
	}
	return scenarios
}

func (e ScenarioEngine) runOneScenario(
	featureRows map[string][]features.DecisionContext,
	timestamps []string,
	equity float64,
	delay int,
	stress float64,
) ScenarioResult {
	account := NewAccount(equity)
	queue := &PendingQueue{}
	points := 0
	var prevDecision time.Time
	for _, ts := range timestamps {
		rows := featureRows[ts]
		decisionTime, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		if !prevDecision.IsZero() {
			symbols := positionSymbols(account)
			account.ApplyHoldFunding(e.Bars.Funding, symbols, prevDecision, decisionTime, stress)
			account.MarkToMarket(ClosePricesAtDecision(e.Bars, e.Profile, symbols, decisionTime))
		}
		inputs := BuildDecisionInputs(rows, account.Positions, account.EquityUSDT)
		ReplayBar(account, queue, e.Policy, inputs, e.Bars, e.Profile, decisionTime, e.Horizons, delay, stress, e.Partition)
		points++
		prevDecision = decisionTime
	}
	_ = queue.DiscardAll()
	if points > 0 && !prevDecision.IsZero() {
		lastTS := timestamps[len(timestamps)-1]
		lastTime, _ := time.Parse(time.RFC3339Nano, lastTS)
		symbols := positionSymbols(account)
		if len(symbols) > 0 {
			account.ApplyHoldFunding(e.Bars.Funding, symbols, prevDecision, lastTime, stress)
		}
		lastRows := featureRows[lastTS]
		priceSymbols := make([]string, 0, len(lastRows)+len(symbols))
		priceSymbols = append(priceSymbols, symbols...)
		for _, row := range lastRows {
			priceSymbols = append(priceSymbols, row.Symbol)
		}
		sort.Strings(priceSymbols)
		uniq := priceSymbols[:0]
		for i, sym := range priceSymbols {
			if i == 0 || sym != priceSymbols[i-1] {
				uniq = append(uniq, sym)
			}
		}
		account.MarkToMarket(ClosePricesAtDecision(e.Bars, e.Profile, uniq, lastTime))
	}
	activeDays, noTrade, maxSym := account.FinalizeStats()
	return ScenarioResult{
		InitialEquityUSDT:     equity,
		DelayBars:             delay,
		CostStressMultiplier:  stress,
		FinalEquityUSDT:       account.EquityUSDT,
		NetReturnFraction:     NetReturnFraction(equity, account.EquityUSDT),
		MaxDrawdownFraction:   account.MaxDrawdown,
		ActiveDays:            activeDays,
		TurnoverUSDT:          account.TurnoverUSDT,
		FundingPaidUSDT:       account.FundingPaidUSDT,
		FundingMissing:        account.FundingMissing,
		NoTradeFraction:       noTrade,
		ActionCoverage:        cloneIntMap(account.ActionCounts),
		CapacityRejectRate:    account.CapacityRejectRate(),
		LeverageBreach:        account.LeverageBreach,
		SolvencyBreach:        account.SolvencyBreach,
		MaxSymbolContribution: maxSym,
		EquityCurveSummary:    account.EquitySummary(points),
	}
}

// FilterTimestamps returns sorted keys present in featureRows.
func FilterTimestamps(featureRows map[string][]features.DecisionContext, timestamps []string) []string {
	if len(timestamps) == 0 {
		timestamps = sortedTimestamps(featureRows)
	}
	out := make([]string, 0, len(timestamps))
	for _, ts := range timestamps {
		if _, ok := featureRows[ts]; ok {
			out = append(out, ts)
		}
	}
	sort.Strings(out)
	return out
}

// BaseScenario picks delay=0 stress=1.0 at the first initial equity tier.
func BaseScenario(scenarios []ScenarioResult) *ScenarioResult {
	for i := range scenarios {
		s := &scenarios[i]
		if s.InitialEquityUSDT == g5protocol.ValidationInitialEquities[0] &&
			s.DelayBars == 0 && s.CostStressMultiplier == 1.0 {
			cp := *s
			return &cp
		}
	}
	return nil
}

// DailyEquityReplay tracks per-day equity deltas for sign tests.
type DailyEquityReplay struct {
	DailyPnL          map[string]float64
	ClusterPnL        map[string]float64
	ShockDayPnL       map[string]float64
	NonShockDayPnL    map[string]float64
	InitialEquityUSDT float64
	FinalEquityUSDT   float64
	TailAccrued       bool
}

// DailyPnLSum returns the summed per-UTC-day equity deltas.
func (d DailyEquityReplay) DailyPnLSum() float64 {
	total := 0.0
	for _, pnl := range d.DailyPnL {
		total += pnl
	}
	return total
}

// ClusterPnLSum returns the summed incremental cluster PnL.
func (d DailyEquityReplay) ClusterPnLSum() float64 {
	total := 0.0
	for _, pnl := range d.ClusterPnL {
		total += pnl
	}
	return total
}

// BalancesWithFinalEquity reports whether daily deltas reconcile to final equity.
func (d DailyEquityReplay) BalancesWithFinalEquity() bool {
	return math.Abs(d.DailyPnLSum()-(d.FinalEquityUSDT-d.InitialEquityUSDT)) <= 1e-6
}

// RunDailyTracking replays base scenario with the same funding/stress path as validation scenarios.
func (e ScenarioEngine) RunDailyTracking(
	featureRows map[string][]features.DecisionContext,
	timestamps []string,
	equity float64,
) DailyEquityReplay {
	return e.runDailyTracking(featureRows, timestamps, equity, 0, 1.0)
}

func (e ScenarioEngine) runDailyTracking(
	featureRows map[string][]features.DecisionContext,
	timestamps []string,
	equity float64,
	delay int,
	stress float64,
) DailyEquityReplay {
	track := DailyEquityReplay{
		DailyPnL:          map[string]float64{},
		ClusterPnL:        map[string]float64{},
		ShockDayPnL:       map[string]float64{},
		NonShockDayPnL:    map[string]float64{},
		InitialEquityUSDT: equity,
	}
	if len(timestamps) == 0 {
		track.FinalEquityUSDT = equity
		return track
	}
	account := NewAccount(equity)
	queue := &PendingQueue{}
	prevEquity := equity
	prevSymbolPnL := map[string]float64{}
	var prevDecision time.Time
	for _, ts := range timestamps {
		rows := featureRows[ts]
		decisionTime, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		day := decisionTime.UTC().Format("2006-01-02")
		shock := isShockDay(rows)
		if !prevDecision.IsZero() {
			symbols := positionSymbols(account)
			account.ApplyHoldFunding(e.Bars.Funding, symbols, prevDecision, decisionTime, stress)
			account.MarkToMarket(ClosePricesAtDecision(e.Bars, e.Profile, symbols, decisionTime))
		}
		inputs := BuildDecisionInputs(rows, account.Positions, account.EquityUSDT)
		ReplayBar(account, queue, e.Policy, inputs, e.Bars, e.Profile, decisionTime, e.Horizons, delay, stress, e.Partition)
		priceSymbols := make([]string, 0, len(rows))
		for _, row := range rows {
			priceSymbols = append(priceSymbols, row.Symbol)
		}
		account.MarkToMarket(ClosePricesAtDecision(e.Bars, e.Profile, priceSymbols, decisionTime))
		accrueReplayDelta(&track, day, shock, &prevEquity, account, prevSymbolPnL)
		prevDecision = decisionTime
	}
	_ = queue.DiscardAll()
	track.TailAccrued = e.accrueEOFTail(
		account,
		&track,
		featureRows,
		timestamps,
		prevDecision,
		prevEquity,
		prevSymbolPnL,
		stress,
	)
	track.FinalEquityUSDT = account.EquityUSDT
	return track
}

func accrueReplayDelta(
	track *DailyEquityReplay,
	day string,
	shock bool,
	prevEquity *float64,
	account *Account,
	prevSymbolPnL map[string]float64,
) {
	delta := account.EquityUSDT - *prevEquity
	track.DailyPnL[day] += delta
	if shock {
		track.ShockDayPnL[day] += delta
	} else {
		track.NonShockDayPnL[day] += delta
	}
	for sym, pnl := range account.SymbolPnL {
		symDelta := pnl - prevSymbolPnL[sym]
		if symDelta != 0 {
			cluster := policy.ClusterFor(nil, sym)
			track.ClusterPnL[cluster] += symDelta
		}
		prevSymbolPnL[sym] = pnl
	}
	*prevEquity = account.EquityUSDT
}

func (e ScenarioEngine) accrueEOFTail(
	account *Account,
	track *DailyEquityReplay,
	featureRows map[string][]features.DecisionContext,
	timestamps []string,
	prevDecision time.Time,
	prevEquity float64,
	prevSymbolPnL map[string]float64,
	stress float64,
) bool {
	if prevDecision.IsZero() {
		return false
	}
	lastTS := timestamps[len(timestamps)-1]
	lastTime, err := time.Parse(time.RFC3339Nano, lastTS)
	if err != nil {
		return false
	}
	symbols := positionSymbols(account)
	if len(symbols) > 0 {
		account.ApplyHoldFunding(e.Bars.Funding, symbols, prevDecision, lastTime, stress)
	}
	lastRows := featureRows[lastTS]
	priceSymbols := make([]string, 0, len(lastRows)+len(symbols))
	priceSymbols = append(priceSymbols, symbols...)
	for _, row := range lastRows {
		priceSymbols = append(priceSymbols, row.Symbol)
	}
	sort.Strings(priceSymbols)
	uniq := priceSymbols[:0]
	for i, sym := range priceSymbols {
		if i == 0 || sym != priceSymbols[i-1] {
			uniq = append(uniq, sym)
		}
	}
	account.MarkToMarket(ClosePricesAtDecision(e.Bars, e.Profile, uniq, lastTime))
	day := lastTime.UTC().Format("2006-01-02")
	accrueReplayDelta(track, day, isShockDay(lastRows), &prevEquity, account, prevSymbolPnL)
	return true
}

func positionSymbols(account *Account) []string {
	symbols := make([]string, 0, len(account.Positions))
	for sym := range account.Positions {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)
	return symbols
}

func isShockDay(rows []features.DecisionContext) bool {
	for _, row := range rows {
		if dd, ok := row.Features["btc_drawdown"]; ok && dd >= 0.5 {
			return true
		}
		if vol, ok := row.Features["btc_vol"]; ok && vol >= 0.8 {
			return true
		}
	}
	return false
}
