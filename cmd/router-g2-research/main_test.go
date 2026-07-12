package main

import (
	"testing"
	"time"

	"lingxi/internal/routerdiscovery/g2prize"
)

func TestPartitionFilesRejectMisplacedEvents(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	valid := []g2prize.Event{{ID: "ok", DecisionTime: start.Add(time.Hour)}}
	if err := validatePartitionEvents(valid, start, end, "forward"); err != nil {
		t.Fatal(err)
	}
	misplaced := []g2prize.Event{{ID: "bad", DecisionTime: end}}
	if err := validatePartitionEvents(misplaced, start, end, "forward"); err == nil {
		t.Fatal("event on/after partition end must fail closed")
	}
}
