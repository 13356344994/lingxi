package registry

import (
	"errors"

	"lingxi/internal/hierarchicalrouter"
)

func ToRouterLeaf(candidate Candidate) (hierarchicalrouter.Leaf, error) {
	if candidate.State != StateResearchPass {
		return hierarchicalrouter.Leaf{}, errors.New("only RESEARCH_PASS candidates may enter router as uncertified leaves")
	}
	direction := hierarchicalrouter.SideUnknown
	switch candidate.Direction {
	case "long":
		direction = hierarchicalrouter.SideLong
	case "short":
		direction = hierarchicalrouter.SideShort
	default:
		return hierarchicalrouter.Leaf{}, errors.New("candidate direction is not routable")
	}
	return hierarchicalrouter.Leaf{
		LeafID: candidate.ID, MechanismFamily: candidate.Mechanism,
		Payer: candidate.Payer, Variant: candidate.ID, Direction: direction,
		Generation: candidate.Generation, ProtocolHash: candidate.ProtocolHash,
		RequiredData: append([]string(nil), candidate.RequiredData...),
		Invalidators: append([]string(nil), candidate.Invalidators...),
		State:        hierarchicalrouter.LeafResearchPass,
	}, nil
}
