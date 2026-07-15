// Package neutralrunner provides a sealed-label, owner-neutral execution
// scaffold. It does not open files and cannot unseal reward labels.
package neutralrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/accountstate"
	"lingxi/internal/datadrivenv3/modelapi"
)

const (
	SchemaVersion = "lingxi.data-driven-router-v3.neutral-runner.v1"
	LabelsSealed  = "SEALED"
)

var EffectiveProtocolHashes = map[string]string{
	"g1": "5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd",
	"g2": "7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226",
	"g3": "63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57",
	"g4": "2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9",
	"g5": "518d0003c829da641f1bcc955c4382023e158ba9938c0ac6f0ff19b5bf729385",
	"g6": "08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b",
	"g7": "4aacc43e928d25b6b83cdf8fe10d5b576e2b1009ea10b7bfad3e750388e47747",
}

const (
	DatasetManifestHash = "f9dc0944919709a49f765fe3291cf60a46952919f796add47d892af5f2808a6d"
	DatasetReviewerHash = "5fbb51bf0bab3be2649b57ba76ed68b2f714cda8a5fc7c0722a1a53f4c52edfb"
	AccountFixtureHash  = "c80745299bfac73495060d18a77cb8201a7c5ca40858ebf0df3010489d39b300"
)

var ErrLabelsSealed = errors.New("real reward labels are sealed")

type OwnerBinding struct {
	OwnerID         string            `json:"owner_id"`
	OwnerCodeHash   string            `json:"owner_code_hash"`
	GridHash        string            `json:"grid_hash"`
	SharedInputHash string            `json:"shared_input_hash"`
	ProtocolHashes  map[string]string `json:"protocol_hashes"`
	DatasetManifest string            `json:"dataset_manifest_hash"`
	DatasetReviewer string            `json:"dataset_reviewer_receipt_hash"`
	AccountFixture  string            `json:"account_fixture_receipt_hash"`
}

func (b OwnerBinding) Validate(expectedSharedInputHash string) error {
	if b.OwnerID == "" || b.OwnerCodeHash == "" || b.GridHash != modelapi.GridHash() || b.SharedInputHash == "" {
		return errors.New("owner binding has missing or mismatched code/grid fields")
	}
	if b.SharedInputHash != expectedSharedInputHash || b.DatasetManifest != DatasetManifestHash ||
		b.DatasetReviewer != DatasetReviewerHash || b.AccountFixture != AccountFixtureHash {
		return errors.New("owner binding shared input or receipt mismatch")
	}
	for generation, expected := range EffectiveProtocolHashes {
		if b.ProtocolHashes[generation] != expected {
			return fmt.Errorf("effective %s protocol hash mismatch", generation)
		}
	}
	return nil
}

// RealLabelProvider is deliberately narrow. The default provider rejects all
// attempts so a model owner cannot accidentally read a real label.
type RealLabelProvider interface {
	Status() string
	Reward(context.Context, string) (float64, error)
}

type SealedLabelProvider struct{}

func (SealedLabelProvider) Status() string { return LabelsSealed }
func (SealedLabelProvider) Reward(context.Context, string) (float64, error) {
	return 0, ErrLabelsSealed
}

type SyntheticRewardEvaluator interface {
	EvaluateSynthetic(datav3.RewardInput) (datav3.RewardResult, error)
}

// SharedRewardEvaluator routes every policy and baseline through the same
// existing reward evaluator. It accepts caller-supplied synthetic inputs only.
type SharedRewardEvaluator struct{}

func (SharedRewardEvaluator) EvaluateSynthetic(in datav3.RewardInput) (datav3.RewardResult, error) {
	return datav3.EvaluateBaseline("shared-neutral-evaluator", in)
}

type Input struct {
	AccountConfig accountstate.Config
	Markets       []modelapi.MarketState
	Ticks         []accountstate.Tick
	SharedHash    string
}

func (in Input) Validate() error {
	if len(in.Markets) == 0 || len(in.Markets) != len(in.Ticks) {
		return errors.New("markets and account ticks must have equal non-zero length")
	}
	for i, market := range in.Markets {
		if err := market.Validate(); err != nil {
			return fmt.Errorf("market %d: %w", i, err)
		}
		if market.DecisionMS != in.Ticks[i].DecisionMS {
			return fmt.Errorf("market/tick decision time mismatch at %d", i)
		}
	}
	if in.SharedHash == "" {
		return errors.New("shared input hash required")
	}
	return nil
}

// SharedInputHash is deterministic and uses only public, sealed decision-time
// IDs and the API/schema identity, never labels or model outputs.
func SharedInputHash(markets []modelapi.MarketState) string {
	h := sha256.New()
	_, _ = h.Write([]byte(SchemaVersion + "\x00" + modelapi.APIHash() + "\x00"))
	for _, market := range markets {
		_, _ = h.Write([]byte(fmt.Sprintf("%s\x00%d\x00", market.EventID, market.DecisionMS)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type Result struct {
	OwnerID       string                         `json:"owner_id"`
	SharedHash    string                         `json:"shared_input_hash"`
	Traces        []accountstate.TransitionTrace `json:"transitions"`
	AttemptLedger []modelapi.AttemptEntry        `json:"attempt_ledger"`
}

// Run executes a single owner model through the identical account-state
// transition engine used for baselines. It never calls the label provider.
func Run(ctx context.Context, binding OwnerBinding, model modelapi.Model, input Input, ledger *modelapi.AttemptLedger) (Result, error) {
	if model == nil || ledger == nil {
		return Result{}, errors.New("model and append-only ledger required")
	}
	if err := input.Validate(); err != nil {
		return Result{}, err
	}
	if input.SharedHash != SharedInputHash(input.Markets) {
		return Result{}, errors.New("shared input hash does not match market input")
	}
	if err := binding.Validate(input.SharedHash); err != nil {
		return Result{}, err
	}
	if _, err := ledger.Append("run:"+binding.OwnerID, binding.OwnerID, "PREDICT_AND_TRANSITION", input.SharedHash); err != nil {
		return Result{}, err
	}
	index := 0
	policy := func(account accountstate.AccountContext) (accountstate.TargetIntent, error) {
		if index >= len(input.Markets) {
			return accountstate.TargetIntent{}, errors.New("policy called after market input exhausted")
		}
		market := input.Markets[index]
		index++
		intent, err := model.Predict(ctx, market, toModelAccount(account))
		if err != nil {
			return accountstate.TargetIntent{}, err
		}
		if err := intent.Validate(); err != nil {
			return accountstate.TargetIntent{}, err
		}
		return accountstate.TargetIntent{
			TargetSignedPositionFraction: intent.TargetSignedPositionFraction,
			TargetHorizonHours:           intent.TargetHorizonHours,
			DelayBars:                    intent.DelayBars,
		}, nil
	}
	traces, err := accountstate.RunBaseline(input.AccountConfig, input.Ticks, policy)
	if err != nil {
		return Result{}, err
	}
	return Result{OwnerID: binding.OwnerID, SharedHash: input.SharedHash, Traces: traces, AttemptLedger: ledger.Entries()}, nil
}

func toModelAccount(account accountstate.AccountContext) modelapi.AccountContext {
	return modelapi.AccountContext{
		SignedPositionFraction:       account.SignedPositionFraction,
		FixedNotionalUSDT:            account.FixedNotionalUSDT,
		WeightedAverageEntryPrice:    account.WeightedAverageEntryPrice,
		PositionAgeBars:              account.PositionAgeBars,
		DecisionTimeUnrealizedReturn: account.DecisionTimeUnrealizedReturn,
		CumulativeSignedFundingUSDT:  account.CumulativeSignedFundingUSDT,
		CashUSDT:                     account.CashUSDT,
		MarkedEquityUSDT:             account.MarkedEquityUSDT,
		RetainedCapitalBaseUSDT:      account.RetainedCapitalBaseUSDT,
		CumulativeWithdrawalsUSDT:    account.CumulativeWithdrawalsUSDT,
		GrossLeverage:                account.GrossLeverage,
		FreeEquityUSDT:               account.FreeEquityUSDT,
		OpenPositionCount:            account.OpenPositionCount,
		PendingActionCount:           account.PendingActionCount,
	}
}

// AtomicOutputBarrier publishes a complete owner set once. No partial results
// are returned and no later replacement is permitted.
type AtomicOutputBarrier struct {
	expected map[string]struct{}
	pending  map[string]Result
	sealed   bool
}

func NewAtomicOutputBarrier(ownerIDs []string) (*AtomicOutputBarrier, error) {
	if len(ownerIDs) == 0 {
		return nil, errors.New("at least one expected owner required")
	}
	expected := make(map[string]struct{}, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		if ownerID == "" {
			return nil, errors.New("empty expected owner")
		}
		if _, duplicate := expected[ownerID]; duplicate {
			return nil, errors.New("duplicate expected owner")
		}
		expected[ownerID] = struct{}{}
	}
	return &AtomicOutputBarrier{expected: expected, pending: make(map[string]Result)}, nil
}

func (b *AtomicOutputBarrier) Submit(result Result) ([]Result, bool, error) {
	if b.sealed {
		return nil, false, errors.New("output barrier already sealed")
	}
	if _, known := b.expected[result.OwnerID]; !known {
		return nil, false, errors.New("unknown owner output")
	}
	if _, duplicate := b.pending[result.OwnerID]; duplicate {
		return nil, false, errors.New("owner already submitted")
	}
	b.pending[result.OwnerID] = result
	if len(b.pending) != len(b.expected) {
		return nil, false, nil
	}
	out := make([]Result, 0, len(b.expected))
	for ownerID := range b.expected {
		out = append(out, b.pending[ownerID])
	}
	b.sealed = true
	return out, true, nil
}

func CodeHash() string {
	sum := sha256.Sum256([]byte(SchemaVersion + "\x00" + modelapi.CodeHash() + "\x00" + DatasetManifestHash))
	return hex.EncodeToString(sum[:])
}
