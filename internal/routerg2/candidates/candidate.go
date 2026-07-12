package candidates

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg2/protocol"
)

type Definition struct {
	SkyState           string
	SorterDomain       string
	Mechanism          string
	Variant            string
	Direction          string
	Payer              string
	ForcedBehavior     string
	CannotWait         bool
	CompetitionDefense string
	DeadlineRule       string
	Invalidators       []string
	RequiredData       []string
	CostModel          string
	Generation         string
	ProtocolHash       string
	DiscoveryDataHash  string
	Origin             registry.Origin
	SearchSpaceSize    int
	Rank               int
	MatchingRule       string
	ControlCount       int
}

func CanonicalID(def Definition) string {
	return strings.Join([]string{
		clean(def.SkyState), clean(def.SorterDomain), clean(def.Mechanism),
		clean(def.Variant), clean(def.Direction),
	}, "/")
}

func ToRegistry(def Definition) (registry.Candidate, error) {
	if clean(def.SkyState) == "" || clean(def.SorterDomain) == "" ||
		clean(def.Mechanism) == "" || clean(def.Variant) == "" ||
		(def.Direction != "long" && def.Direction != "short") ||
		clean(def.Payer) == "" || clean(def.ForcedBehavior) == "" || !def.CannotWait ||
		clean(def.CompetitionDefense) == "" || clean(def.DeadlineRule) == "" ||
		len(def.Invalidators) == 0 ||
		len(def.RequiredData) == 0 || clean(def.CostModel) == "" {
		return registry.Candidate{}, errors.New("routerg2: incomplete conditional candidate")
	}
	if strings.EqualFold(clean(def.Mechanism), "panic_liquidation") {
		return registry.Candidate{}, errors.New("routerg2: frozen g1 panic-liquidation semantics cannot revive")
	}
	switch clean(def.Mechanism) {
	case "funding_post_settlement_release", "funding_settlement_context":
		return registry.Candidate{}, errors.New("routerg2: frozen g1 funding economic mechanism cannot revive")
	}
	if isG1FundingRule(def) {
		return registry.Candidate{}, errors.New("routerg2: frozen g1 funding rule cannot revive")
	}
	if def.Origin != registry.OriginForward && def.Origin != registry.OriginBackward {
		return registry.Candidate{}, errors.New("routerg2: invalid origin")
	}
	if def.Generation != protocol.Generation || def.ProtocolHash != protocol.RouterProtocolHash {
		return registry.Candidate{}, errors.New("routerg2: candidate is not bound to frozen G2 generation/protocol")
	}
	if def.SearchSpaceSize <= 0 || def.Rank <= 0 || def.Rank > def.SearchSpaceSize {
		return registry.Candidate{}, errors.New("routerg2: search provenance is required")
	}
	if def.Origin == registry.OriginBackward && (clean(def.MatchingRule) == "" || def.ControlCount <= 0) {
		return registry.Candidate{}, errors.New("routerg2: backward candidate requires matched controls")
	}
	if def.Origin == registry.OriginBackward && clean(def.Mechanism) != protocol.G2PrizeMechanism {
		return registry.Candidate{}, errors.New("routerg2: backward candidate mechanism is outside frozen G2 novelty contract")
	}
	return registry.Candidate{
		ID: CanonicalID(def), Origin: def.Origin, Generation: def.Generation,
		ProtocolHash: def.ProtocolHash, DiscoveryDataHash: def.DiscoveryDataHash,
		SearchSpaceSize: def.SearchSpaceSize, Rank: def.Rank,
		MatchingRule: def.MatchingRule, ControlCount: def.ControlCount,
		Payer: def.Payer, Mechanism: def.Mechanism, Direction: def.Direction,
		ForcedBehavior: def.ForcedBehavior, CannotWait: def.CannotWait,
		CompetitionDefense: def.CompetitionDefense, DeadlineRule: def.DeadlineRule,
		SupportDomain: []string{"sky=" + clean(def.SkyState), "sorter=" + clean(def.SorterDomain)},
		Invalidators:  def.Invalidators, CostModel: def.CostModel, RequiredData: def.RequiredData,
	}, nil
}

func Register(r registry.Registry, def Definition, now time.Time) (registry.Registry, error) {
	candidate, err := ToRegistry(def)
	if err != nil {
		return r, err
	}
	return registry.Register(r, candidate, now)
}

func isG1FundingRule(def Definition) bool {
	variant := clean(def.Variant)
	return variant == "negative_funding_receiver_release_short" ||
		variant == "positive_funding_receiver_release_long"
}

func clean(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "_", "/", "_", "|", "_")
	return replacer.Replace(value)
}

func ValidateCanonicalID(id string, def Definition) error {
	if id != CanonicalID(def) {
		return fmt.Errorf("routerg2: leaf id %q does not encode conditional path %q", id, CanonicalID(def))
	}
	return nil
}
