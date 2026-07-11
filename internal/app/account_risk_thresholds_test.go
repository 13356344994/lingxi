package app

import "testing"

func TestEffectiveOpenNotionalLimitUSDT_StrictestCapWins(t *testing.T) {
	t.Run("small account uses leverage cap not loose usdt cap", func(t *testing.T) {
		// 旧语义 max(1200, 5*100)=1200 会放宽小账户;修复后应收敛到 500。
		got := effectiveOpenNotionalLimitUSDT(1200, 5, 100)
		if got != 500 {
			t.Fatalf("got %.2f want 500.00", got)
		}
	})
	t.Run("fixed cap still applies on large account", func(t *testing.T) {
		got := effectiveOpenNotionalLimitUSDT(1200, 5, 500) // 杠杆口径 2500,取更严格 1200
		if got != 1200 {
			t.Fatalf("got %.2f want 1200.00", got)
		}
	})
}

func TestEffectiveOpenNotionalLimitUSDT_LeverageHardCappedAtFive(t *testing.T) {
	// 配置给 12x 也要被硬顶到 5x。
	got := effectiveOpenNotionalLimitUSDT(0, 12, 100)
	if got != 500 {
		t.Fatalf("got %.2f want 500.00", got)
	}
}
