package reverselearn

import (
	"math"
	"math/rand"
	"sort"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// FitNormalization computes train-only means/stds for context features.
func FitNormalization(featureKeys []string, train []TrainingExample) Normalization {
	means := map[string]float64{}
	stds := map[string]float64{}
	for _, key := range featureKeys {
		var sum, sumSq float64
		n := 0
		for _, ex := range train {
			v, ok := ex.Features[key]
			if !ok || !isFinite(v) {
				continue
			}
			sum += v
			sumSq += v * v
			n++
		}
		if n == 0 {
			means[key] = 0
			stds[key] = 1
			continue
		}
		mean := sum / float64(n)
		variance := sumSq/float64(n) - mean*mean
		if variance < 1e-12 {
			variance = 1
		}
		means[key] = mean
		stds[key] = math.Sqrt(variance)
	}
	return Normalization{Means: means, Stds: stds}
}

// TrainRidgeSGD fits ridge regression with deterministic SGD.
func TrainRidgeSGD(
	examples []TrainingExample,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
	lambda float64,
	seed int,
) []float64 {
	dim := FeatureDim(len(featureKeys), actionSpec)
	weights := make([]float64, dim)
	rng := rand.New(rand.NewSource(int64(seed)))
	indices := make([]int, len(examples))
	for i := range indices {
		indices[i] = i
	}
	lr := g5protocol.SGDLearningRate
	for epoch := 0; epoch < g5protocol.SGDEpochs; epoch++ {
		rng.Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
		for _, idx := range indices {
			ex := examples[idx]
			x := exampleVector(ex, featureKeys, actionSpec, horizons, norm)
			y := ex.RewardBPS
			pred := dot(weights, x)
			err := pred - y
			for i := range weights {
				grad := err*x[i] + lambda*weights[i]
				weights[i] -= lr * grad
			}
		}
	}
	return weights
}

func exampleVector(ex TrainingExample, featureKeys []string, actionSpec ActionEncodingSpec, horizons []string, norm Normalization) []float64 {
	actionBlock := EncodeAction(actionSpec, ex.StateLabel, ex.Action, HorizonIndex(ex.Action.TargetHorizon, horizons))
	return BuildFeatureVector(featureKeys, norm, ex.Features, actionBlock)
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// PredictScore returns model score for one example.
func PredictScore(ex TrainingExample, weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, horizons []string, norm Normalization) float64 {
	x := exampleVector(ex, featureKeys, actionSpec, horizons, norm)
	return dot(weights, x)
}

type decisionGroup struct {
	key      string
	examples []TrainingExample
}

func groupExamples(examples []TrainingExample) []decisionGroup {
	byKey := map[string][]TrainingExample{}
	order := []string{}
	for _, ex := range examples {
		key := JoinKey(ex.Symbol, ex.DecisionTime) + "\x00" + ex.StateLabel
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], ex)
	}
	sort.Strings(order)
	out := make([]decisionGroup, 0, len(order))
	for _, key := range order {
		out = append(out, decisionGroup{key: key, examples: byKey[key]})
	}
	return out
}

// EvaluateMetrics computes discovery holdout metrics.
func EvaluateMetrics(examples []TrainingExample, weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, horizons []string, norm Normalization) (regret, directionAcc, rewardMean float64) {
	groups := groupExamples(examples)
	if len(groups) == 0 {
		return 0, 0, 0
	}
	var regretSum, rewardSum float64
	var directionTotal, directionHit int
	for _, group := range groups {
		bestReward := math.Inf(-1)
		bestDirection := ""
		bestScore := math.Inf(-1)
		pickedReward := 0.0
		pickedDirection := ""
		for _, ex := range group.examples {
			if ex.RewardBPS > bestReward {
				bestReward = ex.RewardBPS
				bestDirection = ex.Action.Direction
			}
			score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
			if score > bestScore {
				bestScore = score
				pickedReward = ex.RewardBPS
				pickedDirection = ex.Action.Direction
			}
		}
		regretSum += bestReward - pickedReward
		rewardSum += pickedReward
		if bestDirection != "" && bestDirection != "flat" {
			directionTotal++
			if pickedDirection == bestDirection {
				directionHit++
			}
		}
	}
	regret = regretSum / float64(len(groups))
	rewardMean = rewardSum / float64(len(groups))
	if directionTotal > 0 {
		directionAcc = float64(directionHit) / float64(directionTotal)
	}
	return regret, directionAcc, rewardMean
}

// SplitChronologicalHoldout splits discovery rows 80/20 by time.
func SplitChronologicalHoldout(examples []TrainingExample) (train, holdout []TrainingExample) {
	if len(examples) == 0 {
		return nil, nil
	}
	cut := len(examples) * (1000 - g5protocol.DiscoveryLambdaHoldoutPermille) / 1000
	if cut < 1 {
		cut = 1
	}
	if cut >= len(examples) {
		cut = len(examples) - 1
	}
	return examples[:cut], examples[cut:]
}

// RepresentativeContexts returns frozen multi-regime probe contexts for fixed-action detection.
func RepresentativeContexts(featureKeys []string) []map[string]float64 {
	base := map[string]float64{
		"btc_return": 0, "symbol_return": 0, "btc_vol": 0.2, "btc_trend": 0,
		"btc_drawdown": 0.1, "btc_funding_state": 0, "sky_confidence": 0.5,
		"mask_btc_funding_missing": 0, "symbol_rv": 0.2, "symbol_beta": 1,
		"btc_relative_return": 0, "liquidity_percentile": 0.5, "quote_volume_rank": 0.5,
		"return_rank": 0.5, "vol_rank": 0.5, "liquidity_rank": 0.5, "beta_rank": 0.5,
		"btc_relative_strength_rank": 0.5, "liquidity_score": 0.5, "vol_cost_penalty": 0.1,
		"capacity_headroom": 0.5, "cost_estimate": 0.05,
	}
	bull := cloneFeatureMap(base)
	bear := cloneFeatureMap(base)
	neutral := cloneFeatureMap(base)
	for _, key := range featureKeys {
		switch key {
		case "btc_return", "symbol_return", "return_rank", "btc_relative_return", "btc_relative_strength_rank":
			bull[key] = 2.0
			bear[key] = -2.0
			neutral[key] = 0
		case "btc_trend":
			bull[key] = 1.5
			bear[key] = -1.5
			neutral[key] = 0
		case "btc_drawdown":
			bull[key] = 0.05
			bear[key] = 0.6
			neutral[key] = 0.2
		case "btc_vol", "symbol_rv", "vol_rank":
			bull[key] = 0.15
			bear[key] = 0.8
			neutral[key] = 0.3
		}
	}
	return []map[string]float64{bull, bear, neutral}
}

func cloneFeatureMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// RepresentativePositionStates returns flat/long/short probes for fixed-action detection.
func RepresentativePositionStates() []actionspace.PositionState {
	basis := 100.0
	longNotional := actionspace.DeployNotionalUSDT(actionspace.BaseEquityUSDT, actionspace.StandardPositionSizeFraction)
	return []actionspace.PositionState{
		{Side: actionspace.SideFlat, EquityUSDT: actionspace.BaseEquityUSDT},
		{Side: actionspace.SideLong, SizeFraction: actionspace.StandardPositionSizeFraction, BasisPrice: basis, EquityUSDT: actionspace.BaseEquityUSDT, FixedNotionalUSDT: longNotional},
		{Side: actionspace.SideShort, SizeFraction: actionspace.StandardPositionSizeFraction, BasisPrice: basis, EquityUSDT: actionspace.BaseEquityUSDT, FixedNotionalUSDT: longNotional},
	}
}

// IsFixedDirectionCandidate rejects policies that pick the same direction across representative contexts and start states.
func IsFixedDirectionCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization) bool {
	contexts := RepresentativeContexts(featureKeys)
	states := RepresentativePositionStates()
	if len(contexts) < 2 || len(states) == 0 {
		return false
	}
	horizons := []string{"1h"}
	for _, state := range states {
		picked := make([]string, 0, len(contexts))
		for _, ctx := range contexts {
			dir, ok := argmaxDirectionForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok {
				continue
			}
			picked = append(picked, dir)
		}
		if len(picked) < 2 {
			continue
		}
		first := picked[0]
		allSame := true
		for _, dir := range picked[1:] {
			if dir != first {
				allSame = false
				break
			}
		}
		if !allSame {
			continue
		}
		if state.Side == actionspace.SideFlat && first != actionspace.DirectionFlat {
			return true
		}
		if state.Side != actionspace.SideFlat && first != actionspace.DirectionFlat {
			return true
		}
	}
	return false
}

// IsFixedVerbCandidate rejects policies dominated by one verb across representative contexts for each start state.
func IsFixedVerbCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	contexts := RepresentativeContexts(featureKeys)
	if len(contexts) < 2 {
		return false
	}
	for _, state := range RepresentativePositionStates() {
		picked := make([]string, 0, len(contexts))
		for _, ctx := range contexts {
			verb, ok := argmaxVerbForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok {
				continue
			}
			picked = append(picked, verb)
		}
		if len(picked) < 2 {
			continue
		}
		first := picked[0]
		allSame := true
		for _, verb := range picked[1:] {
			if verb != first {
				allSame = false
				break
			}
		}
		if allSame {
			return true
		}
	}
	return false
}

// IsAlwaysReverseCandidate rejects policies that always reverse from positioned states across contexts.
func IsAlwaysReverseCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	contexts := RepresentativeContexts(featureKeys)
	if len(contexts) < 2 {
		return false
	}
	hasPositioned := false
	for _, state := range RepresentativePositionStates() {
		if state.Side == actionspace.SideFlat {
			continue
		}
		hasPositioned = true
		for _, ctx := range contexts {
			verb, ok := argmaxVerbForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok || verb != actionspace.VerbReverse {
				return false
			}
		}
	}
	return hasPositioned
}

// IsDegeneratePolicyCandidate unions frozen fixed/degenerate action detectors.
func IsDegeneratePolicyCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	return IsFixedDirectionCandidate(weights, featureKeys, actionSpec, norm) ||
		IsFixedHorizonCandidate(weights, featureKeys, actionSpec, norm, horizons) ||
		IsFixedVerbCandidate(weights, featureKeys, actionSpec, norm, horizons) ||
		IsHoldForeverCandidate(weights, featureKeys, actionSpec, norm, horizons) ||
		IsNeverCloseCandidate(weights, featureKeys, actionSpec, norm, horizons) ||
		IsAlwaysReverseCandidate(weights, featureKeys, actionSpec, norm, horizons)
}

// IsHoldForeverCandidate rejects policies that always hold from positioned states across contexts.
func IsHoldForeverCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	contexts := RepresentativeContexts(featureKeys)
	for _, state := range RepresentativePositionStates() {
		if state.Side == actionspace.SideFlat {
			continue
		}
		for _, ctx := range contexts {
			verb, ok := argmaxVerbForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok || verb != actionspace.VerbHold {
				return false
			}
		}
	}
	return true
}

// IsNeverCloseCandidate rejects policies that never close/trim from positioned states across contexts.
func IsNeverCloseCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	contexts := RepresentativeContexts(featureKeys)
	for _, state := range RepresentativePositionStates() {
		if state.Side == actionspace.SideFlat {
			continue
		}
		for _, ctx := range contexts {
			verb, ok := argmaxVerbForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok {
				continue
			}
			if verb == actionspace.VerbClose || verb == actionspace.VerbTrim {
				return false
			}
		}
	}
	return true
}

// IsFixedHorizonCandidate rejects policies dominated by one horizon across representative contexts and start states.
func IsFixedHorizonCandidate(weights []float64, featureKeys []string, actionSpec ActionEncodingSpec, norm Normalization, horizons []string) bool {
	if len(horizons) <= 1 {
		return false
	}
	contexts := RepresentativeContexts(featureKeys)
	states := RepresentativePositionStates()
	if len(contexts) < 2 {
		return false
	}
	for _, state := range states {
		picked := make([]string, 0, len(contexts))
		for _, ctx := range contexts {
			h, ok := argmaxHorizonForState(ctx, state, weights, featureKeys, actionSpec, horizons, norm)
			if !ok {
				continue
			}
			picked = append(picked, h)
		}
		if len(picked) < 2 {
			continue
		}
		first := picked[0]
		allSame := true
		for _, h := range picked[1:] {
			if h != first {
				allSame = false
				break
			}
		}
		if allSame {
			return true
		}
	}
	return false
}

func argmaxDirectionForState(
	ctx map[string]float64,
	state actionspace.PositionState,
	weights []float64,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
) (string, bool) {
	horizon := horizons[0]
	var candidates []actionspace.Action
	switch state.Side {
	case actionspace.SideFlat:
		candidates = []actionspace.Action{
			{Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong, TargetHorizon: horizon, SizeFraction: 0.25, DelayBars: 0},
			{Verb: actionspace.VerbOpenShort, Direction: actionspace.DirectionShort, TargetHorizon: horizon, SizeFraction: 0.25, DelayBars: 0},
			{Verb: actionspace.VerbFlat, Direction: actionspace.DirectionFlat, TargetHorizon: horizon, SizeFraction: 0, DelayBars: 0},
		}
	default:
		for _, verb := range []string{
			actionspace.VerbHold, actionspace.VerbIncrease, actionspace.VerbTrim,
			actionspace.VerbClose, actionspace.VerbReverse,
		} {
			size := 0.25
			if verb == actionspace.VerbHold || verb == actionspace.VerbClose {
				size = 0
			}
			candidates = append(candidates, actionspace.Action{
				Verb: verb, Direction: actionspace.DirectionForVerb(verb, state.Side),
				TargetHorizon: horizon, SizeFraction: size, DelayBars: 0,
			})
		}
	}
	bestScore := math.Inf(-1)
	bestDir := ""
	for _, action := range candidates {
		ex := TrainingExample{
			StateLabel: actionspace.StateLabel(state),
			Action:     action,
			Features:   ctx,
		}
		score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
		if score > bestScore {
			bestScore = score
			bestDir = action.Direction
		}
	}
	return bestDir, bestDir != ""
}

func argmaxHorizonForState(
	ctx map[string]float64,
	state actionspace.PositionState,
	weights []float64,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
) (string, bool) {
	bestScore := math.Inf(-1)
	best := ""
	for _, label := range horizons {
		action := actionspace.Action{
			Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
			TargetHorizon: label, SizeFraction: 0.25, DelayBars: 0,
		}
		if state.Side != actionspace.SideFlat {
			action.Verb = actionspace.VerbHold
			action.Direction = state.Side
			action.SizeFraction = 0
		}
		ex := TrainingExample{StateLabel: actionspace.StateLabel(state), Action: action, Features: ctx}
		score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
		if score > bestScore {
			bestScore = score
			best = label
		}
	}
	return best, best != ""
}

func argmaxVerbForState(
	ctx map[string]float64,
	state actionspace.PositionState,
	weights []float64,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
) (string, bool) {
	var verbs []string
	switch state.Side {
	case actionspace.SideFlat:
		verbs = []string{actionspace.VerbFlat, actionspace.VerbOpenLong, actionspace.VerbOpenShort}
	default:
		verbs = []string{actionspace.VerbHold, actionspace.VerbIncrease, actionspace.VerbTrim, actionspace.VerbClose, actionspace.VerbReverse}
	}
	bestScore := math.Inf(-1)
	best := ""
	for _, verb := range verbs {
		size := 0.25
		if verb == actionspace.VerbHold || verb == actionspace.VerbClose || verb == actionspace.VerbFlat {
			size = 0
		}
		action := actionspace.Action{
			Verb: verb, Direction: actionspace.DirectionForVerb(verb, state.Side),
			TargetHorizon: horizons[0], SizeFraction: size, DelayBars: 0,
		}
		ex := TrainingExample{StateLabel: actionspace.StateLabel(state), Action: action, Features: ctx}
		score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
		if score > bestScore {
			bestScore = score
			best = verb
		}
	}
	return best, best != ""
}

func argmaxDirection(
	ctx map[string]float64,
	weights []float64,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
) (string, bool) {
	candidates := []actionspace.Action{
		{Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong, TargetHorizon: horizons[0], SizeFraction: 0.25, DelayBars: 0},
		{Verb: actionspace.VerbOpenShort, Direction: actionspace.DirectionShort, TargetHorizon: horizons[0], SizeFraction: 0.25, DelayBars: 0},
		{Verb: actionspace.VerbFlat, Direction: actionspace.DirectionFlat, TargetHorizon: horizons[0], SizeFraction: 0, DelayBars: 0},
	}
	bestScore := math.Inf(-1)
	bestDir := ""
	for _, action := range candidates {
		ex := TrainingExample{
			StateLabel: actionspace.SideFlat,
			Action:     action,
			Features:   ctx,
		}
		score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
		if score > bestScore {
			bestScore = score
			bestDir = action.Direction
		}
	}
	return bestDir, bestDir != ""
}

func argmaxHorizon(
	ctx map[string]float64,
	weights []float64,
	featureKeys []string,
	actionSpec ActionEncodingSpec,
	horizons []string,
	norm Normalization,
) (string, bool) {
	bestScore := math.Inf(-1)
	best := ""
	for _, label := range horizons {
		action := actionspace.Action{
			Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
			TargetHorizon: label, SizeFraction: 0.25, DelayBars: 0,
		}
		ex := TrainingExample{StateLabel: actionspace.SideFlat, Action: action, Features: ctx}
		score := PredictScore(ex, weights, featureKeys, actionSpec, horizons, norm)
		if score > bestScore {
			bestScore = score
			best = label
		}
	}
	return best, best != ""
}

func horizonLabel(hours int) string {
	switch hours {
	case 1:
		return "1h"
	case 4:
		return "4h"
	case 24:
		return "24h"
	default:
		return "1h"
	}
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
