package personalvalidation

import (
	g4protocol "lingxi/internal/routerg4/protocol"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/eventmap"
)

// BuildHistoryScope records catalog selection metadata for formal validation reports.
func BuildHistoryScope(catalog historydata.HistoryCatalog, profileIDs []string, maxFiles int, allowTruncatedSmoke bool) map[string]HistoryScopeSummary {
	out := make(map[string]HistoryScopeSummary, len(profileIDs))
	for _, profileID := range profileIDs {
		selection, err := eventmap.SelectPrimaryFiles(catalog, profileID, maxFiles, 0)
		if err != nil {
			continue
		}
		scope := HistoryScopeSummary{
			Available: selection.Available,
			Selected:  len(selection.Files),
			Truncated: selection.Truncated,
			FullMarket: !selection.Truncated,
			Scope:     g4protocol.FullMarketScope,
		}
		if allowTruncatedSmoke || selection.Truncated {
			scope.Truncated = true
			scope.FullMarket = false
			scope.Scope = g4protocol.TruncatedSmokeScope
		}
		out[profileID] = scope
	}
	return out
}

// FormalValidationAllowed rejects truncated smoke from certifying formal validation.
func FormalValidationAllowed(allowTruncatedSmoke bool, scope map[string]HistoryScopeSummary) bool {
	if allowTruncatedSmoke {
		return false
	}
	for _, s := range scope {
		if s.Truncated || !s.FullMarket || s.Scope != g4protocol.FullMarketScope {
			return false
		}
	}
	return true
}
