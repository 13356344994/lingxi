// Package changepoint is the Grok owner change-point / rare-state model.
// It trains only on sealed synthetic/discovery examples from modelapi and
// never opens files, reads reward labels, or inspects peer outputs.
package changepoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"lingxi/internal/datadrivenv3/modelapi"
)

const (
	OwnerID       = "grok"
	SchemaVersion = "lingxi.data-driven-router-v3.model.changepoint.v1"
	Seed          = 42
	MaxAttempts   = 16
	MaxLeaves     = 32
	MSPerDay      = int64(24 * 60 * 60 * 1000)
)

// Frozen G1 search grid.
var (
	RegimeCounts     = []int{2, 3, 4}
	MinSegmentDays   = []int{10, 20}
	ChangePenalties  = []float64{1.0, 2.0}
	AllowedFractions = []float64{-1, -.5, -.25, -.1, 0, .1, .25, .5, 1}
	AllowedHorizons  = []int{1, 4, 24}
	AllowedDelays    = []int{0, 1, 2, 3}
)

// Hyperparameters is one grid point.
type Hyperparameters struct {
	RegimeCount     int     `json:"regime_count"`
	MinSegmentDays  int     `json:"minimum_segment_days"`
	ChangePenalty   float64 `json:"change_penalty"`
	AttemptSequence int     `json:"attempt_sequence"`
	AttemptID       string  `json:"attempt_id"`
}

// RegimeDescriptor is a replayable relative-state characterization of a
// time-discovered regime. Absolute dates are never stored as predict features.
type RegimeDescriptor struct {
	RegimeID      string                         `json:"regime_id"`
	Rare          bool                           `json:"rare"`
	Centroid      [modelapi.FeatureCount]float64 `json:"centroid"`
	PresentMask   uint64                         `json:"present_mask"`
	Thresholds    []FeatureThreshold             `json:"thresholds"`
	Intent        modelapi.TargetIntent          `json:"intent"`
	SupportDays   int                            `json:"support_days"`
	SupportEvents int                            `json:"support_events"`
	Reason        string                         `json:"reason"`
}

// FeatureThreshold is an axis-aligned relative predicate clause.
type FeatureThreshold struct {
	FeatureIndex int     `json:"feature_index"`
	FeatureName  string  `json:"feature_name"`
	Op           string  `json:"op"` // ">=", "<", "present", "missing"
	Value        float64 `json:"value,omitempty"`
}

// Model is a deterministic change-point / rare-state segmenter.
type Model struct {
	ledger     *modelapi.AttemptLedger
	policy     modelapi.Policy
	regimes    []RegimeDescriptor
	selected   Hyperparameters
	fitted     bool
	labelsRead bool
}

func New() *Model {
	return &Model{ledger: &modelapi.AttemptLedger{}, labelsRead: false}
}

func (m *Model) Ledger() []modelapi.AttemptEntry {
	if m == nil || m.ledger == nil {
		return nil
	}
	return m.ledger.Entries()
}

func (m *Model) LedgerHead() string {
	if m == nil || m.ledger == nil {
		return ""
	}
	return m.ledger.HeadHash()
}

func (m *Model) Selected() Hyperparameters { return m.selected }
func (m *Model) Policy() modelapi.Policy   { return m.policy }
func (m *Model) Regimes() []RegimeDescriptor {
	out := make([]RegimeDescriptor, len(m.regimes))
	copy(out, m.regimes)
	return out
}
func (m *Model) LabelsRead() bool { return m != nil && m.labelsRead }

// Fit enumerates the frozen grid (≤16 attempts), segments by time-block
// feature-distribution change, then emits relative-state regime leaves.
func (m *Model) Fit(ctx context.Context, examples []modelapi.TrainingExample) (modelapi.Policy, error) {
	if m == nil {
		return modelapi.Policy{}, errors.New("nil changepoint model")
	}
	if m.ledger == nil {
		m.ledger = &modelapi.AttemptLedger{}
	}
	if err := modelapi.ValidateTrainingExamples(examples); err != nil {
		return modelapi.Policy{}, err
	}
	if err := ctx.Err(); err != nil {
		return modelapi.Policy{}, err
	}
	grid := enumerateGrid()
	if len(grid) > MaxAttempts {
		return modelapi.Policy{}, fmt.Errorf("grid size %d exceeds max attempts %d", len(grid), MaxAttempts)
	}

	sorted := append([]modelapi.TrainingExample(nil), examples...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Market.DecisionMS != sorted[j].Market.DecisionMS {
			return sorted[i].Market.DecisionMS < sorted[j].Market.DecisionMS
		}
		return sorted[i].ExampleID < sorted[j].ExampleID
	})
	blocks, err := buildDayBlocks(sorted)
	if err != nil {
		return modelapi.Policy{}, err
	}

	var (
		bestScore   = math.Inf(-1)
		bestPolicy  modelapi.Policy
		bestRegimes []RegimeDescriptor
		bestHP      Hyperparameters
		found       bool
	)
	for _, hp := range grid {
		if err := ctx.Err(); err != nil {
			return modelapi.Policy{}, err
		}
		inputHash := hashAttemptInput(hp, sorted)
		if _, err := m.ledger.Append(hp.AttemptID, OwnerID, "FIT_GRID", inputHash); err != nil {
			return modelapi.Policy{}, err
		}
		regimes, score, err := fitOne(blocks, sorted, hp)
		if err != nil {
			// Invalid under min-segment / regime constraints: recorded attempt, skip.
			continue
		}
		policy, err := regimesToPolicy(regimes, hp)
		if err != nil {
			return modelapi.Policy{}, err
		}
		if len(policy.Leaves) > MaxLeaves {
			return modelapi.Policy{}, fmt.Errorf("attempt %s produced %d leaves > max %d", hp.AttemptID, len(policy.Leaves), MaxLeaves)
		}
		if !found || score > bestScore || (score == bestScore && hp.AttemptSequence < bestHP.AttemptSequence) {
			found = true
			bestScore = score
			bestPolicy = policy
			bestRegimes = regimes
			bestHP = hp
		}
	}
	if !found {
		return modelapi.Policy{}, errors.New("no feasible change-point configuration under frozen grid")
	}
	if _, err := m.ledger.Append("select:"+bestHP.AttemptID, OwnerID, "SELECT_POLICY", hashPolicy(bestPolicy)); err != nil {
		return modelapi.Policy{}, err
	}
	m.policy = bestPolicy
	m.regimes = bestRegimes
	m.selected = bestHP
	m.fitted = true
	return bestPolicy, nil
}

func (m *Model) Predict(ctx context.Context, market modelapi.MarketState, account modelapi.AccountContext) (modelapi.TargetIntent, error) {
	if m == nil || !m.fitted {
		return modelapi.TargetIntent{}, errors.New("changepoint model not fitted")
	}
	if err := ctx.Err(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	if err := market.Validate(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	_ = account // account is decision-time context; regime match is market-state based
	matched := make([]int, 0, len(m.policy.Leaves))
	for i, leaf := range m.policy.Leaves {
		if leaf.RejectionReason != "" {
			continue
		}
		ok, err := evaluatePredicate(leaf.Predicate, market)
		if err != nil {
			return modelapi.TargetIntent{}, err
		}
		if ok {
			matched = append(matched, i)
		}
	}
	choose := -1
	switch {
	case len(matched) == 1:
		choose = matched[0]
	case len(matched) > 1:
		choose = nearestAmong(m.regimes, matched, market)
	default:
		// No leaf clause matched: fall back to missing-aware nearest relative centroid.
		choose = nearestRegime(m.regimes, market)
	}
	if choose < 0 || choose >= len(m.policy.Leaves) {
		return m.policy.DefaultIntent, nil
	}
	intent := m.policy.Leaves[choose].Intent
	if err := intent.Validate(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	return intent, nil
}

func nearestAmong(regimes []RegimeDescriptor, idxs []int, market modelapi.MarketState) int {
	best := -1
	bestDist := math.Inf(1)
	for _, i := range idxs {
		if i < 0 || i >= len(regimes) {
			continue
		}
		d := regimeDistance(regimes[i], market)
		if math.IsInf(d, 1) {
			continue
		}
		if d < bestDist-1e-15 || (math.Abs(d-bestDist) <= 1e-15 && (best < 0 || i < best)) {
			bestDist = d
			best = i
		}
	}
	return best
}

func enumerateGrid() []Hyperparameters {
	out := make([]Hyperparameters, 0, len(RegimeCounts)*len(MinSegmentDays)*len(ChangePenalties))
	seq := 1
	for _, regimes := range RegimeCounts {
		for _, minDays := range MinSegmentDays {
			for _, penalty := range ChangePenalties {
				out = append(out, Hyperparameters{
					RegimeCount:     regimes,
					MinSegmentDays:  minDays,
					ChangePenalty:   penalty,
					AttemptSequence: seq,
					AttemptID:       fmt.Sprintf("grok-cp-r%d-d%d-p%s-s%d", regimes, minDays, formatPenalty(penalty), seq),
				})
				seq++
			}
		}
	}
	return out
}

func formatPenalty(p float64) string {
	return strings.ReplaceAll(strconv.FormatFloat(p, 'f', -1, 64), ".", "p")
}

func fitOne(blocks []dayBlock, examples []modelapi.TrainingExample, hp Hyperparameters) ([]RegimeDescriptor, float64, error) {
	if len(blocks) < hp.RegimeCount*hp.MinSegmentDays {
		return nil, 0, errors.New("insufficient day blocks for regime/min-segment constraints")
	}
	bounds, cost, err := segmentBlocks(blocks, hp.RegimeCount, hp.MinSegmentDays, hp.ChangePenalty)
	if err != nil {
		return nil, 0, err
	}
	// change_penalty shapes boundaries for a fixed regime_count; strip it when
	// comparing across the frozen grid so rarer multi-regime fits can win on SSE.
	sse := cost - float64(hp.RegimeCount-1)*hp.ChangePenalty
	if sse < 0 {
		sse = 0
	}
	regimes := make([]RegimeDescriptor, 0, len(bounds))
	totalDays := len(blocks)
	for i, seg := range bounds {
		start, end := seg[0], seg[1]
		memberBlocks := blocks[start:end]
		centroid, present := blockCentroid(memberBlocks)
		thresholds := discriminativeThresholds(blocks, start, end, centroid, present)
		intent, events := majorityIntent(examples, memberBlocks[0].Day, memberBlocks[len(memberBlocks)-1].Day)
		regimes = append(regimes, RegimeDescriptor{
			RegimeID:      fmt.Sprintf("regime-%d", i),
			Centroid:      centroid,
			PresentMask:   present,
			Thresholds:    thresholds,
			Intent:        intent,
			SupportDays:   end - start,
			SupportEvents: events,
		})
	}
	markRareStates(regimes, totalDays, hp.RegimeCount)
	for i := range regimes {
		r := &regimes[i]
		r.Reason = fmt.Sprintf(
			"change_point regime=%d days=%d events=%d rare=%t penalty=%.1f sse=%.6f seed=%d",
			i, r.SupportDays, r.SupportEvents, r.Rare, hp.ChangePenalty, sse, Seed,
		)
		if r.Rare {
			r.Reason += "; rare_state_relative_predicate"
		}
	}
	score := -sse
	for _, r := range regimes {
		if r.SupportEvents > 0 {
			score += float64(r.SupportEvents) * 1e-3
		}
		if r.Rare {
			score += 0.25 // prefer configurations that isolate uncommon regimes
		}
	}
	// Mild complexity prior after SSE/rare: fewer regimes win ties.
	score -= 0.01 * float64(hp.RegimeCount)
	score -= 0.001 * hp.ChangePenalty
	return regimes, score, nil
}

// markRareStates flags imbalanced / uncommon regimes relative to equal share.
// Absolute calendar identity is never used; only relative support size.
func markRareStates(regimes []RegimeDescriptor, totalDays, regimeCount int) {
	if len(regimes) == 0 || totalDays <= 0 || regimeCount <= 0 {
		return
	}
	minDays := regimes[0].SupportDays
	maxDays := regimes[0].SupportDays
	for _, r := range regimes {
		if r.SupportDays < minDays {
			minDays = r.SupportDays
		}
		if r.SupportDays > maxDays {
			maxDays = r.SupportDays
		}
	}
	equalShare := float64(totalDays) / float64(regimeCount)
	for i := range regimes {
		// Rare iff uniquely (or tied) smallest and strictly below equal share,
		// and the segmentation is imbalanced.
		regimes[i].Rare = regimes[i].SupportDays == minDays &&
			minDays < maxDays &&
			float64(regimes[i].SupportDays) < equalShare-1e-9
	}
}

func regimesToPolicy(regimes []RegimeDescriptor, hp Hyperparameters) (modelapi.Policy, error) {
	leaves := make([]modelapi.CandidateLeaf, 0, len(regimes))
	for _, r := range regimes {
		pred := formatPredicate(r)
		if containsAbsoluteDate(pred) {
			return modelapi.Policy{}, errors.New("absolute date leaked into regime predicate")
		}
		leaf := modelapi.CandidateLeaf{
			LeafID:    fmt.Sprintf("%s-%s", hp.AttemptID, r.RegimeID),
			Predicate: pred,
			Intent:    r.Intent,
		}
		if r.Rare {
			leaf.RejectionReason = "" // rare states remain actionable but reason is in predicate text
		}
		if err := leaf.Intent.Validate(); err != nil {
			return modelapi.Policy{}, err
		}
		leaves = append(leaves, leaf)
	}
	if len(leaves) > MaxLeaves {
		return modelapi.Policy{}, fmt.Errorf("leaf budget exceeded: %d > %d", len(leaves), MaxLeaves)
	}
	defaultIntent := modelapi.TargetIntent{TargetSignedPositionFraction: 0, TargetHorizonHours: 1, DelayBars: 0}
	return modelapi.Policy{
		SchemaVersion: modelapi.SchemaVersion,
		PolicyID:      fmt.Sprintf("changepoint-%s", hp.AttemptID),
		Leaves:        leaves,
		DefaultIntent: defaultIntent,
	}, nil
}

func majorityIntent(examples []modelapi.TrainingExample, dayStart, dayEnd int64) (modelapi.TargetIntent, int) {
	type key struct {
		frac float64
		hor  int
		del  int
	}
	counts := map[key]int{}
	events := 0
	for _, ex := range examples {
		day := ex.Market.DecisionMS / MSPerDay
		if day < dayStart || day > dayEnd {
			continue
		}
		events++
		k := key{ex.SyntheticTarget.TargetSignedPositionFraction, ex.SyntheticTarget.TargetHorizonHours, ex.SyntheticTarget.DelayBars}
		counts[k]++
	}
	if events == 0 {
		return modelapi.TargetIntent{TargetHorizonHours: 1}, 0
	}
	best := key{frac: 0, hor: 1, del: 0}
	bestN := -1
	// Deterministic: highest count, then smaller |frac|, then frac, horizon, delay.
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := counts[keys[i]], counts[keys[j]]
		if ci != cj {
			return ci > cj
		}
		ai, aj := math.Abs(keys[i].frac), math.Abs(keys[j].frac)
		if ai != aj {
			return ai < aj
		}
		if keys[i].frac != keys[j].frac {
			return keys[i].frac < keys[j].frac
		}
		if keys[i].hor != keys[j].hor {
			return keys[i].hor < keys[j].hor
		}
		return keys[i].del < keys[j].del
	})
	if len(keys) > 0 {
		best = keys[0]
		bestN = counts[best]
	}
	_ = bestN
	return modelapi.TargetIntent{
		TargetSignedPositionFraction: best.frac,
		TargetHorizonHours:           best.hor,
		DelayBars:                    best.del,
	}, events
}

func hashAttemptInput(hp Hyperparameters, examples []modelapi.TrainingExample) string {
	h := sha256.New()
	_, _ = h.Write([]byte(SchemaVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fmt.Sprintf("%d|%d|%.6f|%d|%d", hp.RegimeCount, hp.MinSegmentDays, hp.ChangePenalty, hp.AttemptSequence, Seed)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(modelapi.APIHash()))
	_, _ = h.Write([]byte{0})
	for _, ex := range examples {
		_, _ = h.Write([]byte(ex.ExampleID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(ex.Market.EventID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.FormatInt(ex.Market.DecisionMS, 10)))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashPolicy(p modelapi.Policy) string {
	h := sha256.New()
	_, _ = h.Write([]byte(p.SchemaVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(p.PolicyID))
	_, _ = h.Write([]byte{0})
	for _, leaf := range p.Leaves {
		_, _ = h.Write([]byte(leaf.LeafID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(leaf.Predicate))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(fmt.Sprintf("%.12g|%d|%d|%s", leaf.Intent.TargetSignedPositionFraction, leaf.Intent.TargetHorizonHours, leaf.Intent.DelayBars, leaf.RejectionReason)))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte(fmt.Sprintf("%.12g|%d|%d", p.DefaultIntent.TargetSignedPositionFraction, p.DefaultIntent.TargetHorizonHours, p.DefaultIntent.DelayBars)))
	return hex.EncodeToString(h.Sum(nil))
}

// CodeHash is a package identity hash over schema, grid, API, and seed.
func CodeHash() string {
	parts := []string{
		SchemaVersion,
		OwnerID,
		modelapi.APIHash(),
		modelapi.GridHash(),
		strconv.Itoa(Seed),
		strconv.Itoa(MaxAttempts),
		strconv.Itoa(MaxLeaves),
	}
	for _, v := range RegimeCounts {
		parts = append(parts, strconv.Itoa(v))
	}
	for _, v := range MinSegmentDays {
		parts = append(parts, strconv.Itoa(v))
	}
	for _, v := range ChangePenalties {
		parts = append(parts, strconv.FormatFloat(v, 'g', -1, 64))
	}
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func containsAbsoluteDate(pred string) bool {
	lower := strings.ToLower(pred)
	banned := []string{"decision_ms", "decisionms", "calendar_date", "yyyymmdd", "absolute_date", "unix_day="}
	for _, b := range banned {
		if strings.Contains(lower, b) {
			return true
		}
	}
	return false
}
