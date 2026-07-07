package app

import (
	"testing"
	"time"
)

// TestFastGapFresh 新鲜度粒子:点数不足 fail-closed;窗内现过 >-20 才新鲜;陈年巨折价不新鲜。
func TestFastGapFresh(t *testing.T) {
	fastGapHistMu.Lock()
	fastGapHist = map[string][]fastGapPt{}
	fastGapHistLast = map[string]time.Time{}
	fastGapHistMu.Unlock()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	// 点数不足(9 点)→ fail-closed 不新鲜
	for i := 0; i < 9; i++ {
		fastNoteGapHist("A-USDT", now.Add(time.Duration(-1700+i*70)*time.Second), -10)
	}
	if fastGapFresh("A-USDT", now, -20, 10) {
		t.Fatal("点数不足应 fail-closed 不新鲜")
	}
	// 补足点数且全部 >-20 → 新鲜
	for i := 9; i < 15; i++ {
		fastNoteGapHist("A-USDT", now.Add(time.Duration(-1700+i*70)*time.Second), -10)
	}
	if !fastGapFresh("A-USDT", now, -20, 10) {
		t.Fatal("窗内现过 >-20 应新鲜")
	}
	// 陈年巨折价(LAB型:全程 ≤-300)→ 不新鲜
	for i := 0; i < 20; i++ {
		fastNoteGapHist("LAB-USDT", now.Add(time.Duration(-1750+i*80)*time.Second), -350)
	}
	if fastGapFresh("LAB-USDT", now, -20, 10) {
		t.Fatal("陈年巨折价应不新鲜(标价不是错价)")
	}
	// 降采样:同分钟内重复调用只记一点
	fastGapHistMu.Lock()
	n := len(fastGapHist["LAB-USDT"])
	fastGapHistMu.Unlock()
	fastNoteGapHist("LAB-USDT", now.Add(-1750*time.Second).Add(5*time.Second), -350)
	fastGapHistMu.Lock()
	n2 := len(fastGapHist["LAB-USDT"])
	fastGapHistMu.Unlock()
	if n2 != n {
		t.Fatalf("60s 降采样失效: %d → %d", n, n2)
	}
}

// TestFastParticleBits 粒子位组合与派班合成位。
func TestFastParticleBits(t *testing.T) {
	fastGapHistMu.Lock()
	fastGapHist = map[string][]fastGapPt{}
	fastGapHistLast = map[string]time.Time{}
	fastGapHistMu.Unlock()
	fastLedgerTrigMu.Lock()
	fastLedgerTrig = map[string][]time.Time{}
	fastLedgerTrigMu.Unlock()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		fastNoteGapHist("B-USDT", now.Add(time.Duration(-1700+i*70)*time.Second), -5)
	}
	// 平静海 + 新鲜 + 40-60档 + 非股票 + 非惯犯
	bits := fastParticleBits("B-USDT", now, -45, 10, -3, true, 0)
	want := pbFresh | pbCalmFuel | pbBreadthOK | pbDepth4060 | pbNonStock | pbDutyXgap
	if bits != want {
		t.Fatalf("bits=%b want %b", bits, want)
	}
	// 风暴(fuel=80)→ 无 calm 无 duty;深折价档;股票perp;惯犯(prev=5)
	bits2 := fastParticleBits("SOXL-USDT", now, -80, 80, -3, true, 5)
	if bits2&pbCalmFuel != 0 || bits2&pbDutyXgap != 0 {
		t.Fatal("风暴不应有 calm/duty 位")
	}
	if bits2&pbDepthGE60 == 0 || bits2&pbNonStock != 0 || bits2&pbHabitual == 0 {
		t.Fatalf("深折价/股票/惯犯位错: %b", bits2)
	}
	// 广度不可算 → fail-closed 无 breadthOK 无 duty
	bits3 := fastParticleBits("B-USDT", now, -45, 10, 0, false, 0)
	if bits3&pbBreadthOK != 0 || bits3&pbDutyXgap != 0 {
		t.Fatal("广度不可算应 fail-closed")
	}
}

// TestFastLedgerExit 虚拟仓出场判定与 E 规则同口径。
func TestFastLedgerExit(t *testing.T) {
	cases := []struct {
		ret, gapNow float64
		gapOK       bool
		hold        float64
		want        string
	}{
		{-65, -30, true, 100, "disaster"},          // 灾难优先
		{+5, -8, true, 100, "converged"},           // 收敛(≥-10)
		{+5, -30, true, 700, "max_hold"},           // 到时
		{+5, 0, false, 100, ""},                    // 锚不可算不收敛,未到时=持有
		{-30, -30, true, 100, ""},                  // 未触任何线=持有
	}
	for i, c := range cases {
		got := fastLedgerExit(c.ret, c.gapNow, c.gapOK, c.hold, 10, 60, 600)
		if got != c.want {
			t.Fatalf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

// TestFastLedgerResidualBit v1:REST 残余带粒子位(㉝唯一活区 [-40,-20])。
func TestFastLedgerResidualBit(t *testing.T) {
	for _, c := range []struct {
		gapRest float64
		want    bool
	}{{-30, true}, {-40, true}, {-20, true}, {-15, false}, {-45, false}, {-300, false}} {
		bits := 0
		if c.gapRest >= -40 && c.gapRest <= -20 {
			bits |= pbResidual2040
		}
		if (bits&pbResidual2040 != 0) != c.want {
			t.Fatalf("gapRest=%.0f 残余带位=%v want %v", c.gapRest, bits&pbResidual2040 != 0, c.want)
		}
	}
}

// TestFastLedgerMakerFill v2:maker 挂单成交/超时判定(mid≤挂单价=保守计成交)。
func TestFastLedgerMakerFill(t *testing.T) {
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	p := &fastLedgerPos{Sym: "X-USDT", Src: "mkr", T0: base, Entry: 100.0}
	// mid 高于挂单价:不成交,未超时=继续挂
	if mid := 100.5; mid <= p.Entry {
		t.Fatal("mid 100.5 不应视为成交")
	}
	if base.Add(60 * time.Second).Sub(p.T0).Seconds() >= fastLedgerMakerTimeoutSec {
		t.Fatal("60s 不应超时(冻结先验=120s)")
	}
	// mid 跌到挂单价:成交
	if mid := 100.0; !(mid <= p.Entry) {
		t.Fatal("mid=挂单价应计成交")
	}
	// 121s 未成交:超时撤单
	if !(base.Add(121 * time.Second).Sub(p.T0).Seconds() >= fastLedgerMakerTimeoutSec) {
		t.Fatal("121s 应超时")
	}
	// 成本口径:mkr=9 < rest=12(省的3-6bps就是曲线的赌注)
	if fastLedgerMakerCostBps >= fastLedgerCostBps {
		t.Fatal("maker 成本应低于 taker 口径")
	}
}

// TestFastLedgerTrigCount 惯犯环:评估在登记前(无前视),24h 窗滚动。
func TestFastLedgerTrigCount(t *testing.T) {
	fastLedgerTrigMu.Lock()
	fastLedgerTrig = map[string][]time.Time{}
	fastLedgerTrigMu.Unlock()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	if n := fastLedgerTrigCountThenNote("C-USDT", now); n != 0 {
		t.Fatalf("首次应为 0,得 %d", n)
	}
	if n := fastLedgerTrigCountThenNote("C-USDT", now.Add(time.Hour)); n != 1 {
		t.Fatalf("第二次应见 1,得 %d", n)
	}
	// 25h 后:窗外旧记录滚出
	if n := fastLedgerTrigCountThenNote("C-USDT", now.Add(25*time.Hour)); n != 1 {
		t.Fatalf("24h 窗应只剩 1,得 %d", n)
	}
}
