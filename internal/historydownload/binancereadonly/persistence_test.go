package binancereadonly

import (
	"os"
	"testing"

	"lingxi/internal/historydownload"
)

func TestTerminalGenerationRequiresLockAndPreventsSecondWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	current, err := PublishStop(root, 1<<20, lock, PilotReceipt{
		WindowStart: PilotStart, WindowEnd: PilotEnd,
		QuotaSideEffect: "ASYNC_ORDER_HISTORY_JOB_REQUESTED", StopStatus: "STOP_NO_ZERO_FILL",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyCurrent(root)
	if err != nil || verified.ReceiptHash != current.ReceiptHash || verified.StopStatus != "STOP_NO_ZERO_FILL" {
		t.Fatalf("terminal current invalid: current=%+v verified=%+v err=%v", current, verified, err)
	}
	secondLock, err := historydownload.AcquireRootLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLock.Close()
	if _, err := PublishStop(root, 1<<20, secondLock, PilotReceipt{WindowStart: PilotStart, WindowEnd: PilotEnd, StopStatus: "STOP_NO_ACTIVITY"}); err == nil {
		t.Fatal("second pilot window accepted after terminal current")
	}
}
