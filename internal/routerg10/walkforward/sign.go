package walkforward

import (
	"math"
	"sort"

	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/playbook"
	g10validation "lingxi/internal/routerg10/validation"
)

func oneSidedBinomialSignPValue(nonzeroBlocks, positiveBlocks int) float64 {
	if nonzeroBlocks == 0 {
		return 1
	}
	if positiveBlocks <= 0 {
		return 1
	}
	logTail := math.Inf(-1)
	for i := positiveBlocks; i <= nonzeroBlocks; i++ {
		logTail = logAddExp(logTail, logBinomialHalfPMF(nonzeroBlocks, i))
	}
	p := math.Exp(logTail)
	if p > 1 {
		return 1
	}
	return p
}

func logBinomialHalfPMF(n, k int) float64 {
	logN, _ := math.Lgamma(float64(n + 1))
	logK, _ := math.Lgamma(float64(k + 1))
	logNK, _ := math.Lgamma(float64(n - k + 1))
	return logN - logK - logNK - float64(n)*math.Ln2
}

func logAddExp(a, b float64) float64 {
	if math.IsInf(a, -1) {
		return b
	}
	if math.IsInf(b, -1) {
		return a
	}
	if a < b {
		a, b = b, a
	}
	return a + math.Log1p(math.Exp(b-a))
}

// BuildAttemptSignResults computes one-sided sign p-values for the full ledger family.
func BuildAttemptSignResults(ledger playbook.ExperimentLedger, selectedPlaybookHash string, daily DailyEquityReplay) []AttemptSignResult {
	nonzero, positive := dailySignCounts(daily)
	selectedP := oneSidedBinomialSignPValue(nonzero, positive)
	results := make([]AttemptSignResult, 0, len(ledger.Attempts))
	for _, attempt := range ledger.Attempts {
		p := 1.0
		selected := attempt.PlaybookHash == selectedPlaybookHash
		if selected {
			p = selectedP
		}
		results = append(results, AttemptSignResult{
			AttemptOrdinal: attempt.AttemptOrdinal, PlaybookHash: attempt.PlaybookHash,
			Selected: selected, NonzeroDays: nonzero, PositiveDays: positive, SignPValue: p,
		})
	}
	if len(results) == 0 {
		results = append(results, AttemptSignResult{
			AttemptOrdinal: 0, PlaybookHash: selectedPlaybookHash, Selected: true,
			NonzeroDays: nonzero, PositiveDays: positive, SignPValue: selectedP,
		})
	}
	applyHolm(results, g10protocol.WFFamilyAlpha)
	return results
}

func dailySignCounts(daily DailyEquityReplay) (nonzero, positive int) {
	for _, pnl := range daily.DailyPnL {
		if math.Abs(pnl) < 1e-9 {
			continue
		}
		nonzero++
		if pnl > 0 {
			positive++
		}
	}
	return nonzero, positive
}

func applyHolm(results []AttemptSignResult, alpha float64) {
	indexes := make([]int, len(results))
	for i := range results {
		indexes[i] = i
	}
	sort.Slice(indexes, func(i, j int) bool {
		return results[indexes[i]].SignPValue < results[indexes[j]].SignPValue
	})
	m := len(indexes)
	rejected := false
	for rank, idx := range indexes {
		limit := alpha / float64(m-rank)
		results[idx].HolmLimit = limit
		if rejected || results[idx].SignPValue > limit {
			rejected = true
			results[idx].HolmPass = false
			continue
		}
		results[idx].HolmPass = true
	}
}

// FrozenSizingFromSurvivor extracts hash-bound sizing metrics for one capital tier.
func FrozenSizingFromSurvivor(entry g10validation.SurvivorEntry, capitalTier int) FrozenSizingInput {
	ref := float64(capitalTier)
	if ref <= 0 {
		ref = g10protocol.ValidationInitialEquities[0]
	}
	out := FrozenSizingInput{
		ConservativeMeanBPS:       entry.FrozenConservativeMeanBPS,
		StressConservativeMeanBPS: entry.FrozenStressConservativeMeanBPS,
		ReferenceNotional:         ref,
	}
	for _, tr := range entry.TierResults {
		if tr.CapitalTier == capitalTier && tr.FrozenConservativeMeanBPS != 0 {
			out.ConservativeMeanBPS = tr.FrozenConservativeMeanBPS
			out.StressConservativeMeanBPS = tr.StressConservativeMeanBPS
			break
		}
	}
	if out.StressConservativeMeanBPS <= 0 && out.ConservativeMeanBPS > 0 {
		out.StressConservativeMeanBPS = out.ConservativeMeanBPS * g10protocol.CostStressMultiplier
	}
	return out
}
