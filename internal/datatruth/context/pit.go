package contextdata

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type InstrumentSnapshot struct {
	FetchedAt   time.Time                        `json:"fetched_at"`
	Instruments map[string]InstrumentSnapshotRow `json:"instruments"`
}

type InstrumentSnapshotRow struct {
	ListTimeMS int64  `json:"list_time_ms"`
	CTVal      string `json:"ct_val"`
	Lot        string `json:"lot"`
	Tick       string `json:"tick"`
	State      string `json:"state"`
}

type UniverseSnapshot struct {
	At        time.Time `json:"at"`
	Shortlist []struct {
		Symbol string `json:"symbol"`
	} `json:"shortlist"`
}

func ConvertInstrumentSnapshot(raw []byte) (datatruth.InstrumentCatalog, error) {
	var snapshot InstrumentSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("contextdata: decode instrument snapshot: %w", err)
	}
	if snapshot.FetchedAt.IsZero() {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("contextdata: instrument snapshot missing fetched_at")
	}
	if len(snapshot.Instruments) == 0 {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("contextdata: instrument snapshot has no instruments")
	}
	keys := make([]string, 0, len(snapshot.Instruments))
	for symbol := range snapshot.Instruments {
		keys = append(keys, symbol)
	}
	sort.Strings(keys)
	catalog := datatruth.InstrumentCatalog{}
	for _, symbol := range keys {
		row := snapshot.Instruments[symbol]
		if !strings.EqualFold(strings.TrimSpace(row.State), "live") {
			continue
		}
		listedAt := time.UnixMilli(row.ListTimeMS).UTC()
		if row.ListTimeMS <= 0 {
			// A current snapshot cannot prove a historical listing time. Starting validity at
			// fetched_at is conservative and never backfills an unknown interval.
			listedAt = snapshot.FetchedAt.UTC()
		}
		catalog.Versions = append(catalog.Versions, datatruth.InstrumentVersion{
			InstrumentID:  strings.TrimSpace(symbol),
			Venue:         "okx",
			Symbol:        strings.TrimSpace(symbol) + "-SWAP",
			ListedAt:      listedAt,
			TickSize:      strings.TrimSpace(row.Tick),
			LotSize:       strings.TrimSpace(row.Lot),
			ContractValue: strings.TrimSpace(row.CTVal),
			KnownFrom:     snapshot.FetchedAt.UTC(),
		})
	}
	if len(catalog.Versions) == 0 {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("contextdata: instrument snapshot has no live versions")
	}
	if err := catalog.Validate(); err != nil {
		return datatruth.InstrumentCatalog{}, err
	}
	return catalog, nil
}

func ConvertUniverseSnapshot(raw []byte, universeID string) (datatruth.UniverseCatalog, error) {
	var snapshot UniverseSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return datatruth.UniverseCatalog{}, fmt.Errorf("contextdata: decode universe snapshot: %w", err)
	}
	if snapshot.At.IsZero() {
		return datatruth.UniverseCatalog{}, fmt.Errorf("contextdata: universe snapshot missing at")
	}
	if strings.TrimSpace(universeID) == "" {
		universeID = "okx-directional-shortlist"
	}
	members := make([]string, 0, len(snapshot.Shortlist))
	for _, item := range snapshot.Shortlist {
		if symbol := strings.TrimSpace(item.Symbol); symbol != "" {
			members = append(members, symbol)
		}
	}
	members = sortedUnique(members)
	catalog := datatruth.UniverseCatalog{Versions: []datatruth.UniverseVersion{{
		UniverseID: universeID,
		Members:    members,
		Effective:  snapshot.At.UTC(),
		KnownFrom:  snapshot.At.UTC(),
	}}}
	if err := catalog.Validate(); err != nil {
		return datatruth.UniverseCatalog{}, err
	}
	return catalog, nil
}

func MergeInstrumentCatalog(base datatruth.InstrumentCatalog, incoming datatruth.InstrumentCatalog) (datatruth.InstrumentCatalog, error) {
	out := datatruth.InstrumentCatalog{Versions: append([]datatruth.InstrumentVersion(nil), base.Versions...)}
	seen := map[string]bool{}
	for _, version := range out.Versions {
		seen[instrumentVersionKey(version)] = true
	}
	for _, version := range incoming.Versions {
		key := instrumentVersionKey(version)
		if !seen[key] {
			seen[key] = true
			out.Versions = append(out.Versions, version)
		}
	}
	sort.Slice(out.Versions, func(i, j int) bool {
		if out.Versions[i].InstrumentID == out.Versions[j].InstrumentID {
			return out.Versions[i].KnownFrom.Before(out.Versions[j].KnownFrom)
		}
		return out.Versions[i].InstrumentID < out.Versions[j].InstrumentID
	})
	return out, out.Validate()
}

func MergeUniverseCatalog(base datatruth.UniverseCatalog, incoming datatruth.UniverseCatalog) (datatruth.UniverseCatalog, error) {
	out := datatruth.UniverseCatalog{Versions: append([]datatruth.UniverseVersion(nil), base.Versions...)}
	seen := map[string]bool{}
	for _, version := range out.Versions {
		seen[universeVersionKey(version)] = true
	}
	for _, version := range incoming.Versions {
		key := universeVersionKey(version)
		if !seen[key] {
			seen[key] = true
			out.Versions = append(out.Versions, version)
		}
	}
	sort.Slice(out.Versions, func(i, j int) bool {
		if out.Versions[i].UniverseID == out.Versions[j].UniverseID {
			return out.Versions[i].KnownFrom.Before(out.Versions[j].KnownFrom)
		}
		return out.Versions[i].UniverseID < out.Versions[j].UniverseID
	})
	return out, out.Validate()
}

func instrumentVersionKey(v datatruth.InstrumentVersion) string {
	hash, _, _ := datatruth.HashJSON(v)
	return hash
}

func universeVersionKey(v datatruth.UniverseVersion) string {
	v.Members = sortedUnique(v.Members)
	hash, _, _ := datatruth.HashJSON(v)
	return hash
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	for _, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
