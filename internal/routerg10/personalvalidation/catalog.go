package personalvalidation

import (
	"fmt"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/eventmap"
)

// SelectCatalogSymbols returns the full deterministic catalog symbol list for one profile.
func SelectCatalogSymbols(catalog historydata.HistoryCatalog, profileID string, maxFiles int) ([]string, error) {
	selection, err := eventmap.SelectPrimaryFiles(catalog, profileID, maxFiles, 0)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	return symbols, nil
}

// RequireFullCatalogSelection rejects truncated formal scopes.
func RequireFullCatalogSelection(catalog historydata.HistoryCatalog, profileID string, maxFiles int, allowTruncatedSmoke bool) error {
	if allowTruncatedSmoke {
		return nil
	}
	selection, err := eventmap.SelectPrimaryFiles(catalog, profileID, maxFiles, 0)
	if err != nil {
		return err
	}
	if selection.Truncated {
		return fmt.Errorf("formal personal validation requires full history catalog for %s; got %d/%d symbols (raise --max-files, do not truncate)",
			profileID, len(selection.Files), selection.Available)
	}
	return nil
}
