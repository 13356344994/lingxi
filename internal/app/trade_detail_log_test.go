package app

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSettlementAttributionLogFields(t *testing.T) {
	settledAt := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := SettlementAttribution{
		Symbol:           "ethusdt",
		FundingIncome:    0.42,
		SpotFees:         0.10,
		FuturesFees:      0.05,
		BorrowCosts:      0.01,
		OpenSlippage:     -0.02,
		CloseSlippage:    -0.03,
		RecoveryFriction: 0,
		NetPnL:           0.21,
	}
	got := formatSettlementAttributionLog("run-1", "cycle-7", settledAt, a, false)

	for _, want := range []string{
		"TRADE_DETAIL",
		"run=run-1",
		"cycle=cycle-7",
		"symbol=ETHUSDT", // 归一大写
		"settled_at=2026-06-05T12:00:00Z",
		"funding_pending=false",
		"funding=0.420000",
		"spot_fee=0.100000",
		"fut_fee=0.050000",
		"open_slip=-0.020000",
		"close_slip=-0.030000",
		"net_pnl=0.210000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少字段 %q\n行: %s", want, got)
		}
	}
}

func TestFormatSettlementAttributionLogPendingAndEmptyFields(t *testing.T) {
	got := formatSettlementAttributionLog("", "", time.Time{}, SettlementAttribution{}, true)
	if !strings.Contains(got, "funding_pending=true") {
		t.Errorf("应标记 funding_pending=true: %s", got)
	}
	if !strings.Contains(got, "run=- cycle=- symbol=-") {
		t.Errorf("空字段应回退为 '-': %s", got)
	}
	if !strings.Contains(got, "settled_at=-") {
		t.Errorf("零时间应回退为 '-': %s", got)
	}
}
