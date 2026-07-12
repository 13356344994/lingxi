package counterfactual

import (
	"sort"
	"time"

	"lingxi/internal/datatruth"
)

// Compare builds the eight-action counterfactual table for one decision context.
func Compare(ctx DecisionContext, protocolHash string) (CounterfactualTable, error) {
	table := CounterfactualTable{
		SchemaVersion: SchemaVersionTable,
		Generation:    Generation,
		ProtocolHash:  protocolHash,
		DecisionTime:  ctx.DecisionTime.UTC(),
		Symbol:        ctx.Symbol,
	}
	for _, action := range ActionsCompared {
		m := scoreAction(ctx, action)
		applyOpenGates(ctx, &m)
		table.Actions = append(table.Actions, m)
	}
	sort.SliceStable(table.Actions, func(i, j int) bool {
		if table.Actions[i].RankScore == table.Actions[j].RankScore {
			return table.Actions[i].Action < table.Actions[j].Action
		}
		return table.Actions[i].RankScore > table.Actions[j].RankScore
	})
	hash, err := HashTable(table)
	if err != nil {
		return CounterfactualTable{}, err
	}
	table.TableHash = hash
	return table, nil
}

// Decide selects the best allowed action from a counterfactual table.
func Decide(ctx DecisionContext, protocolHash string) (Decision, error) {
	table, err := Compare(ctx, protocolHash)
	if err != nil {
		return Decision{}, err
	}
	byAction := map[string]ActionMetric{}
	var rejects []string
	for _, row := range table.Actions {
		byAction[row.Action] = row
		if !row.Allowed {
			rejects = append(rejects, row.RejectReasons...)
		}
	}
	final := selectFinalAction(ctx, byAction)
	decision := Decision{
		SchemaVersion: SchemaVersionDecision,
		Table:         table,
		FinalAction:   final.Action,
		DelayBars:     final.DelayBars,
		SizeScale:     final.SizeScale,
		RejectReasons: uniqueStrings(rejects),
	}
	if decision.FinalAction == "NO_TRADE" && len(decision.RejectReasons) == 0 && !ctx.Event.HasEvent {
		decision.RejectReasons = append(decision.RejectReasons, RejectNoMatchingEvent)
	}
	hash, err := HashDecision(decision)
	if err != nil {
		return Decision{}, err
	}
	decision.DecisionHash = hash
	return decision, nil
}

func selectFinalAction(ctx DecisionContext, byAction map[string]ActionMetric) ActionMetric {
	openCandidates := []string{"LONG", "SHORT"}
	if ctx.Position.HasPosition {
		openCandidates = append(openCandidates, "ADD", "REVERSE")
	}
	if best, ok := bestAllowed(byAction, openCandidates); ok {
		if nearZeroOpen(best) {
			if row, ok := byAction["NO_TRADE"]; ok && row.Allowed {
				return row
			}
		}
		return best
	}
	if !anyOpenAllowed(byAction, openCandidates) && !ctx.Position.HasPosition {
		if row, ok := byAction["NO_TRADE"]; ok && row.Allowed {
			return row
		}
	}
	if ctx.Position.HasPosition {
		mgmt := []string{"TRIM", "CLOSE", "HOLD", "REVERSE"}
		if !ctx.Event.HasEvent {
			mgmt = []string{"HOLD", "TRIM", "CLOSE"}
		}
		if best, ok := bestAllowed(byAction, mgmt); ok {
			return best
		}
	}
	if ctx.Event.HasEvent {
		if row, ok := byAction["HOLD"]; ok && row.Allowed {
			return row
		}
	}
	if row, ok := byAction["NO_TRADE"]; ok && row.Allowed {
		return row
	}
	return ActionMetric{Action: "NO_TRADE", Allowed: true, SizeScale: 0}
}

func bestAllowed(byAction map[string]ActionMetric, order []string) (ActionMetric, bool) {
	var best *ActionMetric
	for _, action := range order {
		row, ok := byAction[action]
		if !ok || !row.Allowed {
			continue
		}
		if best == nil || row.RankScore > best.RankScore {
			copy := row
			best = &copy
		}
	}
	if best == nil {
		return ActionMetric{}, false
	}
	return *best, true
}

func nearZeroOpen(m ActionMetric) bool {
	return m.PersonalConservativeNetBPS >= 0 &&
		m.PersonalConservativeNetBPS < NearZeroEdgeThresholdBPS
}

func anyOpenAllowed(byAction map[string]ActionMetric, actions []string) bool {
	for _, action := range actions {
		if row, ok := byAction[action]; ok && row.Allowed {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// HashTable returns canonical hash for one counterfactual table.
func HashTable(table CounterfactualTable) (string, error) {
	payload := table
	payload.TableHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashDecision returns canonical hash for one decision (decision_hash cleared).
func HashDecision(d Decision) (string, error) {
	payload := d
	payload.DecisionHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// CompareAt is a convenience wrapper with explicit decision time.
func CompareAt(ctx DecisionContext, protocolHash string, at time.Time) (CounterfactualTable, error) {
	ctx.DecisionTime = at.UTC()
	return Compare(ctx, protocolHash)
}
