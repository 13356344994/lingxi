package portfoliosim

import (
	"sort"
	"time"

	"lingxi/internal/routerg5/policy"
)

// PendingAction is one delayed execution queued at decision time.
type PendingAction struct {
	Proposal      policy.Proposal
	DecisionTime  time.Time
	DecisionIndex int
	ExecuteIndex  int
	ScenarioDelay int
	CostStress    float64
}

// PendingQueue holds in-flight actions awaiting their execute bar.
type PendingQueue struct {
	Actions []PendingAction
}

// Enqueue appends one pending action.
func (q *PendingQueue) Enqueue(pa PendingAction) {
	q.Actions = append(q.Actions, pa)
}

// FlushMatured extracts actions with ExecuteIndex <= maxExecuteIndex.
func (q *PendingQueue) FlushMatured(maxExecuteIndex int) []PendingAction {
	if len(q.Actions) == 0 {
		return nil
	}
	var matured []PendingAction
	remaining := q.Actions[:0]
	for _, pa := range q.Actions {
		if pa.ExecuteIndex <= maxExecuteIndex {
			matured = append(matured, pa)
		} else {
			remaining = append(remaining, pa)
		}
	}
	q.Actions = remaining
	sort.SliceStable(matured, func(i, j int) bool {
		if matured[i].ExecuteIndex != matured[j].ExecuteIndex {
			return matured[i].ExecuteIndex < matured[j].ExecuteIndex
		}
		if !matured[i].DecisionTime.Equal(matured[j].DecisionTime) {
			return matured[i].DecisionTime.Before(matured[j].DecisionTime)
		}
		return matured[i].Proposal.ID < matured[j].Proposal.ID
	})
	return matured
}

// DiscardAll drops unexpired actions at EOF; returns count discarded.
func (q *PendingQueue) DiscardAll() int {
	n := len(q.Actions)
	q.Actions = nil
	return n
}
