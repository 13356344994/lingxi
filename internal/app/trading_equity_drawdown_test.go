package app

import "testing"

// TestUpdateTradingEquityDrawdown_WithdrawalNeutralRealLossCounts 验证交易口径回撤:
// 1) 提现使真实现金权益与全时净充提同步下降 → tradingEquity 连续 → adjusted 回撤≈0;
// 2) 真实交易亏损(无充提)仍如实计入 adjusted 回撤;
// 3) 充值不抬升交易峰值,故不会人为放大随后回撤。
func TestUpdateTradingEquityDrawdown_WithdrawalNeutralRealLossCounts(t *testing.T) {
	const eps = 1e-6

	t.Run("withdrawal does not create drawdown", func(t *testing.T) {
		st := &accountRiskEquityState{}
		// 首 tick:以历史峰值 385 播种,当前=385,无充提 → 回撤 0。
		if dd := st.updateTradingEquityDrawdown(385, 385, false); dd > eps {
			t.Fatalf("seed tick: expected 0 drawdown, got %.6f", dd)
		}
		// 提现 29:realCurrent 386→356 的同时,CumulativeFundFlow 记 -29(由上游累加)。
		st.CumulativeFundFlowUSDT = -29
		// 原始 realDrawdown=385-356=29,但交易口径应≈0(提现不是亏损)。
		dd := st.updateTradingEquityDrawdown(356, 385, false)
		if dd > eps {
			t.Fatalf("withdrawal tick: expected ~0 adjusted drawdown, got %.6f", dd)
		}
	})

	t.Run("real trading loss still counts", func(t *testing.T) {
		st := &accountRiskEquityState{}
		if dd := st.updateTradingEquityDrawdown(385, 385, false); dd > eps {
			t.Fatalf("seed tick: expected 0 drawdown, got %.6f", dd)
		}
		// 无充提、纯交易亏损 30:current 355,peak 385 → adjusted=30 如实计入。
		dd := st.updateTradingEquityDrawdown(355, 385, false)
		if diff := dd - 30; diff < -1e-3 || diff > 1e-3 {
			t.Fatalf("real loss tick: expected ~30 adjusted drawdown, got %.6f", dd)
		}
	})

	t.Run("withdrawal then real loss isolates only the loss", func(t *testing.T) {
		st := &accountRiskEquityState{}
		st.updateTradingEquityDrawdown(385, 385, false) // 播种峰值 385
		// 提现 29(中性)+ 交易再亏 10:realCurrent=385-29-10=346,CumulativeFundFlow=-29。
		st.CumulativeFundFlowUSDT = -29
		dd := st.updateTradingEquityDrawdown(346, 385, false)
		// tradingEquity=346-(-29)=375,峰值仍 385 → adjusted=10(仅交易亏损)。
		if diff := dd - 10; diff < -1e-3 || diff > 1e-3 {
			t.Fatalf("expected ~10 adjusted drawdown (loss only), got %.6f", dd)
		}
	})

	t.Run("deposit after seed does not inflate peak", func(t *testing.T) {
		st := &accountRiskEquityState{}
		st.updateTradingEquityDrawdown(385, 385, false) // 峰值 385
		// 充值 100:realCurrent 485,CumulativeFundFlow +100 → tradingEquity=385 不变,峰值不抬。
		st.CumulativeFundFlowUSDT = 100
		if dd := st.updateTradingEquityDrawdown(485, 485, false); dd > eps {
			t.Fatalf("deposit tick: expected 0 drawdown, got %.6f", dd)
		}
		// 充值后交易亏 20:realCurrent 465 → tradingEquity=365 → adjusted=20。
		dd := st.updateTradingEquityDrawdown(465, 485, false)
		if diff := dd - 20; diff < -1e-3 || diff > 1e-3 {
			t.Fatalf("post-deposit loss: expected ~20 adjusted drawdown, got %.6f", dd)
		}
	})

	t.Run("blind-gap reanchor resets peak to current", func(t *testing.T) {
		st := &accountRiskEquityState{TradingEquityPeakUSDT: 1000}
		// 重锚:峰值压回当前交易口径权益 → 回撤清零(与原始口径盲区重锚一致)。
		if dd := st.updateTradingEquityDrawdown(850, 1000, true); dd > eps {
			t.Fatalf("reanchor tick: expected 0 drawdown, got %.6f", dd)
		}
		if st.TradingEquityPeakUSDT != 850 {
			t.Fatalf("reanchor should reset peak to 850, got %.6f", st.TradingEquityPeakUSDT)
		}
	})
}
