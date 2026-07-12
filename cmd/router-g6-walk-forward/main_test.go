package main

import (
	"testing"

	"lingxi/internal/routerg6/protocol"
)

func TestCLIRequiresFlags(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("missing flags must fail")
	}
}

func TestCLIExamActionsZero(t *testing.T) {
	if protocol.ExamActions != 0 {
		t.Fatalf("exam_actions=%d", protocol.ExamActions)
	}
}
