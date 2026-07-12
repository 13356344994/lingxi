package backward

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
)

// Options configures one backward-mining run.
type Options struct {
	// Events is the full pool of research-partition observations to mine.
	// Every event must already carry a fully net-of-cost outcome.
	Events []Event
	// SearchSpace is the preregistered set of combinations to evaluate. It
	// must be non-empty and is the sole source of what may be searched —
	// Run never invents or widens it.
	SearchSpace SearchSpace

	// NeutralBandBPS is the symmetric band around zero net outcome that is
	// classified neutral rather than winner/loss. Defaults to 0.
	NeutralBandBPS float64
	// MatchWindow bounds how far apart in decision time a control may be
	// from the winner it is matched against. Must be positive: there is no
	// "unlimited" matching window.
	MatchWindow time.Duration
	// VolatilityTolerance / LiquidityTolerance bound how far a control's
	// volatility/liquidity may differ from the winner it is matched
	// against. Default to 0 (exact match required), never negative.
	VolatilityTolerance float64
	LiquidityTolerance  float64

	// PermutationDraws is the number of placebo sign-flip draws used to
	// compute each combination's permutation p-value.
	PermutationDraws int
	// BootstrapSamples is the number of cluster block-bootstrap draws used
	// to compute each combination's cluster confidence interval.
	BootstrapSamples int
	// Seed makes permutation/bootstrap draws reproducible.
	Seed int64
	// FamilyWiseAlpha is the total selection-tax budget shared across every
	// combination in the search space (Bonferroni-style correction). Must
	// be in (0, 1); every searched combination — not just nominated ones —
	// divides this budget.
	FamilyWiseAlpha float64

	// Generation / ProtocolHash are carried through to every nomination for
	// downstream registry binding; they are opaque to this package.
	Generation   string
	ProtocolHash string

	Now           time.Time
	Progress      io.Writer
	ProgressEvery int
}

func normalizeOptions(opts Options) Options {
	if opts.Seed == 0 {
		opts.Seed = defaultSeed
	}
	if opts.PermutationDraws <= 0 {
		opts.PermutationDraws = 2000
	}
	if opts.BootstrapSamples <= 0 {
		opts.BootstrapSamples = 1000
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = 1
	}
	if opts.NeutralBandBPS < 0 {
		opts.NeutralBandBPS = 0
	}
	if opts.VolatilityTolerance < 0 {
		opts.VolatilityTolerance = 0
	}
	if opts.LiquidityTolerance < 0 {
		opts.LiquidityTolerance = 0
	}
	return opts
}

func validateOptions(opts Options) error {
	if len(opts.Events) == 0 {
		return errors.New("backward: events are required")
	}
	if err := opts.SearchSpace.Validate(); err != nil {
		return err
	}
	if opts.ProtocolHash != opts.SearchSpace.ProtocolHash {
		return errors.New("backward: run protocol hash does not match sealed search space")
	}
	if opts.Generation != opts.SearchSpace.Generation {
		return errors.New("backward: run generation does not match sealed search space")
	}
	if len(opts.SearchSpace.Combinations) == 0 {
		return errors.New("backward: search space must be preregistered with at least one combination")
	}
	if opts.MatchWindow <= 0 {
		return errors.New("backward: match window must be positive; unbounded control matching is forbidden")
	}
	if opts.FamilyWiseAlpha <= 0 || opts.FamilyWiseAlpha >= 1 {
		return errors.New("backward: family-wise alpha must be in (0,1)")
	}
	seen := map[string]bool{}
	for _, c := range opts.SearchSpace.Combinations {
		if strings.TrimSpace(c.Key) == "" {
			return errors.New("backward: combination key is required")
		}
		if seen[c.Key] {
			return fmt.Errorf("backward: duplicate combination key %q", c.Key)
		}
		seen[c.Key] = true
		if strings.TrimSpace(c.FeatureKey) == "" {
			return fmt.Errorf("backward: combination %q missing feature key", c.Key)
		}
		if strings.TrimSpace(c.MatchingRule) == "" {
			return fmt.Errorf("backward: combination %q missing matching rule", c.Key)
		}
		if strings.TrimSpace(c.Mechanism) == "" || !c.Direction.IsDirectional() {
			return fmt.Errorf("backward: combination %q requires mechanism and direction", c.Key)
		}
		if c.Op != OpGTE && c.Op != OpLTE {
			return fmt.Errorf("backward: combination %q has invalid operator %q", c.Key, c.Op)
		}
	}
	return nil
}

// Run mines the preregistered search space against Events and returns a
// Report whose only possible outputs are candidate nominations (never a
// pass/approval verdict) and accounted-for rejections. It never mutates or
// consults any registry, exam, or sealed material.
func Run(opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	if err := validateOptions(opts); err != nil {
		return Report{}, err
	}
	eligible, excluded := filterEvents(opts.Events)

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	dataHash, err := HashEvents(eligible)
	if err != nil {
		return Report{}, err
	}

	size := len(opts.SearchSpace.Combinations)
	nominations := make([]Nomination, 0, size)
	rejected := make([]RejectedCombination, 0, size)
	for i, combo := range opts.SearchSpace.Combinations {
		nomination, reason, err := evaluateCombination(eligible, combo, opts.SearchSpace, opts)
		if err != nil {
			return Report{}, err
		}
		if reason != "" {
			rejected = append(rejected, RejectedCombination{Key: combo.Key, Reason: reason})
		} else {
			nomination.DiscoveryDataHash = dataHash
			nomination.SearchSpaceID = opts.SearchSpace.ID
			nomination.SearchSpaceSize = size
			nominations = append(nominations, nomination)
		}
		if opts.Progress != nil && (i+1)%opts.ProgressEvery == 0 {
			fmt.Fprintf(opts.Progress, "search_space=%s combo=%d/%d nominations=%d rejected=%d\n",
				opts.SearchSpace.ID, i+1, size, len(nominations), len(rejected))
		}
	}

	sort.SliceStable(nominations, func(i, j int) bool {
		if nominations[i].PermutationPValue != nominations[j].PermutationPValue {
			return nominations[i].PermutationPValue < nominations[j].PermutationPValue
		}
		return nominations[i].Key < nominations[j].Key
	})
	for i := range nominations {
		nominations[i].Rank = i + 1
	}
	sort.Slice(rejected, func(i, j int) bool { return rejected[i].Key < rejected[j].Key })

	return Report{
		GeneratedAt:         now.UTC(),
		Origin:              OriginBackwardMined,
		SearchSpaceID:       opts.SearchSpace.ID,
		SearchSpaceSize:     size,
		DiscoveryDataHash:   dataHash,
		EventsInput:         len(opts.Events),
		EventsEligible:      len(eligible),
		ExcludedEvents:      excluded,
		FamilyWiseAlpha:     opts.FamilyWiseAlpha,
		PerCombinationAlpha: opts.FamilyWiseAlpha / float64(size),
		Nominations:         nominations,
		Rejected:            rejected,
	}, nil
}

// filterEvents drops any event that cannot safely enter mining: missing
// identity, missing decision-time boundary, a feature snapshot taken after
// decision time (the core leakage rule backward mining must never cross), an
// unidentified payer, or non-finite matching/outcome values. Every drop is
// counted by reason so a report can never quietly explain why it saw fewer
// events than it was given.
func filterEvents(events []Event) ([]Event, map[string]int) {
	excluded := map[string]int{}
	out := make([]Event, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		reason := invalidEventReason(e, seen)
		if reason != "" {
			excluded[reason]++
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out, excluded
}

func invalidEventReason(e Event, seen map[string]bool) string {
	switch {
	case strings.TrimSpace(e.ID) == "":
		return "missing_id"
	case seen[e.ID]:
		return "duplicate_id"
	case strings.TrimSpace(e.Symbol) == "":
		return "missing_symbol"
	case strings.TrimSpace(e.Cluster) == "":
		return "missing_cluster"
	case e.DecisionTime.IsZero():
		return "missing_decision_time"
	case e.FeatureAsOf.IsZero():
		return "missing_feature_as_of"
	case e.FeatureAsOf.After(e.DecisionTime):
		return "post_decision_feature"
	case e.Payer == mechanism.PayerUnknown:
		return "no_payer_mechanism"
	case !e.Direction.IsDirectional():
		return "missing_direction"
	case !isFinite(e.NetOutcomeBPS):
		return "non_finite_outcome"
	case !isFinite(e.Volatility) || !isFinite(e.Liquidity):
		return "non_finite_matching_dimension"
	case len(e.Features) == 0:
		return "missing_pre_decision_features"
	default:
		return ""
	}
}

// registered reports whether combo is byte-for-byte identical to a member of
// space's preregistered combinations. A combination sharing a Key but
// differing in any other field (a bait-and-switch rename of an already
// registered search entry) is not considered registered.
func registered(space SearchSpace, combo Combination) bool {
	for _, c := range space.Combinations {
		if c == combo {
			return true
		}
	}
	return false
}

type pairSample struct {
	WinnerID string
	Cluster  string
	Diff     float64
}

// evaluateCombination scores exactly one preregistered combination. It
// returns either a fully populated Nomination with an empty reason, or a
// zero Nomination with a non-empty rejection reason. err is only non-nil for
// structural failures (e.g. hashing), never for an ordinary "nothing found"
// outcome.
func evaluateCombination(events []Event, combo Combination, space SearchSpace, opts Options) (Nomination, string, error) {
	if !registered(space, combo) {
		return Nomination{}, ReasonUnregisteredSearchSpace, nil
	}

	var treatment []Event
	winnerCount := 0
	treatmentLossCount := 0
	treatmentNeutralCount := 0
	for _, e := range events {
		if e.Direction != combo.Direction {
			continue
		}
		fires, hasFeature := combo.Fires(e)
		if !hasFeature || !fires {
			continue
		}
		treatment = append(treatment, e)
		switch classify(e.NetOutcomeBPS, opts.NeutralBandBPS) {
		case OutcomeWinner:
			winnerCount++
		case OutcomeLoss:
			treatmentLossCount++
		case OutcomeNeutral:
			treatmentNeutralCount++
		}
	}
	if winnerCount == 0 {
		return Nomination{}, ReasonNoWinners, nil
	}

	payer := treatment[0].Payer
	for _, event := range treatment {
		if event.Payer == mechanism.PayerUnknown || event.Payer != payer {
			return Nomination{}, ReasonNoPayerMechanism, nil
		}
	}

	var pairs []pairSample
	controlIDs := map[string]bool{}
	lossIDs := map[string]bool{}
	neutralIDs := map[string]bool{}
	usedControlIDs := map[string]bool{}
	var symbol string
	consistentSymbol := true
	for _, treated := range treatment {
		if symbol == "" {
			symbol = treated.Symbol
		} else if symbol != treated.Symbol {
			consistentSymbol = false
		}
		controls := matchedControls(events, treated, combo, opts, usedControlIDs)
		if len(controls) == 0 {
			continue
		}
		// One-to-one nearest matching: a control cannot be reused by another
		// treated event, preserving pair independence within each shock cluster.
		controls = controls[:1]
		var sum float64
		for _, c := range controls {
			sum += c.NetOutcomeBPS
			controlIDs[c.ID] = true
			usedControlIDs[c.ID] = true
			switch classify(c.NetOutcomeBPS, opts.NeutralBandBPS) {
			case OutcomeLoss:
				lossIDs[c.ID] = true
			case OutcomeNeutral:
				neutralIDs[c.ID] = true
			}
		}
		avgControl := sum / float64(len(controls))
		pairs = append(pairs, pairSample{WinnerID: treated.ID, Cluster: treated.Cluster, Diff: treated.NetOutcomeBPS - avgControl})
	}
	if len(pairs) == 0 {
		return Nomination{}, ReasonOnlyWinnerNoControls, nil
	}
	if len(lossIDs)+len(neutralIDs) == 0 {
		return Nomination{}, ReasonOnlyWinnerNoControls, nil
	}

	diffs := make([]float64, len(pairs))
	for i, p := range pairs {
		diffs[i] = p.Diff
	}
	seedOffset := comboSeedOffset(combo.Key)
	permRng := rand.New(rand.NewSource(opts.Seed + seedOffset))
	pValue := pairedPermutationPValue(diffs, opts.PermutationDraws, permRng)

	clusterCount := distinctClusters(pairs)
	valid := clusterCount >= minIndependentClusters
	var ci ConfidenceInterval
	if valid {
		ci = percentileCI(clusterBlockBootstrap(pairs, opts.BootstrapSamples, opts.Seed+seedOffset+1))
	}

	alpha := opts.FamilyWiseAlpha / float64(len(space.Combinations))
	survives := valid && ci.Low > 0 && pValue <= alpha
	if !survives {
		return Nomination{}, ReasonSelectionTaxFailed, nil
	}

	repSymbol := ""
	if consistentSymbol {
		repSymbol = symbol
	}

	return Nomination{
		Key:                   combo.Key,
		Origin:                OriginBackwardMined,
		Kind:                  KindCandidateNomination,
		SearchSpaceID:         space.ID,
		MatchingRule:          combo.MatchingRule,
		Symbol:                repSymbol,
		TreatmentCount:        len(treatment),
		WinnerCount:           winnerCount,
		TreatmentLossCount:    treatmentLossCount,
		TreatmentNeutralCount: treatmentNeutralCount,
		ControlCount:          len(controlIDs),
		LossControlCount:      len(lossIDs),
		NeutralControlCount:   len(neutralIDs),
		Payer:                 string(payer),
		Mechanism:             combo.Mechanism,
		Direction:             string(combo.Direction),
		NetEffectMeanBPS:      mean(diffs),
		PermutationPValue:     pValue,
		PermutationDraws:      opts.PermutationDraws,
		ClusterCount:          clusterCount,
		ClusterBootstrapValid: valid,
		ClusterBootstrapCI95:  ci,
		SelectionTaxAlpha:     alpha,
		SurvivesSelectionTax:  survives,
		Generation:            opts.Generation,
		ProtocolHash:          opts.ProtocolHash,
	}, "", nil
}

// matchedControls finds every event that could serve as a matched loss or
// neutral counterfactual for winner: same symbol, within the configured time
// window, and within the configured volatility/liquidity tolerance — but
// classified anything other than winner. This is intentionally symmetric
// over the whole eligible pool, not just events that fired the combination's
// rule, so a control need not "almost win" to count.
func matchedControls(all []Event, treated Event, combo Combination, opts Options, used map[string]bool) []Event {
	var out []Event
	for _, c := range all {
		if c.ID == treated.ID || used[c.ID] || c.Symbol != treated.Symbol ||
			c.Direction != treated.Direction || c.Cluster != treated.Cluster {
			continue
		}
		if fires, hasFeature := combo.Fires(c); hasFeature && fires {
			continue
		}
		dt := c.DecisionTime.Sub(treated.DecisionTime)
		if dt < 0 {
			dt = -dt
		}
		if dt > opts.MatchWindow {
			continue
		}
		if math.Abs(c.Volatility-treated.Volatility) > opts.VolatilityTolerance {
			continue
		}
		if math.Abs(c.Liquidity-treated.Liquidity) > opts.LiquidityTolerance {
			continue
		}
		if classify(c.NetOutcomeBPS, opts.NeutralBandBPS) == OutcomeWinner {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].DecisionTime.Sub(treated.DecisionTime)
		if left < 0 {
			left = -left
		}
		right := out[j].DecisionTime.Sub(treated.DecisionTime)
		if right < 0 {
			right = -right
		}
		if left == right {
			return out[i].ID < out[j].ID
		}
		return left < right
	})
	return out
}

func classify(net, band float64) OutcomeClass {
	switch {
	case net > band:
		return OutcomeWinner
	case net < -band:
		return OutcomeLoss
	default:
		return OutcomeNeutral
	}
}

func distinctClusters(pairs []pairSample) int {
	set := map[string]bool{}
	for _, p := range pairs {
		if strings.TrimSpace(p.Cluster) == "" {
			continue
		}
		set[p.Cluster] = true
	}
	return len(set)
}

func HashEvents(events []Event) (string, error) {
	sorted := append([]Event(nil), events...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	hash, _, err := datatruth.HashJSON(sorted)
	return hash, err
}
