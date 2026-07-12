package walkforward

import (
	"math"
	"sort"

	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
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
func BuildAttemptSignResults(ledger reverseevent.ExperimentLedger, selectedCardHash string, daily DailyEquityReplay) []AttemptSignResult {
	nonzero, positive := dailySignCounts(daily)
	selectedP := oneSidedBinomialSignPValue(nonzero, positive)

	results := make([]AttemptSignResult, 0, len(ledger.Attempts))
	for _, attempt := range ledger.Attempts {
		p := 1.0
		if attempt.EventCardHash == selectedCardHash {
			p = selectedP
		} else if attempt.Status == protocol.UnexplainedCandidateStatus || attempt.SourceWinnerCount > 0 {
			p = 0.5
		}
		results = append(results, AttemptSignResult{
			AttemptOrdinal: attempt.AttemptOrdinal,
			EventCardHash:  attempt.EventCardHash,
			Selected:       attempt.EventCardHash == selectedCardHash,
			NonzeroDays:    nonzero,
			PositiveDays:   positive,
			SignPValue:     p,
		})
	}
	if len(results) == 0 {
		results = append(results, AttemptSignResult{
			AttemptOrdinal: 0,
			EventCardHash:  selectedCardHash,
			Selected:       true,
			NonzeroDays:    nonzero,
			PositiveDays:   positive,
			SignPValue:     selectedP,
		})
	}
	applyHolm(results, protocol.WFFamilyAlpha)
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
