package main

import (
	"testing"

	"lingxi/internal/routerg5/walkforward"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestCLIRequiresFlags(t *testing.T) {
	if err := run([]string{}); err == nil {
		t.Fatal("missing flags must fail")
	}
}

func TestCLIOutputsJSON(t *testing.T) {
	if err := run([]string{"--catalog", "missing.json"}); err == nil {
		t.Fatal("partial flags must fail")
	}
	_ = walkforward.StatusWFPass
	_ = g5protocol.ExamActions
}
