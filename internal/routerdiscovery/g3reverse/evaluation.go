package g3reverse

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg3/protocol"
)

const (
	OriginReverseInduction  = "outcome_first_reverse_induction"
	KindCandidateNomination = "CANDIDATE_NOMINATION"

	DiscoveryReceiptSchemaVersion = "lingxi.router-g3-discovery-receipt.v1"
	DiscoveryReceiptGeneratorID   = "router-g3-discovery-v1"
)

const (
	ReasonUnregisteredRule      = "unregistered_rule"
	ReasonNoWinners             = "no_winners"
	ReasonOnlyWinnerNoControls  = "only_winner_no_matched_controls"
	ReasonSelectionTaxFailed    = "selection_tax_failed"
	ReasonPostDecisionFeature   = "post_decision_feature"
	ReasonMissingID             = "missing_id"
	ReasonDuplicateID           = "duplicate_id"
	ReasonMissingStratification = "missing_stratification"
	ReasonNonFiniteOutcome      = "non_finite_outcome"
)

// EvaluationOptions configures one induction-partition rule evaluation run.
type EvaluationOptions struct {
	Events                []Event
	Rules                 []Rule
	Manifest              SearchManifest
	Ledger                AlphaLedger
	NeutralBandBPS        float64
	MatchWindow           time.Duration
	PermutationDraws      int
	BootstrapSamples      int
	ExactPermutationMax   int
	Seed                  int64
	ReverseInductionAlpha float64
	Generation            string
	ProtocolHash          string
	Now                   time.Time
	Progress              io.Writer
	ProgressEvery         int
}

// Nomination is the sole successful output of induction evaluation.
type Nomination struct {
	RuleOrdinal            int                `json:"rule_ordinal"`
	CanonicalRuleHash      string             `json:"canonical_rule_hash"`
	Origin                 string             `json:"origin"`
	Kind                   string             `json:"kind"`
	InductionDataHash      string             `json:"induction_data_hash"`
	SearchManifestHash     string             `json:"search_manifest_hash"`
	SearchSpaceID          string             `json:"search_space_id"`
	ActualDerivedRuleCount int                `json:"actual_derived_rule_count"`
	Rank                   int                `json:"rank"`
	MatchingRule           string             `json:"matching_rule"`
	SkyState               string             `json:"sky_state"`
	SorterDomain           string             `json:"sorter_domain"`
	Horizon                Horizon            `json:"horizon"`
	Symbol                 string             `json:"symbol,omitempty"`
	LiquidityBucket        string             `json:"liquidity_bucket,omitempty"`
	Direction              string             `json:"direction"`
	TreatmentCount         int                `json:"treatment_count"`
	WinnerCount            int                `json:"winner_count"`
	TreatmentLossCount     int                `json:"treatment_loss_count"`
	TreatmentNeutralCount  int                `json:"treatment_neutral_count"`
	ControlCount           int                `json:"control_count"`
	WinnerControlCount     int                `json:"winner_control_count"`
	LossControlCount       int                `json:"loss_control_count"`
	NeutralControlCount    int                `json:"neutral_control_count"`
	PairCount              int                `json:"pair_count"`
	NetEffectMeanBPS       float64            `json:"net_effect_mean_bps"`
	PermutationPValue      float64            `json:"permutation_p_value"`
	PermutationDraws       int                `json:"permutation_draws"`
	ClusterCount           int                `json:"cluster_count"`
	ClusterBootstrapValid  bool               `json:"cluster_bootstrap_valid"`
	ClusterBootstrapCI95   ConfidenceInterval `json:"cluster_bootstrap_ci95"`
	SelectionTaxAlpha      float64            `json:"selection_tax_alpha"`
	SurvivesSelectionTax   bool               `json:"survives_selection_tax"`
	Generation             string             `json:"generation"`
	ProtocolHash           string             `json:"protocol_hash"`
}

// RejectedRule records why a derived rule did not nominate.
type RejectedRule struct {
	RuleOrdinal       int    `json:"rule_ordinal"`
	CanonicalRuleHash string `json:"canonical_rule_hash"`
	Reason            string `json:"reason"`
}

// ControlRuleSummary summarizes matched-control construction for one rule.
type ControlRuleSummary struct {
	RuleOrdinal           int    `json:"rule_ordinal"`
	CanonicalRuleHash     string `json:"canonical_rule_hash"`
	TreatmentCount        int    `json:"treatment_count"`
	WinnerCount           int    `json:"winner_count"`
	TreatmentLossCount    int    `json:"treatment_loss_count"`
	TreatmentNeutralCount int    `json:"treatment_neutral_count"`
	PairCount             int    `json:"pair_count"`
	ControlCount          int    `json:"control_count"`
	WinnerControlCount    int    `json:"winner_control_count"`
	LossControlCount      int    `json:"loss_control_count"`
	NeutralControlCount   int    `json:"neutral_control_count"`
	ClusterCount          int    `json:"cluster_count"`
	ReusedControlRejected bool   `json:"reused_control_rejected"`
}

// InductionEvaluationReport is the full induction evaluation receipt.
type InductionEvaluationReport struct {
	GeneratedAt            time.Time      `json:"generated_at"`
	Origin                 string         `json:"origin"`
	Partition              string         `json:"partition"`
	InductionDataHash      string         `json:"induction_data_hash"`
	SearchManifestHash     string         `json:"search_manifest_hash"`
	SearchSpaceID          string         `json:"search_space_id"`
	ActualDerivedRuleCount int            `json:"actual_derived_rule_count"`
	EventsInput            int            `json:"events_input"`
	EventsEligible         int            `json:"events_eligible"`
	ExcludedEvents         map[string]int `json:"excluded_events,omitempty"`
	ReverseInductionAlpha  float64        `json:"reverse_induction_alpha"`
	PerRuleAlpha           float64        `json:"per_rule_alpha"`
	Nominations            []Nomination   `json:"nominations,omitempty"`
	RejectedRules          []RejectedRule `json:"rejected_rules,omitempty"`
	ExamActions            int            `json:"exam_actions"`
}

// ControlSummary binds per-rule control accounting for audit.
type ControlSummary struct {
	GeneratedAt        time.Time            `json:"generated_at"`
	InductionDataHash  string               `json:"induction_data_hash"`
	SearchManifestHash string               `json:"search_manifest_hash"`
	MatchingRule       string               `json:"matching_rule"`
	Rules              []ControlRuleSummary `json:"rules"`
	ExamActions        int                  `json:"exam_actions"`
}

// DiscoveryReceipt is the signed terminal commitment over every discovery
// input manifest and canonical JSON output artifact.
type DiscoveryReceipt struct {
	SchemaVersion           string `json:"schema_version"`
	GeneratorID             string `json:"generator_id"`
	Generation              string `json:"generation"`
	ProtocolHash            string `json:"protocol_hash"`
	EventManifestHash       string `json:"event_manifest_hash"`
	SearchManifestHash      string `json:"search_manifest_hash"`
	InductionEvaluationHash string `json:"induction_evaluation_hash"`
	RecheckReportHash       string `json:"recheck_report_hash"`
	NominationsHash         string `json:"nominations_hash"`
	ControlSummaryHash      string `json:"control_summary_hash"`
	ExamActions             int    `json:"exam_actions"`
	ReceiptHash             string `json:"receipt_hash"`
	KeyID                   string `json:"key_id"`
	Signature               string `json:"signature"`
}

// DiscoveryReceiptBindings supplies canonical JSON file hashes to the receipt.
type DiscoveryReceiptBindings struct {
	EventManifestHash       string
	SearchManifestHash      string
	InductionEvaluationHash string
	RecheckReportHash       string
	NominationsHash         string
	ControlSummaryHash      string
}

// NewDiscoveryReceipt constructs and seals the frozen G3 discovery receipt.
func NewDiscoveryReceipt(bindings DiscoveryReceiptBindings) (DiscoveryReceipt, error) {
	receipt := DiscoveryReceipt{
		SchemaVersion:           DiscoveryReceiptSchemaVersion,
		GeneratorID:             DiscoveryReceiptGeneratorID,
		Generation:              protocol.Generation,
		ProtocolHash:            protocol.RouterProtocolHash,
		EventManifestHash:       bindings.EventManifestHash,
		SearchManifestHash:      bindings.SearchManifestHash,
		InductionEvaluationHash: bindings.InductionEvaluationHash,
		RecheckReportHash:       bindings.RecheckReportHash,
		NominationsHash:         bindings.NominationsHash,
		ControlSummaryHash:      bindings.ControlSummaryHash,
		ExamActions:             protocol.ExamActions,
	}
	return SealDiscoveryReceipt(receipt)
}

// SealDiscoveryReceipt hashes unsigned receipt content canonically.
func SealDiscoveryReceipt(receipt DiscoveryReceipt) (DiscoveryReceipt, error) {
	receipt.KeyID = ""
	receipt.Signature = ""
	receipt.ReceiptHash = ""
	if err := receipt.validateContent(); err != nil {
		return DiscoveryReceipt{}, err
	}
	hash, _, err := datatruth.HashJSON(receipt)
	if err != nil {
		return DiscoveryReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

// SignDiscoveryReceipt HMAC-signs a freshly sealed receipt hash.
func SignDiscoveryReceipt(receipt DiscoveryReceipt, key []byte, keyID string) (DiscoveryReceipt, error) {
	if len(key) < 32 || strings.TrimSpace(keyID) == "" {
		return DiscoveryReceipt{}, errors.New("g3reverse: discovery receipt signing key >=32 bytes and key id are required")
	}
	sealed, err := SealDiscoveryReceipt(receipt)
	if err != nil {
		return DiscoveryReceipt{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(sealed.ReceiptHash))
	sealed.KeyID = strings.TrimSpace(keyID)
	sealed.Signature = hex.EncodeToString(mac.Sum(nil))
	return sealed, nil
}

// VerifyDiscoveryReceipt fails closed on content, hash, signature, or key drift.
func VerifyDiscoveryReceipt(receipt DiscoveryReceipt, key []byte) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if len(key) < 32 || strings.TrimSpace(receipt.KeyID) == "" || receipt.Signature == "" {
		return errors.New("g3reverse: signed discovery receipt and key >=32 bytes are required")
	}
	expected, err := hex.DecodeString(receipt.Signature)
	if err != nil {
		return errors.New("g3reverse: discovery receipt signature is invalid hex")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(receipt.ReceiptHash))
	if !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("g3reverse: discovery receipt signature mismatch")
	}
	return nil
}

// Validate checks frozen bindings and the canonical receipt hash.
func (receipt DiscoveryReceipt) Validate() error {
	if err := receipt.validateContent(); err != nil {
		return err
	}
	want := receipt.ReceiptHash
	sealed, err := SealDiscoveryReceipt(receipt)
	if err != nil {
		return err
	}
	if want == "" || sealed.ReceiptHash != want {
		return errors.New("g3reverse: discovery receipt hash mismatch")
	}
	return nil
}

func (receipt DiscoveryReceipt) validateContent() error {
	if receipt.SchemaVersion != DiscoveryReceiptSchemaVersion ||
		receipt.GeneratorID != DiscoveryReceiptGeneratorID ||
		receipt.Generation != protocol.Generation ||
		receipt.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("g3reverse: discovery receipt protocol binding mismatch")
	}
	if receipt.EventManifestHash == "" || receipt.SearchManifestHash == "" ||
		receipt.InductionEvaluationHash == "" || receipt.RecheckReportHash == "" ||
		receipt.NominationsHash == "" || receipt.ControlSummaryHash == "" {
		return errors.New("g3reverse: discovery receipt bindings are incomplete")
	}
	if receipt.ExamActions != protocol.ExamActions {
		return errors.New("g3reverse: discovery receipt exam_actions must be zero")
	}
	return nil
}

func normalizeEvaluationOptions(opts EvaluationOptions) EvaluationOptions {
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = 1
	}
	return opts
}

func validateEvaluationOptions(opts EvaluationOptions) error {
	if err := opts.Manifest.Validate(); err != nil {
		return err
	}
	if err := opts.Ledger.Validate(); err != nil {
		return err
	}
	if opts.Generation != opts.Manifest.Generation || opts.ProtocolHash != opts.Manifest.ProtocolHash {
		return errors.New("g3reverse: evaluation generation/protocol must match search manifest")
	}
	if opts.Generation != protocol.Generation || opts.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("g3reverse: evaluation must use frozen G3 protocol")
	}
	rulesHash, err := HashRules(opts.Rules)
	if err != nil {
		return err
	}
	if rulesHash != opts.Manifest.CanonicalDerivedRulesHash {
		return errors.New("g3reverse: rules hash does not match search manifest")
	}
	if opts.Ledger.LedgerHash != opts.Manifest.AlphaLedgerHash {
		return errors.New("g3reverse: ledger hash does not match search manifest")
	}
	if len(opts.Rules) != opts.Manifest.ActualDerivedRuleCount {
		return errors.New("g3reverse: actual derived rule count mismatch")
	}
	registered := 0
	for _, entry := range opts.Ledger.Entries {
		if entry.Status == LedgerStatusRegistered {
			registered++
		}
	}
	if registered != len(opts.Rules) {
		return errors.New("g3reverse: registered ledger count must equal rules")
	}
	if opts.PermutationDraws != protocol.ReverseInductionPermutationDraws ||
		opts.ExactPermutationMax != protocol.ReverseInductionExactPermMax ||
		opts.BootstrapSamples != protocol.ReverseInductionBootstrapSamples ||
		opts.ReverseInductionAlpha != protocol.ReverseInductionAlpha ||
		opts.MatchWindow != time.Duration(protocol.ReverseInductionMatchWindowMs)*time.Millisecond ||
		opts.NeutralBandBPS != protocol.ReverseInductionNeutralBandBPS {
		return errors.New("g3reverse: evaluation statistics must equal frozen production protocol")
	}
	return nil
}

// EvaluateInduction scores every sealed derived rule on the full induction
// event pool. Output is nominations or accounted rejections only.
func EvaluateInduction(opts EvaluationOptions) (InductionEvaluationReport, ControlSummary, error) {
	opts = normalizeEvaluationOptions(opts)
	if err := validateEvaluationOptions(opts); err != nil {
		return InductionEvaluationReport{}, ControlSummary{}, err
	}
	eligible, excluded := filterEvaluationEvents(opts.Events)
	dataHash, err := HashEvents(eligible)
	if err != nil {
		return InductionEvaluationReport{}, ControlSummary{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	perRuleAlpha := 0.0
	if len(opts.Rules) > 0 {
		perRuleAlpha = opts.ReverseInductionAlpha / float64(len(opts.Rules))
	}
	nominations := make([]Nomination, 0)
	rejected := make([]RejectedRule, 0, len(opts.Rules))
	controlRules := make([]ControlRuleSummary, 0, len(opts.Rules))
	for i, rule := range opts.Rules {
		nomination, summary, reason, err := evaluateRule(eligible, rule, opts, dataHash, perRuleAlpha)
		if err != nil {
			return InductionEvaluationReport{}, ControlSummary{}, err
		}
		controlRules = append(controlRules, summary)
		if reason != "" {
			rejected = append(rejected, RejectedRule{
				RuleOrdinal: rule.Ordinal, CanonicalRuleHash: rule.CanonicalHash, Reason: reason,
			})
		} else {
			nominations = append(nominations, nomination)
		}
		if opts.Progress != nil && (i+1)%opts.ProgressEvery == 0 {
			fmt.Fprintf(opts.Progress, "rules=%d/%d nominations=%d rejected=%d\n",
				i+1, len(opts.Rules), len(nominations), len(rejected))
		}
	}
	sort.SliceStable(nominations, func(i, j int) bool {
		if nominations[i].PermutationPValue != nominations[j].PermutationPValue {
			return nominations[i].PermutationPValue < nominations[j].PermutationPValue
		}
		return nominations[i].CanonicalRuleHash < nominations[j].CanonicalRuleHash
	})
	for i := range nominations {
		nominations[i].Rank = i + 1
	}
	sort.Slice(rejected, func(i, j int) bool {
		if rejected[i].RuleOrdinal != rejected[j].RuleOrdinal {
			return rejected[i].RuleOrdinal < rejected[j].RuleOrdinal
		}
		return rejected[i].CanonicalRuleHash < rejected[j].CanonicalRuleHash
	})
	report := InductionEvaluationReport{
		GeneratedAt:            now.UTC(),
		Origin:                 OriginReverseInduction,
		Partition:              string(PartitionInduction),
		InductionDataHash:      dataHash,
		SearchManifestHash:     opts.Manifest.ManifestHash,
		SearchSpaceID:          opts.Manifest.SearchSpaceID,
		ActualDerivedRuleCount: len(opts.Rules),
		EventsInput:            len(opts.Events),
		EventsEligible:         len(eligible),
		ExcludedEvents:         excluded,
		ReverseInductionAlpha:  opts.ReverseInductionAlpha,
		PerRuleAlpha:           perRuleAlpha,
		Nominations:            nominations,
		RejectedRules:          rejected,
		ExamActions:            protocol.ExamActions,
	}
	controlSummary := ControlSummary{
		GeneratedAt:        now.UTC(),
		InductionDataHash:  dataHash,
		SearchManifestHash: opts.Manifest.ManifestHash,
		MatchingRule:       protocol.ReverseInductionMatchRule,
		Rules:              controlRules,
		ExamActions:        protocol.ExamActions,
	}
	return report, controlSummary, nil
}

func filterEvaluationEvents(events []Event) ([]Event, map[string]int) {
	excluded := map[string]int{}
	out := make([]Event, 0, len(events))
	seen := map[string]bool{}
	for _, event := range events {
		reason := invalidEvaluationEventReason(event, seen)
		if reason != "" {
			excluded[reason]++
			continue
		}
		seen[event.ID] = true
		out = append(out, event)
	}
	return out, excluded
}

func invalidEvaluationEventReason(event Event, seen map[string]bool) string {
	switch {
	case strings.TrimSpace(event.ID) == "":
		return ReasonMissingID
	case seen[event.ID]:
		return ReasonDuplicateID
	case strings.TrimSpace(event.Symbol) == "" || strings.TrimSpace(event.Cluster) == "" ||
		strings.TrimSpace(event.SkyState) == "" || strings.TrimSpace(event.SorterDomain) == "" ||
		strings.TrimSpace(event.LiquidityBucket) == "":
		return ReasonMissingStratification
	case !event.Horizon.Valid():
		return "invalid_horizon"
	case event.DecisionTime.IsZero() || event.FeatureAsOf.IsZero():
		return "missing_decision_time"
	case event.FeatureAsOf.After(event.DecisionTime):
		return ReasonPostDecisionFeature
	case !event.Direction.IsDirectional():
		return "missing_direction"
	case !isFinite(event.NetOutcomeBPS):
		return ReasonNonFiniteOutcome
	default:
		return ""
	}
}

func evaluateRule(
	events []Event,
	rule Rule,
	opts EvaluationOptions,
	dataHash string,
	perRuleAlpha float64,
) (Nomination, ControlRuleSummary, string, error) {
	summary := ControlRuleSummary{RuleOrdinal: rule.Ordinal, CanonicalRuleHash: rule.CanonicalHash}
	if err := ValidateRule(rule); err != nil {
		return Nomination{}, summary, ReasonUnregisteredRule, nil
	}
	if rule.CanonicalHash == "" {
		return Nomination{}, summary, ReasonUnregisteredRule, nil
	}
	registered := false
	for _, entry := range opts.Ledger.Entries {
		if entry.Status == LedgerStatusRegistered && entry.CanonicalRuleHash == rule.CanonicalHash {
			registered = true
			break
		}
	}
	if !registered {
		return Nomination{}, summary, ReasonUnregisteredRule, nil
	}

	var treatment []Event
	winnerCount := 0
	treatmentLossCount := 0
	treatmentNeutralCount := 0
	for _, event := range events {
		fires, hasFeature := rule.Fires(event)
		if !hasFeature || !fires {
			continue
		}
		treatment = append(treatment, event)
		switch classifyOutcome(event.NetOutcomeBPS, opts.NeutralBandBPS) {
		case OutcomeWinner:
			winnerCount++
		case OutcomeLoss:
			treatmentLossCount++
		case OutcomeNeutral:
			treatmentNeutralCount++
		}
	}
	summary.TreatmentCount = len(treatment)
	summary.WinnerCount = winnerCount
	summary.TreatmentLossCount = treatmentLossCount
	summary.TreatmentNeutralCount = treatmentNeutralCount
	if winnerCount == 0 {
		return Nomination{}, summary, ReasonNoWinners, nil
	}

	var pairs []pairSample
	controlIDs := map[string]bool{}
	winnerControlIDs := map[string]bool{}
	lossIDs := map[string]bool{}
	neutralIDs := map[string]bool{}
	usedControlIDs := map[string]bool{}
	var symbol, liquidityBucket string
	consistentSymbol := true
	consistentLiquidity := true
	for _, treated := range treatment {
		if symbol == "" {
			symbol = treated.Symbol
		} else if symbol != treated.Symbol {
			consistentSymbol = false
		}
		if liquidityBucket == "" {
			liquidityBucket = treated.LiquidityBucket
		} else if liquidityBucket != treated.LiquidityBucket {
			consistentLiquidity = false
		}
		controls := matchedControlsForRule(events, treated, rule, opts, usedControlIDs)
		if len(controls) == 0 {
			if len(matchedControlsForRule(events, treated, rule, opts, map[string]bool{})) > 0 {
				summary.ReusedControlRejected = true
			}
			continue
		}
		controls = controls[:1]
		control := controls[0]
		if usedControlIDs[control.ID] {
			summary.ReusedControlRejected = true
			continue
		}
		controlIDs[control.ID] = true
		usedControlIDs[control.ID] = true
		switch classifyOutcome(control.NetOutcomeBPS, opts.NeutralBandBPS) {
		case OutcomeWinner:
			winnerControlIDs[control.ID] = true
		case OutcomeLoss:
			lossIDs[control.ID] = true
		case OutcomeNeutral:
			neutralIDs[control.ID] = true
		}
		pairs = append(pairs, pairSample{
			TreatedID: treated.ID, ControlID: control.ID, Cluster: treated.Cluster,
			Diff: treated.NetOutcomeBPS - control.NetOutcomeBPS,
		})
	}
	summary.PairCount = len(pairs)
	summary.ControlCount = len(controlIDs)
	summary.WinnerControlCount = len(winnerControlIDs)
	summary.LossControlCount = len(lossIDs)
	summary.NeutralControlCount = len(neutralIDs)
	summary.ClusterCount = distinctClusters(pairs)
	if len(pairs) == 0 {
		return Nomination{}, summary, ReasonOnlyWinnerNoControls, nil
	}

	diffs := make([]float64, len(pairs))
	for i, pair := range pairs {
		diffs[i] = pair.Diff
	}
	seedOffset := ruleSeedOffset(rule.CanonicalHash)
	permRng := rand.New(rand.NewSource(opts.Seed + seedOffset))
	pValue := pairedPermutationPValue(diffs, opts.PermutationDraws, permRng, opts.ExactPermutationMax)
	clusterCount := distinctClusters(pairs)
	valid := clusterCount >= protocol.MinInductionClusters
	var ci ConfidenceInterval
	if valid {
		ci = percentileCI(clusterBlockBootstrap(pairs, opts.BootstrapSamples, opts.Seed+seedOffset+1))
	}
	survives := valid && ci.Low > 0 && pValue <= perRuleAlpha
	if !survives {
		return Nomination{}, summary, ReasonSelectionTaxFailed, nil
	}

	repSymbol, repLiquidity := "", ""
	if consistentSymbol {
		repSymbol = symbol
	}
	if consistentLiquidity {
		repLiquidity = liquidityBucket
	}
	return Nomination{
		RuleOrdinal: rule.Ordinal, CanonicalRuleHash: rule.CanonicalHash,
		Origin: OriginReverseInduction, Kind: KindCandidateNomination,
		InductionDataHash: dataHash, SearchManifestHash: opts.Manifest.ManifestHash,
		SearchSpaceID:          opts.Manifest.SearchSpaceID,
		ActualDerivedRuleCount: len(opts.Rules),
		MatchingRule:           protocol.ReverseInductionMatchRule,
		SkyState:               rule.SkyState, SorterDomain: rule.SorterDomain,
		Horizon: rule.Horizon, Symbol: repSymbol, LiquidityBucket: repLiquidity,
		Direction:      string(rule.Direction),
		TreatmentCount: len(treatment), WinnerCount: winnerCount,
		TreatmentLossCount: treatmentLossCount, TreatmentNeutralCount: treatmentNeutralCount,
		ControlCount: len(controlIDs), WinnerControlCount: len(winnerControlIDs),
		LossControlCount:    len(lossIDs),
		NeutralControlCount: len(neutralIDs), PairCount: len(pairs),
		NetEffectMeanBPS: meanFloats(diffs), PermutationPValue: pValue,
		PermutationDraws: opts.PermutationDraws, ClusterCount: clusterCount,
		ClusterBootstrapValid: valid, ClusterBootstrapCI95: ci,
		SelectionTaxAlpha: perRuleAlpha, SurvivesSelectionTax: survives,
		Generation: opts.Generation, ProtocolHash: opts.ProtocolHash,
	}, summary, "", nil
}

func matchedControlsForRule(
	all []Event,
	treated Event,
	rule Rule,
	opts EvaluationOptions,
	used map[string]bool,
) []Event {
	var out []Event
	for _, candidate := range all {
		if candidate.ID == treated.ID || used[candidate.ID] {
			continue
		}
		if candidate.SkyState != treated.SkyState || candidate.SorterDomain != treated.SorterDomain ||
			candidate.Symbol != treated.Symbol || candidate.LiquidityBucket != treated.LiquidityBucket ||
			candidate.Direction != treated.Direction || candidate.Horizon != treated.Horizon ||
			candidate.Cluster != treated.Cluster {
			continue
		}
		if fires, hasFeature := rule.Fires(candidate); hasFeature && fires {
			continue
		}
		dt := candidate.DecisionTime.Sub(treated.DecisionTime)
		if dt < 0 {
			dt = -dt
		}
		if dt > opts.MatchWindow {
			continue
		}
		out = append(out, candidate)
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
