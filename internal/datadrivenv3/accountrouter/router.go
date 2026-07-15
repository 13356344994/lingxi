// Package accountrouter performs the preregistered chronological account
// replay for Terra leaf-05. Only validation and leaf_wf partitions are
// accepted; discovery labels and router_holdout are never opened.
package accountrouter

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
	"lingxi/internal/datadrivenv3/pathvalidation"
)

const (
	SchemaVersion       = "lingxi.data-driven-router-v3.account-router.v3"
	leafID              = "leaf-05"
	hourMS        int64 = 60 * 60 * 1000
)

var fractions = []float64{1, .5, .25, .1} // 1.0 is a failed control, never selectable.
var tiers = []float64{30, 100, 1000}

type Config struct {
	Root     string
	Progress io.Writer
}

type Metrics struct {
	InitialDeposit             float64       `json:"initial_deposit"`
	TerminalEquity             float64       `json:"terminal_equity"`
	CashflowAdjustedWealth     float64       `json:"cashflow_adjusted_wealth"`
	Withdrawals                float64       `json:"withdrawals"`
	MaxDrawdown                float64       `json:"max_drawdown_soft"`
	Ruin                       bool          `json:"ruin"`
	Activation                 int           `json:"activation"`
	MissedCertifiedOpportunity int           `json:"missed_certified_opportunity"`
	TradeCount                 int           `json:"trade_count"`
	UnknownCostCount           int           `json:"unknown_cost_count"`
	SkippedIneligibleSignal    int           `json:"skipped_ineligible_signal"`
	FirstRuinMS                *int64        `json:"first_ruin_ms,omitempty"`
	RuinReason                 string        `json:"ruin_reason,omitempty"`
	RuinSnapshot               *RuinSnapshot `json:"ruin_snapshot,omitempty"`
}

type PositionSnapshot struct {
	Instrument string  `json:"instrument"`
	Notional   float64 `json:"notional"`
	Entry      float64 `json:"entry"`
	LastMark   float64 `json:"last_mark"`
	ExpiryMS   int64   `json:"expiry_ms"`
}

type RuinSnapshot struct {
	Equity        float64            `json:"equity"`
	GrossNotional float64            `json:"gross_notional"`
	Positions     []PositionSnapshot `json:"positions"`
}

type ScenarioResult struct {
	DelayBars               int                `json:"delay_bars"`
	CostStress              float64            `json:"cost_stress"`
	Tiers                   map[string]Metrics `json:"tiers"`
	Pass                    bool               `json:"passes_scenario"`
	ExecutionStartMS        int64              `json:"execution_start_ms"`
	ExecutionEndMS          int64              `json:"execution_end_ms"`
	EligibleSignalCount     int                `json:"eligible_signal_count"`
	SkippedIneligibleSignal int                `json:"skipped_ineligible_signal"`
}

type FractionResult struct {
	Fraction  float64          `json:"fraction"`
	Scenarios []ScenarioResult `json:"scenarios"`
	Pass      bool             `json:"passes_selection"`
}

type Report struct {
	SchemaVersion               string           `json:"schema_version"`
	GeneratedAt                 string           `json:"generated_at"`
	InvalidatesRun              string           `json:"invalidates_run"`
	InvalidationReason          string           `json:"invalidation_reason"`
	RouterHoldoutUnblinded      bool             `json:"router_holdout_unblinded"`
	TradeAllowed                bool             `json:"trade_allowed"`
	LeafID                      string           `json:"leaf_id"`
	Predicate                   string           `json:"predicate"`
	HorizonHours                int              `json:"horizon_hours"`
	SelectionPartition          string           `json:"selection_partition"`
	ConfirmationPartition       string           `json:"confirmation_partition"`
	Validation                  []FractionResult `json:"validation"`
	SelectedFraction            *float64         `json:"selected_fraction,omitempty"`
	WalkForward                 []FractionResult `json:"leaf_wf,omitempty"`
	Verdict                     string           `json:"verdict"`
	SealedPolicyHash            string           `json:"sealed_policy_hash"`
	RouterCandidateManifest     string           `json:"router_candidate_manifest,omitempty"`
	RouterCandidateManifestHash string           `json:"router_candidate_manifest_hash,omitempty"`
}

type position struct {
	Instrument string
	Notional   float64
	Entry      float64
	ExpiryMS   int64
	LastMark   float64
}

type account struct {
	Cash, Withdrawals float64
	Positions         map[string]position
	Ruined            bool
	Peak, MaxDD       float64
	Activation        int
	Missed            int
	Trades            int
	Unknown           int
	SkippedIneligible int
	FirstRuinMS       *int64
	RuinReason        string
	RuinSnapshot      *RuinSnapshot
}

func Run(cfg Config) (Report, error) {
	if cfg.Root == "" {
		return Report{}, errors.New("root required")
	}
	policy, policyHash, err := loadTerraPolicy(cfg.Root)
	if err != nil {
		return Report{}, err
	}
	leaf, ok := policyLeaf(policy)
	if !ok {
		return Report{}, fmt.Errorf("sealed Terra %s missing", leafID)
	}
	rows, err := loadRows(cfg.Root, []string{"validation", "leaf_wf"})
	if err != nil {
		return Report{}, err
	}
	funding, _, err := counterfactual.LoadFunding(cfg.Root)
	if err != nil {
		return Report{}, err
	}
	bars, err := loadRequiredBars(cfg.Root, rows)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		InvalidatesRun:         "research/firstbattle/data_driven_router_v3/account_router/run-20260715-gpt56sol-g1-selection-v3",
		InvalidationReason:     "MISSING_PREPARE_ELIGIBILITY_GATE",
		RouterHoldoutUnblinded: false, TradeAllowed: false, LeafID: leafID, Predicate: leaf.Predicate,
		HorizonHours: 24, SelectionPartition: "validation", ConfirmationPartition: "leaf_wf",
		SealedPolicyHash: policyHash,
	}
	for _, fraction := range fractions {
		result, err := replayFraction(rows["validation"], policy, fraction, bars, funding, cfg.Progress)
		if err != nil {
			return Report{}, fmt.Errorf("validation fraction %g: %w", fraction, err)
		}
		result.Pass = fraction < 1 && allScenariosPass(result.Scenarios)
		report.Validation = append(report.Validation, result)
	}
	for _, result := range report.Validation {
		if result.Pass && (report.SelectedFraction == nil || result.Fraction > *report.SelectedFraction) {
			value := result.Fraction
			report.SelectedFraction = &value
		}
	}
	if report.SelectedFraction == nil {
		report.Verdict = "NO_ROUTE"
		return report, nil
	}
	wf, err := replayFraction(rows["leaf_wf"], policy, *report.SelectedFraction, bars, funding, cfg.Progress)
	if err != nil {
		return Report{}, fmt.Errorf("leaf_wf confirmation: %w", err)
	}
	wf.Pass = allScenariosPass(wf.Scenarios)
	report.WalkForward = []FractionResult{wf}
	if !wf.Pass {
		report.Verdict = "NO_ROUTE"
		return report, nil
	}
	report.Verdict = "ROUTER_CANDIDATE"
	return report, nil
}

func replayFraction(rows []datav3.StateRowV2, policy modelapi.Policy, fraction float64, bars map[string]map[int64]counterfactual.Bar, funding counterfactual.FundingSource, progress io.Writer) (FractionResult, error) {
	result := FractionResult{Fraction: fraction}
	for delay := 0; delay <= 3; delay++ {
		for _, stress := range []float64{1, 1.5} {
			scenario, err := replayScenario(rows, policy, fraction, delay, stress, bars, funding)
			if err != nil {
				return FractionResult{}, err
			}
			scenario.Pass = scenarioPassesFrozenGate(scenario)
			result.Scenarios = append(result.Scenarios, scenario)
			if progress != nil {
				fmt.Fprintf(progress, "stage=portfolio_replay fraction=%g delay=%d stress=%g pass=%t\n", fraction, delay, stress, scenario.Pass)
			}
		}
	}
	return result, nil
}

func replayScenario(rows []datav3.StateRowV2, policy modelapi.Policy, fraction float64, delay int, stress float64, bars map[string]map[int64]counterfactual.Bar, funding counterfactual.FundingSource) (ScenarioResult, error) {
	if len(rows) == 0 {
		return ScenarioResult{}, errors.New("empty partition")
	}
	signals := map[int64][]datav3.StateRowV2{}
	skippedIneligible := 0
	eligibleSignals := 0
	minDecision, maxDecision := rows[0].DecisionMS, rows[0].DecisionMS
	for _, row := range rows {
		if row.DecisionMS < minDecision {
			minDecision = row.DecisionMS
		}
		if row.DecisionMS > maxDecision {
			maxDecision = row.DecisionMS
		}
		candidate := counterfactual.CandidateFor(row)
		prepared, eligible, err := pathvalidation.PrepareRow(candidate, bars[row.Instrument], funding)
		if err != nil {
			return ScenarioResult{}, err
		}
		if !eligible {
			skippedIneligible++
			continue
		}
		market := modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: row.EventID, DecisionMS: row.DecisionMS, Features: row.Values, MissingBits: row.MissingBits}
		out, err := pathvalidation.Replay("terra", policy, market, prepared.Account)
		if err != nil {
			return ScenarioResult{}, err
		}
		if out.Status == pathvalidation.MatchUnique && policy.Leaves[out.LeafIndex].LeafID == leafID {
			eligibleSignals++
			executionMS := row.DecisionMS + int64(delay)*hourMS
			signals[executionMS] = append(signals[executionMS], row)
		}
	}
	endMS := maxDecision + int64(delay+24)*hourMS
	accounts := map[float64]*account{}
	for _, tier := range tiers {
		accounts[tier] = &account{Cash: tier, Positions: map[string]position{}, Peak: tier, SkippedIneligible: skippedIneligible}
	}
	for now := minDecision; now <= endMS; now += hourMS {
		for _, tier := range tiers {
			markAndExpire(accounts[tier], now, tier, stress, bars, funding)
		}
		batch := signals[now]
		sort.Slice(batch, func(i, j int) bool { return eventHash(batch[i].EventID) < eventHash(batch[j].EventID) })
		dropped := 0
		if len(batch) > 5 {
			dropped = len(batch) - 5
			batch = batch[:5]
		}
		for _, tier := range tiers {
			a := accounts[tier]
			a.Missed += dropped
			for _, row := range batch {
				if a.Ruined {
					a.Missed++
					continue
				}
				if _, exists := a.Positions[row.Instrument]; exists {
					a.Missed++
					continue
				}
				if grossNotional(a)+fraction*tier > 2*tier+1e-12 {
					a.Missed++
					continue
				}
				bar, ok := bars[row.Instrument][now]
				if !ok {
					a.Unknown++
					a.Missed++
					continue
				}
				if !openPosition(a, row.Instrument, fraction*tier, tier, now, stress, bar) {
					a.Missed++
				}
			}
		}
	}
	result := ScenarioResult{
		DelayBars: delay, CostStress: stress, Tiers: map[string]Metrics{},
		ExecutionStartMS: minDecision, ExecutionEndMS: endMS,
		EligibleSignalCount: eligibleSignals, SkippedIneligibleSignal: skippedIneligible,
	}
	for _, tier := range tiers {
		a := accounts[tier]
		if len(a.Positions) != 0 {
			a.Unknown += len(a.Positions)
			recordRuin(a, endMS, "UNEXPIRED_AT_EOF")
		}
		terminal := a.Cash
		result.Tiers[fmt.Sprintf("%.0f", tier)] = Metrics{
			InitialDeposit: tier, TerminalEquity: terminal,
			CashflowAdjustedWealth: a.Withdrawals + terminal - tier,
			Withdrawals:            a.Withdrawals, MaxDrawdown: a.MaxDD, Ruin: a.Ruined,
			Activation: a.Activation, MissedCertifiedOpportunity: a.Missed,
			TradeCount: a.Trades, UnknownCostCount: a.Unknown,
			SkippedIneligibleSignal: a.SkippedIneligible, FirstRuinMS: a.FirstRuinMS,
			RuinReason: a.RuinReason, RuinSnapshot: a.RuinSnapshot,
		}
	}
	return result, nil
}

func openPosition(a *account, instrument string, notional, tier float64, now int64, stress float64, bar counterfactual.Bar) bool {
	costBPS, err := executionCost(bar, tier, stress)
	if err != nil {
		a.Unknown++
		return false
	}
	cost := notional * costBPS / 10000
	currentEquity := markedEquity(a)
	if a.Cash-cost < -1e-12 || currentEquity-cost <= 0 || grossNotional(a)+notional > 5*(currentEquity-cost)+1e-12 {
		return false
	}
	a.Cash -= cost
	a.Positions[instrument] = position{Instrument: instrument, Notional: notional, Entry: bar.Close, ExpiryMS: now + 24*hourMS, LastMark: bar.Close}
	a.Activation++
	a.Trades++
	return true
}

func markAndExpire(a *account, now int64, tier, stress float64, bars map[string]map[int64]counterfactual.Bar, funding counterfactual.FundingSource) {
	if a.Ruined {
		return
	}
	instruments := make([]string, 0, len(a.Positions))
	for instrument := range a.Positions {
		instruments = append(instruments, instrument)
	}
	sort.Strings(instruments)
	// Mark and settle every open instrument before any close/withdrawal, so
	// account equity and reserve checks cannot depend on Go map iteration.
	for _, instrument := range instruments {
		p := a.Positions[instrument]
		bar, ok := bars[instrument][now]
		if !ok {
			recordRuin(a, now, "MISSING_ACTIVE_BAR")
			a.Unknown++
			return
		}
		p.LastMark = bar.Close
		a.Positions[instrument] = p
		for _, event := range funding.Events[instrument] {
			if event.TimeMS == now {
				a.Cash -= p.Notional * event.Rate
			}
		}
	}
	for _, instrument := range instruments {
		p, exists := a.Positions[instrument]
		if !exists {
			continue
		}
		if p.ExpiryMS == now {
			if !closePosition(a, p, tier, stress, bars[instrument][now]) {
				recordRuin(a, now, "UNKNOWN_CLOSE_COST")
				return
			}
		}
	}
	equity := markedEquity(a)
	if equity <= 0 {
		recordRuin(a, now, "EQUITY_NONPOSITIVE")
	} else if grossNotional(a) > 5*equity+1e-12 {
		recordRuin(a, now, "LEVERAGE_BREACH")
	}
	updateDrawdown(a, equity)
}

func closePosition(a *account, p position, tier, stress float64, bar counterfactual.Bar) bool {
	costBPS, err := executionCost(bar, tier, stress)
	if err != nil {
		a.Unknown++
		return false
	}
	a.Cash += p.Notional*(bar.Close/p.Entry-1) - p.Notional*costBPS/10000
	delete(a.Positions, p.Instrument)
	withdraw(a, tier)
	return true
}

func recordRuin(a *account, now int64, reason string) {
	if a.Ruined {
		return
	}
	equity := markedEquity(a)
	snapshot := &RuinSnapshot{Equity: equity, GrossNotional: grossNotional(a)}
	for _, p := range a.Positions {
		snapshot.Positions = append(snapshot.Positions, PositionSnapshot{
			Instrument: p.Instrument, Notional: p.Notional, Entry: p.Entry,
			LastMark: p.LastMark, ExpiryMS: p.ExpiryMS,
		})
	}
	sort.Slice(snapshot.Positions, func(i, j int) bool { return snapshot.Positions[i].Instrument < snapshot.Positions[j].Instrument })
	at := now
	a.FirstRuinMS = &at
	a.RuinReason = reason
	a.RuinSnapshot = snapshot
	a.Ruined = true
	a.Cash = 0
	a.Positions = map[string]position{}
}

func markedEquity(a *account) float64 {
	equity := a.Cash
	for _, p := range a.Positions {
		equity += p.Notional * (p.LastMark/p.Entry - 1)
	}
	return equity
}

func updateDrawdown(a *account, equity float64) {
	if equity > a.Peak {
		a.Peak = equity
	}
	if a.Peak > 0 {
		a.MaxDD = math.Max(a.MaxDD, (a.Peak-equity)/a.Peak)
	}
}

func executionCost(bar counterfactual.Bar, tier, stress float64) (float64, error) {
	roundTrip, err := datav3.DynamicExecutionCostBPS(datav3.RewardCostInput{
		Known: true, RoundTripFeeBPS: 10, CapitalUSDT: tier,
		RollingQuoteVolumeUSDT: bar.RollingQuoteVolumeUSDT,
		BarHigh:                bar.High, BarLow: bar.Low, BarMid: (bar.High + bar.Low) / 2,
		StressMultiplier: stress,
	})
	return roundTrip / 2, err
}

func withdraw(a *account, tier float64) {
	for markedEquity(a) >= 1.5*tier {
		reserve := grossNotional(a) / 5
		available := math.Min(a.Cash, markedEquity(a)-reserve)
		amount := math.Floor(math.Min(.5*tier, math.Max(0, available))*1e6) / 1e6
		if amount <= 0 {
			return
		}
		a.Cash -= amount
		a.Withdrawals += amount
	}
}

func grossNotional(a *account) float64 {
	var total float64
	for _, p := range a.Positions {
		total += p.Notional
	}
	return total
}

func allScenariosPass(scenarios []ScenarioResult) bool {
	if len(scenarios) != 8 {
		return false
	}
	for _, scenario := range scenarios {
		if !scenario.Pass {
			return false
		}
	}
	return true
}

// scenarioPassesFrozenGate implements the frozen g1 asymmetry:
// every scenario is a hard-safety check, base-cost scenarios additionally
// require positive wealth at every delay, and the 1.5x cost-stress profit
// check applies only at the path's own frozen delay (leaf-05 delay=0).
func scenarioPassesFrozenGate(scenario ScenarioResult) bool {
	if !allTiersSafe(scenario.Tiers) {
		return false
	}
	if scenario.CostStress == 1 || scenario.DelayBars == 0 {
		return allTiersPositive(scenario.Tiers)
	}
	return true
}

func allTiersSafe(results map[string]Metrics) bool {
	if len(results) != 3 {
		return false
	}
	for _, metrics := range results {
		if metrics.Ruin || metrics.UnknownCostCount != 0 {
			return false
		}
	}
	return true
}

func allTiersPositive(results map[string]Metrics) bool {
	if len(results) != 3 {
		return false
	}
	for _, metrics := range results {
		if metrics.CashflowAdjustedWealth <= 0 {
			return false
		}
	}
	return true
}

func eventHash(event string) string {
	sum := sha256.Sum256([]byte(event))
	return hex.EncodeToString(sum[:])
}

func loadRequiredBars(root string, partitions map[string][]datav3.StateRowV2) (map[string]map[int64]counterfactual.Bar, error) {
	out := map[string]map[int64]counterfactual.Bar{}
	for _, partition := range []string{"validation", "leaf_wf"} {
		for _, row := range partitions[partition] {
			if _, exists := out[row.Instrument]; exists {
				continue
			}
			loaded, err := counterfactual.LoadBars(root, row.Instrument, row.SourceVersion)
			if err != nil {
				return nil, err
			}
			out[row.Instrument] = loaded
		}
	}
	return out, nil
}

func loadTerraPolicy(root string) (modelapi.Policy, string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000/policies/terra.json"))
	if err != nil {
		return modelapi.Policy{}, "", err
	}
	var policy modelapi.Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return policy, "", err
	}
	compact, err := json.Marshal(policy)
	if err != nil {
		return policy, "", err
	}
	sum := sha256.Sum256(compact)
	return policy, hex.EncodeToString(sum[:]), nil
}

func policyLeaf(policy modelapi.Policy) (modelapi.CandidateLeaf, bool) {
	for _, leaf := range policy.Leaves {
		if leaf.LeafID == leafID {
			return leaf, true
		}
	}
	return modelapi.CandidateLeaf{}, false
}

func loadRows(root string, allowed []string) (map[string][]datav3.StateRowV2, error) {
	wanted := map[string]bool{}
	for _, partition := range allowed {
		if partition != "validation" && partition != "leaf_wf" {
			return nil, fmt.Errorf("account router refuses partition %q", partition)
		}
		wanted[partition] = true
	}
	raw, err := os.ReadFile(filepath.Join(root, "research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3/dataset_manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Shards []datav3.ShardReceiptV2 `json:"shards"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	out := map[string][]datav3.StateRowV2{}
	for _, shard := range manifest.Shards {
		if !wanted[shard.Partition] {
			continue
		}
		rows, err := readShard(filepath.Join(root, "research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3", shard.Path), shard)
		if err != nil {
			return nil, err
		}
		out[shard.Partition] = append(out[shard.Partition], rows...)
	}
	return out, nil
}

func readShard(path string, receipt datav3.ShardReceiptV2) ([]datav3.StateRowV2, error) {
	hash, err := datav3.HashFileStreaming(path)
	if err != nil || hash != receipt.SHA256 {
		return nil, errors.New("partition shard hash mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(bufio.NewReader(file))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var rows []datav3.StateRowV2
	decoder := json.NewDecoder(zr)
	for {
		var row datav3.StateRowV2
		err = decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if row.Partition != receipt.Partition {
			return nil, errors.New("partition row mismatch")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
