// Package fingerprint builds a local PersonalExecutionFingerprint from private
// execution logs. Output is versioned aggregates + hash only; raw lines and
// identifiers never enter the artifact.
package fingerprint

const (
	SchemaVersion = "lingxi.router-g10-fingerprint.v1"
	Generation    = "firstbattle-router-g10"
	ConfigSchema  = "lingxi.router-g10-fingerprint.config.v1"

	CheckpointSchemaVersion = "lingxi.router-g10-fingerprint.checkpoint.v1"
	OutputFileName          = "personal_execution_fingerprint.json"

	RuntimeGOMAXPROCS = 1
	ProgressEvery     = 5000
	MaxReservoir      = 256
)

// ForbiddenOutputFields must never appear anywhere in fingerprint JSON output.
var ForbiddenOutputFields = []string{
	"order_id",
	"orderid",
	"position_id",
	"fast_event_id",
	"snapshot_id",
	"episode_id",
	"cl_ord_id",
	"token",
	"api_key",
	"apikey",
	"secret",
	"passphrase",
	"password",
	"private_key",
	"raw_line",
	"raw",
	"realized_usdt",
	"realized_pnl",
}
