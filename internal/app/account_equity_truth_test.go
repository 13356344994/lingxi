package app

import (
	"testing"
	"time"
)

func TestAccountEquityTruthFlowFirstThenEquity(t *testing.T) {
	now := time.Now().UTC()
	var st accountRiskEquityState
	base := accountEquityObservation{TotalUSDT: 100, ReadOK: true}
	if dd, pending := st.updateEquityTruthDrawdown(base, 0, now, false); dd != 0 || pending {
		t.Fatalf("baseline dd=%v pending=%v", dd, pending)
	}
	if dd, pending := st.updateEquityTruthDrawdown(base, -20, now.Add(time.Minute), false); dd != 0 || !pending {
		t.Fatalf("flow-first 应等待权益落账: dd=%v pending=%v", dd, pending)
	}
	after := accountEquityObservation{TotalUSDT: 80, ReadOK: true}
	if dd, pending := st.updateEquityTruthDrawdown(after, 0, now.Add(2*time.Minute), false); dd != 0 || pending {
		t.Fatalf("提现配对后不应形成交易回撤: dd=%v pending=%v", dd, pending)
	}
	if st.TradingEquityCurrentUSDT != 100 || st.SolvencyEquityUSDT != 80 {
		t.Fatalf("权益口径错误 trading=%v solvency=%v", st.TradingEquityCurrentUSDT, st.SolvencyEquityUSDT)
	}
}

func TestAccountEquityTruthEquityFirstAndNormalPnL(t *testing.T) {
	now := time.Now().UTC()
	var st accountRiskEquityState
	base := accountEquityObservation{TotalUSDT: 100, ReadOK: true}
	st.updateEquityTruthDrawdown(base, 0, now, false)

	afterWithdrawal := accountEquityObservation{TotalUSDT: 70, ReadOK: true}
	if dd, pending := st.updateEquityTruthDrawdown(afterWithdrawal, 0, now.Add(time.Minute), false); dd != 30 || !pending {
		t.Fatalf("大额权益先到应暂缓确认: dd=%v pending=%v", dd, pending)
	}
	if dd, pending := st.updateEquityTruthDrawdown(afterWithdrawal, -30, now.Add(2*time.Minute), false); dd != 0 || pending {
		t.Fatalf("后到资金流应消除假回撤: dd=%v pending=%v", dd, pending)
	}

	smallLoss := accountEquityObservation{TotalUSDT: 69, ReadOK: true}
	if dd, pending := st.updateEquityTruthDrawdown(smallLoss, 0, now.Add(3*time.Minute), false); dd != 1 || pending {
		t.Fatalf("小额真实PnL应立即进入交易回撤: dd=%v pending=%v", dd, pending)
	}
}

func TestAccountEquityTruthPersistsAcrossRestart(t *testing.T) {
	now := time.Now().UTC()
	var st accountRiskEquityState
	st.updateEquityTruthDrawdown(accountEquityObservation{TotalUSDT: 100, ReadOK: true}, 0, now, false)
	st.updateEquityTruthDrawdown(accountEquityObservation{TotalUSDT: 100, ReadOK: true}, -20, now.Add(time.Minute), false)

	dir := t.TempDir()
	if err := saveAccountRiskEquityState(dir, &st, now); err != nil {
		t.Fatal(err)
	}
	restored, ok := loadAccountRiskEquityState(dir)
	if !ok || !restored.EquityTruthHasPending || len(restored.EquityTruth.PendingFlows) != 1 {
		t.Fatalf("pending 资金流未跨重启恢复: ok=%v state=%+v", ok, restored)
	}
}
