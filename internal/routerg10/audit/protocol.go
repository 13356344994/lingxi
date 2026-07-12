// Package audit implements append-only BrainTrace recording, deterministic hashing,
// degeneracy detection, and replay verification for the G10 brain.
package audit

import g10protocol "lingxi/internal/routerg10/protocol"

const (
	SchemaVersion = g10protocol.BrainTraceSchemaV1
	Generation    = g10protocol.Generation

	SingleActionDominanceFraction = g10protocol.SingleActionDominanceFraction
	NeverExitBarsThreshold        = g10protocol.NeverExitBarsThreshold
	AlwaysDirectionalFraction     = g10protocol.AlwaysDirectionalFraction
	DegeneracyStatus              = g10protocol.BrainDegeneracyStatus
)

// PathMatrixRequired mirrors protocol.BrainTracePathMatrixRequired.
var PathMatrixRequired = append([]string(nil), g10protocol.BrainTracePathMatrixRequired...)

// RequiredFields mirrors protocol.BrainTraceRequiredFields.
var RequiredFields = append([]string(nil), g10protocol.BrainTraceRequiredFields...)

// ForbiddenFields mirrors protocol.BrainTraceForbiddenFields.
var ForbiddenFields = append([]string(nil), g10protocol.BrainTraceForbiddenFields...)
