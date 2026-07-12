package g2prize

import (
	"fmt"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/protocol"
)

const SearchSpaceID = "firstbattle.g2.sky_conditioned_prize.v1"

func DefaultSearchSpace() (SearchSpace, error) {
	labels := []string{"low", "mid", "high"}
	directions := []mechanism.Direction{mechanism.DirectionLong, mechanism.DirectionShort}
	var combinations []Combination
	for _, ret := range labels {
		for _, vol := range labels {
			for _, funding := range labels {
				skyPattern := fmt.Sprintf("ret=%s/te=*/rv=%s/dd=*/fund=%s", ret, vol, funding)
				for _, horizon := range []Horizon{Horizon1h, Horizon4h, Horizon24h} {
					for _, direction := range directions {
						combinations = append(combinations, Combination{
							Key: fmt.Sprintf(
								"ret_%s_rv_%s_fund_%s_funding_flip_%s_%s",
								ret, vol, funding, horizon, direction,
							),
							SkyState: skyPattern, SorterDomain: "liquid", Horizon: horizon,
							FeatureKey: "funding_sign_flip", Op: OpGTE, Threshold: 1,
							MatchingRule: "same_full_sky_sorter_symbol_liquidity_direction_horizon_cluster",
							Mechanism:    "funding_payer_flip_rebalance", Direction: direction,
						})
					}
				}
			}
		}
	}
	if len(combinations) > protocol.BackwardMaxSearchCombos {
		return SearchSpace{}, fmt.Errorf("g2prize: default search space exceeds frozen budget")
	}
	return SealSearchSpace(SearchSpace{
		ID: SearchSpaceID, Generation: protocol.Generation,
		ProtocolHash:   protocol.RouterProtocolHash,
		SourceDataHash: protocol.HistoryDataHash,
		Combinations:   combinations,
	})
}
