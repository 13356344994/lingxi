package app

import "testing"

func TestDirectionFactorCommonEligible(t *testing.T) {
	if !directionFactorCommonEligible(-200, -300, 1.5) {
		t.Fatal("冻结边界应命中")
	}
	for _, tc := range []struct {
		ret, btc, vol float64
	}{
		{-199.9, -300, 1.5},
		{-200, -299.9, 1.5},
		{-200, -300, 1.499},
	} {
		if directionFactorCommonEligible(tc.ret, tc.btc, tc.vol) {
			t.Fatalf("不应命中: %+v", tc)
		}
	}
}

func TestDirectionFactorHelpers(t *testing.T) {
	if got := normalizeDirectionFactorSymbol("abc-usdt-swap"); got != "ABC-USDT" {
		t.Fatalf("symbol=%q", got)
	}
	if got := medianPositive([]float64{0, -1, 4, 2, 3}); got != 3 {
		t.Fatalf("median=%v", got)
	}
}
