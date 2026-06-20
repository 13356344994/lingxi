package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cross_venue_warmstart.go:扫描器快照热加载(hot start)。
//
// 背景:扫描器首轮 scanOnce 要全量拉四所费率 + 借币探测,耗时约 30min;期间 Snapshot() 为空,
// 轮动拿不到机会→无法开仓。进程停启本身只要一两分钟,却要白等 30min 才能交易,浪费极大。
//
// 方案:每轮扫描把**完整快照(含 FundingByVenue)**覆盖写到单文件 latest_snapshot.json;
// 进程重启时在首轮 scanOnce 之前先把它 load 进 lastSnapshot,轮动 60s 循环立即就有真实(略陈旧)
// 数据可用。后台 scanOnce 跑完后用新鲜数据原子替换。
//
// 安全:轮动有 rotationSnapshotMaxAge(30min)新鲜度闸,加载的快照过旧会被自动跳过(不会乱开);
// 真正下单时执行闸仍按**实时余额/盘口/EV**复核,故热加载不牺牲真实性,只是把"可交易"时刻提前。
//
// 与 scan_snapshots.jsonl 的区别:后者是 append-only 历史审计(且不含 FundingByVenue);
// 本文件是单文件、每轮覆盖、含 FundingByVenue,专供热加载,体积可控。

const crossVenueLatestSnapshotFileName = "latest_snapshot.json"

// crossVenueLatestSnapshotFile 是落盘的完整快照镜像(显式包含 FundingByVenue,scan_snapshots.jsonl 不存)。
type crossVenueLatestSnapshotFile struct {
	SnapshotID       string                                       `json:"snapshot_id"`
	ScannedAt        time.Time                                    `json:"scanned_at"`
	VenueStatuses    []CrossVenueVenueStatus                      `json:"venue_statuses,omitempty"`
	Opportunities    []CrossVenueOpportunity                      `json:"opportunities,omitempty"`
	MinSpreadAPR     float64                                      `json:"min_spread_apr,omitempty"`
	OpportunityCount int                                          `json:"opportunity_count,omitempty"`
	FundingByVenue   map[string]map[string]CrossVenueFundingEntry `json:"funding_by_venue,omitempty"`
}

// crossVenuePersistLatestSnapshot 把最近一轮完整快照覆盖写到磁盘(供下次启动热加载)。best-effort。
func crossVenuePersistLatestSnapshot(recordDir string, snap CrossVenueScanSnapshot) error {
	if strings.TrimSpace(snap.SnapshotID) == "" {
		return nil
	}
	rec := crossVenueLatestSnapshotFile{
		SnapshotID:       snap.SnapshotID,
		ScannedAt:        snap.ScannedAt,
		VenueStatuses:    snap.VenueStatuses,
		Opportunities:    snap.Opportunities,
		MinSpreadAPR:     snap.MinSpreadAPR,
		OpportunityCount: snap.OpportunityCount,
		FundingByVenue:   snap.FundingByVenue,
	}
	// writeJSONFile 走临时文件 + rename 原子落盘,避免读到半截文件。
	return writeJSONFile(crossVenueLatestSnapshotPath(recordDir), rec)
}

// crossVenueLoadLatestSnapshot 读取磁盘上最近一轮完整快照;不存在/损坏返回 (零值,false)。
func crossVenueLoadLatestSnapshot(recordDir string) (CrossVenueScanSnapshot, bool) {
	data, err := os.ReadFile(crossVenueLatestSnapshotPath(recordDir))
	if err != nil {
		return CrossVenueScanSnapshot{}, false
	}
	var rec crossVenueLatestSnapshotFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return CrossVenueScanSnapshot{}, false
	}
	if strings.TrimSpace(rec.SnapshotID) == "" {
		return CrossVenueScanSnapshot{}, false
	}
	return CrossVenueScanSnapshot{
		SnapshotID:       rec.SnapshotID,
		ScannedAt:        rec.ScannedAt,
		VenueStatuses:    rec.VenueStatuses,
		Opportunities:    rec.Opportunities,
		MinSpreadAPR:     rec.MinSpreadAPR,
		OpportunityCount: rec.OpportunityCount,
		FundingByVenue:   rec.FundingByVenue,
	}, true
}

func crossVenueLatestSnapshotPath(recordDir string) string {
	return filepath.Join(crossVenueRecordDir(recordDir), crossVenueLatestSnapshotFileName)
}

// warmStartFromDisk 在首轮全量扫描(~30min)完成前,把磁盘上最近一轮完整快照载入 lastSnapshot,
// 让轮动决策立即有真实(略陈旧)数据可用;后台 scanOnce 跑完后用新鲜数据原子替换。
// 过旧快照由轮动 rotationSnapshotMaxAge(30min)兜底跳过,真正下单仍按实时余额/盘口复核,故不牺牲真实性。
func (s *CrossVenueScanner) warmStartFromDisk() {
	if s == nil {
		return
	}
	snap, ok := crossVenueLoadLatestSnapshot(s.recordDir)
	if !ok {
		return
	}
	s.mu.Lock()
	empty := strings.TrimSpace(s.lastSnapshot.SnapshotID) == "" && len(s.lastSnapshot.Opportunities) == 0
	if empty {
		s.lastSnapshot = snap
	}
	s.mu.Unlock()
	if empty && s.logger != nil {
		s.logger.Printf("[跨所扫描] 热加载磁盘快照 snapshot=%s scanned_at=%s opps=%d funding_venues=%d (后台首轮扫描刷新中)",
			snap.SnapshotID, snap.ScannedAt.UTC().Format(time.RFC3339), len(snap.Opportunities), len(snap.FundingByVenue))
	}
}
