package policy

import (
	"math"
	"sort"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// ScoreActions ranks all legal actions for one decision input.
func ScoreActions(policy reverselearn.FrozenPolicy, input DecisionInput, horizons []string) ([]ScoredAction, error) {
	actions, err := actionspace.EnumerateActions(input.Symbol, input.StartState, horizons)
	if err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, nil
	}
	confidence := featureConfidence(input.Features)
	var scored []ScoredAction
	for _, action := range actions {
		ex := reverselearn.TrainingExample{
			Symbol: input.Symbol, StateLabel: input.StateLabel,
			Action: action, Features: input.Features,
		}
		raw := reverselearn.PredictScore(
			ex, policy.Weights, policy.FeatureKeys,
			policy.ActionEncoding, policy.HorizonLabels, policy.Normalization,
		)
		edge := raw
		size := ContinuousSize(edge, confidence, input.Features)
		if action.Verb == actionspace.VerbFlat || action.Verb == actionspace.VerbHold || action.Verb == actionspace.VerbClose {
			size = 0
		} else if size > 0 {
			action.SizeFraction = size
		}
		scored = append(scored, ScoredAction{
			Symbol: input.Symbol, Action: action, StateLabel: input.StateLabel,
			StartState: input.StartState, Features: input.Features,
			Score: raw, Edge: edge, Confidence: confidence, SizeFraction: action.SizeFraction,
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Action.Verb < scored[j].Action.Verb
	})
	return scored, nil
}

// BestAction returns the highest-scoring legal action.
func BestAction(policy reverselearn.FrozenPolicy, input DecisionInput, horizons []string) (ScoredAction, bool, error) {
	scored, err := ScoreActions(policy, input, horizons)
	if err != nil || len(scored) == 0 {
		return ScoredAction{}, false, err
	}
	return scored[0], true, nil
}

// ContinuousSize maps edge/confidence through sigmoid into [0,1] risk budget.
func ContinuousSize(edge, confidence float64, features map[string]float64) float64 {
	headroom := features["capacity_headroom"]
	if !isFinite(headroom) || headroom < 0 {
		headroom = 0
	}
	if headroom > 1 {
		headroom = 1
	}
	z := edge*confidence + features["sky_confidence"]*0.1
	size := sigmoid(z)
	minV, maxV := g5protocol.PolicySizeRange[0], g5protocol.PolicySizeRange[1]
	if size < minV {
		size = minV
	}
	if size > maxV {
		size = maxV
	}
	size *= headroom
	if size < 0.01 {
		return 0
	}
	return size
}

func featureConfidence(features map[string]float64) float64 {
	conf := features["sky_confidence"]
	if !isFinite(conf) {
		conf = 0.5
	}
	penalty := features["vol_cost_penalty"]
	if isFinite(penalty) && penalty > 0 {
		conf *= penalty
	}
	if conf < 0 {
		return 0
	}
	if conf > 1 {
		return 1
	}
	return conf
}

func sigmoid(x float64) float64 {
	if x > 20 {
		return 1
	}
	if x < -20 {
		return 0
	}
	return 1 / (1 + math.Exp(-x))
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// IsFixedDirectionPolicy rejects policies with the same direction across representative contexts.
func IsFixedDirectionPolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsFixedDirectionCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization)
}

// IsFixedHorizonPolicy rejects policies dominated by one horizon across representative contexts.
func IsFixedHorizonPolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsFixedHorizonCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}

// IsHoldForeverPolicy rejects policies that always hold from positioned states.
func IsHoldForeverPolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsHoldForeverCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}

// IsNeverClosePolicy rejects policies that never close/trim from positioned states.
func IsNeverClosePolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsNeverCloseCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}

// IsAlwaysReversePolicy rejects policies that always reverse from positioned states.
func IsAlwaysReversePolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsAlwaysReverseCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}

// IsFixedVerbPolicy rejects policies dominated by one verb across representative contexts.
func IsFixedVerbPolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsFixedVerbCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}

// IsDegeneratePolicy unions frozen fixed/degenerate action detectors for validation entry.
func IsDegeneratePolicy(policy reverselearn.FrozenPolicy) bool {
	return reverselearn.IsDegeneratePolicyCandidate(policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.Normalization, policy.HorizonLabels)
}
