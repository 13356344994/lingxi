package registry

import (
	"fmt"
	"lingxi/internal/routerdiscovery/forward"
)

type ForwardRegistration struct {
	Generation        string
	ProtocolHash      string
	DiscoveryDataHash string
	SearchSpaceSize   int
	Rank              int
	CostModel         string
}

func FromForward(dto forward.CandidateDTO, meta ForwardRegistration) Candidate {
	return Candidate{
		ID: dto.ID, Origin: OriginForward, Generation: meta.Generation,
		ProtocolHash: meta.ProtocolHash, DiscoveryDataHash: meta.DiscoveryDataHash,
		SearchSpaceSize: meta.SearchSpaceSize, Rank: meta.Rank,
		Payer: dto.Payer, Mechanism: dto.Mechanism, Direction: dto.Direction,
		ForcedBehavior: dto.ForcedBehavior, CannotWait: dto.CannotWait,
		CompetitionDefense: dto.CompetitionDefense,
		DeadlineRule:       fmt.Sprintf("unix_ms:%d", dto.DeadlineUnixMs),
		SupportDomain:      dto.SupportDomain, Invalidators: dto.ObservableInvalidators,
		CostModel: meta.CostModel, RequiredData: dto.HistoryFamilies,
	}
}
