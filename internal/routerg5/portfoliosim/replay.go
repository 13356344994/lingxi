package portfoliosim

import (
	"math"
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// ReplayBar applies one decision bar: flush matured pending, then enqueue Top-K.
// allowedPartition restricts scoring to one frozen partition label (validation or walk_forward).
func ReplayBar(
	account *Account,
	queue *PendingQueue,
	pol reverselearn.FrozenPolicy,
	rows []policy.DecisionInput,
	bundle BarsBundle,
	profile features.Profile,
	decisionTime time.Time,
	horizons []string,
	scenarioDelay int,
	costStress float64,
	allowedPartition string,
) {
	if len(rows) == 0 {
		return
	}
	symbols := make([]string, 0, len(rows))
	for _, row := range rows {
		symbols = append(symbols, row.Symbol)
	}
	prices := ClosePricesAtDecision(bundle, profile, symbols, decisionTime)
	account.MarkToMarket(prices)

	decisionIndex := -1
	for _, row := range rows {
		if idx := DecisionBarIndex(bundle, profile, row.Symbol, decisionTime); idx >= 0 {
			decisionIndex = idx
			break
		}
	}
	if decisionIndex < 0 {
		return
	}
	maxExecute := ExecuteIndexForDelay(decisionIndex, 0)
	matured := queue.FlushMatured(maxExecute)
	traded := executePending(account, bundle, matured)

	account.MarkToMarket(prices)

	var proposals []policy.Proposal
	for _, row := range rows {
		if allowedPartition != "" && row.Partition != allowedPartition {
			continue
		}
		state := account.CurrentPositionState(row.Symbol)
		row.StartState = state
		scored, err := policy.ScoreActions(pol, row, horizons)
		if err != nil || len(scored) == 0 {
			continue
		}
		best := scored[0]
		if best.Action.Verb == actionspace.VerbFlat {
			continue
		}
		prop, ok := policy.BuildProposal(best, account.EquityUSDT)
		if !ok {
			continue
		}
		account.TotalProposals++
		if prop.CapacityHeadroom <= 0 {
			account.CapacityRejects++
			continue
		}
		proposals = append(proposals, prop)
	}
	selected := SelectTopK(proposals, account.EquityUSDT)
	for _, prop := range selected {
		action := prop.Action
		action.DelayBars = scenarioDelay
		symIdx := DecisionBarIndex(bundle, profile, prop.Symbol, decisionTime)
		if symIdx < 0 {
			continue
		}
		queue.Enqueue(PendingAction{
			Proposal:      prop,
			DecisionTime:  decisionTime,
			DecisionIndex: symIdx,
			ExecuteIndex:  ExecuteIndexForDelay(symIdx, scenarioDelay),
			ScenarioDelay: scenarioDelay,
			CostStress:    costStress,
		})
	}
	if traded || len(selected) > 0 {
		day := decisionTime.UTC().Format("2006-01-02")
		account.RecordDecision(day, traded)
	} else {
		day := decisionTime.UTC().Format("2006-01-02")
		account.RecordDecision(day, false)
	}
}

func executePending(account *Account, bundle BarsBundle, matured []PendingAction) bool {
	traded := false
	for _, pa := range matured {
		prop := pa.Proposal
		price, ok := EntryOpenPrice(bundle, prop.Symbol, pa.ExecuteIndex)
		if !ok {
			account.StaleRejects++
			continue
		}
		action, ok := PreparePendingExecution(pa, account)
		if !ok {
			account.StaleRejects++
			continue
		}
		current := account.CurrentPositionState(prop.Symbol)
		turnover := estimateTurnover(current, action, account.EquityUSDT)
		cost := executionCost(turnover, prop.CostEstimateBPS, pa.CostStress)
		if err := account.ApplyExecution(prop.Symbol, current, action, price, cost, 0); err != nil {
			account.StaleRejects++
			continue
		}
		traded = true
	}
	return traded
}

func estimateTurnover(state actionspace.PositionState, action actionspace.Action, equity float64) float64 {
	state.EquityUSDT = equity
	_, turnover, err := actionspace.ApplyTransition(state, action, 1)
	if err != nil {
		return 0
	}
	return turnover
}

func executionCost(turnover, costEstimateBPS, stress float64) float64 {
	if turnover <= 0 {
		return 0
	}
	bps := (g5protocol.RoundtripTakerFeeBPS + g5protocol.BaseSpreadSlippageFloorBPS + math.Max(0, costEstimateBPS)) * stress
	return turnover * bps / 10000
}

// NetReturnFraction returns (final-initial)/initial.
func NetReturnFraction(initial, final float64) float64 {
	if initial <= 0 {
		return 0
	}
	return (final - initial) / initial
}

// MergeActionCoverage aggregates verb counts.
func MergeActionCoverage(dst map[string]int, src map[string]int) {
	for k, v := range src {
		dst[k] = dst[k] + v
	}
}

// MaxDrawdownOK checks frozen 30% cap.
func MaxDrawdownOK(dd float64) bool {
	return dd <= g5protocol.ValidationMaxDrawdownFraction+1e-12
}

// PositiveReturn returns true when net return > 0.
func PositiveReturn(initial, final float64) bool {
	return final > initial+1e-9
}

// Sigmoid maps score edge to size — exported for tests.
func Sigmoid(x float64) float64 {
	if x > 20 {
		return 1
	}
	if x < -20 {
		return 0
	}
	return 1 / (1 + math.Exp(-x))
}
