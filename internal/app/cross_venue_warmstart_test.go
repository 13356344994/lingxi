package app

import (
	"io"
	"log"
	"testing"
	"time"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

// TestCrossVenueWarmStartRoundTrip 验证最新完整快照(含 FundingByVenue)落盘→读回一致,
// 且 warmStartFromDisk 仅在 lastSnapshot 为空时填充、不覆盖已有新鲜快照。
func TestCrossVenueWarmStartRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	snap := CrossVenueScanSnapshot{
		SnapshotID:       "cvsnap-warm-1",
		ScannedAt:        now,
		OpportunityCount: 1,
		MinSpreadAPR:     0.1,
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "H-USDT", HighVenue: okx.VenueName, LowVenue: bybit.VenueName,
			SpreadAPR: 5.0, TradableNotional: 80,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			okx.VenueName: {"H-USDT": {Rate: -0.01, IntervalHours: 4}},
		},
	}
	if err := crossVenuePersistLatestSnapshot(dir, snap); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, ok := crossVenueLoadLatestSnapshot(dir)
	if !ok {
		t.Fatal("load returned ok=false")
	}
	if got.SnapshotID != snap.SnapshotID || len(got.Opportunities) != 1 || got.Opportunities[0].Symbol != "H-USDT" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.FundingByVenue[okx.VenueName]["H-USDT"].Rate != -0.01 {
		t.Fatalf("funding_by_venue 未持久化: %+v", got.FundingByVenue)
	}

	// 空扫描器:warmStartFromDisk 应填充。
	s := &CrossVenueScanner{recordDir: dir, logger: log.New(io.Discard, "", 0)}
	s.warmStartFromDisk()
	if s.Snapshot().SnapshotID != snap.SnapshotID {
		t.Fatalf("warmStartFromDisk 未填充: %q", s.Snapshot().SnapshotID)
	}

	// 已有新鲜快照:warmStartFromDisk 不得覆盖。
	s2 := &CrossVenueScanner{recordDir: dir, logger: log.New(io.Discard, "", 0), lastSnapshot: CrossVenueScanSnapshot{SnapshotID: "fresh"}}
	s2.warmStartFromDisk()
	if s2.Snapshot().SnapshotID != "fresh" {
		t.Fatalf("warmStartFromDisk 覆盖了新鲜快照: %q", s2.Snapshot().SnapshotID)
	}
}

// TestCrossVenueLoadLatestSnapshotMissing 不存在/损坏文件返回 ok=false。
func TestCrossVenueLoadLatestSnapshotMissing(t *testing.T) {
	t.Parallel()
	if _, ok := crossVenueLoadLatestSnapshot(t.TempDir()); ok {
		t.Fatal("空目录应返回 ok=false")
	}
}
