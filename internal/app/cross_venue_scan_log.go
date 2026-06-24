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

// crossVenueScanLogMaxBytes scan_snapshots 滚动上限:每行内嵌全量 opportunities(~300KB),无界 append
// 曾达 190MB(撑爆 GitHub 100MB)。超过即把当前文件改名为 .1(只保留一代归档)、开新文件继续。
// loadCrossVenueScanSnapshots 只读当前文件;64MB ≈ 最近 30+ 小时快照,足够回放校验/诊断。
const crossVenueScanLogMaxBytes = 64 * 1024 * 1024

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
	// 超过上限即滚动归档(O(1):改名 + 开新文件),防无限增长。
	if _, rErr := rotateJSONLBySize(path, crossVenueScanLogMaxBytes); rErr != nil && s.logger != nil {
		s.logger.Printf("[跨所扫描] scan_snapshots 滚动归档失败 path=%s: %v", path, rErr)
	}
	if err := appendJSONLRecord(path, rec); err != nil && s.logger != nil {
		s.logger.Printf("[跨所扫描] snapshot 落盘失败 path=%s snapshot=%s: %v", path, rec.SnapshotID, err)
	}
}

// rotateJSONLBySize 若 path 文件大小 ≥ maxBytes,改名为 path+".1"(覆盖旧一代归档)以便调用方开新文件继续追加。
// 返回是否发生滚动。文件不存在/未超限返回 (false,nil)。O(1),不读写文件内容。
func rotateJSONLBySize(path string, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Size() < maxBytes {
		return false, nil
	}
	bak := path + ".1"
	_ = os.Remove(bak)
	if err := os.Rename(path, bak); err != nil {
		return false, err
	}
	return true, nil
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
