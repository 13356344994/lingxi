package candidates

import (
	"errors"

	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerdiscovery/registry"
)

type PrizeRegistration struct {
	RequiredData       []string
	Invalidators       []string
	CostModel          string
	ForcedBehavior     string
	CompetitionDefense string
	DeadlineRule       string
}

func FromPrizeNomination(n g2prize.Nomination, meta PrizeRegistration) (Definition, error) {
	if n.Kind != g2prize.KindCandidateNomination || !n.SurvivesSelectionTax {
		return Definition{}, errors.New("routerg2: prize nomination did not survive frozen selection tax")
	}
	if len(meta.RequiredData) == 0 || len(meta.Invalidators) == 0 || meta.CostModel == "" ||
		meta.ForcedBehavior == "" || meta.CompetitionDefense == "" || meta.DeadlineRule == "" {
		return Definition{}, errors.New("routerg2: nomination requires forward-review data and invalidators")
	}
	return Definition{
		SkyState: n.SkyState, SorterDomain: n.SorterDomain,
		Mechanism: n.Mechanism, Variant: n.Key, Direction: n.Direction,
		Payer: n.Payer, Invalidators: meta.Invalidators, RequiredData: meta.RequiredData,
		ForcedBehavior: meta.ForcedBehavior, CannotWait: true,
		CompetitionDefense: meta.CompetitionDefense, DeadlineRule: meta.DeadlineRule,
		CostModel: meta.CostModel, Generation: n.Generation, ProtocolHash: n.ProtocolHash,
		DiscoveryDataHash: n.DiscoveryDataHash, Origin: registry.OriginBackward,
		SearchSpaceSize: n.SearchSpaceSize, Rank: n.Rank,
		MatchingRule: n.MatchingRule, ControlCount: n.ControlCount,
	}, nil
}
