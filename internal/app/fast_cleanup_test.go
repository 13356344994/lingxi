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

// Test_exchangePositionAbsent_Matrix 钉死窄 allowlist:只认明确「无持仓」码/文案;
// 限频/超时/泛化拒单一律 false(防误删真实仓)。
func Test_exchangePositionAbsent_Matrix(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"okx_51169", errOf(`okx: code=1 msg="All operations failed" detail=[{"sCode":"51169","sMsg":"you don't have any positions in this direction for this contract to reduce or close."}]`), true},
		{"empty_position", errOf("empty position"), true},
		{"position_not_found", errOf("Position not found"), true},
		{"no_position", errOf("no position to close"), true},
		{"no_open_position", errOf("no open position"), true},
		{"position_is_zero", errOf("position is zero"), true},
		// 明确排除的泛化文案(收窄 allowlist)
		{"reduce_only_failed_excluded", errOf("reduce-only order failed"), false},
		{"position_side_mismatch_excluded", errOf("position side does not match"), false},
		// 非无持仓类:必须继续重试
		{"rate_limit_50011", errOf(`okx: code=50011 msg="Too Many Requests"`), false},
		{"timeout", errOf("context deadline exceeded (timeout)"), false},
		{"all_ops_failed_no_51169", errOf(`okx: code=1 msg="All operations failed"`), false},
		{"generic", errOf("some other error"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exchangePositionAbsent(c.err); got != c.want {
				t.Fatalf("exchangePositionAbsent(%q)=%v want %v", c.name, got, c.want)
			}
		})
	}
}

// Test_positionDirectionAbsent_Directionality 钉死方向判定:同向仓禁止清理;仅反向仓允许清理+oppositeOnly;无仓允许清理。
func Test_positionDirectionAbsent_Directionality(t *testing.T) {
	tol := executionQtyTolerance
	sym := "GLW-USDT"
	long := func(amt float64) []venue.Position { return []venue.Position{{Symbol: venue.Symbol(sym), PositionAmt: amt}} }

	// 本地 long
	if absent, _ := positionDirectionAbsent(long(0.5), sym, "long", tol); absent {
		t.Fatal("long: 交易所仍有同向多仓时不应判 absent")
	}
	if absent, opp := positionDirectionAbsent(long(-0.5), sym, "long", tol); !absent || !opp {
		t.Fatalf("long: 仅反向空仓应 absent=true oppositeOnly=true, got absent=%v opp=%v", absent, opp)
	}
	if absent, opp := positionDirectionAbsent(nil, sym, "long", tol); !absent || opp {
		t.Fatalf("long: 无任何仓应 absent=true oppositeOnly=false, got absent=%v opp=%v", absent, opp)
	}

	// 本地 short(镜像)
	if absent, _ := positionDirectionAbsent(long(-0.5), sym, "short", tol); absent {
		t.Fatal("short: 交易所仍有同向空仓时不应判 absent")
	}
	if absent, opp := positionDirectionAbsent(long(0.5), sym, "short", tol); !absent || !opp {
		t.Fatalf("short: 仅反向多仓应 absent=true oppositeOnly=true, got absent=%v opp=%v", absent, opp)
	}

	// 其它 symbol 不干扰 / 近零仓视为无仓
	other := []venue.Position{{Symbol: venue.Symbol("BTC-USDT"), PositionAmt: 3}}
	if absent, opp := positionDirectionAbsent(other, sym, "long", tol); !absent || opp {
		t.Fatalf("其它 symbol 不应影响本 sym 判定, got absent=%v opp=%v", absent, opp)
	}
	if absent, _ := positionDirectionAbsent(long(tol/2), sym, "long", tol); !absent {
		t.Fatal("同 symbol 近零仓(<tol)应视为无仓 absent=true")
	}
}

// Test_fastCleanupAbsentPosition_WritesCleanupAndRemoves 钉死清理留痕:写独立文件(realized=0/fee=0/分层齐),
// 移除本地跟踪,且不写 settlements.jsonl。
func Test_fastCleanupAbsentPosition_WritesCleanupAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	e := &OKXDirectionalEngine{
		venueName:       "cleanuptest",
		recordDir:       tmp,
		logger:          log.New(io.Discard, "", 0),
		nowFn:           func() time.Time { return now },
		settlementsPath: filepath.Join(tmp, okxDirectionalSettlementsFile),
	}
	p := &fastLivePosition{
		Symbol: "GLW-USDT", Side: "long", EntryPrice: 256.19, Qty: 0.02, NotionalUSDT: 7.5,
		OpenedAt: now.Add(-22 * time.Hour), PositionID: "GLW-USDT-1", Score: 0.76,
		ScoreReason: "narrow_spread|deep_book", OpenViaTaker: true, MakerEntry: true,
	}
	fastLiveMu.Lock()
	fastLivePos["cleanuptest"] = map[string]*fastLivePosition{"GLW-USDT": p}
	fastLiveMu.Unlock()
	t.Cleanup(func() {
		fastLiveMu.Lock()
		delete(fastLivePos, "cleanuptest")
		fastLiveMu.Unlock()
	})

	e.fastCleanupAbsentPosition(p)

	// 本地已移除
	fastLiveMu.Lock()
	_, still := fastLivePos["cleanuptest"]["GLW-USDT"]
	fastLiveMu.Unlock()
	if still {
		t.Fatal("清理后 fastLivePos 仍含 GLW-USDT")
	}

	// 独立留痕文件恰 1 条,realized=0/fee=0/分层齐
	recs, err := readJSONLFile[okxDirectionalSettlement](filepath.Join(tmp, fastPositionCleanupFile))
	if err != nil {
		t.Fatalf("读 cleanup 文件失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("cleanup 记录数=%d want 1", len(recs))
	}
	r := recs[0]
	if r.Reason != fastCleanupReason {
		t.Fatalf("reason=%q want %q", r.Reason, fastCleanupReason)
	}
	if r.RealizedUSDT != 0 || r.FeeUSDT != 0 {
		t.Fatalf("realized=%v fee=%v 都必须为 0(不臆造)", r.RealizedUSDT, r.FeeUSDT)
	}
	if r.HoldSeconds <= 0 {
		t.Fatalf("hold_seconds 应正常写, got %d", r.HoldSeconds)
	}
	if r.SignalLayer != fastSignalLayer || r.EntryLayer != fastEntryLayer || r.ManagementLayer != fastManagementLayer {
		t.Fatalf("分层字段应正常写, got %q/%q/%q", r.SignalLayer, r.EntryLayer, r.ManagementLayer)
	}

	// 绝不写 settlements.jsonl
	if _, err := os.Stat(filepath.Join(tmp, okxDirectionalSettlementsFile)); !os.IsNotExist(err) {
		t.Fatalf("幽灵清理不得写 settlements.jsonl(err=%v)", err)
	}
}

// Test_makerFillState_Matrix 钉死成交判定加严:仅 executed>tol 且 status filled/partial 才算成交;
// executed>tol 但 status 非 fill 类 = suspicious(不建仓);否则 not_filled。
func Test_makerFillState_Matrix(t *testing.T) {
	tol := executionQtyTolerance
	q := tol + 0.004
	cases := []struct {
		name     string
		executed float64
		status   string
		want     string
	}{
		{"filled_with_qty", q, "filled", "fill"},
		{"partially_filled_with_qty", q, "partially_filled", "fill"},
		{"partial_filled_alias", q, "partial_filled", "fill"},
		{"canceled_zero", 0, "canceled", "not_filled"},
		{"rejected_zero", 0, "rejected", "not_filled"},
		{"canceled_with_qty_suspicious", q, "canceled", "suspicious"},
		{"rejected_with_qty_suspicious", q, "rejected", "suspicious"},
		{"empty_status_with_qty_suspicious", q, "", "suspicious"},
		{"empty_status_zero", 0, "", "not_filled"},
		{"new_status_with_qty_suspicious", q, "new", "suspicious"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := makerFillState(c.executed, c.status, tol); got != c.want {
				t.Fatalf("makerFillState(%.4f,%q)=%q want %q", c.executed, c.status, got, c.want)
			}
		})
	}
}

func errOf(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }
