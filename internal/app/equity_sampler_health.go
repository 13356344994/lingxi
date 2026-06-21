package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	equitySamplerHealthFileName     = "equity_sampler_health.json"
	equitySamplerHealthSchemaVersion = 1
)

// equitySamplerHealth 权益采样器连通性健康信号(供账户风控与外部看门狗读取)。
type equitySamplerHealth struct {
	SchemaVersion         int    `json:"schema_version"`
	LastSuccessTS         string `json:"last_success_ts,omitempty"`
	LastAttemptTS         string `json:"last_attempt_ts"`
	ConsecutiveAllFail    int    `json:"consecutive_all_fail"`
	ConnectivityBlind     bool   `json:"connectivity_blind"`
	ExpectedVenueCount    int    `json:"expected_venue_count"`
	LastSuccessVenueCount int    `json:"last_success_venue_count"`
}

func equitySamplerHealthPath(recordDir string) string {
	return filepath.Join(crossVenueRecordDir(recordDir), equitySamplerHealthFileName)
}

func loadEquitySamplerHealth(recordDir string) (equitySamplerHealth, bool) {
	path := equitySamplerHealthPath(recordDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return equitySamplerHealth{}, false
	}
	var health equitySamplerHealth
	if err := json.Unmarshal(data, &health); err != nil {
		return equitySamplerHealth{}, false
	}
	return health, true
}

func isEquitySamplerConnectivityBlind(recordDir string) bool {
	health, ok := loadEquitySamplerHealth(recordDir)
	return ok && health.ConnectivityBlind
}

func recordEquitySamplerAttempt(recordDir, attemptTS string, expectedVenues, successVenues int, complete bool) error {
	dir := crossVenueRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	prev, _ := loadEquitySamplerHealth(recordDir)
	health := equitySamplerHealth{
		SchemaVersion:         equitySamplerHealthSchemaVersion,
		LastAttemptTS:         attemptTS,
		ExpectedVenueCount:    expectedVenues,
		LastSuccessVenueCount: prev.LastSuccessVenueCount,
		LastSuccessTS:         prev.LastSuccessTS,
		ConsecutiveAllFail:    prev.ConsecutiveAllFail,
		ConnectivityBlind:     prev.ConnectivityBlind,
	}

	if successVenues == 0 {
		health.ConsecutiveAllFail = prev.ConsecutiveAllFail + 1
		health.ConnectivityBlind = true
	} else {
		health.ConsecutiveAllFail = 0
		health.ConnectivityBlind = false
		health.LastSuccessVenueCount = successVenues
		if complete {
			health.LastSuccessTS = attemptTS
		}
	}

	return writeJSONFile(equitySamplerHealthPath(recordDir), health)
}
