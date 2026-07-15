package pathvalidation

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
)

// Config drives one end-to-end validation run.
type Config struct {
	Root     string
	Progress io.Writer
	// Limit caps the number of prepared rows consumed per partition, for
	// fast smoke/canary runs. Zero means "no limit" (the full partition).
	Limit int
}

// leafKey identifies one candidate path across the whole run.
type leafKey struct {
	Owner  string
	LeafID string
}

// LeafRuntime accumulates everything needed to score one candidate leaf.
type leafRuntime struct {
	owner          string
	leafIndex      int
	leaf           modelapi.CandidateLeaf
	validationRows []PathRow
	leafWFRows     []PathRow
}

// Report is the atomic, self-contained output of one validation run.
type Report struct {
	SchemaVersion          string            `json:"schema_version"`
	ValidationOwner        string            `json:"validation_owner"`
	GeneratedAt            string            `json:"generated_at"`
	SealedManifestPath     string            `json:"sealed_manifest_path"`
	SealedManifestByteHash string            `json:"sealed_manifest_byte_hash"`
	SealedBundleByteHash   string            `json:"sealed_bundle_byte_hash"`
	TournamentCodeHash     string            `json:"tournament_code_hash"`
	DatasetManifestHash    string            `json:"dataset_manifest_hash"`
	DatasetReviewerHash    string            `json:"dataset_reviewer_receipt_hash"`
	G9ReviewerHash         string            `json:"g9_reviewer_receipt_hash"`
	G10ReviewerHash        string            `json:"g10_reviewer_receipt_hash"`
	FundingManifestHash    string            `json:"funding_manifest_hash"`
	FundingReceiptHash     string            `json:"funding_receipt_hash"`
	PolicyHashes           map[string]string `json:"policy_hashes"`
	LedgerHashes           map[string]string `json:"attempt_ledger_hashes"`
	SharedLedgerHash       string            `json:"shared_attempt_ledger_hash"`
	ProtocolHashes         map[string]string `json:"protocol_hashes"`
	ProtocolChainVerified  []string          `json:"protocol_chain_verified"`
	RouterHoldoutUnblinded bool              `json:"router_holdout_unblinded"`
	TradeAllowed           bool              `json:"trade_allowed"`
	Owners                 []OwnerReport     `json:"owners"`
	ValidationFamilySize   int               `json:"validation_family_size"`
	LeafWFFamilySize       int               `json:"leaf_wf_family_size"`
}

type OwnerReport struct {
	Owner                string             `json:"owner"`
	PolicyID             string             `json:"policy_id"`
	ReplayFamily         string             `json:"replay_family"`
	ForbiddenTokenLeaves []string           `json:"forbidden_token_leaves,omitempty"`
	ValidationCoverage   ActivationCoverage `json:"validation_coverage"`
	LeafWFCoverage       ActivationCoverage `json:"leaf_wf_coverage"`
	Paths                []PathVerdict      `json:"paths"`
}

// Run performs the complete, independent validation pass: hash verification,
// validation/leaf_wf-only dataset loading, per-owner predicate replay, g10
// counterfactual/account/cost reward replay across all delays and the 1.5x
// cost-stress scenario, g1 robustness gating with BH-FDR (validation) and
// Holm (leaf_wf confirmation) multiple-comparison correction, and inactivity
// detection. It never reads router_holdout and never decodes the discovery
// label bundle's content.
func Run(ctx context.Context, cfg Config) (Report, error) {
	progress(cfg.Progress, "stage=verify_sealed status=started")
	sealed, err := VerifySealed(cfg.Root)
	if err != nil {
		return Report{}, fmt.Errorf("sealed verification failed: %w", err)
	}
	progress(cfg.Progress, "stage=verify_sealed status=complete owners=%d", len(sealed.Policies))

	progress(cfg.Progress, "stage=load_partitions status=started")
	partitionRows, err := LoadPartitionRows(cfg.Root, sealed.DatasetManifest, []string{"validation", "leaf_wf"})
	if err != nil {
		return Report{}, err
	}
	for _, partition := range []string{"validation", "leaf_wf"} {
		progress(cfg.Progress, "stage=load_partitions partition=%s rows=%d", partition, len(partitionRows[partition]))
	}

	funding, _, err := counterfactual.LoadFunding(cfg.Root)
	if err != nil {
		return Report{}, err
	}

	barCache := map[string]map[int64]counterfactual.Bar{}
	leaves := map[leafKey]*leafRuntime{}
	coverage := map[string]map[string]*coverageAccumulator{} // owner -> partition -> acc
	for _, owner := range OwnerIDs {
		coverage[owner] = map[string]*coverageAccumulator{
			"validation": {}, "leaf_wf": {},
		}
		for leafIndex, leaf := range sealed.Policies[owner].Leaves {
			key := leafKey{Owner: owner, LeafID: leaf.LeafID}
			leaves[key] = &leafRuntime{owner: owner, leafIndex: leafIndex, leaf: leaf}
		}
	}

	regimeBlocks := map[string]map[int64]int{}
	for _, partition := range []string{"validation", "leaf_wf"} {
		btcByMS := map[int64][2]float64{}
		for _, row := range partitionRows[partition] {
			if row.MissingBits&(uint64(1)<<10) == 0 && row.MissingBits&(uint64(1)<<11) == 0 {
				btcByMS[row.DecisionMS] = [2]float64{row.Values[10], row.Values[11]}
			}
		}
		regimeBlocks[partition] = AssignRegimeBlocks(btcByMS)
	}

	for _, partition := range []string{"validation", "leaf_wf"} {
		rows := partitionRows[partition]
		processed := 0
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			if cfg.Limit > 0 && processed >= cfg.Limit {
				break
			}
			candidate := counterfactual.CandidateFor(row)
			bars, err := loadBarsCached(cfg.Root, barCache, row.Instrument, row.SourceVersion)
			if err != nil {
				return Report{}, err
			}
			prepared, ok, err := PrepareRow(candidate, bars, funding)
			if err != nil {
				return Report{}, err
			}
			if !ok {
				continue
			}
			processed++
			market := modelapi.MarketState{
				SchemaVersion: modelapi.SchemaVersion,
				EventID:       row.EventID,
				DecisionMS:    row.DecisionMS,
				Features:      row.Values,
				MissingBits:   row.MissingBits,
			}
			for _, owner := range OwnerIDs {
				outcome, err := Replay(owner, sealed.Policies[owner], market, prepared.Account)
				if err != nil {
					return Report{}, fmt.Errorf("owner %s: %w", owner, err)
				}
				coverage[owner][partition].add(outcome)
				if outcome.Status != MatchUnique {
					continue
				}
				key := leafKey{Owner: owner, LeafID: sealed.Policies[owner].Leaves[outcome.LeafIndex].LeafID}
				runtime := leaves[key]
				pathRow, err := scoreRow(prepared, outcome.Intent, row)
				if err != nil {
					return Report{}, err
				}
				if partition == "validation" {
					runtime.validationRows = append(runtime.validationRows, pathRow)
				} else {
					runtime.leafWFRows = append(runtime.leafWFRows, pathRow)
				}
			}
			if processed%200000 == 0 {
				progress(cfg.Progress, "stage=replay partition=%s prepared=%d elapsed_rows=%d", partition, processed, len(rows))
			}
		}
		progress(cfg.Progress, "stage=replay partition=%s status=complete prepared=%d", partition, processed)
	}

	// Validation-family BH-FDR across every replayable candidate leaf.
	type familyEntry struct {
		key    leafKey
		report GateReport
	}
	var family []familyEntry
	for key, runtime := range leaves {
		report := EvaluateGates(runtime.validationRows, regimeBlocks["validation"])
		family = append(family, familyEntry{key: key, report: report})
	}
	sort.Slice(family, func(i, j int) bool {
		if family[i].key.Owner != family[j].key.Owner {
			return family[i].key.Owner < family[j].key.Owner
		}
		return family[i].key.LeafID < family[j].key.LeafID
	})
	pValues := make([]float64, len(family))
	for i, f := range family {
		pValues[i] = f.report.SignTestPValue
	}
	significant := BenjaminiHochberg(pValues, ValidationBHFDRQ)

	owners := make(map[string]*OwnerReport, len(OwnerIDs))
	for _, owner := range OwnerIDs {
		owners[owner] = &OwnerReport{
			Owner:                owner,
			PolicyID:             sealed.Policies[owner].PolicyID,
			ReplayFamily:         replayFamilyLabel(owner),
			ForbiddenTokenLeaves: ScanForbiddenTokens(sealed.Policies[owner]),
			ValidationCoverage:   coverage[owner]["validation"].summarize(owner, "validation"),
			LeafWFCoverage:       coverage[owner]["leaf_wf"].summarize(owner, "leaf_wf"),
		}
	}

	// Second pass: leaf_wf Holm confirmation only for validation-passing paths.
	var validatedKeys []leafKey
	verdictByKey := map[leafKey]Verdict{}
	hardByKey := map[leafKey][]HardFailureReason{}
	softByKey := map[leafKey][]string{}
	for i, f := range family {
		v, hard, soft := DecideValidationVerdict(f.report, significant[i])
		if f.key.Owner == "sonnet" || f.key.Owner == "opus" {
			v = VerdictUnreplayable
			hard = nil
			soft = []string{unreplayableReason(f.key.Owner)}
		}
		verdictByKey[f.key] = v
		hardByKey[f.key] = hard
		softByKey[f.key] = soft
		if v == VerdictValidatedPath {
			validatedKeys = append(validatedKeys, f.key)
		}
	}
	sort.Slice(validatedKeys, func(i, j int) bool {
		if validatedKeys[i].Owner != validatedKeys[j].Owner {
			return validatedKeys[i].Owner < validatedKeys[j].Owner
		}
		return validatedKeys[i].LeafID < validatedKeys[j].LeafID
	})
	wfReports := map[leafKey]GateReport{}
	wfPValues := make([]float64, len(validatedKeys))
	for i, key := range validatedKeys {
		runtime := leaves[key]
		report := EvaluateGates(runtime.leafWFRows, regimeBlocks["leaf_wf"])
		wfReports[key] = report
		wfPValues[i] = report.SignTestPValue
	}
	wfSignificant := HolmBonferroni(wfPValues, LeafWFHolmAlpha)

	for i, f := range family {
		key := f.key
		runtime := leaves[key]
		pv := PathVerdict{
			Owner:                 key.Owner,
			LeafID:                key.LeafID,
			Predicate:             runtime.leaf.Predicate,
			Intent:                intentString(runtime.leaf.Intent),
			ReplayStatus:          string(MatchUnique),
			Verdict:               verdictByKey[key],
			ValidationVerdict:     verdictByKey[key],
			HardFailures:          hardByKey[key],
			SoftEvidenceNotes:     softByKey[key],
			Validation:            f.report,
			ValidationPValue:      f.report.SignTestPValue,
			ValidationSignificant: significant[i],
			FidelityNote:          replayFidelityNote(key.Owner),
		}
		if key.Owner == "sonnet" || key.Owner == "opus" {
			pv.ReplayStatus = string(MatchModelUnreplayable)
		}
		if verdictByKey[key] == VerdictValidatedPath {
			pv.WFAttempted = true
			pv.WFReport = wfReports[key]
			idx := indexOfKey(validatedKeys, key)
			wfHardOK := wfReports[key].RuinCount == 0 && wfReports[key].UnknownCostCount == 0
			pv.WFPassed = wfHardOK && wfReports[key].MeetsBaseNetPositive && idx >= 0 && wfSignificant[idx]
			wfMatched := len(runtime.leafWFRows)
			wfEligible := coverage[key.Owner]["leaf_wf"].eligible
			pv.InactivityFailure = IsInactivityFailure(wfMatched, wfEligible)
			switch {
			case pv.InactivityFailure:
				pv.Verdict = VerdictInactivityFailure
			case !pv.WFPassed:
				pv.Verdict = VerdictUnstable
				pv.SoftEvidenceNotes = append(pv.SoftEvidenceNotes, "leaf_wf confirmation did not clear frozen Holm/net-positive/hard-safety checks")
			}
		}
		owners[key.Owner].Paths = append(owners[key.Owner].Paths, pv)
	}
	for _, owner := range OwnerIDs {
		sort.Slice(owners[owner].Paths, func(i, j int) bool { return owners[owner].Paths[i].LeafID < owners[owner].Paths[j].LeafID })
	}

	protocolChainVerified := make([]string, 0, len(sealed.VerifiedChain))
	for path := range sealed.VerifiedChain {
		protocolChainVerified = append(protocolChainVerified, path)
	}
	sort.Strings(protocolChainVerified)

	report := Report{
		SchemaVersion:          "lingxi.data-driven-router-v3.path-validation.v1",
		ValidationOwner:        "GPT-5.6 Sol",
		GeneratedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		SealedManifestPath:     sealed.ManifestPath,
		SealedManifestByteHash: sealed.ManifestByteHash,
		SealedBundleByteHash:   sealed.BundleByteHash,
		TournamentCodeHash:     sealed.Manifest.CodeHash,
		DatasetManifestHash:    counterfactual.DatasetManifestHash,
		DatasetReviewerHash:    sealed.Manifest.DatasetReviewer,
		G9ReviewerHash:         sealed.Manifest.G9ReviewerReceipt,
		G10ReviewerHash:        sealed.Manifest.G10ReviewerReceipt,
		FundingManifestHash:    sealed.Manifest.FundingManifest,
		FundingReceiptHash:     sealed.Manifest.FundingReceipt,
		PolicyHashes:           sealed.PolicyHashes,
		LedgerHashes:           sealed.LedgerHashes,
		SharedLedgerHash:       sealed.SharedLedgerHash,
		ProtocolHashes:         sealed.Manifest.ProtocolHashes,
		ProtocolChainVerified:  protocolChainVerified,
		RouterHoldoutUnblinded: false,
		TradeAllowed:           false,
		ValidationFamilySize:   len(family),
		LeafWFFamilySize:       len(validatedKeys),
	}
	for _, owner := range OwnerIDs {
		report.Owners = append(report.Owners, *owners[owner])
	}
	return report, nil
}

func replayFidelityNote(owner string) string {
	switch owner {
	case "terra":
		return "sealed tree leaf predicates form an exhaustive replayed partition and synthetic fidelity tests match the frozen model Predict result"
	case "opus":
		return "sealed gate routing is inspectable, but per-row linear-expert parameters are absent; representative leaf intents are not substituted for Predict"
	case "grok":
		return "only rows with exactly one matching sealed regime predicate are evaluated; centroid fallback/tie-break rows are excluded as unreplayable"
	case "sonnet":
		return "aggregated episodic evidence leaves have no observable-state membership predicate; no discovery examples were opened or refit"
	default:
		return ""
	}
}

type coverageAccumulator struct {
	summary     ActivationCoverage
	fixedCounts map[string]int
	eligible    int
}

func (c *coverageAccumulator) add(o ReplayOutcome) {
	c.eligible++
	switch o.Status {
	case MatchUnique:
		c.summary.ReplayableRows++
		c.summary.MatchedNonDefaultRows++
		if o.Intent.TargetSignedPositionFraction != 0 {
			c.summary.ActiveActionRows++
		}
	case MatchDefault:
		c.summary.ReplayableRows++
		c.summary.DefaultIntentRows++
	case MatchAmbiguousTiebreak, MatchParseIncomplete:
		c.summary.AmbiguousRows++
	case MatchModelUnreplayable:
		c.summary.ModelUnreplayableRows++
	}
	if o.Status == MatchUnique || o.Status == MatchDefault {
		if o.Intent.TargetSignedPositionFraction == 0 {
			c.summary.NoTradeRows++
		}
		if c.fixedCounts == nil {
			c.fixedCounts = map[string]int{}
		}
		c.fixedCounts[fixedActionKey(o.Intent.TargetSignedPositionFraction)]++
	}
}

func (c *coverageAccumulator) summarize(owner, partition string) ActivationCoverage {
	if c == nil {
		return ActivationCoverage{Owner: owner, Partition: partition}
	}
	out := c.summary
	out.Owner = owner
	out.Partition = partition
	out.EligibleRows = c.eligible
	if out.ReplayableRows > 0 {
		best := 0
		for _, count := range c.fixedCounts {
			if count > best {
				best = count
			}
		}
		out.DegenerateFixedFrac = float64(best) / float64(out.ReplayableRows)
		out.NoTradeRate = float64(out.NoTradeRows) / float64(out.ReplayableRows)
	}
	if out.EligibleRows > 0 {
		out.ActivationRate = float64(out.ActiveActionRows) / float64(out.EligibleRows)
		out.MatchedCoverage = float64(out.MatchedNonDefaultRows) / float64(out.EligibleRows)
	}
	return out
}

func replayFamilyLabel(owner string) string {
	switch owner {
	case "terra":
		return "FULLY_REPLAYABLE_EXHAUSTIVE_PARTITION"
	case "opus":
		return "UNREPLAYABLE_LINEAR_EXPERT_PARAMETERS_ABSENT"
	case "grok":
		return "PARTIALLY_REPLAYABLE_UNIQUE_MATCH_ONLY"
	case "sonnet":
		return "UNREPLAYABLE"
	default:
		return "UNKNOWN"
	}
}

func unreplayableReason(owner string) string {
	if owner == "opus" {
		return "sealed policy exposes gate predicates and representative intents, but omits the fitted linear-expert parameters required for per-row Predict"
	}
	return "sealed episodic policy lacks replayable per-row membership predicates; discovery neighbors remain sealed"
}

func intentString(intent modelapi.TargetIntent) string {
	return fmt.Sprintf("fraction=%g horizon_h=%d delay_bars=%d", intent.TargetSignedPositionFraction, intent.TargetHorizonHours, intent.DelayBars)
}

func indexOfKey(keys []leafKey, key leafKey) int {
	for i, k := range keys {
		if k == key {
			return i
		}
	}
	return -1
}

func loadBarsCached(root string, cache map[string]map[int64]counterfactual.Bar, instrument, sourceHash string) (map[int64]counterfactual.Bar, error) {
	if bars, ok := cache[instrument]; ok {
		return bars, nil
	}
	bars, err := counterfactual.LoadBars(root, instrument, sourceHash)
	if err != nil {
		return nil, err
	}
	cache[instrument] = bars
	return bars, nil
}

// scoreRow computes the full delay/stress reward grid for one matched row
// under one leaf's predicted intent, using the g10 counterfactual/account/
// cost primitive reimplemented in reward.go.
func scoreRow(prepared PreparedRow, intent modelapi.TargetIntent, row datav3.StateRowV2) (PathRow, error) {
	pr := PathRow{
		Instrument:   row.Instrument,
		DecisionMS:   row.DecisionMS,
		BTCReturn24h: row.Values[10],
		BTCVol24h:    row.Values[11],
	}
	base, err := EvaluateIntent(prepared, intent, 1)
	if err != nil {
		return PathRow{}, err
	}
	pr.Ruin = base.Ruin
	pr.UnknownCost = base.UnknownCost
	pr.NetRewardBase = base.NetReward

	stress, err := EvaluateIntent(prepared, intent, CostStressMultiplier)
	if err != nil {
		return PathRow{}, err
	}
	pr.Ruin = pr.Ruin || stress.Ruin
	pr.UnknownCost = pr.UnknownCost || stress.UnknownCost
	if !stress.Ruin && !stress.UnknownCost {
		pr.NetRewardStress = stress.NetReward
	}

	for d := 0; d < 4; d++ {
		variant := intent
		variant.DelayBars = d
		out, err := EvaluateIntent(prepared, variant, 1)
		if err != nil {
			return PathRow{}, err
		}
		pr.Ruin = pr.Ruin || out.Ruin
		pr.UnknownCost = pr.UnknownCost || out.UnknownCost
		if !out.Ruin && !out.UnknownCost {
			pr.NetRewardDelay[d] = out.NetReward
			pr.DelayOK[d] = true
		}
	}
	return pr, nil
}

func progress(out io.Writer, format string, args ...any) {
	if out != nil {
		fmt.Fprintf(out, format+"\n", args...)
	}
}
