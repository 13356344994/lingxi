package announcements

import (
	"sort"

	"lingxi/internal/datatruth"
)

// BuildEventChain orders cross-venue announcements per symbol by first_seen_at.
// Non-OKX venues are info-only; execution remains fixed on OKX.
func BuildEventChain(items []Announcement, generation string) (EventChain, error) {
	bySymbol := map[string][]Announcement{}
	for _, a := range items {
		bySymbol[a.Symbol] = append(bySymbol[a.Symbol], a)
	}
	symbols := make([]string, 0, len(bySymbol))
	for sym := range bySymbol {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)

	out := EventChain{
		SchemaVersion:  ChainSchemaVersion,
		Generation:     generation,
		ExecutionVenue: ExecutionVenue,
		Symbols:        make([]SymbolChain, 0, len(symbols)),
	}
	for _, sym := range symbols {
		rows := bySymbol[sym]
		sortAnnouncements(rows)
		chain := SymbolChain{Symbol: sym, Events: make([]ChainEvent, 0, len(rows))}
		for i, a := range rows {
			chain.Events = append(chain.Events, ChainEvent{
				Sequence:       i + 1,
				Symbol:         a.Symbol,
				AnnouncementID: a.ID,
				Venue:          a.Venue,
				Kind:           a.Kind,
				PublishedAt:    a.PublishedAt.UTC(),
				FirstSeenAt:    a.FirstSeenAt.UTC(),
				EffectiveAt:    a.EffectiveAt.UTC(),
				InfoOnly:       a.InfoOnly,
				ExecutionVenue: ExecutionVenue,
			})
		}
		out.Symbols = append(out.Symbols, chain)
	}
	hash, err := hashEventChain(out)
	if err != nil {
		return EventChain{}, err
	}
	out.ChainHash = hash
	return out, nil
}

func hashEventChain(chain EventChain) (string, error) {
	payload := chain
	payload.ChainHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateEventChain checks chain integrity and OKX-only execution binding.
func ValidateEventChain(chain EventChain) error {
	if chain.SchemaVersion != ChainSchemaVersion {
		return errChain("schema mismatch")
	}
	if chain.ExecutionVenue != ExecutionVenue {
		return errChain("execution venue must be OKX")
	}
	want, err := hashEventChain(chain)
	if err != nil {
		return err
	}
	if chain.ChainHash == "" || chain.ChainHash != want {
		return errChain("chain hash mismatch")
	}
	for _, sym := range chain.Symbols {
		for i, ev := range sym.Events {
			if ev.ExecutionVenue != ExecutionVenue {
				return errChain("event execution venue must be OKX")
			}
			if ev.Sequence != i+1 {
				return errChain("sequence gap")
			}
			if ev.InfoOnly && ev.Venue == VenueOKX {
				return errChain("OKX events cannot be info-only")
			}
			if !ev.InfoOnly && ev.Venue != VenueOKX {
				return errChain("non-OKX events must be info-only")
			}
		}
	}
	return nil
}

func errChain(msg string) error {
	return &chainError{msg: msg}
}

type chainError struct{ msg string }

func (e *chainError) Error() string { return "announcements: chain " + e.msg }
