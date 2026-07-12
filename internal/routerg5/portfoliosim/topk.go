package portfoliosim

import (
	"sort"

	"lingxi/internal/routerg5/policy"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// SelectTopK picks up to TopK proposals with cluster and leverage caps.
func SelectTopK(proposals []policy.Proposal, equityUSDT float64) []policy.Proposal {
	if len(proposals) == 0 {
		return nil
	}
	sorted := append([]policy.Proposal(nil), proposals...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].ID < sorted[j].ID
	})
	maxAccount := equityUSDT * float64(g5protocol.MaxAccountLeverage)
	clusterCap := maxAccount * 0.4
	usedAccount := 0.0
	usedCluster := map[string]float64{}
	var out []policy.Proposal
	for _, p := range sorted {
		if len(out) >= g5protocol.TopK {
			break
		}
		if p.NotionalUSDT <= 0 || p.CapacityHeadroom <= 0 {
			continue
		}
		alloc := p.NotionalUSDT
		remainingAccount := maxAccount - usedAccount
		remainingCluster := clusterCap - usedCluster[p.Cluster]
		if remainingCluster < alloc {
			alloc = remainingCluster
		}
		if remainingAccount < alloc {
			alloc = remainingAccount
		}
		if alloc <= 0 {
			continue
		}
		cp := p
		if alloc < p.NotionalUSDT {
			cp.NotionalUSDT = alloc
			cp.SizeFraction = alloc / (equityUSDT * float64(g5protocol.MaxAccountLeverage))
			cp.Action.SizeFraction = cp.SizeFraction
		}
		out = append(out, cp)
		usedAccount += alloc
		usedCluster[p.Cluster] += alloc
	}
	return out
}
