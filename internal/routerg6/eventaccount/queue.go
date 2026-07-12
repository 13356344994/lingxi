package eventaccount

import (
	"sort"

	"lingxi/internal/routerg6/sizing"
)

// QueueAdmission selects events by ranking score up to max concurrent — not Top-K.
type QueueAdmission struct {
	MaxConcurrent int
}

// Select admits ranked candidates subject to concurrency, overlap, and sizing floors.
func (q QueueAdmission) Select(account *Account, candidates []EventCandidate) ([]EventCandidate, []RejectedCandidate) {
	if len(candidates) == 0 {
		return nil, nil
	}
	maxC := q.MaxConcurrent
	if maxC <= 0 {
		maxC = sizing.MaxConcurrent(account.InitialEquity)
	}
	open := len(account.OpenSlots())
	capacity := maxC - open
	if capacity <= 0 {
		rejected := make([]RejectedCandidate, 0, len(candidates))
		for _, c := range candidates {
			rejected = append(rejected, RejectedCandidate{Candidate: c, Reason: RejectConcurrencyCap})
		}
		return nil, rejected
	}

	sorted := append([]EventCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si := sizing.RankingScore(sorted[i].ConservativeEdgeUSDT, sorted[i].StressLossUSDT, sorted[i].MarginTimeSec)
		sj := sizing.RankingScore(sorted[j].ConservativeEdgeUSDT, sorted[j].StressLossUSDT, sorted[j].MarginTimeSec)
		if si != sj {
			return si > sj
		}
		return sorted[i].EventID < sorted[j].EventID
	})

	var accepted []EventCandidate
	var rejected []RejectedCandidate
	for _, c := range sorted {
		if len(accepted) >= capacity {
			rejected = append(rejected, RejectedCandidate{Candidate: c, Reason: RejectConcurrencyCap})
			continue
		}
		if reason, ok := OverlapReject(account, c); ok {
			account.OverlapRejects++
			rejected = append(rejected, RejectedCandidate{Candidate: c, Reason: reason})
			continue
		}
		sized := sizing.SizeEventNotional(sizing.Input{
			EquityUSDT:            account.Core.EquityUSDT,
			ConservativeEdgeUSDT:  c.ConservativeEdgeUSDT,
			StressLossUSDT:        c.StressLossUSDT,
			ReferenceNotionalUSDT: c.ReferenceNotional,
			MarginTimeSec:         c.MarginTimeSec,
			MinNotionalUSDT:       c.MinNotionalUSDT,
			LotNotionalUSDT:       c.LotNotionalUSDT,
			UsedAccountStressUSDT: account.UsedAccountStress(),
			UsedNotionalUSDT:      account.UsedNotional(),
		})
		if sized.Skipped {
			account.QueueRejects++
			rejected = append(rejected, RejectedCandidate{Candidate: c, Reason: sized.SkipReason})
			continue
		}
		accepted = append(accepted, c)
	}
	return accepted, rejected
}

// RejectedCandidate records one queue rejection.
type RejectedCandidate struct {
	Candidate EventCandidate
	Reason    string
}

const (
	RejectConcurrencyCap = "concurrency_cap"
	RejectSameEvent      = "same_event"
	RejectSymbolOverlap  = "symbol_overlap"
)
