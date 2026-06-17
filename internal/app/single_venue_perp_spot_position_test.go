package app

import "testing"

func TestLedgerCappedCloseQtyUsesLedgerWithMeasuredCap(t *testing.T) {
	got := ledgerCappedCloseQty(0.5, 2.0)
	if got < 0.5-executionQtyTolerance || got > 0.5+executionQtyTolerance {
		t.Fatalf("ledger-capped close = %.6f, want 0.5", got)
	}
}

func TestLedgerCappedCloseQtyZeroWhenMeasuredZero(t *testing.T) {
	if got := ledgerCappedCloseQty(0.5, 0); got > executionQtyTolerance {
		t.Fatalf("measured=0 must not blind-flatten, got %.6f", got)
	}
}

func TestLedgerCappedCloseQtyZeroWhenLedgerZero(t *testing.T) {
	if got := ledgerCappedCloseQty(0, 2.0); got > executionQtyTolerance {
		t.Fatalf("ledger=0 must return 0, got %.6f", got)
	}
}
