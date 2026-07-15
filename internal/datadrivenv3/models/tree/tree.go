// Package tree implements Terra's bounded, synthetic-only decision tree.
// It deliberately has no dataset, reward-provider, peer-output, or filesystem
// dependency. Event IDs and decision times are validated by modelapi but are
// never considered as split features.
package tree

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
	OwnerID                 = "terra"
	Seed                    = 42
	MaxAttempts             = 16
	MaxCandidateLeaf        = 32
	MaxTrainingRows         = 8192
	MaxThresholdsPerFeature = 31
)

var (
	ErrBudgetExhausted = errors.New("tree attempt budget exhausted")
	ErrNotFitted       = errors.New("tree model has not been fitted")
)

type Config struct {
	MaxDepth      int
	MinLeafBlocks int
	MaxLeaves     int
}

// GridG1 is the complete frozen g1 search grid. Its Cartesian product has 12
// entries, leaving no ambiguity about which attempts are legal.
var GridG1 = []Config{
	{2, 10, 8}, {2, 10, 16}, {2, 20, 8}, {2, 20, 16},
	{3, 10, 8}, {3, 10, 16}, {3, 20, 8}, {3, 20, 16},
	{4, 10, 8}, {4, 10, 16}, {4, 20, 8}, {4, 20, 16},
}

type feature struct {
	name    string
	value   func(modelapi.MarketState, modelapi.AccountContext) float64
	missing func(modelapi.MarketState, modelapi.AccountContext) bool
}

type split struct {
	feature     int
	threshold   float64
	missingOnly bool
}

type node struct {
	split  *split
	left   *node
	right  *node
	rows   []int
	intent modelapi.TargetIntent
}

// Model is safe for concurrent predictions after Fit returns. Fit is intended
// for one owner instance and returns a defensive policy copy.
type Model struct {
	root      *node
	policy    modelapi.Policy
	ledger    modelapi.AttemptLedger
	modelHash string
}

func New() *Model { return &Model{} }

func (m *Model) Fit(ctx context.Context, examples []modelapi.TrainingExample) (modelapi.Policy, error) {
	if err := ctx.Err(); err != nil {
		return modelapi.Policy{}, err
	}
	if err := modelapi.ValidateTrainingExamples(examples); err != nil {
		return modelapi.Policy{}, err
	}
	if len(m.ledger.Entries())+len(GridG1) > MaxAttempts {
		return modelapi.Policy{}, ErrBudgetExhausted
	}
	ranked := rankExamples(examples)
	inputHash := examplesHash(ranked)
	trainingExamples := trainingSubset(ranked)
	var best *candidate
	for i, cfg := range GridG1 {
		if err := ctx.Err(); err != nil {
			return modelapi.Policy{}, err
		}
		if _, err := m.ledger.Append(fmt.Sprintf("terra-tree-%02d", i+1), OwnerID, "FIT_SYNTHETIC_TREE", inputHash); err != nil {
			return modelapi.Policy{}, err
		}
		c := train(trainingExamples, cfg)
		if best == nil || c.loss < best.loss || (c.loss == best.loss && c.key < best.key) {
			best = c
		}
	}
	if best == nil {
		return modelapi.Policy{}, errors.New("no permitted tree candidates")
	}
	leaves := make([]modelapi.CandidateLeaf, 0, len(best.leaves))
	collectLeaves(best.root, nil, &leaves)
	if len(leaves) > MaxCandidateLeaf {
		return modelapi.Policy{}, errors.New("candidate leaf budget exceeded")
	}
	policy := modelapi.Policy{
		SchemaVersion: modelapi.SchemaVersion,
		PolicyID:      "terra-bounded-tree-" + best.key,
		Leaves:        leaves,
		DefaultIntent: majorityIntent(trainingExamples, allRows(len(trainingExamples))),
	}
	m.root = best.root
	m.policy = clonePolicy(policy)
	m.modelHash = hashStrings(CodeHash(), strconv.Itoa(Seed), best.key, inputHash, canonicalPolicy(policy))
	return clonePolicy(policy), nil
}

func (m *Model) Predict(ctx context.Context, market modelapi.MarketState, account modelapi.AccountContext) (modelapi.TargetIntent, error) {
	if err := ctx.Err(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	if err := market.Validate(); err != nil {
		return modelapi.TargetIntent{}, err
	}
	if m.root == nil {
		return modelapi.TargetIntent{}, ErrNotFitted
	}
	current := m.root
	for current.split != nil {
		s := current.split
		f := features()[s.feature]
		isMissing := f.missing(market, account)
		goLeft := isMissing
		if s.missingOnly {
			goLeft = isMissing
		} else if !isMissing {
			goLeft = f.value(market, account) <= s.threshold
		}
		if goLeft {
			current = current.left
		} else {
			current = current.right
		}
	}
	return current.intent, nil
}

func (m *Model) AttemptLedger() []modelapi.AttemptEntry { return m.ledger.Entries() }
func (m *Model) Policy() (modelapi.Policy, error) {
	if m.root == nil {
		return modelapi.Policy{}, ErrNotFitted
	}
	return clonePolicy(m.policy), nil
}
func (m *Model) ModelHash() string { return m.modelHash }

// CodeHash identifies the frozen bounded-tree family, including its bounded
// sampling and quantile-search rules. A fitted model additionally binds its
// complete canonical input and selected policy.
func CodeHash() string {
	return hashStrings(
		"terra-bounded-tree.v3",
		modelapi.APIHash(),
		strconv.Itoa(Seed),
		strconv.Itoa(MaxAttempts),
		strconv.Itoa(MaxCandidateLeaf),
		strconv.Itoa(MaxTrainingRows),
		strconv.Itoa(MaxThresholdsPerFeature),
		"sampling-key=seed,example-id,event-id,decision-ms",
		"depth=2,3,4",
		"min-leaf-blocks=10,20",
		"max-leaves=8,16",
	)
}

type candidate struct {
	root   *node
	leaves []*node
	loss   float64
	key    string
}

func train(examples []modelapi.TrainingExample, cfg Config) *candidate {
	root := &node{rows: allRows(len(examples)), intent: majorityIntent(examples, allRows(len(examples)))}
	leaves := []*node{root}
	for depth := 0; depth < cfg.MaxDepth && len(leaves) < cfg.MaxLeaves; depth++ {
		next := make([]*node, 0, len(leaves)*2)
		for _, leaf := range leaves {
			s, ok := bestSplit(examples, leaf.rows, cfg.MinLeafBlocks)
			if !ok || len(next)+len(leaves)-1+2 > cfg.MaxLeaves {
				next = append(next, leaf)
				continue
			}
			left, right := partition(examples, leaf.rows, *s)
			leaf.split = s
			leaf.left = &node{rows: left, intent: majorityIntent(examples, left)}
			leaf.right = &node{rows: right, intent: majorityIntent(examples, right)}
			next = append(next, leaf.left, leaf.right)
		}
		if len(next) == len(leaves) {
			break
		}
		leaves = next
	}
	loss := 0.0
	for _, leaf := range leaves {
		loss += leafLoss(examples, leaf.rows, leaf.intent)
	}
	return &candidate{root: root, leaves: leaves, loss: loss, key: fmt.Sprintf("d%d-b%d-l%d", cfg.MaxDepth, cfg.MinLeafBlocks, cfg.MaxLeaves)}
}

func bestSplit(examples []modelapi.TrainingExample, rows []int, minLeaf int) (*split, bool) {
	if len(rows) < minLeaf*2 {
		return nil, false
	}
	bestLoss := math.Inf(1)
	var best *split
	for fi, f := range features() {
		present := make([]float64, 0, len(rows))
		missingCount := 0
		for _, row := range rows {
			if f.missing(examples[row].Market, examples[row].Account) {
				missingCount++
			} else {
				present = append(present, f.value(examples[row].Market, examples[row].Account))
			}
		}
		if missingCount >= minLeaf && len(rows)-missingCount >= minLeaf {
			s := split{feature: fi, missingOnly: true}
			if loss, ok := splitLoss(examples, rows, s, minLeaf); ok && betterSplit(loss, s, bestLoss, best) {
				bestLoss, best = loss, &s
			}
		}
		sort.Float64s(present)
		for _, i := range quantileSplitIndicesFromSorted(present, len(present), minLeaf) {
			s := split{feature: fi, threshold: present[i]/2 + present[i+1]/2}
			if loss, ok := splitLoss(examples, rows, s, minLeaf); ok && betterSplit(loss, s, bestLoss, best) {
				bestLoss, best = loss, &s
			}
		}
	}
	return best, best != nil
}

func betterSplit(loss float64, s split, bestLoss float64, best *split) bool {
	if loss < bestLoss-1e-12 {
		return true
	}
	if math.Abs(loss-bestLoss) > 1e-12 || best == nil {
		return false
	}
	if s.feature != best.feature {
		return s.feature < best.feature
	}
	if s.missingOnly != best.missingOnly {
		return s.missingOnly
	}
	return s.threshold < best.threshold
}

func splitLoss(examples []modelapi.TrainingExample, rows []int, s split, minLeaf int) (float64, bool) {
	left, right := partition(examples, rows, s)
	if len(left) < minLeaf || len(right) < minLeaf {
		return 0, false
	}
	return leafLoss(examples, left, majorityIntent(examples, left)) + leafLoss(examples, right, majorityIntent(examples, right)), true
}

func partition(examples []modelapi.TrainingExample, rows []int, s split) ([]int, []int) {
	f := features()[s.feature]
	left, right := make([]int, 0, len(rows)), make([]int, 0, len(rows))
	for _, row := range rows {
		example := examples[row]
		missing := f.missing(example.Market, example.Account)
		goLeft := missing
		if s.missingOnly {
			goLeft = missing
		} else if !missing {
			goLeft = f.value(example.Market, example.Account) <= s.threshold
		}
		if goLeft {
			left = append(left, row)
		} else {
			right = append(right, row)
		}
	}
	return left, right
}

func majorityIntent(examples []modelapi.TrainingExample, rows []int) modelapi.TargetIntent {
	counts := map[string]int{}
	intents := map[string]modelapi.TargetIntent{}
	bestKey, bestCount := "", -1
	for _, row := range rows {
		intent := examples[row].SyntheticTarget
		key := intentKey(intent)
		counts[key]++
		intents[key] = intent
		if counts[key] > bestCount || (counts[key] == bestCount && key < bestKey) {
			bestKey, bestCount = key, counts[key]
		}
	}
	return intents[bestKey]
}

func leafLoss(examples []modelapi.TrainingExample, rows []int, predicted modelapi.TargetIntent) float64 {
	loss := 0.0
	for _, row := range rows {
		actual := examples[row].SyntheticTarget
		loss += math.Pow(actual.TargetSignedPositionFraction-predicted.TargetSignedPositionFraction, 2)
		if actual.TargetHorizonHours != predicted.TargetHorizonHours {
			loss++
		}
		if actual.DelayBars != predicted.DelayBars {
			loss++
		}
	}
	return loss
}

func collectLeaves(n *node, path []string, out *[]modelapi.CandidateLeaf) {
	if n.split == nil {
		predicate := "TRUE"
		if len(path) > 0 {
			predicate = strings.Join(path, " AND ")
		}
		*out = append(*out, modelapi.CandidateLeaf{
			LeafID:          fmt.Sprintf("leaf-%02d", len(*out)+1),
			Predicate:       predicate,
			Intent:          n.intent,
			RejectionReason: "synthetic-only candidate; real-label validation remains sealed",
		})
		return
	}
	f := features()[n.split.feature]
	left, right := splitPredicates(f.name, *n.split)
	collectLeaves(n.left, append(append([]string(nil), path...), left), out)
	collectLeaves(n.right, append(append([]string(nil), path...), right), out)
}

func splitPredicates(name string, s split) (string, string) {
	if s.missingOnly {
		return name + " IS MISSING", name + " IS PRESENT"
	}
	v := strconv.FormatFloat(s.threshold, 'g', -1, 64)
	return name + " IS MISSING OR " + name + " <= " + v, name + " IS PRESENT AND " + name + " > " + v
}

func features() []feature {
	out := make([]feature, 0, modelapi.FeatureCount+4)
	for i, name := range modelapi.FeatureOrder {
		index := i
		out = append(out, feature{
			name:  name,
			value: func(m modelapi.MarketState, _ modelapi.AccountContext) float64 { return m.Features[index] },
			missing: func(m modelapi.MarketState, _ modelapi.AccountContext) bool {
				return m.MissingBits&(uint64(1)<<index) != 0
			},
		})
	}
	return append(out,
		feature{"account_signed_position_fraction", func(_ modelapi.MarketState, a modelapi.AccountContext) float64 { return a.SignedPositionFraction }, func(modelapi.MarketState, modelapi.AccountContext) bool { return false }},
		feature{"account_gross_leverage", func(_ modelapi.MarketState, a modelapi.AccountContext) float64 { return a.GrossLeverage }, func(modelapi.MarketState, modelapi.AccountContext) bool { return false }},
		feature{"account_free_equity_usdt", func(_ modelapi.MarketState, a modelapi.AccountContext) float64 { return a.FreeEquityUSDT }, func(modelapi.MarketState, modelapi.AccountContext) bool { return false }},
		feature{"account_pending_action_count", func(_ modelapi.MarketState, a modelapi.AccountContext) float64 { return float64(a.PendingActionCount) }, func(modelapi.MarketState, modelapi.AccountContext) bool { return false }},
	)
}

func quantileSplitIndices(presentCount, minLeaf int) []int {
	return quantileSplitIndicesFromSorted(nil, presentCount, minLeaf)
}

func quantileSplitIndicesFromSorted(present []float64, presentCount, minLeaf int) []int {
	first, last := minLeaf-1, presentCount-minLeaf-1
	if first > last {
		return nil
	}
	eligible := make([]int, 0, presentCount)
	for i := first; i <= last; i++ {
		if len(present) == 0 || present[i] != present[i+1] {
			eligible = append(eligible, i)
		}
	}
	count := len(eligible)
	if count == 0 {
		return nil
	}
	if count <= MaxThresholdsPerFeature {
		return eligible
	}
	count = MaxThresholdsPerFeature
	indices := make([]int, 0, count)
	for q := 1; q <= count; q++ {
		// The candidate positions cover the legal unique-value split range
		// evenly, including its endpoints. Integer arithmetic is reproducible.
		indices = append(indices, eligible[(q-1)*(len(eligible)-1)/(count-1)])
	}
	return indices
}

type rankedExample struct {
	example  modelapi.TrainingExample
	rankHash string
}

func rankExamples(examples []modelapi.TrainingExample) []rankedExample {
	ranked := make([]rankedExample, len(examples))
	for i, example := range examples {
		ranked[i] = rankedExample{example: example, rankHash: samplingRankHash(example)}
	}
	sort.Slice(ranked, func(i, j int) bool { return rankedExampleLess(ranked[i], ranked[j]) })
	return ranked
}

func rankedExampleLess(left, right rankedExample) bool {
	if left.rankHash != right.rankHash {
		return left.rankHash < right.rankHash
	}
	if left.example.ExampleID != right.example.ExampleID {
		return left.example.ExampleID < right.example.ExampleID
	}
	if left.example.Market.EventID != right.example.Market.EventID {
		return left.example.Market.EventID < right.example.Market.EventID
	}
	return left.example.Market.DecisionMS < right.example.Market.DecisionMS
}

func trainingSubset(ranked []rankedExample) []modelapi.TrainingExample {
	count := len(ranked)
	if count > MaxTrainingRows {
		count = MaxTrainingRows
	}
	examples := make([]modelapi.TrainingExample, count)
	for i := range examples {
		examples[i] = ranked[i].example
	}
	return examples
}

func examplesHash(ranked []rankedExample) string {
	h := sha256.New()
	_, _ = h.Write([]byte(modelapi.APIHash() + "\x00"))
	for _, example := range ranked {
		_, _ = h.Write([]byte(exampleHash(example.example) + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// samplingRankHash is deliberately label- and data-independent. Its only
// inputs are the frozen seed and immutable example identity fields.
func samplingRankHash(example modelapi.TrainingExample) string {
	return hashStrings(
		strconv.Itoa(Seed),
		example.ExampleID,
		example.Market.EventID,
		strconv.FormatInt(example.Market.DecisionMS, 10),
	)
}

func exampleHash(example modelapi.TrainingExample) string {
	h := sha256.New()
	writeString := func(value string) { _, _ = h.Write([]byte(value + "\x00")) }
	writeFloat := func(value float64) { writeString(strconv.FormatUint(math.Float64bits(value), 16)) }
	writeInt := func(value int) { writeString(strconv.Itoa(value)) }

	writeString("terra-tree-training-row.v1")
	writeString(example.SchemaVersion)
	writeString(string(example.Kind))
	writeString(example.ExampleID)
	writeString(example.Market.SchemaVersion)
	writeString(example.Market.EventID)
	writeString(strconv.FormatInt(example.Market.DecisionMS, 10))
	writeString(strconv.FormatUint(example.Market.MissingBits, 10))
	for _, value := range example.Market.Features {
		writeFloat(value)
	}
	writeFloat(example.Account.SignedPositionFraction)
	writeFloat(example.Account.FixedNotionalUSDT)
	writeFloat(example.Account.WeightedAverageEntryPrice)
	writeInt(example.Account.PositionAgeBars)
	writeFloat(example.Account.DecisionTimeUnrealizedReturn)
	writeFloat(example.Account.CumulativeSignedFundingUSDT)
	writeFloat(example.Account.CashUSDT)
	writeFloat(example.Account.MarkedEquityUSDT)
	writeFloat(example.Account.RetainedCapitalBaseUSDT)
	writeFloat(example.Account.CumulativeWithdrawalsUSDT)
	writeFloat(example.Account.GrossLeverage)
	writeFloat(example.Account.FreeEquityUSDT)
	writeInt(example.Account.OpenPositionCount)
	writeInt(example.Account.PendingActionCount)
	writeFloat(example.SyntheticTarget.TargetSignedPositionFraction)
	writeInt(example.SyntheticTarget.TargetHorizonHours)
	writeInt(example.SyntheticTarget.DelayBars)
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalPolicy(p modelapi.Policy) string {
	var b strings.Builder
	b.WriteString(p.PolicyID)
	for _, leaf := range p.Leaves {
		b.WriteString("\x00" + leaf.Predicate + "\x00" + intentKey(leaf.Intent))
	}
	return b.String()
}
func intentKey(i modelapi.TargetIntent) string {
	return strconv.FormatFloat(i.TargetSignedPositionFraction, 'g', -1, 64) + "/" + strconv.Itoa(i.TargetHorizonHours) + "/" + strconv.Itoa(i.DelayBars)
}
func allRows(n int) []int {
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	return rows
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
