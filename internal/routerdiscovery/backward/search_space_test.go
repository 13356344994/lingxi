package backward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/hierarchicalrouter"
)

func TestPublicSearchSpaceIsSealedAndProtocolBound(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean("../../../research/firstbattle/backward_search_space.json"))
	if err != nil {
		t.Fatal(err)
	}
	var space SearchSpace
	if err := json.Unmarshal(raw, &space); err != nil {
		t.Fatal(err)
	}
	sealed, err := SealSearchSpace(space)
	if err != nil {
		t.Fatal(err)
	}
	if space.DefinitionHash == "" {
		t.Fatalf("definition_hash is empty; fill with %s (manifest_hash=%s)", sealed.DefinitionHash, sealed.ManifestHash)
	}
	if space.ManifestHash == "" {
		t.Fatalf("manifest_hash is empty; fill with %s", sealed.ManifestHash)
	}
	if err := space.Validate(); err != nil {
		t.Fatalf("%v; expected manifest_hash=%s", err, sealed.ManifestHash)
	}
	if space.ProtocolHash != hierarchicalrouter.RouterProtocolHash ||
		space.Generation != hierarchicalrouter.Generation ||
		space.SourceDataHash != hierarchicalrouter.HistoryDataHash {
		t.Fatal("public search space is not bound to frozen router/history")
	}
	if len(space.Combinations) > hierarchicalrouter.BackwardMaxSearchCombos {
		t.Fatal("search space exceeds frozen budget")
	}
}
