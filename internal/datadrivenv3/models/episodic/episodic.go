// Package episodic implements the "sonnet" owner's deterministic, bounded
// episodic nearest-neighbor model for the data-driven-router-v3 model API.
// Sonnet is standing in for the "fable" owner seat, which could not launch
// behind an external policy gate; this package is otherwise an independent
// implementation. It trains only on sealed synthetic/discovery examples from
// modelapi and has no dataset, reward-provider, peer-output, or filesystem
// dependency. Event IDs and decision times are validated by modelapi and are
// used only for self/embargo bookkeeping — never as nearest-neighbor
// features and never as a direct driver of the predicted intent.
//
// Production-scale bounding: both the candidate pool consulted at Predict
// time and the pseudo-query set consulted during Fit's embargoed
// leave-one-out grid search are capped at data-independent constants
// (MaxCandidatePoolSize, MaxLOOQueries). This turns Predict's previous
// O(n) and Fit's previous near-O(n^2) cost, over an arbitrarily large
// training set, into fixed-size bounds. Pool/query membership is chosen by
// deterministic, seed-bound hash priority — never by input slice order or
// feature/target values — so it is invariant under any permutation of the
// supplied examples. The k grid, feature-set grid, attempt budget, and 24h
// self-symbol embargo are unchanged by this bounding.
package episodic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"lingxi/internal/datadrivenv3/modelapi"
)

const (
	OwnerID       = "sonnet"
	SchemaVersion = "lingxi.data-driven-router-v3.model.episodic.v1"
	Seed          = 42
	MaxAttempts   = 16
	MaxLeaves     = 32

	// MaxCandidatePoolSize is the data-independent upper bound on how many
	// training rows are ever retained as nearest-neighbor candidates, at
	// both Fit (embargoed LOO scoring, policy summarization) and Predict
	// time. It caps Predict's per-call cost at
	// O(MaxCandidatePoolSize) regardless of how many training examples are
	// supplied, instead of the previous O(n). Pool membership is chosen by
	// deterministic, seed-bound hash priority (see selectPool) and never by
	// input slice order, so it is stable under any permutation of the
	// supplied examples and independent of feature/target values.
	MaxCandidatePoolSize = 8192

	// MaxLOOQueries is the data-independent upper bound on how many pooled
	// rows are used as pseudo-queries during the embargoed
	// leave-one-out grid search at Fit time. Combined with
	// MaxCandidatePoolSize this bounds one grid attempt's LOO cost at
	// O(MaxLOOQueries * MaxCandidatePoolSize) — a fixed constant — instead
	// of the previous O(n^2) over the full training set. Query rows are
	// chosen by the same deterministic hash-priority selection, drawn from
	// the already-frozen candidate pool.
	MaxLOOQueries = 1024

	// EmbargoWindowMS is the frozen self-symbol embargo: any candidate whose
	// decision time falls within this window of the query (or which shares
	// the query's exact event identity) is excluded from the neighbor set.
	EmbargoWindowMS = int64(24 * 60 * 60 * 1000)

	// missingPenalty is the fixed, data-free squared contribution a feature
	// makes to the standardized distance whenever it is missing on either
	// side of the comparison. It is not fitted from data so it cannot leak
	// training rows.
	missingPenalty = 1.0

	// neighborWeightEpsilon keeps inverse-distance neighbor weighting
	// well-defined at distance zero while still letting an exact (or
	// near-exact) match dominate a merely nearby one, rather than the two
	// being almost indistinguishable in vote weight.
	neighborWeightEpsilon = 1e-6

	requiredDims = modelapi.FeatureCount // 25 frozen market features
	accountDims  = 4                     // decision-time account features
	totalDims    = requiredDims + accountDims
)

// FeatureSet is one of the two frozen g1 feature-set grid choices.
type FeatureSet string

const (
	// FeatureSetRequiredOnly excludes the dataset's own documented optional
	// feed features (rubik_taker_ratio, rubik_ls_ratio) — see
	// datadrivenv3.Coverage.OptionalMissing — while always keeping the
	// decision-time account (local state) features.
	FeatureSetRequiredOnly FeatureSet = "required_only"
	// FeatureSetRequiredPlusOptional keeps every frozen market feature plus
	// account features.
	FeatureSetRequiredPlusOptional FeatureSet = "required_plus_optional"
)

// FeatureSets is the frozen, ordered list of feature-set grid choices.
var FeatureSets = []FeatureSet{FeatureSetRequiredOnly, FeatureSetRequiredPlusOptional}

// KGrid is the frozen neighbor-count grid.
var KGrid = []int{16, 32, 64}

// Frozen intent grid, mirrored from modelapi's sealed validation grid.
var (
	AllowedFractions = []float64{-1, -.5, -.25, -.1, 0, .1, .25, .5, 1}
	AllowedHorizons  = []int{1, 4, 24}
	AllowedDelays    = []int{0, 1, 2, 3}
)

// optionalFeatureNames names the dataset's own documented optional feed
// features. They are looked up by name against modelapi.FeatureOrder rather
// than hard-coded indices so a reordering of the frozen feature list cannot
// silently change which features "required_only" excludes.
var optionalFeatureNames = map[string]bool{
	"rubik_taker_ratio": true,
	"rubik_ls_ratio":    true,
}

var (
	ErrNotFitted       = errors.New("episodic model has not been fitted")
	ErrBudgetExhausted = errors.New("episodic attempt budget exhausted")
	ErrNoGridCandidate = errors.New("no episodic grid candidate produced a finite loss")
)

// gridPoint is one (k, feature_set) attempt on the frozen g1 grid.
type gridPoint struct {
	k          int
	featureSet FeatureSet
}

func enumerateGrid() []gridPoint {
	out := make([]gridPoint, 0, len(KGrid)*len(FeatureSets))
	for _, k := range KGrid {
		for _, fs := range FeatureSets {
			out = append(out, gridPoint{k: k, featureSet: fs})
		}
	}
	return out
}

func attemptID(g gridPoint) string {
	return fmt.Sprintf("sonnet-episodic-k%02d-%s", g.k, g.featureSet)
}

// row is the internal, unexported per-example cache. It is never returned
// to callers: Policy() and PredictWithEvidence() only ever expose bounded,
// aggregated summaries, never individual rows.
type row struct {
	eventID    string
	decisionMS int64
	values     [totalDims]float64
	missing    [totalDims]bool
	target     modelapi.TargetIntent
	priority   string // deterministic, seed-bound hash used only for tie-breaks
}

// EvidenceSummary is a bounded, aggregated description of the neighbor
// evidence behind one prediction. It never contains an ExampleID, EventID,
// per-neighbor distance, or per-neighbor feature/target value — only
// dataset-wide aggregates over the chosen neighbor set.
type EvidenceSummary struct {
	SchemaVersion           string  `json:"schema_version"`
	K                       int     `json:"k"`
	FeatureSet              string  `json:"feature_set"`
	ConsideredCount         int     `json:"considered_count"`
	EmbargoedCount          int     `json:"embargoed_count"`
	NeighborCount           int     `json:"neighbor_count"`
	MeanDistance            float64 `json:"mean_distance"`
	MinDistance             float64 `json:"min_distance"`
	MaxDistance             float64 `json:"max_distance"`
	DominantVoteWeightShare float64 `json:"dominant_vote_weight_share"`
	Reason                  string  `json:"reason"`
}

// Model is a deterministic, bounded episodic k-nearest-neighbor model. It is
// safe for concurrent Predict/PredictWithEvidence calls after Fit returns.
type Model struct {
	// rows is the frozen candidate pool: at most MaxCandidatePoolSize rows,
	// selected deterministically by hash priority in Fit. Both LOO scoring
	// (Fit) and neighbor search (Predict) only ever consult this pool, never
	// the full, unbounded example set.
	rows       []row
	stats      [totalDims]featureStat
	dims       []int
	k          int
	featureSet FeatureSet
	ledger     modelapi.AttemptLedger
	policy     modelapi.Policy
	modelHash  string
	fitted     bool
}

func New() *Model { return &Model{} }

// Fit enumerates the frozen (k, feature_set) grid, scores each attempt with
// an embargoed leave-one-out loss over a deterministically bounded
// candidate pool, and retains the lowest-loss configuration (hash-priority
// tie-break) for Predict.
//
// Complexity: building all rows and hashing the input is O(n) in the number
// of supplied examples (a one-time, non-repeated pass). Freezing the
// candidate pool is O(n log n) (one deterministic sort, bounded by
// MaxCandidatePoolSize on output size). Grid scoring is
// O(len(grid) * MaxLOOQueries * MaxCandidatePoolSize) — a fixed constant
// independent of n. None of this depends on dataset content, k/feature-set
// grid, attempt budget, or the embargo window, all of which stay frozen.
func (m *Model) Fit(ctx context.Context, examples []modelapi.TrainingExample) (modelapi.Policy, error) {
	if err := ctx.Err(); err != nil {
		return modelapi.Policy{}, err
	}
	if err := modelapi.ValidateTrainingExamples(examples); err != nil {
		return modelapi.Policy{}, err
	}
	grid := enumerateGrid()
	if len(m.ledger.Entries())+len(grid)+1 > MaxAttempts {
		return modelapi.Policy{}, ErrBudgetExhausted
	}

	rows := buildRows(examples)
	pool := selectPool(rows, MaxCandidatePoolSize)
	stats := computeStats(pool)
	inputHash := examplesHash(examples)

	type scored struct {
		cfg      gridPoint
		loss     float64
		priority string
	}
	var best *scored
	for _, cfg := range grid {
		if err := ctx.Err(); err != nil {
			return modelapi.Policy{}, err
		}
		if _, err := m.ledger.Append(attemptID(cfg), OwnerID, "FIT_GRID_EMBARGOED_LOO", inputHash); err != nil {
			return modelapi.Policy{}, err
		}
		loss := looLoss(pool, activeDims(cfg.featureSet), stats, cfg.k)
		if math.IsInf(loss, 1) || math.IsNaN(loss) {
			continue
		}
		s := scored{cfg: cfg, loss: loss, priority: hashPriority("grid", attemptID(cfg))}
		if best == nil || s.loss < best.loss-1e-12 || (math.Abs(s.loss-best.loss) <= 1e-12 && s.priority < best.priority) {
			best = &s
		}
	}
	if best == nil {
		return modelapi.Policy{}, ErrNoGridCandidate
	}
	if _, err := m.ledger.Append("select:"+attemptID(best.cfg), OwnerID, "SELECT_EPISODIC_CONFIG", inputHash); err != nil {
		return modelapi.Policy{}, err
	}

	policy, err := buildPolicy(pool, best.cfg)
	if err != nil {
		return modelapi.Policy{}, err
	}

	m.rows = pool
	m.stats = stats
	m.dims = activeDims(best.cfg.featureSet)
	m.k = best.cfg.k
	m.featureSet = best.cfg.featureSet
	m.policy = policy
	m.modelHash = hashStrings("episodic-nearest-neighbor.v1", strconv.Itoa(Seed), attemptID(best.cfg), inputHash, canonicalPolicy(policy))
	m.fitted = true
	return clonePolicy(policy), nil
}

// Predict satisfies modelapi.Model. It delegates to PredictWithEvidence and
// discards the evidence summary.
func (m *Model) Predict(ctx context.Context, market modelapi.MarketState, account modelapi.AccountContext) (modelapi.TargetIntent, error) {
	intent, _, err := m.PredictWithEvidence(ctx, market, account)
	return intent, err
}

// PredictWithEvidence returns the predicted intent alongside a bounded,
// aggregated evidence summary and human-readable reason. No individual
// training row (ExampleID, EventID, feature vector, or per-neighbor target)
// is ever included in the summary.
func (m *Model) PredictWithEvidence(ctx context.Context, market modelapi.MarketState, account modelapi.AccountContext) (modelapi.TargetIntent, EvidenceSummary, error) {
	if err := ctx.Err(); err != nil {
		return modelapi.TargetIntent{}, EvidenceSummary{}, err
	}
	if !m.fitted {
		return modelapi.TargetIntent{}, EvidenceSummary{}, ErrNotFitted
	}
	if err := market.Validate(); err != nil {
		return modelapi.TargetIntent{}, EvidenceSummary{}, err
	}
	qv, qm := extractDims(market, account)
	chosen, considered, embargoedCount := kNearest(m.rows, m.dims, m.stats, market.EventID, market.DecisionMS, qv, qm, m.k)

	predicted := m.policy.DefaultIntent
	agg := evidenceAgg{}
	if len(chosen) > 0 {
		predicted, agg = combine(m.rows, chosen)
	}
	if err := predicted.Validate(); err != nil {
		return modelapi.TargetIntent{}, EvidenceSummary{}, err
	}

	summary := EvidenceSummary{
		SchemaVersion:           SchemaVersion,
		K:                       m.k,
		FeatureSet:              string(m.featureSet),
		ConsideredCount:         considered,
		EmbargoedCount:          embargoedCount,
		NeighborCount:           len(chosen),
		MeanDistance:            agg.meanDistance,
		MinDistance:             agg.minDistance,
		MaxDistance:             agg.maxDistance,
		DominantVoteWeightShare: agg.dominantShare,
		Reason: fmt.Sprintf(
			"episodic_knn k=%d(used=%d) feature_set=%s considered=%d embargoed=%d mean_distance=%.6f seed=%d",
			m.k, len(chosen), m.featureSet, considered, embargoedCount, agg.meanDistance, Seed,
		),
	}
	return predicted, summary, nil
}

func (m *Model) AttemptLedger() []modelapi.AttemptEntry { return m.ledger.Entries() }
func (m *Model) LedgerHead() string                     { return m.ledger.HeadHash() }
func (m *Model) Policy() (modelapi.Policy, error) {
	if !m.fitted {
		return modelapi.Policy{}, ErrNotFitted
	}
	return clonePolicy(m.policy), nil
}
func (m *Model) ModelHash() string          { return m.modelHash }
func (m *Model) SelectedK() int             { return m.k }
func (m *Model) SelectedFeatureSet() string { return string(m.featureSet) }
func (m *Model) LabelsRead() bool           { return false }

// --- feature extraction & missing-aware standardization ---------------------

func extractDims(market modelapi.MarketState, account modelapi.AccountContext) (values [totalDims]float64, missing [totalDims]bool) {
	for i := 0; i < requiredDims; i++ {
		values[i] = market.Features[i]
		missing[i] = market.MissingBits&(uint64(1)<<uint(i)) != 0
	}
	values[requiredDims+0] = account.SignedPositionFraction
	values[requiredDims+1] = account.GrossLeverage
	values[requiredDims+2] = account.FreeEquityUSDT
	values[requiredDims+3] = float64(account.PendingActionCount)
	return values, missing
}

func activeDims(fs FeatureSet) []int {
	dims := make([]int, 0, totalDims)
	for i := 0; i < requiredDims; i++ {
		if fs == FeatureSetRequiredOnly && optionalFeatureNames[modelapi.FeatureOrder[i]] {
			continue
		}
		dims = append(dims, i)
	}
	for i := 0; i < accountDims; i++ {
		dims = append(dims, requiredDims+i)
	}
	return dims
}

type featureStat struct{ mean, std float64 }

func computeStats(rows []row) [totalDims]featureStat {
	var stats [totalDims]featureStat
	for d := 0; d < totalDims; d++ {
		sum, n := 0.0, 0
		for _, r := range rows {
			if !r.missing[d] {
				sum += r.values[d]
				n++
			}
		}
		mean := 0.0
		if n > 0 {
			mean = sum / float64(n)
		}
		varSum, vn := 0.0, 0
		for _, r := range rows {
			if !r.missing[d] {
				diff := r.values[d] - mean
				varSum += diff * diff
				vn++
			}
		}
		std := 1.0
		if vn > 0 {
			std = math.Sqrt(varSum / float64(vn))
		}
		if std <= 1e-12 {
			std = 1
		}
		stats[d] = featureStat{mean: mean, std: std}
	}
	return stats
}

// distance is the missing-aware standardized mean-squared distance over the
// active dims. A feature missing on either side contributes a fixed,
// data-free penalty instead of an imputed value.
func distance(dims []int, stats [totalDims]featureStat, qv [totalDims]float64, qm [totalDims]bool, cv [totalDims]float64, cm [totalDims]bool) float64 {
	if len(dims) == 0 {
		return 0
	}
	sum := 0.0
	for _, d := range dims {
		if qm[d] || cm[d] {
			sum += missingPenalty
			continue
		}
		zq := (qv[d] - stats[d].mean) / stats[d].std
		zc := (cv[d] - stats[d].mean) / stats[d].std
		diff := zq - zc
		sum += diff * diff
	}
	return sum / float64(len(dims))
}

// --- self-symbol embargo -----------------------------------------------------

// embargoed reports whether a candidate must be excluded from the neighbor
// set for the given query: either it is the exact same decision event
// (self-neighbor), or its decision time falls within the frozen 24h window
// of the query's decision time (self-symbol embargo). Only identity fields
// participate; neither is ever used as a distance feature.
func embargoed(queryEventID string, queryDecisionMS int64, candidateEventID string, candidateDecisionMS int64) bool {
	if candidateEventID == queryEventID {
		return true
	}
	delta := candidateDecisionMS - queryDecisionMS
	if delta < 0 {
		delta = -delta
	}
	return delta < EmbargoWindowMS
}

// --- neighbor search ---------------------------------------------------------

type neighbor struct {
	idx      int
	distance float64
}

// kNearest returns up to k non-embargoed neighbors, sorted by distance and
// then by a deterministic, seed-bound hash priority so ties never depend on
// input slice order.
func kNearest(rows []row, dims []int, stats [totalDims]featureStat, queryEventID string, queryDecisionMS int64, qv [totalDims]float64, qm [totalDims]bool, k int) (chosen []neighbor, considered, embargoedCount int) {
	candidates := make([]neighbor, 0, len(rows))
	for i, r := range rows {
		if embargoed(queryEventID, queryDecisionMS, r.eventID, r.decisionMS) {
			embargoedCount++
			continue
		}
		considered++
		candidates = append(candidates, neighbor{idx: i, distance: distance(dims, stats, qv, qm, r.values, r.missing)})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return rows[candidates[i].idx].priority < rows[candidates[j].idx].priority
	})
	if k < len(candidates) {
		candidates = candidates[:k]
	}
	return candidates, considered, embargoedCount
}

type evidenceAgg struct {
	meanDistance  float64
	minDistance   float64
	maxDistance   float64
	dominantShare float64
}

// combine produces a distance-weighted vote across the chosen neighbors.
// The continuous fraction is weight-averaged then snapped to the frozen
// grid; the categorical horizon/delay fields use weighted-mode voting with
// hash-priority tie-breaks.
func combine(rows []row, chosen []neighbor) (modelapi.TargetIntent, evidenceAgg) {
	weightSum := 0.0
	fracWeighted := 0.0
	horizonWeight := map[int]float64{}
	delayWeight := map[int]float64{}
	horizonPriority := map[int]string{}
	delayPriority := map[int]string{}
	minDist := math.Inf(1)
	maxDist := 0.0
	sumDist := 0.0
	for _, n := range chosen {
		r := rows[n.idx]
		w := 1.0 / (neighborWeightEpsilon + n.distance)
		weightSum += w
		fracWeighted += w * r.target.TargetSignedPositionFraction
		horizonWeight[r.target.TargetHorizonHours] += w
		delayWeight[r.target.DelayBars] += w
		if p, ok := horizonPriority[r.target.TargetHorizonHours]; !ok || r.priority < p {
			horizonPriority[r.target.TargetHorizonHours] = r.priority
		}
		if p, ok := delayPriority[r.target.DelayBars]; !ok || r.priority < p {
			delayPriority[r.target.DelayBars] = r.priority
		}
		if n.distance < minDist {
			minDist = n.distance
		}
		if n.distance > maxDist {
			maxDist = n.distance
		}
		sumDist += n.distance
	}
	if weightSum <= 0 {
		weightSum = 1
	}
	frac := nearestAllowedFraction(fracWeighted / weightSum)
	horizon := pickModeInt(horizonWeight, horizonPriority, 1)
	delay := pickModeInt(delayWeight, delayPriority, 0)
	dominant := horizonWeight[horizon] / weightSum
	if math.IsInf(minDist, 1) {
		minDist = 0
	}
	mean := sumDist / float64(len(chosen))
	return modelapi.TargetIntent{TargetSignedPositionFraction: frac, TargetHorizonHours: horizon, DelayBars: delay},
		evidenceAgg{meanDistance: mean, minDistance: minDist, maxDistance: maxDist, dominantShare: dominant}
}

func nearestAllowedFraction(x float64) float64 {
	best := AllowedFractions[0]
	bestDiff := math.Abs(x - best)
	for _, v := range AllowedFractions[1:] {
		d := math.Abs(x - v)
		if d < bestDiff-1e-12 || (math.Abs(d-bestDiff) <= 1e-12 && v < best) {
			best, bestDiff = v, d
		}
	}
	return best
}

// pickModeInt returns the key with the highest total weight, breaking ties
// with the lowest hash priority recorded for that key (never input order).
func pickModeInt(weights map[int]float64, priority map[int]string, fallback int) int {
	if len(weights) == 0 {
		return fallback
	}
	keys := make([]int, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	best := keys[0]
	bestWeight := weights[best]
	bestPriority := priority[best]
	for _, k := range keys[1:] {
		w := weights[k]
		p := priority[k]
		if w > bestWeight+1e-12 || (math.Abs(w-bestWeight) <= 1e-12 && p < bestPriority) {
			best, bestWeight, bestPriority = k, w, p
		}
	}
	return best
}

// --- fit-time embargoed leave-one-out scoring --------------------------------

// looLoss scores one (dims, k) grid attempt by treating up to MaxLOOQueries
// deterministically chosen pool rows as pseudo-queries against the (already
// bounded) candidate pool, under the same embargo rule used at Predict time
// (a row's own event identity always self-embargoes). Bounding the query
// count keeps this O(MaxLOOQueries * len(pool)) — a fixed constant — rather
// than the O(len(pool)^2) that scoring every pool row would cost.
func looLoss(pool []row, dims []int, stats [totalDims]featureStat, k int) float64 {
	if len(pool) == 0 {
		return math.Inf(1)
	}
	queries := selectPool(pool, MaxLOOQueries)
	total := 0.0
	for _, r := range queries {
		chosen, _, _ := kNearest(pool, dims, stats, r.eventID, r.decisionMS, r.values, r.missing, k)
		predicted := modelapi.TargetIntent{TargetHorizonHours: 1}
		if len(chosen) > 0 {
			predicted, _ = combine(pool, chosen)
		}
		total += lossFn(predicted, r.target)
	}
	return total / float64(len(queries))
}

func lossFn(predicted, actual modelapi.TargetIntent) float64 {
	diff := predicted.TargetSignedPositionFraction - actual.TargetSignedPositionFraction
	loss := diff * diff
	if predicted.TargetHorizonHours != actual.TargetHorizonHours {
		loss++
	}
	if predicted.DelayBars != actual.DelayBars {
		loss++
	}
	return loss
}

// --- bounded, non-leaking policy summary -------------------------------------

// buildPolicy summarizes the fitted training set into at most MaxLeaves
// aggregated evidence buckets, grouped by distinct observed intent value.
// No ExampleID, EventID, or feature value is ever attached to a leaf.
func buildPolicy(rows []row, cfg gridPoint) (modelapi.Policy, error) {
	type bucket struct {
		intent   modelapi.TargetIntent
		count    int
		priority string
	}
	buckets := map[string]*bucket{}
	order := make([]string, 0)
	for _, r := range rows {
		key := intentKey(r.target)
		b, ok := buckets[key]
		if !ok {
			b = &bucket{intent: r.target, priority: hashPriority("bucket", key)}
			buckets[key] = b
			order = append(order, key)
		}
		b.count++
	}
	sort.Slice(order, func(i, j int) bool {
		bi, bj := buckets[order[i]], buckets[order[j]]
		if bi.count != bj.count {
			return bi.count > bj.count
		}
		return bi.priority < bj.priority
	})

	limit := len(order)
	if limit > MaxLeaves {
		limit = MaxLeaves - 1
	}
	leaves := make([]modelapi.CandidateLeaf, 0, MaxLeaves)
	otherCount := 0
	for i, key := range order {
		b := buckets[key]
		if i >= limit {
			otherCount += b.count
			continue
		}
		leaf := modelapi.CandidateLeaf{
			LeafID:          fmt.Sprintf("episodic-bucket-%02d", len(leaves)+1),
			Predicate:       fmt.Sprintf("EPISODIC_NEIGHBOR_EVIDENCE k=%d feature_set=%s support=%d", cfg.k, cfg.featureSet, b.count),
			Intent:          b.intent,
			RejectionReason: "synthetic-only aggregated neighbor evidence; individual training rows are sealed and never exposed",
		}
		if err := leaf.Intent.Validate(); err != nil {
			return modelapi.Policy{}, err
		}
		leaves = append(leaves, leaf)
	}
	if otherCount > 0 {
		leaves = append(leaves, modelapi.CandidateLeaf{
			LeafID:          "episodic-bucket-other",
			Predicate:       fmt.Sprintf("EPISODIC_NEIGHBOR_EVIDENCE k=%d feature_set=%s support=%d bucket=other", cfg.k, cfg.featureSet, otherCount),
			Intent:          modelapi.TargetIntent{TargetHorizonHours: 1},
			RejectionReason: "aggregated overflow bucket beyond leaf budget; individual training rows are sealed and never exposed",
		})
	}
	defaultIntent := modelapi.TargetIntent{TargetHorizonHours: 1}
	if len(order) > 0 {
		defaultIntent = buckets[order[0]].intent
	}
	return modelapi.Policy{
		SchemaVersion: modelapi.SchemaVersion,
		PolicyID:      fmt.Sprintf("sonnet-episodic-k%d-%s", cfg.k, cfg.featureSet),
		Leaves:        leaves,
		DefaultIntent: defaultIntent,
	}, nil
}

// --- hashing & row construction ----------------------------------------------

func buildRows(examples []modelapi.TrainingExample) []row {
	rows := make([]row, len(examples))
	for i, ex := range examples {
		v, m := extractDims(ex.Market, ex.Account)
		rows[i] = row{
			eventID:    ex.Market.EventID,
			decisionMS: ex.Market.DecisionMS,
			values:     v,
			missing:    m,
			target:     ex.SyntheticTarget,
			priority:   hashPriority("neighbor", ex.ExampleID, ex.Market.EventID),
		}
	}
	return rows
}

// selectPool deterministically bounds rows to at most limit entries and
// fixes their iteration order, using each row's seed-bound hash priority
// (see hashPriority) as the sole ordering key. Because priority depends
// only on each row's own identity (example ID / event ID), never on its
// position in the input slice, the resulting pool — both its membership
// when len(rows) > limit and its order in every case — is invariant under
// any permutation of rows. The ExampleID uniqueness enforced by
// modelapi.ValidateTrainingExamples makes SHA-256 priority collisions
// practically unreachable; the eventID/decisionMS fallback below exists
// only to keep the comparator strict (and still input-order-independent)
// in that theoretical case.
func selectPool(rows []row, limit int) []row {
	ordered := make([]row, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].priority != ordered[j].priority {
			return ordered[i].priority < ordered[j].priority
		}
		if ordered[i].eventID != ordered[j].eventID {
			return ordered[i].eventID < ordered[j].eventID
		}
		return ordered[i].decisionMS < ordered[j].decisionMS
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

// hashPriority is a deterministic, seed-bound tie-break key. It never
// depends on caller-supplied slice order and is never exposed to callers.
func hashPriority(parts ...string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strconv.Itoa(Seed)))
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func examplesHash(examples []modelapi.TrainingExample) string {
	entries := make([]string, 0, len(examples))
	for _, e := range examples {
		// EventID and DecisionMS bind the supplied fixture identity but are
		// never used as nearest-neighbor features. Sort the canonical entries
		// before hashing so the fit identity is invariant under input order,
		// just like candidate-pool selection.
		entries = append(entries, e.ExampleID+"\x00"+e.Market.EventID+"\x00"+strconv.FormatInt(e.Market.DecisionMS, 10)+"\x00"+intentKey(e.SyntheticTarget)+"\x00")
	}
	sort.Strings(entries)
	h := sha256.New()
	_, _ = h.Write([]byte(modelapi.APIHash() + "\x00"))
	for _, entry := range entries {
		_, _ = h.Write([]byte(entry))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalPolicy(p modelapi.Policy) string {
	var b []byte
	b = append(b, p.PolicyID...)
	for _, leaf := range p.Leaves {
		b = append(b, 0)
		b = append(b, leaf.LeafID...)
		b = append(b, 0)
		b = append(b, leaf.Predicate...)
		b = append(b, 0)
		b = append(b, intentKey(leaf.Intent)...)
	}
	b = append(b, 0)
	b = append(b, intentKey(p.DefaultIntent)...)
	return string(b)
}

func intentKey(i modelapi.TargetIntent) string {
	return strconv.FormatFloat(i.TargetSignedPositionFraction, 'g', -1, 64) + "/" + strconv.Itoa(i.TargetHorizonHours) + "/" + strconv.Itoa(i.DelayBars)
}

func clonePolicy(p modelapi.Policy) modelapi.Policy {
	p.Leaves = append([]modelapi.CandidateLeaf(nil), p.Leaves...)
	return p
}

func hashStrings(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		_, _ = h.Write([]byte(v + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CodeHash is a package identity hash over schema, grid, API, and seed.
func CodeHash() string {
	parts := []string{SchemaVersion, OwnerID, modelapi.APIHash(), modelapi.GridHash(), strconv.Itoa(Seed), strconv.Itoa(MaxAttempts), strconv.Itoa(MaxLeaves), strconv.FormatInt(EmbargoWindowMS, 10), strconv.Itoa(MaxCandidatePoolSize), strconv.Itoa(MaxLOOQueries)}
	for _, k := range KGrid {
		parts = append(parts, strconv.Itoa(k))
	}
	for _, fs := range FeatureSets {
		parts = append(parts, string(fs))
	}
	return hashStrings(parts...)
}
