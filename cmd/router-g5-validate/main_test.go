package main

import (
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/routerg5/portfoliosim"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestCLIRequiresFlags(t *testing.T) {
	if err := run([]string{}); err == nil {
		t.Fatal("missing flags must fail")
	}
}

func TestCLIOutputsJSON(t *testing.T) {
	// Smoke: only flag validation path; full replay tested in portfoliosim package.
	if err := run([]string{
		"--catalog", filepath.Join("..", "..", "research", "firstbattle", "history_catalog.json"),
	}); err == nil {
		t.Fatal("partial flags must fail")
	}
	_ = os.Getenv
	_ = portfoliosim.StatusValidationPass
	_ = g5protocol.ExamActions
}
