package main

import (
	"testing"
	"time"
)

func TestSleepUntilServerTimeResyncInterval(t *testing.T) {
	if got := sleepUntilServerTimeResyncInterval(30 * time.Second); got != sleepResyncIntervalNear {
		t.Fatalf("near interval mismatch: got %s want %s", got, sleepResyncIntervalNear)
	}
	if got := sleepUntilServerTimeResyncInterval(5 * time.Minute); got != sleepResyncIntervalMid {
		t.Fatalf("mid interval mismatch: got %s want %s", got, sleepResyncIntervalMid)
	}
	if got := sleepUntilServerTimeResyncInterval(30 * time.Minute); got != sleepResyncIntervalFar {
		t.Fatalf("far interval mismatch: got %s want %s", got, sleepResyncIntervalFar)
	}
}

func TestSleepUntilServerTimeWaitChunk(t *testing.T) {
	if got := sleepUntilServerTimeWaitChunk(20 * time.Second); got != sleepWaitChunkNear {
		t.Fatalf("near chunk mismatch: got %s want %s", got, sleepWaitChunkNear)
	}
	if got := sleepUntilServerTimeWaitChunk(8 * time.Minute); got != sleepWaitChunkMid {
		t.Fatalf("mid chunk mismatch: got %s want %s", got, sleepWaitChunkMid)
	}
	if got := sleepUntilServerTimeWaitChunk(45 * time.Minute); got != sleepWaitChunkFar {
		t.Fatalf("far chunk mismatch: got %s want %s", got, sleepWaitChunkFar)
	}
}
