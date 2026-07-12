// Package announcements archives cross-venue listing/delisting/maintenance/suspension
// announcements with strict point-in-time visibility for G10 research.
package announcements

const (
	SchemaVersion = "lingxi.router-g10-announcements.v1"
	ConfigSchema  = "lingxi.router-g10-announcements.config.v1"
	Generation    = "firstbattle-router-g10"

	CheckpointSchemaVersion = "lingxi.router-g10-announcements.checkpoint.v1"
	ManifestSchemaVersion   = "lingxi.router-g10-announcements.manifest.v1"
	ChainSchemaVersion      = "lingxi.router-g10-announcements.chain.v1"
	ArchiveLineSchema       = "lingxi.router-g10-announcements.archive-line.v1"

	OutputAnnouncementsFile = "g10_announcement_tape.jsonl"
	OutputChainFile         = "g10_cross_venue_event_chain.json"
	OutputManifestFile      = "g10_announcement_manifest.json"

	ExecutionVenue = "OKX"

	RuntimeGOMAXPROCS = 1
	ProgressEvery     = 2000
	DefaultPageSize   = 100
)
