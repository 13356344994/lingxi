package personalvalidation

import (
	"time"

	"lingxi/internal/routerg10/personalbrain"
)

// CanonicalEventID is the stable EventID/TriggerID namespace shared with
// exclusionmap, entryproof, executionpolicy, and exitproof artifacts.
func CanonicalEventID(symbol string, decision time.Time, eventFamily string) string {
	return personalbrain.CanonicalEventID(symbol, decision, eventFamily)
}
