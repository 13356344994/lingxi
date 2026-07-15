package main

import "testing"

func TestOfflineTargetHasOneBoundedWindow(t *testing.T) {
	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	if pilotEnd.Sub(pilotStart).Hours() != 168 {
		t.Fatalf("window is not exactly seven days: %v", pilotEnd.Sub(pilotStart))
	}
}

func TestConnectionStopsBeforeNetwork(t *testing.T) {
	err := run([]string{"--connect"})
	if err != nil {
		t.Fatalf("connection was not fail-closed: %v", err)
	}
}
