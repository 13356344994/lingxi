package app

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// Test_slowCleanupAbsentPosition_WritesAndRemoves 钉死慢层幽灵清理:写独立 slow_position_cleanup.jsonl
// (realized=0/fee=0/hold_seconds/分层齐),仅移除目标仓(不误删其它真实仓),且不写 settlements.jsonl。
func Test_slowCleanupAbsentPosition_WritesAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	ghost := &okxDirectionalPosition{
		Symbol: "GLW-USDT", Side: "short", EntryPrice: 256.52, Qty: 0.02, NotionalUSDT: 7.5,
		OpenedAt: now.Add(-22 * time.Hour), Regime: "range", PositionID: "GLW-USDT-1",
		SignalLayer: "slow_5m", EntryLayer: "slow_5m_close", ManagementLayer: "slow_90s_20s",
	}
	real := &okxDirectionalPosition{
		Symbol: "KORU-USDT", Side: "long", EntryPrice: 680.04, Qty: 0.01, NotionalUSDT: 7.5,
		OpenedAt: now, Regime: "range", PositionID: "KORU-USDT-1", SignalLayer: "slow_5m",
	}
	e := &OKXDirectionalEngine{
		venueName:       "slowcleanuptest",
		recordDir:       tmp,
		logger:          log.New(io.Discard, "", 0),
		nowFn:           func() time.Time { return now },
		positionPath:    filepath.Join(tmp, okxDirectionalPositionFile),
		settlementsPath: filepath.Join(tmp, okxDirectionalSettlementsFile),
		positions:       map[string]*okxDirectionalPosition{"GLW-USDT": ghost, "KORU-USDT": real},
	}

	e.slowCleanupAbsentPosition(ghost)

	// 仅移除 GLW,保留 KORU(防误删真实仓)
	e.mu.RLock()
	_, glwStill := e.positions["GLW-USDT"]
	_, koruStill := e.positions["KORU-USDT"]
	e.mu.RUnlock()
	if glwStill {
		t.Fatal("清理后 e.positions 仍含 GLW-USDT")
	}
	if !koruStill {
		t.Fatal("KORU-USDT 真实仓被误删")
	}

	// 独立留痕文件恰 1 条,realized=0/fee=0/分层齐
	recs, err := readJSONLFile[okxDirectionalSettlement](filepath.Join(tmp, slowPositionCleanupFile))
	if err != nil {
		t.Fatalf("读 slow cleanup 文件失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("cleanup 记录数=%d want 1", len(recs))
	}
	r := recs[0]
	if r.Symbol != "GLW-USDT" || r.Reason != slowCleanupReason {
		t.Fatalf("记录 symbol=%q reason=%q want GLW-USDT/%s", r.Symbol, r.Reason, slowCleanupReason)
	}
	if r.RealizedUSDT != 0 || r.FeeUSDT != 0 {
		t.Fatalf("realized=%v fee=%v 都必须为 0(不臆造)", r.RealizedUSDT, r.FeeUSDT)
	}
	if r.HoldSeconds <= 0 {
		t.Fatalf("hold_seconds 应正常写, got %d", r.HoldSeconds)
	}
	if r.SignalLayer != "slow_5m" {
		t.Fatalf("signal_layer=%q want slow_5m", r.SignalLayer)
	}

	// 绝不写 settlements.jsonl
	if _, err := os.Stat(filepath.Join(tmp, okxDirectionalSettlementsFile)); !os.IsNotExist(err) {
		t.Fatalf("慢层幽灵清理不得写 settlements.jsonl(err=%v)", err)
	}
}

// Test_slowGhost_DecisionMatrix 钉死慢层复用的方向判定(与 fast 同一 positionDirectionAbsent):
// 同向仓→不清;仅反向仓→清+warn;无仓→清。short/long 对称。
func Test_slowGhost_DecisionMatrix(t *testing.T) {
	tol := executionQtyTolerance
	sym := "GLW-USDT"
	pos := func(amt float64) []venue.Position { return []venue.Position{{Symbol: venue.Symbol(sym), PositionAmt: amt}} }

	// 本地 short + 交易所同向空仓仍在 → 不清
	if absent, _ := positionDirectionAbsent(pos(-0.5), sym, "short", tol); absent {
		t.Fatal("short: 同向空仓仍在不应清理")
	}
	// 本地 short + 仅反向多仓 → 清 + oppositeOnly
	if absent, opp := positionDirectionAbsent(pos(0.5), sym, "short", tol); !absent || !opp {
		t.Fatalf("short: 仅反向多仓应清理+warn, got absent=%v opp=%v", absent, opp)
	}
	// 本地 short + 无仓 → 清
	if absent, opp := positionDirectionAbsent(nil, sym, "short", tol); !absent || opp {
		t.Fatalf("short: 无仓应清理, got absent=%v opp=%v", absent, opp)
	}
	// 镜像:本地 long + 同向多仓仍在 → 不清
	if absent, _ := positionDirectionAbsent(pos(0.5), sym, "long", tol); absent {
		t.Fatal("long: 同向多仓仍在不应清理")
	}
}
