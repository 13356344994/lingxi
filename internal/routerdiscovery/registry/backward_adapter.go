package registry

import (
	"errors"
	"strings"

	"lingxi/internal/routerdiscovery/backward"
)

type BackwardRegistration struct {
	ID                 string
	CostModel          string
	RequiredData       []string
	SupportDomain      []string
	Invalidators       []string
	ForcedBehavior     string
	CompetitionDefense string
	DeadlineRule       string
}

// FromBackwardNomination converts only a statistically surviving nomination
// into a registry candidate. It still requires explicit forward-review fields
// (invalidators, required data, support domain) and remains NOMINATED until
// Freeze + RecordForwardFalsification.
func FromBackwardNomination(n backward.Nomination, meta BackwardRegistration) (Candidate, error) {
	if n.Kind != backward.KindCandidateNomination || !n.SurvivesSelectionTax {
		return Candidate{}, errors.New("backward nomination did not survive the frozen research selection tax")
	}
	if strings.TrimSpace(meta.ID) == "" || strings.TrimSpace(n.Mechanism) == "" ||
		strings.TrimSpace(n.Direction) == "" || len(meta.Invalidators) == 0 ||
		len(meta.RequiredData) == 0 || len(meta.SupportDomain) == 0 ||
		strings.TrimSpace(meta.ForcedBehavior) == "" ||
		strings.TrimSpace(meta.CompetitionDefense) == "" ||
		strings.TrimSpace(meta.DeadlineRule) == "" {
		return Candidate{}, errors.New("backward nomination lacks mandatory forward-review fields")
	}
	return Candidate{
		ID: meta.ID, Origin: OriginBackward, Generation: n.Generation,
		ProtocolHash: n.ProtocolHash, DiscoveryDataHash: n.DiscoveryDataHash,
		SearchSpaceSize: n.SearchSpaceSize, Rank: n.Rank,
		MatchingRule: n.MatchingRule, ControlCount: n.ControlCount,
		Payer: n.Payer, Mechanism: n.Mechanism, Direction: n.Direction,
		ForcedBehavior: meta.ForcedBehavior, CannotWait: true,
		CompetitionDefense: meta.CompetitionDefense, DeadlineRule: meta.DeadlineRule,
		SupportDomain: meta.SupportDomain, Invalidators: meta.Invalidators,
		CostModel: meta.CostModel, RequiredData: meta.RequiredData,
	}, nil
}
