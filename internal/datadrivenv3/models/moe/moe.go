// Package moe is the Claude Opus 4.8 owner submission for the data-driven
// router V3 tournament: an explainable, sparse Mixture-of-Experts.
//
// A shallow, axis-aligned gate performs hard top-1 routing over the fixed
// decision-time market features and emits a replayable predicate per region.
// Each region is served by one L1-regularised (Lasso) linear expert that snaps
// its continuous score onto the frozen signed-position grid and carries a
// representative horizon and delay.
//
// By construction this package:
//   - reads only sealed synthetic/discovery examples through modelapi; it never
//     touches real reward labels, the dataset on disk, peer model outputs, or
//     any file/network IO;
//   - routes on the 25 frozen market features only and is missing-aware (a set
//     MissingBit follows a fixed branch rather than an imputed zero); it never
//     uses symbol or calendar-date identity as a feature;
//   - guards against the tournament degeneration modes: it rejects any fitted
//     mixture that collapses onto a single expert or that emits a single fixed
//     trade direction across the training states.
//
// At production scale the fit cost is bounded rather than quadratic: the search
// fits on a deterministic, input-order-independent seed-42 identity-hash subsample
// of at most MaxFitRows rows, and each feature contributes at most
// MaxThresholdsPerFeature quantile split thresholds per split search. The frozen
// expert/gate-depth/L1 grid and the attempt/leaf budgets are unchanged, and the
// fitted Policy can still Predict any full decision-time state.
package moe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	"lingxi/internal/datadrivenv3/modelapi"
)

const (
	// OwnerID is the tournament owner label for this submission.
	OwnerID = "opus"
	// SchemaVersion identifies this model artifact's on-wire shape.
	SchemaVersion = "lingxi.data-driven-router-v3.moe-opus.v1"
	// Seed is the single frozen source of deterministic tie-breaking.
	Seed = 42
	// MaxAttempts caps the number of fitted mixtures per Fit call.
	MaxAttempts = 16
	// MaxLeaves caps the cumulative number of expert regions grown per Fit.
	MaxLeaves = 32
	// MaxFitRows caps how many training rows the search fits on. When the input
	// exceeds this cap, rows are selected by a deterministic, input-order-
	// independent seed-42 identity hash that excludes features, account data,
	// and targets, keeping fit cost bounded regardless of dataset size. The
	// fitted Policy can still Predict any full decision-time state.
	MaxFitRows = 8192
	// MaxThresholdsPerFeature caps how many quantile split thresholds each
	// feature contributes per split search. This bounds the split search at
	// O(rows) per feature instead of the O(rows^2) of scanning every unique
	// threshold, and is a deterministic function of the sorted values so input
	// order does not affect the chosen split.
	MaxThresholdsPerFeature = 31

	lassoIterations = 64
	minLeafExamples = 1
	gainEpsilon     = 1e-9
	stdEpsilon      = 1e-12
)

// Frozen search grid. experts x gate_depth x l1 == 3*2*2 == 12 combinations,
// well inside the 16-attempt budget; infeasible combinations (more experts than
// a tree of the given depth can address) are skipped before spending an attempt.
var (
	ExpertGrid    = []int{2, 3, 4}
	GateDepthGrid = []int{1, 2}
	L1Grid        = []float64{0.001, 0.01}

	fractionGrid = []float64{-1, -0.5, -0.25, -0.1, 0, 0.1, 0.25, 0.5, 1}
	horizonGrid  = []int{1, 4, 24}
	delayGrid    = []int{0, 1, 2, 3}
)

// Choice records which grid point won selection and the search budget it used.
type Choice struct {
	Experts       int     `json:"experts"`
	GateDepth     int     `json:"gate_depth"`
	L1            float64 `json:"l1"`
	Seed          int     `json:"seed"`
	PenalizedLoss float64 `json:"penalized_loss"`
	Attempts      int     `json:"attempts"`
	LeavesUsed    int     `json:"leaves_used"`
}

// LeafInfo is the inspectable description of one routed expert region.
type LeafInfo struct {
	LeafID         string                `json:"leaf_id"`
	ExpertIdx      int                   `json:"expert_idx"`
	Predicate      string                `json:"predicate"`
	ExampleCount   int                   `json:"example_count"`
	NonzeroWeights int                   `json:"nonzero_weights"`
	Representative modelapi.TargetIntent `json:"representative_intent"`
}

// expert is one L1-regularised linear region head. It standardises each present
// feature before applying a weight so the L1 penalty is scale-comparable.
type expert struct {
	id       int
	base     float64
	means    [modelapi.FeatureCount]float64
	stds     [modelapi.FeatureCount]float64
	weights  [modelapi.FeatureCount]float64
	active   [modelapi.FeatureCount]bool
	horizon  int
	delay    int
	examples int
}

func (e expert) predictFraction(m modelapi.MarketState) float64 {
	score := e.base
	for j := 0; j < modelapi.FeatureCount; j++ {
		if !e.active[j] || !present(m, j) {
			continue
		}
		score += e.weights[j] * (m.Features[j] - e.means[j]) / e.stds[j]
	}
	return snapFraction(score)
}

func (e expert) nonzeroWeights() int {
	n := 0
	for j := 0; j < modelapi.FeatureCount; j++ {
		if e.active[j] && e.weights[j] != 0 {
			n++
		}
	}
	return n
}

func (e expert) intent(m modelapi.MarketState) modelapi.TargetIntent {
	return modelapi.TargetIntent{
		TargetSignedPositionFraction: e.predictFraction(m),
		TargetHorizonHours:           e.horizon,
		DelayBars:                    e.delay,
	}
}

// gateNode is one node of the hard top-1 routing tree.
type gateNode struct {
	leaf        bool
	expertIdx   int
	feature     int
	threshold   float64
	missingLeft bool
	left        *gateNode
	right       *gateNode
}

func (n *gateNode) route(m modelapi.MarketState) int {
	for !n.leaf {
		goLeft := n.missingLeft
		if present(m, n.feature) {
			goLeft = m.Features[n.feature] < n.threshold
		}
		if goLeft {
			n = n.left
		} else {
			n = n.right
		}
	}
	return n.expertIdx
}

// Model implements modelapi.Model. A single Model owns an append-only attempt
// ledger describing the search that produced its fitted mixture.
type Model struct {
	ledger  modelapi.AttemptLedger
	fitted  bool
	root    *gateNode
	experts []expert
	leaves  []LeafInfo
	choice  Choice
	policy  modelapi.Policy
}

// New returns an unfitted model bound to the opus owner id.
func New() *Model { return &Model{} }

// Fit runs the frozen grid search over sealed synthetic/discovery examples.
func (m *Model) Fit(ctx context.Context, examples []modelapi.TrainingExample) (modelapi.Policy, error) {
	return m.fitWithBudget(ctx, examples, MaxAttempts, MaxLeaves)
}

func (m *Model) fitWithBudget(ctx context.Context, examples []modelapi.TrainingExample, maxAttempts, maxLeaves int) (modelapi.Policy, error) {
	if err := modelapi.ValidateTrainingExamples(examples); err != nil {
		return modelapi.Policy{}, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return modelapi.Policy{}, err
		}
	}
	// Fit on a bounded, input-order-independent subsample so the search cost is
	// bounded even for production-scale inputs. The full input is still fully
	// validated above, and Predict remains defined for every state.
	fitExamples := selectFitExamples(examples, MaxFitRows)
	feats := make([][modelapi.FeatureCount]float64, len(fitExamples))
	pres := make([][modelapi.FeatureCount]bool, len(fitExamples))
	y := make([]float64, len(fitExamples))
	for i, ex := range fitExamples {
		feats[i] = ex.Market.Features
		for j := 0; j < modelapi.FeatureCount; j++ {
			pres[i][j] = present(ex.Market, j)
		}
		y[i] = ex.SyntheticTarget.TargetSignedPositionFraction
	}

	examplesHash := hashExamples(fitExamples)
	if _, err := m.ledger.Append("fit:begin:"+OwnerID, OwnerID, "FIT_BEGIN", examplesHash); err != nil {
		return modelapi.Policy{}, err
	}

	rng := rand.New(rand.NewSource(Seed))
	var best *candidate
	attempts, leavesUsed := 0, 0

	for _, depth := range GateDepthGrid {
		for _, numExperts := range ExpertGrid {
			for _, l1 := range L1Grid {
				if numExperts > 1<<uint(depth) {
					continue // a depth-d tree cannot address this many regions.
				}
				if attempts >= maxAttempts {
					break
				}
				if leavesUsed+numExperts > maxLeaves {
					continue // would exceed the frozen leaf budget.
				}
				cand := buildCandidate(y, feats, pres, fitExamples, numExperts, depth, l1, rng)
				attempts++
				leavesUsed += len(cand.leaves)
				if _, err := m.ledger.Append(
					fmt.Sprintf("fit:attempt:%d", attempts), OwnerID, "FIT_ATTEMPT",
					comboHash(examplesHash, numExperts, depth, l1),
				); err != nil {
					return modelapi.Policy{}, err
				}
				if cand.rejection != "" {
					continue
				}
				c := cand
				if best == nil || lessCandidate(&c, best) {
					best = &c
				}
			}
		}
		if attempts >= maxAttempts {
			break
		}
	}

	if best == nil {
		return modelapi.Policy{}, errors.New("moe: no non-degenerate mixture found under budget")
	}

	m.root = best.root
	m.experts = best.experts
	m.choice = Choice{
		Experts: best.numExperts, GateDepth: best.depth, L1: best.l1, Seed: Seed,
		PenalizedLoss: best.penalizedLoss, Attempts: attempts, LeavesUsed: leavesUsed,
	}
	m.leaves = best.leaves
	m.policy = best.policy(examplesHash)
	m.fitted = true

	if _, err := m.ledger.Append("fit:select:"+OwnerID, OwnerID, "FIT_SELECT", comboHash(examplesHash, best.numExperts, best.depth, best.l1)); err != nil {
		return modelapi.Policy{}, err
	}
	return m.policy, nil
}

// Predict routes a single decision-time state to its expert and returns the
// grid-valid target intent. It never reads labels, peers, or files.
func (m *Model) Predict(ctx context.Context, market modelapi.MarketState, _ modelapi.AccountContext) (modelapi.TargetIntent, error) {
	if !m.fitted {
		return modelapi.TargetIntent{}, errors.New("moe: model is not fitted")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return modelapi.TargetIntent{}, err
		}
	}
	if err := market.Validate(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	idx := m.root.route(market)
	if idx < 0 || idx >= len(m.experts) {
		return modelapi.TargetIntent{}, errors.New("moe: routing produced an unknown expert")
	}
	intent := m.experts[idx].intent(market)
	if err := intent.Validate(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	return intent, nil
}

// RouteExpert exposes the deterministic top-1 routing decision for inspection.
func (m *Model) RouteExpert(market modelapi.MarketState) (int, error) {
	if !m.fitted {
		return -1, errors.New("moe: model is not fitted")
	}
	return m.root.route(market), nil
}

// Choice returns the selected grid point and budget usage.
func (m *Model) Choice() Choice { return m.choice }

// Leaves returns the inspectable expert regions of the fitted mixture.
func (m *Model) Leaves() []LeafInfo { return append([]LeafInfo(nil), m.leaves...) }

// Policy returns the replayable state-to-intent policy.
func (m *Model) Policy() modelapi.Policy { return m.policy }

// HeadHash returns the head of the append-only attempt ledger.
func (m *Model) HeadHash() string { return m.ledger.HeadHash() }

// Ledger returns a defensive copy of the append-only attempt ledger.
func (m *Model) Ledger() []modelapi.AttemptEntry { return m.ledger.Entries() }

// candidate is one fully fitted mixture considered during the search.
type candidate struct {
	numExperts    int
	depth         int
	l1            float64
	root          *gateNode
	experts       []expert
	leaves        []LeafInfo
	penalizedLoss float64
	rejection     string
}

func (c candidate) policy(examplesHash string) modelapi.Policy {
	leaves := make([]modelapi.CandidateLeaf, 0, len(c.leaves))
	for _, li := range c.leaves {
		leaves = append(leaves, modelapi.CandidateLeaf{
			LeafID:    li.LeafID,
			Predicate: li.Predicate,
			Intent:    li.Representative,
		})
	}
	return modelapi.Policy{
		SchemaVersion: modelapi.SchemaVersion,
		PolicyID:      hashStrings(SchemaVersion, examplesHash, fmt.Sprintf("e%d d%d l%g", c.numExperts, c.depth, c.l1)),
		Leaves:        leaves,
		DefaultIntent: modelapi.TargetIntent{TargetHorizonHours: 1},
	}
}

func lessCandidate(a *candidate, b *candidate) bool {
	if math.Abs(a.penalizedLoss-b.penalizedLoss) > 1e-12 {
		return a.penalizedLoss < b.penalizedLoss
	}
	if len(a.leaves) != len(b.leaves) {
		return len(a.leaves) < len(b.leaves)
	}
	return false
}

// growLeaf is a mutable region during best-first tree growth.
type growLeaf struct {
	node  *gateNode
	idx   []int
	depth int
	frags []string
}

func buildCandidate(y []float64, feats [][modelapi.FeatureCount]float64, pres [][modelapi.FeatureCount]bool, examples []modelapi.TrainingExample, numExperts, depth int, l1 float64, rng *rand.Rand) candidate {
	all := make([]int, len(y))
	for i := range all {
		all[i] = i
	}
	root := &gateNode{leaf: true}
	leaves := []*growLeaf{{node: root, idx: all, depth: 0}}

	for len(leaves) < numExperts {
		bestLeaf, bestSplitIdx := -1, split{}
		bestGain := gainEpsilon
		var ties []int
		for li, gl := range leaves {
			if gl.depth >= depth || len(gl.idx) < 2*minLeafExamples {
				continue
			}
			sp, ok := bestSplit(y, feats, pres, gl.idx, rng)
			if !ok {
				continue
			}
			if sp.gain > bestGain+gainEpsilon {
				bestGain, bestLeaf, bestSplitIdx = sp.gain, li, sp
				ties = ties[:0]
			} else if math.Abs(sp.gain-bestGain) <= gainEpsilon {
				ties = append(ties, li)
			}
		}
		if bestLeaf < 0 {
			break
		}
		if len(ties) > 0 {
			cands := append([]int{bestLeaf}, ties...)
			sort.Ints(cands)
			bestLeaf = cands[rng.Intn(len(cands))]
			sp, _ := bestSplit(y, feats, pres, leaves[bestLeaf].idx, rng)
			bestSplitIdx = sp
		}
		leaves = applySplit(leaves, bestLeaf, bestSplitIdx)
	}

	cand := candidate{numExperts: numExperts, depth: depth, l1: l1, root: root}
	preds := make([]float64, len(y))
	for _, gl := range leaves {
		e := buildExpert(len(cand.experts), y, feats, pres, examples, gl.idx, l1)
		gl.node.leaf = true
		gl.node.expertIdx = e.id
		cand.experts = append(cand.experts, e)
		predicate := "ALL_STATES"
		if len(gl.frags) > 0 {
			predicate = strings.Join(gl.frags, " AND ")
		}
		for _, i := range gl.idx {
			preds[i] = e.predictFraction(marketOf(feats[i], pres[i]))
		}
		cand.leaves = append(cand.leaves, LeafInfo{
			LeafID:         fmt.Sprintf("opus-e%d-d%d-l%g-x%d", numExperts, depth, l1, e.id),
			ExpertIdx:      e.id,
			Predicate:      predicate,
			ExampleCount:   e.examples,
			NonzeroWeights: e.nonzeroWeights(),
			Representative: modelapi.TargetIntent{
				TargetSignedPositionFraction: snapFraction(e.base),
				TargetHorizonHours:           e.horizon,
				DelayBars:                    e.delay,
			},
		})
	}

	cand.rejection = degeneracyReason(len(leaves), numExperts, preds)
	if cand.rejection == "" {
		loss := 0.0
		for i := range y {
			d := preds[i] - y[i]
			loss += d * d
		}
		for _, e := range cand.experts {
			for j := 0; j < modelapi.FeatureCount; j++ {
				loss += l1 * math.Abs(e.weights[j])
			}
		}
		cand.penalizedLoss = loss
	}
	return cand
}

// degeneracyReason enforces the tournament anti-degeneration rules: no expert
// collapse and no single fixed trade direction across the training states.
func degeneracyReason(leavesUsed, numExperts int, preds []float64) string {
	if leavesUsed < 2 {
		return "EXPERT_COLLAPSE_SINGLE_REGION"
	}
	distinct := map[float64]struct{}{}
	pos, neg, zero := false, false, false
	for _, p := range preds {
		distinct[p] = struct{}{}
		switch {
		case p > 0:
			pos = true
		case p < 0:
			neg = true
		default:
			zero = true
		}
	}
	if len(distinct) < 2 {
		return "FIXED_ACTION_NO_STATE_DEPENDENCE"
	}
	if (pos && !neg && !zero) || (neg && !pos && !zero) {
		return "FIXED_DIRECTION_ALWAYS_DIRECTIONAL"
	}
	return ""
}

// split describes one candidate axis-aligned partition.
type split struct {
	feature     int
	threshold   float64
	missingLeft bool
	gain        float64
	left        []int
	right       []int
}

func bestSplit(y []float64, feats [][modelapi.FeatureCount]float64, pres [][modelapi.FeatureCount]bool, idx []int, rng *rand.Rand) (split, bool) {
	total := sse(y, idx)
	best := split{gain: 0}
	found := false
	for f := 0; f < modelapi.FeatureCount; f++ {
		var presentIdx, missingIdx []int
		for _, i := range idx {
			if pres[i][f] {
				presentIdx = append(presentIdx, i)
			} else {
				missingIdx = append(missingIdx, i)
			}
		}
		if len(presentIdx) < minLeafExamples {
			continue
		}
		vals := make([]float64, len(presentIdx))
		for k, i := range presentIdx {
			vals[k] = feats[i][f]
		}
		sort.Float64s(vals)
		for _, threshold := range quantileThresholds(vals, MaxThresholdsPerFeature) {
			var lp, rp []int
			for _, i := range presentIdx {
				if feats[i][f] < threshold {
					lp = append(lp, i)
				} else {
					rp = append(rp, i)
				}
			}
			for _, missingLeft := range []bool{true, false} {
				left, right := lp, rp
				if missingLeft {
					left = append(append([]int(nil), lp...), missingIdx...)
				} else {
					right = append(append([]int(nil), rp...), missingIdx...)
				}
				if len(left) < minLeafExamples || len(right) < minLeafExamples {
					continue
				}
				gain := total - sse(y, left) - sse(y, right)
				if gain > best.gain+gainEpsilon || (!found && gain > gainEpsilon) {
					best = split{feature: f, threshold: threshold, missingLeft: missingLeft, gain: gain, left: left, right: right}
					found = true
				}
				if len(missingIdx) == 0 {
					break // missing side is irrelevant with no missing rows.
				}
			}
		}
	}
	_ = rng
	return best, found
}

// quantileThresholds returns at most maxT midpoint split candidates for a slice
// of already-sorted present feature values. Every midpoint between adjacent
// distinct values is a candidate; when there are more than maxT of them the
// candidates are subsampled at evenly spaced quantile positions. The result is a
// deterministic function of the sorted values only, so it is independent of the
// input row order. maxT <= 0 means "use every midpoint".
func quantileThresholds(vals []float64, maxT int) []float64 {
	cuts := make([]float64, 0, len(vals))
	for k := 1; k < len(vals); k++ {
		if vals[k]-vals[k-1] <= stdEpsilon {
			continue
		}
		cuts = append(cuts, (vals[k]+vals[k-1])/2)
	}
	if maxT <= 0 || len(cuts) <= maxT {
		return cuts
	}
	if maxT == 1 {
		return []float64{cuts[(len(cuts)-1)/2]}
	}
	out := make([]float64, 0, maxT)
	last := -1
	for t := 0; t < maxT; t++ {
		pos := int(math.Round(float64(t) * float64(len(cuts)-1) / float64(maxT-1)))
		if pos == last {
			continue
		}
		out = append(out, cuts[pos])
		last = pos
	}
	return out
}

func applySplit(leaves []*growLeaf, at int, sp split) []*growLeaf {
	parent := leaves[at]
	parent.node.leaf = false
	parent.node.feature = sp.feature
	parent.node.threshold = sp.threshold
	parent.node.missingLeft = sp.missingLeft
	parent.node.left = &gateNode{leaf: true}
	parent.node.right = &gateNode{leaf: true}

	name := modelapi.FeatureOrder[sp.feature]
	missingNote := "missing->right"
	if sp.missingLeft {
		missingNote = "missing->left"
	}
	leftFrag := fmt.Sprintf("%s<%.6g[%s]", name, sp.threshold, missingNote)
	rightFrag := fmt.Sprintf("%s>=%.6g[%s]", name, sp.threshold, missingNote)

	left := &growLeaf{node: parent.node.left, idx: sp.left, depth: parent.depth + 1, frags: append(append([]string(nil), parent.frags...), leftFrag)}
	right := &growLeaf{node: parent.node.right, idx: sp.right, depth: parent.depth + 1, frags: append(append([]string(nil), parent.frags...), rightFrag)}

	out := make([]*growLeaf, 0, len(leaves)+1)
	out = append(out, leaves[:at]...)
	out = append(out, left, right)
	out = append(out, leaves[at+1:]...)
	return out
}

// buildExpert fits one region head via deterministic coordinate-descent Lasso
// on standardised present features, then attaches modal horizon and delay.
func buildExpert(id int, y []float64, feats [][modelapi.FeatureCount]float64, pres [][modelapi.FeatureCount]bool, examples []modelapi.TrainingExample, idx []int, l1 float64) expert {
	e := expert{id: id, examples: len(idx)}
	if len(idx) == 0 {
		e.horizon, e.delay = 1, 0
		return e
	}
	base := 0.0
	for _, i := range idx {
		base += y[i]
	}
	base /= float64(len(idx))
	e.base = base

	for j := 0; j < modelapi.FeatureCount; j++ {
		var vals []float64
		for _, i := range idx {
			if pres[i][j] {
				vals = append(vals, feats[i][j])
			}
		}
		if len(vals) < 2 {
			continue
		}
		mean := 0.0
		for _, v := range vals {
			mean += v
		}
		mean /= float64(len(vals))
		variance := 0.0
		for _, v := range vals {
			d := v - mean
			variance += d * d
		}
		std := math.Sqrt(variance / float64(len(vals)))
		if std <= stdEpsilon {
			continue
		}
		e.means[j], e.stds[j], e.active[j] = mean, std, true
	}

	pred := make([]float64, len(idx))
	for k := range idx {
		pred[k] = base
	}
	lambda := l1 * float64(len(idx))
	for iter := 0; iter < lassoIterations; iter++ {
		for j := 0; j < modelapi.FeatureCount; j++ {
			if !e.active[j] {
				continue
			}
			rho, denom := 0.0, 0.0
			for k, i := range idx {
				if !pres[i][j] {
					continue
				}
				z := (feats[i][j] - e.means[j]) / e.stds[j]
				rho += z * (y[i] - pred[k] + e.weights[j]*z)
				denom += z * z
			}
			if denom <= stdEpsilon {
				continue
			}
			newW := softThreshold(rho, lambda) / denom
			if newW == e.weights[j] {
				continue
			}
			for k, i := range idx {
				if !pres[i][j] {
					continue
				}
				z := (feats[i][j] - e.means[j]) / e.stds[j]
				pred[k] += (newW - e.weights[j]) * z
			}
			e.weights[j] = newW
		}
	}

	e.horizon = modeHorizon(examples, idx)
	e.delay = modeDelay(examples, idx)
	return e
}

func modeHorizon(examples []modelapi.TrainingExample, idx []int) int {
	counts := make([]int, len(horizonGrid))
	for _, i := range idx {
		for g, h := range horizonGrid {
			if examples[i].SyntheticTarget.TargetHorizonHours == h {
				counts[g]++
			}
		}
	}
	return horizonGrid[argmaxFirst(counts)]
}

func modeDelay(examples []modelapi.TrainingExample, idx []int) int {
	counts := make([]int, len(delayGrid))
	for _, i := range idx {
		d := examples[i].SyntheticTarget.DelayBars
		if d >= 0 && d < len(delayGrid) {
			counts[d]++
		}
	}
	return delayGrid[argmaxFirst(counts)]
}

func argmaxFirst(counts []int) int {
	best, at := counts[0], 0
	for g := 1; g < len(counts); g++ {
		if counts[g] > best {
			best, at = counts[g], g
		}
	}
	return at
}

func sse(y []float64, idx []int) float64 {
	if len(idx) == 0 {
		return 0
	}
	mean := 0.0
	for _, i := range idx {
		mean += y[i]
	}
	mean /= float64(len(idx))
	total := 0.0
	for _, i := range idx {
		d := y[i] - mean
		total += d * d
	}
	return total
}

func softThreshold(a, k float64) float64 {
	if a > k {
		return a - k
	}
	if a < -k {
		return a + k
	}
	return 0
}

func snapFraction(score float64) float64 {
	best, bestDist := fractionGrid[0], math.Abs(score-fractionGrid[0])
	for _, f := range fractionGrid[1:] {
		if d := math.Abs(score - f); d < bestDist-1e-15 {
			best, bestDist = f, d
		}
	}
	return best
}

func present(m modelapi.MarketState, j int) bool {
	return m.MissingBits&(uint64(1)<<uint(j)) == 0
}

func marketOf(features [modelapi.FeatureCount]float64, presence [modelapi.FeatureCount]bool) modelapi.MarketState {
	m := modelapi.MarketState{Features: features}
	for j := 0; j < modelapi.FeatureCount; j++ {
		if !presence[j] {
			m.MissingBits |= uint64(1) << uint(j)
		}
	}
	return m
}

// selectFitExamples returns a deterministic, input-order-independent subset of
// at most maxRows examples, emitted in a canonical order. Each row is keyed only
// by seed 42 and immutable identity (ExampleID, EventID, DecisionMS); features,
// account fields, and SyntheticTarget are deliberately excluded so discovery
// labels cannot affect inclusion. The smallest identity hashes are kept.
// Hash collisions use the same identity tuple as a deterministic total-order
// tie-break, with no positional tie-breaking. maxRows <= 0 means "use every row".
func selectFitExamples(examples []modelapi.TrainingExample, maxRows int) []modelapi.TrainingExample {
	type keyed struct {
		key string
		ex  modelapi.TrainingExample
	}
	ranked := make([]keyed, len(examples))
	for i, ex := range examples {
		ranked[i] = keyed{key: rowIdentityHash(ex), ex: ex}
	}
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].key != ranked[b].key {
			return ranked[a].key < ranked[b].key
		}
		ae, be := ranked[a].ex, ranked[b].ex
		if ae.ExampleID != be.ExampleID {
			return ae.ExampleID < be.ExampleID
		}
		if ae.Market.EventID != be.Market.EventID {
			return ae.Market.EventID < be.Market.EventID
		}
		return ae.Market.DecisionMS < be.Market.DecisionMS
	})
	n := len(ranked)
	if maxRows > 0 && n > maxRows {
		n = maxRows
	}
	out := make([]modelapi.TrainingExample, n)
	for i := 0; i < n; i++ {
		out[i] = ranked[i].ex
	}
	return out
}

// rowIdentityHash is independent of row position and all model inputs/targets.
// It may use only the frozen seed and immutable row identity.
func rowIdentityHash(ex modelapi.TrainingExample) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%d", Seed, ex.ExampleID, ex.Market.EventID, ex.Market.DecisionMS)
	return hex.EncodeToString(h.Sum(nil))
}

func hashExamples(examples []modelapi.TrainingExample) string {
	h := sha256.New()
	_, _ = h.Write([]byte(SchemaVersion))
	for _, ex := range examples {
		fmt.Fprintf(h, "\x00%s\x00%d", ex.Market.EventID, ex.Market.MissingBits)
		for _, f := range ex.Market.Features {
			fmt.Fprintf(h, "\x00%x", math.Float64bits(f))
		}
		t := ex.SyntheticTarget
		fmt.Fprintf(h, "\x00%x\x00%d\x00%d", math.Float64bits(t.TargetSignedPositionFraction), t.TargetHorizonHours, t.DelayBars)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func comboHash(examplesHash string, numExperts, depth int, l1 float64) string {
	return hashStrings(examplesHash, fmt.Sprintf("experts=%d", numExperts), fmt.Sprintf("depth=%d", depth), fmt.Sprintf("l1=%g", l1))
}

func hashStrings(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

var _ modelapi.Model = (*Model)(nil)
