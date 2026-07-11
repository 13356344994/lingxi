package datatruth

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type InstrumentVersion struct {
	InstrumentID  string    `json:"instrument_id"`
	Venue         string    `json:"venue"`
	Symbol        string    `json:"symbol"`
	ListedAt      time.Time `json:"listed_at"`
	DelistedAt    time.Time `json:"delisted_at,omitempty"`
	TickSize      string    `json:"tick_size"`
	LotSize       string    `json:"lot_size"`
	MinNotional   string    `json:"min_notional"`
	ContractValue string    `json:"contract_value,omitempty"`
	KnownFrom     time.Time `json:"known_from"`
	KnownUntil    time.Time `json:"known_until,omitempty"`
}

type InstrumentCatalog struct {
	Versions []InstrumentVersion `json:"versions"`
}

func (c InstrumentCatalog) InstrumentAt(instrumentID string, eventTime, knownAt time.Time) (InstrumentVersion, bool) {
	var matches []InstrumentVersion
	for _, v := range c.Versions {
		if v.InstrumentID != instrumentID {
			continue
		}
		if !activeDuring(eventTime, v.ListedAt, v.DelistedAt) || !knownDuring(knownAt, v.KnownFrom, v.KnownUntil) {
			continue
		}
		matches = append(matches, v)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].KnownFrom.Equal(matches[j].KnownFrom) {
			return matches[i].ListedAt.Before(matches[j].ListedAt)
		}
		return matches[i].KnownFrom.Before(matches[j].KnownFrom)
	})
	if len(matches) == 0 {
		return InstrumentVersion{}, false
	}
	return matches[len(matches)-1], true
}

func (c InstrumentCatalog) Validate() error {
	for i, v := range c.Versions {
		if strings.TrimSpace(v.InstrumentID) == "" || strings.TrimSpace(v.Venue) == "" || strings.TrimSpace(v.Symbol) == "" {
			return fmt.Errorf("instrument version %d missing identity", i)
		}
		if v.ListedAt.IsZero() || v.KnownFrom.IsZero() {
			return fmt.Errorf("instrument version %s missing listed_at or known_from", v.InstrumentID)
		}
		if !v.DelistedAt.IsZero() && v.DelistedAt.Before(v.ListedAt) {
			return fmt.Errorf("instrument version %s delisted before listed", v.InstrumentID)
		}
		if !v.KnownUntil.IsZero() && v.KnownUntil.Before(v.KnownFrom) {
			return fmt.Errorf("instrument version %s known_until before known_from", v.InstrumentID)
		}
	}
	return nil
}

type UniverseVersion struct {
	UniverseID string    `json:"universe_id"`
	Members    []string  `json:"members"`
	Effective  time.Time `json:"effective"`
	Expires    time.Time `json:"expires,omitempty"`
	KnownFrom  time.Time `json:"known_from"`
	KnownUntil time.Time `json:"known_until,omitempty"`
}

type UniverseCatalog struct {
	Versions []UniverseVersion `json:"versions"`
}

func (c UniverseCatalog) UniverseAt(universeID string, eventTime, knownAt time.Time) (UniverseVersion, bool) {
	var matches []UniverseVersion
	for _, v := range c.Versions {
		if v.UniverseID != universeID {
			continue
		}
		if !activeDuring(eventTime, v.Effective, v.Expires) || !knownDuring(knownAt, v.KnownFrom, v.KnownUntil) {
			continue
		}
		cp := v
		cp.Members = uniqueSorted(cp.Members)
		matches = append(matches, cp)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].KnownFrom.Equal(matches[j].KnownFrom) {
			return matches[i].Effective.Before(matches[j].Effective)
		}
		return matches[i].KnownFrom.Before(matches[j].KnownFrom)
	})
	if len(matches) == 0 {
		return UniverseVersion{}, false
	}
	return matches[len(matches)-1], true
}

func (c UniverseCatalog) Validate() error {
	for i, v := range c.Versions {
		if strings.TrimSpace(v.UniverseID) == "" {
			return fmt.Errorf("universe version %d missing universe_id", i)
		}
		if v.Effective.IsZero() || v.KnownFrom.IsZero() {
			return fmt.Errorf("universe %s missing effective or known_from", v.UniverseID)
		}
		if len(v.Members) == 0 {
			return fmt.Errorf("universe %s has no members", v.UniverseID)
		}
		if !v.Expires.IsZero() && v.Expires.Before(v.Effective) {
			return fmt.Errorf("universe %s expires before effective", v.UniverseID)
		}
		if !v.KnownUntil.IsZero() && v.KnownUntil.Before(v.KnownFrom) {
			return fmt.Errorf("universe %s known_until before known_from", v.UniverseID)
		}
	}
	return nil
}

func (u UniverseVersion) Contains(instrumentID string) bool {
	for _, m := range u.Members {
		if m == instrumentID {
			return true
		}
	}
	return false
}

func ValidateUniverseMembers(u UniverseVersion, instruments InstrumentCatalog, eventTime, knownAt time.Time) error {
	for _, member := range u.Members {
		if _, ok := instruments.InstrumentAt(member, eventTime, knownAt); !ok {
			return fmt.Errorf("universe %s member %s is not valid point-in-time", u.UniverseID, member)
		}
	}
	return nil
}

func activeDuring(t, start, end time.Time) bool {
	if t.IsZero() || start.IsZero() || t.Before(start) {
		return false
	}
	return end.IsZero() || t.Before(end)
}

func knownDuring(t, start, end time.Time) bool {
	if t.IsZero() || start.IsZero() || t.Before(start) {
		return false
	}
	return end.IsZero() || t.Before(end)
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			seen[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

var ErrPointInTimeMiss = errors.New("point-in-time record not found")
