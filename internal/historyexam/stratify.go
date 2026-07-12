// Package historyexam implements the historical exam boundary described in the
// first-battle master plan (阶段 A2). It only manipulates data identity,
// stratification, hidden multi-paper commitments, and a protocol-bound grader.
//
// Hard boundaries enforced by this package:
//   - Stratification consumes only decision-time-visible signals. Any revenue,
//     pnl, forward-return, or performance-derived axis is rejected fail-closed,
//     so market regimes can never be defined by "what made money".
//   - A correlated cluster belongs to exactly one of research / walk-forward /
//     exam. Cross-partition leakage and insufficient time embargo are rejected.
//   - Hidden papers (time ranges, fixture paths, strata) live only in an
//     examiner/custodian manifest persisted with 0600 permissions. The public
//     surface exposes nothing but Ticket commitments.
//   - Grading never accepts an externally supplied pass/fail. A caller must go
//     through a protocol-bound Evaluator that first re-verifies the hidden
//     paper's DataHash/ChallengeHash. Researchers receive only a SignedResult.
//
// This package reads no real records, revenue, or existing papers; all inputs
// are synthetic fixtures supplied by the caller.
package historyexam

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/datatruth"
)

// allowedSignals is the frozen allow-list of decision-time-visible
// stratification axes. Every axis here can be computed from information that is
// knowable at the decision instant, never from realized outcomes.
var allowedSignals = map[string]struct{}{
	"time_scale":       {},
	"volatility":       {},
	"trend_efficiency": {},
	"market_breadth":   {},
	"liquidity_proxy":  {},
	"funding_state":    {},
}

// forbiddenTokens are substrings that indicate an outcome- or revenue-derived
// input. They are rejected whether they appear in an axis name or its value so
// that a regime label can never smuggle in "what made money".
var forbiddenTokens = []string{
	"pnl", "return", "revenue", "profit", "reward", "income",
	"yield", "gain", "loss", "sharpe", "sortino", "equity",
	"drawdown", "payoff", "roi", "edge",
}

// StratumInput is a frozen categorical description of a market regime. Axes are
// keyed by an allow-listed signal name and hold a coarse bucket value (for
// example volatility=high). It carries no time range and no revenue.
type StratumInput struct {
	Axes map[string]string `json:"axes"`
}

// Validate enforces the no-revenue input contract: every axis must be
// allow-listed, non-empty, and free of any outcome-derived token.
func (in StratumInput) Validate() error {
	if len(in.Axes) == 0 {
		return errors.New("stratum input requires at least one decision-time axis")
	}
	for rawKey, rawValue := range in.Axes {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		value := strings.TrimSpace(rawValue)
		if key == "" {
			return errors.New("stratum axis name must not be empty")
		}
		if value == "" {
			return fmt.Errorf("stratum axis %q must have a non-empty bucket value", rawKey)
		}
		if _, ok := allowedSignals[key]; !ok {
			return fmt.Errorf("stratum axis %q is not an allow-listed decision-time signal", rawKey)
		}
		lowerValue := strings.ToLower(value)
		for _, tok := range forbiddenTokens {
			if strings.Contains(key, tok) || strings.Contains(lowerValue, tok) {
				return fmt.Errorf("stratum axis %q references forbidden revenue-derived input %q", rawKey, tok)
			}
		}
	}
	return nil
}

// Key returns a canonical, order-independent identity for the stratum. Two
// stratum inputs with the same axes produce the same key regardless of map
// iteration order, so clusters can be grouped by regime deterministically.
func (in StratumInput) Key() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	names := make([]string, 0, len(in.Axes))
	for k := range in.Axes {
		names = append(names, strings.ToLower(strings.TrimSpace(k)))
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		// Re-resolve the value case-insensitively to keep the key stable.
		for rawKey, rawValue := range in.Axes {
			if strings.ToLower(strings.TrimSpace(rawKey)) == name {
				parts = append(parts, name+"="+strings.TrimSpace(rawValue))
				break
			}
		}
	}
	return datatruth.HashStrings(parts...), nil
}
