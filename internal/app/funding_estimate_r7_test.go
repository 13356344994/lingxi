package app

import "testing"

// M-S3.4(R7):资金费估计在「符号冲突」时的行为。
//   - ConservativeOnConflict=false(回测/默认关):保持既有行为,符号冲突退回 lastFundingRate(旧方向、乐观)。
//   - ConservativeOnConflict=true:符号冲突且反向信号可信时保守估 0,不在资金费反转时刻按旧方向开仓。
func TestEstimateScanFundingSignalSignConflict(t *testing.T) {
	// lastFundingRate=+0.001(上期为正),但当前向估计 premium+interest = -0.0099(转负)→ 严格反号,且反向信号强(可信)。
	item := PremiumIndexItem{
		LastFundingRate: "0.001",
		InterestRate:    "0.0001",
		MarkPrice:       "99",
		IndexPrice:      "100",
	}

	rateOld, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{})
	if !approxEqual(rateOld, 0.001) {
		t.Fatalf("关闭时符号冲突应退回 lastFundingRate=0.001(旧行为), got %v", rateOld)
	}

	rateSafe, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{ConservativeOnConflict: true})
	if rateSafe != 0 {
		t.Fatalf("开启时强反向冲突应保守估 0, got %v", rateSafe)
	}
}

// 非冲突场景:当前向估计与上期同号时,开关取值应一致(取保守的同号估计),不受影响。
func TestEstimateScanFundingSignalNoConflictUnaffected(t *testing.T) {
	// lastFundingRate=+0.002,当前向估计 +0.0101 → 同为正,取 min=0.002。
	item := PremiumIndexItem{
		LastFundingRate: "0.002",
		InterestRate:    "0.0001",
		MarkPrice:       "101",
		IndexPrice:      "100",
	}
	rateOff, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{})
	rateOn, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{ConservativeOnConflict: true, ReversalConfirmRatio: 0.5, ConflictingPremiumSignGuard: true})
	if !approxEqual(rateOff, rateOn) {
		t.Fatalf("非冲突场景两种开关应一致, off=%v on=%v", rateOff, rateOn)
	}
	if !approxEqual(rateOff, 0.002) {
		t.Fatalf("非冲突同号应取保守 min=0.002, got %v", rateOff)
	}
}

// R7c 同号守卫 + EDENUSDT 误杀复现/修复:上期实收强负费率(-0.585%),盘口基差为负(同向),
// 但预估结算价基差为正且绝对值更大(瞬时毛刺)。
//   - 旧逻辑(守卫关 + R7 开):毛刺翻转前瞻符号 → 符号冲突 → 归零 → 错过(bug)。
//   - 修复后(守卫开):反号 settleBasis 不得翻转前瞻符号 → 前瞻仍为负 → 估计 ≈ -0.585% → 正常纳入。
func TestEstimateScanFundingSignalEDENReproAndFix(t *testing.T) {
	// markBasis=(99-100)/100=-0.01(负,与费率同向);settleBasis=(101.5-100)/100=+0.015(正,绝对值更大,毛刺)。
	item := PremiumIndexItem{
		LastFundingRate:      "-0.00585",
		InterestRate:         "0.0001",
		MarkPrice:            "99",
		IndexPrice:           "100",
		EstimatedSettlePrice: "101.5",
	}

	// 复现 bug:R7 开、守卫关、无可信度门槛 → 毛刺翻正前瞻 → 冲突归零。
	buggy, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{ConservativeOnConflict: true})
	if buggy != 0 {
		t.Fatalf("复现路径应被误杀归零(用于回归对照), got %v", buggy)
	}

	// 修复:出厂默认(R7 开、守卫开、ratio=0.5)→ 前瞻保留负盘口基差 → 估计 ≈ 上期实收 -0.00585。
	fixed, _, _, _, _ := estimateScanFundingSignal(item, fundingSignalConfig{
		ConservativeOnConflict:      true,
		ReversalConfirmRatio:        0.5,
		ConflictingPremiumSignGuard: true,
	})
	if !approxEqual(fixed, -0.00585) {
		t.Fatalf("修复后应取保守同向估计 ≈ -0.00585, got %v", fixed)
	}
}

// R7b 可信反转门槛:微弱反向信号(毛刺)不得否决既有强势费率;真反转(强反向)仍归零。
func TestEstimateScanFundingSignalReversalConfirmRatio(t *testing.T) {
	// 上期强负 -0.006;盘口基差仅 +0.0005(弱反向毛刺),interest=0 → forward=+0.0005。
	weak := PremiumIndexItem{
		LastFundingRate: "-0.006",
		InterestRate:    "0",
		MarkPrice:       "100.05",
		IndexPrice:      "100",
	}
	// ratio=0.5:|forward|=0.0005 < |last|0.006×0.5=0.003 → 不可信 → 不归零,退回 last=-0.006。
	notZeroed, _, _, _, _ := estimateScanFundingSignal(weak, fundingSignalConfig{ConservativeOnConflict: true, ReversalConfirmRatio: 0.5})
	if !approxEqual(notZeroed, -0.006) {
		t.Fatalf("弱反向毛刺不应否决既有费率, want -0.006 got %v", notZeroed)
	}
	// ratio=0(旧 R7,无门槛):任何反向冲突都归零。
	oldZeroed, _, _, _, _ := estimateScanFundingSignal(weak, fundingSignalConfig{ConservativeOnConflict: true})
	if oldZeroed != 0 {
		t.Fatalf("无门槛旧 R7 行为应归零, got %v", oldZeroed)
	}

	// 真反转:上期 -0.006,盘口基差 +0.01(强反向)→ forward=+0.01 ≥ 0.003 → 可信 → 归零。
	strong := PremiumIndexItem{
		LastFundingRate: "-0.006",
		InterestRate:    "0",
		MarkPrice:       "101",
		IndexPrice:      "100",
	}
	zeroed, _, _, _, _ := estimateScanFundingSignal(strong, fundingSignalConfig{ConservativeOnConflict: true, ReversalConfirmRatio: 0.5})
	if zeroed != 0 {
		t.Fatalf("强反向真反转仍应保守归零, got %v", zeroed)
	}
}

func TestCredibleFundingReversal(t *testing.T) {
	cases := []struct {
		name                           string
		last, premium, interest, ratio float64
		want                           bool
	}{
		{"ratio<=0 不设门槛", -0.006, 0.0005, 0, 0, true},
		{"last=0 视为可信", 0, 0.0005, 0, 0.5, true},
		{"弱反向不可信", -0.006, 0.0005, 0, 0.5, false},
		{"强反向可信", -0.006, 0.01, 0, 0.5, true},
		{"恰好达标可信", -0.006, 0.003, 0, 0.5, true},
	}
	for _, c := range cases {
		if got := credibleFundingReversal(c.last, c.premium, c.interest, c.ratio); got != c.want {
			t.Fatalf("%s: credibleFundingReversal=%v want %v", c.name, got, c.want)
		}
	}
}

func TestFundingSignConflict(t *testing.T) {
	cases := []struct {
		last, premium, interest float64
		want                    bool
	}{
		{0.001, -0.01, 0.0001, true},   // 正 → 转负
		{-0.001, 0.01, -0.0001, true},  // 负 → 转正
		{0.001, 0.01, 0.0001, false},   // 同正
		{-0.001, -0.01, 0.0001, false}, // 同负(forward=-0.0099)
		{0, -0.01, 0.0, false},         // 上期为 0 不算冲突
		{0.001, 0, 0, false},           // forward=0 不算冲突
	}
	for i, c := range cases {
		if got := fundingSignConflict(c.last, c.premium, c.interest); got != c.want {
			t.Fatalf("case %d: fundingSignConflict(%v,%v,%v)=%v want %v", i, c.last, c.premium, c.interest, got, c.want)
		}
	}
}
