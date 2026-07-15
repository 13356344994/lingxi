// Package modelapi is the sealed, owner-neutral contract for V3 model owners.
// It has no filesystem or label-provider dependency by design.
package modelapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	SchemaVersion = "lingxi.data-driven-router-v3.model-api.v1"
	FeatureCount  = 25
)

var FeatureOrder = [FeatureCount]string{
	"own_return_1h", "own_return_4h", "own_return_24h",
	"own_rms_vol_4h", "own_rms_vol_24h", "own_drawdown_24h",
	"quote_volume_24h", "bar_range_bps",
	"btc_return_1h", "btc_return_4h", "btc_return_24h",
	"btc_rms_vol_24h", "btc_drawdown_24h", "beta_24h",
	"cross_section_return_rank", "cross_section_vol_rank", "cross_section_liquidity_rank",
	"funding_rate_latest", "funding_age_hours", "rubik_taker_ratio", "rubik_ls_ratio",
	"utc_hour_sin", "utc_hour_cos", "utc_weekday_sin", "utc_weekday_cos",
}

// MarketState is the complete, fixed decision-time market input. MissingBits
// identifies unavailable features; a set bit makes the corresponding value
// semantically unavailable, rather than an imputed zero.
type MarketState struct {
	SchemaVersion string                `json:"schema_version"`
	EventID       string                `json:"event_id"`
	DecisionMS    int64                 `json:"decision_ms"`
	Features      [FeatureCount]float64 `json:"features"`
	MissingBits   uint64                `json:"missing_bits"`
}

// AccountContext contains only decision-time account information.
type AccountContext struct {
	SignedPositionFraction       float64 `json:"signed_position_fraction"`
	FixedNotionalUSDT            float64 `json:"fixed_notional_usdt"`
	WeightedAverageEntryPrice    float64 `json:"weighted_average_entry_price"`
	PositionAgeBars              int     `json:"position_age_bars"`
	DecisionTimeUnrealizedReturn float64 `json:"decision_time_unrealized_return"`
	CumulativeSignedFundingUSDT  float64 `json:"cumulative_signed_funding_usdt"`
	CashUSDT                     float64 `json:"cash_usdt"`
	MarkedEquityUSDT             float64 `json:"marked_equity_usdt"`
	RetainedCapitalBaseUSDT      float64 `json:"retained_capital_base_usdt"`
	CumulativeWithdrawalsUSDT    float64 `json:"cumulative_withdrawals_usdt"`
	GrossLeverage                float64 `json:"gross_leverage"`
	FreeEquityUSDT               float64 `json:"free_equity_usdt"`
	OpenPositionCount            int     `json:"open_position_count"`
	PendingActionCount           int     `json:"pending_action_count"`
}

type TargetIntent struct {
	TargetSignedPositionFraction float64 `json:"target_signed_position_fraction"`
	TargetHorizonHours           int     `json:"target_horizon_hours"`
	DelayBars                    int     `json:"delay_bars"`
}

type ExampleKind string

const (
	ExampleSynthetic ExampleKind = "SYNTHETIC"
	ExampleDiscovery ExampleKind = "DISCOVERY"
)

// TrainingExample deliberately has no reward value or outcome vector.
// SyntheticTarget is a legacy transport name: SYNTHETIC examples carry
// fixture targets, while DISCOVERY examples carry only the neutral runner's
// selected discovery TargetIntent under the effective protocol.
type TrainingExample struct {
	SchemaVersion   string         `json:"schema_version"`
	Kind            ExampleKind    `json:"kind"`
	ExampleID       string         `json:"example_id"`
	Market          MarketState    `json:"market"`
	Account         AccountContext `json:"account"`
	SyntheticTarget TargetIntent   `json:"synthetic_target"`
}

type CandidateLeaf struct {
	LeafID          string       `json:"leaf_id"`
	Predicate       string       `json:"predicate"`
	Intent          TargetIntent `json:"intent"`
	RejectionReason string       `json:"rejection_reason,omitempty"`
}

// Policy is a replayable, inspectable collection of state-to-intent leaves.
type Policy struct {
	SchemaVersion string          `json:"schema_version"`
	PolicyID      string          `json:"policy_id"`
	Leaves        []CandidateLeaf `json:"leaves"`
	DefaultIntent TargetIntent    `json:"default_intent"`
}

type AttemptEntry struct {
	Sequence     uint64 `json:"sequence"`
	AttemptID    string `json:"attempt_id"`
	OwnerID      string `json:"owner_id"`
	Operation    string `json:"operation"`
	InputHash    string `json:"input_hash"`
	PreviousHash string `json:"previous_hash"`
	EntryHash    string `json:"entry_hash"`
}

// AttemptLedger can only grow through Append. Callers receive defensive copies.
type AttemptLedger struct {
	entries []AttemptEntry
}

func (l *AttemptLedger) Append(attemptID, ownerID, operation, inputHash string) (AttemptEntry, error) {
	if attemptID == "" || ownerID == "" || operation == "" || inputHash == "" {
		return AttemptEntry{}, errors.New("attempt ledger requires non-empty fields")
	}
	previous := ""
	if n := len(l.entries); n > 0 {
		previous = l.entries[n-1].EntryHash
	}
	entry := AttemptEntry{Sequence: uint64(len(l.entries) + 1), AttemptID: attemptID, OwnerID: ownerID, Operation: operation, InputHash: inputHash, PreviousHash: previous}
	entry.EntryHash = hashStrings(fmt.Sprintf("%d", entry.Sequence), entry.AttemptID, entry.OwnerID, entry.Operation, entry.InputHash, entry.PreviousHash)
	l.entries = append(l.entries, entry)
	return entry, nil
}

func (l *AttemptLedger) Entries() []AttemptEntry {
	return append([]AttemptEntry(nil), l.entries...)
}

func (l *AttemptLedger) HeadHash() string {
	if len(l.entries) == 0 {
		return ""
	}
	return l.entries[len(l.entries)-1].EntryHash
}

// Model accepts only sealed synthetic/discovery examples and predicts a target
// intent from decision-time inputs. It has no label, peer-output, or IO hook.
type Model interface {
	Fit(context.Context, []TrainingExample) (Policy, error)
	Predict(context.Context, MarketState, AccountContext) (TargetIntent, error)
}

func (m MarketState) Validate() error {
	if m.SchemaVersion != SchemaVersion || m.EventID == "" || m.DecisionMS <= 0 || m.MissingBits>>FeatureCount != 0 {
		return errors.New("invalid market state schema or identity")
	}
	for i, value := range m.Features {
		if m.MissingBits&(uint64(1)<<i) == 0 && !finite(value) {
			return fmt.Errorf("feature %d is not finite", i)
		}
	}
	return nil
}

func (i TargetIntent) Validate() error {
	if i.TargetHorizonHours != 1 && i.TargetHorizonHours != 4 && i.TargetHorizonHours != 24 {
		return errors.New("target horizon must be 1, 4, or 24 hours")
	}
	if i.DelayBars < 0 || i.DelayBars > 3 {
		return errors.New("delay bars must be in [0,3]")
	}
	for _, allowed := range []float64{-1, -.5, -.25, -.1, 0, .1, .25, .5, 1} {
		if math.Abs(i.TargetSignedPositionFraction-allowed) < 1e-12 {
			return nil
		}
	}
	return errors.New("target fraction is not on the frozen grid")
}

func ValidateTrainingExamples(examples []TrainingExample) error {
	if len(examples) == 0 {
		return errors.New("at least one synthetic or discovery example required")
	}
	seen := make(map[string]struct{}, len(examples))
	for _, example := range examples {
		if example.SchemaVersion != SchemaVersion || example.ExampleID == "" {
			return errors.New("invalid training example identity")
		}
		if example.Kind != ExampleSynthetic && example.Kind != ExampleDiscovery {
			return errors.New("real labels and unknown example kinds are sealed")
		}
		if _, duplicate := seen[example.ExampleID]; duplicate {
			return errors.New("duplicate training example ID")
		}
		seen[example.ExampleID] = struct{}{}
		if err := example.Market.Validate(); err != nil {
			return err
		}
		if err := example.SyntheticTarget.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func GridHash() string {
	return hashStrings("-1", "-0.5", "-0.25", "-0.1", "0", "0.1", "0.25", "0.5", "1", "1", "4", "24", "0", "1", "2", "3")
}

func APIHash() string {
	names := append([]string(nil), FeatureOrder[:]...)
	sort.Strings(names)
	values := append([]string{SchemaVersion, GridHash()}, names...)
	return hashStrings(values...)
}

func CodeHash() string { return hashStrings("modelapi", APIHash()) }

func hashStrings(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
