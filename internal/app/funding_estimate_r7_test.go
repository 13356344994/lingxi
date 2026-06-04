package app

import "testing"

// M-S3.4(R7):资金费估计在「符号冲突」时的行为。
//   - flag=false(默认):保持既有行为,符号冲突退回 lastFundingRate(旧方向、乐观)。
//   - flag=true:符号冲突时保守估 0,不在资金费反转时刻按旧方向开仓。
func TestEstimateScanFundingSignalSignConflict(t *testing.T) {
	// lastFundingRate=+0.001(上期为正),但当前向估计 premium+interest = -0.0099(转负)→ 严格反号。
	item := PremiumIndexItem{
		LastFundingRate: "0.001",
		InterestRate:    "0.0001",
		MarkPrice:       "99",
		IndexPrice:      "100",
	}

	rateOld, _, _, _, _ := estimateScanFundingSignal(item, false)
	if !approxEqual(rateOld, 0.001) {
		t.Fatalf("flag=false 时符号冲突应退回 lastFundingRate=0.001(旧行为), got %v", rateOld)
	}

	rateSafe, _, _, _, _ := estimateScanFundingSignal(item, true)
	if rateSafe != 0 {
		t.Fatalf("flag=true 时符号冲突应保守估 0, got %v", rateSafe)
	}
}

// 非冲突场景:当前向估计与上期同号时,两种 flag 取值应一致(取保守的同号估计),不受开关影响。
func TestEstimateScanFundingSignalNoConflictUnaffected(t *testing.T) {
	// lastFundingRate=+0.002,当前向估计 +0.0101 → 同为正,取 min=0.002。
	item := PremiumIndexItem{
		LastFundingRate: "0.002",
		InterestRate:    "0.0001",
		MarkPrice:       "101",
		IndexPrice:      "100",
	}
	rateOff, _, _, _, _ := estimateScanFundingSignal(item, false)
	rateOn, _, _, _, _ := estimateScanFundingSignal(item, true)
	if !approxEqual(rateOff, rateOn) {
		t.Fatalf("非冲突场景两种开关应一致, off=%v on=%v", rateOff, rateOn)
	}
	if !approxEqual(rateOff, 0.002) {
		t.Fatalf("非冲突同号应取保守 min=0.002, got %v", rateOff)
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
