package reverselearn

import (
	"sort"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// FrozenActionEncoding returns the protocol-bound action encoding spec.
func FrozenActionEncoding(horizonLabels []string) ActionEncodingSpec {
	verbs := append([]string(nil), g5protocol.ActionVerbs...)
	dirs := append([]string(nil), g5protocol.ActionEncodingDirections...)
	states := append([]string(nil), g5protocol.ActionEncodingStates...)
	numeric := append([]string(nil), g5protocol.ActionEncodingNumericFields...)
	dim := len(verbs) + len(dirs) + len(states) + len(numeric)
	return ActionEncodingSpec{
		Verbs:         verbs,
		Directions:    dirs,
		StartStates:   states,
		NumericFields: numeric,
		Dim:           dim,
	}
}

// EncodeAction builds the frozen action feature block.
func EncodeAction(spec ActionEncodingSpec, stateLabel string, action actionspace.Action, horizonIndex float64) []float64 {
	out := make([]float64, spec.Dim)
	idx := 0
	for _, verb := range spec.Verbs {
		if action.Verb == verb {
			out[idx] = 1
		}
		idx++
	}
	for _, dir := range spec.Directions {
		if action.Direction == dir {
			out[idx] = 1
		}
		idx++
	}
	for _, state := range spec.StartStates {
		if stateLabel == state {
			out[idx] = 1
		}
		idx++
	}
	out[idx] = action.SizeFraction
	idx++
	out[idx] = float64(action.DelayBars) / float64(g5protocol.DelayBars[len(g5protocol.DelayBars)-1])
	idx++
	out[idx] = horizonIndex
	return out
}

// HorizonIndex maps a horizon label to [0,1].
func HorizonIndex(label string, horizons []string) float64 {
	if len(horizons) == 0 {
		return 0
	}
	for i, h := range horizons {
		if h == label {
			if len(horizons) == 1 {
				return 0
			}
			return float64(i) / float64(len(horizons)-1)
		}
	}
	return 0
}

// BuildFeatureVector concatenates standardized context, action block, and interactions.
func BuildFeatureVector(
	featureKeys []string,
	norm Normalization,
	features map[string]float64,
	actionBlock []float64,
) []float64 {
	ctx := make([]float64, len(featureKeys))
	for i, key := range featureKeys {
		v := features[key]
		mean := norm.Means[key]
		std := norm.Stds[key]
		if std <= 1e-12 {
			std = 1
		}
		ctx[i] = (v - mean) / std
	}
	verbStart, verbEnd := actionVerbSlice(actionBlock)
	dirStart, dirEnd := actionDirectionSlice(actionBlock)
	stateStart, stateEnd := actionStateSlice(actionBlock)
	numericStart := len(actionBlock) - len(g5protocol.ActionEncodingNumericFields)

	interactions := make([]float64, 0, len(featureKeys)*(len(g5protocol.ActionVerbs)+len(g5protocol.ActionEncodingDirections)+len(g5protocol.ActionEncodingStates)+len(g5protocol.ActionEncodingNumericFields)))
	for i := range featureKeys {
		for j := verbStart; j < verbEnd; j++ {
			interactions = append(interactions, ctx[i]*actionBlock[j])
		}
		for j := dirStart; j < dirEnd; j++ {
			interactions = append(interactions, ctx[i]*actionBlock[j])
		}
		for j := stateStart; j < stateEnd; j++ {
			interactions = append(interactions, ctx[i]*actionBlock[j])
		}
		for j := numericStart; j < len(actionBlock); j++ {
			interactions = append(interactions, ctx[i]*actionBlock[j])
		}
	}
	out := make([]float64, 0, len(ctx)+len(actionBlock)+len(interactions))
	out = append(out, ctx...)
	out = append(out, actionBlock...)
	out = append(out, interactions...)
	return out
}

func actionVerbSlice(actionBlock []float64) (start, end int) {
	return 0, len(g5protocol.ActionVerbs)
}

func actionDirectionSlice(actionBlock []float64) (start, end int) {
	start = len(g5protocol.ActionVerbs)
	end = start + len(g5protocol.ActionEncodingDirections)
	return start, end
}

func actionStateSlice(actionBlock []float64) (start, end int) {
	start = len(g5protocol.ActionVerbs) + len(g5protocol.ActionEncodingDirections)
	end = start + len(g5protocol.ActionEncodingStates)
	return start, end
}

// FeatureDim returns total model input dimension.
func FeatureDim(featureCount int, actionSpec ActionEncodingSpec) int {
	return featureCount + actionSpec.Dim +
		featureCount*len(actionSpec.Verbs) +
		featureCount*len(actionSpec.Directions) +
		featureCount*len(actionSpec.StartStates) +
		featureCount*len(actionSpec.NumericFields)
}

// SortedHorizons returns sorted unique horizon labels from examples.
func SortedHorizons(examples []TrainingExample) []string {
	seen := map[string]bool{}
	for _, ex := range examples {
		seen[ex.Action.TargetHorizon] = true
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
