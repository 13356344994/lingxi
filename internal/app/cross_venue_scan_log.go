package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const crossVenueScanLogFileName = "scan_snapshots.jsonl"

type crossVenueScanLogRecord struct {
	SnapshotID       string                  `json:"snapshot_id,omitempty"`
	ScannedAt        time.Time               `json:"scanned_at"`
	MinSpreadAPR     float64                 `json:"min_spread_apr,omitempty"`
	OpportunityCount int                     `json:"opportunity_count,omitempty"`
	MaxConcurrent    int                     `json:"max_concurrent,omitempty"`
	MaxPositionUSDT  float64                 `json:"max_position_usdt,omitempty"`
	Opportunities    []CrossVenueOpportunity `json:"opportunities,omitempty"`
}

func (s *CrossVenueScanner) appendScanSnapshot(snapshot CrossVenueScanSnapshot) {
	if s == nil || strings.TrimSpace(snapshot.SnapshotID) == "" {
		return
	}
	rec := crossVenueScanLogRecord{
		SnapshotID:       strings.TrimSpace(snapshot.SnapshotID),
		ScannedAt:        snapshot.ScannedAt,
		MinSpreadAPR:     snapshot.MinSpreadAPR,
		OpportunityCount: snapshot.OpportunityCount,
		MaxConcurrent:    s.execCfg.MaxConcurrent,
		MaxPositionUSDT:  s.execCfg.MaxPositionUSDT,
		Opportunities:    snapshot.Opportunities,
	}
	path := filepath.Join(crossVenueRecordDir(s.recordDir), crossVenueScanLogFileName)
	if err := appendJSONLRecord(path, rec); err != nil && s.logger != nil {
		s.logger.Printf("[跨所扫描] snapshot 落盘失败 path=%s snapshot=%s: %v", path, rec.SnapshotID, err)
	}
}

func loadCrossVenueScanSnapshots(recordDir string, since time.Time) ([]crossVenueScanLogRecord, error) {
	path := filepath.Join(crossVenueRecordDir(recordDir), crossVenueScanLogFileName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	out := make([]crossVenueScanLogRecord, 0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec crossVenueScanLogRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if !since.IsZero() && rec.ScannedAt.Before(since) {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
